//go:build windows

// Package ipc is the engine's local API: newline-delimited JSON over a
// per-user named pipe that only the current user can open.
package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// Message is both request and response. A response echoes the request ID.
type Message struct {
	ID      uint64          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// Handler answers one request. It must be safe for concurrent use.
type Handler func(Message) Message

// maxLine bounds a single message so a bad client can't grow memory without
// limit; it matches the browser's largest native message plus framing slack.
const maxLine = 65 << 20

func currentUserSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

// PipeName returns the default pipe name for the current user.
func PipeName() (string, error) {
	sid, err := currentUserSID()
	if err != nil {
		return "", fmt.Errorf("ipc: read user SID: %w", err)
	}
	return `\\.\pipe\InternetDownloader-` + sid, nil
}

// Listen creates the pipe with a DACL granting access to the current user
// only. It fails if another process already owns the name, which is how the
// engine enforces a single instance.
func Listen(name string) (net.Listener, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, fmt.Errorf("ipc: read user SID: %w", err)
	}
	return winio.ListenPipe(name, &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;" + sid + ")",
	})
}

// Serve accepts connections until l is closed.
func Serve(l net.Listener, h Handler) error {
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, winio.ErrPipeListenerClosed) {
				return nil
			}
			return err
		}
		go serveConn(c, h)
	}
}

func serveConn(c net.Conn, h Handler) {
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	enc := json.NewEncoder(c)
	for sc.Scan() {
		var req Message
		var resp Message
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			resp = Message{Type: "error", Error: "invalid request: " + err.Error()}
		} else {
			resp = h(req)
			resp.ID = req.ID
		}
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

// Client is a connection to the engine. Calls are serialized.
type Client struct {
	mu   sync.Mutex
	conn net.Conn
	sc   *bufio.Scanner
	next uint64
}

func Dial(ctx context.Context, name string) (*Client, error) {
	c, err := winio.DialPipeContext(ctx, name)
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	return &Client{conn: c, sc: sc}, nil
}

func (c *Client) Call(req Message) (Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	req.ID = c.next
	b, err := json.Marshal(req)
	if err != nil {
		return Message{}, err
	}
	if _, err := c.conn.Write(append(b, '\n')); err != nil {
		return Message{}, err
	}
	if !c.sc.Scan() {
		if err := c.sc.Err(); err != nil {
			return Message{}, err
		}
		return Message{}, errors.New("ipc: engine closed the connection")
	}
	var resp Message
	if err := json.Unmarshal(c.sc.Bytes(), &resp); err != nil {
		return Message{}, err
	}
	if resp.ID != req.ID {
		return Message{}, fmt.Errorf("ipc: response id %d, want %d", resp.ID, req.ID)
	}
	return resp, nil
}

func (c *Client) Close() error { return c.conn.Close() }
