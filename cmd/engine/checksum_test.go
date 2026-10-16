package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/testserver"
)

func TestCleanSHA256(t *testing.T) {
	sum := strings.Repeat("ab12", 16)
	for in, want := range map[string]string{
		"":                        "",
		"  ":                      "",
		sum:                       sum,
		strings.ToUpper(sum):      sum,
		"SHA256: " + sum:          sum,
		sum[:32] + " " + sum[32:]: sum,
	} {
		if got, err := cleanSHA256(in); err != nil || got != want {
			t.Errorf("%q: %q, %v", in, got, err)
		}
	}
	for _, in := range []string{sum[:63], sum + "0", strings.Repeat("g", 64), "md5:" + sum} {
		if _, err := cleanSHA256(in); err == nil {
			t.Errorf("%q taken", in)
		}
	}
}

// A download given a SHA-256 is done only when the file matches it; one
// that doesn't match fails, and its file is kept to be checked.
func TestSHA256Check(t *testing.T) {
	body := testserver.Content(300000)
	h := sha256.Sum256(body)
	good := hex.EncodeToString(h[:])
	bad := strings.Repeat("0", 64)
	srv, _ := rangeServer(t, body, nil)
	e, stopped := testEngine(t, nil)
	dir := t.TempDir()
	start := func(sum string) ipc.Message {
		id := e.newAsk(media.Context{URL: srv.URL + "/file.bin"})
		return e.handle(ipc.Message{Type: "asks.start", Payload: payload(map[string]any{"id": id, "dir": dir, "name": "f.bin", "sha256": sum})})
	}

	if r := start(strings.ToUpper(good)); r.Type != "asks.start" {
		t.Fatalf("start: %+v", r)
	}
	<-stopped
	if d := e.dl.list()[0]; d.State != fetch.Done || d.SHA256 != good {
		t.Fatalf("matching: %+v", d)
	}

	if r := start(bad); r.Type != "asks.start" {
		t.Fatalf("start: %+v", r)
	}
	<-stopped
	d := e.dl.list()[0]
	if d.State != fetch.Failed || !strings.Contains(d.Error, "doesn't match") || d.Resumable {
		t.Fatalf("not matching: %+v", d)
	}
	if got, _ := os.ReadFile(d.Path); string(got) != string(body) || filepath.Dir(d.Path) != dir {
		t.Fatalf("file not kept: %s", d.Path)
	}

	// A sum that isn't one is refused, and the box can try again.
	id := e.newAsk(media.Context{URL: srv.URL + "/file.bin"})
	r := e.handle(ipc.Message{Type: "asks.start", Payload: payload(map[string]any{"id": id, "dir": dir, "name": "f.bin", "sha256": "abc"})})
	if r.Type != "error" || !strings.Contains(r.Error, "SHA-256") {
		t.Fatalf("bad sum: %+v", r)
	}
	if _, err := e.askInfo(id); err != nil {
		t.Fatalf("the box was forgotten: %v", err)
	}
}

// Add URL can put links in the queue without starting them; a stream's
// address becomes a stream download.
func TestAddLater(t *testing.T) {
	srv, sent := rangeServer(t, testserver.Content(1000), nil)
	e, _ := testEngine(t, nil)
	a := call(t, e, "downloads.add", map[string]any{"url": srv.URL + "/a.bin", "later": true})
	b := call(t, e, "downloads.add", map[string]any{"url": "https://a.test/v/master.m3u8", "later": true})
	var ia, ib idRequest
	if err := jsonOf(a, &ia); err != nil {
		t.Fatal(err)
	}
	jsonOf(b, &ib)
	for i, id := range []uint64{ia.ID, ib.ID} {
		d, _ := e.detail(id)
		if s := d.Status; s.State != fetch.Paused || !s.Resumable || s.Queue != i+1 {
			t.Fatalf("%d: %+v", id, s)
		}
	}
	if e.dl.find(ib.ID).stream == nil || e.dl.find(ia.ID).stream != nil {
		t.Fatal("stream not told from file")
	}
	if sent.Load() != 0 {
		t.Fatal("a queued link was downloaded")
	}
}

func jsonOf(m ipc.Message, v any) error { return json.Unmarshal(m.Payload, v) }
