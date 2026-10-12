// Command ui is the desktop window. It holds no download logic: everything
// goes to the engine over the IPC pipe, so closing the window never stops a
// download.
package main

import (
	"embed"
	"fmt"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := &App{}
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
