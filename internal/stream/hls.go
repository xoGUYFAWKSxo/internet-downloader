package stream

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
)

func resolveHLS(ctx context.Context, u, text string, get Getter) (*Plan, error) {
	if !strings.Contains(text, "#EXT-X-STREAM-INF") {
		t, err := parseMedia(u, text)
		if err != nil {
			return nil, err
		}
		return &Plan{Tracks: []Track{*t}}, nil
	}
	v, audio, err := parseMaster(u, text)
	if err != nil {
		return nil, err
	}
	load := func(u string) (*Track, error) {
		b, err := get(ctx, u)
		if err != nil {
			return nil, err
		}
		return parseMedia(u, string(b))
	}
	vt, err := load(v.uri)
	if err != nil {
		return nil, err
	}
	vt.Bandwidth, vt.Width, vt.Height = v.bandwidth, v.width, v.height
	if audioOnly(v.codecs) {
		vt.Kind = Audio
	}
	p := &Plan{Tracks: []Track{*vt}}
	if audio != "" {
		at, err := load(audio)
		if err != nil {
			return nil, fmt.Errorf("the audio playlist: %w", err)
		}
		at.Kind = Audio
		p.Tracks[0].Kind = Video
		p.Tracks = append(p.Tracks, *at)
	}
	return p, nil
}

type variant struct {
	uri                      string
	bandwidth, width, height int
	codecs, audio            string
}

// parseMaster picks the variant with the highest bandwidth, and the audio
// rendition it names when that is a separate playlist.
func parseMaster(base, text string) (variant, string, error) {
	var vs []variant
	type rendition struct {
		uri       string
		def, auto bool
	}
	groups := map[string][]rendition{}
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			a := attrs(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
			for i+1 < len(lines) {
				i++
				next := strings.TrimSpace(lines[i])
				if next == "" || strings.HasPrefix(next, "#") {
					continue
				}
				uri, err := resolve(base, next)
				if err != nil {
					return variant{}, "", err
				}
				v := variant{uri: uri, codecs: a["CODECS"], audio: a["AUDIO"]}
				v.bandwidth, _ = strconv.Atoi(a["BANDWIDTH"])
				if w, h, ok := strings.Cut(a["RESOLUTION"], "x"); ok {
					v.width, _ = strconv.Atoi(w)
					v.height, _ = strconv.Atoi(h)
				}
				vs = append(vs, v)
				break
			}
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			a := attrs(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
			if a["TYPE"] != "AUDIO" || a["URI"] == "" {
				continue
			}
			uri, err := resolve(base, a["URI"])
			if err != nil {
				return variant{}, "", err
			}
			groups[a["GROUP-ID"]] = append(groups[a["GROUP-ID"]], rendition{uri, a["DEFAULT"] == "YES", a["AUTOSELECT"] == "YES"})
		}
	}
	if len(vs) == 0 {
		return variant{}, "", errors.New("the playlist lists no streams")
	}
	// Prefer streams with video; an audio-only variant wins only when
	// there is nothing else.
	best := -1
	for i, v := range vs {
		if best < 0 || audioOnly(vs[best].codecs) && !audioOnly(v.codecs) ||
			audioOnly(vs[best].codecs) == audioOnly(v.codecs) && v.bandwidth > vs[best].bandwidth {
			best = i
		}
	}
	v := vs[best]
	audio := ""
	if rs := groups[v.audio]; len(rs) > 0 {
		// DEFAULT=YES, else AUTOSELECT=YES, else the first.
		audio = rs[0].uri
		for _, r := range rs {
			if r.auto {
				audio = r.uri
				break
			}
		}
		for _, r := range rs {
			if r.def {
				audio = r.uri
				break
			}
		}
	}
	return v, audio, nil
}

// audioOnly reports CODECS that list no video codec.
func audioOnly(codecs string) bool {
	if codecs == "" {
		return false
	}
	for _, c := range strings.Split(codecs, ",") {
		c = strings.TrimSpace(c)
		if !strings.HasPrefix(c, "mp4a") && !strings.HasPrefix(c, "ac-3") && !strings.HasPrefix(c, "ec-3") && !strings.HasPrefix(c, "opus") && !strings.HasPrefix(c, "flac") && c != "mp3" {
			return false
		}
	}
	return true
}

// parseMedia reads a media playlist into a track.
func parseMedia(base, text string) (*Track, error) {
	if !strings.HasPrefix(strings.TrimLeft(text, "\xef\xbb\xbf \t\r\n"), "#EXTM3U") {
		return nil, errors.New("a playlist the stream points to isn't a valid HLS playlist")
	}
	t := &Track{Kind: Both}
	var (
		seq      int64
		key      *fetch.Key
		ended    bool
		hasRange bool
		rLen     int64
		rOff     int64 = -1
		ends           = map[string]int64{} // where the last range of each URL ended
		segs     []fetch.Fragment
	)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		tag, val, _ := strings.Cut(line, ":")
		switch {
		case line == "":
		case tag == "#EXT-X-MEDIA-SEQUENCE":
			seq, _ = strconv.ParseInt(val, 10, 64)
		case tag == "#EXT-X-ENDLIST", line == "#EXT-X-PLAYLIST-TYPE:VOD":
			ended = true
		case tag == "#EXT-X-KEY":
			a := attrs(val)
			switch a["METHOD"] {
			case "NONE":
				key = nil
			case "AES-128":
				if f := a["KEYFORMAT"]; f != "" && f != "identity" {
					return nil, media.ErrDRM
				}
				ku, err := resolve(base, a["URI"])
				if err != nil {
					return nil, err
				}
				key = &fetch.Key{URL: ku}
				if iv := a["IV"]; iv != "" {
					b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(iv, "0x"), "0X"))
					if err != nil || len(b) != 16 {
						return nil, errors.New("the playlist has an invalid encryption IV")
					}
					key.IV = b
				}
			default:
				return nil, media.ErrDRM
			}
		case tag == "#EXT-X-MAP":
			a := attrs(val)
			mu, err := resolve(base, a["URI"])
			if err != nil {
				return nil, err
			}
			init := fetch.Fragment{URL: mu, Len: -1}
			if br := a["BYTERANGE"]; br != "" {
				n, off, ok := byteRange(br)
				if !ok || off < 0 {
					return nil, errors.New("the playlist has an invalid byte range")
				}
				init.Start, init.Len = off, n
			}
			if key != nil && key.IV != nil {
				init.Key = key
			}
			segs = append(segs, init)
			t.Format = "mp4"
		case tag == "#EXT-X-BYTERANGE":
			n, off, ok := byteRange(val)
			if !ok {
				return nil, errors.New("the playlist has an invalid byte range")
			}
			hasRange, rLen, rOff = true, n, off
		case strings.HasPrefix(line, "#"):
		default:
			su, err := resolve(base, line)
			if err != nil {
				return nil, err
			}
			s := fetch.Fragment{URL: su, Len: -1}
			if hasRange {
				if rOff < 0 {
					rOff = ends[su]
				}
				s.Start, s.Len = rOff, rLen
				ends[su] = rOff + rLen
				hasRange, rOff = false, -1
			}
			if key != nil {
				k := *key
				if k.IV == nil {
					// No IV: it is the segment's sequence number.
					k.IV = make([]byte, 16)
					binary.BigEndian.PutUint64(k.IV[8:], uint64(seq))
				}
				s.Key = &k
			}
			if t.Format == "" {
				t.Format = formatOf(su)
			}
			segs = append(segs, s)
			seq++
		}
	}
	if !ended {
		return nil, ErrLive
	}
	if len(segs) == 0 {
		return nil, errors.New("the playlist has no segments")
	}
	t.Fragments = segs
	return t, nil
}

// byteRange parses "n[@o]"; o is -1 when absent.
func byteRange(s string) (n, off int64, ok bool) {
	ns, os, has := strings.Cut(strings.Trim(s, `"`), "@")
	n, err := strconv.ParseInt(ns, 10, 64)
	if err != nil || n <= 0 {
		return 0, 0, false
	}
	off = -1
	if has {
		if off, err = strconv.ParseInt(os, 10, 64); err != nil || off < 0 {
			return 0, 0, false
		}
	}
	return n, off, true
}

// attrs parses an HLS attribute list: KEY=value,KEY="quoted, value".
func attrs(s string) map[string]string {
	m := map[string]string{}
	for s != "" {
		k, rest, ok := strings.Cut(s, "=")
		if !ok {
			break
		}
		k = strings.TrimSpace(k)
		var v string
		if strings.HasPrefix(rest, `"`) {
			end := strings.Index(rest[1:], `"`)
			if end < 0 {
				v, s = rest[1:], ""
			} else {
				v, s = rest[1:end+1], rest[end+2:]
			}
			s = strings.TrimPrefix(s, ",")
		} else {
			v, s, _ = strings.Cut(rest, ",")
		}
		m[k] = v
	}
	return m
}
