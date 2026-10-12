package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
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
}

func newEngine(log io.Writer) *engine {
	return &engine{inbox: media.NewInbox(100), log: log, now: time.Now}
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
		}
		return fail("unknown message from the browser: " + in.Message.Type)
	default:
		return fail("unknown request type: " + m.Type)
	}
}

func (e *engine) addMedia(browser string, c media.Context) ipc.Message {
	if err := c.Validate(); err != nil {
		return fail(capitalise(err.Error()) + ".")
	}
	id := e.inbox.Add(browser, c, e.now())
	// Host only: query strings often carry access tokens.
	host := ""
	if u, err := url.Parse(c.URL); err == nil {
		host = u.Host
	}
	fmt.Fprintf(e.log, "received %s stream from %s: %s (%d cookies, %d-byte manifest)\n",
		c.Kind, browser, host, len(c.Cookies), len(c.Manifest))
	return ipc.Message{Type: "media.added", Payload: payload(map[string]any{"id": id})}
}

func capitalise(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}
