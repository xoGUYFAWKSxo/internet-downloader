package main

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
)

func TestTrayText(t *testing.T) {
	for n, want := range map[int]string{0: "Internet Downloader", 1: "Internet Downloader: 1 download running", 3: "Internet Downloader: 3 downloads running"} {
		if got := trayTip(n); got != want {
			t.Errorf("trayTip(%d) = %q", n, got)
		}
	}
	if q := quitQuestion(1); q != "A download is still running. Quit and stop it?" {
		t.Error(q)
	}
	if q := quitQuestion(2); q != "2 downloads are still running. Quit and stop them?" {
		t.Error(q)
	}
}

// Quitting from the tray stops running downloads and waits for them, so
// their unfinished files are removed before the engine exits.
func TestStopAll(t *testing.T) {
	e := newEngine(io.Discard)
	block := func(ctx context.Context, j *job) {
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond) // cleanup takes a moment
		e.fail(j, ctx.Err())
	}
	e.start("https://a.test/1", block)
	e.start("https://a.test/2", block)
	e.start("https://a.test/3", func(_ context.Context, j *job) { e.done(j, "3.bin", 1) })
	for end := time.Now().Add(time.Second); e.running() != 2 && time.Now().Before(end); {
		time.Sleep(5 * time.Millisecond)
	}
	if n := e.running(); n != 2 {
		t.Fatalf("running = %d, want 2", n)
	}
	e.stopAll(2 * time.Second)
	if n := e.running(); n != 0 {
		t.Fatalf("running after stopAll = %d", n)
	}
	for _, s := range e.dl.list() {
		if s.State != fetch.Cancelled && s.State != fetch.Done {
			t.Errorf("download %d: %s", s.ID, s.State)
		}
	}
}
