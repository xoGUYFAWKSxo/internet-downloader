package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
)

func releaseServer(t *testing.T, body string, status int) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "GalacticDownloader/0.1.7" {
			t.Errorf("User-Agent %q", r.Header.Get("User-Agent"))
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestUpdateCheck(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		want       string
	}{
		{"newer", `{"tag_name":"v0.1.8"}`, 200, "0.1.8"},
		{"newer minor", `{"tag_name":"v0.2.0"}`, 200, "0.2.0"},
		{"same", `{"tag_name":"v0.1.7"}`, 200, ""},
		{"older", `{"tag_name":"v0.1.6"}`, 200, ""},
		{"ten above nine", `{"tag_name":"v0.1.10"}`, 200, "0.1.10"},
		{"draft", `{"tag_name":"v0.1.8","draft":true}`, 200, ""},
		{"prerelease", `{"tag_name":"v0.1.8","prerelease":true}`, 200, ""},
		{"odd name", `{"tag_name":"latest"}`, 200, ""},
		{"rate limited", `{}`, 403, ""},
		{"not json", `<html>`, 200, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, _ := testEngine(t, nil)
			e.version = "0.1.7"
			e.upd.url = releaseServer(t, c.body, c.status).URL
			e.checkUpdate(context.Background())
			if got := e.newerVersion(); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// A failed check keeps what the last one found; turning checks off or a
// development build asks nothing and offers nothing.
func TestUpdateCheckOffOrDev(t *testing.T) {
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		w.Write([]byte(`{"tag_name":"v9.0.0"}`))
	}))
	defer srv.Close()
	e, _ := testEngine(t, nil)
	e.version, e.upd.url = "0.1.7", srv.URL
	e.checkUpdate(context.Background())
	if e.newerVersion() != "9.0.0" {
		t.Fatalf("got %q", e.newerVersion())
	}
	r := e.handle(ipc.Message{Type: "update.get"})
	var got map[string]string
	if err := json.Unmarshal(r.Payload, &got); err != nil || got["version"] != "9.0.0" || got["page"] != releasesPage {
		t.Fatalf("update.get: %+v %v", r, err)
	}
	e.upd.url = "http://127.0.0.1:1/nothing"
	e.checkUpdate(context.Background())
	if e.newerVersion() != "9.0.0" {
		t.Errorf("a failed check forgot the version: %q", e.newerVersion())
	}
	if err := e.setSetting(setUpdates, "0"); err != nil {
		t.Fatal(err)
	}
	e.upd.url = srv.URL
	e.checkUpdate(context.Background())
	if e.newerVersion() != "" || asked != 1 {
		t.Errorf("off: got %q, asked %d times", e.newerVersion(), asked)
	}
	e.setSetting(setUpdates, "1")
	e.version = "0.0.0-dev"
	e.checkUpdate(context.Background())
	if e.newerVersion() != "" || asked != 1 {
		t.Errorf("development build: got %q, asked %d times", e.newerVersion(), asked)
	}
}

// The computer is kept awake only while a download runs, and only when the
// setting is on.
func TestKeepAwake(t *testing.T) {
	e, _ := testEngine(t, nil)
	calls := make(chan bool, 10)
	quit := make(chan struct{})
	ended := make(chan struct{})
	go func() {
		e.keepAwake(5*time.Millisecond, func(on bool) error { calls <- on; return nil }, quit)
		close(ended)
	}()
	expect := func(want bool) {
		t.Helper()
		select {
		case got := <-calls:
			if got != want {
				t.Fatalf("got %v, want %v", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("never asked for %v", want)
		}
	}
	j := &job{Status: fetch.Status{ID: 1, State: fetch.Downloading}}
	e.dl.mu.Lock()
	e.dl.jobs = append(e.dl.jobs, j)
	e.dl.mu.Unlock()
	expect(true)
	e.setSetting(setAwake, "0")
	expect(false)
	e.setSetting(setAwake, "1")
	expect(true)
	e.dl.update(j, func(j *job) { j.State = fetch.Done })
	expect(false)
	e.dl.update(j, func(j *job) { j.State = fetch.Waiting })
	expect(true)
	close(quit)
	expect(false)
	<-ended
	select {
	case got := <-calls:
		t.Errorf("extra call %v", got)
	default:
	}
}

func TestStayAwake(t *testing.T) {
	if err := stayAwake(true); err != nil {
		t.Fatal(err)
	}
	if err := stayAwake(false); err != nil {
		t.Fatal(err)
	}
}
