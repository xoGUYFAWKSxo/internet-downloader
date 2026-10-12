package main

import (
	"errors"
	"testing"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
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

func TestProbeFrom(t *testing.T) {
	got, err := probeFrom(ipc.Message{Type: "media.probe", Payload: []byte(`{"protocol":"HTTP/2","size":5,"plan":{"mode":"single"}}`)}, nil)
	if err != nil || got.Protocol != "HTTP/2" || got.Plan.Mode != "single" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := probeFrom(ipc.Message{Type: "error", Error: "The server answered 404 Not Found."}, nil); err == nil || err.Error() != "The server answered 404 Not Found." {
		t.Fatalf("engine error: %v", err)
	}
}

func TestDecode(t *testing.T) {
	var list []fetch.Status
	err := decode(ipc.Message{Type: "downloads.list", Payload: []byte(`[{"id":1,"name":"a.mp4","size":10,"done":4,"state":"downloading"}]`)}, nil)(&list)
	if err != nil || len(list) != 1 || list[0].Done != 4 || list[0].State != fetch.Downloading {
		t.Fatalf("got %+v, %v", list, err)
	}
	if err := decode(ipc.Message{Type: "error", Error: "nope"}, nil)(nil); err == nil || err.Error() != "nope" {
		t.Fatalf("engine error: %v", err)
	}
	if err := decode(ipc.Message{}, errors.New("pipe"))(&list); err == nil {
		t.Fatal("call error lost")
	}
	if err := (&App{}).ShowInFolder("relative.txt"); err == nil {
		t.Fatal("relative path accepted")
	}
}
