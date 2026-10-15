package fetch

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/outfile"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/testserver"
)

// A limited download, over ranges and over a single stream, takes about as
// long as its size over the rate, and still arrives whole.
func TestLimitedDownload(t *testing.T) {
	const n, rate = 600 << 10, 1 << 20 // about 0.6 s
	s := testserver.Start(t, testserver.Content(n))
	for _, u := range []string{s.H1Range, s.H1NoRange} {
		lim := NewLimiter()
		lim.SetRate(rate)
		req := probe.Request{URL: u}
		res, err := probe.Probe(context.Background(), req, probe.Options{})
		if err != nil {
			t.Fatal(err)
		}
		out, err := outfile.Create(t.TempDir(), res.FileName, res.Size)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if err := Download(context.Background(), req, res, out, Options{Limit: lim}); err != nil {
			t.Fatal(err)
		}
		took := time.Since(start)
		path, err := out.Finish()
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); !bytes.Equal(got, s.Body) {
			t.Fatalf("%s: content differs", res.Plan.Mode)
		}
		// The bucket starts empty, so even the first bytes wait.
		if took < 450*time.Millisecond || took > 3*time.Second {
			t.Fatalf("%s: %d bytes at %d B/s took %s", res.Plan.Mode, n, rate, took)
		}
	}
}

// Changing the rate lets waiting readers go at once; a stopped download
// stops waiting.
func TestLimiterChanges(t *testing.T) {
	lim := NewLimiter()
	lim.SetRate(1 << 10)
	if c := lim.chunk(bufSize); c != 4<<10 {
		t.Fatalf("chunk %d", c)
	}
	done := make(chan error)
	go func() { done <- lim.wait(context.Background(), 1<<20) }() // ~17 minutes
	time.Sleep(20 * time.Millisecond)
	lim.SetRate(0)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("still waiting after the limit was turned off")
	}
	if c := lim.chunk(bufSize); c != bufSize {
		t.Fatalf("unlimited chunk %d", c)
	}
	lim.SetRate(1 << 10)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- lim.wait(ctx, 1<<20) }()
	cancel()
	if err := <-done; err == nil {
		t.Fatal("a cancelled wait reported success")
	}
	var none *Limiter
	if none.wait(context.Background(), 1<<30) != nil || none.chunk(9) != 9 {
		t.Fatal("no limiter should mean no limit")
	}
}
