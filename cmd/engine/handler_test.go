package main

import (
	"strings"
	"testing"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
)

func TestHandle(t *testing.T) {
	cases := []struct {
		name     string
		in       ipc.Message
		wantType string
		wantErr  string
	}{
		{"ping", ipc.Message{Type: "ping"}, "pong", ""},
		{"extension ping", ipc.Message{Type: "extension", Payload: []byte(`{"browser":"firefox","extension":"x","message":{"type":"ping"}}`)}, "pong", ""},
		{"extension other", ipc.Message{Type: "extension", Payload: []byte(`{"browser":"chromium","message":{"type":"download"}}`)}, "error", "not implemented"},
		{"extension bad json", ipc.Message{Type: "extension", Payload: []byte(`{`)}, "error", "invalid message"},
		{"unknown", ipc.Message{Type: "nope"}, "error", "unknown request type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := handle(tc.in)
			if got.Type != tc.wantType || !strings.Contains(got.Error, tc.wantErr) {
				t.Fatalf("got %+v", got)
			}
			if tc.wantType == "pong" && !strings.Contains(string(got.Payload), `"version"`) {
				t.Fatalf("pong without version: %s", got.Payload)
			}
		})
	}
}
