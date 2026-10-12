// Package probe asks a server what it can do before a download starts: which
// HTTP version it speaks, whether it answers range requests, and how big the
// file is. From that it picks how to fetch the file.
//
// It sends one small request (the first byte only) over TCP, letting TLS
// choose HTTP/2 or HTTP/1.1. If the reply advertises HTTP/3 through Alt-Svc,
// it repeats the request over QUIC and uses HTTP/3 only if that works and
// describes the same file.
package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/quic-go/quic-go/http3"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
)

// Limits from CLAUDE.md. They are ceilings; Phase 4 adjusts within them.
const (
	MaxStreams     = 8
	MaxConnections = 4
	// Below this, splitting a file costs more round trips than it saves.
	MinSplitSize = 1 << 20
)

const (
	HTTP3 = "HTTP/3"
	HTTP2 = "HTTP/2"
	HTTP1 = "HTTP/1.1"

	Multiplexed = "multiplexed" // several streams on one HTTP/2 or HTTP/3 connection
	Ranges      = "ranges"      // several HTTP/1.1 connections, one range each
	Single      = "single"      // one request, start to end
)

// Request is what to fetch and how the browser fetched it.
type Request struct {
	URL     string
	Referer string
	Headers map[string]string
	Cookies []media.Cookie
}

type Options struct {
	RootCAs   *x509.CertPool // nil uses the system roots
	Timeout   time.Duration  // for the TCP probe; default 15s
	H3Timeout time.Duration  // for the QUIC probe; default 5s
	DisableH3 bool
}

// Plan is how the download will be fetched.
type Plan struct {
	Mode        string `json:"mode"`
	Streams     int    `json:"streams"`     // parallel requests in flight
	Connections int    `json:"connections"` // physical connections
	Reason      string `json:"reason"`
}

type Result struct {
	URL          string `json:"url"` // after redirects
	Protocol     string `json:"protocol"`
	H3Addr       string `json:"h3Addr,omitempty"` // host:port for QUIC when Protocol is HTTP/3
	Size         int64  `json:"size"`             // -1 when unknown
	Ranges       bool   `json:"ranges"`
	ContentType  string `json:"contentType,omitempty"`
	FileName     string `json:"fileName,omitempty"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
	AltSvcH3     string `json:"altSvcH3,omitempty"` // what the server advertised
	H3Error      string `json:"h3Error,omitempty"`  // why advertised HTTP/3 wasn't used
	Plan         Plan   `json:"plan"`
}

// StatusError is a refusal from the server. RetryAfter is set for 429 and
// 503 when the server says when to come back.
type StatusError struct {
	Code       int
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("the server answered %d %s", e.Code, http.StatusText(e.Code))
	if e.RetryAfter > 0 {
		msg += fmt.Sprintf("; it asks to wait %s before trying again", e.RetryAfter.Round(time.Second))
	}
	return msg
}

// NewStatusError describes a refusal in resp, with its Retry-After.
func NewStatusError(resp *http.Response) *StatusError {
	return &StatusError{Code: resp.StatusCode, RetryAfter: retryAfter(resp, time.Now())}
}

// Probe checks the server for r and returns what it found and the plan.
func Probe(ctx context.Context, r Request, o Options) (*Result, error) {
	if o.Timeout == 0 {
		o.Timeout = 15 * time.Second
	}
	if o.H3Timeout == 0 {
		o.H3Timeout = 5 * time.Second
	}
	u, err := url.Parse(r.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("the address isn't a web (http or https) address")
	}

	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ForceAttemptHTTP2 = true
	tr.TLSClientConfig = &tls.Config{RootCAs: o.RootCAs}
	defer tr.CloseIdleConnections()
	tcpCtx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	res, altSvc, err := firstByte(tcpCtx, &http.Client{Transport: tr}, r, r.URL)
	if err != nil {
		return nil, err
	}
	final, _ := url.Parse(res.URL)
	if final.Scheme == "https" && !o.DisableH3 {
		res.AltSvcH3 = h3Authority(altSvc)
	}
	if res.AltSvcH3 != "" {
		tryH3(ctx, res, final, r, o)
	}
	res.Plan = choose(res.Protocol, res.Ranges, res.Size)
	return res, nil
}

// tryH3 switches res to HTTP/3 if the advertised QUIC endpoint serves the
// same file. Only same-host alternatives are used: the certificate is checked
// against the host name either way, so changing the port is safe.
func tryH3(ctx context.Context, res *Result, final *url.URL, r Request, o Options) {
	host, port, err := net.SplitHostPort(res.AltSvcH3)
	if err != nil {
		res.H3Error = "the server's HTTP/3 address couldn't be read"
		return
	}
	if host != "" && !strings.EqualFold(host, final.Hostname()) {
		res.H3Error = "the server offers HTTP/3 only on another host"
		return
	}
	h3URL := *final
	h3URL.Host = net.JoinHostPort(final.Hostname(), port)

	tr := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: o.RootCAs}}
	defer tr.Close()
	ctx, cancel := context.WithTimeout(ctx, o.H3Timeout)
	defer cancel()
	// No redirects here: the TCP probe already followed them.
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	h3, _, err := firstByte(ctx, client, r, h3URL.String())
	switch {
	case err != nil:
		res.H3Error = "HTTP/3 didn't connect (UDP may be blocked): " + err.Error()
	case h3.Size != res.Size || h3.Ranges != res.Ranges || h3.ETag != res.ETag:
		res.H3Error = "HTTP/3 described the file differently, so it isn't used"
	default:
		res.Protocol = HTTP3
		res.H3Addr = h3URL.Host
	}
}

// firstByte asks for byte 0 only and reads what the reply says about the file.
func firstByte(ctx context.Context, c *http.Client, r Request, target string) (*Result, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", err
	}
	SetHeaders(req.Header, r)
	req.Header.Set("Range", "bytes=0-0")

	resp, err := c.Do(req)
	if err != nil {
		return nil, "", err
	}
	// Don't read on: a server that ignores Range would send the whole file.
	defer resp.Body.Close()
	io.CopyN(io.Discard, resp.Body, 1)

	res := &Result{
		URL:          resp.Request.URL.String(),
		Protocol:     protocolName(resp.ProtoMajor),
		Size:         -1,
		ContentType:  resp.Header.Get("Content-Type"),
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		FileName:     fileName(resp.Header.Get("Content-Disposition"), resp.Request.URL),
	}
	encoded := ce(resp.Header.Get("Content-Encoding"))
	switch resp.StatusCode {
	case http.StatusPartialContent:
		start, total, ok := ContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != 0 {
			return nil, "", errors.New("the server's reply to a range request couldn't be read")
		}
		res.Size, res.Ranges = total, !encoded
	case http.StatusOK:
		res.Size = resp.ContentLength
	case http.StatusRequestedRangeNotSatisfiable:
		// Only an empty file has no byte 0.
		if _, total, ok := ContentRange(resp.Header.Get("Content-Range")); ok && total == 0 {
			res.Size, res.Ranges = 0, true
			break
		}
		return nil, "", &StatusError{Code: resp.StatusCode}
	default:
		return nil, "", NewStatusError(resp)
	}
	if encoded {
		res.Size = -1 // the length is of the compressed bytes
	}
	return res, resp.Header.Get("Alt-Svc"), nil
}

func choose(protocol string, ranges bool, size int64) Plan {
	single := func(reason string) Plan { return Plan{Mode: Single, Streams: 1, Connections: 1, Reason: reason} }
	switch {
	case !ranges:
		return single("The server doesn't accept range requests, so the file comes in one piece and can't be resumed.")
	case size < 0:
		return single("The server didn't say how big the file is.")
	case size < MinSplitSize:
		return single("The file is small, so splitting it wouldn't help.")
	case protocol == HTTP3 || protocol == HTTP2:
		return Plan{Mode: Multiplexed, Streams: MaxStreams, Connections: 1,
			Reason: fmt.Sprintf("%s carries up to %d parallel ranges on one connection.", protocol, MaxStreams)}
	default:
		return Plan{Mode: Ranges, Streams: MaxConnections, Connections: MaxConnections,
			Reason: fmt.Sprintf("HTTP/1.1 with range support: up to %d connections, one range each.", MaxConnections)}
	}
}

// SetHeaders gives a request the browser's headers, Referer and cookies, and
// asks for the file's own bytes rather than a compressed copy, so sizes and
// ranges count the same bytes.
func SetHeaders(h http.Header, r Request) {
	for k, v := range r.Headers {
		switch strings.ToLower(k) {
		case "range", "cookie", "host", "accept-encoding":
			continue
		}
		h.Set(k, v)
	}
	if r.Referer != "" && h.Get("Referer") == "" {
		h.Set("Referer", r.Referer)
	}
	if ck := cookieHeader(r.Cookies); ck != "" {
		// Go drops this header if a redirect leaves the host, as a browser would.
		h.Set("Cookie", ck)
	}
	h.Set("Accept-Encoding", "identity")
}

func protocolName(major int) string {
	switch major {
	case 3:
		return HTTP3
	case 2:
		return HTTP2
	}
	return HTTP1
}

// ContentRange parses "bytes 0-0/1234". An unknown total ("/*") gives -1.
func ContentRange(v string) (start, total int64, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(v), "bytes ")
	if !found {
		return 0, 0, false
	}
	span, size, found := strings.Cut(rest, "/")
	if !found {
		return 0, 0, false
	}
	total = -1
	if size != "*" {
		n, err := strconv.ParseInt(size, 10, 64)
		if err != nil || n < 0 {
			return 0, 0, false
		}
		total = n
	}
	if span == "*" {
		return 0, total, true
	}
	a, b, found := strings.Cut(span, "-")
	start, err1 := strconv.ParseInt(a, 10, 64)
	end, err2 := strconv.ParseInt(b, 10, 64)
	if !found || err1 != nil || err2 != nil || end < start || (total >= 0 && end >= total) {
		return 0, 0, false
	}
	return start, total, true
}

// h3Authority returns the "host:port" of the first h3 entry in an Alt-Svc
// header, e.g. `h3=":443"; ma=86400, h3-29=":443"` gives ":443".
func h3Authority(altSvc string) string {
	for _, entry := range strings.Split(altSvc, ",") {
		proto, rest, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok || proto != "h3" {
			continue
		}
		v, _, _ := strings.Cut(rest, ";")
		v = strings.Trim(strings.TrimSpace(v), `"`)
		if _, port, err := net.SplitHostPort(v); err == nil && port != "" {
			return v
		}
	}
	return ""
}

func ce(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v != "" && v != "identity"
}

func cookieHeader(cs []media.Cookie) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		if c.Name != "" {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	return strings.Join(parts, "; ")
}

func fileName(disposition string, u *url.URL) string {
	if _, params, err := mime.ParseMediaType(disposition); err == nil && params["filename"] != "" {
		return path.Base(strings.ReplaceAll(params["filename"], `\`, "/"))
	}
	if b := path.Base(u.Path); b != "/" && b != "." {
		return b
	}
	return ""
}

// retryAfter reads Retry-After as seconds or an HTTP date.
func retryAfter(resp *http.Response, now time.Time) time.Duration {
	if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusServiceUnavailable {
		return 0
	}
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

// For is r for another address, such as a segment on a CDN: it keeps only
// the cookies a browser would send there (domain, path and Secure match).
// A cookie without a domain belongs to r's own host only.
func (r Request) For(raw string) Request {
	out := r
	out.URL, out.Cookies = raw, nil
	u, err := url.Parse(raw)
	if err != nil {
		return out
	}
	own := ""
	if o, err := url.Parse(r.URL); err == nil {
		own = strings.ToLower(o.Hostname())
	}
	host := strings.ToLower(u.Hostname())
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	for _, c := range r.Cookies {
		d := strings.TrimPrefix(strings.ToLower(c.Domain), ".")
		switch {
		case d == "" && host != own:
			continue
		case d != "" && host != d && !strings.HasSuffix(host, "."+d):
			continue
		case c.Secure && u.Scheme != "https":
			continue
		case c.Path != "" && !pathMatch(path, c.Path):
			continue
		}
		out.Cookies = append(out.Cookies, c)
	}
	return out
}

// pathMatch is the cookie path rule of RFC 6265 section 5.1.4.
func pathMatch(path, cookie string) bool {
	if path == cookie {
		return true
	}
	return strings.HasPrefix(path, cookie) && (strings.HasSuffix(cookie, "/") || path[len(cookie)] == '/')
}
