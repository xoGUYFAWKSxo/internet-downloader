package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
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
			// Offset 1 matches no part, so every part sees the wrong range.
			w.Header().Set("Content-Range", "bytes 1-100/"+itoa(size))
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

func TestGateGrowsBack(t *testing.T) {
	g := newGate(4)
	now := time.Unix(0, 0)
	g.now = func() time.Time { return now }
	if _, ok := g.grow(4, time.Minute); ok {
		t.Fatal("grew without a refusal")
	}
	ctx := context.Background()
	for range 3 {
		g.acquire(ctx)
	}
	g.refused() // two still served
	now = now.Add(59 * time.Second)
	if _, ok := g.grow(4, time.Minute); ok {
		t.Fatal("grew too soon")
	}
	now = now.Add(time.Second)
	if n, ok := g.grow(4, time.Minute); !ok || n != 3 {
		t.Fatalf("limit %d", n)
	}
	if _, ok := g.grow(4, time.Minute); ok {
		t.Fatal("grew twice in one quiet spell")
	}
	now = now.Add(time.Minute)
	g.grow(4, time.Minute)
	now = now.Add(time.Minute)
	if n, ok := g.grow(4, time.Minute); ok || n != 4 {
		t.Fatalf("grew past the plan: %d", n)
	}
}

// A stream that finishes early takes over half of a slow one.
func TestSlowStreamGetsHelp(t *testing.T) {
	const big = 8 << 20
	body := testserver.Content(big)
	var starts sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rng := r.Header.Get("Range")
		starts.Store(rng, true)
		if strings.HasPrefix(rng, "bytes=0-") {
			// The first piece trickles; the others are fast.
			w = slowWriter{w}
		}
		http.ServeContent(w, r, "f.bin", time.Time{}, bytes.NewReader(body))
	}))
	defer srv.Close()
	res := &probe.Result{URL: srv.URL, Protocol: probe.HTTP1, Size: big, Ranges: true,
		Plan: probe.Plan{Mode: probe.Ranges, Streams: 2, Connections: 2}}
	out, _ := outfile.Create(t.TempDir(), "f", big)
	if err := Download(context.Background(), probe.Request{URL: srv.URL}, res, out, Options{}); err != nil {
		out.Abort()
		t.Fatal(err)
	}
	path, err := out.Finish()
	got, _ := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("content differs: %v", err)
	}
	n := 0
	starts.Range(func(any, any) bool { n++; return true })
	if n < 3 {
		t.Fatalf("only %d range requests; the slow piece was never split", n)
	}
}

type slowWriter struct{ http.ResponseWriter }

func (s slowWriter) Write(p []byte) (int, error) {
	done := 0
	for len(p) > 0 {
		k := min(len(p), 32<<10)
		time.Sleep(10 * time.Millisecond)
		n, err := s.ResponseWriter.Write(p[:k])
		done += n
		if err != nil {
			return done, err
		}
		s.ResponseWriter.(http.Flusher).Flush()
		p = p[k:]
	}
	return done, nil
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
			if p.pos != at || p.end <= p.pos {
				t.Fatalf("%d/%d: piece %d-%d", tc.size, tc.n, p.pos, p.end)
			}
			at = p.end
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
		if got := Validator(&probe.Result{ETag: c.etag, LastModified: c.lm}); got != c.want {
			t.Errorf("%+v: got %q", c, got)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestResumeFetchesOnlyMissing(t *testing.T) {
	body := testserver.Content(size)
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Header.Get("Range"))
		mu.Unlock()
		w.Header().Set("ETag", `"v1"`)
		http.ServeContent(w, r, "f.bin", time.Time{}, bytes.NewReader(body))
	}))
	defer srv.Close()
	res := &probe.Result{URL: srv.URL, Protocol: probe.HTTP1, Size: size, ETag: `"v1"`, Plan: probe.Plan{Mode: probe.Ranges, Streams: 4, Connections: 4}}
	// An earlier run got the first 1 MiB and the last 100 bytes.
	first, err := outfile.Create(t.TempDir(), "f.bin", size)
	if err != nil {
		t.Fatal(err)
	}
	first.WriteAt(body[:1<<20], 0)
	first.WriteAt(body[size-100:], size-100)
	have := first.Have()
	first.Close()
	o, err := outfile.Open(first.Path(), size, have)
	if err != nil {
		t.Fatal(err)
	}
	if err := Download(context.Background(), probe.Request{URL: srv.URL}, res, o, Options{}); err != nil {
		o.Abort()
		t.Fatal(err)
	}
	path, err := o.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, body) {
		t.Fatal("the resumed file differs")
	}
	for _, rng := range asked {
		var a, b int64
		if _, err := fmt.Sscanf(rng, "bytes=%d-%d", &a, &b); err != nil || a < 1<<20 || b >= size-100 {
			t.Errorf("asked again for %q", rng)
		}
	}
}

func TestPieces(t *testing.T) {
	rg := func(a, b int64) outfile.Range { return outfile.Range{Start: a, End: b} }
	ps := pieces([]outfile.Range{rg(0, 10), rg(20, 60), rg(90, 100)}, 4)
	got := [][2]int64{}
	for _, p := range ps {
		got = append(got, [2]int64{p.pos, p.end})
	}
	want := [][2]int64{{0, 10}, {20, 40}, {90, 100}, {40, 60}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("pieces = %v", got)
	}
	if ps := pieces([]outfile.Range{rg(100, 110)}, 2); ps[0].pos != 100 || ps[1].end != 110 {
		t.Fatalf("one span: %v %v", *ps[0], *ps[1])
	}
}

func TestDropped(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("%w after 1 of 2 bytes", ErrDropped), true},
		{&net.OpError{Op: "read", Err: errors.New("reset")}, true},
		{&probe.StatusError{Code: 503}, true},
		{&probe.StatusError{Code: 404}, false},
		{ErrChanged, false},
		{context.Canceled, false},
	}
	for _, c := range cases {
		if got := Dropped(c.err); got != c.want {
			t.Errorf("%v: got %v", c.err, got)
		}
	}
}

// Watch shows each stream's piece and what it has written while the
// download runs, and nothing once it has ended.
func TestWatchShowsConnections(t *testing.T) {
	body := testserver.Content(size)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var a, b int
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &a, &b)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", a, b, size))
		w.Header().Set("Content-Length", strconv.Itoa(b-a+1))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(body[a : a+bufSize])
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Write(body[a+bufSize : b+1])
	}))
	defer srv.Close()
	res := &probe.Result{URL: srv.URL, Protocol: probe.HTTP1, Size: size, Ranges: true,
		Plan: probe.Plan{Mode: probe.Ranges, Streams: 3, Connections: 3}}
	out, _ := outfile.Create(t.TempDir(), "f", size)
	defer out.Abort()
	w := &Watch{}
	errc := make(chan error, 1)
	go func() { errc <- Download(context.Background(), probe.Request{URL: srv.URL}, res, out, Options{Watch: w}) }()

	var cs []Conn
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		cs = w.Connections()
		if len(cs) == 3 && out.Written() == 3*bufSize {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("connections: %+v, %d written", cs, out.Written())
		}
	}
	// Streams are listed as they started, which isn't file order.
	sort.Slice(cs, func(i, j int) bool { return cs[i].Start < cs[j].Start })
	var got int64
	for i, c := range cs {
		if c.State != ConnReceiving || c.Pos != c.Start+bufSize || c.Got != bufSize || c.End <= c.Pos {
			t.Errorf("stream %d: %+v", i, c)
		}
		if i > 0 && c.Start != cs[i-1].End {
			t.Errorf("stream %d doesn't start where %d ends: %+v", i, i-1, cs)
		}
		got += c.Got
	}
	if cs[0].Start != 0 || cs[2].End != size || got != out.Written() {
		t.Errorf("pieces don't cover the file: %+v", cs)
	}
	close(release)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if cs := w.Connections(); cs != nil {
		t.Fatalf("after the end: %+v", cs)
	}
}
