package probe

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/testserver"
)

const size = 2 << 20

func probe(t *testing.T, url string, o Options) *Result {
	t.Helper()
	res, err := Probe(context.Background(), Request{URL: url}, o)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestServers(t *testing.T) {
	s := testserver.Start(t, testserver.Content(size))
	cases := []struct {
		name, url     string
		o             Options
		protocol      string
		ranges        bool
		mode          string
		streams, conn int
	}{
		{"http1 ranges", s.H1Range, Options{}, HTTP1, true, Ranges, 4, 4},
		{"http1 no ranges", s.H1NoRange, Options{}, HTTP1, false, Single, 1, 1},
		{"http2", s.H2, Options{RootCAs: s.RootCAs, DisableH3: true}, HTTP2, true, Multiplexed, 8, 1},
		{"http3 via alt-svc", s.H2, Options{RootCAs: s.RootCAs}, HTTP3, true, Multiplexed, 8, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := probe(t, tc.url, tc.o)
			if r.Protocol != tc.protocol || r.Ranges != tc.ranges || r.Size != size || r.FileName != "file.bin" {
				t.Fatalf("got %+v", r)
			}
			if r.Plan.Mode != tc.mode || r.Plan.Streams != tc.streams || r.Plan.Connections != tc.conn || r.Plan.Reason == "" {
				t.Fatalf("plan %+v", r.Plan)
			}
			if tc.protocol == HTTP3 && (r.H3Addr == "" || r.H3Error != "") {
				t.Fatalf("h3 addr %q, error %q", r.H3Addr, r.H3Error)
			}
		})
	}
}

// A server that advertises HTTP/3 where nothing answers, as when a firewall
// drops UDP: the probe must fall back to HTTP/2 and say why.
func TestH3UnreachableFallsBack(t *testing.T) {
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close() // bound but silent
	port := udp.LocalAddr().(*net.UDPAddr).Port
	body := testserver.Content(size)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Alt-Svc", `h3=":`+strconv.Itoa(port)+`"; ma=60`)
		http.ServeContent(w, r, "f", time.Time{}, strings.NewReader(string(body)))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs

	start := time.Now()
	r := probe(t, srv.URL+"/f", Options{RootCAs: pool, H3Timeout: 500 * time.Millisecond})
	if r.Protocol != HTTP2 || r.H3Error == "" || r.AltSvcH3 == "" || r.Plan.Mode != Multiplexed {
		t.Fatalf("got %+v", r)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("fallback took %s", time.Since(start))
	}
}

func TestSendsBrowserContextAndFollowsRedirects(t *testing.T) {
	var got http.Header
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/real", http.StatusFound) })
	mux.HandleFunc("/real", func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Disposition", `attachment; filename="..\evil\Movie.mp4"`)
		http.ServeContent(w, r, "", time.Time{}, strings.NewReader("small"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r, err := Probe(context.Background(), Request{
		URL:     srv.URL + "/start",
		Referer: "https://site.example/watch",
		Headers: map[string]string{"user-agent": "TestBrowser/1", "range": "bytes=5-", "cookie": "x=1"},
		Cookies: []media.Cookie{{Name: "sid", Value: "abc"}, {Name: "lang", Value: "en"}},
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(r.URL, "/real") || r.FileName != "Movie.mp4" || r.Size != 5 {
		t.Fatalf("got %+v", r)
	}
	if r.Plan.Mode != Single || !strings.Contains(r.Plan.Reason, "small") {
		t.Fatalf("plan %+v", r.Plan)
	}
	want := map[string]string{
		"User-Agent": "TestBrowser/1", "Referer": "https://site.example/watch",
		"Cookie": "sid=abc; lang=en", "Range": "bytes=0-0", "Accept-Encoding": "identity",
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, got.Get(k), v)
		}
	}
}

func TestRefusals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/busy":
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
		case "/gone":
			w.WriteHeader(http.StatusNotFound)
		case "/empty":
			w.Header().Set("Content-Range", "bytes */0")
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		}
	}))
	defer srv.Close()

	_, err := Probe(context.Background(), Request{URL: srv.URL + "/busy"}, Options{})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 429 || se.RetryAfter != 2*time.Minute || !strings.Contains(err.Error(), "wait 2m0s") {
		t.Fatalf("busy: %v", err)
	}
	if _, err := Probe(context.Background(), Request{URL: srv.URL + "/gone"}, Options{}); !errors.As(err, &se) || se.Code != 404 {
		t.Fatalf("gone: %v", err)
	}
	r := probe(t, srv.URL+"/empty", Options{})
	if r.Size != 0 || r.Plan.Mode != Single {
		t.Fatalf("empty: %+v", r)
	}
	if _, err := Probe(context.Background(), Request{URL: "ftp://x/y"}, Options{}); err == nil {
		t.Fatal("ftp accepted")
	}
}

// A compressed reply's length isn't the file's length.
func TestCompressedReplyHasNoKnownSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Range", "bytes 0-0/5000000")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte{0})
	}))
	defer srv.Close()
	r := probe(t, srv.URL, Options{})
	if r.Size != -1 || r.Ranges || r.Plan.Mode != Single {
		t.Fatalf("got %+v", r)
	}
}

func TestContentRange(t *testing.T) {
	cases := []struct {
		in           string
		start, total int64
		ok           bool
	}{
		{"bytes 0-0/1234", 0, 1234, true},
		{"bytes 0-0/*", 0, -1, true},
		{"bytes */0", 0, 0, true},
		{"bytes 5-9/10", 5, 10, true},
		{"bytes 0-10/10", 0, 0, false},
		{"bytes 9-5/10", 0, 0, false},
		{"0-0/10", 0, 0, false},
		{"bytes 0-0/x", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, c := range cases {
		s, n, ok := contentRange(c.in)
		if ok != c.ok || (ok && (s != c.start || n != c.total)) {
			t.Errorf("%q: got %d %d %v", c.in, s, n, ok)
		}
	}
}

func TestH3Authority(t *testing.T) {
	cases := map[string]string{
		`h3=":443"; ma=86400, h3-29=":443"`:   ":443",
		`h3-29=":443", h3="alt.example:8443"`: "alt.example:8443",
		`h2=":443"`:                           "",
		`clear`:                               "",
		`h3=":"`:                              "",
		``:                                    "",
	}
	for in, want := range cases {
		if got := h3Authority(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestOtherHostH3IsNotUsed(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Alt-Svc", `h3="other.example:443"`)
		w.Write([]byte("x"))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	r := probe(t, srv.URL, Options{RootCAs: pool})
	if r.Protocol != HTTP2 || !strings.Contains(r.H3Error, "another host") {
		t.Fatalf("got %+v", r)
	}
}
