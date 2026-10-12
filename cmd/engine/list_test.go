package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/testserver"
)

func waitFor(t *testing.T, e *engine, ok func(fetch.Status) bool) fetch.Status {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); ; {
		if d := e.dl.list()[0]; ok(d) {
			return d
		}
		if time.Now().After(end) {
			t.Fatalf("never got there: %+v", e.dl.list()[0])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Stop pauses a file download and keeps its unfinished file; Resume
// finishes it.
func TestStopKeepsTheFile(t *testing.T) {
	body := testserver.Content(8 << 20)
	release := make(chan struct{})
	srv, _ := rangeServer(t, body, func(n int, w http.ResponseWriter, r *http.Request) io.Writer {
		select {
		case <-release:
			return w
		default:
		}
		return &stall{w: w, n: 1 << 20, ctx: r.Context(), release: release}
	})
	e, stopped := testEngine(t, openJournal(t, filepath.Join(t.TempDir(), "d.db")))
	id, err := e.addURL(srv.URL + "/file.bin")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, func(d fetch.Status) bool { return d.State == fetch.Downloading && d.Done >= 4<<20 })
	if r := e.handle(ipc.Message{Type: "downloads.pause", Payload: payload(idRequest{id})}); r.Type != "downloads.pause" {
		t.Fatalf("pause: %+v", r)
	}
	<-stopped
	d := e.dl.list()[0]
	if d.State != fetch.Paused || !d.Resumable || d.Error != "" {
		t.Fatalf("after Stop: %+v", d)
	}
	if r := e.handle(ipc.Message{Type: "downloads.pause", Payload: payload(idRequest{id})}); r.Type != "error" {
		t.Fatalf("paused a stopped download: %+v", r)
	}
	close(release)
	if err := e.resumeDownload(id); err != nil {
		t.Fatal(err)
	}
	<-stopped
	d = e.dl.list()[0]
	if got, _ := os.ReadFile(d.Path); d.State != fetch.Done || !bytes.Equal(got, body) {
		t.Fatalf("resumed: %+v", d)
	}
}

// Remove leaves running downloads alone, deletes a paused download's
// unfinished file, and deletes finished files only when asked.
func TestRemoveDownloads(t *testing.T) {
	db := filepath.Join(t.TempDir(), "d.db")
	jr := openJournal(t, db)
	e, stopped := testEngine(t, jr)
	srv, _ := rangeServer(t, testserver.Content(1<<20), nil)
	kept, _ := e.addURL(srv.URL + "/file.bin")
	<-stopped
	gone, _ := e.addURL(srv.URL + "/file.bin")
	<-stopped
	list := e.dl.list()
	keptPath, gonePath := list[1].Path, list[0].Path
	if keptPath == gonePath || !fileExists(keptPath) || !fileExists(gonePath) {
		t.Fatalf("downloads: %+v", list)
	}
	part := filepath.Join(e.dir, "c.bin.part")
	os.WriteFile(part, []byte("x"), 0o644)
	e.dl.mu.Lock()
	e.dl.next++
	paused := &job{part: part}
	paused.ID, paused.State, paused.Resumable = e.dl.next, fetch.Paused, true
	running := &job{}
	e.dl.next++
	running.ID, running.State = e.dl.next, fetch.Downloading
	e.dl.jobs = append(e.dl.jobs, paused, running)
	e.dl.mu.Unlock()

	r := e.handle(ipc.Message{Type: "downloads.remove", Payload: payload(map[string]any{"ids": []uint64{kept, paused.ID, running.ID}})})
	if r.Type != "error" || !strings.Contains(r.Error, "still running") {
		t.Fatalf("remove: %+v", r)
	}
	if !fileExists(keptPath) || fileExists(part) {
		t.Fatal("Remove without deleting files deleted the wrong ones")
	}
	r = e.handle(ipc.Message{Type: "downloads.remove", Payload: payload(map[string]any{"ids": []uint64{gone}, "deleteFiles": true})})
	if r.Type != "downloads.remove" || fileExists(gonePath) {
		t.Fatalf("remove with files: %+v", r)
	}
	if list := e.dl.list(); len(list) != 1 || list[0].ID != running.ID {
		t.Fatalf("left %+v", list)
	}
	recs, _ := jr.Load()
	if len(recs) != 0 {
		t.Fatalf("saved list still has %+v", recs)
	}
}

func TestAddURL(t *testing.T) {
	e, _ := testEngine(t, nil)
	for _, u := range []string{"", "file:///c:/x", "ftp://a.test/x", "https://", "not a url"} {
		if r := e.handle(ipc.Message{Type: "downloads.add", Payload: payload(map[string]string{"url": u})}); r.Type != "error" {
			t.Errorf("%q: %+v", u, r)
		}
	}
	if len(e.dl.list()) != 0 {
		t.Fatal("a bad address was added")
	}
}

// The progress window sees the streams and the written parts while a file
// downloads, what is kept once it is stopped, and the whole file at the end.
func TestDetail(t *testing.T) {
	body := testserver.Content(8 << 20)
	release := make(chan struct{})
	srv, _ := rangeServer(t, body, func(n int, w http.ResponseWriter, r *http.Request) io.Writer {
		select {
		case <-release:
			return w
		default:
		}
		return &stall{w: w, n: 1 << 20, ctx: r.Context(), release: release}
	})
	e, stopped := testEngine(t, openJournal(t, filepath.Join(t.TempDir(), "d.db")))
	if r := e.handle(ipc.Message{Type: "downloads.detail", Payload: payload(idRequest{99})}); r.Type != "error" {
		t.Fatalf("unknown id: %+v", r)
	}
	id, _ := e.addURL(srv.URL + "/file.bin")
	waitFor(t, e, func(d fetch.Status) bool { return d.State == fetch.Downloading && d.Done >= 4<<20 })
	get := func() fetch.Detail {
		t.Helper()
		r := e.handle(ipc.Message{Type: "downloads.detail", Payload: payload(idRequest{id})})
		var d fetch.Detail
		if r.Type != "downloads.detail" || json.Unmarshal(r.Payload, &d) != nil {
			t.Fatalf("detail: %+v", r)
		}
		return d
	}
	spans := func(d fetch.Detail) (n int64) {
		for _, s := range d.Have {
			n += s.End - s.Start
		}
		return n
	}
	d := get()
	if len(d.Conns) == 0 || d.Resume != "yes" || d.Host == "" || strings.Contains(d.Host, "/") || spans(d) < d.Status.Done {
		t.Fatalf("while running: %+v", d)
	}
	var got int64
	for _, c := range d.Conns {
		got += c.Got
	}
	if got < 4<<20 {
		t.Fatalf("streams wrote %d bytes: %+v", got, d.Conns)
	}
	e.pauseDownload(id)
	<-stopped
	if d = get(); len(d.Conns) != 0 || d.Status.State != fetch.Paused || d.Resume != "yes" || spans(d) == 0 || spans(d) != d.Status.Done {
		t.Fatalf("stopped: %+v", d)
	}
	close(release)
	e.resumeDownload(id)
	<-stopped
	if d = get(); d.Status.State != fetch.Done || len(d.Have) != 1 || spans(d) != int64(len(body)) {
		t.Fatalf("done: %+v", d)
	}
}
