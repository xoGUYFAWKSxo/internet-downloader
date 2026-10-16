package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/outfile"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
)

// An ask is a link waiting in the Download File Info box. Its server is
// checked while the box is open; nothing is downloaded until the user
// chooses Start Download or Download Later.
type ask struct {
	made   time.Time
	media  media.Context // cookies and headers stay here, in memory
	stream bool
	info   AskInfo
}

type AskInfo = fetch.AskInfo

type asks struct {
	mu   sync.Mutex
	next uint64
	m    map[uint64]*ask
}

const (
	askFor  = time.Hour // an unanswered box is forgotten after this
	maxAsks = 20
)

// Settings and their defaults.
const (
	setAsk        = "ask"        // "1": show the Download File Info box for links from the browser
	setDone       = "done"       // "1": show a box when a download finishes
	setDir        = "dir"        // the folder last saved into
	setLimit      = "limit"      // "1": the speed limiter is on
	setLimitKB    = "limitKB"    // its cap, in KB/s
	setQueueMax   = "queueMax"   // how many queued downloads run at once
	setSched      = "sched"      // "1": the scheduler starts the queue
	setSchedStart = "schedStart" // at this time, "15:04"
	setSchedStop  = "schedStop"  // and stops it at this one, or ""
	setSchedDays  = "schedDays"  // on these days: digits, 0 for Sunday
	setUpdates    = "updates"    // "1": check once a day for a new version
	setAwake      = "awake"      // "1": keep the computer awake while downloading
	setCatchMin   = "catchMinMB" // browser downloads smaller than this many MB stay in the browser
	setCatchSites = "catchSites" // and those from these sites, one per line
	setCatchTypes = "catchTypes" // and these file types, space-separated extensions
	setClip       = "clip"       // "1": the window offers links copied to the clipboard
)

var settingDefaults = map[string]string{
	setAsk: "1", setDone: "1", setLimit: "0", setLimitKB: "1024", setQueueMax: "2",
	setSched: "0", setSchedStart: "02:00", setSchedStop: "", setSchedDays: "0123456",
	setUpdates: "1", setAwake: "1", setCatchMin: "1", setCatchSites: "", setCatchTypes: "",
	setClip: "0",
}

func (e *engine) setting(key string) string {
	def := settingDefaults[key]
	if e.jr != nil {
		return e.jr.Setting(key, def)
	}
	e.asks.mu.Lock()
	defer e.asks.mu.Unlock()
	if v, ok := e.memSettings[key]; ok {
		return v
	}
	return def
}

func (e *engine) setSetting(key, value string) error {
	return e.setSettings(map[string]string{key: value})
}

// setSettings checks every value before saving any, so the Options window
// saves all or nothing.
func (e *engine) setSettings(m map[string]string) error {
	for k, v := range m {
		if clean, ok := cleanSetting[k]; ok {
			c, err := clean(v)
			if err != nil {
				return err
			}
			m[k] = c
		} else if err := checkSetting(k, v); err != nil {
			return err
		}
	}
	for k, v := range m {
		if e.jr != nil {
			if err := e.jr.SetSetting(k, v); err != nil {
				return err
			}
			continue
		}
		e.asks.mu.Lock()
		if e.memSettings == nil {
			e.memSettings = map[string]string{}
		}
		e.memSettings[k] = v
		e.asks.mu.Unlock()
	}
	e.applyLimit()
	e.pumpQueue() // the number at once may have gone up
	return nil
}

func checkSetting(key, value string) error {
	switch key {
	case setAsk, setDone, setLimit, setSched, setUpdates, setAwake, setClip:
		if value != "0" && value != "1" {
			return errors.New("invalid setting value")
		}
	case setDir:
		if value != "" && !filepath.IsAbs(value) {
			return errors.New("Choose a folder to save into.")
		}
	case setLimitKB:
		if n, err := strconv.Atoi(value); err != nil || n < 1 || n > 1000000 {
			return errors.New("The speed limit should be a whole number of KB/s, from 1 to 1000000.")
		}
	case setCatchMin:
		if n, err := strconv.Atoi(value); err != nil || n < 1 || n > 1000000 {
			return errors.New("The smallest size to take from the browser should be a whole number of MB, from 1 to 1000000.")
		}
	case setQueueMax:
		if n, err := strconv.Atoi(value); err != nil || n < 1 || n > 10 {
			return errors.New("The queue can run from 1 to 10 downloads at once.")
		}
	case setSchedStart, setSchedStop:
		if value == "" && key == setSchedStop {
			return nil
		}
		if _, err := time.Parse("15:04", value); err != nil {
			return errors.New("Times should look like 23:30.")
		}
	case setSchedDays:
		for _, c := range value {
			if c < '0' || c > '6' {
				return errors.New("invalid days")
			}
		}
	default:
		return fmt.Errorf("unknown setting %q", key)
	}
	return nil
}

func (e *engine) settings() map[string]string {
	m := map[string]string{}
	for k := range settingDefaults {
		m[k] = e.setting(k)
	}
	m[setDir] = e.saveDir()
	return m
}

// applyLimit sets the speed limiter from the settings.
func (e *engine) applyLimit() {
	var bps int64
	if e.setting(setLimit) == "1" {
		kb, _ := strconv.Atoi(e.setting(setLimitKB))
		bps = int64(kb) << 10
	}
	if bps != e.limit.Rate() {
		e.limit.SetRate(bps)
		fmt.Fprintf(e.log, "engine: speed limit %d B/s (0: none)\n", bps)
	}
}

// saveDir is the folder the box offers: the one last used, while it is
// still there, else Downloads.
func (e *engine) saveDir() string {
	if d := e.setting(setDir); d != "" {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
	}
	return e.dir
}

// newAsk keeps c for the box and checks its server in the background.
func (e *engine) newAsk(c media.Context) uint64 {
	stream := c.Kind == media.KindHLS || c.Kind == media.KindDASH
	a := &ask{made: e.now(), media: c, stream: stream,
		info: AskInfo{Host: hostOf(c.URL), Size: -1, Stream: stream, Dir: e.saveDir()}}
	if stream {
		a.info.Name, a.info.Resume = streamName(c), "yes"
	} else {
		a.info.Name, a.info.Checking = fileNameOf(c.URL), true
	}
	a.info.Same, a.info.SameID, a.info.SameName = e.sameLink(c.URL)
	e.asks.mu.Lock()
	if e.asks.m == nil {
		e.asks.m = map[uint64]*ask{}
	}
	var oldest uint64
	for id, o := range e.asks.m {
		if e.now().Sub(o.made) > askFor {
			delete(e.asks.m, id)
		} else if oldest == 0 || id < oldest {
			oldest = id
		}
	}
	if len(e.asks.m) >= maxAsks {
		delete(e.asks.m, oldest)
	}
	e.asks.next++
	id := e.asks.next
	a.info.ID = id
	e.asks.m[id] = a
	e.asks.mu.Unlock()
	if !stream {
		go e.checkAsk(a, request(c))
	}
	return id
}

// sameLink finds the newest download of address u still in the list:
// "running" for one not finished (downloading, paused or queued), "done" for
// a finished one, "" for none. Failed and cancelled ones don't count. The
// list knows the addresses of downloads made since the app started, and of
// unfinished ones from before.
func (e *engine) sameLink(u string) (string, uint64, string) {
	e.dl.mu.Lock()
	defer e.dl.mu.Unlock()
	for i := len(e.dl.jobs) - 1; i >= 0; i-- {
		j := e.dl.jobs[i]
		if j.from != u {
			continue
		}
		switch {
		case j.State == fetch.Done:
			return "done", j.ID, j.Name
		case isRunning(j.State) || j.State == fetch.Paused:
			return "running", j.ID, j.Name
		}
	}
	return "", 0, ""
}

func (e *engine) checkAsk(a *ask, r probe.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, name, err := e.probeFile(ctx, r)
	e.asks.mu.Lock()
	defer e.asks.mu.Unlock()
	a.info.Checking = false
	if err != nil {
		fmt.Fprintf(e.log, "ask %d (%s): %v\n", a.info.ID, a.info.Host, err)
		a.info.Error = sentence(err)
		return
	}
	a.info.Name, a.info.Size, a.info.Resume = name, res.Size, "no"
	if fetch.Validator(res) != "" && res.Size > 0 && res.Plan.Mode != probe.Single {
		a.info.Resume = "yes"
	}
}

func (e *engine) askInfo(id uint64) (AskInfo, error) {
	e.asks.mu.Lock()
	defer e.asks.mu.Unlock()
	a, ok := e.asks.m[id]
	if !ok {
		return AskInfo{}, errors.New("That link is no longer waiting; send it from the browser again.")
	}
	return a.info, nil
}

func (e *engine) dropAsk(id uint64) {
	e.asks.mu.Lock()
	delete(e.asks.m, id)
	e.asks.mu.Unlock()
}

// answerAsk downloads an ask's link into dir under name, or with later adds
// it to the queue, to start with Resume or the queue. A sum, if given, is the
// SHA-256 the finished file must have.
func (e *engine) answerAsk(id uint64, dir, name string, later bool, sum string) (uint64, error) {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if !filepath.IsAbs(dir) {
		return 0, errors.New("Choose a folder to save into.")
	}
	if strings.TrimSpace(name) == "" {
		return 0, errors.New("Type a name for the file.")
	}
	name = outfile.CleanName(name)
	sum, err := cleanSHA256(sum)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("Couldn't use that folder: %s.", strings.TrimRight(err.Error(), ". "))
	}
	e.asks.mu.Lock()
	a, ok := e.asks.m[id]
	delete(e.asks.m, id)
	e.asks.mu.Unlock()
	if !ok {
		return 0, errors.New("That link is no longer waiting; send it from the browser again.")
	}
	if err := e.setSetting(setDir, dir); err != nil {
		fmt.Fprintf(e.log, "couldn't save the folder: %v\n", err)
	}
	set := func(j *job) { j.dir, j.name, j.Name, j.SHA256 = dir, name, name, sum }
	r := request(a.media)
	req, run := &r, e.runFile
	if a.stream {
		c, target := a.media, set
		req, run = nil, e.runStreamJob
		set = func(j *job) { target(j); j.referrer, j.stream = c.Referer, &streamState{Media: c} }
	}
	if !later {
		return e.startJob(r.URL, req, set, run), nil
	}
	return e.queueLater(r.URL, req, func(j *job) { set(j); j.Size = a.info.Size }), nil
}

// queueLater adds a download to the list, paused and at the end of the
// queue, without starting it.
func (e *engine) queueLater(u string, req *probe.Request, set func(*job)) uint64 {
	j := e.newJob(u, req, func(j *job) {
		set(j)
		j.State, j.Resumable = fetch.Paused, true
		j.Error = "Not started yet. Resume, or the queue, starts it."
		j.Queue = e.lastInQueue() + 1
	})
	fmt.Fprintf(e.log, "download %d (%s): added to the queue\n", j.ID, j.host)
	e.pumpQueue()
	return j.ID
}

// cleanSHA256 tidies a SHA-256 typed or pasted in: spaces and a "sha256:"
// label go, and letters are made lower case. "" stays "".
func cleanSHA256(s string) (string, error) {
	s = strings.ToLower(strings.Join(strings.Fields(s), ""))
	for _, p := range []string{"sha256:", "sha-256:", "sha256="} {
		s = strings.TrimPrefix(s, p)
	}
	if s == "" {
		return "", nil
	}
	if len(s) != 64 || strings.Trim(s, "0123456789abcdef") != "" {
		return "", errors.New("That isn't a SHA-256: it should be 64 characters, the digits 0-9 and letters a-f.")
	}
	return s, nil
}

func request(c media.Context) probe.Request {
	return probe.Request{URL: c.URL, Referer: c.Referer, Headers: c.Headers, Cookies: c.Cookies}
}

// fileNameOf guesses a file name from an address until the server says.
func fileNameOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if b := filepath.Base(u.Path); b != "." && b != "/" && b != `\` {
		if n, err := url.PathUnescape(b); err == nil {
			return outfile.CleanName(n)
		}
	}
	return u.Host
}
