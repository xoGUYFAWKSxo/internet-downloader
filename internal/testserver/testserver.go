// Package testserver runs local HTTP servers with known content for engine
// tests: HTTP/1.1 with and without range support, HTTP/2 over TLS, and
// HTTP/3 over QUIC. The HTTP/2 server advertises the HTTP/3 one via Alt-Svc,
// the way real servers do.
package testserver

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

// Content returns n deterministic, non-repeating-looking bytes, so a part
// written at the wrong offset shows up as a mismatch.
func Content(n int) []byte {
	b := make([]byte, n)
	var x uint32 = 2463534242
	for i := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = byte(x)
	}
	return b
}

type Servers struct {
	Body []byte

	H1Range   string // http://, Accept-Ranges: bytes
	H1NoRange string // http://, ignores Range, no Content-Length hint for ranges
	H2        string // https://, HTTP/2, Alt-Svc points at H3
	H3        string // https:// on the UDP port of the QUIC server

	// RootCAs trusts the self-signed certificate used by H2 and H3.
	RootCAs *x509.CertPool
}

const path = "/file.bin"

// Start launches all servers for body; they stop when the test ends.
func Start(t testing.TB, body []byte) *Servers {
	t.Helper()
	s := &Servers{Body: body}
	modTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	ranged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "file.bin", modTime, bytes.NewReader(body))
	})
	noRange := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body)
	})

	h1r := httptest.NewServer(ranged)
	t.Cleanup(h1r.Close)
	s.H1Range = h1r.URL + path

	h1n := httptest.NewServer(noRange)
	t.Cleanup(h1n.Close)
	s.H1NoRange = h1n.URL + path

	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("testserver: listen udp: %v", err)
	}
	h3Port := udp.LocalAddr().(*net.UDPAddr).Port
	altSvc := fmt.Sprintf(`h3=":%d"; ma=60`, h3Port)

	h2 := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Alt-Svc", altSvc)
		ranged(w, r)
	}))
	h2.EnableHTTP2 = true
	h2.StartTLS()
	t.Cleanup(h2.Close)
	s.H2 = h2.URL + path
	s.RootCAs = x509.NewCertPool()
	s.RootCAs.AddCert(h2.Certificate())

	h3 := &http3.Server{
		Handler: ranged,
		TLSConfig: http3.ConfigureTLSConfig(&tls.Config{
			Certificates: h2.TLS.Certificates,
		}),
	}
	go h3.Serve(udp)
	t.Cleanup(func() { h3.Close(); udp.Close() })
	s.H3 = fmt.Sprintf("https://127.0.0.1:%d%s", h3Port, path)
	return s
}
