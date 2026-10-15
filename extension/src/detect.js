// Pure helpers for spotting media requests. No browser APIs here, so the
// Node tests can cover them.

const HLS_TYPES = ["application/vnd.apple.mpegurl", "application/x-mpegurl", "audio/mpegurl", "audio/x-mpegurl"];
const DASH_TYPES = ["application/dash+xml"];
// Pieces of a stream the player fetches one by one; the playlist is what matters.
const SEGMENT = /\.(ts|m4s|m4f|m4v?s|cmf[va]|aac|vtt|webvtt|key)$/;
const SEGMENT_TYPES = ["video/mp2t", "video/iso.segment", "audio/iso.segment"];

// Short clips (UI sounds, previews) aren't worth listing.
export const MIN_MEDIA_BYTES = 256 * 1024;

export function mimeOf(contentType) {
  return (contentType ?? "").split(";")[0].trim().toLowerCase();
}

// classify returns "hls", "dash", "video", "audio" or null.
export function classify(url, contentType) {
  let u;
  try {
    u = new URL(url);
  } catch {
    return null;
  }
  if (u.protocol !== "http:" && u.protocol !== "https:") return null;
  const path = u.pathname.toLowerCase();
  const ct = mimeOf(contentType);
  if (path.endsWith(".m3u8") || HLS_TYPES.includes(ct)) return "hls";
  if (path.endsWith(".mpd") || DASH_TYPES.includes(ct)) return "dash";
  if (SEGMENT.test(path) || SEGMENT_TYPES.includes(ct)) return null;
  if (/\.(mp4|m4v|webm|mkv|mov)$/.test(path) || ct.startsWith("video/")) return "video";
  if (/\.(mp3|m4a|ogg|oga|opus|flac|wav)$/.test(path) || ct.startsWith("audio/")) return "audio";
  return null;
}

// headerMap turns webRequest's [{name, value}] into a lower-cased object.
// isSegment tells whether a request is one piece of a stream. Pieces aren't
// listed, but they show which stream is playing.
export function isSegment(url, contentType) {
  try {
    return SEGMENT.test(new URL(url).pathname.toLowerCase()) || SEGMENT_TYPES.includes(mimeOf(contentType));
  } catch {
    return false;
  }
}

export function headerMap(list) {
  const out = {};
  for (const h of list ?? []) out[h.name.toLowerCase()] = h.value ?? "";
  return out;
}

// totalSize reads the full size from a response, or null when unknown.
// A 206 reply's Content-Length is only the part, so Content-Range wins.
export function totalSize(responseHeaders) {
  const range = /\/(\d+)\s*$/.exec(responseHeaders["content-range"] ?? "");
  if (range) return Number(range[1]);
  const len = responseHeaders["content-length"];
  return len && /^\d+$/.test(len) ? Number(len) : null;
}

// Headers the engine must not replay: cookies travel separately, and the rest
// describe this one request or connection.
const DROP = new Set([
  "cookie", "range", "host", "connection", "content-length", "accept-encoding",
  "upgrade-insecure-requests", "priority", "te", "keep-alive",
]);

export function replayHeaders(requestHeaders) {
  const out = {};
  for (const [k, v] of Object.entries(requestHeaders)) {
    if (DROP.has(k) || k.startsWith("if-") || k.startsWith("sec-fetch-") || k.startsWith("proxy-")) continue;
    out[k] = v;
  }
  return out;
}

// worthListing drops short audio/video clips; playlists are always listed.
// A file of unknown size is skipped too: sites serve their UI sounds that way
// (YouTube's failure.mp3, success.mp3 and so on), while real video files
// almost always state a size or answer a range request.
export function worthListing(kind, size) {
  if (kind === "hls" || kind === "dash") return true;
  return size !== null && size >= MIN_MEDIA_BYTES;
}

export function fileName(url) {
  try {
    const parts = new URL(url).pathname.split("/").filter(Boolean);
    return decodeURIComponent(parts.at(-1) ?? new URL(url).host);
  } catch {
    return url;
  }
}

export function formatBytes(n) {
  if (n === null || n === undefined) return "";
  const units = ["bytes", "KB", "MB", "GB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return (i === 0 ? n : n.toFixed(1)) + " " + units[i];
}

export function toCookie(c) {
  return {
    name: c.name, value: c.value, domain: c.domain, path: c.path,
    secure: !!c.secure, httpOnly: !!c.httpOnly, expires: c.expirationDate ?? 0,
  };
}

// dedupeCookies keeps one cookie per name/domain/path; asking more than one
// cookie store can return the same cookie twice.
export function dedupeCookies(list) {
  const seen = new Map();
  for (const c of list) seen.set(`${c.name}\u0000${c.domain}\u0000${c.path}`, c);
  return [...seen.values()];
}

// Downloads smaller than this stay with the browser: page images, small
// documents and the like aren't worth a second request.
export const CATCH_MIN = 1 << 20;

// worthCatching says whether a download the browser just started should go
// to the app. It stays with the browser when the app can't fetch it the same
// way (blob:, data: and file: addresses, private windows, other extensions'
// downloads), and when it's small or its size is unknown: those are mostly
// files a page builds on the fly.
export function worthCatching(d) {
  if (!/^https?:\/\//i.test(d.url ?? "")) return false;
  if (d.incognito || d.byExtensionId) return false;
  if (d.state && d.state !== "in_progress") return false;
  const size = d.totalBytes > 0 ? d.totalBytes : d.fileSize > 0 ? d.fileSize : 0;
  return size >= CATCH_MIN;
}
