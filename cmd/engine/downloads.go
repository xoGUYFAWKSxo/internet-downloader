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
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/outfile"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
)

type job struct {
	fetch.Status
	out    *outfile.File
	cancel context.CancelFunc
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
		if j.out != nil && v.State == fetch.Downloading {
			v.Done = j.out.Written()
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

// startDownload downloads a received item in the background. Only plain
// files are supported so far: HLS and DASH need their fragments fetched and
// joined, which is Phase 4.
func (e *engine) startDownload(id uint64) (uint64, error) {
	it, ok := e.inbox.Get(id)
	if !ok {
		return 0, errors.New("That item is no longer in the list.")
	}
	if it.Context.Kind == "hls" || it.Context.Kind == "dash" {
		return 0, errors.New("Downloading HLS and DASH streams isn't built yet; only plain video and audio files can be downloaded so far.")
	}
	c := it.Context
	return e.download(probe.Request{URL: c.URL, Referer: c.Referer, Headers: c.Headers, Cookies: c.Cookies}), nil
}

// download starts r and returns its id at once.
func (e *engine) download(r probe.Request) uint64 {
	ctx, cancel := context.WithCancel(context.Background())
	e.dl.mu.Lock()
	e.dl.next++
	j := &job{Status: fetch.Status{ID: e.dl.next, Name: hostOf(r.URL), Size: -1, State: fetch.Checking, Started: e.now().Format(time.RFC3339)}, cancel: cancel}
	e.dl.jobs = append(e.dl.jobs, j)
	e.dl.mu.Unlock()
	go func() {
		defer cancel()
		e.run(ctx, j, r)
		if e.finished != nil {
			e.finished(j.ID)
		}
	}()
	return j.ID
}

func (e *engine) run(ctx context.Context, j *job, r probe.Request) {
	host := hostOf(r.URL)
	fail := func(err error) {
		state := fetch.Failed
		if errors.Is(err, context.Canceled) {
			state, err = fetch.Cancelled, errors.New("cancelled")
		}
		fmt.Fprintf(e.log, "download %d (%s): %s: %v\n", j.ID, host, state, err)
		e.dl.update(j, func(j *job) {
			j.State, j.Error = state, capitalise(err.Error())+"."
			if j.out != nil {
				j.Done = j.out.Written()
			}
		})
	}

	pctx, pcancel := context.WithTimeout(ctx, 30*time.Second)
	res, err := probe.Probe(pctx, r, e.probe)
	pcancel()
	if err != nil {
		fail(err)
		return
	}
	name := res.FileName
	if name == "" {
		name = host
	}
	out, err := outfile.Create(e.dir, name, res.Size)
	if err != nil {
		fail(err)
		return
	}
	e.dl.update(j, func(j *job) {
		j.out, j.Name, j.Size, j.State = out, out.Name(), res.Size, fetch.Downloading
		j.Protocol, j.Mode, j.Streams = res.Protocol, res.Plan.Mode, res.Plan.Streams
	})
	fmt.Fprintf(e.log, "download %d (%s): %s, %s, %d streams, %d bytes\n", j.ID, host, res.Protocol, res.Plan.Mode, res.Plan.Streams, res.Size)

	if err := fetch.Download(ctx, r, res, out, fetch.Options{RootCAs: e.probe.RootCAs, OnLimit: func(n int) {
		fmt.Fprintf(e.log, "download %d (%s): the server is busy; now %d parallel requests\n", j.ID, host, n)
		e.dl.update(j, func(j *job) { j.Streams = n })
	}}); err != nil {
		// Without the Phase 5 range log a partial file can't be resumed.
		out.Abort()
		fail(err)
		return
	}
	path, err := out.Finish()
	if err != nil {
		out.Abort()
		fail(err)
		return
	}
	fmt.Fprintf(e.log, "download %d (%s): done, %d bytes\n", j.ID, host, out.Written())
	e.dl.update(j, func(j *job) {
		j.State, j.Path, j.Name, j.Done = fetch.Done, path, filepath.Base(path), out.Written()
		if j.Size < 0 {
			j.Size = j.Done
		}
	})
}

func (e *engine) cancelDownload(id uint64) error {
	j := e.dl.find(id)
	if j == nil {
		return errors.New("That download is no longer in the list.")
	}
	j.cancel()
	return nil
}
