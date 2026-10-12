# Decisions

These are settled. Don't reopen them unless the user asks. Newest first.

- **2026-10-12: Merge on green CI.** Claude may merge its own PRs without asking once the `build-and-test` check passes. A failed or pending check is never merged. GitHub auto-merge is off; wait with `gh run watch <id> --exit-status`, then `gh pr merge N --merge`.
- **2026-10-12: The open questions were left to Claude's judgement.** The user asked Claude to pick. The five decisions below are those picks.
- **2026-10-12: Go for all native code.** That covers the engine, the Native Messaging relay and the UI backend, so there's one toolchain and one language. Go's standard library covers HTTP/1.1 and HTTP/2. HTTP/3 uses `quic-go`. Rust was dropped: it's harder to maintain for this team, and there was no measured need for it.
- **2026-10-12: Three processes.** (1) `engine.exe`: a background process that starts at login, owns the tray icon, does all downloads, and keeps running when the window is closed. (2) `nmhost.exe`: a small relay that the browser launches. It forwards the extension's JSON messages to the engine and holds no download logic. (3) `ui.exe`: a Wails v2 window (WebView2 + TypeScript), opened from the tray. The engine is the only process that writes files or the database. Why: downloads must survive the UI closing, and Chrome kills the Native Messaging host when the extension disconnects.
- **2026-10-12: IPC is JSON over a per-user named pipe** (`\\.\pipe\InternetDownloader-<user SID>`, via `go-winio`). The pipe's ACL allows only the current user. Nothing listens on a TCP port.
- **2026-10-12: Runs as the normal user, never elevated.** No service and no admin rights. That rules out `SetFileValidData`. Pre-allocation uses `SetEndOfFile`, plus `FSCTL_SET_SPARSE` on NTFS.
- **2026-10-12: Disk writes default to positioned writes (`WriteAt`), not mmap.** mmap stays an option to benchmark in Phase 3. It's adopted only if it's measurably faster with no loss of safety: with mmap, a full disk or a dropped network drive crashes the process instead of returning an error.
- **2026-10-12: FFmpeg is a bundled `ffmpeg.exe`, run as a subprocess** for muxing (`-c copy`, no re-encoding). There's no libav linking, so no cgo and no linking-license obligations. The installer ships the binary with its license and source link.
- **2026-10-12: Chrome and Edge first.** It's one MV3 extension, registered under both `HKCU\Software\Google\Chrome\NativeMessagingHosts` and `HKCU\Software\Microsoft\Edge\NativeMessagingHosts`. Firefox comes after v1.
- **2026-10-12: Public GitHub repo, GPL-3.0, same as the TuneUp Suite.** CI minutes are free, and the license works with a GPL FFmpeg build. The name is `internet-downloader` until the user picks one. Creating the repo still needs the user's go-ahead.
- **2026-10-12: Windows 10/11 x64 only for v1.** The Go code keeps OS-specific pieces (pipe, pre-allocation, tray, registry) behind small interfaces, so a later port doesn't need a rewrite.

- **2026-10-12: Protocol priority is HTTP/3, then HTTP/2, then HTTP/1.1 ranges, then a single stream.** Multiplex on one connection when the server allows it; fall back to multiple TCP connections only for HTTP/1.1 with `Accept-Ranges`; download in one stream when ranges aren't supported.
- **2026-10-12: Default limits: 8 logical streams, 4 physical TCP connections per download.** These are ceilings; the adaptive controller may use fewer.
- **2026-10-12: One output file per download, pre-allocated, written at byte offsets.** No part-files joined at the end.
- **2026-10-12: Progress is tracked per byte range in SQLite (WAL mode).** Resume restarts only uncommitted ranges.
- **2026-10-12: The browser extension sends a full context object** (URL, cookies, headers, Referer, manifest text) over Native Messaging, not just a URL.
- **2026-10-12: No DRM circumvention.** DRM-protected streams are detected and refused with a message.
- **2026-10-12: Plain language, measured numbers only.** No invented speeds, counters or marketing claims in the UI, README or docs.
- **2026-10-12: Commit with the GitHub noreply address** (`206061236+xoGUYFAWKSxo@users.noreply.github.com`), same as the TuneUp Suite.
