import { test } from "node:test";
import assert from "node:assert/strict";
import {
  classify, totalSize, replayHeaders, headerMap, worthListing, fileName, formatBytes, dedupeCookies, toCookie,
} from "./src/detect.js";

test("classify", () => {
  const cases = [
    ["https://cdn.example/live/master.m3u8?token=abc", "", "hls"],
    ["https://cdn.example/playlist", "application/vnd.apple.mpegURL; charset=utf-8", "hls"],
    ["https://cdn.example/x/manifest.mpd", "", "dash"],
    ["https://cdn.example/api/stream", "application/dash+xml", "dash"],
    ["https://cdn.example/seg/00001.ts", "video/mp2t", null],
    ["https://cdn.example/seg/chunk-3.m4s", "", null],
    ["https://cdn.example/seg/init", "video/iso.segment", null],
    ["https://cdn.example/clip.MP4", "", "video"],
    ["https://cdn.example/stream", "video/webm", "video"],
    ["https://cdn.example/song.mp3", "", "audio"],
    ["https://cdn.example/a", "audio/ogg", "audio"],
    ["https://cdn.example/page.html", "text/html", null],
    ["blob:https://site/abc", "video/mp4", null],
    ["data:video/mp4;base64,AAAA", "", null],
    ["not a url", "", null],
  ];
  for (const [url, ct, want] of cases) assert.equal(classify(url, ct), want, url);
});

test("totalSize prefers Content-Range", () => {
  assert.equal(totalSize({ "content-range": "bytes 0-1023/52428800", "content-length": "1024" }), 52428800);
  assert.equal(totalSize({ "content-length": "2048" }), 2048);
  assert.equal(totalSize({ "content-range": "bytes 0-1023/*" }), null);
  assert.equal(totalSize({}), null);
});

test("replayHeaders drops cookies and per-request headers", () => {
  const h = headerMap([
    { name: "Cookie", value: "sid=1" }, { name: "Referer", value: "https://site/" },
    { name: "Range", value: "bytes=0-" }, { name: "Sec-Fetch-Mode", value: "cors" },
    { name: "Authorization", value: "Bearer t" }, { name: "If-None-Match", value: "x" },
    { name: "User-Agent", value: "UA" }, { name: "X-Custom-Token", value: "k" },
  ]);
  assert.deepEqual(replayHeaders(h), {
    referer: "https://site/", authorization: "Bearer t", "user-agent": "UA", "x-custom-token": "k",
  });
});

test("worthListing", () => {
  assert.equal(worthListing("hls", 10), true);
  assert.equal(worthListing("video", 1000), false);
  assert.equal(worthListing("video", 10 << 20), true);
  // Bug: YouTube's UI sounds (no Content-Length) were listed as audio.
  assert.equal(worthListing("audio", null), false);
  assert.equal(worthListing("video", null), false);
});

test("fileName and formatBytes", () => {
  assert.equal(fileName("https://x.example/a/My%20Video.mp4?x=1"), "My Video.mp4");
  assert.equal(fileName("https://x.example/"), "x.example");
  assert.equal(formatBytes(512), "512 bytes");
  assert.equal(formatBytes(5 * 1024 * 1024), "5.0 MB");
  assert.equal(formatBytes(null), "");
});

test("cookies", () => {
  const c = toCookie({ name: "a", value: "1", domain: ".x", path: "/", secure: true, httpOnly: false, expirationDate: 5 });
  assert.deepEqual(c, { name: "a", value: "1", domain: ".x", path: "/", secure: true, httpOnly: false, expires: 5 });
  assert.equal(toCookie({ name: "s", value: "", domain: "x", path: "/" }).expires, 0);
  assert.equal(dedupeCookies([c, { ...c }, { ...c, path: "/b" }]).length, 2);
});
