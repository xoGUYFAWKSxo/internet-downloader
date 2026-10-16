package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
)

// Streams the extension found are still offered after a restart, for a day;
// files aren't kept, and new items get new IDs.
func TestReceivedSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "downloads.db")
	e, _ := testEngine(t, openJournal(t, path))
	now := time.Now()
	e.now = func() time.Time { return now }
	if r := e.handle(ext(`{"type":"media","media":{"url":"https://cdn.example/a.m3u8?token=t","kind":"hls","manifest":"#EXTM3U\n",
		"pageTitle":"A video","cookies":[{"name":"s","value":"SECRET"}]}}`)); r.Type != "media.added" {
		t.Fatalf("stream: %+v", r)
	}
	e.addMedia("chrome", media.Context{URL: "https://files.example/big.zip", Kind: media.KindFile})
	e.jr.Close()

	e, _ = testEngine(t, openJournal(t, path))
	got := e.inbox.Summaries()
	if len(got) != 1 || got[0].Kind != media.KindHLS || got[0].PageTitle != "A video" || got[0].Cookies != 1 || got[0].Browser != "firefox" {
		t.Fatalf("after restart: %+v", got)
	}
	it, _ := e.inbox.Get(got[0].ID)
	if it.Context.Cookies[0].Value != "SECRET" || !strings.HasPrefix(it.Context.Manifest, "#EXTM3U") {
		t.Fatalf("item %+v", it)
	}
	if id := e.inbox.Add("chrome", media.Context{URL: "https://x.example/b.mpd", Kind: media.KindDASH}, time.Now()); id <= got[0].ID {
		t.Fatalf("new ID %d reuses %d", id, got[0].ID)
	}
	e.jr.Close()

	// A day later, it's gone.
	jr := openJournal(t, path)
	e = newEngine(e.log)
	e.dir = t.TempDir()
	e.now = func() time.Time { return now.Add(receivedFor + time.Minute) }
	if err := e.restore(jr); err != nil {
		t.Fatal(err)
	}
	if got := e.inbox.Summaries(); len(got) != 0 {
		t.Fatalf("a day later: %+v", got)
	}
}
