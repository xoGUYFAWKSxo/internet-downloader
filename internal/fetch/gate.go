package fetch

import (
	"context"
	"sync"
)

// gate limits how many parts are in flight. It starts at the plan's stream
// count and only shrinks: when the server refuses a request as too many, the
// limit drops to the number of requests it is still serving. Phase 4 grows
// it again from measurements.
type gate struct {
	mu     sync.Mutex
	limit  int
	active int
	wake   chan struct{} // closed and replaced whenever a slot frees up
}

func newGate(limit int) *gate {
	return &gate{limit: max(limit, 1), wake: make(chan struct{})}
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
	close(g.wake)
	g.wake = make(chan struct{})
	g.mu.Unlock()
}

// refused releases a slot whose request the server turned away, and lowers
// the limit to what the server is accepting. It returns the new limit.
func (g *gate) refused() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active--
	g.limit = max(1, min(g.limit, g.active))
	return g.limit
}

func (g *gate) size() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limit
}
