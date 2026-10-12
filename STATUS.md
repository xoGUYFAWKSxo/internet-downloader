# Status

_Last updated: 2026-10-12, project start._

**Project status: planning.** No code yet. The plan is the five phases below. The next step is to settle the open questions, then start Phase 1.

## Repository
- Local folder: `C:\Users\Allen\OneDrive\Documents\projects\internet-downloader`.
- Not a git repo yet, and no GitHub repo yet (name, public/private and license are open questions).

## Architecture
```
Browser extension (HLS/DASH sniffer)
   | Native Messaging (JSON context object)
   v
App orchestrator --> Network engine (HTTP/3 -> HTTP/2 -> HTTP/1.1 ranges -> single stream)
                        |
                        v
                     Pre-allocated output file (writes at byte offsets)
                        |
                     SQLite WAL progress log (per byte range)
```

## Roadmap
| Phase | Goal | State |
|---|---|---|
| 0 | Scaffold: repo, engine skeleton, CI, test harness with a local test server (HTTP/1.1, HTTP/2, HTTP/3, with and without range support) | Not started |
| 1 | Stream interception: MV3 extension finds `.m3u8` / `.mpd` / media requests with `chrome.webRequest`; sends URL, cookies, headers, Referer and manifest text over Native Messaging | Not started |
| 2 | Protocol probing: one probe connection, pick the path (multiplexed streams / multi-connection ranges / single stream) from the capabilities found | Not started |
| 3 | Disk I/O: pre-allocate on size known; write each part at its offset into the one file | Not started |
| 4 | Adaptive concurrency: measure RTT and throughput, adjust buffer sizes and stream count within the limits; parallel HLS/DASH fragments, mux with FFmpeg | Not started |
| 5 | Recovery: SQLite WAL range log; resume only uncommitted ranges after a drop or crash | Not started |

## Open questions
None. The user left them to Claude's judgement (2026-10-12), and the answers are in DECISIONS.md. In short: Go everywhere. Three processes: a tray engine, a Native Messaging relay, and a Wails UI. JSON over a per-user named pipe. Runs as the normal user. `WriteAt` by default. A bundled `ffmpeg.exe`. Chrome and Edge first. Public GPL-3.0 repo.

## Dev machine (checked 2026-10-12)
- Go: **not installed**. Phase 0 needs it (`winget install GoLang.Go`), plus the Wails CLI (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`).
- Node v24, `gh`, and WebView2 154 are installed.

## Technical notes to check before building (corrections to the original plan)
- **HTTP/3 discovery isn't ALPN on the first connection.** A client learns a server speaks HTTP/3 from the `Alt-Svc` header (or HTTPS DNS records), after a TCP+TLS connection. ALPN chooses between HTTP/2 and HTTP/1.1. Phase 2 should probe over TCP, read `Alt-Svc`, then try QUIC and fall back if UDP is blocked.
- **`SetFileValidData` needs admin rights** (SeManageVolumePrivilege) and exposes whatever old data was on the disk in that space. It also doesn't make a sparse file. For a non-elevated app, use `SetEndOfFile` (optionally with `FSCTL_SET_SPARSE`) or `SetFileInformationByHandle(FileAllocationInfo)`.
- **mmap isn't zero-copy for network data**: bytes are still copied from the socket buffer into the mapped pages. On Windows, a disk-full or network-drive error while writing to a mapping becomes an access-violation crash rather than a normal error. Plan: benchmark mmap against positioned writes (`WriteAt`) in Phase 3 and pick by measurement; keep the safer one unless mmap is clearly faster.
- **MV3 `webRequest` can observe but can't read response bodies.** The extension must fetch the manifest itself (or the engine fetches it with the forwarded cookies/headers).
- **Native Messaging host** is registered on Windows under `HKCU\Software\Google\Chrome\NativeMessagingHosts\<name>`; messages are length-prefixed JSON, max 1 MB from host to browser (64 MiB from browser to host).

## Not done yet
Everything. Nothing has been built or tested.
