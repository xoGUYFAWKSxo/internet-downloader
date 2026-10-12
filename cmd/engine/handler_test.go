package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/testserver"
)

func ext(msg string) ipc.Message {
	return ipc.Message{Type: "extension", Payload: []byte(`{"browser":"firefox","extension":"x","message":` + msg + `}`)}
}

func TestHandle(t *testing.T) {
	cases := []struct {
		name     string
		in       ipc.Message
		wantType string
		wantErr  string
	}{
		{"ping", ipc.Message{Type: "ping"}, "pong", ""},
		{"extension ping", ext(`{"type":"ping"}`), "pong", ""},
		{"extension media", ext(`{"type":"media","media":{"url":"https://cdn.example/a.m3u8?token=t","kind":"hls","manifest":"#EXTM3U\n"}}`), "media.added", ""},
		{"extension media invalid", ext(`{"type":"media","media":{"url":"ftp://x/a","kind":"video"}}`), "error", "The stream address"},
		{"extension media drm", ext(`{"type":"media","media":{"url":"https://x/a.mpd","kind":"dash","manifest":"<MPD><ContentProtection/></MPD>"}}`), "error", "DRM"},
		{"extension other", ext(`{"type":"download"}`), "error", "unknown message"},
		{"extension bad json", ipc.Message{Type: "extension", Payload: []byte(`{`)}, "error", "invalid message"},
		{"list", ipc.Message{Type: "media.list"}, "media.list", ""},
		{"unknown", ipc.Message{Type: "nope"}, "error", "unknown request type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := newEngine(&bytes.Buffer{}).handle(tc.in)
			if got.Type != tc.wantType || !strings.Contains(got.Error, tc.wantErr) {
				t.Fatalf("got %+v", got)
			}
			if tc.wantType == "pong" && !strings.Contains(string(got.Payload), `"version"`) {
				t.Fatalf("pong without version: %s", got.Payload)
			}
		})
	}
}

func TestMediaIsListedAndSecretsStayOutOfTheLog(t *testing.T) {
	var log bytes.Buffer
	e := newEngine(&log)
	resp := e.handle(ext(`{"type":"media","media":{"url":"https://cdn.example/a.mp4?token=SECRETTOKEN","kind":"video",
		"headers":{"Authorization":"Bearer SECRETAUTH"},"cookies":[{"name":"sid","value":"SECRETCOOKIE"}]}}`))
	if resp.Type != "media.added" {
		t.Fatalf("got %+v", resp)
	}
	for _, s := range []string{"SECRETTOKEN", "SECRETAUTH", "SECRETCOOKIE"} {
		if strings.Contains(log.String(), s) {
			t.Fatalf("log leaks %s: %q", s, log.String())
		}
	}
	if !strings.Contains(log.String(), "cdn.example") {
		t.Fatalf("log missing host: %q", log.String())
	}
	var list []media.Summary
	if err := json.Unmarshal(e.handle(ipc.Message{Type: "media.list"}).Payload, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Browser != "firefox" || list[0].Cookies != 1 {
		t.Fatalf("list = %+v", list)
	}
	if strings.Contains(string(e.handle(ipc.Message{Type: "media.list"}).Payload), "SECRETCOOKIE") {
		t.Fatal("list leaks cookie values")
	}
}

func TestProbeReceivedMedia(t *testing.T) {
	s := testserver.Start(t, testserver.Content(2<<20))
	var log bytes.Buffer
	e := newEngine(&log)
	e.probe = probe.Options{RootCAs: s.RootCAs}
	added := e.handle(ext(`{"type":"media","media":{"url":"` + s.H2 + `?token=SECRETTOKEN","kind":"video",
		"cookies":[{"name":"sid","value":"SECRETCOOKIE"}]}}`))
	var id struct{ ID uint64 }
	json.Unmarshal(added.Payload, &id)

	resp := e.handle(ipc.Message{Type: "media.probe", Payload: payload(map[string]uint64{"id": id.ID})})
	var res probe.Result
	if err := json.Unmarshal(resp.Payload, &res); err != nil || resp.Type != "media.probe" {
		t.Fatalf("got %+v (%v)", resp, err)
	}
	if res.Protocol != probe.HTTP3 || res.Plan.Mode != probe.Multiplexed || res.Size != 2<<20 {
		t.Fatalf("result %+v", res)
	}
	for _, secret := range []string{"SECRETTOKEN", "SECRETCOOKIE"} {
		if strings.Contains(log.String()+string(resp.Payload), secret) {
			t.Fatalf("%s leaked: %s / %s", secret, log.String(), resp.Payload)
		}
	}

	if r := e.handle(ipc.Message{Type: "media.probe", Payload: []byte(`{"id":999}`)}); r.Type != "error" || !strings.Contains(r.Error, "no longer") {
		t.Fatalf("missing id: %+v", r)
	}
}
