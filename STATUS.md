# Status

_Last updated: 2026-10-12._

**Project status: Phase 3 built (PR #12), waiting for the user's check in the real app.** Go module, IPC, Native Messaging framing, engine and relay skeletons, test servers and CI are built. The extension skeleton (Chrome, Firefox, Edge) with a "Check connection to the app" popup, and the Native Messaging registration script, are done. The Wails window shows whether the engine is running. Phase 1: the extension lists streams found on each page and sends one to the app on request; the window shows what was received. Phase 2: the engine checks a received stream's server (HTTP version, range support, size, HTTP/3 via Alt-Svc) and picks how to download it; the window has a "Check server" button per item. Phase 3: plain video and audio files sent from the browser can be downloaded from the window into the Downloads folder: one pre-allocated file, each part written at its offset, renamed from `.part` when complete; the window shows progress, measured speed, protocol and parallel requests, with Cancel and "Show in folder". Next: Phase 4 (adaptive concurrency, HLS/DASH).

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
| 0 | Scaffold: repo, engine skeleton, CI, test harness with a local test server (HTTP/1.1, HTTP/2, HTTP/3, with and without range support) | Done |
| 1 | Stream interception: MV3 extension finds `.m3u8` / `.mpd` / media requests with `chrome.webRequest`; sends URL, cookies, headers, Referer and manifest text over Native Messaging | Done |
| 2 | Protocol probing: one probe connection, pick the path (multiplexed streams / multi-connection ranges / single stream) from the capabilities found | Done |
| 3 | Disk I/O: pre-allocate on size known; write each part at its offset into the one file | Built, waiting for the user's check |
| 4 | Adaptive concurrency: measure RTT and throughput, adjust buffer sizes and stream count within the limits; parallel HLS/DASH fragments, mux with FFmpeg | Not started |
| 5 | Recovery: SQLite WAL range log; resume only uncommitted ranges after a drop or crash | Not started |

## Open questions
None. The user left them to Claude's judgement (2026-10-12), and the answers are in DECISIONS.md. In short: Go everywhere. Three processes: a tray engine, a Native Messaging relay, and a Wails UI. JSON over a per-user named pipe. Runs as the normal user. `WriteAt` by default. A bundled `ffmpeg.exe`. Chrome, Firefox and Edge. Public GPL-3.0 repo.

## Dev machine (checked 2026-10-12)
- Go 1.27.0 (winget) and Wails CLI v2.16.0 installed.
- Node v24, `gh`, and WebView2 154 are installed.

## Technical notes to check before building (corrections to the original plan)
- **HTTP/3 discovery isn't ALPN on the first connection.** A client learns a server speaks HTTP/3 from the `Alt-Svc` header (or HTTPS DNS records), after a TCP+TLS connection. ALPN chooses between HTTP/2 and HTTP/1.1. Phase 2 should probe over TCP, read `Alt-Svc`, then try QUIC and fall back if UDP is blocked.
- **`SetFileValidData` needs admin rights** (SeManageVolumePrivilege) and exposes whatever old data was on the disk in that space. It also doesn't make a sparse file. For a non-elevated app, use `SetEndOfFile` (optionally with `FSCTL_SET_SPARSE`) or `SetFileInformationByHandle(FileAllocationInfo)`.
- **mmap isn't zero-copy for network data**: bytes are still copied from the socket buffer into the mapped pages. On Windows, a disk-full or network-drive error while writing to a mapping becomes an access-violation crash rather than a normal error. Plan: benchmark mmap against positioned writes (`WriteAt`) in Phase 3 and pick by measurement; keep the safer one unless mmap is clearly faster.
- **MV3 `webRequest` can observe but can't read response bodies.** The extension must fetch the manifest itself (or the engine fetches it with the forwarded cookies/headers).
- **Native Messaging host** is registered per browser under HKCU (Chrome, Edge, Mozilla) `\NativeMessagingHosts\<name>`. Firefox launches the host with the manifest path and the add-on ID as arguments; Chromium launches it with the extension origin. messages are length-prefixed JSON, max 1 MB from host to browser (64 MiB from browser to host).

## Verified
- Unit tests: Native Messaging framing (limits, truncation, invalid JSON), pipe round-trip, single-instance pipe, SID-based pipe name, all four test servers (protocol, full body, range behaviour, Alt-Svc).
- Registration script (2026-10-12): it registers the Chrome, Edge and Firefox keys and manifests. `-Browsers` limits it to the browsers you name, and `-Unregister` removes everything. A Firefox-style launch of `nmhost.exe` reached the engine.
- Extension (2026-10-12): manifest tests for Chrome and Firefox, including that the key gives the fixed ID. An extension-style `{"type":"ping"}` sent through `nmhost.exe` returns ok with the version, and returns a plain-English error when the engine isn't running. **Confirmed by the user in real Chrome and Firefox (2026-10-12):** both popups reported "Connected". Edge isn't tested; the user doesn't use it.
- Phase 1 (2026-10-12): unit tests for stream detection (playlist/media/segment rules, sizes, which headers are replayed, cookies), for `background.js` run against a fake browser API (lists, skips segments and tiny clips, keeps cookies out of storage, builds the full context, resets on a new page), for engine intake (validation, DRM refusal, no cookies/tokens in the log, the list for the window) and for the window's list call. First real-browser try (user, Firefox, YouTube, 2026-10-12): the popup worked but listed YouTube's UI sounds (`failure.mp3`, `success.mp3`...); fixed in PR #8 by skipping audio/video of unknown size. YouTube's own video isn't listed: it streams through its own format, not HLS/DASH files or plain video files. **Confirmed by the user in real Firefox (2026-10-12):** on the hls.js demo page the popup listed the HLS playlists; "Send to app" delivered them through the relay to the engine, and the window showed each with its playlist text (752-3670 bytes). A YouTube send earlier delivered 41 cookies. Chrome and DASH not yet tried for Phase 1.
- Phase 2 (2026-10-12): unit tests against the local servers: HTTP/1.1 with and without ranges, HTTP/2, HTTP/3 found through Alt-Svc, fallback to HTTP/2 when the advertised QUIC port is silent, HTTP/3 on another host not used, redirects, the browser's headers and cookies sent, file name from Content-Disposition, 429 with Retry-After, 404, empty file, compressed reply. Engine test: probing a received item works and no token or cookie reaches the log or the reply. `engine.exe -probe` against real servers: OVH test file over HTTP/3 with ranges (8 streams); Tele2 test file over HTTP/1.1 with ranges (4 connections); Cloudflare speed test without ranges (one request); Google's home page offers HTTP/3 but the page differs between requests, so HTTP/3 wasn't used. **Confirmed by the user in the real app (2026-10-12):** an HLS playlist sent from Firefox (hls.js demo, test-streams.mux.dev) was checked from the window: HTTP/2, 3.6 KB, accepts ranges, plan one request (too small to split).
- Phase 3 (2026-10-12): unit tests for the output file (eight writers filling one file out of order, overlapping writes counted once, an incomplete file refused, name clashes including a file appearing before the rename, unknown size, not enough disk space, Windows name cleaning) and for fetching over every path against the local servers (HTTP/3 and HTTP/2 multiplexed, HTTP/1.1 ranges, single request; byte-for-byte equal), connection counts (HTTP/2: one connection for 8 range requests; HTTP/1.1: at most 4), a file changed after the probe, short body, wrong part, 503 with a long Retry-After, a server that accepts only 3 requests at a time, and cancelling. Engine tests: downloading a received item end to end with no token or cookie in the log; HLS refused; cancel removes the `.part` file. Benchmark of the write methods is in DECISIONS.md. `engine.exe -get` against real servers: Go's installer (dl.google.com, 79 MB) over HTTP/3 with 8 streams in about 2 s, SHA-256 equal to the published one; Tele2 100 MB over 4 HTTP/1.1 connections; Cloudflare 50 MB in one request (no ranges). proof.ovh.net refused 8 parallel requests with 429 (it accepts about 3), which led to the back-off; after the test burst it stopped answering this machine for a while, so the back-off was checked with a local test server instead.
- Known rough edge: an HLS master playlist and each variant playlist the player loads are listed separately. Phase 4 reads the master and picks variants, so the popup should later group variants under their master.
- Window (2026-10-12): `wails build` works. Through `wails dev` in the browser pane, the window showed "The download engine is running. Version 0.0.0-dev." With the engine stopped, "Check again" showed "The download engine isn't running."
- Manual check (2026-10-12): engine started; a framed message piped into `nmhost.exe` reached the engine and its reply came back; nmhost exits cleanly on EOF; a second engine is refused.

## Not done yet
HLS/DASH downloads, adaptive concurrency (Phase 4), resume after a drop or crash (Phase 5). The engine keeps received streams and the download list in memory only, so both are lost when it restarts, and a failed download starts over.
