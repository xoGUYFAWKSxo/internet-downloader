package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/journal"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/outfile"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/testserver"
)

// rangeServer serves body with ranges and an ETag. hook, if set, wraps the
// writer of each request with a Range header longer than one byte, and
// counts the bytes sent.
func rangeServer(t *testing.T, body []byte, hook func(n int, w http.ResponseWriter, r *http.Request) io.Writer) (*httptest.Server, *atomic.Int64) {
	var sent atomic.Int64
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		var out io.Writer = w
		if hook != nil && r.Header.Get("Range") != "" && r.Header.Get("Range") != "bytes=0-0" {
			out = hook(int(count.Add(1)), w, r)
		}
		http.ServeContent(counting{w, out, &sent}, r, "file.bin", time.Time{}, bytes.NewReader(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &sent
}

type counting struct {
	http.ResponseWriter
	w    io.Writer
	sent *atomic.Int64
}

func (c counting) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.sent.Add(int64(n))
	return n, err
}

// cutAfter ends the connection once n bytes are sent.
type cutAfter struct {
	w http.ResponseWriter
	n int
}

func (c *cutAfter) Write(p []byte) (int, error) {
	if len(p) > c.n {
		c.w.Write(p[:c.n])
		c.w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	c.n -= len(p)
	return c.w.Write(p)
}

func testEngine(t *testing.T, jr *journal.Journal) (*engine, chan uint64) {
	e := newEngine(io.Discard)
	e.dir = t.TempDir()
	e.retries = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond}
	e.checkpointEvery = 20 * time.Millisecond
	if jr != nil {
		if err := e.restore(jr); err != nil {
			t.Fatal(err)
		}
	}
	stopped := make(chan uint64, 4)
	e.finished = func(id uint64) { stopped <- id }
	return e, stopped
}

func openJournal(t *testing.T, path string) *journal.Journal {
	jr, err := journal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jr.Close() })
	return jr
}

// A dropped connection is retried, and only the missing bytes are fetched.
func TestDroppedConnectionResumes(t *testing.T) {
	body := testserver.Content(8 << 20)
	var mu sync.Mutex
	var asked []string
	srv, _ := rangeServer(t, body, func(n int, w http.ResponseWriter, r *http.Request) io.Writer {
		mu.Lock()
		asked = append(asked, r.Header.Get("Range"))
		mu.Unlock()
		if n <= 2 {
			return &cutAfter{w: w, n: 512 << 10}
		}
		return w
	})
	e, stopped := testEngine(t, openJournal(t, filepath.Join(t.TempDir(), "d.db")))
	e.download(probe.Request{URL: srv.URL + "/file.bin"})
	<-stopped
	d := e.dl.list()[0]
	if d.State != fetch.Done {
		t.Fatalf("download %+v", d)
	}
	if got, _ := os.ReadFile(d.Path); !bytes.Equal(got, body) {
		t.Fatal("saved file differs")
	}
	// The first piece was cut after 512 KiB; the retry carries on from there.
	mu.Lock()
	defer mu.Unlock()
	starts := 0
	for _, a := range asked {
		if strings.HasPrefix(a, "bytes=0-") {
			starts++
		}
	}
	if starts != 1 || len(asked) <= 4 {
		t.Fatalf("asked for %v", asked)
	}
}

// Quitting pauses a file download; the next engine lists it as paused and
// Resume finishes it without fetching the saved bytes again.
func TestPauseAndResumeAcrossRestart(t *testing.T) {
	body := testserver.Content(8 << 20)
	var mu sync.Mutex
	var asked []string
	release := make(chan struct{})
	srv, _ := rangeServer(t, body, func(n int, w http.ResponseWriter, r *http.Request) io.Writer {
		mu.Lock()
		asked = append(asked, r.Header.Get("Range"))
		mu.Unlock()
		select {
		case <-release:
			return w
		default:
		}
		return &stall{w: w, n: 1 << 20, ctx: r.Context(), release: release}
	})
	db := filepath.Join(t.TempDir(), "d.db")
	jr := openJournal(t, db)
	e, stopped := testEngine(t, jr)
	id := e.download(probe.Request{URL: srv.URL + "/file.bin", Cookies: nil})
	for end := time.Now().Add(5 * time.Second); ; {
		if d := e.dl.list()[0]; d.State == fetch.Downloading && d.Done >= 4<<20 {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("never got going: %+v", e.dl.list()[0])
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.stopAll(5 * time.Second)
	<-stopped
	d := e.dl.list()[0]
	if d.State != fetch.Paused || !d.Resumable {
		t.Fatalf("after quit: %+v", d)
	}
	jr.Close()

	// The next start.
	e2, stopped2 := testEngine(t, openJournal(t, db))
	e2.dir = e.dir
	d = e2.dl.list()[0]
	if d.ID != id || d.State != fetch.Paused || !d.Resumable || d.Done != 4<<20 {
		t.Fatalf("restored %+v", d)
	}
	mu.Lock()
	asked = nil
	mu.Unlock()
	close(release)
	if r := e2.handle(ipc.Message{Type: "downloads.resume", Payload: payload(idRequest{id})}); r.Type != "downloads.resume" {
		t.Fatalf("resume: %+v", r)
	}
	<-stopped2
	d = e2.dl.list()[0]
	if d.State != fetch.Done {
		t.Fatalf("resumed %+v", d)
	}
	if got, _ := os.ReadFile(d.Path); !bytes.Equal(got, body) {
		t.Fatal("resumed file differs")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, a := range asked {
		if strings.HasPrefix(a, "bytes=0-") || strings.HasPrefix(a, "bytes=2097152-") {
			t.Errorf("fetched a saved part again: %s", a)
		}
	}
	if r := e2.handle(ipc.Message{Type: "downloads.resume", Payload: payload(idRequest{id})}); r.Type != "error" {
		t.Fatalf("resumed a finished download: %+v", r)
	}
}

// stall sends n bytes, then waits for release or for the client to go.
type stall struct {
	w       http.ResponseWriter
	n       int
	ctx     context.Context
	release chan struct{}
}

func (s *stall) Write(p []byte) (int, error) {
	if s.n <= 0 {
		select {
		case <-s.release:
		case <-s.ctx.Done():
			return 0, s.ctx.Err()
		}
		return s.w.Write(p)
	}
	k := min(len(p), s.n)
	s.n -= k
	n, err := s.w.Write(p[:k])
	s.w.(http.Flusher).Flush()
	if err != nil || k == len(p) {
		return n, err
	}
	m, err := s.Write(p[k:])
	return n + m, err
}

// After a crash, a file download is paused with its committed bytes, and a
// stream's leftovers are deleted.
func TestRestoreAfterCrash(t *testing.T) {
	dir := t.TempDir()
	jr := openJournal(t, filepath.Join(t.TempDir(), "d.db"))
	file, _ := outfile.Create(dir, "a.bin", 100)
	file.Close()
	stream, _ := outfile.Create(dir, "b.mp4", -1)
	stream.Close()
	work := filepath.Join(dir, workPrefix+"123")
	os.Mkdir(work, 0o755)
	os.WriteFile(filepath.Join(work, "track0.ts"), []byte("x"), 0o644)

	jr.Add(fetch.Status{ID: 1, Name: "a.bin", State: fetch.Downloading}, &probe.Request{URL: "https://a.test/a.bin"})
	jr.SetFile(1, file.Path(), 100, `"v1"`)
	jr.Commit(1, []outfile.Range{{Start: 0, End: 30}})
	jr.Add(fetch.Status{ID: 2, Name: "b.mp4", State: fetch.Joining}, nil)
	jr.SetFile(2, stream.Path(), -1, "")
	jr.Add(fetch.Status{ID: 3, Name: "c.bin", State: fetch.Done}, nil)

	e := newEngine(io.Discard)
	e.dir = dir
	if err := e.restore(jr); err != nil {
		t.Fatal(err)
	}
	list := e.dl.list()
	if len(list) != 3 {
		t.Fatalf("list %+v", list)
	}
	if a := list[2]; a.State != fetch.Paused || !a.Resumable || a.Done != 30 {
		t.Fatalf("file: %+v", a)
	}
	if b := list[1]; b.State != fetch.Failed || b.Resumable {
		t.Fatalf("stream: %+v", b)
	}
	if list[0].State != fetch.Done {
		t.Fatalf("done: %+v", list[0])
	}
	if fileExists(stream.Path()) || fileExists(filepath.Join(work, "track0.ts")) {
		t.Fatal("a stream's leftovers are still there")
	}
	if !fileExists(file.Path()) {
		t.Fatal("the paused file was deleted")
	}
	// New downloads don't reuse a saved id.
	if e.dl.next != 3 {
		t.Fatalf("next id %d", e.dl.next)
	}

	// Cancelling a paused download deletes its file and forgets it.
	if r := e.handle(ipc.Message{Type: "downloads.cancel", Payload: payload(idRequest{1})}); r.Type != "downloads.cancel" {
		t.Fatalf("cancel: %+v", r)
	}
	if fileExists(file.Path()) {
		t.Fatal("the cancelled file is still there")
	}
	recs, _ := jr.Load()
	if recs[0].Status.State != fetch.Cancelled || recs[0].Request != nil || recs[0].Part != "" {
		t.Fatalf("saved %+v", recs[0])
	}
}
