# Internet Downloader

A download manager for Windows 10/11 (working name). Early development: nothing usable yet.

Planned: HTTP/3 and HTTP/2 multiplexed downloads with HTTP/1.1 range fallback, resume after drops or crashes, and a Chrome/Edge extension that hands HLS/DASH streams to the app. DRM-protected streams are not supported.

See `STATUS.md` for progress and `DECISIONS.md` for design choices.

License: GPL-3.0.

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
