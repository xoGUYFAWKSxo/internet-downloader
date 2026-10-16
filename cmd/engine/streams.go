package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
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

// streamState is what resuming a stream needs. It is kept in the saved
// list, cookies included like a file's request, until the stream ends.
type streamState struct {
	Media media.Context `json:"media"`
	// Work is the folder the tracks download into; "" until it is made.
	Work string `json:"work,omitempty"`
	// Plan fingerprints the tracks in Work: when the playlist lists other
	// segments by the time it resumes, the stream starts over.
	Plan   string       `json:"plan,omitempty"`
	Tracks []trackState `json:"tracks,omitempty"`
}

// trackState is how far one track got. Segments and Bytes are saved only
// once those bytes are flushed to the disk.
type trackState struct {
	File     string `json:"file,omitempty"`
	Segments int    `json:"segments,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
	Done     bool   `json:"done,omitempty"`
}

// saved gives the bytes and segments a resumed stream already has.
func (s *streamState) saved() (int64, int) {
	var n int64
	parts := 0
	for _, t := range s.Tracks {
		n += t.Bytes
		parts += t.Segments
	}
	return n, parts
}

// downloadStream downloads an HLS or DASH stream: each track's segments in
// parallel into a work folder, then FFmpeg joins the tracks into one file.
func (e *engine) downloadStream(c media.Context) uint64 {
	return e.startJob(c.URL, nil, func(j *job) { j.referrer, j.stream = c.Referer, &streamState{Media: c} }, e.runStreamJob)
}

func (e *engine) runStreamJob(ctx context.Context, j *job) {
	path, size, err := e.runStream(ctx, j)
	if err != nil {
		e.stopped(ctx, j, err)
		return
	}
	e.done(j, path, size)
}

func (e *engine) runStream(ctx context.Context, j *job) (string, int64, error) {
	e.dl.mu.Lock()
	c := j.stream.Media
	e.dl.mu.Unlock()
	r := request(c)
	plan, err := stream.Resolve(ctx, c.Kind, c.URL, c.Manifest, e.getText(r))
	if err != nil {
		return "", 0, err
	}
	ext, err := stream.Ext(plan.Tracks, e.ffmpeg != "")
	if err != nil {
		return "", 0, err
	}
	dir, name := e.dirFor(streamName(c)+ext), streamName(c)
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
	work, tracks, err := e.workFolder(j, plan)
	if err != nil {
		return "", 0, err
	}

	parts := 0
	for _, t := range plan.Tracks {
		parts += segments(t)
	}
	had, hadParts := (&streamState{Tracks: tracks}).saved()
	e.dl.update(j, func(j *job) {
		j.Name, j.State, j.Parts, j.Mode = out.Name(), fetch.Downloading, parts, "segments"
		j.progress = func() (int64, int) { return had, hadParts }
	})
	resumed := ""
	if hadParts > 0 {
		resumed = fmt.Sprintf(", resuming with %d already in", hadParts)
	}
	fmt.Fprintf(e.log, "download %d (%s): %s, %d tracks, %d segments%s\n", j.ID, j.host, c.Kind, len(plan.Tracks), parts, resumed)

	var inputs []string
	var bytesBefore int64 // in tracks already finished
	partsBefore := 0
	for i, t := range plan.Tracks {
		b, pb := bytesBefore, partsBefore
		if ts := tracks[i]; ts.Done && sizeIs(ts.File, ts.Bytes) {
			inputs = append(inputs, ts.File)
			bytesBefore += ts.Bytes
			partsBefore += segments(t)
			continue
		}
		var p string
		var n int64
		if t.Single {
			p, n, err = e.getFile(ctx, j, r.For(t.Fragments[0].URL), work, func(res *probe.Result, f *outfile.File) {
				e.dl.update(j, func(j *job) {
					j.Protocol, j.Streams = res.Protocol, res.Plan.Streams
					j.progress = func() (int64, int) { return b + f.Written(), pb }
				})
			})
		} else {
			p = filepath.Join(work, fmt.Sprintf("track%d.%s", i, t.Format))
			n, err = e.getFragments(ctx, j, r, t, i, p, b, pb)
		}
		if err != nil {
			if len(plan.Tracks) > 1 && ctx.Err() == nil {
				err = fmt.Errorf("the %s: %w", t.Kind, err)
			}
			return "", 0, err
		}
		e.setTrack(j, i, trackState{File: p, Segments: segments(t), Bytes: n, Done: true})
		inputs = append(inputs, p)
		bytesBefore += n
		partsBefore += segments(t)
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
	e.dl.mu.Lock()
	work = takeWork(j)
	e.dl.mu.Unlock()
	removeWork(work)
	return final, out.Written(), nil
}

func segments(t stream.Track) int {
	if t.Single {
		return 1
	}
	return len(t.Fragments)
}

func sizeIs(p string, n int64) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Size() == n
}

// workFolder gives the folder j's tracks download into and how far each
// got there, starting over when the folder is gone or the stream changed.
func (e *engine) workFolder(j *job, plan *stream.Plan) (string, []trackState, error) {
	fp := planPrint(plan)
	e.dl.mu.Lock()
	work, was, tracks := j.stream.Work, j.stream.Plan, slices.Clone(j.stream.Tracks)
	e.dl.mu.Unlock()
	if work != "" {
		fi, err := os.Stat(work)
		switch {
		case err != nil || !fi.IsDir():
			fmt.Fprintf(e.log, "download %d (%s): its downloaded segments are gone; starting over\n", j.ID, j.host)
		case was != fp || len(tracks) != len(plan.Tracks):
			fmt.Fprintf(e.log, "download %d (%s): the stream lists other segments now; starting over\n", j.ID, j.host)
		default:
			return work, tracks, nil
		}
		e.dl.mu.Lock()
		work = takeWork(j)
		e.dl.mu.Unlock()
		removeWork(work)
	}
	work, err := os.MkdirTemp(e.dir, workPrefix)
	if err != nil {
		return "", nil, err
	}
	if p, err := windows.UTF16PtrFromString(work); err == nil {
		windows.SetFileAttributes(p, windows.FILE_ATTRIBUTE_HIDDEN)
	}
	tracks = make([]trackState, len(plan.Tracks))
	e.dl.update(j, func(j *job) { j.stream.Work, j.stream.Plan, j.stream.Tracks = work, fp, slices.Clone(tracks) })
	e.saveStream(j)
	return work, tracks, nil
}

// planPrint fingerprints what a plan downloads. The query is left out, as
// sites put tokens there that change from one visit to the next.
func planPrint(p *stream.Plan) string {
	h := sha256.New()
	for _, t := range p.Tracks {
		fmt.Fprintf(h, "%s|%s|%v|%d\n", t.Kind, t.Format, t.Single, len(t.Fragments))
		for _, f := range t.Fragments {
			key := ""
			if f.Key != nil {
				key = hex.EncodeToString(f.Key.IV)
			}
			fmt.Fprintf(h, "%s|%d|%d|%s\n", bareURL(f.URL), f.Start, f.Len, key)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func bareURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u.String()
}

// setTrack records how far track i got.
func (e *engine) setTrack(j *job, i int, ts trackState) {
	e.dl.update(j, func(j *job) {
		if i < len(j.stream.Tracks) {
			j.stream.Tracks[i] = ts
		}
	})
	e.saveStream(j)
}

// saveStream writes j's stream state to the saved list.
func (e *engine) saveStream(j *job) {
	if e.jr == nil {
		return
	}
	e.dl.mu.Lock()
	b, err := json.Marshal(j.stream)
	e.dl.mu.Unlock()
	if err == nil {
		err = e.jr.SetStream(j.ID, string(b))
	}
	if err != nil {
		fmt.Fprintf(e.log, "download %d (%s): couldn't save progress: %v\n", j.ID, j.host, err)
	}
}

// takeWork forgets j's work folder and gives it, to delete with removeWork
// once e.dl.mu is unlocked. The caller holds e.dl.mu.
func takeWork(j *job) string {
	if j.stream == nil {
		return ""
	}
	w := j.stream.Work
	j.stream.Work, j.stream.Plan, j.stream.Tracks = "", "", nil
	return w
}

// removeWork deletes a stream's work folder. Only a folder named like one
// is deleted, whatever the saved list says.
func removeWork(w string) {
	if w != "" && strings.HasPrefix(filepath.Base(w), workPrefix) {
		os.RemoveAll(w)
	}
}

// getFragments downloads track i's segments into the file p, carrying on
// after the segments saved from an earlier run.
func (e *engine) getFragments(ctx context.Context, j *job, r probe.Request, t stream.Track, i int, p string, before int64, partsBefore int) (int64, error) {
	e.dl.mu.Lock()
	ts := j.stream.Tracks[i]
	e.dl.mu.Unlock()
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return 0, err
	}
	from, have := 0, int64(0)
	if fi, err := f.Stat(); err == nil && ts.File == p && ts.Segments <= len(t.Fragments) && fi.Size() >= ts.Bytes {
		from, have = ts.Segments, ts.Bytes
	}
	// Bytes after the last saved segment may be half a segment.
	if err := f.Truncate(have); err != nil {
		f.Close()
		return 0, err
	}
	if _, err := f.Seek(have, io.SeekStart); err != nil {
		f.Close()
		return 0, err
	}
	if from == len(t.Fragments) {
		return have, f.Close()
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	w := &countWriter{w: bw}
	var prog fetch.Progress
	e.dl.update(j, func(j *job) {
		j.progress = func() (int64, int) {
			return before + have + prog.Bytes.Load(), partsBefore + from + int(prog.Fragments.Load())
		}
	})
	written, last := 0, time.Now()
	save := func() error {
		if err := bw.Flush(); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
		e.setTrack(j, i, trackState{File: p, Segments: from + written, Bytes: have + w.n})
		return nil
	}
	err = fetch.Fragments(ctx, r, t.Fragments[from:], w, fetch.FragmentOptions{
		Options:  fetch.Options{RootCAs: e.probe.RootCAs, OnLimit: e.onLimit(j), Limit: e.limiter(j)},
		Progress: &prog,
		OnStart: func(protocol string, streams int) {
			e.dl.update(j, func(j *job) { j.Protocol, j.Streams = protocol, streams })
		},
		OnWritten: func(n int) error {
			written = n
			if e.jr == nil || time.Since(last) < e.checkpointEvery {
				return nil
			}
			last = time.Now()
			return save()
		},
	})
	// What was written is kept for a resume, finished or not.
	if serr := save(); err == nil {
		err = serr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	return have + w.n, nil
}

// countWriter counts what goes through it.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
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
