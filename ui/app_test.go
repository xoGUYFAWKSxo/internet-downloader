package main

import (
	"errors"
	"testing"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
)

func TestStatusFromPong(t *testing.T) {
	s := statusFromPong(ipc.Message{Type: "pong", Payload: []byte(`{"version":"1.2.3"}`)}, nil)
	if !s.Running || s.Version != "1.2.3" {
		t.Fatalf("got %+v", s)
	}
	if s := statusFromPong(ipc.Message{}, errors.New("broken pipe")); s.Running {
		t.Fatalf("call error should not be running: %+v", s)
	}
	if s := statusFromPong(ipc.Message{Type: "error", Error: "nope"}, nil); s.Running {
		t.Fatalf("engine error should not be running: %+v", s)
	}
}
