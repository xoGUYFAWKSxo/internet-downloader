package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/testserver"
)

func ext(msg string) ipc.Message {
	return ipc.Message{Type: "extension", Payload: []byte(`{"browser":"firefox","extension":"x","message":` + msg + `}`)}
}

func TestHandle(t *testing.T) {
	cases := []struct {
		name     string
		in       ipc.Message
		wantType string
		wantErr  string
	}{
		{"ping", ipc.Message{Type: "ping"}, "pong", ""},
		{"extension ping", ext(`{"type":"ping"}`), "pong", ""},
		{"extension media", ext(`{"type":"media","media":{"url":"https://cdn.example/a.m3u8?token=t","kind":"hls","manifest":"#EXTM3U\n"}}`), "media.added", ""},
		{"extension media invalid", ext(`{"type":"media","media":{"url":"ftp://x/a","kind":"video"}}`), "error", "The stream address"},
		{"extension media drm", ext(`{"type":"media","media":{"url":"https://x/a.mpd","kind":"dash","manifest":"<MPD><ContentProtection/></MPD>"}}`), "error", "DRM"},
		{"extension other", ext(`{"type":"upload"}`), "error", "unknown message"},
		{"extension bad json", ipc.Message{Type: "extension", Payload: []byte(`{`)}, "error", "invalid message"},
		{"list", ipc.Message{Type: "media.list"}, "media.list", ""},
		{"unknown", ipc.Message{Type: "nope"}, "error", "unknown request type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := newEngine(&bytes.Buffer{}).handle(tc.in)
			if got.Type != tc.wantType || !strings.Contains(got.Error, tc.wantErr) {
				t.Fatalf("got %+v", got)
			}
			if tc.wantType == "pong" && (!strings.Contains(string(got.Payload), `"version"`) || !strings.Contains(string(got.Payload), `"features":["file"]`)) {
				t.Fatalf("pong without version: %s", got.Payload)
			}
		})
	}
}

func TestMediaIsListedAndSecretsStayOutOfTheLog(t *testing.T) {
	var log bytes.Buffer
	e := newEngine(&log)
	resp := e.handle(ext(`{"type":"media","media":{"url":"https://cdn.example/a.mp4?token=SECRETTOKEN","kind":"video",
		"headers":{"Authorization":"Bearer SECRETAUTH"},"cookies":[{"name":"sid","value":"SECRETCOOKIE"}]}}`))
	if resp.Type != "media.added" {
		t.Fatalf("got %+v", resp)
	}
	for _, s := range []string{"SECRETTOKEN", "SECRETAUTH", "SECRETCOOKIE"} {
		if strings.Contains(log.String(), s) {
			t.Fatalf("log leaks %s: %q", s, log.String())
		}
	}
	if !strings.Contains(log.String(), "cdn.example") {
		t.Fatalf("log missing host: %q", log.String())
	}
	var list []media.Summary
	if err := json.Unmarshal(e.handle(ipc.Message{Type: "media.list"}).Payload, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Browser != "firefox" || list[0].Cookies != 1 {
		t.Fatalf("list = %+v", list)
	}
	if strings.Contains(string(e.handle(ipc.Message{Type: "media.list"}).Payload), "SECRETCOOKIE") {
		t.Fatal("list leaks cookie values")
	}
}

func TestProbeReceivedMedia(t *testing.T) {
	s := testserver.Start(t, testserver.Content(2<<20))
	var log bytes.Buffer
	e := newEngine(&log)
	e.probe = probe.Options{RootCAs: s.RootCAs}
	added := e.handle(ext(`{"type":"media","media":{"url":"` + s.H2 + `?token=SECRETTOKEN","kind":"video",
		"cookies":[{"name":"sid","value":"SECRETCOOKIE"}]}}`))
	var id struct{ ID uint64 }
	json.Unmarshal(added.Payload, &id)

	resp := e.handle(ipc.Message{Type: "media.probe", Payload: payload(map[string]uint64{"id": id.ID})})
	var res probe.Result
	if err := json.Unmarshal(resp.Payload, &res); err != nil || resp.Type != "media.probe" {
		t.Fatalf("got %+v (%v)", resp, err)
	}
	if res.Protocol != probe.HTTP3 || res.Plan.Mode != probe.Multiplexed || res.Size != 2<<20 {
		t.Fatalf("result %+v", res)
	}
	for _, secret := range []string{"SECRETTOKEN", "SECRETCOOKIE"} {
		if strings.Contains(log.String()+string(resp.Payload), secret) {
			t.Fatalf("%s leaked: %s / %s", secret, log.String(), resp.Payload)
		}
	}

	if r := e.handle(ipc.Message{Type: "media.probe", Payload: []byte(`{"id":999}`)}); r.Type != "error" || !strings.Contains(r.Error, "no longer") {
		t.Fatalf("missing id: %+v", r)
	}
}

func TestDownloadReceivedMedia(t *testing.T) {
	s := testserver.Start(t, testserver.Content(3<<20))
	var log bytes.Buffer
	e := newEngine(&log)
	e.probe = probe.Options{RootCAs: s.RootCAs}
	e.dir = t.TempDir()
	stopped := make(chan uint64, 1)
	e.finished = func(id uint64) { stopped <- id }

	added := e.handle(ext(`{"type":"media","media":{"url":"` + s.H2 + `?token=SECRETTOKEN","kind":"video",
		"cookies":[{"name":"sid","value":"SECRETCOOKIE"}]}}`))
	var item idRequest
	json.Unmarshal(added.Payload, &item)
	resp := e.handle(ipc.Message{Type: "media.download", Payload: payload(item)})
	var job idRequest
	if err := json.Unmarshal(resp.Payload, &job); err != nil || resp.Type != "media.download" {
		t.Fatalf("got %+v", resp)
	}
	if id := <-stopped; id != job.ID {
		t.Fatalf("finished %d, want %d", id, job.ID)
	}

	var list []fetch.Status
	json.Unmarshal(e.handle(ipc.Message{Type: "downloads.list"}).Payload, &list)
	d := list[0]
	if d.State != fetch.Done || d.Done != 3<<20 || d.Size != 3<<20 || d.Name != "file.bin" || d.Protocol != probe.HTTP3 || d.Streams != 8 {
		t.Fatalf("download %+v", d)
	}
	if got, _ := os.ReadFile(d.Path); !bytes.Equal(got, s.Body) {
		t.Fatal("saved file differs")
	}
	for _, secret := range []string{"SECRETTOKEN", "SECRETCOOKIE"} {
		if strings.Contains(log.String(), secret) {
			t.Fatalf("%s leaked: %s", secret, log.String())
		}
	}
}

// The right-click menu sends "download": the item is added and started in one go.
func TestDownloadFromMenu(t *testing.T) {
	s := testserver.Start(t, testserver.Content(1<<20))
	e := newEngine(&bytes.Buffer{})
	e.probe = probe.Options{RootCAs: s.RootCAs}
	e.dir = t.TempDir()
	stopped := make(chan uint64, 1)
	e.finished = func(id uint64) { stopped <- id }
	resp := e.handle(ext(`{"type":"download","media":{"url":"` + s.H2 + `","kind":"video"}}`))
	var job idRequest
	if err := json.Unmarshal(resp.Payload, &job); err != nil || resp.Type != "media.download" {
		t.Fatalf("got %+v", resp)
	}
	if id := <-stopped; id != job.ID {
		t.Fatalf("finished %d, want %d", id, job.ID)
	}
	var list []fetch.Status
	json.Unmarshal(e.handle(ipc.Message{Type: "downloads.list"}).Payload, &list)
	if len(list) != 1 || list[0].State != fetch.Done || list[0].Done != 1<<20 {
		t.Fatalf("downloads %+v", list)
	}
	if r := e.handle(ext(`{"type":"download","media":{"url":"blob:https://site/x","kind":"video"}}`)); r.Type != "error" {
		t.Fatalf("bad address accepted: %+v", r)
	}
}

// A download the browser started ("file") is checked with the server first,
// so the browser keeps the ones the app can't fetch again.
func TestFileFromBrowser(t *testing.T) {
	s := testserver.Start(t, testserver.Content(1<<20))
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<html>Your session has expired</html>"))
	}))
	t.Cleanup(page.Close)
	gone := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(gone.Close)
	e := newEngine(&bytes.Buffer{})
	e.probe = probe.Options{RootCAs: s.RootCAs}
	e.dir = t.TempDir()
	stopped := make(chan uint64, 1)
	e.finished = func(id uint64) { stopped <- id }
	file := func(u, contentType string, size int) ipc.Message {
		return e.handle(ext(fmt.Sprintf(`{"type":"file","media":{"url":%q,"kind":"file","contentType":%q,"size":%d}}`, u, contentType, size)))
	}

	refusals := []struct {
		name, url, contentType string
		size                   int
		want                   string
	}{
		{"size differs", s.H2, "application/octet-stream", 1234, "different file"},
		{"web page", page.URL + "/setup.exe", "application/x-msdownload", 0, "web page"},
		{"server refuses", gone.URL + "/setup.exe", "", 0, "404"},
		{"not a web address", "blob:https://site/x", "", 0, "web address"},
	}
	for _, tc := range refusals {
		if r := file(tc.url, tc.contentType, tc.size); r.Type != "error" || !strings.Contains(r.Error, tc.want) {
			t.Fatalf("%s: got %+v", tc.name, r)
		}
	}
	if r := e.handle(ext(`{"type":"file","media":{"url":"` + s.H2 + `","kind":"video"}}`)); r.Type != "error" {
		t.Fatalf("wrong kind accepted: %+v", r)
	}
	if n := len(e.inbox.Summaries()); n != 0 {
		t.Fatalf("refused files were added: %d", n)
	}
	// A link from the right-click menu has no type or size; a page is fine then.
	if r := file(page.URL+"/notes.html", "", 0); r.Type != "media.download" {
		t.Fatalf("link to a page: %+v", r)
	}
	<-stopped

	resp := file(s.H2, "application/octet-stream", 1<<20)
	var job idRequest
	if err := json.Unmarshal(resp.Payload, &job); err != nil || resp.Type != "media.download" {
		t.Fatalf("got %+v", resp)
	}
	if id := <-stopped; id != job.ID {
		t.Fatalf("finished %d, want %d", id, job.ID)
	}
	var list []fetch.Status
	json.Unmarshal(e.handle(ipc.Message{Type: "downloads.list"}).Payload, &list)
	var done *fetch.Status
	for i := range list {
		if list[i].ID == job.ID {
			done = &list[i]
		}
	}
	if done == nil || done.State != fetch.Done || done.Done != 1<<20 {
		t.Fatalf("downloads %+v", list)
	}
}

func TestDownloadRefusals(t *testing.T) {
	e := newEngine(&bytes.Buffer{})
	if r := e.handle(ipc.Message{Type: "media.download", Payload: []byte(`{"id":999}`)}); r.Type != "error" || !strings.Contains(r.Error, "no longer") {
		t.Fatalf("missing: %+v", r)
	}
	if r := e.handle(ipc.Message{Type: "downloads.cancel", Payload: []byte(`{"id":999}`)}); r.Type != "error" {
		t.Fatalf("cancel missing: %+v", r)
	}
}

func TestCancelDownload(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10000000")
		w.Write(make([]byte, 1000))
		w.(http.Flusher).Flush()
		<-release
	}))
	defer srv.Close()
	defer close(release)
	e := newEngine(&bytes.Buffer{})
	e.dir = t.TempDir()
	stopped := make(chan uint64, 1)
	e.finished = func(id uint64) { stopped <- id }
	id := e.download(probe.Request{URL: srv.URL + "/big.bin"})
	for d := e.dl.list()[0]; d.State != fetch.Downloading; d = e.dl.list()[0] {
		if d.State != fetch.Checking {
			t.Fatalf("never started: %+v", d)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r := e.handle(ipc.Message{Type: "downloads.cancel", Payload: payload(idRequest{id})}); r.Type != "downloads.cancel" {
		t.Fatalf("cancel: %+v", r)
	}
	<-stopped
	if d := e.dl.list()[0]; d.State != fetch.Cancelled {
		t.Fatalf("got %+v", d)
	}
	if entries, _ := os.ReadDir(e.dir); len(entries) != 0 {
		t.Fatalf("left %v", entries)
	}
}

func TestSentence(t *testing.T) {
	for in, want := range map[string]string{
		"no route":                "No route.",
		"the machine refused it.": "The machine refused it.",
		"HTTP 404":                "HTTP 404.",
	} {
		if got := sentence(errors.New(in)); got != want {
			t.Errorf("%q: got %q", in, got)
		}
	}
}
