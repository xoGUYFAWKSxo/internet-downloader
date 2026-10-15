# Galactic Downloader

A download manager for Windows 10/11 (working name). Early development: it works, but there is no published release yet.

Planned: HTTP/3 and HTTP/2 multiplexed downloads with HTTP/1.1 range fallback, resume after drops or crashes, and a Chrome/Edge extension that hands HLS/DASH streams to the app. DRM-protected streams are not supported.

See `STATUS.md` for progress and `DECISIONS.md` for design choices.

License: GPL-3.0.

## Installing a release

Download `GalacticDownloader-<version>-setup.exe` from the Releases page and run it. It installs for your user only (no admin rights) into `%LOCALAPPDATA%\Programs\Galactic Downloader`, with FFmpeg included, and connects the browser relay for Chrome, Edge and Firefox. Uninstall it from Windows Settings > Apps; your download list and downloaded files are kept.

The extension isn't in the browser stores yet. Each release has it zipped for Chrome/Edge and for Firefox: unzip the Chrome one and load it with Load unpacked (step 5 below).

## Building a release

`powershell -ExecutionPolicy Bypass -File installer\build-release.ps1 -Version 0.1.0` builds everything into `dist\release`. It needs Go, Node.js, the Wails CLI and Inno Setup 6, and downloads a pinned FFmpeg build once (checked against its SHA-256). Pushing a tag such as `v0.1.0` does the same on GitHub and puts the files in a draft release, to check and publish by hand.

## Trying the development build

Run these in PowerShell from the project folder (`cd` into it first). Needs Go and Node.js.

1. `node extension\build.mjs` builds the extension into `extension\dist\chrome` and `extension\dist\firefox`.
2. `go build -o bin\ ./cmd/...` builds the engine and the browser relay.
3. `powershell -ExecutionPolicy Bypass -File installer\register-native-host.ps1` connects the relay to Chrome, Edge and Firefox, for your user only. Add `-Unregister` to remove it again.
4. `.\bin\engine.exe` starts the engine. Leave the window open.
5. Load the extension:
   - Chrome or Edge: open `chrome://extensions` (or `edge://extensions`), turn on Developer mode, click Load unpacked, and pick `extension\dist\chrome`.
   - Firefox: open `about:debugging`, click This Firefox, then Load Temporary Add-on, and pick `extension\dist\firefox\manifest.json`.
6. Click the extension's icon, then "Check connection to the app". It should say it's connected and show the version.
