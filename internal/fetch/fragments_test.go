package fetch

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/testserver"
)

// segServer serves /seg/N as the Nth 10 000-byte slice of body. Earlier
// segments answer more slowly, so they complete out of order.
func segServer(t *testing.T, body []byte, h func(n int, w http.ResponseWriter) bool) (*httptest.Server, []Fragment) {
	const segLen = 10000
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/seg/"))
		if err != nil || n*segLen >= len(body) {
			http.NotFound(w, r)
			return
		}
		if h != nil && h(n, w) {
			return
		}
		time.Sleep(time.Duration(10-n%10) * time.Millisecond)
		w.Write(body[n*segLen : min((n+1)*segLen, len(body))])
	}))
	t.Cleanup(srv.Close)
	var frags []Fragment
	for i := 0; i*segLen < len(body); i++ {
		frags = append(frags, Fragment{URL: srv.URL + "/seg/" + strconv.Itoa(i), Len: -1})
	}
	return srv, frags
}

func TestFragmentsArriveInOrder(t *testing.T) {
	body := testserver.Content(456789)
	srv, frags := segServer(t, body, nil)
	var buf bytes.Buffer
	var p Progress
	var proto string
	err := Fragments(context.Background(), probe.Request{URL: srv.URL}, frags, &buf, FragmentOptions{
		Progress: &p, OnStart: func(pr string, _ int) { proto = pr },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), body) {
		t.Fatalf("content differs (%d bytes)", buf.Len())
	}
	if p.Bytes.Load() != int64(len(body)) || p.Fragments.Load() != int64(len(frags)) || proto != probe.HTTP1 {
		t.Fatalf("progress %d bytes, %d segments, %s", p.Bytes.Load(), p.Fragments.Load(), proto)
	}
}

// Byte ranges of one file over HTTP/3, the way fMP4 HLS uses EXT-X-BYTERANGE.
func TestFragmentRangesOverHTTP3(t *testing.T) {
	s := testserver.Start(t, testserver.Content(size))
	var frags []Fragment
	for at := int64(0); at < size; at += 100000 {
		frags = append(frags, Fragment{URL: s.H2, Start: at, Len: min(100000, size-at)})
	}
	var buf bytes.Buffer
	var proto string
	err := Fragments(context.Background(), probe.Request{URL: s.H2}, frags, &buf, FragmentOptions{
		Options: Options{RootCAs: s.RootCAs}, OnStart: func(pr string, _ int) { proto = pr },
	})
	if err != nil || !bytes.Equal(buf.Bytes(), s.Body) || proto != probe.HTTP3 {
		t.Fatalf("err %v, %d bytes, %s", err, buf.Len(), proto)
	}
}

func TestEncryptedFragments(t *testing.T) {
	key := []byte("0123456789abcdef")
	plain := testserver.Content(50000)
	var enc [][]byte
	var ivs [][]byte
	for i := range 5 {
		iv := make([]byte, 16)
		iv[15] = byte(i)
		seg := plain[i*10000 : (i+1)*10000]
		pad := 16 - len(seg)%16
		b := append(append([]byte{}, seg...), bytes.Repeat([]byte{byte(pad)}, pad)...)
		c, _ := aes.NewCipher(key)
		cipher.NewCBCEncrypter(c, iv).CryptBlocks(b, b)
		enc, ivs = append(enc, b), append(ivs, iv)
	}
	var keyFetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/key" {
			keyFetches.Add(1)
			w.Write(key)
			return
		}
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/seg/"))
		w.Write(enc[n])
	}))
	defer srv.Close()
	var frags []Fragment
	for i := range 5 {
		frags = append(frags, Fragment{URL: srv.URL + "/seg/" + strconv.Itoa(i), Len: -1, Key: &Key{URL: srv.URL + "/key", IV: ivs[i]}})
	}
	var buf bytes.Buffer
	if err := Fragments(context.Background(), probe.Request{URL: srv.URL}, frags, &buf, FragmentOptions{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), plain) || keyFetches.Load() != 1 {
		t.Fatalf("%d bytes, key fetched %d times", buf.Len(), keyFetches.Load())
	}
	if _, err := DecryptAES128([]byte("fedcba9876543210"), ivs[0], append([]byte{}, enc[0]...)); err == nil {
		t.Fatal("a wrong key decrypted")
	}
}

func TestFragmentFailures(t *testing.T) {
	body := testserver.Content(100000)
	t.Run("missing segment", func(t *testing.T) {
		srv, frags := segServer(t, body, func(n int, w http.ResponseWriter) bool {
			if n == 6 {
				w.WriteHeader(http.StatusNotFound)
				return true
			}
			return false
		})
		err := Fragments(context.Background(), probe.Request{URL: srv.URL}, frags, &bytes.Buffer{}, FragmentOptions{})
		if err == nil || !strings.Contains(err.Error(), "segment 7 of 10") || !strings.Contains(err.Error(), "404") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("server error is tried again", func(t *testing.T) {
		var failed atomic.Bool
		srv, frags := segServer(t, body, func(n int, w http.ResponseWriter) bool {
			if n == 3 && !failed.Swap(true) {
				w.WriteHeader(http.StatusBadGateway)
				return true
			}
			return false
		})
		var buf bytes.Buffer
		if err := Fragments(context.Background(), probe.Request{URL: srv.URL}, frags, &buf, FragmentOptions{}); err != nil || !bytes.Equal(buf.Bytes(), body) {
			t.Fatalf("err %v, %d bytes", err, buf.Len())
		}
	})
	t.Run("cancel", func(t *testing.T) {
		srv, frags := segServer(t, body, func(n int, w http.ResponseWriter) bool {
			if n == 2 {
				time.Sleep(2 * time.Second)
			}
			return false
		})
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := Fragments(ctx, probe.Request{URL: srv.URL}, frags, &bytes.Buffer{}, FragmentOptions{})
		if err == nil || time.Since(start) > time.Second {
			t.Fatalf("got %v after %v", err, time.Since(start))
		}
	})
}

func TestOrderedWindow(t *testing.T) {
	var buf bytes.Buffer
	o := newOrdered(&buf, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := o.wait(ctx, 2); err == nil {
		t.Fatal("started a segment beyond the window")
	}
	o.put(1, []byte("b"))
	o.put(0, []byte("a"))
	if err := o.wait(context.Background(), 3); err != nil || buf.String() != "ab" {
		t.Fatalf("%v %q", err, buf.String())
	}
}
