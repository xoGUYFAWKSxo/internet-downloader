package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/testserver"
)

// shown records the windows the engine opens.
type shown struct {
	mu   sync.Mutex
	args []string
	fail bool
}

func (s *shown) show(args ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return os.ErrNotExist
	}
	s.args = append(s.args, strings.Join(args, " "))
	return nil
}

func (s *shown) list() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.args...)
}

func rightClick(u string) ipc.Message {
	return ext(fmt.Sprintf(`{"type":"download","media":{"url":%q,"kind":"video","cookies":[{"name":"s","value":"secret"}]}}`, u))
}

func askInfoOf(t *testing.T, e *engine, id uint64) AskInfo {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); ; {
		r := e.handle(ipc.Message{Type: "asks.get", Payload: payload(idRequest{id})})
		var a AskInfo
		if r.Type != "asks.get" || json.Unmarshal(r.Payload, &a) != nil {
			t.Fatalf("asks.get: %+v", r)
		}
		if !a.Checking {
			return a
		}
		if time.Now().After(end) {
			t.Fatalf("still checking: %+v", a)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func answer(e *engine, id uint64, dir, name string, later bool) ipc.Message {
	return e.handle(ipc.Message{Type: "asks.start", Payload: payload(map[string]any{"id": id, "dir": dir, "name": name, "later": later})})
}

// A link from the browser waits in the box, with what the server said,
// until Start Download saves it where the user chose; the box for the
// finished download follows, and the folder is offered next time.
func TestAskThenStart(t *testing.T) {
	body := testserver.Content(1 << 20)
	srv, _ := rangeServer(t, body, nil)
	e, stopped := testEngine(t, openJournal(t, filepath.Join(t.TempDir(), "d.db")))
	var w shown
	e.show = w.show

	if r := e.handle(rightClick(srv.URL + "/file.bin")); r.Type != "downloads.ask" || !strings.Contains(string(r.Payload), `"asked":true`) {
		t.Fatalf("right-click: %+v", r)
	}
	if got := w.list(); len(got) != 1 || got[0] != "--ask=1" {
		t.Fatalf("windows: %q", got)
	}
	if len(e.dl.list()) != 0 {
		t.Fatal("a download started before the box was answered")
	}
	a := askInfoOf(t, e, 1)
	if a.Name != "file.bin" || a.Size != int64(len(body)) || a.Resume != "yes" || a.Stream || a.Error != "" || a.Dir != e.dir || strings.Contains(a.Host, "/") {
		t.Fatalf("ask: %+v", a)
	}
	if b, _ := json.Marshal(a); strings.Contains(string(b), "secret") || strings.Contains(string(b), "file.bin?") {
		t.Fatalf("the box was sent the cookies: %s", b)
	}

	dir := filepath.Join(t.TempDir(), "new folder")
	if r := answer(e, 1, "relative", "x.bin", false); r.Type != "error" {
		t.Fatalf("relative folder: %+v", r)
	}
	if r := answer(e, 1, dir, "  ", false); r.Type != "error" {
		t.Fatalf("no name: %+v", r)
	}
	r := answer(e, 1, dir, "my:file.bin", false)
	var started idRequest
	if r.Type != "asks.start" || json.Unmarshal(r.Payload, &started) != nil {
		t.Fatalf("start: %+v", r)
	}
	<-stopped
	d := e.dl.list()[0]
	if got, _ := os.ReadFile(d.Path); d.State != fetch.Done || d.Path != filepath.Join(dir, "my_file.bin") || !bytes.Equal(got, body) {
		t.Fatalf("done: %+v", d)
	}
	if got := w.list(); len(got) != 2 || got[1] != fmt.Sprintf("--progress=%d", started.ID) {
		t.Fatalf("windows after: %q", got)
	}
	if r := e.handle(ipc.Message{Type: "asks.get", Payload: payload(idRequest{1})}); r.Type != "error" {
		t.Fatalf("answered ask still there: %+v", r)
	}
	e.handle(rightClick(srv.URL + "/file.bin"))
	if a := askInfoOf(t, e, 2); a.Dir != dir {
		t.Fatalf("offered %q, want the last folder %q", a.Dir, dir)
	}
	e.handle(ipc.Message{Type: "asks.cancel", Payload: payload(idRequest{2})})
	if r := answer(e, 2, dir, "x.bin", false); r.Type != "error" {
		t.Fatalf("cancelled ask started: %+v", r)
	}
}

// Download Later adds the link paused with nothing on disk; it stays that
// way when the app starts again, and Resume downloads it where chosen.
func TestAskLater(t *testing.T) {
	body := testserver.Content(1 << 20)
	srv, _ := rangeServer(t, body, nil)
	db := filepath.Join(t.TempDir(), "d.db")
	jr := openJournal(t, db)
	e, _ := testEngine(t, jr)
	e.show = (&shown{}).show
	e.handle(rightClick(srv.URL + "/file.bin"))
	askInfoOf(t, e, 1)
	dir := t.TempDir()
	if r := answer(e, 1, dir, "later.bin", true); r.Type != "asks.start" {
		t.Fatalf("later: %+v", r)
	}
	d := e.dl.list()[0]
	if d.State != fetch.Paused || !d.Resumable || d.Name != "later.bin" || d.Size != int64(len(body)) {
		t.Fatalf("added: %+v", d)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("a file was made: %v", left)
	}
	jr.Close()

	e, stopped := testEngine(t, openJournal(t, db))
	if d := e.dl.list()[0]; d.State != fetch.Paused || !d.Resumable {
		t.Fatalf("after restart: %+v", d)
	}
	if err := e.resumeDownload(d.ID); err != nil {
		t.Fatal(err)
	}
	<-stopped
	d = e.dl.list()[0]
	if got, _ := os.ReadFile(d.Path); d.State != fetch.Done || d.Path != filepath.Join(dir, "later.bin") || !bytes.Equal(got, body) {
		t.Fatalf("resumed: %+v", d)
	}
}

// With the box turned off, or when it can't be shown, a right-click starts
// the download at once, as before.
func TestAskOffOrUnavailable(t *testing.T) {
	srv, _ := rangeServer(t, testserver.Content(1<<10), nil)
	for _, off := range []bool{true, false} {
		e, stopped := testEngine(t, nil)
		w := &shown{fail: !off}
		e.show = w.show
		if off {
			if r := e.handle(ipc.Message{Type: "settings.set", Payload: payload(map[string]string{"key": "ask", "value": "0"})}); r.Type != "settings.set" {
				t.Fatalf("set: %+v", r)
			}
			e.setSetting(setDone, "0")
		}
		if r := e.handle(rightClick(srv.URL + "/file.bin")); r.Type != "media.download" {
			t.Fatalf("off=%v: %+v", off, r)
		}
		<-stopped
		if d := e.dl.list()[0]; d.State != fetch.Done {
			t.Fatalf("off=%v: %+v", off, d)
		}
		if got := w.list(); len(got) != 0 {
			t.Fatalf("off=%v: windows %q", off, got)
		}
	}
}

func TestAskLimits(t *testing.T) {
	e, _ := testEngine(t, nil)
	for _, m := range []ipc.Message{
		{Type: "settings.set", Payload: payload(map[string]string{"key": "path", "value": "x"})},
		{Type: "settings.set", Payload: payload(map[string]string{"key": "ask", "value": "yes"})},
		{Type: "asks.add", Payload: payload(map[string]string{"url": "ftp://a.test/x"})},
		{Type: "asks.get", Payload: payload(idRequest{7})},
	} {
		if r := e.handle(m); r.Type != "error" {
			t.Errorf("%s: %+v", m.Payload, r)
		}
	}
	// A stream's name comes from the page, and it can wait in the list.
	r := e.handle(ipc.Message{Type: "asks.add", Payload: payload(map[string]string{"url": "https://a.test/v/master.m3u8"})})
	var in idRequest
	json.Unmarshal(r.Payload, &in)
	if a := askInfoOf(t, e, in.ID); !a.Stream || a.Name != "a.test" || a.Resume != "yes" {
		t.Fatalf("stream: %+v", a)
	}
	if r := answer(e, in.ID, t.TempDir(), "v", true); r.Type != "asks.start" {
		t.Fatalf("stream later: %+v", r)
	}
	if d := e.dl.list()[0]; d.State != fetch.Paused || !d.Resumable || e.dl.find(d.ID).stream == nil {
		t.Fatalf("stream waiting: %+v", d)
	}
	// Boxes never answered are forgotten.
	now := time.Now()
	e.now = func() time.Time { return now }
	for i := 0; i < maxAsks+5; i++ {
		e.newAsk(mediaFor("https://a.test/v.m3u8"))
	}
	if n := len(e.asks.m); n != maxAsks {
		t.Fatalf("%d asks kept", n)
	}
	e.now = func() time.Time { return now.Add(2 * askFor) }
	e.newAsk(mediaFor("https://a.test/v.m3u8"))
	if n := len(e.asks.m); n != 1 {
		t.Fatalf("%d asks kept after an hour", n)
	}
}

func mediaFor(u string) media.Context { return media.Context{URL: u, Kind: media.KindHLS} }
