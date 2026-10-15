package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"

	"github.com/xoGUYFAWKSxo/internet-downloader/internal/fetch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/ipc"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/launch"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/media"
	"github.com/xoGUYFAWKSxo/internet-downloader/internal/probe"
)

// App is bound to the frontend; its exported methods become TypeScript calls.
type App struct {
	ctx context.Context
	// progress is the download whose progress window this is, or 0 for the
	// main window.
	progress uint64
	// ask is the link whose Download File Info box this is, or 0.
	ask uint64
	// options is the tab the Options window opened at, or "" in others.
	options string
}

// The progress window's size, without and with its details.
const (
	progressWidth   = 520
	progressHeight  = 330
	progressDetails = 590
	askWidth        = 560
	askHeight       = 290
	optionsWidth    = 540
	optionsHeight   = 400
)

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// shutdown forgets the link when its box is closed without an answer. After
// an answer the engine has forgotten it already.
func (a *App) shutdown(context.Context) {
	if a.ask != 0 {
		a.call(ipc.Message{Type: "asks.cancel", Payload: idPayload(a.ask)})
	}
}

func (a *App) show() {
	runtime.WindowUnminimise(a.ctx)
	runtime.Show(a.ctx)
	runtime.WindowSetAlwaysOnTop(a.ctx, true)
	runtime.WindowSetAlwaysOnTop(a.ctx, false)
}

// EngineStatus is what the window shows about the background engine.
type EngineStatus struct {
	Running bool   `json:"running"`
	Version string `json:"version,omitempty"`
	Message string `json:"message"`
}

// CheckEngine pings the engine, starting it first if it isn't running. A
// fresh connection each time keeps this simple until the window needs a live
// progress feed.
func (a *App) CheckEngine() EngineStatus {
	name, err := ipc.PipeName()
	if err != nil {
		return EngineStatus{Message: "Couldn't work out the engine's address: " + err.Error()}
	}
	if err := launch.Ensure(a.ctx, name); err != nil {
		return EngineStatus{Message: "The download engine isn't running, and starting it failed: " + err.Error()}
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

// ResumeDownload carries on with a paused or failed download.
func (a *App) ResumeDownload(id uint64) error {
	return decode(a.call(ipc.Message{Type: "downloads.resume", Payload: idPayload(id)}))(nil)
}

// PauseDownload stops a running download, keeping what is in when it can be
// resumed.
func (a *App) PauseDownload(id uint64) error {
	return decode(a.call(ipc.Message{Type: "downloads.pause", Payload: idPayload(id)}))(nil)
}

// RemoveDownloads takes downloads off the list, and with deleteFiles also
// deletes the finished files.
func (a *App) RemoveDownloads(ids []uint64, deleteFiles bool) error {
	p, _ := json.Marshal(map[string]any{"ids": ids, "deleteFiles": deleteFiles})
	return decode(a.call(ipc.Message{Type: "downloads.remove", Payload: p}))(nil)
}

// Added is what AddURL did: started download ID, or opened the Download
// File Info box for it.
type Added struct {
	ID    uint64 `json:"id"`
	Asked bool   `json:"asked"`
}

// AddURL opens the Download File Info box for a web address, or, with the
// box turned off, downloads it into the folder last used.
func (a *App) AddURL(u string) (Added, error) {
	p, _ := json.Marshal(map[string]string{"url": u})
	set, err := a.Settings()
	if err != nil {
		return Added{}, err
	}
	var out Added
	if set["ask"] != "1" {
		return out, decode(a.call(ipc.Message{Type: "downloads.add", Payload: p}))(&out)
	}
	if err := decode(a.call(ipc.Message{Type: "asks.add", Payload: p}))(&out); err != nil {
		return out, err
	}
	out.Asked = true
	return out, a.open(fmt.Sprintf("--ask=%d", out.ID))
}

// AskID is the link this Download File Info box is for, or 0 in other
// windows.
func (a *App) AskID() uint64 { return a.ask }

// AskInfo gives what the box shows: the server's answer comes in while it
// is open.
func (a *App) AskInfo(id uint64) (fetch.AskInfo, error) {
	var out fetch.AskInfo
	return out, decode(a.call(ipc.Message{Type: "asks.get", Payload: idPayload(id)}))(&out)
}

// StartAsk downloads the link into dir under name, or with later adds it
// to the list to start with Resume. It returns the download's id.
func (a *App) StartAsk(id uint64, dir, name string, later bool) (uint64, error) {
	p, _ := json.Marshal(map[string]any{"id": id, "dir": dir, "name": name, "later": later})
	var out idOnly
	return out.ID, decode(a.call(ipc.Message{Type: "asks.start", Payload: p}))(&out)
}

// ChooseFolder asks for a folder to save into, starting at current. It
// gives "" when the user cancels.
func (a *App) ChooseFolder(current string) (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Save into", DefaultDirectory: current, CanCreateDirectories: true})
}

// Settings gives the engine's settings: "ask" and "done" ("1" or "0") and
// "dir", the folder last saved into.
func (a *App) Settings() (map[string]string, error) {
	var out map[string]string
	return out, decode(a.call(ipc.Message{Type: "settings.get"}))(&out)
}

func (a *App) SetSetting(key, value string) error {
	p, _ := json.Marshal(map[string]string{"key": key, "value": value})
	return decode(a.call(ipc.Message{Type: "settings.set", Payload: p}))(nil)
}

// SaveSettings saves the Options window's settings, all or none.
func (a *App) SaveSettings(set map[string]string) error {
	p, _ := json.Marshal(set)
	return decode(a.call(ipc.Message{Type: "settings.save", Payload: p}))(nil)
}

// OptionsTab is the tab the Options window opened at, or "" in other
// windows.
func (a *App) OptionsTab() string { return a.options }

// ShowOptions opens the Options window at tab, or brings it forward.
func (a *App) ShowOptions(tab string) error {
	return a.open("--options=" + tab)
}

// QueueInfo says whether the queue is running, and the speed limit.
func (a *App) QueueInfo() (fetch.QueueInfo, error) {
	var out fetch.QueueInfo
	return out, decode(a.call(ipc.Message{Type: "queue.get"}))(&out)
}

func (a *App) StartQueue() error { return decode(a.call(ipc.Message{Type: "queue.start"}))(nil) }

// StopQueue stops the queue, pausing its running downloads.
func (a *App) StopQueue() error { return decode(a.call(ipc.Message{Type: "queue.stop"}))(nil) }

// AddToQueue puts stopped downloads at the end of the queue.
func (a *App) AddToQueue(ids []uint64) error {
	p, _ := json.Marshal(map[string]any{"ids": ids})
	return decode(a.call(ipc.Message{Type: "queue.add", Payload: p}))(nil)
}

func (a *App) RemoveFromQueue(ids []uint64) error {
	p, _ := json.Marshal(map[string]any{"ids": ids})
	return decode(a.call(ipc.Message{Type: "queue.remove", Payload: p}))(nil)
}

// MoveInQueue moves a download one place earlier (by < 0) or later.
func (a *App) MoveInQueue(id uint64, by int) error {
	p, _ := json.Marshal(map[string]any{"id": id, "by": by})
	return decode(a.call(ipc.Message{Type: "queue.move", Payload: p}))(nil)
}

type idOnly struct {
	ID uint64 `json:"id"`
}

// ProgressID is the download this window shows, or 0 in the main window.
func (a *App) ProgressID() uint64 { return a.progress }

// DownloadDetail gives what the progress window shows of a download.
func (a *App) DownloadDetail(id uint64) (fetch.Detail, error) {
	var out fetch.Detail
	return out, decode(a.call(ipc.Message{Type: "downloads.detail", Payload: idPayload(id)}))(&out)
}

// ShowProgress opens a download's progress window, or brings it forward if
// it is open already.
func (a *App) ShowProgress(id uint64) error {
	return a.open(fmt.Sprintf("--progress=%d", id))
}

// open starts another window of this program.
func (a *App) open(arg string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := exec.Command(self, arg).Start(); err != nil {
		return fmt.Errorf("couldn't open the window: %w", err)
	}
	return nil
}

// ShowDetails makes the progress window tall enough for its details, or
// short again.
func (a *App) ShowDetails(show bool) {
	h := progressHeight
	if show {
		h = progressDetails
	}
	runtime.WindowSetSize(a.ctx, progressWidth, h)
}

// OpenFile opens a finished download with its usual program.
func (a *App) OpenFile(path string) error {
	if err := existingFile(path); err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(path))
	if err := windows.ShellExecute(0, verb, file, nil, dir, windows.SW_SHOWNORMAL); err != nil {
		return fmt.Errorf("Windows couldn't open the file: %w", err)
	}
	return nil
}

func existingFile(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("not a file path")
	}
	if _, err := os.Stat(path); err != nil {
		return errors.New("the file isn't there any more")
	}
	return nil
}

// ShowInFolder opens Explorer with the file selected.
func (a *App) ShowInFolder(path string) error {
	if err := existingFile(path); err != nil {
		return err
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
