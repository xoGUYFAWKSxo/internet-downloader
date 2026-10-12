# Internet Downloader (working name)

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

## Layout (planned; built so far: `cmd/engine`, `cmd/nmhost`, `internal/ipc`, `internal/nativemsg`, `internal/testserver`)
One Go module (Windows 10/11 x64, runs as the normal user, never elevated):
- `cmd/engine/`: `engine.exe`. Starts at login, owns the tray icon, does all downloads, and is the only process that writes files or the database.
- `cmd/nmhost/`: `nmhost.exe`. Native Messaging relay: browser JSON in, then out over the pipe to the engine. No download logic.
- `ui/`: `ui.exe`. A Wails v2 app (WebView2 + TypeScript frontend) that talks to the engine over the pipe.
- `internal/`: engine packages (probe, transfer, storage, media, journal, ipc).
- `extension/`: MV3 extension for Chrome and Edge.
- `installer/`: installer, Native Messaging registration (HKCU, Chrome and Edge), bundled `ffmpeg.exe`.
- IPC: JSON over the named pipe `\\.\pipe\InternetDownloader-<user SID>`. Only the current user can open it.
Update this section when the real layout exists.

## Build and test
Go is at `C:\Program Files\Goin` (add it to PATH in Git Bash: `export PATH="/c/Program Files/Go/bin:$PATH"`). Wails CLI: `%USERPROFILE%\goin\wails.exe`.
```
go vet ./...
go build -o bin/ ./cmd/...
go test -count=1 ./...
```
- `go vet` must be clean. CI runs the `build-and-test` check (`.github/workflows/build.yml`) on every PR.
- Network tests use `internal/testserver` (local HTTP/1.1 with and without ranges, HTTP/2, HTTP/3). Never hit the real internet in tests.

## Workflow
- Branch, then open a PR. Merge rules: see DECISIONS.md once the repo exists.
- Commit as `xoGUYFAWKSxo <206061236+xoGUYFAWKSxo@users.noreply.github.com>`. Never commit with a personal email address.
- Commit messages: write them to a file and use `git commit -F`, in UTF-8 without a BOM.
- Update `STATUS.md` when a PR merges. Add to `DECISIONS.md` when the user makes a choice.

## Rules for engine code
- Never report a download as complete unless every byte range is committed and the size (and checksum, when the server gives one) matches.
- A failed or skipped step is reported as an error with a message, never as success.
- Respect the server: honour `Retry-After`, 429 and 503, and back off. The connection limits above are ceilings, not targets.
- Cookies and auth headers from the extension stay in memory or the local database only. Never log them in plain text.
- No DRM circumvention. Encrypted streams (Widevine, PlayReady, FairPlay) are detected and refused with a clear message. AES-128 HLS with a key the page itself serves is fine.
- Show measured numbers only (speed, ETA, RTT). Never invent a value for the UI.

## Style
- Keep comments sparse: explain why, not what. Match the surrounding code.
- User-facing text is plain English. No marketing claims in README or UI.
- Add a test for every bug fix.
