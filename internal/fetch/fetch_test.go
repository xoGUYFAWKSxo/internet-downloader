package fetch

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/outfile"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/testserver"
)

const size = 3<<20 + 7 // not a multiple of the part count

// get probes url, downloads it and returns the file's bytes.
func get(t *testing.T, url string, po probe.Options) ([]byte, *probe.Result) {
	t.Helper()
	req := probe.Request{URL: url}
	res, err := probe.Probe(context.Background(), req, po)
	if err != nil {
		t.Fatal(err)
	}
	out, err := outfile.Create(t.TempDir(), res.FileName, res.Size)
	if err != nil {
		t.Fatal(err)
	}
	if err := Download(context.Background(), req, res, out, Options{RootCAs: po.RootCAs}); err != nil {
		out.Abort()
		t.Fatal(err)
	}
	path, err := out.Finish()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	return b, res
}

func TestEveryPath(t *testing.T) {
	s := testserver.Start(t, testserver.Content(size))
	cases := []struct {
		name, url string
		o         probe.Options
		protocol  string
		mode      string
	}{
		{"http3 multiplexed", s.H2, probe.Options{RootCAs: s.RootCAs}, probe.HTTP3, probe.Multiplexed},
		{"http2 multiplexed", s.H2, probe.Options{RootCAs: s.RootCAs, DisableH3: true}, probe.HTTP2, probe.Multiplexed},
		{"http1 ranges", s.H1Range, probe.Options{}, probe.HTTP1, probe.Ranges},
		{"single", s.H1NoRange, probe.Options{}, probe.HTTP1, probe.Single},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, res := get(t, tc.url, tc.o)
			if res.Protocol != tc.protocol || res.Plan.Mode != tc.mode {
				t.Fatalf("probe chose %s %s", res.Protocol, res.Plan.Mode)
			}
			if !bytes.Equal(got, s.Body) {
				t.Fatalf("content differs (%d bytes)", len(got))
			}
		})
	}
}

// counting serves body with ranges and counts connections and range requests.
type counting struct {
	conns, ranges atomic.Int32
	protos        sync.Map
}

func serve(t *testing.T, body []byte, tlsOn bool, c *counting) *httptest.Server {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-0" && r.Header.Get("Range") != "" {
			c.ranges.Add(1)
		}
		c.protos.Store(r.Proto, true)
		http.ServeContent(w, r, "f.bin", time.Time{}, bytes.NewReader(body))
	}))
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			c.conns.Add(1)
		}
	}
	if tlsOn {
		srv.EnableHTTP2 = true
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv
}

func TestConnectionLimits(t *testing.T) {
	body := testserver.Content(size)
	t.Run("http1 uses up to 4 connections", func(t *testing.T) {
		var c counting
		srv := serve(t, body, false, &c)
		got, _ := get(t, srv.URL+"/f.bin", probe.Options{})
		// One connection for the probe, then at most four.
		if !bytes.Equal(got, body) || c.ranges.Load() != probe.MaxConnections || c.conns.Load() > 1+probe.MaxConnections {
			t.Fatalf("ranges %d, connections %d", c.ranges.Load(), c.conns.Load())
		}
	})
	t.Run("http2 uses one connection for 8 streams", func(t *testing.T) {
		var c counting
		srv := serve(t, body, true, &c)
		pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
		got, _ := get(t, srv.URL+"/f.bin", probe.Options{RootCAs: pool})
		if _, h1 := c.protos.Load("HTTP/1.1"); !bytes.Equal(got, body) || h1 || c.ranges.Load() != probe.MaxStreams || c.conns.Load() != 2 {
			t.Fatalf("ranges %d, connections %d (probe + download), http/1.1 seen %v", c.ranges.Load(), c.conns.Load(), h1)
		}
	})
}

func TestChangedFileIsRefused(t *testing.T) {
	var version atomic.Value
	version.Store(`"v1"`)
	body := testserver.Content(size)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", version.Load().(string))
		http.ServeContent(w, r, "f.bin", time.Time{}, bytes.NewReader(body))
	}))
	defer srv.Close()
	req := probe.Request{URL: srv.URL}
	res, err := probe.Probe(context.Background(), req, probe.Options{})
	if err != nil {
		t.Fatal(err)
	}
	version.Store(`"v2"`) // the file is replaced after the probe
	out, _ := outfile.Create(t.TempDir(), "f", res.Size)
	defer out.Abort()
	if err := Download(context.Background(), req, res, out, Options{}); !errors.Is(err, ErrChanged) {
		t.Fatalf("got %v", err)
	}
}

func TestBadServers(t *testing.T) {
	body := testserver.Content(size)
	cases := map[string]struct {
		handler func(w http.ResponseWriter, r *http.Request)
		want    string
	}{
		"short body": {func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Range", "bytes "+strings.TrimPrefix(r.Header.Get("Range"), "bytes=")+"/"+itoa(size))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(body[:100])
		}, "connection ended"},
		"wrong part": {func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Range", "bytes 0-99/"+itoa(size))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(body[:100])
		}, "wrong part"},
		"busy": {func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "120") // longer than the engine waits
			w.WriteHeader(http.StatusServiceUnavailable)
		}, "wait 2m0s"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(tc.handler))
			defer srv.Close()
			res := &probe.Result{URL: srv.URL, Protocol: probe.HTTP1, Size: size, Ranges: true,
				Plan: probe.Plan{Mode: probe.Ranges, Streams: 4, Connections: 4}}
			out, _ := outfile.Create(t.TempDir(), "f", size)
			defer out.Abort()
			err := Download(context.Background(), probe.Request{URL: srv.URL}, res, out, Options{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

// Like proof.ovh.net: three requests at a time, the rest refused with 429.
func TestBusyServerGetsFewerStreams(t *testing.T) {
	body := testserver.Content(size)
	var mu sync.Mutex
	active, refused := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if active >= 3 {
			refused++
			mu.Unlock()
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		active++
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock() }()
		time.Sleep(50 * time.Millisecond)
		http.ServeContent(w, r, "f.bin", time.Time{}, bytes.NewReader(body))
	}))
	defer srv.Close()
	res := &probe.Result{URL: srv.URL, Protocol: probe.HTTP1, Size: size, Ranges: true,
		Plan: probe.Plan{Mode: probe.Ranges, Streams: 8, Connections: 8}}
	out, _ := outfile.Create(t.TempDir(), "f", size)
	if err := Download(context.Background(), probe.Request{URL: srv.URL}, res, out, Options{}); err != nil {
		out.Abort()
		t.Fatal(err)
	}
	path, err := out.Finish()
	got, _ := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("content differs: %v", err)
	}
	// The first wave hits the limit; after that the gate stays within it.
	if refused == 0 || refused > 5 {
		t.Fatalf("refused %d times", refused)
	}
}

func TestGate(t *testing.T) {
	g := newGate(4)
	ctx := context.Background()
	for range 4 {
		g.acquire(ctx)
	}
	g.refused()
	g.refused() // two refused while two are served
	if g.size() != 2 {
		t.Fatalf("limit %d", g.size())
	}
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := g.acquire(short); err == nil {
		t.Fatal("acquired past the limit")
	}
	done := make(chan error)
	go func() { done <- g.acquire(ctx) }()
	g.release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	g.refused()
	g.refused()
	if g.size() != 1 {
		t.Fatalf("limit %d, want at least 1", g.size())
	}
}

func TestCancel(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", itoa(size))
		w.Write(make([]byte, 1000))
		w.(http.Flusher).Flush()
		<-block
	}))
	defer srv.Close()
	defer close(block)
	res := &probe.Result{URL: srv.URL, Protocol: probe.HTTP1, Size: size, Plan: probe.Plan{Mode: probe.Single, Streams: 1, Connections: 1}}
	out, _ := outfile.Create(t.TempDir(), "f", size)
	defer out.Abort()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := Download(ctx, probe.Request{URL: srv.URL}, res, out, Options{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestSplit(t *testing.T) {
	for _, tc := range []struct {
		size int64
		n    int
	}{{10, 4}, {3, 8}, {size, 8}, {1, 1}, {7, 0}} {
		parts := split(tc.size, tc.n)
		at := int64(0)
		for _, p := range parts {
			if p.Start != at || p.End <= p.Start {
				t.Fatalf("%d/%d: %v", tc.size, tc.n, parts)
			}
			at = p.End
		}
		if at != tc.size {
			t.Fatalf("%d/%d: ends at %d", tc.size, tc.n, at)
		}
	}
}

func TestIfRange(t *testing.T) {
	cases := []struct{ etag, lm, want string }{
		{`"abc"`, "Mon", `"abc"`},
		{`W/"abc"`, "Mon", "Mon"},
		{"", "Mon", "Mon"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := ifRange(&probe.Result{ETag: c.etag, LastModified: c.lm}); got != c.want {
			t.Errorf("%+v: got %q", c, got)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
