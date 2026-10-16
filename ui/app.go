package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"io"
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
	// power tells that this is the countdown before sleep or turning off.
	power bool
}

// The progress window's size, without and with its details.
const (
	progressWidth   = 520
	progressHeight  = 358
	progressDetails = 618
	askWidth        = 560
	askHeight       = 318
	optionsWidth    = 540
	optionsHeight   = 420
	powerWidth      = 440
	powerHeight     = 170
)

func (a *App) startup(ctx context.Context) { a.ctx = ctx }

// shutdown forgets the link when its box is closed without an answer. After
// an answer the engine has forgotten it already.
func (a *App) shutdown(context.Context) {
	if a.ask != 0 {
		a.call(ipc.Message{Type: "asks.cancel", Payload: idPayload(a.ask)})
	}
	// Closing the countdown is cancelling it.
	if a.power {
		a.call(ipc.Message{Type: "power.cancel"})
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

// SetDownloadLimit sets one download's own speed limit in KB/s; 0 takes it
// off. The limit Options sets for all downloads applies as well.
func (a *App) SetDownloadLimit(id uint64, kb int64) error {
	p, _ := json.Marshal(map[string]any{"id": id, "kb": kb})
	return decode(a.call(ipc.Message{Type: "downloads.limit", Payload: p}))(nil)
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

// AddURLs downloads several addresses at once into the folder last used,
// with no box, or with later adds them to the queue. It gives how many were
// added; an error says how many weren't.
func (a *App) AddURLs(urls []string, later bool) (int, error) {
	added := 0
	var last error
	for _, u := range urls {
		p, _ := json.Marshal(map[string]any{"url": u, "later": later})
		if err := decode(a.call(ipc.Message{Type: "downloads.add", Payload: p}))(nil); err != nil {
			last = err
			continue
		}
		added++
	}
	switch left := len(urls) - added; {
	case left == 1 && len(urls) == 1:
		return added, last
	case left == 1:
		return added, fmt.Errorf("1 of the %d links wasn't added: %v", len(urls), last)
	case left > 1:
		return added, fmt.Errorf("%d of the %d links weren't added. The last one: %v", left, len(urls), last)
	}
	return added, nil
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
// to the list to start with Resume. sum, if not "", is the SHA-256 the file
// must have. It returns the download's id.
func (a *App) StartAsk(id uint64, dir, name string, later bool, sum string) (uint64, error) {
	p, _ := json.Marshal(map[string]any{"id": id, "dir": dir, "name": name, "later": later, "sha256": sum})
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

// releasesPage is where a new version is downloaded from.
const releasesPage = "https://github.com/xoGUYFAWKSxo/internet-downloader/releases/latest"

// NewerVersion is a newer version of the app the engine found, or "". An
// engine from before 0.1.8 doesn't check, which also gives "".
func (a *App) NewerVersion() string {
	var out struct {
		Version string `json:"version"`
	}
	if err := decode(a.call(ipc.Message{Type: "update.get"}))(&out); err != nil {
		return ""
	}
	return out.Version
}

// OpenReleasesPage opens the download page in the default browser.
func (a *App) OpenReleasesPage() error {
	verb, _ := windows.UTF16PtrFromString("open")
	page, _ := windows.UTF16PtrFromString(releasesPage)
	if err := windows.ShellExecute(0, verb, page, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		return fmt.Errorf("Windows couldn't open the download page: %w", err)
	}
	return nil
}

// Target is what the Download File Info box says about where a file would
// go: whether a file of that name is already there, and the free space on
// that drive (-1 when unknown).
type Target struct {
	Exists bool  `json:"exists"`
	Free   int64 `json:"free"`
}

// CheckTarget looks at the folder and name chosen in the box.
func (a *App) CheckTarget(dir, name string) Target {
	t := Target{Free: -1}
	if !filepath.IsAbs(dir) {
		return t
	}
	if name != "" && filepath.Base(name) == name {
		_, err := os.Lstat(filepath.Join(dir, name))
		t.Exists = err == nil
	}
	// The folder may not exist yet; it is made on Start, so ask its drive.
	for d := dir; ; d = filepath.Dir(d) {
		var free uint64
		p, err := windows.UTF16PtrFromString(d)
		if err == nil && windows.GetDiskFreeSpaceEx(p, &free, nil, nil) == nil {
			t.Free = int64(free)
			break
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	return t
}

// GrowWindow makes the window taller by px, for a box whose warnings no
// longer fit.
func (a *App) GrowWindow(px int) {
	if px <= 0 || px > 400 {
		return
	}
	w, h := runtime.WindowGetSize(a.ctx)
	runtime.WindowSetSize(a.ctx, w, h+px)
}

// OptionsTab is the tab the Options window opened at, or "" in other
// windows.
func (a *App) OptionsTab() string { return a.options }

// PowerWindow tells that this is the countdown before the computer sleeps
// or turns off, when the queue has finished.
func (a *App) PowerWindow() bool { return a.power }

// PowerInfo is what will happen when the countdown ends, and in how many
// seconds; Action is "" once it has been cancelled or done.
func (a *App) PowerInfo() (PowerInfo, error) {
	var out PowerInfo
	return out, decode(a.call(ipc.Message{Type: "power.get"}))(&out)
}

// CancelPower stops the countdown.
func (a *App) CancelPower() error {
	return decode(a.call(ipc.Message{Type: "power.cancel"}))(nil)
}

// PowerNow sleeps or turns off without waiting for the countdown.
func (a *App) PowerNow() error {
	return decode(a.call(ipc.Message{Type: "power.now"}))(nil)
}

type PowerInfo struct {
	Action string `json:"action,omitempty"`
	Left   int    `json:"left,omitempty"`
}

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

// FileSHA256 works out the SHA-256 of a finished download, in lower case.
func (a *App) FileSHA256(path string) (string, error) {
	if err := existingFile(path); err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("couldn't read the file: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// maxLogShown is how much of the end of the engine's log Options shows.
const maxLogShown = 256 << 10

// EngineLog gives the end of the download engine's log, whole lines only.
func (a *App) EngineLog() (string, error) {
	return logTail(launch.LogPath(), maxLogShown)
}

func logTail(path string, most int64) (string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", errors.New("There is no log yet: the engine writes one once the window or the browser has started it.")
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	from := max(0, fi.Size()-most)
	b := make([]byte, fi.Size()-from)
	if _, err := f.ReadAt(b, from); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if from > 0 {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return string(b), nil
}

// ShowLog opens the folder with the engine's log, the log selected.
func (a *App) ShowLog() error {
	return a.ShowInFolder(launch.LogPath())
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
