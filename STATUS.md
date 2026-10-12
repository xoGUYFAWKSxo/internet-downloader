# Status

_Last updated: 2026-10-12, branch phase0-scaffold._

**Project status: Phase 0 in progress.** Go module, IPC, Native Messaging framing, engine and relay skeletons, test servers and CI are built. Still to do in Phase 0: Wails UI skeleton, extension skeleton, Native Messaging registration script.

## Repository
- Local folder: `C:\Users\Allen\OneDrive\Documents\projects\internet-downloader`.
- GitHub: https://github.com/xoGUYFAWKSxo/internet-downloader (public, GPL-3.0, created 2026-10-12, remote `origin`). Auto-merge off, branches deleted on merge.

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
| 0 | Scaffold: repo, engine skeleton, CI, test harness with a local test server (HTTP/1.1, HTTP/2, HTTP/3, with and without range support) | In progress (see top) |
| 1 | Stream interception: MV3 extension finds `.m3u8` / `.mpd` / media requests with `chrome.webRequest`; sends URL, cookies, headers, Referer and manifest text over Native Messaging | Not started |
| 2 | Protocol probing: one probe connection, pick the path (multiplexed streams / multi-connection ranges / single stream) from the capabilities found | Not started |
| 3 | Disk I/O: pre-allocate on size known; write each part at its offset into the one file | Not started |
| 4 | Adaptive concurrency: measure RTT and throughput, adjust buffer sizes and stream count within the limits; parallel HLS/DASH fragments, mux with FFmpeg | Not started |
| 5 | Recovery: SQLite WAL range log; resume only uncommitted ranges after a drop or crash | Not started |

## Open questions
None. The user left them to Claude's judgement (2026-10-12), and the answers are in DECISIONS.md. In short: Go everywhere. Three processes: a tray engine, a Native Messaging relay, and a Wails UI. JSON over a per-user named pipe. Runs as the normal user. `WriteAt` by default. A bundled `ffmpeg.exe`. Chrome and Edge first. Public GPL-3.0 repo.

## Dev machine (checked 2026-10-12)
- Go 1.27.0 (winget) and Wails CLI v2.16.0 installed.
- Node v24, `gh`, and WebView2 154 are installed.

## Technical notes to check before building (corrections to the original plan)
- **HTTP/3 discovery isn't ALPN on the first connection.** A client learns a server speaks HTTP/3 from the `Alt-Svc` header (or HTTPS DNS records), after a TCP+TLS connection. ALPN chooses between HTTP/2 and HTTP/1.1. Phase 2 should probe over TCP, read `Alt-Svc`, then try QUIC and fall back if UDP is blocked.
- **`SetFileValidData` needs admin rights** (SeManageVolumePrivilege) and exposes whatever old data was on the disk in that space. It also doesn't make a sparse file. For a non-elevated app, use `SetEndOfFile` (optionally with `FSCTL_SET_SPARSE`) or `SetFileInformationByHandle(FileAllocationInfo)`.
- **mmap isn't zero-copy for network data**: bytes are still copied from the socket buffer into the mapped pages. On Windows, a disk-full or network-drive error while writing to a mapping becomes an access-violation crash rather than a normal error. Plan: benchmark mmap against positioned writes (`WriteAt`) in Phase 3 and pick by measurement; keep the safer one unless mmap is clearly faster.
- **MV3 `webRequest` can observe but can't read response bodies.** The extension must fetch the manifest itself (or the engine fetches it with the forwarded cookies/headers).
- **Native Messaging host** is registered on Windows under `HKCU\Software\Google\Chrome\NativeMessagingHosts\<name>`; messages are length-prefixed JSON, max 1 MB from host to browser (64 MiB from browser to host).

## Verified
- Unit tests: Native Messaging framing (limits, truncation, invalid JSON), pipe round-trip, single-instance pipe, SID-based pipe name, all four test servers (protocol, full body, range behaviour, Alt-Svc).
- Manual check (2026-10-12): engine started; a framed message piped into `nmhost.exe` reached the engine and its reply came back; nmhost exits cleanly on EOF; a second engine is refused.

## Not done yet
Everything past the Phase 0 items above. Nothing downloads yet.
