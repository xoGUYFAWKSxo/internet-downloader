// Package fetch downloads a probed file into an outfile.File, following the
// probe's plan: one request, several HTTP/1.1 connections with one range
// each, or several ranges multiplexed on one HTTP/2 or HTTP/3 connection.
//
// The file starts as one piece per stream. A stream that finishes takes over
// the back half of the largest piece still being fetched, so a slow stream
// doesn't hold up the end. Bytes already in the file (from an earlier,
// interrupted run) aren't fetched again.
package fetch

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/outfile"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
)

const (
	bufSize = 256 << 10
	// A part refused as busy this many times, or told to wait longer than
	// maxRetryWait, ends the download with the server's answer.
	maxRefusals  = 10
	maxRetryWait = time.Minute
	// A piece is split only if both halves are at least this big; smaller
	// requests cost more in round trips than they save.
	minPiece = 1 << 20
	// A limit lowered by a busy server goes up by one after this long
	// without a refusal, up to the plan's streams.
	growAfter = 30 * time.Second
)

type Options struct {
	RootCAs *x509.CertPool // nil uses the system roots
	// RetryWait is the pause after a busy refusal without Retry-After;
	// default 5s.
	RetryWait time.Duration
	// OnLimit, if set, hears when a busy server makes the download use
	// fewer parallel requests.
	OnLimit func(streams int)
}

// ErrChanged means the server's file is no longer the one the probe saw.
var ErrChanged = errors.New("the file on the server changed during the download")

// ErrDropped means a connection ended before its bytes were all in.
var ErrDropped = errors.New("the connection ended early")

// Dropped tells whether err is a lost or failed connection, or a server
// error that may pass, so trying again later can work.
func Dropped(err error) bool {
	var ne net.Error
	var se *probe.StatusError
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, ErrChanged):
		return false
	case errors.Is(err, ErrDropped), errors.Is(err, io.ErrUnexpectedEOF), errors.As(err, &ne):
		return true
	case errors.As(err, &se):
		return se.Code == http.StatusBadGateway || se.Code == http.StatusServiceUnavailable || se.Code == http.StatusGatewayTimeout
	}
	var qe *quic.IdleTimeoutError
	return errors.As(err, &qe)
}

// Download fetches res's file into out. out must have res.Size bytes (or -1).
func Download(ctx context.Context, r probe.Request, res *probe.Result, out *outfile.File, o Options) error {
	if out.Size() != res.Size {
		return fmt.Errorf("the output file is %d bytes but the server's file is %d", out.Size(), res.Size)
	}
	if o.RetryWait == 0 {
		o.RetryWait = 5 * time.Second
	}
	target, err := url.Parse(res.URL)
	if err != nil {
		return err
	}
	client, closeIdle := newClient(res, o)
	defer closeIdle()
	if res.Protocol == probe.HTTP3 {
		target.Host = res.H3Addr
	}
	d := &download{gate: newGate(res.Plan.Streams), retryWait: o.RetryWait, onLimit: o.OnLimit, client: client, url: target.String(), req: r, res: res, out: out}

	if res.Plan.Mode == probe.Single || res.Size <= 0 {
		return d.whole(ctx)
	}
	d.queue = pieces(out.Missing(), res.Plan.Streams)
	if len(d.queue) == 0 {
		return nil
	}
	// The first failure stops the other streams.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go d.regrow(ctx, res.Plan.Streams)
	var wg sync.WaitGroup
	for range len(d.queue) {
		wg.Go(func() {
			if err := d.worker(ctx); err != nil {
				cancel(err)
			}
		})
	}
	wg.Wait()
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if m := out.Missing(); len(m) > 0 {
		return fmt.Errorf("bytes %d-%d were never fetched", m[0].Start, m[0].End-1)
	}
	return nil
}

type download struct {
	gate      *gate
	retryWait time.Duration
	onLimit   func(int)
	client    *http.Client
	url       string
	req       probe.Request
	res       *probe.Result
	out       *outfile.File

	mu     sync.Mutex
	queue  []*piece // not started, or handed back after a refusal
	active []*piece // being fetched
}

// piece is a range a stream is fetching. pos only moves forward, by the
// stream that owns it; end can drop when another stream takes the back half.
type piece struct{ pos, end int64 }

// newClient gives a client limited to the plan's connections and the
// protocol the probe chose.
func newClient(res *probe.Result, o Options) (*http.Client, func()) {
	tlsConf := &tls.Config{RootCAs: o.RootCAs}
	if res.Protocol == probe.HTTP3 {
		tr := &http3.Transport{TLSClientConfig: tlsConf}
		// One QUIC connection carries every stream. Redirects were followed
		// by the probe.
		return &http.Client{Transport: tr, CheckRedirect: noRedirect}, func() { tr.Close() }
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tlsConf
	tr.Protocols = new(http.Protocols)
	tr.Protocols.SetHTTP1(true)
	if res.Protocol == probe.HTTP2 {
		tr.Protocols.SetHTTP2(true)
	}
	tr.MaxConnsPerHost = max(res.Plan.Connections, 1)
	tr.MaxIdleConnsPerHost = tr.MaxConnsPerHost
	// The file's bytes as they are: no transparent gzip.
	tr.DisableCompression = true
	return &http.Client{Transport: tr}, tr.CloseIdleConnections
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func (d *download) request(ctx context.Context) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return nil, err
	}
	probe.SetHeaders(req.Header, d.req)
	return req, nil
}

// worker fetches pieces until none are left. A refusal for being busy (429,
// 503) hands the piece back, lowers the limit and waits as long as the server
// asks; any other error ends the download.
func (d *download) worker(ctx context.Context) error {
	for refusals := 0; ; {
		if err := d.gate.acquire(ctx); err != nil {
			return err
		}
		p := d.next()
		if p == nil {
			d.gate.release()
			return nil
		}
		err := d.fetchPiece(ctx, p)
		var se *probe.StatusError
		if !errors.As(err, &se) || (se.Code != http.StatusTooManyRequests && se.Code != http.StatusServiceUnavailable) {
			d.gate.release()
			if err != nil {
				return err
			}
			d.finished(p)
			refusals = 0
			continue
		}
		d.handBack(p)
		if n := d.gate.refused(); d.onLimit != nil {
			d.onLimit(n)
		}
		wait := se.RetryAfter
		if wait == 0 {
			wait = d.retryWait
		}
		if refusals++; refusals > maxRefusals || wait > maxRetryWait {
			return err
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// next gives a stream the next piece to fetch: one waiting in the queue, else
// the back half of the largest piece in flight, else nil.
func (d *download) next() *piece {
	d.mu.Lock()
	defer d.mu.Unlock()
	var p *piece
	if len(d.queue) > 0 {
		p, d.queue = d.queue[0], d.queue[1:]
	} else {
		var big *piece
		for _, a := range d.active {
			if big == nil || a.end-a.pos > big.end-big.pos {
				big = a
			}
		}
		if big == nil || big.end-big.pos < 2*minPiece {
			return nil
		}
		mid := big.pos + (big.end-big.pos)/2
		p = &piece{pos: mid, end: big.end}
		big.end = mid
	}
	d.active = append(d.active, p)
	return p
}

func (d *download) finished(p *piece) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.drop(p)
}

func (d *download) handBack(p *piece) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.drop(p)
	if p.pos < p.end {
		d.queue = append(d.queue, p)
	}
}

func (d *download) drop(p *piece) {
	for i, a := range d.active {
		if a == p {
			d.active = append(d.active[:i], d.active[i+1:]...)
			return
		}
	}
}

// claim takes up to n bytes from the front of p and returns where they go.
// Fewer than n means the rest of the piece now belongs to another stream.
func (d *download) claim(p *piece, n int) (off int64, k int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	k = int(min(int64(n), p.end-p.pos))
	off = p.pos
	p.pos += int64(k)
	return off, k
}

func (d *download) left(p *piece) (pos, end int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return p.pos, p.end
}

// regrow raises a limit that a busy server lowered, one stream at a time.
func (d *download) regrow(ctx context.Context, max int) {
	t := time.NewTicker(growAfter / 6)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, ok := d.gate.grow(max, growAfter); ok && d.onLimit != nil {
				d.onLimit(n)
			}
		}
	}
}

// fetchPiece fetches p with a range request. If-Range makes a server whose
// file changed send the whole new file (200) instead of a piece of it, which
// is then refused.
func (d *download) fetchPiece(ctx context.Context, p *piece) error {
	req, err := d.request(ctx)
	if err != nil {
		return err
	}
	start, end := d.left(p)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end-1))
	if v := Validator(d.res); v != "" {
		req.Header.Set("If-Range", v)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusOK:
		return ErrChanged
	default:
		return probe.NewStatusError(resp)
	}
	got, total, ok := probe.ContentRange(resp.Header.Get("Content-Range"))
	if !ok || got != start || total != d.res.Size {
		return fmt.Errorf("the server sent the wrong part of the file (%q for bytes %d-%d)", resp.Header.Get("Content-Range"), start, end-1)
	}
	if et := resp.Header.Get("ETag"); d.res.ETag != "" && et != "" && et != d.res.ETag {
		return ErrChanged
	}
	// Read until the piece is done. It ends early if another stream took its
	// back half; closing the body then stops the transfer.
	buf := make([]byte, bufSize)
	for {
		n, rerr := io.ReadFull(resp.Body, buf)
		if n > 0 {
			off, k := d.claim(p, n)
			if _, err := d.out.WriteAt(buf[:k], off); err != nil {
				return err
			}
		}
		pos, end := d.left(p)
		if pos == end {
			return nil
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			return fmt.Errorf("%w after %d of %d bytes", ErrDropped, pos-start, end-start)
		}
		if rerr != nil {
			return rerr
		}
	}
}

// whole fetches the file in one request, start to end.
func (d *download) whole(ctx context.Context) error {
	if d.res.Size == 0 {
		return nil
	}
	req, err := d.request(ctx)
	if err != nil {
		return err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return probe.NewStatusError(resp)
	}
	if ce := strings.ToLower(resp.Header.Get("Content-Encoding")); ce != "" && ce != "identity" && d.res.Size >= 0 {
		return fmt.Errorf("the server compressed the file (%s) although it was asked not to", ce)
	}
	if d.res.Size >= 0 && resp.ContentLength >= 0 && resp.ContentLength != d.res.Size {
		return ErrChanged
	}
	return d.copy(resp.Body, 0, d.res.Size)
}

// copy writes body into the file from off. n is the number of bytes
// expected, or -1 to read to the end.
func (d *download) copy(body io.Reader, off, n int64) error {
	if n >= 0 {
		body = io.LimitReader(body, n)
	}
	buf := make([]byte, bufSize)
	var got int64
	for {
		k, err := io.ReadFull(body, buf)
		if k > 0 {
			if _, werr := d.out.WriteAt(buf[:k], off+got); werr != nil {
				return werr
			}
			got += int64(k)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if n >= 0 && got != n {
		return fmt.Errorf("%w after %d of %d bytes", ErrDropped, got, n)
	}
	return nil
}

// Validator tells whether the server's file is still the same one: a strong
// ETag, else Last-Modified, or "" when there is neither. It is sent as
// If-Range; a weak ETag (W/"...") isn't allowed there.
func Validator(res *probe.Result) string {
	if res.ETag != "" && !strings.HasPrefix(res.ETag, "W/") {
		return res.ETag
	}
	return res.LastModified
}

// pieces cuts the missing spans into n pieces to start with: one span is
// split evenly, several are halved, largest first, until there are n.
func pieces(missing []outfile.Range, n int) []*piece {
	if len(missing) == 1 {
		ps := split(missing[0].End-missing[0].Start, n)
		for _, p := range ps {
			p.pos += missing[0].Start
			p.end += missing[0].Start
		}
		return ps
	}
	ps := make([]*piece, len(missing))
	for i, m := range missing {
		ps[i] = &piece{pos: m.Start, end: m.End}
	}
	for len(ps) < n {
		big := 0
		for i, p := range ps {
			if p.end-p.pos > ps[big].end-ps[big].pos {
				big = i
			}
		}
		b := ps[big]
		if b.end-b.pos < 2 {
			break
		}
		mid := b.pos + (b.end-b.pos)/2
		ps = append(ps, &piece{pos: mid, end: b.end})
		b.end = mid
	}
	return ps
}

// split cuts size bytes into n near-equal pieces, none smaller than 1 byte.
func split(size int64, n int) []*piece {
	n = int(min(int64(max(n, 1)), size))
	parts := make([]*piece, n)
	for i := range n {
		parts[i] = &piece{pos: size * int64(i) / int64(n), end: size * int64(i+1) / int64(n)}
	}
	return parts
}
