package fetch

import (
	"context"
	"sync"
	"time"
)

// Limiter caps the bytes per second of every download that shares it, as
// the speed limiter. Readers take bytes on credit: whoever goes past the
// rate waits until the bucket refills, so a few fast streams can't starve
// the rest for long.
type Limiter struct {
	mu      sync.Mutex
	rate    int64   // bytes per second; 0 means no limit
	avail   float64 // may go below zero: bytes taken on credit
	last    time.Time
	changed chan struct{} // closed and replaced when the rate changes
	now     func() time.Time
	parent  *Limiter // bytes taken here are taken from it too
}

func NewLimiter() *Limiter {
	return &Limiter{changed: make(chan struct{}), now: time.Now}
}

// Child gives a limiter of its own, off until SetRate, whose bytes also
// count against l: one download's limit under the one all of them share.
func (l *Limiter) Child() *Limiter {
	c := NewLimiter()
	c.parent = l
	return c
}

// SetRate sets the limit in bytes per second; 0 or less turns it off.
// Readers waiting under the old rate carry on at once.
func (l *Limiter) SetRate(bps int64) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rate = max(bps, 0)
	l.avail, l.last = 0, l.now()
	close(l.changed)
	l.changed = make(chan struct{})
}

// Rate is the limit in bytes per second, or 0 for none.
func (l *Limiter) Rate() int64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rate
}

// chunk is how much to read at a time, up to n: small enough under a limit
// that progress keeps moving instead of arriving in bursts.
func (l *Limiter) chunk(n int) int {
	if l == nil {
		return n
	}
	n = l.parent.chunk(n)
	r := l.Rate()
	if r == 0 {
		return n
	}
	return int(min(int64(n), max(r/8, 4<<10)))
}

// wait takes n bytes from the bucket, and from its parent's, waiting while
// either is overdrawn.
func (l *Limiter) wait(ctx context.Context, n int) error {
	if l == nil {
		return nil
	}
	if err := l.waitOwn(ctx, n); err != nil {
		return err
	}
	return l.parent.wait(ctx, n)
}

func (l *Limiter) waitOwn(ctx context.Context, n int) error {
	l.mu.Lock()
	if l.rate == 0 {
		l.mu.Unlock()
		return nil
	}
	now := l.now()
	burst := float64(max(l.rate/4, 16<<10))
	l.avail = min(l.avail+now.Sub(l.last).Seconds()*float64(l.rate), burst) - float64(n)
	l.last = now
	var d time.Duration
	if l.avail < 0 {
		d = time.Duration(-l.avail / float64(l.rate) * float64(time.Second))
	}
	changed := l.changed
	l.mu.Unlock()
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-changed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
