// Command ui is the desktop window. It holds no download logic: everything
// goes to the engine over the IPC pipe, so closing the window never stops a
// download.
package main

import (
	"embed"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := &App{progress: progressArg(os.Args[1:])}
	if app.progress != 0 {
		runProgress(app)
		return
	}
	err := wails.Run(&options.App{
		Title:     "Internet Downloader",
		Width:     1040,
		Height:    640,
		MinWidth:  720,
		MinHeight: 420,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup: app.startup,
		// Opening the app again (from the tray or a shortcut) shows this window.
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "com.xoguyfawksxo.internet_downloader.ui",
			OnSecondInstanceLaunch: func(options.SecondInstanceData) { app.show() },
		},
		Bind: []interface{}{app},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ui:", err)
		os.Exit(1)
	}
}

// progressArg reads --progress=ID, which opens one download's progress
// window instead of the list.
func progressArg(args []string) uint64 {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "--progress="); ok {
			id, _ := strconv.ParseUint(v, 10, 64)
			return id
		}
	}
	return 0
}

// runProgress shows a download's progress window. It is a process of its
// own, as Wails gives each process one window; opening it again for the
// same download brings the open one forward.
func runProgress(app *App) {
	err := wails.Run(&options.App{
		Title:         "Download progress",
		Width:         progressWidth,
		Height:        progressHeight,
		DisableResize: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup: app.startup,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               fmt.Sprintf("com.xoguyfawksxo.internet_downloader.progress.%d", app.progress),
			OnSecondInstanceLaunch: func(options.SecondInstanceData) { app.show() },
		},
		Bind: []interface{}{app},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ui:", err)
		os.Exit(1)
	}
}
