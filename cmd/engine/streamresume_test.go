package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/testserver"
)

// stallSite serves an HLS playlist of 12 segments named prefix0.ts to
// prefix11.ts. Until released, segments from 6 on wait for the client to
// give up. It records which segments were asked for.
type stallSite struct {
	srv     *httptest.Server
	segs    []byte
	mu      sync.Mutex
	asked   []int
	prefix  string
	release chan struct{}
}

func newStallSite(t *testing.T) *stallSite {
	s := &stallSite{segs: testserver.Content(12 * 5000), prefix: "seg", release: make(chan struct{})}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		prefix := s.prefix
		s.mu.Unlock()
		switch p := r.URL.Path; {
		case p == "/v/index.m3u8":
			var b strings.Builder
			b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:4\n")
			for i := range 12 {
				fmt.Fprintf(&b, "#EXTINF:4,\n%s%d.ts\n", prefix, i)
			}
			b.WriteString("#EXT-X-ENDLIST\n")
			w.Write([]byte(b.String()))
		case strings.HasPrefix(p, "/v/"+prefix):
			n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(p, "/v/"+prefix), ".ts"))
			if err != nil || n < 0 || n > 11 {
				http.NotFound(w, r)
				return
			}
			s.mu.Lock()
			s.asked = append(s.asked, n)
			s.mu.Unlock()
			if n >= 6 {
				select {
				case <-s.release:
				case <-r.Context().Done():
					return
				}
			}
			w.Write(s.segs[n*5000 : (n+1)*5000])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stallSite) takeAsked() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.asked
	s.asked = nil
	return a
}

// startStalled starts the stream and waits until its first 6 segments are
// written and it is stuck on the rest.
func startStalled(t *testing.T, e *engine, s *stallSite) uint64 {
	t.Helper()
	id := e.downloadStream(media.Context{URL: s.srv.URL + "/v/index.m3u8?token=SECRETTOKEN", Kind: media.KindHLS, PageTitle: "clip",
		Cookies: []media.Cookie{{Name: "sid", Value: "SECRETCOOKIE"}}})
	for end := time.Now().Add(5 * time.Second); ; {
		if d := e.dl.list()[0]; d.State == fetch.Downloading && d.PartsDone >= 6 {
			return id
		}
		if time.Now().After(end) {
			t.Fatalf("never got going: %+v", e.dl.list()[0])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Quitting pauses a stream; the next engine lists it as paused with its
// segments, and Resume fetches only the segments that were missing.
func TestStreamResumesAcrossRestart(t *testing.T) {
	s := newStallSite(t)
	db := filepath.Join(t.TempDir(), "d.db")
	jr := openJournal(t, db)
	e, stopped := testEngine(t, jr)
	e.ffmpeg = ""
	id := startStalled(t, e, s)
	e.stopAll(5 * time.Second)
	<-stopped
	d := e.dl.list()[0]
	if d.State != fetch.Paused || !d.Resumable {
		t.Fatalf("after quit: %+v", d)
	}
	if left, _ := filepath.Glob(filepath.Join(e.dir, "*.part")); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
	jr.Close()

	e2, stopped2 := testEngine(t, openJournal(t, db))
	e2.dir, e2.ffmpeg = e.dir, ""
	d = e2.dl.list()[0]
	if d.ID != id || d.State != fetch.Paused || !d.Resumable || d.PartsDone != 6 || d.Done != 6*5000 {
		t.Fatalf("restored %+v", d)
	}
	if det, _ := e2.detail(id); det.Resume != "yes" {
		t.Fatalf("detail %+v", det)
	}
	s.takeAsked()
	close(s.release)
	call(t, e2, "downloads.resume", idRequest{id})
	<-stopped2
	d = e2.dl.list()[0]
	if d.State != fetch.Done || d.PartsDone != 12 || d.Done != int64(len(s.segs)) {
		t.Fatalf("resumed %+v", d)
	}
	if got, _ := os.ReadFile(d.Path); !bytes.Equal(got, s.segs) {
		t.Fatal("resumed stream differs")
	}
	for _, n := range s.takeAsked() {
		if n < 6 {
			t.Errorf("fetched saved segment %d again", n)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(e.dir, workPrefix+"*")); len(left) != 0 {
		t.Fatalf("work folder left: %v", left)
	}
	db2 := openJournal(t, db)
	recs, _ := db2.Load()
	if recs[0].Stream != "" {
		t.Fatalf("the stream's request was kept after it finished: %s", recs[0].Stream)
	}
}

// Pause keeps the segments; a stream whose playlist lists other segments
// by the time it resumes starts over, and still ends up right.
func TestStreamPauseThenChanged(t *testing.T) {
	s := newStallSite(t)
	var log bytes.Buffer
	e, stopped := testEngine(t, openJournal(t, filepath.Join(t.TempDir(), "d.db")))
	e.log, e.ffmpeg = &log, ""
	id := startStalled(t, e, s)
	call(t, e, "downloads.pause", idRequest{id})
	<-stopped
	if d := e.dl.list()[0]; d.State != fetch.Paused || !d.Resumable {
		t.Fatalf("paused: %+v", d)
	}
	s.mu.Lock()
	s.prefix = "part"
	s.mu.Unlock()
	s.takeAsked()
	close(s.release)
	call(t, e, "downloads.resume", idRequest{id})
	<-stopped
	d := e.dl.list()[0]
	if d.State != fetch.Done {
		t.Fatalf("resumed %+v", d)
	}
	if got, _ := os.ReadFile(d.Path); !bytes.Equal(got, s.segs) {
		t.Fatal("stream differs")
	}
	// Every segment again (the first one twice: the server is probed first).
	got := map[int]bool{}
	for _, n := range s.takeAsked() {
		got[n] = true
	}
	if len(got) != 12 {
		t.Fatalf("fetched segments %v", got)
	}
	if !strings.Contains(log.String(), "starting over") {
		t.Fatalf("log: %s", log.String())
	}
	for _, secret := range []string{"SECRETTOKEN", "SECRETCOOKIE"} {
		if strings.Contains(log.String(), secret) {
			t.Fatalf("%s leaked: %s", secret, log.String())
		}
	}
}

// Cancelling a paused stream deletes its segments; at the next start a
// work folder no paused stream needs is deleted, and one that is needed
// is kept.
func TestStreamLeftovers(t *testing.T) {
	s := newStallSite(t)
	db := filepath.Join(t.TempDir(), "d.db")
	jr := openJournal(t, db)
	e, stopped := testEngine(t, jr)
	e.ffmpeg = ""
	a := startStalled(t, e, s)
	call(t, e, "downloads.pause", idRequest{a})
	<-stopped
	b := startStalled(t, e, s)
	call(t, e, "downloads.pause", idRequest{b})
	<-stopped
	work := func(id uint64) string {
		e.dl.mu.Lock()
		defer e.dl.mu.Unlock()
		return e.dl.findLocked(id).stream.Work
	}
	wa, wb := work(a), work(b)
	if !dirExists(wa) || !dirExists(wb) {
		t.Fatal("no work folders")
	}
	call(t, e, "downloads.cancel", idRequest{a})
	if dirExists(wa) {
		t.Fatal("cancelled stream's segments kept")
	}
	stray := filepath.Join(e.dir, workPrefix+"stray")
	os.Mkdir(stray, 0o755)
	jr.Close()

	e2, _ := testEngine(t, nil)
	e2.dir = e.dir
	if err := e2.restore(openJournal(t, db)); err != nil {
		t.Fatal(err)
	}
	if dirExists(stray) || !dirExists(wb) {
		t.Fatalf("stray kept %v, needed one deleted %v", dirExists(stray), !dirExists(wb))
	}
	call(t, e2, "downloads.remove", map[string]any{"ids": []uint64{b}})
	if dirExists(wb) {
		t.Fatal("removed stream's segments kept")
	}
}

// A saved list naming some other folder as a stream's work folder doesn't
// get that folder deleted.
func TestRemoveWorkOnlyItsOwn(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, "Documents")
	os.Mkdir(other, 0o755)
	removeWork(other)
	if !dirExists(other) {
		t.Fatal("deleted a folder that isn't a work folder")
	}
}
