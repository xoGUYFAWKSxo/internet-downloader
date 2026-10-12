package main

import (
	"encoding/json"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/version"
)

// fromExtension is what nmhost forwards: the calling browser plus the
// extension's own message.
type fromExtension struct {
	Browser   string `json:"browser"`
	Extension string `json:"extension"`
	Message   struct {
		Type string `json:"type"`
	} `json:"message"`
}

func pong() ipc.Message {
	p, _ := json.Marshal(map[string]string{"version": version.Version})
	return ipc.Message{Type: "pong", Payload: p}
}

func handle(m ipc.Message) ipc.Message {
	switch m.Type {
	case "ping":
		return pong()
	case "extension":
		var in fromExtension
		if err := json.Unmarshal(m.Payload, &in); err != nil {
			return ipc.Message{Type: "error", Error: "invalid message from the browser: " + err.Error()}
		}
		if in.Message.Type == "ping" {
			return pong()
		}
		return ipc.Message{Type: "error", Error: "downloads from the browser are not implemented yet"}
	default:
		return ipc.Message{Type: "error", Error: "unknown request type: " + m.Type}
	}
}
