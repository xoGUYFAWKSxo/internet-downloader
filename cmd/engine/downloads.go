package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/journal"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/outfile"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
)

// errShutdown is why downloads stop when the engine quits, and errPaused
// why one stops when the user presses Stop. Both keep the unfinished file
// when it can be resumed; a plain cancel throws it away.
var (
	errShutdown = errors.New("the app was closed")
	errPaused   = errors.New("paused")
)

// keepDownloads is how many downloads the saved list holds.
const keepDownloads = 200

type job struct {
	fetch.Status
	host string // for the log; never the full address, which can hold tokens
	// from and referrer are where the file came from, for its mark of the web.
	from, referrer string
	// progress, while set, gives the bytes and segments written so far.
	progress func() (int64, int)
	cancel   context.CancelCauseFunc
	// While a file downloads: its file and streams, for the progress window.
	out   *outfile.File
	watch *fetch.Watch
	// resume is "yes" or "no" once the server has said whether the file
	// can be resumed.
	resume string

	// To resume a file: its request (nil for streams, which can't resume),
	// the unfinished file, the server's size and validator it came from,
	// and the spans known to be on disk.
	req       *probe.Request
	part      string
	size      int64
	validator string
	have      []outfile.Range
	// dir and name are where the user chose to save the file; "" for
	// Downloads and the server's name. Set before the job first runs.
	dir, name string
}

type downloads struct {
	mu   sync.Mutex
	next uint64
	jobs []*job
}

func (d *downloads) list() []fetch.Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]fetch.Status, 0, len(d.jobs))
	for i := len(d.jobs) - 1; i >= 0; i-- { // newest first
		j := d.jobs[i]
		v := j.Status
		if j.progress != nil && v.State == fetch.Downloading {
			v.Done, v.PartsDone = j.progress()
		}
		out = append(out, v)
	}
	return out
}

func (d *downloads) update(j *job, f func(*job)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f(j)
}

func (d *downloads) find(id uint64) *job {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.findLocked(id)
}

// findLocked is find for a caller holding d.mu.
func (d *downloads) findLocked(id uint64) *job {
	for _, j := range d.jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

// downloadDir is the user's Downloads folder.
func downloadDir() string {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, 0); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads")
}

// detail gives what the progress window shows of a download.
func (e *engine) detail(id uint64) (fetch.Detail, error) {
	j := e.dl.find(id)
	if j == nil {
		return fetch.Detail{}, errors.New("That download is no longer in the list.")
	}
	e.dl.mu.Lock()
	d := fetch.Detail{Status: j.Status, Host: j.host, Resume: j.resume, Have: []fetch.Span{}}
	out, watch, have := j.out, j.watch, j.have
	if j.progress != nil && d.Status.State == fetch.Downloading {
		d.Status.Done, d.Status.PartsDone = j.progress()
	}
	switch {
	case j.req == nil:
		d.Resume = "no" // streams start over
	case d.Resume == "" && j.Resumable:
		d.Resume = "yes"
	}
	e.dl.mu.Unlock()
	switch {
	case d.Status.State == fetch.Done:
		if d.Status.Size > 0 {
			have = []outfile.Range{{Start: 0, End: d.Status.Size}}
		}
	case out != nil:
		have = out.Have()
	}
	for _, r := range have {
		d.Have = append(d.Have, fetch.Span{Start: r.Start, End: r.End})
	}
	if watch != nil {
		d.Conns = watch.Connections()
	}
	if d.Conns == nil {
		d.Conns = []fetch.Conn{}
	}
	return d, nil
}

// startDownload downloads a received item in the background.
func (e *engine) startDownload(id uint64) (uint64, error) {
	it, ok := e.inbox.Get(id)
	if !ok {
		return 0, errors.New("That item is no longer in the list.")
	}
	c := it.Context
	if c.Kind == media.KindHLS || c.Kind == media.KindDASH {
		return e.downloadStream(c), nil
	}
	return e.download(probe.Request{URL: c.URL, Referer: c.Referer, Headers: c.Headers, Cookies: c.Cookies}), nil
}

// start adds a job for address u and runs it in the background; it returns
// the job's id at once. req is kept to resume the job, or nil.
func (e *engine) start(u string, req *probe.Request, run func(context.Context, *job)) uint64 {
	return e.startJob(u, req, nil, run)
}

// startJob is start with set, if not nil, changing the job first.
func (e *engine) startJob(u string, req *probe.Request, set func(*job), run func(context.Context, *job)) uint64 {
	j := e.newJob(u, req, set)
	e.launch(j, run)
	return j.ID
}

// newJob adds a job to the list and the saved list without running it.
func (e *engine) newJob(u string, req *probe.Request, set func(*job)) *job {
	e.dl.mu.Lock()
	e.dl.next++
	j := &job{Status: fetch.Status{ID: e.dl.next, Name: hostOf(u), Size: -1, State: fetch.Checking, Started: e.now().Format(time.RFC3339)}, host: hostOf(u), from: u, req: req}
	if req != nil {
		j.referrer = req.Referer
	}
	if set != nil {
		set(j)
	}
	e.dl.jobs = append(e.dl.jobs, j)
	s := j.Status
	e.dl.mu.Unlock()
	if e.jr != nil {
		err := e.jr.Add(s, req)
		if err == nil && (j.dir != "" || j.name != "") {
			err = e.jr.SetTarget(j.ID, j.dir, j.name)
		}
		if err != nil {
			fmt.Fprintf(e.log, "download %d (%s): couldn't save it to the list: %v\n", j.ID, j.host, err)
		}
	}
	return j
}

func (e *engine) launch(j *job, run func(context.Context, *job)) {
	ctx, cancel := context.WithCancelCause(context.Background())
	e.dl.update(j, func(j *job) { j.cancel = cancel })
	go func() {
		defer cancel(nil)
		run(ctx, j)
		e.ranOut(j)
		if e.finished != nil {
			e.finished(j.ID)
		}
	}()
}

// download starts a plain file download.
func (e *engine) download(r probe.Request) uint64 {
	return e.start(r.URL, &r, e.runFile)
}

// runFile downloads a file. A dropped connection is retried after a pause,
// keeping the bytes already in.
func (e *engine) runFile(ctx context.Context, j *job) {
	for try := 0; ; try++ {
		path, size, err := e.getResumable(ctx, j)
		if err == nil {
			e.done(j, path, size)
			return
		}
		if !fetch.Dropped(err) || try >= len(e.retries) || ctx.Err() != nil {
			e.stopped(ctx, j, err)
			return
		}
		wait := e.retries[try]
		fmt.Fprintf(e.log, "download %d (%s): %v; trying again in %s\n", j.ID, j.host, err, wait)
		e.dl.update(j, func(j *job) {
			j.State, j.Streams = fetch.Waiting, 0
			j.Error = fmt.Sprintf("The connection dropped. Trying again in %d seconds.", int(wait.Seconds()))
			if j.progress != nil {
				j.Done, j.PartsDone = j.progress()
			}
			j.progress = nil
		})
		e.save(j)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			e.stopped(ctx, j, ctx.Err())
			return
		}
		e.dl.update(j, func(j *job) { j.State, j.Error = fetch.Checking, "" })
	}
}

// getResumable probes j's file and downloads it, carrying on from j's
// unfinished file when the server still has the same file.
func (e *engine) getResumable(ctx context.Context, j *job) (string, int64, error) {
	res, name, err := e.probeFile(ctx, *j.req)
	if err != nil {
		return "", 0, err
	}
	v := fetch.Validator(res)
	resumable := v != "" && res.Size > 0 && res.Plan.Mode != probe.Single
	var out *outfile.File
	if j.part != "" {
		if resumable && j.size == res.Size && j.validator == v {
			if out, err = outfile.Open(j.part, res.Size, j.have); err != nil {
				fmt.Fprintf(e.log, "download %d (%s): starting over: %v\n", j.ID, j.host, err)
			}
		} else {
			fmt.Fprintf(e.log, "download %d (%s): the server's file changed; starting over\n", j.ID, j.host)
		}
		if out == nil {
			os.Remove(j.part)
		}
	}
	if out == nil {
		dir := e.saveDir()
		if j.dir != "" {
			dir = j.dir
		}
		if j.name != "" {
			name = j.name
		}
		if out, err = outfile.Create(dir, name, res.Size); err != nil {
			return "", 0, err
		}
		if !resumable {
			v = ""
		}
		if e.jr != nil {
			if err := e.jr.SetFile(j.ID, out.Path(), res.Size, v); err != nil {
				fmt.Fprintf(e.log, "download %d (%s): couldn't save its file: %v\n", j.ID, j.host, err)
			}
		}
	}
	watch := &fetch.Watch{}
	e.dl.update(j, func(j *job) {
		j.part, j.size, j.validator, j.have = out.Path(), res.Size, v, nil
		j.Name, j.Size, j.State, j.Error, j.Resumable = out.Name(), res.Size, fetch.Downloading, "", false
		j.Protocol, j.Mode, j.Streams = res.Protocol, res.Plan.Mode, res.Plan.Streams
		j.progress = func() (int64, int) { return out.Written(), 0 }
		j.out, j.watch, j.resume = out, watch, "no"
		if resumable {
			j.resume = "yes"
		}
	})
	defer e.dl.update(j, func(j *job) { j.out, j.watch = nil, nil })
	e.save(j)
	fmt.Fprintf(e.log, "download %d (%s): %s, %s, %d streams, %d bytes, %d already in\n", j.ID, j.host, res.Protocol, res.Plan.Mode, res.Plan.Streams, res.Size, out.Written())

	stop := e.checkpoints(j, out)
	err = fetch.Download(ctx, *j.req, res, out, fetch.Options{RootCAs: e.probe.RootCAs, OnLimit: e.onLimit(j), Watch: watch, Limit: e.limit})
	stop()
	if err == nil {
		var path string
		if path, err = out.Finish(); err == nil {
			return path, out.Written(), nil
		}
	}
	// Keep what is in unless it can't be used again.
	if !resumable || errors.Is(err, fetch.ErrChanged) || cancelledByUser(ctx) {
		out.Abort()
		e.dl.update(j, func(j *job) { j.part, j.have = "", nil })
		return "", 0, err
	}
	e.commit(j, out)
	out.Close()
	have := out.Have()
	e.dl.update(j, func(j *job) { j.have = have })
	return "", 0, err
}

// checkpoints commits j's progress every few seconds until stop is called.
func (e *engine) checkpoints(j *job, out *outfile.File) (stop func()) {
	if e.jr == nil {
		return func() {}
	}
	quit, ended := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(ended)
		t := time.NewTicker(e.checkpointEvery)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-t.C:
				e.commit(j, out)
			}
		}
	}()
	return func() { close(quit); <-ended }
}

// commit flushes out to disk, then records the spans that were written
// before the flush: only bytes really on the disk are committed.
func (e *engine) commit(j *job, out *outfile.File) {
	if e.jr == nil {
		return
	}
	have := out.Have()
	err := out.Sync()
	if err == nil {
		err = e.jr.Commit(j.ID, have)
	}
	if err != nil {
		fmt.Fprintf(e.log, "download %d (%s): couldn't save progress: %v\n", j.ID, j.host, err)
	}
}

// cancelledByUser is true when the user cancelled the download, which
// throws away its unfinished file.
func cancelledByUser(ctx context.Context) bool {
	return ctx.Err() != nil && errors.Is(context.Cause(ctx), context.Canceled)
}

// stopped records why j stopped, and whether it can be resumed.
func (e *engine) stopped(ctx context.Context, j *job, err error) {
	byUser := cancelledByUser(ctx)
	shutdown := errors.Is(context.Cause(ctx), errShutdown)
	paused := errors.Is(context.Cause(ctx), errPaused)
	e.dl.mu.Lock()
	canResume := e.jr != nil && j.req != nil && j.part != "" && j.validator != "" && !byUser
	if !canResume && j.part != "" {
		os.Remove(j.part)
		j.part, j.have = "", nil
	}
	if !canResume {
		j.Queue = 0
	}
	state := fetch.Failed
	switch {
	case byUser:
		state, err = fetch.Cancelled, errors.New("cancelled")
	case paused && canResume:
		state, err = fetch.Paused, nil
	case paused:
		state, err = fetch.Cancelled, errors.New("stopped; this download can't be resumed, so its unfinished file was deleted")
	case shutdown && canResume:
		state, err = fetch.Paused, errors.New("paused when the app was closed")
	case shutdown:
		err = errors.New("stopped when the app was closed; send it from the browser again")
	}
	j.State, j.Error, j.Resumable, j.Streams = state, "", canResume, 0
	if err != nil {
		j.Error = sentence(err)
	}
	if j.progress != nil {
		j.Done, j.PartsDone = j.progress()
	}
	j.progress = nil
	e.dl.mu.Unlock()
	fmt.Fprintf(e.log, "download %d (%s): %s: %v\n", j.ID, j.host, state, err)
	if canResume {
		e.save(j)
	} else {
		e.end(j)
	}
}

// done records where j's file went.
func (e *engine) done(j *job, path string, size int64) {
	fmt.Fprintf(e.log, "download %d (%s): done, %d bytes\n", j.ID, j.host, size)
	e.dl.update(j, func(j *job) {
		j.State, j.Path, j.Name, j.Done, j.Error = fetch.Done, path, filepath.Base(path), size, ""
		j.Size, j.PartsDone, j.progress = size, j.Parts, nil
		j.part, j.have, j.Queue = "", nil, 0
	})
	if err := outfile.MarkFromInternet(path, j.from, j.referrer); err != nil {
		// The file is complete; it just lacks the tag (FAT32 and exFAT have no place for it).
		fmt.Fprintf(e.log, "download %d (%s): couldn't mark the file as from the internet: %v\n", j.ID, j.host, err)
	}
	e.end(j)
	if e.show != nil && e.setting(setDone) == "1" {
		if err := e.show(fmt.Sprintf("--progress=%d", j.ID)); err != nil {
			fmt.Fprintf(e.log, "download %d: couldn't show that it finished: %v\n", j.ID, err)
		}
	}
}

// save writes j's status to the saved list.
func (e *engine) save(j *job) {
	if e.jr == nil {
		return
	}
	e.dl.mu.Lock()
	s := j.Status
	e.dl.mu.Unlock()
	if err := e.jr.SetStatus(s); err != nil {
		fmt.Fprintf(e.log, "download %d (%s): couldn't save it to the list: %v\n", j.ID, j.host, err)
	}
}

// end saves j's final status and forgets how to resume it.
func (e *engine) end(j *job) {
	if e.jr == nil {
		return
	}
	e.dl.mu.Lock()
	s := j.Status
	e.dl.mu.Unlock()
	if err := e.jr.End(s); err != nil {
		fmt.Fprintf(e.log, "download %d (%s): couldn't save it to the list: %v\n", j.ID, j.host, err)
	}
}

// probeFile probes r and picks the file name.
func (e *engine) probeFile(ctx context.Context, r probe.Request) (*probe.Result, string, error) {
	pctx, pcancel := context.WithTimeout(ctx, 30*time.Second)
	res, err := probe.Probe(pctx, r, e.probe)
	pcancel()
	if err != nil {
		return nil, "", err
	}
	name := res.FileName
	if name == "" {
		name = hostOf(r.URL)
	}
	return res, name, nil
}

// getFile probes r and downloads it into dir under the server's file name,
// for one track of a stream. started is called once the file exists. A
// failed download deletes its .part file.
func (e *engine) getFile(ctx context.Context, j *job, r probe.Request, dir string, started func(*probe.Result, *outfile.File)) (string, int64, error) {
	res, name, err := e.probeFile(ctx, r)
	if err != nil {
		return "", 0, err
	}
	out, err := outfile.Create(dir, name, res.Size)
	if err != nil {
		return "", 0, err
	}
	started(res, out)
	fmt.Fprintf(e.log, "download %d (%s): %s, %s, %d streams, %d bytes\n", j.ID, hostOf(r.URL), res.Protocol, res.Plan.Mode, res.Plan.Streams, res.Size)
	err = fetch.Download(ctx, r, res, out, fetch.Options{RootCAs: e.probe.RootCAs, OnLimit: e.onLimit(j), Limit: e.limit})
	var path string
	if err == nil {
		path, err = out.Finish()
	}
	if err != nil {
		out.Abort()
		return "", 0, err
	}
	return path, out.Written(), nil
}

func (e *engine) onLimit(j *job) func(int) {
	return func(n int) {
		fmt.Fprintf(e.log, "download %d (%s): the server is busy; now %d parallel requests\n", j.ID, j.host, n)
		e.dl.update(j, func(j *job) { j.Streams = n })
	}
}

// cancelDownload stops a running download, or throws away the unfinished
// file of one that was waiting to be resumed.
func (e *engine) cancelDownload(id uint64) error {
	j := e.dl.find(id)
	if j == nil {
		return errors.New("That download is no longer in the list.")
	}
	e.dl.mu.Lock()
	if !j.Resumable {
		cancel := j.cancel
		e.dl.mu.Unlock()
		if cancel != nil {
			cancel(nil)
		}
		return nil
	}
	if j.part != "" {
		os.Remove(j.part)
	}
	j.part, j.have, j.Resumable, j.Queue = "", nil, false, 0
	j.State, j.Error = fetch.Cancelled, "Cancelled."
	e.dl.mu.Unlock()
	fmt.Fprintf(e.log, "download %d (%s): unfinished file discarded\n", j.ID, j.host)
	e.end(j)
	return nil
}

// pauseDownload stops a running download, keeping its unfinished file when
// it can be resumed.
func (e *engine) pauseDownload(id uint64) error {
	j := e.dl.find(id)
	if j == nil {
		return errors.New("That download is no longer in the list.")
	}
	e.dl.mu.Lock()
	cancel, active := j.cancel, isRunning(j.State)
	e.dl.mu.Unlock()
	if !active || cancel == nil {
		return errors.New("That download isn't running.")
	}
	cancel(errPaused)
	return nil
}

// removeDownloads takes downloads off the list. A paused download's
// unfinished file is deleted; with deleteFiles, so is a finished one's file.
// Running downloads are left alone and reported.
func (e *engine) removeDownloads(ids []uint64, deleteFiles bool) error {
	busy, failed := 0, 0
	for _, id := range ids {
		e.dl.mu.Lock()
		var j *job
		at := -1
		for i, x := range e.dl.jobs {
			if x.ID == id {
				j, at = x, i
				break
			}
		}
		if j == nil {
			e.dl.mu.Unlock()
			continue
		}
		if isRunning(j.State) {
			busy++
			e.dl.mu.Unlock()
			continue
		}
		var files []string
		if j.part != "" {
			files = append(files, j.part)
		}
		if deleteFiles && j.State == fetch.Done && j.Path != "" {
			files = append(files, j.Path)
		}
		e.dl.jobs = append(e.dl.jobs[:at], e.dl.jobs[at+1:]...)
		e.dl.mu.Unlock()
		for _, f := range files {
			if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
				failed++
				fmt.Fprintf(e.log, "download %d (%s): couldn't delete its file: %v\n", j.ID, j.host, err)
			}
		}
		if e.jr != nil {
			if err := e.jr.Delete(j.ID); err != nil {
				fmt.Fprintf(e.log, "download %d (%s): couldn't take it off the saved list: %v\n", j.ID, j.host, err)
			}
		}
	}
	switch {
	case busy == 1:
		return errors.New("A download is still running; stop it first.")
	case busy > 1:
		return fmt.Errorf("%d downloads are still running; stop them first.", busy)
	case failed > 0:
		return errors.New("Some files couldn't be deleted; they may be open in another program.")
	}
	return nil
}

// addURL downloads an address typed or pasted into the window.
func (e *engine) addURL(raw string) (uint64, error) {
	u, err := webAddress(raw)
	if err != nil {
		return 0, err
	}
	return e.download(probe.Request{URL: u}), nil
}

func webAddress(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("That isn't a web address. It should start with http:// or https://.")
	}
	return u.String(), nil
}

// resumeDownload carries on with a paused or failed download.
func (e *engine) resumeDownload(id uint64) error {
	j := e.dl.find(id)
	if j == nil {
		return errors.New("That download is no longer in the list.")
	}
	e.dl.mu.Lock()
	if !j.Resumable {
		e.dl.mu.Unlock()
		return errors.New("That download can't be resumed.")
	}
	j.Resumable, j.State, j.Error = false, fetch.Checking, ""
	e.dl.mu.Unlock()
	fmt.Fprintf(e.log, "download %d (%s): resuming\n", j.ID, j.host)
	e.save(j)
	e.launch(j, e.runFile)
	return nil
}

// restore loads the saved list. A download that was running when the engine
// last stopped is paused if its file can be resumed, else marked failed and
// its leftovers deleted.
func (e *engine) restore(jr *journal.Journal) error {
	e.jr = jr
	if err := jr.Trim(keepDownloads); err != nil {
		return err
	}
	recs, err := jr.Load()
	if err != nil {
		return err
	}
	// Work folders of streams cut off by a crash.
	if old, _ := filepath.Glob(filepath.Join(e.dir, workPrefix+"*")); len(old) > 0 {
		for _, p := range old {
			os.RemoveAll(p)
		}
	}
	for _, r := range recs {
		j := &job{Status: r.Status, req: r.Request, part: r.Part, size: r.Size, validator: r.Validator, have: r.Have, dir: r.Dir, name: r.Name, cancel: func(error) {}}
		if r.Request != nil {
			j.host, j.from, j.referrer = hostOf(r.Request.URL), r.Request.URL, r.Request.Referer
		}
		wasRunning := false
		switch j.State {
		case fetch.Checking, fetch.Downloading, fetch.Waiting, fetch.Joining:
			wasRunning = true
		}
		if wasRunning || j.Resumable {
			// With no file yet (added to start later, or stopped before
			// the server answered), there is nothing to lose by starting.
			ok := j.req != nil && (j.part == "" || j.validator != "" && fileExists(j.part))
			switch {
			case ok:
				j.State, j.Resumable = fetch.Paused, true
				j.Done, j.Streams = covered(j.have), 0
				if wasRunning {
					j.Error = "Paused: the app stopped during this download."
				}
				e.save(j)
			default:
				if j.part != "" {
					os.Remove(j.part)
				}
				j.part, j.have, j.Resumable, j.State, j.Streams, j.Queue = "", nil, false, fetch.Failed, 0, 0
				j.Error = "The app stopped during this download; send it from the browser again."
				if !wasRunning {
					j.Error = "The unfinished file is gone; send it from the browser again."
				}
				e.end(j)
			}
		}
		e.dl.jobs = append(e.dl.jobs, j)
		e.dl.next = max(e.dl.next, j.ID)
	}
	return nil
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func covered(rs []outfile.Range) int64 {
	var n int64
	for _, r := range rs {
		n += r.End - r.Start
	}
	return n
}

// running counts the downloads that haven't stopped yet.
func (e *engine) running() int {
	n := 0
	for _, s := range e.dl.list() {
		if isRunning(s.State) {
			n++
		}
	}
	return n
}

func isRunning(state string) bool {
	switch state {
	case fetch.Checking, fetch.Downloading, fetch.Joining, fetch.Waiting:
		return true
	}
	return false
}

// stopAll stops every running download and waits (up to wait) for them to
// save their progress, or clean up the files of those that can't resume.
func (e *engine) stopAll(wait time.Duration) {
	e.dl.mu.Lock()
	e.q.running = false // start nothing new
	for _, j := range e.dl.jobs {
		if j.cancel != nil {
			j.cancel(errShutdown)
		}
	}
	e.dl.mu.Unlock()
	for end := time.Now().Add(wait); e.running() > 0 && time.Now().Before(end); {
		time.Sleep(50 * time.Millisecond)
	}
}
