package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
)

// The queue runs waiting downloads a few at a time, in order, like IDM's
// main queue. Download Later puts a link in it; Start Queue or the
// scheduler runs it, and it stops by itself when nothing is left to start.
type queueState struct {
	running bool
	// skip holds the downloads that stopped during this run (paused by
	// hand, or failed): they aren't started again until the queue is.
	skip map[uint64]bool
}

type QueueInfo = fetch.QueueInfo

func (e *engine) queueInfo() QueueInfo {
	e.dl.mu.Lock()
	defer e.dl.mu.Unlock()
	return QueueInfo{Running: e.q.running, Limit: e.limit.Rate()}
}

// lastInQueue is the highest place taken. The caller holds e.dl.mu.
func (e *engine) lastInQueue() int {
	n := 0
	for _, j := range e.dl.jobs {
		n = max(n, j.Queue)
	}
	return n
}

func (e *engine) startQueue(by string) {
	e.dl.mu.Lock()
	e.q.running, e.q.skip = true, map[uint64]bool{}
	e.dl.mu.Unlock()
	fmt.Fprintf(e.log, "queue: started by %s\n", by)
	e.pumpQueue()
}

// stopQueue stops the queue and pauses its running downloads. One whose
// server can't resume is left to finish rather than thrown away.
func (e *engine) stopQueue(by string) {
	e.dl.mu.Lock()
	was := e.q.running
	e.q.running = false
	var stop []context.CancelCauseFunc
	for _, j := range e.dl.jobs {
		if j.Queue > 0 && isRunning(j.State) && j.resume != "no" && j.cancel != nil {
			stop = append(stop, j.cancel)
		}
	}
	e.dl.mu.Unlock()
	for _, c := range stop {
		c(errPaused)
	}
	if was {
		fmt.Fprintf(e.log, "queue: stopped by %s\n", by)
	}
}

// pumpQueue starts the next queued downloads while the queue runs, up to
// the number allowed at once.
func (e *engine) pumpQueue() {
	most, _ := strconv.Atoi(e.setting(setQueueMax))
	e.dl.mu.Lock()
	if !e.q.running {
		e.dl.mu.Unlock()
		return
	}
	busy := 0
	var next []*job
	for _, j := range e.dl.jobs {
		switch {
		case j.Queue == 0:
		case isRunning(j.State):
			busy++
		case j.Resumable && !e.q.skip[j.ID]:
			next = append(next, j)
		}
	}
	if busy == 0 && len(next) == 0 {
		e.q.running = false
		e.dl.mu.Unlock()
		fmt.Fprintln(e.log, "queue: nothing left to start; stopped")
		return
	}
	sort.Slice(next, func(a, b int) bool { return next[a].Queue < next[b].Queue })
	next = next[:max(0, min(len(next), most-busy))]
	for _, j := range next {
		j.Resumable, j.State, j.Error = false, fetch.Checking, ""
	}
	e.dl.mu.Unlock()
	for _, j := range next {
		fmt.Fprintf(e.log, "download %d (%s): started by the queue\n", j.ID, j.host)
		e.save(j)
		e.launch(j, e.runFile)
	}
}

// ranOut is called when any download stops. A queued one that stopped
// without finishing is skipped for the rest of this run.
func (e *engine) ranOut(j *job) {
	e.dl.mu.Lock()
	if j.Queue > 0 && e.q.skip != nil && j.State != fetch.Done {
		e.q.skip[j.ID] = true
	}
	e.dl.mu.Unlock()
	e.pumpQueue()
}

// queueAdd puts downloads at the end of the queue.
func (e *engine) queueAdd(ids []uint64) error {
	refused := 0
	var changed []*job
	e.dl.mu.Lock()
	for _, id := range ids {
		j := e.dl.findLocked(id)
		switch {
		case j == nil || j.Queue > 0:
		case j.req == nil || !j.Resumable:
			refused++
		default:
			j.Queue = e.lastInQueue() + 1
			changed = append(changed, j)
		}
	}
	e.dl.mu.Unlock()
	for _, j := range changed {
		e.save(j)
	}
	e.pumpQueue()
	if refused > 0 {
		return errors.New("Only stopped downloads that can be resumed can go in the queue.")
	}
	return nil
}

// queueRemove takes downloads out of the queue; running ones carry on.
func (e *engine) queueRemove(ids []uint64) {
	var changed []*job
	e.dl.mu.Lock()
	for _, id := range ids {
		if j := e.dl.findLocked(id); j != nil && j.Queue > 0 {
			j.Queue = 0
			changed = append(changed, j)
		}
	}
	e.dl.mu.Unlock()
	for _, j := range changed {
		e.save(j)
	}
}

// queueMove moves a download one place earlier (by < 0) or later.
func (e *engine) queueMove(id uint64, by int) error {
	e.dl.mu.Lock()
	var q []*job
	for _, j := range e.dl.jobs {
		if j.Queue > 0 {
			q = append(q, j)
		}
	}
	sort.Slice(q, func(a, b int) bool { return q[a].Queue < q[b].Queue })
	at := -1
	for i, j := range q {
		if j.ID == id {
			at = i
		}
	}
	if at < 0 {
		e.dl.mu.Unlock()
		return errors.New("That download isn't in the queue.")
	}
	to := at + 1
	if by < 0 {
		to = at - 1
	}
	if to < 0 || to >= len(q) {
		e.dl.mu.Unlock()
		return nil
	}
	a, b := q[at], q[to]
	a.Queue, b.Queue = b.Queue, a.Queue
	e.dl.mu.Unlock()
	e.save(a)
	e.save(b)
	return nil
}

// runScheduler starts and stops the queue at the times set in Options.
func (e *engine) runScheduler() {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	last := e.now()
	for range t.C {
		now := e.now()
		e.scheduleTick(last, now)
		last = now
	}
}

func (e *engine) scheduleTick(last, now time.Time) {
	if e.setting(setSched) != "1" {
		return
	}
	if at(last, now, e.setting(setSchedStart), e.setting(setSchedDays)) {
		e.startQueue("the scheduler")
	}
	if stop := e.setting(setSchedStop); stop != "" && at(last, now, stop, "0123456") {
		e.stopQueue("the scheduler")
	}
}

// at tells whether the time of day hhmm, on one of days (0 for Sunday),
// came after last and by now. A time missed by more than a few minutes,
// while the computer slept, is let go.
func at(last, now time.Time, hhmm, days string) bool {
	c, err := time.Parse("15:04", hhmm)
	if err != nil {
		return false
	}
	t := time.Date(now.Year(), now.Month(), now.Day(), c.Hour(), c.Minute(), 0, 0, now.Location())
	if t.After(now) {
		t = t.AddDate(0, 0, -1)
	}
	return t.After(last) && now.Sub(t) < 5*time.Minute && strings.ContainsRune(days, rune('0'+int(t.Weekday())))
}
