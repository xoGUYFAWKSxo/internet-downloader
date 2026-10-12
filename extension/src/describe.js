// Works out, in plain words, what each listed item is: a whole video with
// sound, video with no sound, or sound only. It reads playlists and the start
// of MP4 files, then files each separate part under the playlist it belongs
// to. No browser APIs here, so the Node tests can cover it.
import { fileName, formatBytes } from "./detect.js";

const AUDIO_CODEC = /^(mp4a|ac-3|ec-3|opus|flac|mp3|vorbis|alac|dts)/i;
const VIDEO_CODEC = /^(avc|hvc|hev|vp0?[89]|av01|dvh|mp4v)/i;
// A media playlist made of more files than this is ordinary segments, which
// are never listed anyway.
const MAX_FILES = 20;
// How much of an MP4 file to read looking for its track list.
export const SNIFF_BYTES = 64 * 1024;

// key names a file regardless of its query string, where tokens change.
export function key(url) {
  try {
    const u = new URL(url);
    return u.host + u.pathname;
  } catch {
    return url;
  }
}

function abs(base, ref) {
  try {
    return new URL(ref, base).href;
  } catch {
    return null;
  }
}

function hlsAttrs(s) {
  const out = {};
  for (const m of s.matchAll(/([A-Z0-9-]+)=("[^"]*"|[^,]*)/g)) out[m[1]] = m[2].replace(/^"|"$/g, "");
  return out;
}

function codecs(s) {
  const list = (s ?? "").split(",").map((c) => c.trim()).filter(Boolean);
  return { audio: list.some((c) => AUDIO_CODEC.test(c)), video: list.some((c) => VIDEO_CODEC.test(c)), known: list.length > 0 };
}

// shortSide turns "854x480" into 480, the number people call "480p" whichever
// way up the video is.
function shortSide(res) {
  const m = /^(\d+)x(\d+)$/.exec(res ?? "");
  return m ? side(m[1], m[2]) : 0;
}

function side(w, h) {
  const n = [Number(w), Number(h)].filter((x) => x > 0);
  return n.length ? Math.min(...n) : 0;
}

// parseHLS describes a master playlist (qualities and audio) or a media
// playlist (its length and the files it reads from).
export function parseHLS(text, url) {
  const lines = text.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
  const nextURI = (i) => lines.slice(i + 1).find((l) => !l.startsWith("#"));
  if (lines.some((l) => l.startsWith("#EXT-X-STREAM-INF:"))) {
    const video = [];
    const audio = [];
    lines.forEach((l, i) => {
      if (l.startsWith("#EXT-X-STREAM-INF:")) {
        const a = hlsAttrs(l.slice(18));
        const c = codecs(a.CODECS);
        const u = abs(url, nextURI(i) ?? "");
        if (!u) return;
        if (c.known && !c.video && a.RESOLUTION === undefined) {
          audio.push({ url: u, bandwidth: Number(a.BANDWIDTH) || 0 });
          return;
        }
        // With a separate audio group the variant itself has no sound.
        video.push({ url: u, height: shortSide(a.RESOLUTION), bandwidth: Number(a.BANDWIDTH) || 0,
          sound: a.AUDIO ? false : c.known ? c.audio : null });
      } else if (l.startsWith("#EXT-X-MEDIA:")) {
        const a = hlsAttrs(l.slice(13));
        const u = a.URI && abs(url, a.URI);
        if (a.TYPE === "AUDIO" && u) audio.push({ url: u, name: a.NAME ?? "", bandwidth: 0 });
      }
    });
    const separate = audio.length > 0;
    const inside = video.some((v) => v.sound);
    const unknown = video.some((v) => v.sound === null);
    return { type: "master", video, audio, sound: separate || inside ? true : unknown ? null : video.length === 0 ? true : false };
  }
  let duration = 0;
  let segments = 0;
  let drm = false;
  const files = new Set();
  lines.forEach((l, i) => {
    if (l.startsWith("#EXTINF:")) {
      duration += parseFloat(l.slice(8)) || 0;
      segments++;
      const u = abs(url, nextURI(i) ?? "");
      if (u) files.add(u);
    } else if (l.startsWith("#EXT-X-MAP:")) {
      const u = abs(url, hlsAttrs(l.slice(11)).URI ?? "");
      if (u) files.add(u);
    } else if (l.startsWith("#EXT-X-KEY:")) {
      const a = hlsAttrs(l.slice(11));
      if ((a.METHOD && a.METHOD !== "NONE" && a.METHOD !== "AES-128") || a.KEYFORMAT) drm = true;
    }
  });
  const ended = lines.some((l) => l === "#EXT-X-ENDLIST" || l === "#EXT-X-PLAYLIST-TYPE:VOD");
  return { type: "media", duration, segments, live: !ended, drm, files: files.size <= MAX_FILES ? [...files] : [] };
}

function xmlAttrs(s) {
  const out = {};
  for (const m of s.matchAll(/([\w:-]+)\s*=\s*"([^"]*)"/g)) out[m[1]] = m[2];
  return out;
}

// isoSeconds reads an ISO 8601 duration such as PT1H2M3.5S.
export function isoSeconds(s) {
  const m = /^P(?:(\d+(?:\.\d+)?)D)?(?:T(?:(\d+(?:\.\d+)?)H)?(?:(\d+(?:\.\d+)?)M)?(?:(\d+(?:\.\d+)?)S)?)?$/.exec(s ?? "");
  if (!m) return 0;
  const [d, h, min, sec] = m.slice(1).map((x) => Number(x ?? 0));
  return d * 86400 + h * 3600 + min * 60 + sec;
}

// parseDASH describes a manifest's video and audio. A rough read with
// regular expressions: service workers have no XML parser, and only the
// description depends on it; the app parses the manifest properly.
export function parseDASH(text, url) {
  const mpd = xmlAttrs(/<MPD\b([^>]*)>/.exec(text)?.[1] ?? "");
  const top = /^[\s\S]*?(?=<Period\b)/.exec(text)?.[0] ?? "";
  const base = abs(url, /<BaseURL>([^<]*)<\/BaseURL>/.exec(top)?.[1]?.trim() ?? "") ?? url;
  const video = [];
  const audio = [];
  for (const set of text.matchAll(/<AdaptationSet\b([^>]*)>([\s\S]*?)<\/AdaptationSet>/g)) {
    const setAttrs = xmlAttrs(set[1]);
    for (const rep of set[2].matchAll(/<Representation\b([^>]*?)(?:\/>|>([\s\S]*?)<\/Representation>)/g)) {
      const r = { ...setAttrs, ...xmlAttrs(rep[1]) };
      const c = codecs(r.codecs);
      const type = (r.contentType || (r.mimeType ?? "").split("/")[0] || (c.video ? "video" : c.audio ? "audio" : ""));
      const ref = /<BaseURL>([^<]*)<\/BaseURL>/.exec(rep[2] ?? "")?.[1]?.trim();
      const entry = { url: ref ? abs(base, ref) : null, bandwidth: Number(r.bandwidth) || 0 };
      if (type === "video") video.push({ ...entry, height: side(r.width, r.height), sound: c.audio });
      else if (type === "audio") audio.push(entry);
    }
  }
  return {
    type: "dash", video, audio,
    sound: audio.length > 0 || video.some((v) => v.sound),
    duration: isoSeconds(mpd.mediaPresentationDuration),
    live: mpd.type === "dynamic",
    drm: /<ContentProtection\b/.test(text),
  };
}

const CONTAINERS = new Set(["moov", "trak", "mdia"]);

// sniffMP4 lists the tracks in the start of an MP4 file: [{kind, height}],
// kind "video" or "audio". It returns null when the track list isn't in the
// bytes given (the file keeps it at the end, or it isn't an MP4).
export function sniffMP4(bytes) {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const type = (p) => String.fromCharCode(...bytes.subarray(p, p + 4));
  const tracks = [];
  let found = false;
  function walk(start, end, depth) {
    let p = start;
    while (p + 8 <= end) {
      let size = view.getUint32(p);
      const t = type(p + 4);
      let head = 8;
      if (size === 1) {
        if (p + 16 > end) return false;
        size = Number(view.getBigUint64(p + 8));
        head = 16;
      } else if (size === 0) {
        size = end - p;
      }
      if (size < head) return false;
      // A cut-off track list can't be read; a cut-off box before it (the
      // media data, when the list is kept at the end) just ends the search.
      if (p + size > end) return t !== "moov" && depth === 0;
      if (t === "moov") found = true;
      if (t === "trak") tracks.push({ kind: null, height: 0 });
      const track = tracks.at(-1);
      if (CONTAINERS.has(t)) {
        if (!walk(p + head, p + size, depth + 1)) return false;
      } else if (t === "tkhd" && track && size >= 92) {
        const off = p + head + (bytes[p + head] === 1 ? 88 : 76);
        if (off + 8 <= p + size) {
          const w = view.getUint32(off) >>> 16;
          const h = view.getUint32(off + 4) >>> 16;
          track.height = side(w, h);
        }
      } else if (t === "hdlr" && track && depth === 3 && size >= 20) {
        const h = type(p + head + 8);
        track.kind = h === "vide" ? "video" : h === "soun" ? "audio" : null;
      }
      if (depth === 0 && t === "moov") return true;
      p += size;
    }
    return true;
  }
  if (!walk(0, bytes.length, 0) || !found) return null;
  return tracks.filter((t) => t.kind);
}

// Track lists larger than this aren't fetched just to describe a file.
export const MAX_MOOV = 8 << 20;
// Boxes an MP4 or QuickTime file can open with; anything else isn't one.
const FIRST_BOX = new Set(["ftyp", "styp", "moov", "mdat", "free", "skip", "wide", "pdin"]);

// findMoov walks the top-level boxes in bytes, which start at offset base in
// the file. It returns {moov: [start, end]} once it finds the track list,
// {next: offset} when the next box starts past these bytes, or null.
export function findMoov(bytes, base) {
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  let p = 0;
  while (p + 16 <= bytes.length) {
    let size = view.getUint32(p);
    const t = String.fromCharCode(...bytes.subarray(p + 4, p + 8));
    if (!/^[\x20-\x7e]{4}$/.test(t)) return null; // not an MP4 box
    if (base === 0 && p === 0 && !FIRST_BOX.has(t)) return null;
    if (size === 1) size = Number(view.getBigUint64(p + 8));
    else if (size === 0) return null; // runs to the end of the file
    if (size < 8) return null;
    if (t === "moov") return { moov: [base + p, base + p + size] };
    p += size;
  }
  return { next: base + p };
}

// fileInfo turns a sniffed track list into the description of a plain file.
export function fileInfo(tracks) {
  const v = tracks.filter((t) => t.kind === "video");
  return { type: "file", hasVideo: v.length > 0, sound: tracks.some((t) => t.kind === "audio"),
    height: Math.max(0, ...v.map((t) => t.height)) };
}

export function clock(seconds) {
  const s = Math.round(seconds);
  if (!(s > 0)) return "";
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const ss = String(s % 60).padStart(2, "0");
  return h ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`;
}

const kbps = (b) => (b ? Math.round(b / 1000) + " kbps" : "");
const quality = (h) => (h ? h + "p" : "");

// title says what a stream or file is, as the person would put it.
function streamTitle(info) {
  if (info.drm) return "Copy-protected video (can't be saved)";
  if (info.live) return "Live stream (can't be saved yet)";
  if (info.video.length === 0 && info.audio.length > 0) return "Sound only";
  if (info.sound === true) return "Video with sound";
  if (info.sound === false) return "Video only, no sound";
  return "Video";
}

function roleTitle(role) {
  if (role.kind === "audio") return "Sound only";
  return role.sound ? "Video with sound" : role.sound === false ? "Video only, no sound" : "Video";
}

function fileTitle(item) {
  const i = item.info;
  if (i?.type === "file") {
    if (!i.hasVideo) return i.sound ? "Sound only" : "Audio or video file";
    return i.sound ? "Video with sound" : "Video only, no sound";
  }
  if (item.kind === "audio") return "Sound only";
  if (item.kind === "hls" || item.kind === "dash") return "Video stream";
  return "Video file";
}

// describe groups the listed items. Each group is something worth
// downloading, with the separate parts that belong to it listed under it:
// [{item, title, detail, parts: [{item, title, detail}]}].
export function describe(items) {
  const byKey = new Map(items.map((it) => [key(it.url), it]));
  const parent = new Map(); // item -> the item it is part of
  const role = new Map(); // item -> {kind, height, sound, bandwidth, name}
  const adopt = (child, owner, r) => {
    if (!child || child === owner || parent.has(child)) return;
    parent.set(child, owner);
    if (r) role.set(child, r);
  };
  // Masters and manifests first, so the media playlists under them know
  // their role before handing it down to their files.
  for (const it of items) {
    const i = it.info;
    if (i?.type !== "master" && i?.type !== "dash") continue;
    for (const v of i.video) adopt(v.url && byKey.get(key(v.url)), it, { kind: "video", height: v.height, sound: v.sound });
    for (const a of i.audio) adopt(a.url && byKey.get(key(a.url)), it, { kind: "audio", bandwidth: a.bandwidth, name: a.name });
  }
  for (const it of items) {
    if (it.info?.type !== "media") continue;
    for (const f of it.info.files) adopt(byKey.get(key(f)), it, role.get(it));
  }
  const top = (it) => {
    let t = it;
    for (let n = 0; parent.has(t) && n < 5; n++) t = parent.get(t);
    return t;
  };
  // A playlist's own size says nothing about the video, so it's left out.
  const playlist = (it) => it.kind === "hls" || it.kind === "dash";
  const meta = (it, extra) => [fileName(it.url), ...extra, playlist(it) ? "" : formatBytes(it.size)].filter(Boolean).join(" · ");
  const groups = new Map();
  for (const it of items) {
    if (parent.has(it)) continue;
    const i = it.info;
    let title;
    let extra = [];
    if (i?.type === "master" || i?.type === "dash") {
      title = streamTitle(i);
      const best = Math.max(0, ...i.video.map((v) => v.height));
      // An HLS master doesn't state its length; its media playlists do.
      const length = i.duration || Math.max(0, ...items.filter((x) => x !== it && top(x) === it).map((x) => x.info?.duration ?? 0));
      extra = [best ? (i.video.length > 1 ? "up to " : "") + quality(best) : "", clock(length)];
    } else if (i?.type === "media") {
      title = i.drm ? "Copy-protected video (can't be saved)" : i.live ? "Live stream (can't be saved yet)" : "Video stream";
      extra = [clock(i.duration)];
    } else {
      title = fileTitle(it);
      extra = [quality(i?.height)];
    }
    groups.set(it, { item: it, title, detail: meta(it, extra), parts: [] });
  }
  for (const it of items) {
    if (!parent.has(it)) continue;
    const r = role.get(it);
    groups.get(top(it))?.parts.push({
      item: it,
      title: r ? roleTitle(r) : fileTitle(it),
      detail: meta(it, r ? [r.kind === "video" ? quality(r.height) : kbps(r.bandwidth) || (r.name && /^\d+$/.test(r.name) ? r.name + " kbps" : "")] : [quality(it.info?.height)]),
    });
  }
  // Whole videos with sound first; otherwise newest first, as listed.
  const rank = (g) => (g.title === "Video with sound" ? 0 : 1);
  return [...groups.values()].sort((a, b) => rank(a) - rank(b));
}
