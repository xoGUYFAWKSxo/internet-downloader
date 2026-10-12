// Package fetch downloads a probed file into an outfile.File, following the
// probe's plan: one request, several HTTP/1.1 connections with one range
// each, or several ranges multiplexed on one HTTP/2 or HTTP/3 connection.
//
// The split is fixed for now: the file is cut into as many equal parts as the
// plan has streams. Phase 4 measures the line and adjusts; Phase 5 retries
// and resumes.
package fetch

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

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
	// The first failure stops the other parts.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var wg sync.WaitGroup
	for _, p := range split(res.Size, res.Plan.Streams) {
		wg.Go(func() {
			if err := d.part(ctx, p); err != nil {
				cancel(err)
			}
		})
	}
	wg.Wait()
	if err := context.Cause(ctx); err != nil {
		return err
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
}

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

// part fetches [p.Start, p.End) with a range request. If-Range makes a server
// whose file changed send the whole new file (200) instead of a piece of it,
// which is then refused.
// part fetches p when the gate lets it. A refusal for being busy (429, 503)
// waits as long as the server asks and tries again with fewer parts in
// flight; any other error ends the download.
func (d *download) part(ctx context.Context, p outfile.Range) error {
	for refusals := 0; ; refusals++ {
		if err := d.gate.acquire(ctx); err != nil {
			return err
		}
		err := d.fetchPart(ctx, p)
		var se *probe.StatusError
		if !errors.As(err, &se) || (se.Code != http.StatusTooManyRequests && se.Code != http.StatusServiceUnavailable) {
			d.gate.release()
			return err
		}
		if n := d.gate.refused(); d.onLimit != nil {
			d.onLimit(n)
		}
		wait := se.RetryAfter
		if wait == 0 {
			wait = d.retryWait
		}
		if refusals == maxRefusals || wait > maxRetryWait {
			return err
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (d *download) fetchPart(ctx context.Context, p outfile.Range) error {
	req, err := d.request(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", p.Start, p.End-1))
	if v := ifRange(d.res); v != "" {
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
	start, total, ok := probe.ContentRange(resp.Header.Get("Content-Range"))
	if !ok || start != p.Start || total != d.res.Size {
		return fmt.Errorf("the server sent the wrong part of the file (%q for bytes %d-%d)", resp.Header.Get("Content-Range"), p.Start, p.End-1)
	}
	if et := resp.Header.Get("ETag"); d.res.ETag != "" && et != "" && et != d.res.ETag {
		return ErrChanged
	}
	return d.copy(resp.Body, p.Start, p.End-p.Start)
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
		return fmt.Errorf("the connection ended after %d of %d bytes", got, n)
	}
	return nil
}

// ifRange is the validator for If-Range: a strong ETag, else Last-Modified.
// A weak ETag (W/"...") isn't allowed there.
func ifRange(res *probe.Result) string {
	if res.ETag != "" && !strings.HasPrefix(res.ETag, "W/") {
		return res.ETag
	}
	return res.LastModified
}

// split cuts size bytes into n near-equal parts, none smaller than 1 byte.
func split(size int64, n int) []outfile.Range {
	n = int(min(int64(max(n, 1)), size))
	parts := make([]outfile.Range, n)
	for i := range n {
		parts[i] = outfile.Range{Start: size * int64(i) / int64(n), End: size * int64(i+1) / int64(n)}
	}
	return parts
}
