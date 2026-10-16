package media

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	const master = "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000\nlow.m3u8\n"
	cases := []struct {
		name    string
		c       Context
		wantErr string
	}{
		{"hls ok", Context{URL: "https://cdn.example/a.m3u8", Kind: KindHLS, Manifest: master}, ""},
		{"hls with BOM", Context{URL: "https://cdn.example/a.m3u8", Kind: KindHLS, Manifest: "\ufeff" + master}, ""},
		{"hls no manifest", Context{URL: "https://cdn.example/a.m3u8", Kind: KindHLS}, ""},
		{"hls aes-128 is fine", Context{URL: "https://x/a.m3u8", Kind: KindHLS, Manifest: "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k.bin\"\n"}, ""},
		{"hls identity keyformat", Context{URL: "https://x/a.m3u8", Kind: KindHLS, Manifest: "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\",KEYFORMAT=\"identity\"\n"}, ""},
		{"hls sample-aes", Context{URL: "https://x/a.m3u8", Kind: KindHLS, Manifest: "#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://k\"\n"}, "DRM"},
		{"hls widevine session key", Context{URL: "https://x/a.m3u8", Kind: KindHLS, Manifest: "#EXTM3U\n#EXT-X-SESSION-KEY:METHOD=SAMPLE-AES-CTR,KEYFORMAT=\"urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed\"\n"}, "DRM"},
		{"hls html page", Context{URL: "https://x/a.m3u8", Kind: KindHLS, Manifest: "<html>403</html>"}, "valid HLS"},
		{"dash ok", Context{URL: "https://x/a.mpd", Kind: KindDASH, Manifest: `<?xml version="1.0"?><MPD></MPD>`}, ""},
		{"dash drm", Context{URL: "https://x/a.mpd", Kind: KindDASH, Manifest: `<MPD><ContentProtection schemeIdUri="x"/></MPD>`}, "DRM"},
		{"dash not mpd", Context{URL: "https://x/a.mpd", Kind: KindDASH, Manifest: "{}"}, "valid DASH"},
		{"video", Context{URL: "http://x/v.mp4", Kind: KindVideo}, ""},
		{"bad scheme", Context{URL: "file:///c:/x.mp4", Kind: KindVideo}, "web address"},
		{"no host", Context{URL: "https:///x.mp4", Kind: KindVideo}, "web address"},
		{"bad kind", Context{URL: "https://x/a", Kind: "torrent"}, "unknown stream type"},
		{"huge manifest", Context{URL: "https://x/a.m3u8", Kind: KindHLS, Manifest: strings.Repeat("a", MaxManifest+1)}, "too large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.c.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, tc.wantErr)
			}
			if tc.wantErr == "DRM" && !errors.Is(err, ErrDRM) {
				t.Fatalf("want ErrDRM, got %v", err)
			}
		})
	}
}

func TestInbox(t *testing.T) {
	b := NewInbox(2)
	now := time.Unix(1, 0)
	b.Add("chromium", Context{URL: "https://x/1", Kind: KindVideo}, now)
	b.Add("firefox", Context{URL: "https://x/2", Kind: KindHLS, Manifest: "#EXTM3U", Cookies: []Cookie{{Name: "s", Value: "secret"}}}, now)
	id := b.Add("firefox", Context{URL: "https://x/3", Kind: KindDASH}, now)
	if id != 3 {
		t.Fatalf("id = %d, want 3", id)
	}
	s := b.Summaries()
	if len(s) != 2 || s[0].URL != "https://x/3" || s[1].URL != "https://x/2" {
		t.Fatalf("summaries = %+v", s)
	}
	if s[1].ManifestBytes != 7 || s[1].Cookies != 1 {
		t.Fatalf("counts wrong: %+v", s[1])
	}
}

func TestInboxRestore(t *testing.T) {
	b := NewInbox(2)
	b.Restore([]Item{{ID: 3}, {ID: 7}, {ID: 9}})
	if s := b.Summaries(); len(s) != 2 || s[0].ID != 9 || s[1].ID != 7 {
		t.Fatalf("restored %+v", s)
	}
	if id := b.Add("chrome", Context{URL: "https://x.test/a.mpd", Kind: KindDASH}, time.Now()); id != 10 {
		t.Fatalf("next ID %d", id)
	}
}
