package stream

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
)

const master = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",DEFAULT=NO,AUTOSELECT=YES,URI="audio/en.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Main",DEFAULT=YES,URI="audio/main.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2",AUDIO="aud"
low/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2",AUDIO="aud"
https://cdn.example.com/high/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=9000000,CODECS="mp4a.40.2"
audio-only.m3u8
#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=90000,URI="iframes.m3u8"
`

const mediaPlaylist = `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:7
#EXT-X-MAP:URI="init.mp4",BYTERANGE="720@0"
#EXTINF:6.0,
#EXT-X-BYTERANGE:1000@720
media.mp4
#EXTINF:6.0,
#EXT-X-BYTERANGE:2000
media.mp4
#EXT-X-KEY:METHOD=AES-128,URI="/keys/k1"
#EXTINF:6.0,
seg3.m4s?token=abc
#EXT-X-KEY:METHOD=AES-128,URI="k2",IV=0x000102030405060708090A0B0C0D0E0F
#EXTINF:6.0,
seg4.m4s
#EXT-X-KEY:METHOD=NONE
#EXTINF:6.0,
seg5.m4s
#EXT-X-ENDLIST
`

func TestHLSMaster(t *testing.T) {
	asked := map[string]bool{}
	get := func(_ context.Context, u string) ([]byte, error) {
		asked[u] = true
		return []byte("#EXTM3U\n#EXTINF:4,\nsegment0.ts\n#EXT-X-ENDLIST\n"), nil
	}
	p, err := Resolve(context.Background(), "hls", "https://site.com/v/master.m3u8", master, get)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tracks) != 2 || p.Tracks[0].Kind != Video || p.Tracks[1].Kind != Audio {
		t.Fatalf("tracks %+v", p.Tracks)
	}
	v := p.Tracks[0]
	if v.Height != 1080 || v.Fragments[0].URL != "https://cdn.example.com/high/segment0.ts" || v.Format != "ts" {
		t.Fatalf("video %+v", v)
	}
	if !asked["https://site.com/v/audio/main.m3u8"] || asked["https://site.com/v/audio/en.m3u8"] {
		t.Fatalf("audio playlists fetched: %v", asked)
	}
}

func TestHLSMedia(t *testing.T) {
	p, err := Resolve(context.Background(), "hls", "https://site.com/v/index.m3u8", mediaPlaylist, nil)
	if err != nil {
		t.Fatal(err)
	}
	tr := p.Tracks[0]
	if tr.Kind != Both || tr.Format != "mp4" || len(tr.Fragments) != 6 {
		t.Fatalf("%+v", tr)
	}
	f := tr.Fragments
	want := []struct {
		url        string
		start, len int64
	}{
		{"https://site.com/v/init.mp4", 0, 720},
		{"https://site.com/v/media.mp4", 720, 1000},
		{"https://site.com/v/media.mp4", 1720, 2000}, // continues the last range
		{"https://site.com/v/seg3.m4s?token=abc", 0, -1},
		{"https://site.com/v/seg4.m4s", 0, -1},
		{"https://site.com/v/seg5.m4s", 0, -1},
	}
	for i, w := range want {
		if f[i].URL != w.url || f[i].Start != w.start || f[i].Len != w.len {
			t.Errorf("fragment %d: %+v", i, f[i])
		}
	}
	// Sequence 7 + 2 segments before it: IV is 9.
	if k := f[3].Key; k == nil || k.URL != "https://site.com/keys/k1" || k.IV[15] != 9 || k.IV[0] != 0 {
		t.Errorf("key %+v", f[3].Key)
	}
	if k := f[4].Key; k == nil || k.IV[1] != 1 || k.IV[15] != 15 {
		t.Errorf("explicit IV %+v", f[4].Key)
	}
	if f[5].Key != nil || f[0].Key != nil {
		t.Error("clear segments got a key")
	}
}

func TestHLSRefusals(t *testing.T) {
	cases := map[string]struct {
		text string
		want error
	}{
		"live":       {"#EXTM3U\n#EXTINF:4,\na.ts\n", ErrLive},
		"sample-aes": {"#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"k\"\n#EXTINF:4,\na.ts\n#EXT-X-ENDLIST\n", media.ErrDRM},
		"keyformat":  {"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\",KEYFORMAT=\"com.apple.streamingkeydelivery\"\n#EXTINF:4,\na.ts\n#EXT-X-ENDLIST\n", media.ErrDRM},
	}
	for name, c := range cases {
		if _, err := Resolve(context.Background(), "hls", "https://s/x.m3u8", c.text, nil); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if _, err := Resolve(context.Background(), "hls", "https://s/x.m3u8", "#EXTM3U\n#EXT-X-ENDLIST\n", nil); err == nil {
		t.Error("empty playlist accepted")
	}
}

func TestAttrs(t *testing.T) {
	a := attrs(`BANDWIDTH=1,CODECS="avc1.64001f,mp4a.40.2",URI="a,b.m3u8",DEFAULT=YES`)
	if a["BANDWIDTH"] != "1" || a["CODECS"] != "avc1.64001f,mp4a.40.2" || a["URI"] != "a,b.m3u8" || a["DEFAULT"] != "YES" {
		t.Fatal(a)
	}
}

const dashTemplate = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT0H0M10.5S">
 <BaseURL>https://cdn.site.com/media/</BaseURL>
 <Period>
  <AdaptationSet contentType="video" mimeType="video/mp4">
   <SegmentTemplate timescale="1000" duration="4000" startNumber="1" media="$RepresentationID$/seg-$Number%05d$.m4s" initialization="$RepresentationID$/init.mp4"/>
   <Representation id="v480" bandwidth="1000000" width="854" height="480"/>
   <Representation id="v1080" bandwidth="4000000" width="1920" height="1080"/>
  </AdaptationSet>
  <AdaptationSet mimeType="audio/mp4">
   <Role schemeIdUri="urn:mpeg:dash:role:2011" value="main"/>
   <Representation id="a128" bandwidth="128000">
    <SegmentTemplate timescale="48000" media="audio/$Time$.m4s" initialization="audio/init.mp4">
     <SegmentTimeline><S t="0" d="96000" r="2"/><S d="48000" r="-1"/></SegmentTimeline>
    </SegmentTemplate>
   </Representation>
  </AdaptationSet>
  <AdaptationSet mimeType="audio/mp4">
   <Role schemeIdUri="urn:mpeg:dash:role:2011" value="description"/>
   <Representation id="ad" bandwidth="999999"><BaseURL>ad.mp4</BaseURL></Representation>
  </AdaptationSet>
 </Period>
</MPD>`

func TestDASHTemplate(t *testing.T) {
	p, err := Resolve(context.Background(), "dash", "https://site.com/v/manifest.mpd", dashTemplate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tracks) != 2 {
		t.Fatalf("%d tracks", len(p.Tracks))
	}
	v, a := p.Tracks[0], p.Tracks[1]
	// 10.5 s at 4 s each: 3 segments plus the init.
	if v.Kind != Video || v.Height != 1080 || len(v.Fragments) != 4 ||
		v.Fragments[0].URL != "https://cdn.site.com/media/v1080/init.mp4" ||
		v.Fragments[3].URL != "https://cdn.site.com/media/v1080/seg-00003.m4s" {
		t.Fatalf("video %+v", v)
	}
	// 3 x 2 s, then 1 s segments until 10.5 s: 6, 7, 8, 9, 10 → 5 more.
	var urls []string
	for _, f := range a.Fragments {
		urls = append(urls, strings.TrimPrefix(f.URL, "https://cdn.site.com/media/audio/"))
	}
	got := strings.Join(urls, " ")
	if a.Kind != Audio || a.Bandwidth != 128000 || got != "init.mp4 0.m4s 96000.m4s 192000.m4s 288000.m4s 336000.m4s 384000.m4s 432000.m4s 480000.m4s" {
		t.Fatalf("audio %s", got)
	}
}

func TestDASHSingleFileAndList(t *testing.T) {
	// Reddit's shape: one file per representation.
	reddit := `<MPD type="static" mediaPresentationDuration="PT12S"><Period>
<AdaptationSet contentType="video"><Representation id="1" bandwidth="500" mimeType="video/mp4"><BaseURL>DASH_480.mp4</BaseURL><SegmentBase indexRange="800-900"/></Representation>
<Representation id="2" bandwidth="900" mimeType="video/mp4"><BaseURL>DASH_720.mp4</BaseURL></Representation></AdaptationSet>
<AdaptationSet contentType="audio"><Representation id="3" bandwidth="64" mimeType="audio/mp4"><BaseURL>DASH_AUDIO_64.mp4</BaseURL></Representation></AdaptationSet>
</Period></MPD>`
	p, err := Resolve(context.Background(), "dash", "https://v.redd.it/abc/DASHPlaylist.mpd", reddit, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tracks) != 2 || !p.Tracks[0].Single || p.Tracks[0].Fragments[0].URL != "https://v.redd.it/abc/DASH_720.mp4" || p.Tracks[1].Fragments[0].URL != "https://v.redd.it/abc/DASH_AUDIO_64.mp4" {
		t.Fatalf("%+v", p.Tracks)
	}
	list := `<MPD type="static"><Period><AdaptationSet mimeType="video/webm"><Representation id="1" bandwidth="1">
<BaseURL>file.webm</BaseURL><SegmentList><Initialization range="0-99"/><SegmentURL mediaRange="100-199"/><SegmentURL media="other.webm"/></SegmentList>
</Representation></AdaptationSet></Period></MPD>`
	p, err = Resolve(context.Background(), "dash", "https://s.com/m.mpd", list, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := p.Tracks[0].Fragments
	if p.Tracks[0].Format != "webm" || len(f) != 3 || f[0].Len != 100 || f[1].Start != 100 || f[2].URL != "https://s.com/other.webm" || f[2].Len != -1 {
		t.Fatalf("%+v", p.Tracks[0])
	}
}

func TestDASHRefusals(t *testing.T) {
	cases := map[string]string{
		"live":    `<MPD type="dynamic"><Period/></MPD>`,
		"drm":     `<MPD><Period><AdaptationSet contentType="video"><ContentProtection schemeIdUri="urn:uuid:edef8ba9"/><Representation id="1"/></AdaptationSet></Period></MPD>`,
		"periods": `<MPD><Period/><Period/></MPD>`,
		"broken":  `<MPD><Period>`,
		"empty":   `<MPD><Period><AdaptationSet contentType="text"><Representation id="1"/></AdaptationSet></Period></MPD>`,
		"endless": `<MPD mediaPresentationDuration="PT100000H"><Period><AdaptationSet contentType="video"><Representation id="1"><SegmentTemplate duration="1" media="$Number$"/></Representation></AdaptationSet></Period></MPD>`,
	}
	for name, m := range cases {
		_, err := Resolve(context.Background(), "dash", "https://s/m.mpd", m, nil)
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if name == "live" && !errors.Is(err, ErrLive) || name == "drm" && !errors.Is(err, media.ErrDRM) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestFill(t *testing.T) {
	rep := &representation{ID: "v1", Bandwidth: 300}
	got := fill("$RepresentationID$/$Bandwidth$/$Number%04d$-$Time$-$$.m4s", rep, 7, 9000)
	if got != "v1/300/0007-9000-$.m4s" {
		t.Fatal(got)
	}
}

func TestISODuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"PT1H2M3.5S": time.Hour + 2*time.Minute + 3500*time.Millisecond,
		"P0Y0M1DT1S": 24*time.Hour + time.Second,
		"PT634.566S": 634566 * time.Millisecond,
		"":           0,
	} {
		got, err := isoDuration(in)
		if err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := isoDuration("1 hour"); err == nil {
		t.Error("accepted nonsense")
	}
}

func ExampleResolve() {
	p, _ := Resolve(context.Background(), "hls", "https://s.com/a.m3u8", "#EXTM3U\n#EXTINF:4,\na.ts\n#EXTINF:4,\nb.ts\n#EXT-X-ENDLIST\n", nil)
	fmt.Println(p.Tracks[0].Kind, p.Tracks[0].Format, len(p.Tracks[0].Fragments))
	// Output: video+audio ts 2
}
