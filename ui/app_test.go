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

func TestSummariesFrom(t *testing.T) {
	got, err := summariesFrom(ipc.Message{Type: "media.list", Payload: []byte(`[{"id":2,"kind":"hls","url":"https://x/a.m3u8"}]`)}, nil)
	if err != nil || len(got) != 1 || got[0].Kind != "hls" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := summariesFrom(ipc.Message{Type: "error", Error: "nope"}, nil); err == nil {
		t.Fatal("engine error should be an error")
	}
	if got, err := summariesFrom(ipc.Message{Type: "media.list", Payload: []byte(`[]`)}, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty list: %+v, %v", got, err)
	}
}
