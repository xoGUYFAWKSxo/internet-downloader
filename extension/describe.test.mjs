import { test } from "node:test";
import assert from "node:assert/strict";
import { parseHLS, parseDASH, sniffMP4, findMoov, fileInfo, pick, playlistFor, CLEAR_LEAD, describe, isoSeconds, clock, key } from "./src/describe.js";

const V = "https://v.redd.it/abc/";
const MASTER = `#EXTM3U
#EXT-X-VERSION:6
#EXT-X-MEDIA:URI="CMAF_AUDIO_128.m3u8",TYPE=AUDIO,GROUP-ID="audio",NAME="128",DEFAULT=YES
#EXT-X-STREAM-INF:BANDWIDTH=900000,RESOLUTION=480x854,CODECS="vp09.00.30.08,mp4a.40.2",AUDIO="audio"
CMAF_VP9_480.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=400000,RESOLUTION=270x480,CODECS="vp09.00.21.08,mp4a.40.2",AUDIO="audio"
CMAF_VP9_270.m3u8
`;
const media = (file) => `#EXTM3U
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-MAP:URI="${file}",BYTERANGE="800@0"
#EXTINF:10.0,
#EXT-X-BYTERANGE:5000@800
${file}
#EXTINF:5.2,
#EXT-X-BYTERANGE:3000@5800
${file}
#EXT-X-ENDLIST
`;

test("parseHLS master and media playlists", () => {
  const m = parseHLS(MASTER, V + "HLSPlaylist.m3u8?f=sd");
  assert.equal(m.type, "master");
  assert.equal(m.sound, true);
  assert.deepEqual(m.video.map((v) => [v.url, v.height, v.sound]),
    [[V + "CMAF_VP9_480.m3u8", 480, false], [V + "CMAF_VP9_270.m3u8", 270, false]]);
  assert.deepEqual(m.audio.map((a) => [a.url, a.name]), [[V + "CMAF_AUDIO_128.m3u8", "128"]]);

  const muxed = parseHLS('#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2"\nhi.m3u8\n', V + "m.m3u8");
  assert.equal(muxed.sound, true);
  assert.equal(muxed.video[0].sound, true);
  const silent = parseHLS('#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1,CODECS="avc1.640028"\nhi.m3u8\n', V + "m.m3u8");
  assert.equal(silent.sound, false);
  const unknown = parseHLS("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nhi.m3u8\n", V + "m.m3u8");
  assert.equal(unknown.sound, null);

  const p = parseHLS(media("CMAF_VP9_480.mp4"), V + "CMAF_VP9_480.m3u8");
  assert.deepEqual([p.type, p.duration, p.segments, p.live, p.drm, p.files], ["media", 15.2, 2, false, false, [V + "CMAF_VP9_480.mp4"]]);
  const many = "#EXTM3U\n" + Array.from({ length: 30 }, (_, i) => `#EXTINF:2,\ns${i}.ts\n`).join("");
  const live = parseHLS(many, V + "live.m3u8");
  assert.equal(live.live, true);
  assert.deepEqual(live.files, [], "ordinary segments aren't kept");
  assert.equal(parseHLS('#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI="skd://x"\n#EXTINF:2,\na.ts\n#EXT-X-ENDLIST\n', V).drm, true);
  assert.equal(parseHLS('#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI="k"\n#EXTINF:2,\na.ts\n#EXT-X-ENDLIST\n', V).drm, false);
});

const MPD = `<?xml version="1.0"?><MPD type="static" mediaPresentationDuration="PT1M5.5S"><Period>
<AdaptationSet contentType="video" mimeType="video/mp4">
<Representation id="v1" bandwidth="2000000" width="1280" height="720" codecs="avc1.64001f"><BaseURL>DASH_720.mp4</BaseURL></Representation>
<Representation id="v2" bandwidth="800000" width="640" height="360" codecs="avc1.64001e"><BaseURL>DASH_360.mp4</BaseURL></Representation>
</AdaptationSet>
<AdaptationSet mimeType="audio/mp4"><Representation id="a" bandwidth="128000" codecs="mp4a.40.2"><BaseURL>DASH_AUDIO_128.mp4</BaseURL></Representation></AdaptationSet>
</Period></MPD>`;

test("parseDASH", () => {
  const d = parseDASH(MPD, V + "DASHPlaylist.mpd?a=1");
  assert.deepEqual([d.type, d.sound, d.duration, d.live, d.drm], ["dash", true, 65.5, false, false]);
  assert.deepEqual(d.video.map((v) => [v.url, v.height]), [[V + "DASH_720.mp4", 720], [V + "DASH_360.mp4", 360]]);
  assert.deepEqual(d.audio.map((a) => [a.url, a.bandwidth]), [[V + "DASH_AUDIO_128.mp4", 128000]]);
  assert.equal(parseDASH('<MPD type="dynamic"><Period></Period></MPD>', V).live, true);
  assert.equal(parseDASH('<MPD><Period><AdaptationSet><ContentProtection schemeIdUri="x"/></AdaptationSet></Period></MPD>', V).drm, true);
  assert.equal(isoSeconds("PT1H2M3S"), 3723);
  assert.equal(isoSeconds("bad"), 0);
});

function box(type, ...parts) {
  const body = parts.reduce((n, p) => n + p.length, 0);
  const out = new Uint8Array(8 + body);
  new DataView(out.buffer).setUint32(0, 8 + body);
  out.set([...type].map((c) => c.charCodeAt(0)), 4);
  let o = 8;
  for (const p of parts) {
    out.set(p, o);
    o += p.length;
  }
  return out;
}
function tkhd(w, h) {
  const b = new Uint8Array(84); // version 0
  const v = new DataView(b.buffer);
  v.setUint32(76, w << 16);
  v.setUint32(80, h << 16);
  return box("tkhd", b);
}
const hdlr = (t) => box("hdlr", new Uint8Array(8), new TextEncoder().encode(t), new Uint8Array(12));
const trak = (t, w = 0, h = 0) => box("trak", tkhd(w, h), box("mdia", box("mdhd", new Uint8Array(24)), hdlr(t)));
const cat = (...a) => {
  const out = new Uint8Array(a.reduce((n, x) => n + x.length, 0));
  let o = 0;
  for (const x of a) {
    out.set(x, o);
    o += x.length;
  }
  return out;
};

test("sniffMP4 reads the track list", () => {
  const ftyp = box("ftyp", new TextEncoder().encode("isom0000"));
  const both = cat(ftyp, box("moov", box("mvhd", new Uint8Array(100)), trak("vide", 854, 480), trak("soun")), box("mdat", new Uint8Array(1000)));
  assert.deepEqual(sniffMP4(both), [{ kind: "video", height: 480 }, { kind: "audio", height: 0 }]);
  assert.deepEqual(fileInfo(sniffMP4(both)), { type: "file", hasVideo: true, sound: true, height: 480 });
  const audioOnly = cat(ftyp, box("moov", trak("soun")));
  assert.deepEqual(fileInfo(sniffMP4(audioOnly)), { type: "file", hasVideo: false, sound: true, height: 0 });
  // Track list at the end, after media data the first bytes can't reach.
  const late = cat(ftyp, box("mdat", new Uint8Array(5000)), box("moov", trak("vide")));
  assert.equal(sniffMP4(late.subarray(0, 1024)), null);
  assert.equal(sniffMP4(both.subarray(0, 60)), null, "cut-off track list");
  assert.equal(sniffMP4(new TextEncoder().encode("<html>not a video</html>")), null);
});

test("findMoov locates the track list", () => {
  const ftyp = box("ftyp", new TextEncoder().encode("isom0000"));
  const moov = box("moov", trak("vide", 1920, 1080), trak("soun"));
  const early = cat(ftyp, moov, box("mdat", new Uint8Array(100)));
  assert.deepEqual(findMoov(early, 0), { moov: [16, 16 + moov.length] });
  assert.deepEqual(sniffMP4(early.subarray(16, 16 + moov.length)).map((t) => t.kind), ["video", "audio"]);
  const late = cat(ftyp, box("mdat", new Uint8Array(5000)), moov);
  assert.deepEqual(findMoov(late.subarray(0, 1024), 0), { next: 16 + 5008 });
  assert.deepEqual(findMoov(late.subarray(5024), 5024), { moov: [5024, 5024 + moov.length] });
  assert.equal(findMoov(new TextEncoder().encode("<!doctype html><html>"), 0), null);
});

test("describe groups Reddit's parts under the playlist", () => {
  const item = (url, kind, info, size = null) => ({ url, kind, info, size });
  const items = [
    item("https://packaged-media.redd.it/x/m2-vp9-res_480p.mp4?e=1", "video", { type: "file", hasVideo: true, sound: true, height: 480 }, 1048576),
    item(V + "HLSPlaylist.m3u8?f=sd", "hls", parseHLS(MASTER, V + "HLSPlaylist.m3u8?f=sd"), 651),
    item(V + "CMAF_AUDIO_128.m3u8?x=2", "hls", parseHLS(media("CMAF_AUDIO_128.mp4"), V + "CMAF_AUDIO_128.m3u8")),
    item(V + "CMAF_VP9_480.m3u8", "hls", parseHLS(media("CMAF_VP9_480.mp4"), V + "CMAF_VP9_480.m3u8")),
    item(V + "CMAF_VP9_480.mp4", "video", { type: "none" }, 5242880),
    item(V + "CMAF_AUDIO_128.mp4", "video", { type: "file", hasVideo: false, sound: true, height: 0 }, 1153434),
    item("https://cdn.example/clip.webm", "video", { type: "none" }, 9e6),
  ];
  const g = describe(items);
  assert.deepEqual(g.map((x) => [x.title, x.detail]), [
    ["Video with sound", "m2-vp9-res_480p.mp4 · 480p · 1.0 MB"],
    ["Video with sound", "HLSPlaylist.m3u8 · up to 480p · 0:15"],
    ["Video file", "clip.webm · 8.6 MB"],
  ]);
  assert.deepEqual(g[1].parts.map((p) => [p.title, p.detail]), [
    ["Sound only", "CMAF_AUDIO_128.m3u8 · 128 kbps"],
    ["Video only, no sound", "CMAF_VP9_480.m3u8 · 480p"],
    ["Video only, no sound", "CMAF_VP9_480.mp4 · 480p · 5.0 MB"],
    ["Sound only", "CMAF_AUDIO_128.mp4 · 128 kbps · 1.1 MB"],
  ]);
});

test("describe without readable details", () => {
  const g = describe([
    { url: "https://cdn.example/a.m3u8", kind: "hls" },
    { url: "https://cdn.example/song.mp3", kind: "audio", size: 4e6 },
    { url: "https://cdn.example/live.m3u8", kind: "hls", info: parseHLS("#EXTM3U\n#EXTINF:2,\na.ts\n", "https://cdn.example/live.m3u8") },
    { url: "https://cdn.example/d.mpd", kind: "dash", info: parseDASH(MPD, "https://cdn.example/d.mpd") },
  ]);
  assert.deepEqual(g.map((x) => x.title), ["Video with sound", "Video stream", "Sound only", "Live stream (can't be saved yet)"]);
  assert.equal(g[0].detail, "d.mpd · up to 720p · 1:06");
});

test("clock and key", () => {
  assert.equal(clock(15.2), "0:15");
  assert.equal(clock(3723), "1:02:03");
  assert.equal(clock(0), "");
  assert.equal(key("https://a.example/x/y.m3u8?t=1"), "a.example/x/y.m3u8");
});

test("pick chooses what a right-click means", () => {
  const hls = (url, seenAt, extra = {}) => ({ url, kind: "hls", seenAt, info: parseHLS("#EXTM3U\n#EXTINF:2,\na.ts\n#EXT-X-ENDLIST\n", url), ...extra });
  const a = hls("https://cdn.example/a/index.m3u8", 1000);
  const b = hls("https://cdn.example/b/index.m3u8", 2000);
  const part = { url: "https://cdn.example/a/clip.mp4?x=1", kind: "video", seenAt: 0, info: { type: "none" } };
  a.info.files = ["https://cdn.example/a/clip.mp4"];
  assert.deepEqual(pick([], "blob:https://site/1"), {});
  assert.equal(pick([a], "blob:https://site/1").item, a);
  assert.deepEqual(pick([a, b], "blob:https://site/1"), { ambiguous: true }, "both loaded within 3 s");
  b.activeAt = 1000 + CLEAR_LEAD + 1;
  assert.equal(pick([a, b], "blob:https://site/1").item, b, "b is clearly the one playing");
  assert.equal(pick([a, b, part], "https://cdn.example/a/clip.mp4").item, a, "a part leads to its whole stream");
  assert.deepEqual(pick([a], "https://other.example/x.mp4"), { url: "https://other.example/x.mp4" });
  const live = { url: "https://cdn.example/l.m3u8", kind: "hls", seenAt: 9e9, info: parseHLS("#EXTM3U\n#EXTINF:2,\na.ts\n", "https://cdn.example/l.m3u8") };
  assert.equal(pick([a, live], undefined).item, a, "live streams can't be saved, so aren't picked");
});

test("playlistFor matches segments to the deepest playlist folder", () => {
  const items = [
    { url: "https://cdn.example/v/master.m3u8", kind: "hls" },
    { url: "https://cdn.example/v/720/index.m3u8?t=1", kind: "hls" },
    { url: "https://cdn.example/v/720/clip.mp4", kind: "video" },
  ];
  assert.equal(playlistFor(items, "https://cdn.example/v/720/seg/9.ts").url, items[1].url);
  assert.equal(playlistFor(items, "https://cdn.example/v/a/1.aac").url, items[0].url);
  assert.equal(playlistFor(items, "https://cdn.other/v/720/9.ts"), null);
});
