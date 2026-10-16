package main

import (
	"strings"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/testserver"
)

// powerEngine is an engine whose power action only records what it was
// asked to do.
func powerEngine(t *testing.T, delay time.Duration) (*engine, chan uint64, *shown, chan string) {
	e, stopped := testEngine(t, nil)
	s := &shown{}
	e.show = s.show
	e.setSetting(setDone, "0")
	did := make(chan string, 1)
	e.power = func(action string) error { did <- action; return nil }
	e.powerDelay = delay
	return e, stopped, s, did
}

func runQueueOnce(t *testing.T, e *engine, stopped chan uint64) {
	t.Helper()
	srv, _ := rangeServer(t, testserver.Content(32<<10), nil)
	call(t, e, "downloads.add", map[string]any{"url": srv.URL + "/a.bin", "later": true})
	call(t, e, "queue.start", nil)
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the queue stalled")
	}
}

// When the queue finishes by itself, the computer turns off after the
// countdown, which has a window of its own.
func TestPowerWhenQueueFinishes(t *testing.T) {
	e, stopped, s, did := powerEngine(t, 50*time.Millisecond)
	call(t, e, "settings.save", map[string]string{setWhenDone: powerOff})
	runQueueOnce(t, e, stopped)
	select {
	case a := <-did:
		if a != powerOff {
			t.Fatalf("did %q", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the computer wasn't turned off")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.Join(s.args, ",") != "--power" {
		t.Fatalf("shown %q", s.args)
	}
	if e.powerInfo().Action != "" {
		t.Fatal("countdown left running")
	}
}

// Cancel stops the countdown, and Now has nothing left to do.
func TestPowerCancel(t *testing.T) {
	e, stopped, _, did := powerEngine(t, time.Hour)
	call(t, e, "settings.save", map[string]string{setWhenDone: powerSleep})
	runQueueOnce(t, e, stopped)
	deadline := time.Now().Add(5 * time.Second)
	for e.powerInfo().Action == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	var p PowerInfo
	if err := jsonOf(call(t, e, "power.get", nil), &p); err != nil || p.Action != powerSleep || p.Left < 3500 || p.Left > 3600 {
		t.Fatalf("countdown: %+v %v", p, err)
	}
	call(t, e, "power.cancel", nil)
	if e.powerInfo().Action != "" {
		t.Fatal("not cancelled")
	}
	if r := e.handle(ipcMsg("power.now")); r.Type != "error" {
		t.Fatalf("now after cancel: %+v", r)
	}
	select {
	case a := <-did:
		t.Fatalf("did %q after cancel", a)
	default:
	}
}

// Nothing happens with the option off, for a queue started with nothing in
// it, when the countdown can't be shown, or when a download has started.
func TestPowerNot(t *testing.T) {
	e, stopped, s, did := powerEngine(t, time.Millisecond)
	runQueueOnce(t, e, stopped)
	call(t, e, "settings.save", map[string]string{setWhenDone: powerOff})
	call(t, e, "queue.start", nil) // nothing left in it
	s.fail = true
	e.dl.mu.Lock()
	e.q.started = true
	e.dl.mu.Unlock()
	e.queueFinished()
	time.Sleep(50 * time.Millisecond)
	select {
	case a := <-did:
		t.Fatalf("did %q", a)
	default:
	}
	if len(s.args) != 0 || e.powerInfo().Action != "" {
		t.Fatalf("shown %q, %+v", s.args, e.powerInfo())
	}

	s.fail = false
	e.powerDelay = time.Hour
	e.queueFinished()
	e.dl.mu.Lock()
	e.dl.jobs = append(e.dl.jobs, &job{Status: fetch.Status{ID: 99, State: fetch.Downloading}})
	e.dl.mu.Unlock()
	if err := e.powerNow("a test"); err == nil || !strings.Contains(err.Error(), "started") {
		t.Fatalf("with a download running: %v", err)
	}
	select {
	case a := <-did:
		t.Fatalf("did %q", a)
	default:
	}
	if err := e.setSetting(setWhenDone, "restart"); err == nil {
		t.Fatal("unknown action taken")
	}
}

func ipcMsg(typ string) ipc.Message { return ipc.Message{Type: typ} }
