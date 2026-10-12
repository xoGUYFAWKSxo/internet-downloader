// Package stream reads HLS playlists and DASH manifests and works out what to
// download: the best video and, when the stream keeps it separate, the best
// audio. Each track is a list of fragments that, written one after another,
// make a playable file.
package stream

import (
	"context"
	"errors"
	"net/url"
	"path"
	"strings"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
)

// Track kinds.
const (
	Video = "video"
	Audio = "audio"
	Both  = "video+audio" // one track that carries both
)

// Track is one thing to download.
type Track struct {
	Kind   string
	Format string // container of the fragments: ts, mp4, webm, aac, mp3
	// Fragments in order, any initialization segment first.
	Fragments []fetch.Fragment
	// Single is a whole file (DASH with only a BaseURL), fetched like a
	// plain download rather than fragment by fragment.
	Single    bool
	Bandwidth int
	Width     int
	Height    int
}

// Plan is what a stream resolves to.
type Plan struct {
	Tracks []Track
}

// Getter fetches a playlist or manifest the way the browser would.
type Getter func(ctx context.Context, url string) ([]byte, error)

// ErrLive is returned for streams that are still being broadcast.
var ErrLive = errors.New("this is a live stream; recording live streams isn't supported yet")

// Resolve reads the stream at u. text is the manifest the extension sent, or
// "" to fetch it.
func Resolve(ctx context.Context, kind, u, text string, get Getter) (*Plan, error) {
	if text == "" {
		b, err := get(ctx, u)
		if err != nil {
			return nil, err
		}
		text = string(b)
	}
	switch kind {
	case "hls":
		return resolveHLS(ctx, u, text, get)
	case "dash":
		return resolveDASH(u, text)
	}
	return nil, errors.New("not an HLS or DASH stream")
}

// resolve makes ref absolute against base.
func resolve(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}

// formatOf guesses a fragment's container from its file extension.
func formatOf(u string) string {
	p := u
	if pu, err := url.Parse(u); err == nil {
		p = pu.Path
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".aac":
		return "aac"
	case ".mp3":
		return "mp3"
	case ".mp4", ".m4s", ".m4v", ".m4a", ".cmfv", ".cmfa":
		return "mp4"
	case ".webm":
		return "webm"
	}
	return "ts"
}
