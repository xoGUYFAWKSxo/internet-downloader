package testserver

import (
	"bytes"
	"crypto/tls"
	"io"
	"net/http"
	"testing"

	"github.com/quic-go/quic-go/http3"
)

func get(t *testing.T, c *http.Client, url, rng string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, b
}

func TestServers(t *testing.T) {
	body := Content(1 << 20)
	s := Start(t, body)

	tlsConf := &tls.Config{RootCAs: s.RootCAs}
	h12 := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConf, ForceAttemptHTTP2: true}}
	h3t := &http3.Transport{TLSClientConfig: tlsConf}
	defer h3t.Close()
	h3c := &http.Client{Transport: h3t}

	cases := []struct {
		name      string
		c         *http.Client
		url       string
		proto     int
		rangeable bool
	}{
		{"h1-range", h12, s.H1Range, 1, true},
		{"h1-norange", h12, s.H1NoRange, 1, false},
		{"h2", h12, s.H2, 2, true},
		{"h3", h3c, s.H3, 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, b := get(t, tc.c, tc.url, "")
			if resp.ProtoMajor != tc.proto {
				t.Fatalf("proto %s, want major %d", resp.Proto, tc.proto)
			}
			if !bytes.Equal(b, body) {
				t.Fatal("full body mismatch")
			}
			resp, b = get(t, tc.c, tc.url, "bytes=1000-1999")
			if tc.rangeable {
				if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(b, body[1000:2000]) {
					t.Fatalf("range: status %d, %d bytes", resp.StatusCode, len(b))
				}
			} else if resp.StatusCode != http.StatusOK || len(b) != len(body) {
				t.Fatalf("no-range server: status %d, %d bytes", resp.StatusCode, len(b))
			}
		})
	}

	resp, _ := get(t, h12, s.H2, "")
	if resp.Header.Get("Alt-Svc") == "" {
		t.Fatal("H2 server should advertise Alt-Svc")
	}
}
