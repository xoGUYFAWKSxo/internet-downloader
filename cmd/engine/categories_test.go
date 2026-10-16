package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/testserver"
)

// A category's folder is offered for its files, made when needed, and used
// when nothing asks; saving into it doesn't change the folder other files
// are offered.
func TestCategoryFolders(t *testing.T) {
	body := testserver.Content(64 << 10)
	srv, _ := rangeServer(t, body, nil)
	e, stopped := testEngine(t, openJournal(t, filepath.Join(t.TempDir(), "d.db")))
	e.show = (&shown{}).show
	for _, bad := range []map[string]string{{"dir.video": "relative"}, {"dir.nope": `C:\x`}} {
		if r := e.handle(ipc.Message{Type: "settings.save", Payload: payload(bad)}); r.Type != "error" {
			t.Fatalf("%v: %+v", bad, r)
		}
	}
	vid := filepath.Join(t.TempDir(), "My videos")
	call(t, e, "settings.save", map[string]string{"dir.video": vid})
	if got := e.settings()["dir.video"]; got != vid {
		t.Fatalf("setting %q", got)
	}

	e.handle(rightClick(srv.URL + "/clip.MP4"))
	if a := askInfoOf(t, e, 1); a.Dir != vid || !dirExists(vid) {
		t.Fatalf("video offered %q", a.Dir)
	}
	if r := answer(e, 1, vid, "clip.mp4", true); r.Type != "asks.start" {
		t.Fatalf("later: %+v", r)
	}
	if got := e.setting(setDir); got != "" {
		t.Fatalf("the video folder became the folder for everything: %q", got)
	}
	e.handle(rightClick(srv.URL + "/file.bin"))
	if a := askInfoOf(t, e, 2); a.Dir != e.dir {
		t.Fatalf("other file offered %q", a.Dir)
	}
	e.dropAsk(2)

	// With no box, the file goes straight into its folder.
	e.setSetting(setAsk, "0")
	e.handle(rightClick(srv.URL + "/b.webm"))
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the download stalled")
	}
	var d fetch.Status
	for _, s := range e.dl.list() {
		if s.Name == "b.webm" {
			d = s
		}
	}
	if got, _ := os.ReadFile(d.Path); d.State != fetch.Done || d.Path != filepath.Join(vid, "b.webm") || !bytes.Equal(got, body) {
		t.Fatalf("done: %+v", d)
	}
}

// The window's categories (TYPES in list.ts) are the engine's.
func TestCategoriesMatchWindow(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "ui", "frontend", "src", "list.ts"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(\w+): \{label: '([^']+)', ext: \[([^\]]*)\]\}`)
	found := re.FindAllStringSubmatch(string(src), -1)
	if len(found) != len(categories) {
		t.Fatalf("list.ts has %d categories, the engine %d", len(found), len(categories))
	}
	for _, m := range found {
		c, ok := categories[m[1]]
		var ext []string
		for _, x := range strings.Split(m[3], ",") {
			ext = append(ext, strings.Trim(strings.TrimSpace(x), "'"))
		}
		if !ok || c.label != m[2] || !slices.Equal(c.ext, ext) {
			t.Fatalf("%s differs: window %q %v, engine %+v", m[1], m[2], ext, c)
		}
	}
	if categoryOf("a.tar.GZ") != "compressed" || categoryOf("noext") != "" || categoryOf("x.bin") != "" {
		t.Fatal("categoryOf")
	}
}

// A download's own limit is checked, kept across a restart, sits under the
// shared one, and slows the download.
func TestDownloadLimit(t *testing.T) {
	body := testserver.Content(192 << 10)
	srv, _ := rangeServer(t, body, nil)
	db := filepath.Join(t.TempDir(), "d.db")
	jr := openJournal(t, db)
	e, _ := testEngine(t, jr)
	e.show = (&shown{}).show
	e.handle(rightClick(srv.URL + "/file.bin"))
	askInfoOf(t, e, 1)
	answer(e, 1, t.TempDir(), "file.bin", true)
	id := e.dl.list()[0].ID
	for _, bad := range []map[string]any{{"id": id, "kb": -1}, {"id": id, "kb": 1000001}, {"id": 999, "kb": 5}} {
		if r := e.handle(ipc.Message{Type: "downloads.limit", Payload: payload(bad)}); r.Type != "error" {
			t.Fatalf("%v: %+v", bad, r)
		}
	}
	call(t, e, "downloads.limit", map[string]any{"id": id, "kb": 256})
	jr.Close()

	e, stopped := testEngine(t, openJournal(t, db))
	if d := e.dl.list()[0]; d.LimitKB != 256 {
		t.Fatalf("after restart: %+v", d)
	}
	j := e.dl.find(id)
	if l := e.limiter(j); l.Rate() != 256<<10 || e.limit.Rate() != 0 {
		t.Fatalf("limiter %d, shared %d", l.Rate(), e.limit.Rate())
	}
	start := time.Now()
	if err := e.resumeDownload(id); err != nil {
		t.Fatal(err)
	}
	<-stopped
	took := time.Since(start)
	d := e.dl.list()[0]
	if got, _ := os.ReadFile(d.Path); d.State != fetch.Done || !bytes.Equal(got, body) {
		t.Fatalf("done: %+v", d)
	}
	// 192 KB at 256 KB/s, less the first burst: about half a second.
	if took < 400*time.Millisecond {
		t.Fatalf("took %s: not limited", took)
	}
	call(t, e, "downloads.limit", map[string]any{"id": id, "kb": 0})
	if e.limiter(j).Rate() != 0 {
		t.Fatal("limit not taken off")
	}
}
