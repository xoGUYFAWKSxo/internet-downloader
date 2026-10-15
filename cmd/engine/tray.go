package main

import (
	_ "embed"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
)

//go:embed tray.ico
var trayIcon []byte

// runTray shows the notification-area icon and blocks until Quit is chosen
// or the pipe closes. Downloads keep going while the window is closed; the
// icon is how the user knows the engine is there and how they stop it.
func runTray(e *engine, l net.Listener, served <-chan error) {
	systray.Run(func() {
		systray.SetIcon(trayIcon)
		systray.SetTooltip(trayTip(0))
		systray.SetOnTapped(openWindow)
		open := systray.AddMenuItem("Open Internet Downloader", "")
		if _, err := os.Stat(windowPath()); err != nil {
			open.Disable()
		}
		folder := systray.AddMenuItem("Open Downloads folder", "")
		systray.AddSeparator()
		ask := systray.AddMenuItemCheckbox("Ask where to save links from the browser", "Show the Download File Info box", e.setting(setAsk) == "1")
		done := systray.AddMenuItemCheckbox("Show a box when a download finishes", "", e.setting(setDone) == "1")
		toggle := func(item *systray.MenuItem, key string) {
			v := "1"
			if item.Checked() {
				v = "0"
			}
			if err := e.setSetting(key, v); err != nil {
				fmt.Fprintln(e.log, "engine: couldn't save the setting:", err)
			}
			syncCheck(item, e.setting(key) == "1")
		}
		systray.AddSeparator()
		quit := systray.AddMenuItem("Quit", "Stop the downloader")
		go func() {
			tick := time.NewTicker(2 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-open.ClickedCh:
					openWindow()
				case <-folder.ClickedCh:
					exec.Command("explorer.exe", e.dir).Start()
				case <-ask.ClickedCh:
					toggle(ask, setAsk)
				case <-done.ClickedCh:
					toggle(done, setDone)
				case <-quit.ClickedCh:
					if n := e.running(); n > 0 && !confirm(quitQuestion(n)) {
						continue
					}
					systray.Quit()
					return
				case err := <-served:
					fmt.Fprintln(e.log, "engine: pipe closed:", err)
					systray.Quit()
					return
				case <-tick.C:
					systray.SetTooltip(trayTip(e.running()))
					// The windows can change these too.
					syncCheck(ask, e.setting(setAsk) == "1")
					syncCheck(done, e.setting(setDone) == "1")
				}
			}
		}()
	}, func() {
		l.Close()
		e.stopAll(10 * time.Second)
	})
}

func trayTip(running int) string {
	switch running {
	case 0:
		return "Internet Downloader"
	case 1:
		return "Internet Downloader: 1 download running"
	}
	return fmt.Sprintf("Internet Downloader: %d downloads running", running)
}

func quitQuestion(running int) string {
	if running == 1 {
		return "A download is still running. Quit anyway?\n\nA file download is paused and can be resumed later; a stream has to start again."
	}
	return fmt.Sprintf("%d downloads are still running. Quit anyway?\n\nFile downloads are paused and can be resumed later; streams have to start again.", running)
}

func syncCheck(item *systray.MenuItem, on bool) {
	switch {
	case on && !item.Checked():
		item.Check()
	case !on && item.Checked():
		item.Uncheck()
	}
}

// showWindow opens one of ui.exe's small windows: --ask=ID for the Download
// File Info box, --progress=ID for a download's progress.
func showWindow(args ...string) error {
	p := windowPath()
	if _, err := os.Stat(p); err != nil {
		return err
	}
	cmd := exec.Command(p, args...)
	cmd.Dir = filepath.Dir(p)
	return cmd.Start()
}

// openWindow starts ui.exe from the engine's folder. A second copy hands
// over to the open window, so this also brings it to the front.
func openWindow() {
	cmd := exec.Command(windowPath())
	cmd.Dir = filepath.Dir(cmd.Path)
	cmd.Start()
}

func windowPath() string {
	self, _ := os.Executable()
	return filepath.Join(filepath.Dir(self), "ui.exe")
}

func confirm(question string) bool {
	const yesNo, warning, yes = 0x4, 0x30, 6
	title, _ := windows.UTF16PtrFromString("Internet Downloader")
	text, _ := windows.UTF16PtrFromString(question)
	r, _ := windows.MessageBox(0, text, title, yesNo|warning)
	return r == yes
}
