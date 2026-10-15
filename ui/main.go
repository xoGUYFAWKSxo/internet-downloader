// Command ui is the desktop window. It holds no download logic: everything
// goes to the engine over the IPC pipe, so closing the window never stops a
// download.
package main

import (
	"context"
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
	app := &App{progress: idArg(os.Args[1:], "--progress="), ask: idArg(os.Args[1:], "--ask=")}
	switch {
	case app.progress != 0:
		runSmall(app, "Download progress", progressWidth, progressHeight, fmt.Sprintf("progress.%d", app.progress))
		return
	case app.ask != 0:
		runSmall(app, "Download File Info", askWidth, askHeight, fmt.Sprintf("ask.%d", app.ask))
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

// idArg reads --progress=ID, which opens one download's progress window
// instead of the list, or --ask=ID, the Download File Info box of a link.
func idArg(args []string, prefix string) uint64 {
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, prefix); ok {
			id, _ := strconv.ParseUint(v, 10, 64)
			return id
		}
	}
	return 0
}

// runSmall shows a download's progress window or a link's Download File
// Info box. Each is a process of its own, as Wails gives each process one
// window; opening it again for the same download or link brings the open
// one forward.
func runSmall(app *App, title string, width, height int, id string) {
	err := wails.Run(&options.App{
		Title:         title,
		Width:         width,
		Height:        height,
		DisableResize: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup: app.startup,
		// Opened by the engine, a background process, Windows can leave
		// the window minimised behind others; bring it up once it's drawn.
		OnDomReady: func(context.Context) { app.show() },
		OnShutdown: app.shutdown,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "com.xoguyfawksxo.internet_downloader." + id,
			OnSecondInstanceLaunch: func(options.SecondInstanceData) { app.show() },
		},
		Bind: []interface{}{app},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ui:", err)
		os.Exit(1)
	}
}
