package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/journal"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/stream"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/version"
)

// fromExtension is what nmhost forwards: the calling browser plus the
// extension's own message.
type fromExtension struct {
	Browser   string `json:"browser"`
	Extension string `json:"extension"`
	Message   struct {
		Type  string        `json:"type"`
		Media media.Context `json:"media"`
	} `json:"message"`
}

type engine struct {
	inbox *media.Inbox
	log   io.Writer
	now   func() time.Time
	probe probe.Options // tests point this at their own certificates
	dir   string        // where downloads are saved
	dl    downloads
	// ffmpeg is the FFmpeg program, or "" when there is none.
	ffmpeg string
	// finished, if set, is called when a download stops for any reason.
	finished func(id uint64)
	// jr saves the download list and progress; nil keeps them in memory.
	jr *journal.Journal
	// retries are the pauses before trying a dropped download again.
	retries []time.Duration
	// checkpointEvery is how often a download's progress is committed.
	checkpointEvery time.Duration
	// show, if set, opens a window of ui.exe with these arguments.
	show func(args ...string) error
	// asks are links waiting in the Download File Info box.
	asks asks
	// memSettings holds settings when there is no saved list.
	memSettings map[string]string
	// limit is the speed limiter all downloads share.
	limit *fetch.Limiter
	// q is the queue's state; e.dl.mu guards it.
	q queueState
	// version is this app's; upd is what the update check found.
	version string
	upd     updates
}

func newEngine(log io.Writer) *engine {
	return &engine{inbox: media.NewInbox(100), log: log, now: time.Now, dir: downloadDir(), ffmpeg: stream.FFmpeg(),
		retries: []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second}, checkpointEvery: 3 * time.Second, limit: fetch.NewLimiter(),
		version: version.Version, upd: updates{url: latestRelease, client: &http.Client{}}}
}

type idRequest struct {
	ID uint64 `json:"id"`
}

func payload(v any) json.RawMessage {
	p, _ := json.Marshal(v)
	return p
}

func pong() ipc.Message {
	// features lets a newer extension check before using something an older
	// app doesn't have: "file" is the "file" message for any download.
	return ipc.Message{Type: "pong", Payload: payload(map[string]any{"version": version.Version, "features": []string{"file"}})}
}

func fail(msg string) ipc.Message { return ipc.Message{Type: "error", Error: msg} }

func (e *engine) handle(m ipc.Message) ipc.Message {
	switch m.Type {
	case "ping":
		return pong()
	case "media.list":
		return ipc.Message{Type: "media.list", Payload: payload(e.inbox.Summaries())}
	case "media.probe":
		var in idRequest
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		return e.probeMedia(in.ID)
	case "media.download":
		var in idRequest
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		id, err := e.startDownload(in.ID)
		if err != nil {
			return fail(err.Error())
		}
		return ipc.Message{Type: "media.download", Payload: payload(idRequest{id})}
	case "downloads.list":
		return ipc.Message{Type: "downloads.list", Payload: payload(e.dl.list())}
	case "downloads.detail":
		var in idRequest
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		d, err := e.detail(in.ID)
		if err != nil {
			return fail(err.Error())
		}
		return ipc.Message{Type: "downloads.detail", Payload: payload(d)}
	case "downloads.cancel":
		var in idRequest
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		if err := e.cancelDownload(in.ID); err != nil {
			return fail(err.Error())
		}
		return ipc.Message{Type: "downloads.cancel"}
	case "downloads.resume":
		var in idRequest
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		if err := e.resumeDownload(in.ID); err != nil {
			return fail(err.Error())
		}
		return ipc.Message{Type: "downloads.resume"}
	case "downloads.pause":
		var in idRequest
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		if err := e.pauseDownload(in.ID); err != nil {
			return fail(err.Error())
		}
		return ipc.Message{Type: "downloads.pause"}
	case "downloads.remove":
		var in struct {
			IDs         []uint64 `json:"ids"`
			DeleteFiles bool     `json:"deleteFiles"`
		}
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		if err := e.removeDownloads(in.IDs, in.DeleteFiles); err != nil {
			return fail(err.Error())
		}
		return ipc.Message{Type: "downloads.remove"}
	case "downloads.add":
		var in struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		id, err := e.addURL(in.URL)
		if err != nil {
			return fail(err.Error())
		}
		return ipc.Message{Type: "downloads.add", Payload: payload(idRequest{id})}
	case "asks.add":
		var in struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		u, err := webAddress(in.URL)
		if err != nil {
			return fail(err.Error())
		}
		return ipc.Message{Type: "asks.add", Payload: payload(idRequest{e.newAsk(media.Context{URL: u, Kind: streamKind(u)})})}
	case "asks.get":
		var in idRequest
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		a, err := e.askInfo(in.ID)
		if err != nil {
			return fail(err.Error())
		}
		return ipc.Message{Type: "asks.get", Payload: payload(a)}
	case "asks.start":
		var in struct {
			ID    uint64 `json:"id"`
			Dir   string `json:"dir"`
			Name  string `json:"name"`
			Later bool   `json:"later"`
		}
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		id, err := e.answerAsk(in.ID, in.Dir, in.Name, in.Later)
		if err != nil {
			return fail(err.Error())
		}
		return ipc.Message{Type: "asks.start", Payload: payload(idRequest{id})}
	case "asks.cancel":
		var in idRequest
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		e.dropAsk(in.ID)
		return ipc.Message{Type: "asks.cancel"}
	case "settings.save":
		var in map[string]string
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		if err := e.setSettings(in); err != nil {
			return fail(err.Error())
		}
		return ipc.Message{Type: "settings.save"}
	case "queue.get":
		return ipc.Message{Type: "queue.get", Payload: payload(e.queueInfo())}
	case "queue.start":
		e.startQueue("the user")
		return ipc.Message{Type: "queue.start"}
	case "queue.stop":
		e.stopQueue("the user")
		return ipc.Message{Type: "queue.stop"}
	case "queue.add", "queue.remove":
		var in struct {
			IDs []uint64 `json:"ids"`
		}
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		if m.Type == "queue.remove" {
			e.queueRemove(in.IDs)
		} else if err := e.queueAdd(in.IDs); err != nil {
			return fail(err.Error())
		}
		return ipc.Message{Type: m.Type}
	case "queue.move":
		var in struct {
			ID uint64 `json:"id"`
			By int    `json:"by"`
		}
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		if err := e.queueMove(in.ID, in.By); err != nil {
			return fail(err.Error())
		}
		return ipc.Message{Type: "queue.move"}
	case "update.get":
		// version is "" when this is the latest, or the check is off or failed.
		return ipc.Message{Type: "update.get", Payload: payload(map[string]string{"version": e.newerVersion(), "page": releasesPage})}
	case "settings.get":
		return ipc.Message{Type: "settings.get", Payload: payload(e.settings())}
	case "settings.set":
		var in struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		if err := e.setSetting(in.Key, in.Value); err != nil {
			return fail(err.Error())
		}
		return ipc.Message{Type: "settings.set"}
	case "extension":
		var in fromExtension
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid message from the browser: " + err.Error())
		}
		switch in.Message.Type {
		case "ping":
			return pong()
		case "media":
			return e.addMedia(in.Browser, in.Message.Media)
		case "download":
			// From the right-click menu: add the item, then ask where to
			// save it, or start it at once.
			return e.downloadNow(in.Browser, in.Message.Media)
		case "file":
			// A download the browser started, or a link from the right-click
			// menu. The extension keeps the browser's download paused until
			// this answers, and lets the browser carry on after an error.
			if err := e.checkFile(in.Message.Media); errors.Is(err, errTooSmall) {
				// Not an error: the browser just keeps it, without a notice.
				return ipc.Message{Type: "file.declined", Payload: payload(map[string]any{"declined": true})}
			} else if err != nil {
				return fail(sentence(err))
			}
			return e.downloadNow(in.Browser, in.Message.Media)
		}
		return fail("unknown message from the browser: " + in.Message.Type)
	default:
		return fail("unknown request type: " + m.Type)
	}
}

// downloadNow adds an item from the browser, then shows the Download File
// Info box or starts it.
func (e *engine) downloadNow(browser string, c media.Context) ipc.Message {
	added := e.addMedia(browser, c)
	if added.Type != "media.added" {
		return added
	}
	var item idRequest
	json.Unmarshal(added.Payload, &item)
	if e.show != nil && e.setting(setAsk) == "1" {
		id := e.newAsk(c)
		err := e.show(fmt.Sprintf("--ask=%d", id))
		if err == nil {
			return ipc.Message{Type: "downloads.ask", Payload: payload(map[string]any{"asked": true})}
		}
		e.dropAsk(id)
		fmt.Fprintf(e.log, "couldn't show the Download File Info box: %v\n", err)
	}
	id, err := e.startDownload(item.ID)
	if err != nil {
		return fail(sentence(err))
	}
	return ipc.Message{Type: "media.download", Payload: payload(idRequest{id})}
}

// checkFile asks the server for a file the browser was downloading, with the
// browser's cookies, before the browser lets go of it. Some downloads can't
// be fetched again (a form sent with POST, a one-time link); the server then
// usually answers with an error or a web page, and the browser keeps those.
func (e *engine) checkFile(c media.Context) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Kind != media.KindFile {
		return errors.New("unknown file type " + strconv.Quote(c.Kind))
	}
	// minSize marks a download the browser started; the user's rules apply
	// to those, not to links sent from the right-click menu.
	caught := c.MinSize > 0
	if caught {
		if why := e.leftToBrowser(c.URL, c.PageURL, c.Referer); why != "" {
			fmt.Fprintf(e.log, "file from the browser, %s: left to the browser (%s)\n", hostOf(c.URL), why)
			return errTooSmall
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := probe.Probe(ctx, probe.Request{URL: c.URL, Referer: c.Referer, Headers: c.Headers, Cookies: c.Cookies}, e.probe)
	if err != nil {
		fmt.Fprintf(e.log, "file from the browser, %s: %v\n", hostOf(c.URL), err)
		return err
	}
	if caught {
		if res.Size < e.catchMin(c.MinSize) { // also when the size is unknown (-1)
			return errTooSmall
		}
		if why := e.typeLeft(res.FileName); why != "" {
			fmt.Fprintf(e.log, "file from the browser, %s: left to the browser (%s)\n", hostOf(c.URL), why)
			return errTooSmall
		}
	}
	if c.Size > 0 && res.Size >= 0 && res.Size != c.Size {
		return fmt.Errorf("the server sent the app a different file (%d bytes, the browser's was %d)", res.Size, c.Size)
	}
	if c.ContentType != "" && isPage(res.ContentType) && !isPage(c.ContentType) {
		return errors.New("the server sent the app a web page instead of the file")
	}
	return nil
}

// errTooSmall is a download the browser keeps: smaller than the smallest
// size taken, of unknown size, or from a site or of a type the user leaves
// to the browser.
var errTooSmall = errors.New("the browser keeps this download")

func isPage(contentType string) bool {
	t := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	return t == "text/html" || t == "application/xhtml+xml"
}

func (e *engine) addMedia(browser string, c media.Context) ipc.Message {
	if err := c.Validate(); err != nil {
		return fail(sentence(err))
	}
	id := e.inbox.Add(browser, c, e.now())
	// Host only: query strings often carry access tokens.
	host := hostOf(c.URL)
	fmt.Fprintf(e.log, "received %s stream from %s: %s (%d cookies, %d-byte manifest)\n",
		c.Kind, browser, host, len(c.Cookies), len(c.Manifest))
	return ipc.Message{Type: "media.added", Payload: payload(map[string]any{"id": id})}
}

// probeMedia checks the server of a received stream with the browser's
// cookies and headers. For HLS and DASH this is the playlist's server; the
// fragments are checked when Phase 4 fetches them.
func (e *engine) probeMedia(id uint64) ipc.Message {
	it, ok := e.inbox.Get(id)
	if !ok {
		return fail("That item is no longer in the list.")
	}
	c := it.Context
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := probe.Probe(ctx, probe.Request{URL: c.URL, Referer: c.Referer, Headers: c.Headers, Cookies: c.Cookies}, e.probe)
	host := hostOf(c.URL)
	if err != nil {
		fmt.Fprintf(e.log, "probe %s: %v\n", host, err)
		return fail(sentence(err))
	}
	fmt.Fprintf(e.log, "probe %s: %s, ranges %v, size %d, plan %s\n", host, res.Protocol, res.Ranges, res.Size, res.Plan.Mode)
	// The final address can carry a signed token; the window doesn't need it.
	res.URL = ""
	return ipc.Message{Type: "media.probe", Payload: payload(res)}
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return ""
}

func capitalise(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

// sentence turns an error into a sentence for the window: capitalised, with
// one full stop. Some Windows messages already end with one.
func sentence(err error) string {
	return capitalise(strings.TrimRight(err.Error(), ". ")) + "."
}
