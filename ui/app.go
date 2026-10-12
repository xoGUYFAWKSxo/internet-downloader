package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
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
// first.
func (a *App) ReceivedMedia() ([]media.Summary, error) {
	return summariesFrom(a.call(ipc.Message{Type: "media.list"}))
}

// CheckServer asks the engine to probe the server of a received stream. The
// engine gives up after 30 seconds, so this doesn't hang.
func (a *App) CheckServer(id uint64) (*probe.Result, error) {
	p, _ := json.Marshal(map[string]uint64{"id": id})
	return probeFrom(a.call(ipc.Message{Type: "media.probe", Payload: p}))
}

// Download asks the engine to download a received item into the Downloads
// folder. It returns the download's id at once; Downloads shows progress.
func (a *App) Download(id uint64) (uint64, error) {
	var out struct {
		ID uint64 `json:"id"`
	}
	err := decode(a.call(ipc.Message{Type: "media.download", Payload: idPayload(id)}))(&out)
	return out.ID, err
}

// Downloads lists this session's downloads, newest first.
func (a *App) Downloads() ([]fetch.Status, error) {
	var out []fetch.Status
	return out, decode(a.call(ipc.Message{Type: "downloads.list"}))(&out)
}

func (a *App) CancelDownload(id uint64) error {
	return decode(a.call(ipc.Message{Type: "downloads.cancel", Payload: idPayload(id)}))(nil)
}

// ShowInFolder opens Explorer with the file selected.
func (a *App) ShowInFolder(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("not a file path")
	}
	if _, err := os.Stat(path); err != nil {
		return errors.New("the file isn't there any more")
	}
	// Explorer's exit code is 1 even when it worked, so it isn't checked.
	exec.Command("explorer.exe", "/select,", path).Start()
	return nil
}

func idPayload(id uint64) json.RawMessage {
	p, _ := json.Marshal(map[string]uint64{"id": id})
	return p
}

// decode turns an engine reply into an error or a filled value.
func decode(resp ipc.Message, err error) func(v any) error {
	return func(v any) error {
		if err == nil && resp.Error != "" {
			err = errors.New(resp.Error)
		}
		if err != nil || v == nil {
			return err
		}
		return json.Unmarshal(resp.Payload, v)
	}
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
