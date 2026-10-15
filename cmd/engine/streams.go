package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/outfile"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/stream"
)

// workPrefix starts the name of a stream's hidden work folder.
const workPrefix = ".internet-downloader-"

// downloadStream downloads an HLS or DASH stream: each track's segments in
// parallel into a work folder beside the final file, then FFmpeg joins the
// tracks into one file.
func (e *engine) downloadStream(c media.Context) uint64 {
	return e.start(c.URL, nil, func(ctx context.Context, j *job) { e.runStreamJob(ctx, j, c) })
}

func (e *engine) runStreamJob(ctx context.Context, j *job, c media.Context) {
	path, size, err := e.runStream(ctx, j, c)
	if err != nil {
		e.stopped(ctx, j, err)
		return
	}
	e.done(j, path, size)
}

func (e *engine) runStream(ctx context.Context, j *job, c media.Context) (string, int64, error) {
	r := probe.Request{URL: c.URL, Referer: c.Referer, Headers: c.Headers, Cookies: c.Cookies}
	plan, err := stream.Resolve(ctx, c.Kind, c.URL, c.Manifest, e.getText(r))
	if err != nil {
		return "", 0, err
	}
	ext, err := stream.Ext(plan.Tracks, e.ffmpeg != "")
	if err != nil {
		return "", 0, err
	}
	dir, name := e.saveDir(), streamName(c)
	if j.dir != "" {
		dir = j.dir
	}
	if j.name != "" {
		// The file type is the one the tracks need, whatever was typed.
		name = strings.TrimSuffix(j.name, filepath.Ext(j.name))
	}
	out, err := outfile.Create(dir, name+ext, -1)
	if err != nil {
		return "", 0, err
	}
	// Recorded so a crash's leftovers are found and deleted at the next start.
	e.dl.update(j, func(j *job) { j.part = out.Path() })
	if e.jr != nil {
		e.jr.SetFile(j.ID, out.Path(), -1, "")
	}
	kept := false
	defer func() {
		if !kept {
			out.Abort()
		}
	}()
	work, err := os.MkdirTemp(e.dir, workPrefix)
	if err != nil {
		return "", 0, err
	}
	defer os.RemoveAll(work)
	if p, err := windows.UTF16PtrFromString(work); err == nil {
		windows.SetFileAttributes(p, windows.FILE_ATTRIBUTE_HIDDEN)
	}

	parts := 0
	for _, t := range plan.Tracks {
		if t.Single {
			parts++
		} else {
			parts += len(t.Fragments)
		}
	}
	e.dl.update(j, func(j *job) {
		j.Name, j.State, j.Parts, j.Mode = out.Name(), fetch.Downloading, parts, "segments"
		j.progress = func() (int64, int) { return 0, 0 }
	})
	fmt.Fprintf(e.log, "download %d (%s): %s, %d tracks, %d segments\n", j.ID, j.host, c.Kind, len(plan.Tracks), parts)

	var inputs []string
	var bytesBefore int64 // in tracks already finished
	partsBefore := 0
	for i, t := range plan.Tracks {
		b, pb := bytesBefore, partsBefore
		var p string
		var n int64
		if t.Single {
			p, n, err = e.getFile(ctx, j, r.For(t.Fragments[0].URL), work, func(res *probe.Result, f *outfile.File) {
				e.dl.update(j, func(j *job) {
					j.Protocol, j.Streams = res.Protocol, res.Plan.Streams
					j.progress = func() (int64, int) { return b + f.Written(), pb }
				})
			})
			partsBefore++
		} else {
			p = filepath.Join(work, fmt.Sprintf("track%d.%s", i, t.Format))
			n, err = e.getFragments(ctx, j, r, t, p, b, pb)
			partsBefore += len(t.Fragments)
		}
		if err != nil {
			if len(plan.Tracks) > 1 && ctx.Err() == nil {
				err = fmt.Errorf("the %s: %w", t.Kind, err)
			}
			return "", 0, err
		}
		inputs = append(inputs, p)
		bytesBefore += n
	}

	src := inputs[0]
	if e.ffmpeg != "" {
		e.dl.update(j, func(j *job) { j.State = fetch.Joining })
		src = filepath.Join(work, "joined"+ext)
		if err := stream.Mux(ctx, e.ffmpeg, inputs, src); err != nil {
			return "", 0, err
		}
	}
	final, err := out.Replace(src)
	if err != nil {
		return "", 0, err
	}
	kept = true
	return final, out.Written(), nil
}

// getFragments downloads one track's segments into the file p.
func (e *engine) getFragments(ctx context.Context, j *job, r probe.Request, t stream.Track, p string, before int64, partsBefore int) (int64, error) {
	f, err := os.Create(p)
	if err != nil {
		return 0, err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	var prog fetch.Progress
	e.dl.update(j, func(j *job) {
		j.progress = func() (int64, int) { return before + prog.Bytes.Load(), partsBefore + int(prog.Fragments.Load()) }
	})
	err = fetch.Fragments(ctx, r, t.Fragments, w, fetch.FragmentOptions{
		Options:  fetch.Options{RootCAs: e.probe.RootCAs, OnLimit: e.onLimit(j), Limit: e.limit},
		Progress: &prog,
		OnStart: func(protocol string, streams int) {
			e.dl.update(j, func(j *job) { j.Protocol, j.Streams = protocol, streams })
		},
	})
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// getText fetches a playlist the way the browser did.
func (e *engine) getText(r probe.Request) stream.Getter {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: e.probe.RootCAs}
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	return func(ctx context.Context, u string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		probe.SetHeaders(req.Header, r.For(u))
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("the playlist: %w", probe.NewStatusError(resp))
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, media.MaxManifest+1))
		if err != nil {
			return nil, err
		}
		if len(b) > media.MaxManifest {
			return nil, errors.New("the playlist is too large to be a real playlist")
		}
		return b, nil
	}
}

// streamName names a stream's file after its page, else its address.
func streamName(c media.Context) string {
	name := strings.TrimSpace(c.PageTitle)
	if name == "" {
		if u, err := url.Parse(c.URL); err == nil {
			name = strings.TrimSuffix(path.Base(u.Path), path.Ext(u.Path))
		}
		switch strings.ToLower(name) {
		case "", ".", "/", "index", "master", "playlist", "manifest", "dashplaylist", "hlsplaylist":
			name = hostOf(c.URL)
		}
	}
	// Room for " (12).mp4" within outfile's 200-character limit.
	if r := []rune(name); len(r) > 150 {
		name = strings.TrimSpace(string(r[:150]))
	}
	return name
}
