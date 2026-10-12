package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/outfile"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
)

type job struct {
	fetch.Status
	host string // for the log; never the full address, which can hold tokens
	// progress, while set, gives the bytes and segments written so far.
	progress func() (int64, int)
	cancel   context.CancelFunc
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
// the job's id at once.
func (e *engine) start(u string, run func(context.Context, *job)) uint64 {
	ctx, cancel := context.WithCancel(context.Background())
	e.dl.mu.Lock()
	e.dl.next++
	j := &job{Status: fetch.Status{ID: e.dl.next, Name: hostOf(u), Size: -1, State: fetch.Checking, Started: e.now().Format(time.RFC3339)}, host: hostOf(u), cancel: cancel}
	e.dl.jobs = append(e.dl.jobs, j)
	e.dl.mu.Unlock()
	go func() {
		defer cancel()
		run(ctx, j)
		if e.finished != nil {
			e.finished(j.ID)
		}
	}()
	return j.ID
}

// download starts a plain file download.
func (e *engine) download(r probe.Request) uint64 {
	return e.start(r.URL, func(ctx context.Context, j *job) {
		path, size, err := e.getFile(ctx, j, r, e.dir, func(res *probe.Result, out *outfile.File) {
			e.dl.update(j, func(j *job) {
				j.Name, j.Size, j.State = out.Name(), res.Size, fetch.Downloading
				j.Protocol, j.Mode, j.Streams = res.Protocol, res.Plan.Mode, res.Plan.Streams
				j.progress = func() (int64, int) { return out.Written(), 0 }
			})
		})
		if err != nil {
			e.fail(j, err)
			return
		}
		e.done(j, path, size)
	})
}

// fail records why j stopped.
func (e *engine) fail(j *job, err error) {
	state := fetch.Failed
	if errors.Is(err, context.Canceled) {
		state, err = fetch.Cancelled, errors.New("cancelled")
	}
	fmt.Fprintf(e.log, "download %d (%s): %s: %v\n", j.ID, j.host, state, err)
	e.dl.update(j, func(j *job) {
		j.State, j.Error = state, sentence(err)
		if j.progress != nil {
			j.Done, j.PartsDone = j.progress()
		}
		j.progress = nil
	})
}

// done records where j's file went.
func (e *engine) done(j *job, path string, size int64) {
	fmt.Fprintf(e.log, "download %d (%s): done, %d bytes\n", j.ID, j.host, size)
	e.dl.update(j, func(j *job) {
		j.State, j.Path, j.Name, j.Done = fetch.Done, path, filepath.Base(path), size
		j.Size, j.PartsDone, j.progress = size, j.Parts, nil
	})
}

// getFile probes r and downloads it into dir under the server's file name.
// started is called once the file exists. A failed download deletes its
// .part file: nothing can resume it until the Phase 5 range log exists.
func (e *engine) getFile(ctx context.Context, j *job, r probe.Request, dir string, started func(*probe.Result, *outfile.File)) (string, int64, error) {
	host := hostOf(r.URL)
	pctx, pcancel := context.WithTimeout(ctx, 30*time.Second)
	res, err := probe.Probe(pctx, r, e.probe)
	pcancel()
	if err != nil {
		return "", 0, err
	}
	name := res.FileName
	if name == "" {
		name = host
	}
	out, err := outfile.Create(dir, name, res.Size)
	if err != nil {
		return "", 0, err
	}
	started(res, out)
	fmt.Fprintf(e.log, "download %d (%s): %s, %s, %d streams, %d bytes\n", j.ID, host, res.Protocol, res.Plan.Mode, res.Plan.Streams, res.Size)
	err = fetch.Download(ctx, r, res, out, fetch.Options{RootCAs: e.probe.RootCAs, OnLimit: e.onLimit(j)})
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

func (e *engine) cancelDownload(id uint64) error {
	j := e.dl.find(id)
	if j == nil {
		return errors.New("That download is no longer in the list.")
	}
	j.cancel()
	return nil
}

// running counts the downloads that haven't stopped yet.
func (e *engine) running() int {
	n := 0
	for _, s := range e.dl.list() {
		switch s.State {
		case fetch.Checking, fetch.Downloading, fetch.Joining:
			n++
		}
	}
	return n
}

// stopAll cancels every running download and waits (up to wait) for them to
// stop, so their unfinished files are cleaned up before the engine exits.
func (e *engine) stopAll(wait time.Duration) {
	e.dl.mu.Lock()
	for _, j := range e.dl.jobs {
		j.cancel()
	}
	e.dl.mu.Unlock()
	for end := time.Now().Add(wait); e.running() > 0 && time.Now().Before(end); {
		time.Sleep(50 * time.Millisecond)
	}
}
