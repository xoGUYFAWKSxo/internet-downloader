package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/journal"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/stream"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/version"
)

// fromExtension is what nmhost forwards: the calling browser plus the
// extension's own message.
type fromExtension struct {
	Browser   string `json:"browser"`
	Extension string `json:"extension"`
	Message   struct {
		Type  string        `json:"type"`
		Media media.Context `json:"media"`
	} `json:"message"`
}

type engine struct {
	inbox *media.Inbox
	log   io.Writer
	now   func() time.Time
	probe probe.Options // tests point this at their own certificates
	dir   string        // where downloads are saved
	dl    downloads
	// ffmpeg is the FFmpeg program, or "" when there is none.
	ffmpeg string
	// finished, if set, is called when a download stops for any reason.
	finished func(id uint64)
	// jr saves the download list and progress; nil keeps them in memory.
	jr *journal.Journal
	// retries are the pauses before trying a dropped download again.
	retries []time.Duration
	// checkpointEvery is how often a download's progress is committed.
	checkpointEvery time.Duration
}

func newEngine(log io.Writer) *engine {
	return &engine{inbox: media.NewInbox(100), log: log, now: time.Now, dir: downloadDir(), ffmpeg: stream.FFmpeg(),
		retries: []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second}, checkpointEvery: 3 * time.Second}
}

type idRequest struct {
	ID uint64 `json:"id"`
}

func payload(v any) json.RawMessage {
	p, _ := json.Marshal(v)
	return p
}

func pong() ipc.Message {
	return ipc.Message{Type: "pong", Payload: payload(map[string]string{"version": version.Version})}
}

func fail(msg string) ipc.Message { return ipc.Message{Type: "error", Error: msg} }

func (e *engine) handle(m ipc.Message) ipc.Message {
	switch m.Type {
	case "ping":
		return pong()
	case "media.list":
		return ipc.Message{Type: "media.list", Payload: payload(e.inbox.Summaries())}
	case "media.probe":
		var in idRequest
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		return e.probeMedia(in.ID)
	case "media.download":
		var in idRequest
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		id, err := e.startDownload(in.ID)
		if err != nil {
			return fail(err.Error())
		}
		return ipc.Message{Type: "media.download", Payload: payload(idRequest{id})}
	case "downloads.list":
		return ipc.Message{Type: "downloads.list", Payload: payload(e.dl.list())}
	case "downloads.cancel":
		var in idRequest
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		if err := e.cancelDownload(in.ID); err != nil {
			return fail(err.Error())
		}
		return ipc.Message{Type: "downloads.cancel"}
	case "downloads.resume":
		var in idRequest
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid request: " + err.Error())
		}
		if err := e.resumeDownload(in.ID); err != nil {
			return fail(err.Error())
		}
		return ipc.Message{Type: "downloads.resume"}
	case "extension":
		var in fromExtension
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return fail("invalid message from the browser: " + err.Error())
		}
		switch in.Message.Type {
		case "ping":
			return pong()
		case "media":
			return e.addMedia(in.Browser, in.Message.Media)
		case "download":
			// From the right-click menu: add the item and start it at once.
			added := e.addMedia(in.Browser, in.Message.Media)
			if added.Type != "media.added" {
				return added
			}
			var item idRequest
			json.Unmarshal(added.Payload, &item)
			id, err := e.startDownload(item.ID)
			if err != nil {
				return fail(sentence(err))
			}
			return ipc.Message{Type: "media.download", Payload: payload(idRequest{id})}
		}
		return fail("unknown message from the browser: " + in.Message.Type)
	default:
		return fail("unknown request type: " + m.Type)
	}
}

func (e *engine) addMedia(browser string, c media.Context) ipc.Message {
	if err := c.Validate(); err != nil {
		return fail(sentence(err))
	}
	id := e.inbox.Add(browser, c, e.now())
	// Host only: query strings often carry access tokens.
	host := hostOf(c.URL)
	fmt.Fprintf(e.log, "received %s stream from %s: %s (%d cookies, %d-byte manifest)\n",
		c.Kind, browser, host, len(c.Cookies), len(c.Manifest))
	return ipc.Message{Type: "media.added", Payload: payload(map[string]any{"id": id})}
}

// probeMedia checks the server of a received stream with the browser's
// cookies and headers. For HLS and DASH this is the playlist's server; the
// fragments are checked when Phase 4 fetches them.
func (e *engine) probeMedia(id uint64) ipc.Message {
	it, ok := e.inbox.Get(id)
	if !ok {
		return fail("That item is no longer in the list.")
	}
	c := it.Context
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := probe.Probe(ctx, probe.Request{URL: c.URL, Referer: c.Referer, Headers: c.Headers, Cookies: c.Cookies}, e.probe)
	host := hostOf(c.URL)
	if err != nil {
		fmt.Fprintf(e.log, "probe %s: %v\n", host, err)
		return fail(sentence(err))
	}
	fmt.Fprintf(e.log, "probe %s: %s, ranges %v, size %d, plan %s\n", host, res.Protocol, res.Ranges, res.Size, res.Plan.Mode)
	// The final address can carry a signed token; the window doesn't need it.
	res.URL = ""
	return ipc.Message{Type: "media.probe", Payload: payload(res)}
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return ""
}

func capitalise(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

// sentence turns an error into a sentence for the window: capitalised, with
// one full stop. Some Windows messages already end with one.
func sentence(err error) string {
	return capitalise(strings.TrimRight(err.Error(), ". ")) + "."
}
