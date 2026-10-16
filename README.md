# Galactic Downloader

A download manager for Windows 10 and 11, with a browser extension for Chrome, Edge and Firefox. It is early software: it works, but expect rough edges, and please report problems on the [Issues](https://github.com/xoGUYFAWKSxo/internet-downloader/issues) page.

**Download:** [latest release](https://github.com/xoGUYFAWKSxo/internet-downloader/releases/latest)

## What it does

- Downloads files over several connections at once: HTTP/3 or HTTP/2 when the server supports it, otherwise up to 4 HTTP/1.1 connections, or one when the server allows nothing else.
- Resumes after a dropped connection, a closed app or a crash, fetching only the parts still missing, when the server supports it.
- With the extension, takes over the browser's own downloads of 1 MB and larger (you can raise the size, and leave chosen sites and file types to the browser), and adds "Download link with Galactic Downloader" to the right-click menu.
- Finds HLS and DASH video streams on a page and downloads them, joining separate video and sound into one file with FFmpeg (included). They resume by segment, like files.
- Add URL takes one link or a whole list, and links can be dropped onto the window. An optional SHA-256 checks the finished file.
- A window laid out like Internet Download Manager: categories (each can have its own folder), a progress window per download, a box asking where to save each link, a queue, a scheduler, and a speed limit for all downloads or for one. The computer can sleep, hibernate, turn off or restart when the queue finishes, after a minute's countdown you can cancel.
- Marks downloaded files as from the internet, as browsers do. Keeps the computer awake while downloading, and checks once a day for a new version; both can be turned off in Options.
- Copy-protected (DRM) streams are refused. YouTube's own player isn't supported.

See `STATUS.md` for progress, `DECISIONS.md` for design choices and `PRIVACY.md` for what the app and extension read and send.

License: GPL-3.0.

## Installing a release

Download `GalacticDownloader-<version>-setup.exe` from the Releases page and run it. It installs for your user only (no admin rights) into `%LOCALAPPDATA%\Programs\Galactic Downloader`, with FFmpeg included, and connects the browser relay for Chrome, Edge and Firefox. Uninstall it from Windows Settings > Apps; your download list and downloaded files are kept.

Windows SmartScreen will probably warn about the installer, as it isn't signed yet: click **More info**, then **Run anyway**.

Or download `GalacticDownloader-<version>-portable.zip` and unzip it anywhere, a USB stick included. It keeps its download list, log and settings in a `data` folder beside the programs, and writes nothing elsewhere; run `Connect browsers.cmd` in it once to use the extension with it. `portable.txt` in the folder says more. The installed and the portable version can't run at the same time.

The extension isn't in the browser stores yet. Each release has it zipped for Chrome/Edge and for Firefox; the release notes say how to load it.

## Building a release

`powershell -ExecutionPolicy Bypass -File installer\build-release.ps1 -Version 0.1.0` builds everything into `dist\release`. It needs Go, Node.js, the Wails CLI and Inno Setup 6, and downloads a pinned FFmpeg build once (checked against its SHA-256). Pushing a tag such as `v0.1.0` does the same on GitHub and puts the files in a draft release, to check and publish by hand.

## Trying the development build

Run these in PowerShell from the project folder (`cd` into it first). Needs Go and Node.js, and the Wails CLI for the window.

1. `node extension\build.mjs` builds the extension into `extension\dist\chrome` and `extension\dist\firefox`.
2. `go build -o bin\ ./cmd/...` builds the engine and the browser relay.
3. `powershell -ExecutionPolicy Bypass -File installer\register-native-host.ps1` connects the relay to Chrome, Edge and Firefox, for your user only. Add `-Unregister` to remove it again.
4. `.\bin\engine.exe` starts the engine; its icon appears in the tray, and Quit is on its menu. For the window, run `wails build` in `ui` and copy `ui\build\bin\ui.exe` into `bin`.
5. Load the extension:
   - Chrome or Edge: open `chrome://extensions` (or `edge://extensions`), turn on Developer mode, click Load unpacked, and pick `extension\dist\chrome`.
   - Firefox: open `about:debugging`, click This Firefox, then Load Temporary Add-on, and pick `extension\dist\firefox\manifest.json`.
6. Click the extension's icon, then "Check connection to the app". It should say it's connected and show the version.
