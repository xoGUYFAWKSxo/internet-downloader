package fetch

import (
	"context"
	"sync"
	"time"
)

// gate limits how many requests are in flight. It starts at the plan's
// stream count. When the server refuses a request as too many, the limit
// drops to the number of requests it is still serving; after a quiet spell
// it grows back one at a time.
type gate struct {
	mu     sync.Mutex
	limit  int
	active int
	wake   chan struct{} // closed and replaced whenever a slot frees up
	lastNo time.Time     // last refusal, or last step up
	now    func() time.Time
}

func newGate(limit int) *gate {
	return &gate{limit: max(limit, 1), wake: make(chan struct{}), now: time.Now}
}

func (g *gate) acquire(ctx context.Context) error {
	for {
		g.mu.Lock()
		if g.active < g.limit {
			g.active++
			g.mu.Unlock()
			return nil
		}
		wake := g.wake
		g.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (g *gate) release() {
	g.mu.Lock()
	g.active--
	g.signal()
	g.mu.Unlock()
}

func (g *gate) signal() {
	close(g.wake)
	g.wake = make(chan struct{})
}

// refused releases a slot whose request the server turned away, and lowers
// the limit to what the server is accepting. It returns the new limit.
func (g *gate) refused() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active--
	g.limit = max(1, min(g.limit, g.active))
	g.lastNo = g.now()
	return g.limit
}

// grow raises a lowered limit by one, up to max, once quiet has passed since
// the last refusal or step up. It reports the limit and whether it changed.
func (g *gate) grow(max int, quiet time.Duration) (int, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.limit >= max || g.lastNo.IsZero() || g.now().Sub(g.lastNo) < quiet {
		return g.limit, false
	}
	g.limit++
	g.lastNo = g.now()
	g.signal()
	return g.limit, true
}

func (g *gate) size() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limit
}
