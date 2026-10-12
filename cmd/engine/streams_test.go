package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/testserver"
)

// With FAKE_FFMPEG set, the test binary stands in for FFmpeg: it writes its
// inputs one after another to the output and its arguments to FAKE_FFMPEG,
// or fails if it can't write there.
func TestMain(m *testing.M) {
	if log := os.Getenv("FAKE_FFMPEG"); log != "" {
		args := os.Args[1:]
		if err := os.WriteFile(log, []byte(strings.Join(args, " ")), 0o644); err != nil {
			os.Stderr.WriteString("Invalid argument")
			os.Exit(1)
		}
		var out []byte
		for i, a := range args {
			if a == "-i" {
				b, err := os.ReadFile(args[i+1])
				if err != nil {
					os.Exit(1)
				}
				out = append(out, b...)
			}
		}
		if err := os.WriteFile(args[len(args)-1], out, 0o644); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// streamSite serves an HLS playlist of 12 segments and a Reddit-style DASH
// manifest with one video file and one audio file. Cookies reach only its
// own host.
func streamSite(t *testing.T) (*httptest.Server, []byte, []byte, []byte) {
	segs := testserver.Content(12 * 5000)
	video := testserver.Content(2<<20 + 3)
	audio := testserver.Content(300001)[1:]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("sid"); err != nil || c.Value != "SECRETCOOKIE" {
			http.Error(w, "no cookie", http.StatusForbidden)
			return
		}
		switch p := r.URL.Path; {
		case p == "/v/index.m3u8":
			var b strings.Builder
			b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:4\n")
			for i := range 12 {
				fmt.Fprintf(&b, "#EXTINF:4,\nseg%d.ts\n", i)
			}
			b.WriteString("#EXT-X-ENDLIST\n")
			w.Write([]byte(b.String()))
		case strings.HasPrefix(p, "/v/seg"):
			n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(p, "/v/seg"), ".ts"))
			time.Sleep(time.Duration(12-n) * time.Millisecond) // out of order
			w.Write(segs[n*5000 : (n+1)*5000])
		case p == "/r/DASH_720.mp4":
			http.ServeContent(w, r, "DASH_720.mp4", time.Time{}, bytes.NewReader(video))
		case p == "/r/DASH_AUDIO_128.mp4":
			http.ServeContent(w, r, "DASH_AUDIO_128.mp4", time.Time{}, bytes.NewReader(audio))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, segs, video, audio
}

const redditMPD = `<MPD type="static" mediaPresentationDuration="PT10S"><Period>
<AdaptationSet contentType="video"><Representation id="v" bandwidth="900" mimeType="video/mp4"><BaseURL>DASH_720.mp4</BaseURL></Representation></AdaptationSet>
<AdaptationSet contentType="audio"><Representation id="a" bandwidth="128" mimeType="audio/mp4"><BaseURL>DASH_AUDIO_128.mp4</BaseURL></Representation></AdaptationSet>
</Period></MPD>`

func getStream(t *testing.T, e *engine, c media.Context) fetch.Status {
	t.Helper()
	stopped := make(chan uint64, 1)
	e.finished = func(id uint64) { stopped <- id }
	id := e.inbox.Add("firefox", c, time.Now())
	if _, err := e.startDownload(id); err != nil {
		t.Fatal(err)
	}
	<-stopped
	d := e.dl.list()[0]
	left, _ := os.ReadDir(e.dir)
	for _, f := range left {
		if d.State != fetch.Done || f.Name() != filepath.Base(d.Path) {
			if d.State == fetch.Done || strings.HasPrefix(f.Name(), ".") || strings.HasSuffix(f.Name(), ".part") {
				t.Errorf("left behind: %s", f.Name())
			}
		}
	}
	return d
}

func TestDownloadHLS(t *testing.T) {
	srv, segs, _, _ := streamSite(t)
	var log bytes.Buffer
	e := newEngine(&log)
	e.dir, e.ffmpeg = t.TempDir(), ""
	d := getStream(t, e, media.Context{URL: srv.URL + "/v/index.m3u8?token=SECRETTOKEN", Kind: "hls", PageTitle: "My: clip?",
		Cookies: []media.Cookie{{Name: "sid", Value: "SECRETCOOKIE"}}})
	if d.State != fetch.Done || d.Name != "My_ clip_.ts" || d.Parts != 12 || d.PartsDone != 12 || d.Done != int64(len(segs)) {
		t.Fatalf("%+v", d)
	}
	if got, _ := os.ReadFile(d.Path); !bytes.Equal(got, segs) {
		t.Fatal("segments joined wrongly")
	}
	for _, secret := range []string{"SECRETTOKEN", "SECRETCOOKIE"} {
		if strings.Contains(log.String(), secret) {
			t.Fatalf("%s leaked: %s", secret, log.String())
		}
	}
}

func TestDownloadDASHJoinsTracks(t *testing.T) {
	srv, _, video, audio := streamSite(t)
	e := newEngine(&bytes.Buffer{})
	e.dir = t.TempDir()
	args := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_FFMPEG", args)
	e.ffmpeg, _ = os.Executable()
	d := getStream(t, e, media.Context{URL: srv.URL + "/r/DASHPlaylist.mpd", Kind: "dash", Manifest: redditMPD,
		Cookies: []media.Cookie{{Name: "sid", Value: "SECRETCOOKIE"}}})
	if d.State != fetch.Done || !strings.HasSuffix(d.Name, ".mp4") || d.Parts != 2 {
		t.Fatalf("%+v", d)
	}
	got, _ := os.ReadFile(d.Path)
	if !bytes.Equal(got, append(append([]byte{}, video...), audio...)) {
		t.Fatalf("joined file has %d bytes", len(got))
	}
	a, _ := os.ReadFile(args)
	if !strings.Contains(string(a), "-map 0:v:0 -map 1:a:0 -c copy -movflags +faststart") {
		t.Fatalf("ffmpeg called with %s", a)
	}
}

func TestStreamFailures(t *testing.T) {
	srv, _, _, _ := streamSite(t)
	cookie := []media.Cookie{{Name: "sid", Value: "SECRETCOOKIE"}}
	t.Run("no ffmpeg", func(t *testing.T) {
		e := newEngine(&bytes.Buffer{})
		e.dir, e.ffmpeg = t.TempDir(), ""
		d := getStream(t, e, media.Context{URL: srv.URL + "/r/x.mpd", Kind: "dash", Manifest: redditMPD, Cookies: cookie})
		if d.State != fetch.Failed || !strings.Contains(d.Error, "FFmpeg") {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("ffmpeg fails", func(t *testing.T) {
		e := newEngine(&bytes.Buffer{})
		e.dir = t.TempDir()
		t.Setenv("FAKE_FFMPEG", filepath.Join(t.TempDir(), "no", "such", "dir"))
		e.ffmpeg, _ = os.Executable()
		d := getStream(t, e, media.Context{URL: srv.URL + "/r/x.mpd", Kind: "dash", Manifest: redditMPD, Cookies: cookie})
		if d.State != fetch.Failed || !strings.Contains(d.Error, "FFmpeg") {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("missing segment", func(t *testing.T) {
		e := newEngine(&bytes.Buffer{})
		e.dir, e.ffmpeg = t.TempDir(), ""
		d := getStream(t, e, media.Context{URL: srv.URL + "/v/index.m3u8", Kind: "hls", Cookies: nil})
		if d.State != fetch.Failed || !strings.Contains(d.Error, "403") {
			t.Fatalf("%+v", d)
		}
	})
	t.Run("live", func(t *testing.T) {
		e := newEngine(&bytes.Buffer{})
		e.dir = t.TempDir()
		d := getStream(t, e, media.Context{URL: srv.URL + "/live.m3u8", Kind: "hls", Manifest: "#EXTM3U\n#EXTINF:4,\na.ts\n"})
		if d.State != fetch.Failed || !strings.Contains(d.Error, "live") {
			t.Fatalf("%+v", d)
		}
	})
}

func TestStreamName(t *testing.T) {
	for _, tc := range []struct {
		c    media.Context
		want string
	}{
		{media.Context{URL: "https://v.redd.it/x/DASHPlaylist.mpd", PageTitle: "  A cat  "}, "A cat"},
		{media.Context{URL: "https://v.redd.it/x/DASHPlaylist.mpd"}, "v.redd.it"},
		{media.Context{URL: "https://cdn.com/show/episode-4.m3u8?t=1"}, "episode-4"},
		{media.Context{URL: "https://cdn.com/a.m3u8", PageTitle: strings.Repeat("é", 300)}, strings.Repeat("é", 150)},
	} {
		c, want := tc.c, tc.want
		if got := streamName(c); got != want {
			t.Errorf("%+v: got %q", c, got)
		}
	}
}
