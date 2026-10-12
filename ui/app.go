package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
)

// App is bound to the frontend; its exported methods become TypeScript calls.
type App struct {
	ctx context.Context
}

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// EngineStatus is what the window shows about the background engine.
type EngineStatus struct {
	Running bool   `json:"running"`
	Version string `json:"version,omitempty"`
	Message string `json:"message"`
}

// CheckEngine pings the engine. A fresh connection each time keeps this
// simple until the window needs a live progress feed.
func (a *App) CheckEngine() EngineStatus {
	name, err := ipc.PipeName()
	if err != nil {
		return EngineStatus{Message: "Couldn't work out the engine's address: " + err.Error()}
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	c, err := ipc.Dial(ctx, name)
	if err != nil {
		return EngineStatus{Message: "The download engine isn't running."}
	}
	defer c.Close()
	return statusFromPong(c.Call(ipc.Message{Type: "ping"}))
}

func statusFromPong(resp ipc.Message, err error) EngineStatus {
	if err == nil && resp.Error != "" {
		err = errors.New(resp.Error)
	}
	if err != nil {
		return EngineStatus{Message: "The engine didn't answer properly: " + err.Error()}
	}
	var p struct {
		Version string `json:"version"`
	}
	json.Unmarshal(resp.Payload, &p)
	return EngineStatus{Running: true, Version: p.Version, Message: "The download engine is running."}
}

// ReceivedMedia lists the streams the browser extension has sent, newest
// first. Downloading them comes in a later phase.
func (a *App) ReceivedMedia() ([]media.Summary, error) {
	return summariesFrom(a.call(ipc.Message{Type: "media.list"}))
}

// CheckServer asks the engine to probe the server of a received stream. The
// engine gives up after 30 seconds, so this doesn't hang.
func (a *App) CheckServer(id uint64) (*probe.Result, error) {
	p, _ := json.Marshal(map[string]uint64{"id": id})
	return probeFrom(a.call(ipc.Message{Type: "media.probe", Payload: p}))
}

func (a *App) call(m ipc.Message) (ipc.Message, error) {
	name, err := ipc.PipeName()
	if err != nil {
		return ipc.Message{}, err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Second)
	defer cancel()
	c, err := ipc.Dial(ctx, name)
	if err != nil {
		return ipc.Message{}, errors.New("the download engine isn't running")
	}
	defer c.Close()
	return c.Call(m)
}

func probeFrom(resp ipc.Message, err error) (*probe.Result, error) {
	if err == nil && resp.Error != "" {
		err = errors.New(resp.Error)
	}
	if err != nil {
		return nil, err
	}
	var out probe.Result
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func summariesFrom(resp ipc.Message, err error) ([]media.Summary, error) {
	if err == nil && resp.Error != "" {
		err = errors.New(resp.Error)
	}
	if err != nil {
		return nil, err
	}
	var out []media.Summary
	if err := json.Unmarshal(resp.Payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}
