package main

import (
	"bytes"
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
