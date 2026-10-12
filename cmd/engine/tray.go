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
		return "A download is still running. Quit and stop it?"
	}
	return fmt.Sprintf("%d downloads are still running. Quit and stop them?", running)
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
