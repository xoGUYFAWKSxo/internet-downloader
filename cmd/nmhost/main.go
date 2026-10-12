// Command nmhost is the Native Messaging host the browser launches. It only
// relays: each message from the extension goes to the engine over the pipe,
// and the engine's reply goes back. Stdout carries protocol frames only, so
// diagnostics go to stderr.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/nativemsg"
)

type reply struct {
	OK      bool            `json:"ok"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
}

func send(r reply) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return nativemsg.Write(os.Stdout, b)
}

func dial() (*ipc.Client, error) {
	name, err := ipc.PipeName()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return ipc.Dial(ctx, name)
}

func main() {
	var client *ipc.Client
	defer func() {
		if client != nil {
			client.Close()
		}
	}()
	for {
		msg, err := nativemsg.Read(os.Stdin)
		if err == io.EOF {
			return
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "nmhost:", err)
			return
		}
		if client == nil {
			if client, err = dial(); err != nil {
				client = nil
				send(reply{Error: "The downloader isn't running. Start it and try again."})
				continue
			}
		}
		resp, err := client.Call(ipc.Message{Type: "extension", Payload: msg})
		if err != nil {
			client.Close()
			client = nil
			send(reply{Error: "Lost the connection to the downloader: " + err.Error()})
			continue
		}
		if resp.Error != "" {
			send(reply{Error: resp.Error})
		} else {
			send(reply{OK: true, Payload: resp.Payload})
		}
	}
}
