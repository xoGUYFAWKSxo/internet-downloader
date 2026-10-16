# Galactic Downloader (working name)

Desktop download manager for Windows: a background download engine, a desktop UI, and a browser extension that finds media streams (HLS/DASH) and hands them to the app. The project is new (started 2026-10-12). Read `STATUS.md` for current state and `DECISIONS.md` before changing anything listed there. Don't re-read the whole codebase; open only the files the task touches.

## Engineering intent
1. **Protocol priority:** HTTP/3 (QUIC) multiplexing, then HTTP/2 multiplexing, then HTTP/1.1 multi-connection range requests, then a single linear stream when the server has no range support.
2. **I/O:** pre-allocate the target file as soon as the size is known, and write every part at its byte offset into that one file. No temp part-files that get joined at the end.
3. **Media:** the extension sends a full context object (URL, cookies, headers, Referer, manifest text). HLS/DASH fragments download in parallel and are muxed into one output file.
4. **Recovery:** progress is logged per byte range in a local SQLite database (WAL mode). A resumed download restarts only the ranges that weren't committed.

## System constants
- Default max logical streams per download: 8 (HTTP/2 and HTTP/3).
- Default max physical TCP connections per download (HTTP/1.1 fallback): 4.
- One pre-allocated output file per download. Never write multiple part-files.

## Layout (planned; built so far: `cmd/engine`, `cmd/nmhost`, `internal/ipc`, `internal/media`, `internal/nativemsg`, `internal/probe`, `internal/fetch`, `internal/outfile`, `internal/stream`, `internal/testserver`, `internal/launch`, `internal/journal`, `extension/`, `installer/install.ps1`, `installer/register-native-host.ps1`, `installer/build-release.ps1`, `installer/setup.iss`, `installer/portable/`, `ui/`)
Windows 10/11 x64, runs as the normal user, never elevated. The root Go module holds the engine, relay and `internal/`. `ui/` is a nested module (Wails needs its own `go.mod`) that uses the root module through a `replace ../` line:
- `cmd/engine/`: `engine.exe`. Started on demand by the window or the relay (`internal/launch`, from the same folder), keeps running after they exit, owns the tray icon (Quit stops it), does all downloads, and is the only process that writes files or the database.
- `cmd/nmhost/`: `nmhost.exe`. Native Messaging relay: browser JSON in, then out over the pipe to the engine. No download logic.
- `ui/`: `ui.exe`. A Wails v2 app (WebView2 + TypeScript frontend) that talks to the engine over the pipe.
- `internal/`: engine packages (probe, fetch, outfile, stream, media, journal, ipc, launch). The saved download list and range log: `%LOCALAPPDATA%\InternetDownloader\downloads.db`.
- `extension/`: MV3 extension for Chrome, Firefox and Edge. Firefox MV3 needs `background.scripts` (not `service_worker`) and `browser_specific_settings.gecko.id`.
- FFmpeg (for joining stream tracks) is looked up next to `engine.exe`, then on PATH; tests use a stand-in.
- `installer/`: the release build (`build-release.ps1` stages everything, with a pinned, checksummed FFmpeg, then `setup.iss` makes the Inno Setup installer), the development install (`install.ps1`) and Native Messaging registration (`register-native-host.ps1`: HKCU, Chrome, Edge and Firefox).
- IPC: JSON over the named pipe `\\.\pipe\InternetDownloader-<user SID>`. Only the current user can open it.
Update this section when the real layout exists.

## Build and test
Go is at `C:\Program Files\Go\bin` (add it to PATH in Git Bash: `export PATH="/c/Program Files/Go/bin:$PATH"`). Wails CLI: `%USERPROFILE%\go\bin\wails.exe`.
```
go vet ./...
go build -o bin/ ./cmd/...
go test -count=1 ./...
node --test extension/build.test.mjs extension/detect.test.mjs extension/describe.test.mjs extension/background.test.mjs
bin/engine.exe -probe URL       # check one server: HTTP version, ranges, size, download plan
bin/engine.exe -get URL -dir D  # download one file, or an HLS/DASH stream (.m3u8/.mpd), default folder: Downloads
go test ./internal/outfile -run - -bench . -benchtime 3x   # disk write methods compared
node extension/build.mjs        # unpacked builds in extension/dist/{chrome,firefox}
cd ui; wails build -clean; go test ./...   # window -> ui/build/bin/ui.exe
wails dev -browser=false        # in ui/: the window at http://localhost:34115 with live Go calls, handy for checking the UI
powershell -File installer/register-native-host.ps1   # register the relay (bin/nmhost.exe) for Chrome, Edge, Firefox
powershell -File installer/install.ps1   # build, install to %LOCALAPPDATA%\Programs\Galactic Downloader, shortcuts, relay from there
powershell -File installer/build-release.ps1 -Version 0.1.0   # release into dist/release: setup.exe, extension zips, SHA256SUMS (needs Inno Setup 6)
```
- `go vet` must be clean. CI runs the `build-and-test` check (`.github/workflows/build.yml`) on every PR. The `release` workflow builds the installer on PRs that touch the release build, and on a `v*` tag into a draft release; publishing a release is the user's call.
- Network tests use `internal/testserver` (local HTTP/1.1 with and without ranges, HTTP/2, HTTP/3). Never hit the real internet in tests.

## Workflow
- Work in `xoGUYFAWKSxo/internet-downloader` (public, remote `origin`). Branch, then open a PR. Once the `build-and-test` check passes green, merge it yourself with `gh pr merge N --merge`; no need to ask. Never merge a PR whose CI failed or is still running. Wait with `gh run watch <id> --exit-status`.
- Commit as `xoGUYFAWKSxo <206061236+xoGUYFAWKSxo@users.noreply.github.com>`. Never commit with a personal email address.
- Commit messages: write them to a file and use `git commit -F`, in UTF-8 without a BOM.
- Update `STATUS.md` when a PR merges. Add to `DECISIONS.md` when the user makes a choice.
- Release notes on a draft: `gh release edit vX.Y.Z --notes-file FILE`, never an API PATCH without `tag_name`. That PATCH swaps the draft's tag for an `untagged-…` placeholder, and publishing then creates a stray tag at main's head (happened to 0.1.1-0.1.4). After the user publishes, check `gh api repos/xoGUYFAWKSxo/internet-downloader/releases/latest` names the new `vX.Y.Z`.

## Rules for engine code
- Never report a download as complete unless every byte range is committed and the size (and checksum, when the server gives one) matches.
- A failed or skipped step is reported as an error with a message, never as success.
- Respect the server: honour `Retry-After`, 429 and 503, and back off. The connection limits above are ceilings, not targets.
- Cookies and auth headers from the extension stay in memory or the local database only. Never log them in plain text.
- No DRM circumvention. Encrypted streams (Widevine, PlayReady, FairPlay) are detected and refused with a clear message. AES-128 HLS with a key the page itself serves is fine.
- Show measured numbers only (speed, ETA, RTT). Never invent a value for the UI.

## Compatibility with the extension
The extension updates itself from the browser stores, and each change there means a new store review; people update the app themselves. Any app version must work with any released extension version, and the other way round.
- Never change the relay name `com.xoguyfawksxo.internet_downloader`, and never remove an ID from the relay's allowed list (`ChromeExtensionIds` and `FirefoxExtensionId` in `installer/register-native-host.ps1`). The installer's AppId, the data folder and the pipe name stay the same too.
- Never break the messages the extension sends (`toApp`, `sendItem` and `fileContext` in `extension/src/background.js`: the `type` values and the `media` fields). The engine and relay keep accepting every released form: new fields are optional, and existing ones are never removed, renamed or given a new meaning. The same goes for the replies the extension reads (`ok`, `error`, `payload`).
- An extension change must still work with the oldest supported app: check what the app says before using a new feature, and fall back when it isn't there.
- Prefer putting new work in the app. Change the extension only when the browser side must change.

## Style
- Keep comments sparse: explain why, not what. Match the surrounding code.
- User-facing text is plain English. No marketing claims in README or UI.
- Add a test for every bug fix.
