package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/testserver"
)

// later sends u from the browser and answers its box with Download Later.
func later(t *testing.T, e *engine, u, name string) uint64 {
	t.Helper()
	e.handle(rightClick(u))
	e.asks.mu.Lock()
	id := e.asks.next
	e.asks.mu.Unlock()
	askInfoOf(t, e, id)
	r := answer(e, id, e.dir, name, true)
	var out idRequest
	if r.Type != "asks.start" || json.Unmarshal(r.Payload, &out) != nil {
		t.Fatalf("later: %+v", r)
	}
	return out.ID
}

func call(t *testing.T, e *engine, typ string, v any) ipc.Message {
	t.Helper()
	r := e.handle(ipc.Message{Type: typ, Payload: payload(v)})
	if r.Type != typ {
		t.Fatalf("%s: %+v", typ, r)
	}
	return r
}

func statusOf(e *engine, id uint64) fetch.Status {
	e.dl.mu.Lock()
	defer e.dl.mu.Unlock()
	return e.dl.findLocked(id).Status
}

// Download Later queues links; Start Queue runs them one at a time in the
// queue's order, and the queue stops when they are done. A download that
// fails leaves the queue without holding up the rest.
func TestQueueRunsInOrder(t *testing.T) {
	srv, _ := rangeServer(t, testserver.Content(64<<10), nil)
	bad := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(bad.Close)
	e, stopped := testEngine(t, openJournal(t, filepath.Join(t.TempDir(), "d.db")))
	e.show = (&shown{}).show
	e.setSetting(setDone, "0")
	call(t, e, "settings.save", map[string]string{setQueueMax: "1"})

	a := later(t, e, srv.URL+"/a.bin", "a.bin")
	b := later(t, e, srv.URL+"/b.bin", "b.bin")
	c := later(t, e, srv.URL+"/c.bin", "c.bin")
	if s := statusOf(e, a); s.Queue != 1 || s.State != fetch.Paused {
		t.Fatalf("queued: %+v", s)
	}
	call(t, e, "queue.move", map[string]any{"id": c, "by": -1}) // a, c, b
	call(t, e, "queue.move", map[string]any{"id": a, "by": -1}) // already first
	f := later(t, e, bad.URL+"/gone.bin", "gone.bin")
	call(t, e, "queue.move", map[string]any{"id": f, "by": -1})
	call(t, e, "queue.move", map[string]any{"id": f, "by": -1}) // a, f, c, b

	call(t, e, "queue.start", nil)
	var order []uint64
	for range 4 {
		select {
		case id := <-stopped:
			order = append(order, id)
		case <-time.After(10 * time.Second):
			t.Fatalf("the queue stalled after %v", order)
		}
	}
	if want := []uint64{a, f, c, b}; !equal(order, want) {
		t.Fatalf("ran %v, want %v", order, want)
	}
	for _, id := range []uint64{a, b, c} {
		if s := statusOf(e, id); s.State != fetch.Done || s.Queue != 0 {
			t.Fatalf("after: %+v", s)
		}
	}
	if s := statusOf(e, f); s.State != fetch.Failed || s.Queue != 0 {
		t.Fatalf("failed one: %+v", s)
	}
	var q QueueInfo
	json.Unmarshal(call(t, e, "queue.get", nil).Payload, &q)
	if q.Running {
		t.Fatal("the queue still runs with nothing left")
	}
}

func equal(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Stop Queue pauses the queue's downloads, which stay queued; under the
// speed limiter they run slowly enough to catch. A download paused by hand
// isn't started again until the queue is.
func TestQueueStopAndLimit(t *testing.T) {
	srv, _ := rangeServer(t, testserver.Content(4<<20), nil)
	e, stopped := testEngine(t, openJournal(t, filepath.Join(t.TempDir(), "d.db")))
	e.show = (&shown{}).show
	call(t, e, "settings.save", map[string]string{setLimit: "1", setLimitKB: "64", setQueueMax: "2", setDone: "0"})
	var q QueueInfo
	json.Unmarshal(call(t, e, "queue.get", nil).Payload, &q)
	if q.Limit != 64<<10 {
		t.Fatalf("limit %d", q.Limit)
	}
	a := later(t, e, srv.URL+"/a.bin", "a.bin")
	b := later(t, e, srv.URL+"/b.bin", "b.bin")
	c := later(t, e, srv.URL+"/c.bin", "c.bin")
	call(t, e, "queue.start", nil)
	waitState(t, e, a, fetch.Downloading)
	waitState(t, e, b, fetch.Downloading)
	if s := statusOf(e, c); s.State != fetch.Paused {
		t.Fatalf("a third ran with two allowed: %+v", s)
	}

	if err := e.pauseDownload(a); err != nil {
		t.Fatal(err)
	}
	<-stopped
	waitState(t, e, c, fetch.Downloading) // takes a's turn
	if s := statusOf(e, a); s.State != fetch.Paused || s.Queue == 0 {
		t.Fatalf("paused by hand: %+v", s)
	}

	call(t, e, "queue.stop", nil)
	<-stopped
	<-stopped
	for _, id := range []uint64{a, b, c} {
		if s := statusOf(e, id); s.State != fetch.Paused || !s.Resumable || s.Queue == 0 {
			t.Fatalf("after stop: %+v", s)
		}
	}
	call(t, e, "queue.remove", map[string]any{"ids": []uint64{b}})
	if s := statusOf(e, b); s.Queue != 0 {
		t.Fatalf("removed: %+v", s)
	}
	call(t, e, "queue.add", map[string]any{"ids": []uint64{b}})
	if s := statusOf(e, b); s.Queue <= statusOf(e, c).Queue {
		t.Fatalf("added back: %+v, want after %d", s, statusOf(e, c).Queue)
	}
	e.stopAll(5 * time.Second)
}

func waitState(t *testing.T, e *engine, id uint64, state string) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); statusOf(e, id).State != state; {
		if time.Now().After(end) {
			t.Fatalf("download %d: %+v, want %s", id, statusOf(e, id), state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestQueueAndSettingsLimits(t *testing.T) {
	e, _ := testEngine(t, nil)
	for _, m := range []map[string]string{
		{setLimitKB: "0"},
		{setLimitKB: "fast"},
		{setQueueMax: "11"},
		{setSchedStart: "25:00"},
		{setSchedDays: "17"},
		{setDir: "relative"},
		{setLimit: "1", setQueueMax: "0"}, // all or nothing
	} {
		if r := e.handle(ipc.Message{Type: "settings.save", Payload: payload(m)}); r.Type != "error" {
			t.Errorf("%v: %+v", m, r)
		}
	}
	if e.setting(setLimit) != "0" || e.limit.Rate() != 0 {
		t.Fatal("a refused save changed a setting")
	}
	call(t, e, "settings.save", map[string]string{setSchedStop: ""})
	// Streams and finished downloads can't wait in the queue.
	id := e.newJob("https://a.test/v.m3u8", nil, func(j *job) { j.State = fetch.Failed }).ID
	if r := e.handle(ipc.Message{Type: "queue.add", Payload: payload(map[string]any{"ids": []uint64{id}})}); r.Type != "error" {
		t.Fatalf("stream queued: %+v", r)
	}
	if r := e.handle(ipc.Message{Type: "queue.move", Payload: payload(map[string]any{"id": id, "by": 1})}); r.Type != "error" {
		t.Fatalf("moved: %+v", r)
	}
}

// The scheduler starts the queue at its time on the chosen days, and
// stops it at the stop time.
func TestSchedule(t *testing.T) {
	loc := time.Local
	mon := time.Date(2026, 10, 12, 1, 59, 50, 0, loc) // a Monday
	cases := []struct {
		last, now  time.Time
		hhmm, days string
		want       bool
	}{
		{mon, mon.Add(20 * time.Second), "02:00", "1", true},
		{mon, mon.Add(5 * time.Second), "02:00", "1", false},                        // not yet
		{mon, mon.Add(20 * time.Second), "02:00", "06", false},                      // not on Mondays
		{mon.Add(10 * time.Second), mon.Add(30 * time.Second), "02:00", "1", false}, // already passed
		{mon.Add(-8 * time.Hour), mon.Add(20 * time.Second), "02:00", "1", true},    // woke just after
		{mon.Add(-8 * time.Hour), mon.Add(time.Hour), "02:00", "1", false},          // an hour late
		{time.Date(2026, 10, 11, 23, 59, 50, 0, loc), time.Date(2026, 10, 12, 0, 0, 10, 0, loc), "00:00", "1", true},
		{mon, mon.Add(20 * time.Second), "bad", "1", false},
	}
	for i, c := range cases {
		if got := at(c.last, c.now, c.hhmm, c.days); got != c.want {
			t.Errorf("case %d: got %v", i, got)
		}
	}

	e, _ := testEngine(t, nil)
	e.scheduleTick(mon, mon.Add(20*time.Second))
	if e.queueInfo().Running {
		t.Fatal("ran with the scheduler off")
	}
	// Something to run, so the queue doesn't stop at once.
	e.newJob("https://a.test/x", nil, func(j *job) { j.Queue, j.State = 1, fetch.Downloading })
	call(t, e, "settings.save", map[string]string{setSched: "1", setSchedStart: "02:00", setSchedDays: "1", setSchedStop: "03:00"})
	e.scheduleTick(mon, mon.Add(20*time.Second))
	if !e.queueInfo().Running {
		t.Fatal("the scheduler didn't start the queue")
	}
	e.scheduleTick(mon.Add(time.Hour), mon.Add(time.Hour+20*time.Second))
	if e.queueInfo().Running {
		t.Fatal("the scheduler didn't stop the queue")
	}
}
