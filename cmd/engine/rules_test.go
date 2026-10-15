package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
)

func TestCleanLists(t *testing.T) {
	sites, err := cleanSites("https://www.Example.com/page\n*.cdn.example.net, example.org:8080 ; .foo.io.\nexample.org")
	if want := "www.example.com\ncdn.example.net\nexample.org\nfoo.io"; err != nil || sites != want {
		t.Errorf("sites: got %q, %v; want %q", sites, err, want)
	}
	if _, err := cleanSites("exa mple!.com"); err == nil || !strings.Contains(err.Error(), "mple!.com") {
		t.Errorf("bad site: %v", err)
	}
	types, err := cleanTypes("PDF, .mp3 *.zip;pdf")
	if want := "pdf mp3 zip"; err != nil || types != want {
		t.Errorf("types: got %q, %v; want %q", types, err, want)
	}
	if _, err := cleanTypes("tar.gz"); err == nil {
		t.Error("tar.gz accepted as one type")
	}
	if got, err := cleanSites(""); got != "" || err != nil {
		t.Errorf("empty: %q, %v", got, err)
	}
	e := newEngine(&bytes.Buffer{})
	if err := e.setSettings(map[string]string{setCatchSites: "Example.com", setCatchTypes: ".PDF", setCatchMin: "5"}); err != nil {
		t.Fatal(err)
	}
	if e.setting(setCatchSites) != "example.com" || e.setting(setCatchTypes) != "pdf" {
		t.Errorf("saved %q, %q", e.setting(setCatchSites), e.setting(setCatchTypes))
	}
	for _, bad := range []string{"0", "x", "1000001"} {
		if err := e.setSetting(setCatchMin, bad); err == nil {
			t.Errorf("catchMinMB %q accepted", bad)
		}
	}
}

// The user's rules decide which of the browser's downloads the app takes;
// links sent from the right-click menu are always taken.
func TestBrowserRules(t *testing.T) {
	body := make([]byte, 3<<20)
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.Header().Set("Content-Disposition", `attachment; filename="report.pdf"`)
		w.Header().Set("ETag", `"r1"`)
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
	}))
	t.Cleanup(srv.Close)
	e, stopped := testEngine(t, nil)
	send := func(u, page string, minSize int) ipc.Message {
		return e.handle(ext(fmt.Sprintf(`{"type":"file","media":{"url":%q,"kind":"file","pageUrl":%q,"minSize":%d}}`, u, page, minSize)))
	}
	set := func(k, v string) {
		t.Helper()
		if err := e.setSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	declined := func(name string, r ipc.Message) {
		t.Helper()
		if r.Type != "file.declined" || r.Error != "" {
			t.Fatalf("%s: got %+v", name, r)
		}
	}
	get := srv.URL + "/get?id=1"

	set(setCatchMin, "4")
	declined("smaller than the user's minimum", send(get, "", 1<<20))
	set(setCatchMin, "1")

	set(setCatchSites, "example.com")
	before := asked.Load()
	declined("page on a listed site", send(get, "https://www.example.com/files", 1<<20))
	if asked.Load() != before {
		t.Error("asked the server about a download left to the browser by its site")
	}
	set(setCatchSites, "127.0.0.1")
	declined("file on a listed site", send(get, "https://other.test/", 1<<20))
	set(setCatchSites, "")

	set(setCatchTypes, "zip pdf")
	before = asked.Load()
	declined("type in the address", send(srv.URL+"/archive.ZIP", "", 1<<20))
	if asked.Load() != before {
		t.Error("asked the server about a type left to the browser by its address")
	}
	declined("type the server names", send(get, "", 1<<20))

	// The right-click menu sends no minimum: every rule is skipped.
	set(setCatchMin, "100")
	set(setCatchSites, "127.0.0.1")
	if r := send(get, "", 0); r.Type != "media.download" {
		t.Fatalf("right-click link: %+v", r)
	}
	<-stopped
	if d := e.dl.list()[0]; d.State != fetch.Done || !strings.HasSuffix(d.Path, "report.pdf") {
		t.Fatalf("download: %+v", d)
	}
}

// The Download File Info box says when the list already has the link.
func TestSameLink(t *testing.T) {
	e := newEngine(&bytes.Buffer{})
	const u = "https://dl.test/a.iso"
	add := func(id uint64, from, name, state string) {
		e.dl.jobs = append(e.dl.jobs, &job{Status: fetch.Status{ID: id, Name: name, State: state}, from: from})
	}
	same := func() AskInfo {
		t.Helper()
		info, err := e.askInfo(e.newAsk(media.Context{URL: u, Kind: media.KindHLS}))
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	if s := same(); s.Same != "" {
		t.Fatalf("empty list: %+v", s)
	}
	add(1, u, "a.iso", fetch.Done)
	add(2, "https://dl.test/b.iso", "b.iso", fetch.Downloading)
	add(3, u, "a (1).iso", fetch.Failed)
	if s := same(); s.Same != "done" || s.SameID != 1 || s.SameName != "a.iso" {
		t.Fatalf("finished: %+v", s)
	}
	add(4, u, "a (2).iso", fetch.Paused)
	if s := same(); s.Same != "running" || s.SameID != 4 {
		t.Fatalf("paused: %+v", s)
	}
}
