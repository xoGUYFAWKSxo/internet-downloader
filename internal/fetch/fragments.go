package fetch

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
)

// maxFragment caps one segment held in memory. Real segments are a few MB.
const maxFragment = 256 << 20

// Fragment is one piece of an HLS or DASH track: a whole resource, or a byte
// range of one.
type Fragment struct {
	URL   string
	Start int64
	Len   int64 // -1: the whole resource
	Key   *Key  // AES-128 key; nil when the segment is clear
}

// Key is an HLS AES-128 key that the page itself serves.
type Key struct {
	URL string
	IV  []byte // 16 bytes
}

// Progress counts what a fragment download has written so far.
type Progress struct {
	Bytes     atomic.Int64
	Fragments atomic.Int64
}

// FragmentOptions adds to Options what a fragment download needs.
type FragmentOptions struct {
	Options
	Progress *Progress
	// OnStart hears the protocol chosen and the parallel requests planned.
	OnStart func(protocol string, streams int)
	// OnWritten, if set, is called each time more segments have been
	// written to w, with how many are written in all. Nothing is written to
	// w while it runs, so it can flush w and record the count.
	OnWritten func(written int) error
}

// Fragments fetches frags in parallel and writes them to w in order. The
// first segment's server is probed to choose the protocol, as for a file.
func Fragments(ctx context.Context, r probe.Request, frags []Fragment, w io.Writer, o FragmentOptions) error {
	if len(frags) == 0 {
		return errors.New("the stream has no segments")
	}
	if o.RetryWait == 0 {
		o.RetryWait = 5 * time.Second
	}
	if o.Progress == nil {
		o.Progress = new(Progress)
	}
	pctx, pcancel := context.WithTimeout(ctx, 30*time.Second)
	res, err := probe.Probe(pctx, r.For(frags[0].URL), probe.Options{RootCAs: o.RootCAs})
	pcancel()
	if err != nil {
		return fmt.Errorf("segment 1: %w", err)
	}
	h3 := res.Protocol == probe.HTTP3 && sameHost(frags, res.URL)
	if !h3 && res.Protocol == probe.HTTP3 {
		res.Protocol = probe.HTTP2 // TCP for segments on other hosts
	}
	streams, conns := probe.MaxStreams, 1
	if res.Protocol == probe.HTTP1 {
		streams, conns = probe.MaxConnections, probe.MaxConnections
	}
	if res.Protocol != probe.HTTP3 {
		// Allow both over TCP: segment hosts can differ from the probed one.
		conns = probe.MaxConnections
	}
	res.Plan = probe.Plan{Streams: streams, Connections: conns}
	if o.OnStart != nil {
		o.OnStart(res.Protocol, streams)
	}
	client, closeIdle := newClient(res, o.Options)
	defer closeIdle()
	f := &fragments{
		gate: newGate(streams), o: o, client: client, req: r, frags: frags,
		ow: newOrdered(w, 2*streams), keys: map[string][]byte{},
	}
	f.ow.onWritten = o.OnWritten
	if h3 {
		f.h3Addr = res.H3Addr
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	d := &download{gate: f.gate, onLimit: o.OnLimit}
	go d.regrow(ctx, streams)
	var wg sync.WaitGroup
	for range streams {
		wg.Go(func() {
			if err := f.worker(ctx); err != nil {
				cancel(err)
			}
		})
	}
	wg.Wait()
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if f.ow.next != len(frags) {
		return fmt.Errorf("only %d of %d segments were written", f.ow.next, len(frags))
	}
	return nil
}

func sameHost(frags []Fragment, probed string) bool {
	pu, err := url.Parse(probed)
	if err != nil {
		return false
	}
	for _, fr := range frags {
		u, err := url.Parse(fr.URL)
		if err != nil || u.Host != pu.Host {
			return false
		}
	}
	return true
}

type fragments struct {
	gate   *gate
	o      FragmentOptions
	client *http.Client
	req    probe.Request
	frags  []Fragment
	h3Addr string
	ow     *ordered
	taken  atomic.Int64

	keyMu sync.Mutex
	keys  map[string][]byte
}

func (f *fragments) worker(ctx context.Context) error {
	n := len(f.frags)
	for {
		i := int(f.taken.Add(1) - 1)
		if i >= n {
			return nil
		}
		if err := f.ow.wait(ctx, i); err != nil {
			return err
		}
		b, err := f.get(ctx, f.frags[i])
		if err != nil {
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			return fmt.Errorf("segment %d of %d: %w", i+1, n, err)
		}
		if err := f.ow.put(i, b); err != nil {
			return err
		}
		f.o.Progress.Fragments.Add(1)
	}
}

// get fetches one fragment. Busy refusals lower the limit and wait as for a
// file; a dropped connection or a server error is tried twice more.
func (f *fragments) get(ctx context.Context, fr Fragment) ([]byte, error) {
	for refusals, tries := 0, 0; ; {
		if err := f.gate.acquire(ctx); err != nil {
			return nil, err
		}
		b, err := f.fetch(ctx, fr)
		var se *probe.StatusError
		if errors.As(err, &se) && (se.Code == http.StatusTooManyRequests || se.Code == http.StatusServiceUnavailable) {
			if n := f.gate.refused(); f.o.OnLimit != nil {
				f.o.OnLimit(n)
			}
			wait := se.RetryAfter
			if wait == 0 {
				wait = f.o.RetryWait
			}
			if refusals++; refusals > maxRefusals || wait > maxRetryWait {
				return nil, err
			}
			if err := sleep(ctx, wait); err != nil {
				return nil, err
			}
			continue
		}
		f.gate.release()
		if err == nil {
			if fr.Key != nil {
				return f.decrypt(ctx, fr, b)
			}
			return b, nil
		}
		if !transient(err) || tries == 2 || ctx.Err() != nil {
			return nil, err
		}
		tries++
		if err := sleep(ctx, time.Duration(tries)*time.Second); err != nil {
			return nil, err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// transient errors are worth another try: dropped connections and 5xx.
func transient(err error) bool {
	var se *probe.StatusError
	if errors.As(err, &se) {
		return se.Code >= 500
	}
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, errShort)
}

var errShort = errors.New("the connection ended before the segment was complete")

func (f *fragments) fetch(ctx context.Context, fr Fragment) ([]byte, error) {
	b, err := f.read(ctx, fr.URL, fr.Start, fr.Len)
	if err != nil {
		f.o.Progress.Bytes.Add(-int64(len(b)))
	}
	return b, err
}

// read GETs u, or bytes [start, start+n) of it. It counts bytes into
// Progress as they arrive and returns what it read even on error.
func (f *fragments) read(ctx context.Context, u string, start, n int64) ([]byte, error) {
	target := u
	if f.h3Addr != "" {
		pu, err := url.Parse(u)
		if err != nil {
			return nil, err
		}
		pu.Host = f.h3Addr
		target = pu.String()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	probe.SetHeaders(req.Header, f.req.For(u))
	if n >= 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, start+n-1))
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	skip := int64(0)
	switch {
	case resp.StatusCode == http.StatusPartialContent && n >= 0:
		if got, _, ok := probe.ContentRange(resp.Header.Get("Content-Range")); !ok || got != start {
			return nil, fmt.Errorf("the server sent the wrong part of the segment (%q)", resp.Header.Get("Content-Range"))
		}
	case resp.StatusCode == http.StatusOK:
		skip = max(start, 0) // a server that ignores Range sends it all
		if n < 0 {
			skip = 0
		}
	default:
		return nil, probe.NewStatusError(resp)
	}
	limit := int64(maxFragment)
	if n >= 0 {
		limit = n
	}
	if skip > 0 {
		if _, err := io.CopyN(io.Discard, resp.Body, skip); err != nil {
			return nil, errShort
		}
	}
	b := make([]byte, 0, min(max(resp.ContentLength, 0), limit)+1)
	buf := make([]byte, 64<<10)
	for {
		k, rerr := resp.Body.Read(buf[:f.o.Limit.chunk(len(buf))])
		if int64(len(b)+k) > limit {
			if n < 0 {
				return b, errors.New("a segment is larger than 256 MB, which no real segment is")
			}
			k = int(limit) - len(b)
			rerr = io.EOF
		}
		b = append(b, buf[:k]...)
		f.o.Progress.Bytes.Add(int64(k))
		if err := f.o.Limit.wait(ctx, k); err != nil {
			return b, err
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return b, rerr
		}
	}
	if n >= 0 && int64(len(b)) != n {
		return b, errShort
	}
	return b, nil
}

// decrypt undoes HLS AES-128: CBC with PKCS#7 padding, the key fetched once
// from the page's own server.
func (f *fragments) decrypt(ctx context.Context, fr Fragment, b []byte) ([]byte, error) {
	key, err := f.key(ctx, fr.Key.URL)
	if err != nil {
		return nil, err
	}
	return DecryptAES128(key, fr.Key.IV, b)
}

func (f *fragments) key(ctx context.Context, u string) ([]byte, error) {
	f.keyMu.Lock()
	defer f.keyMu.Unlock()
	if k, ok := f.keys[u]; ok {
		return k, nil
	}
	k, err := f.read(ctx, u, 0, -1)
	f.o.Progress.Bytes.Add(-int64(len(k))) // not part of the video
	if err != nil {
		return nil, fmt.Errorf("the decryption key: %w", err)
	}
	if len(k) != 16 {
		return nil, fmt.Errorf("the decryption key is %d bytes, not 16", len(k))
	}
	f.keys[u] = k
	return k, nil
}

func DecryptAES128(key, iv, b []byte) ([]byte, error) {
	if len(b) == 0 || len(b)%aes.BlockSize != 0 || len(iv) != aes.BlockSize {
		return nil, errors.New("an encrypted segment has the wrong length")
	}
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	cipher.NewCBCDecrypter(c, iv).CryptBlocks(b, b)
	pad := int(b[len(b)-1])
	if pad == 0 || pad > aes.BlockSize {
		return nil, errors.New("a segment didn't decrypt with the page's key")
	}
	for _, x := range b[len(b)-pad:] {
		if int(x) != pad {
			return nil, errors.New("a segment didn't decrypt with the page's key")
		}
	}
	return b[:len(b)-pad], nil
}

// ordered writes fragments in index order as they arrive out of order. A
// stream may not start fragment i until i < next+window, which bounds the
// memory held by fragments waiting for a slow earlier one.
type ordered struct {
	mu      sync.Mutex
	w       io.Writer
	next    int
	window  int
	pending map[int][]byte
	wake    chan struct{}
	// onWritten hears next each time it moves.
	onWritten func(int) error
}

func newOrdered(w io.Writer, window int) *ordered {
	return &ordered{w: w, window: window, pending: map[int][]byte{}, wake: make(chan struct{})}
}

func (o *ordered) wait(ctx context.Context, i int) error {
	for {
		o.mu.Lock()
		if i < o.next+o.window {
			o.mu.Unlock()
			return nil
		}
		wake := o.wake
		o.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (o *ordered) put(i int, b []byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pending[i] = b
	moved := false
	for {
		b, ok := o.pending[o.next]
		if !ok {
			break
		}
		if _, err := o.w.Write(b); err != nil {
			return err
		}
		delete(o.pending, o.next)
		o.next++
		moved = true
	}
	if moved {
		close(o.wake)
		o.wake = make(chan struct{})
		if o.onWritten != nil {
			return o.onWritten(o.next)
		}
	}
	return nil
}
