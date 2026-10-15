// Package media holds what the browser extension sends about a stream it
// found on a page, checks it, and keeps the recent ones until the download
// engine (Phase 2 onwards) picks them up.
package media

import (
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Kinds the extension can report.
const (
	KindHLS   = "hls"
	KindDASH  = "dash"
	KindVideo = "video"
	KindAudio = "audio"
	// KindFile is any other file: a download the browser started, or a link.
	KindFile = "file"
)

// MaxManifest is the largest manifest text the engine keeps. Real playlists
// are kilobytes; anything near this is not a playlist.
const MaxManifest = 8 << 20

// Cookie is one browser cookie for the stream's URL.
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain,omitempty"`
	Path     string  `json:"path,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	Expires  float64 `json:"expires,omitempty"` // Unix seconds; 0 = session cookie
}

// Context is everything needed to fetch the stream the way the browser did.
// Cookies and headers are secrets: never log them.
type Context struct {
	URL           string            `json:"url"`
	Kind          string            `json:"kind"`
	ContentType   string            `json:"contentType,omitempty"`
	Size          int64             `json:"size,omitempty"` // bytes, as the browser saw them; 0 = unknown
	PageURL       string            `json:"pageUrl,omitempty"`
	PageTitle     string            `json:"pageTitle,omitempty"`
	Referer       string            `json:"referer,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	Cookies       []Cookie          `json:"cookies,omitempty"`
	Manifest      string            `json:"manifest,omitempty"`
	ManifestError string            `json:"manifestError,omitempty"`
}

// ErrDRM is returned for copy-protected streams, which this app never downloads.
var ErrDRM = errors.New("this stream is copy-protected (DRM), so Galactic Downloader can't download it")

// Validate checks the context is usable. The messages are shown to the user.
func (c *Context) Validate() error {
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("the stream address isn't a web address")
	}
	switch c.Kind {
	case KindHLS, KindDASH, KindVideo, KindAudio, KindFile:
	default:
		return errors.New("unknown stream type " + quote(c.Kind))
	}
	if len(c.Manifest) > MaxManifest {
		return errors.New("the playlist is too large to be a real playlist")
	}
	m := strings.TrimLeft(c.Manifest, "\xef\xbb\xbf \t\r\n")
	if m == "" {
		return nil // The engine fetches it itself later.
	}
	switch c.Kind {
	case KindHLS:
		if !strings.HasPrefix(m, "#EXTM3U") {
			return errors.New("the page's playlist isn't a valid HLS playlist")
		}
		if hlsDRM(m) {
			return ErrDRM
		}
	case KindDASH:
		if !strings.Contains(m, "<MPD") {
			return errors.New("the page's manifest isn't a valid DASH manifest")
		}
		if strings.Contains(m, "<ContentProtection") {
			return ErrDRM
		}
	}
	return nil
}

// hlsDRM reports key tags that use a DRM system. Plain AES-128 is not DRM:
// the key is an ordinary file the player downloads.
func hlsDRM(m string) bool {
	for _, line := range strings.Split(m, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#EXT-X-KEY:") && !strings.HasPrefix(line, "#EXT-X-SESSION-KEY:") {
			continue
		}
		if strings.Contains(line, "METHOD=NONE") {
			continue
		}
		if strings.Contains(line, "METHOD=SAMPLE-AES") || strings.Contains(line, "KEYFORMAT=") && !strings.Contains(line, `KEYFORMAT="identity"`) {
			return true
		}
	}
	return false
}

func quote(s string) string { return `"` + s + `"` }

// Item is a received context plus bookkeeping.
type Item struct {
	ID       uint64
	Received time.Time
	Browser  string
	Context  Context
}

// Summary is the safe-to-show part of an Item: no cookies, headers or manifest.
type Summary struct {
	ID            uint64    `json:"id"`
	Received      time.Time `json:"received"`
	Browser       string    `json:"browser"`
	Kind          string    `json:"kind"`
	URL           string    `json:"url"`
	PageTitle     string    `json:"pageTitle,omitempty"`
	ManifestBytes int       `json:"manifestBytes"`
	Cookies       int       `json:"cookies"`
}

// Inbox keeps the most recent items, newest last. Safe for concurrent use.
type Inbox struct {
	mu     sync.Mutex
	max    int
	nextID uint64
	items  []Item
}

func NewInbox(max int) *Inbox { return &Inbox{max: max} }

// Add stores the context and returns its ID.
func (b *Inbox) Add(browser string, c Context, now time.Time) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	b.items = append(b.items, Item{ID: b.nextID, Received: now, Browser: browser, Context: c})
	if len(b.items) > b.max {
		b.items = append([]Item(nil), b.items[len(b.items)-b.max:]...)
	}
	return b.nextID
}

// Get returns the full item with that ID, if it's still kept.
func (b *Inbox) Get(id uint64) (Item, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, it := range b.items {
		if it.ID == id {
			return it, true
		}
	}
	return Item{}, false
}

// Summaries lists the items, newest first.
func (b *Inbox) Summaries() []Summary {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Summary, 0, len(b.items))
	for i := len(b.items) - 1; i >= 0; i-- {
		it := b.items[i]
		out = append(out, Summary{
			ID: it.ID, Received: it.Received, Browser: it.Browser,
			Kind: it.Context.Kind, URL: it.Context.URL, PageTitle: it.Context.PageTitle,
			ManifestBytes: len(it.Context.Manifest), Cookies: len(it.Context.Cookies),
		})
	}
	return out
}
