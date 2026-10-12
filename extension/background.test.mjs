// Runs background.js against a fake browser API, so the request-watching and
// sending flow is tested without a real browser.
import { test } from "node:test";
import assert from "node:assert/strict";

function event() {
  const fns = [];
  return { addListener: (fn) => fns.push(fn), fire: (...a) => fns.map((f) => f(...a)) };
}

const session = {};
const sent = [];
const badges = {};
const cookieQueries = [];
const menuItems = new Map();
const notes = [];
let popups = 0;
globalThis.chrome = {
  storage: { session: {
    get: async () => ({ ...session }),
    set: async (o) => Object.assign(session, o),
    remove: async (k) => delete session[k],
  } },
  action: {
    setBadgeText: async ({ tabId, text }) => { badges[tabId] = text; },
    openPopup: async () => { popups++; },
  },
  contextMenus: {
    removeAll: async () => menuItems.clear(),
    create: (o) => menuItems.set(o.id, { ...o }),
    update: async (id, o) => Object.assign(menuItems.get(id), o),
    onClicked: event(),
  },
  notifications: { create: async (o) => { notes.push(o); } },
  webRequest: {
    OnSendHeadersOptions: { EXTRA_HEADERS: "extraHeaders" },
    onSendHeaders: event(), onHeadersReceived: event(), onErrorOccurred: event(), onBeforeRequest: event(),
  },
  tabs: {
    onRemoved: event(), onActivated: event(),
    get: async (id) => ({ id, url: "https://site.example/watch", title: "A video" }),
    query: async () => [{ id: 7 }],
  },
  cookies: { getAll: async (q) => {
    cookieQueries.push(q);
    if ("partitionKey" in q) throw new Error("unsupported");
    return [{ name: "sid", value: "s1", domain: "cdn.example", path: "/" }];
  } },
  runtime: {
    onMessage: event(), onInstalled: event(), onStartup: event(),
    getURL: (p) => "chrome-extension://x/" + p,
    sendNativeMessage: async (_host, msg) => { sent.push(msg); return { ok: true, payload: { id: 1 } }; },
  },
};
globalThis.fetch = async () => ({ ok: true, status: 200, text: async () => "#EXTM3U\n" });

await import("./src/background.js");
const wr = chrome.webRequest;
const tick = () => new Promise((r) => setTimeout(r, 0));
const ask = (msg) => new Promise((r) => { chrome.runtime.onMessage.fire(msg, {}, r); });

function request(id, url, reqHeaders, resHeaders, tabId = 7, statusCode = 200) {
  wr.onSendHeaders.fire({ requestId: id, tabId, requestHeaders: reqHeaders });
  wr.onHeadersReceived.fire({ requestId: id, tabId, url, statusCode, responseHeaders: resHeaders });
}

test("lists playlists and large media, skips segments and errors", async () => {
  request("1", "https://cdn.example/master.m3u8?t=1",
    [{ name: "Referer", value: "https://site.example/watch" }, { name: "Cookie", value: "sid=s1" }],
    [{ name: "Content-Type", value: "application/vnd.apple.mpegurl" }]);
  request("2", "https://cdn.example/seg1.ts", [], [{ name: "Content-Type", value: "video/mp2t" }]);
  request("3", "https://cdn.example/tiny.mp4", [], [{ name: "Content-Length", value: "100" }]);
  request("4", "https://cdn.example/gone.m3u8", [], [], 7, 404);
  request("5", "https://cdn.example/film.mp4", [], [{ name: "Content-Range", value: "bytes 0-99/900000000" }]);
  request("6", "https://cdn.example/other-tab.mp4", [], [], -1);
  request("7", "https://www.example/sounds/success.mp3", [], [{ name: "Content-Type", value: "audio/mpeg" }]);
  await tick();
  const list = await ask({ type: "list", tabId: 7 });
  assert.deepEqual(list.map((i) => i.url), ["https://cdn.example/film.mp4", "https://cdn.example/master.m3u8?t=1"]);
  assert.equal(list[0].size, 900000000);
  assert.equal(badges[7], "2");
  assert.equal(list[1].headers.cookie, undefined, "cookies must not be kept in the list");
  assert.ok(!JSON.stringify(session).includes("sid=s1"), "cookies must not reach session storage");
  assert.equal(list[1].info.type, "media", "the playlist is read so the popup can describe it");
  assert.equal(list[0].info.type, "none", "nothing readable: described from its name");
});

test("send builds the full context", async () => {
  const r = await ask({ type: "send", tabId: 7, url: "https://cdn.example/master.m3u8?t=1" });
  assert.equal(r.ok, true);
  const m = sent.at(-1);
  assert.equal(m.type, "media");
  assert.equal(m.media.kind, "hls");
  assert.equal(m.media.referer, "https://site.example/watch");
  assert.equal(m.media.pageTitle, "A video");
  assert.equal(m.media.manifest, "#EXTM3U\n");
  assert.deepEqual(m.media.cookies.map((c) => c.name), ["sid"]);
  assert.ok(cookieQueries.some((q) => "partitionKey" in q), "tries partitioned cookies first");
});

test("unknown stream and new page", async () => {
  const r = await ask({ type: "send", tabId: 7, url: "https://nope.example/x.m3u8" });
  assert.equal(r.ok, false);
  wr.onBeforeRequest.fire({ tabId: 7, type: "main_frame" });
  await tick();
  assert.deepEqual(await ask({ type: "list", tabId: 7 }), []);
  assert.equal(badges[7], "");
});

test("right-click menu downloads the clicked or playing video", async () => {
  globalThis.fetch = async () => ({ ok: true, status: 200, text: async () => "#EXTM3U\n#EXTINF:2,\nseg1.ts\n#EXT-X-ENDLIST\n" });
  chrome.runtime.onInstalled.fire();
  await tick();
  assert.deepEqual(menuItems.get("download-media").contexts, ["video", "audio"]);
  assert.equal(menuItems.get("download-playing").visible, false, "hidden while the page has nothing");

  request("20", "https://cdn.example/v/master.m3u8", [], [{ name: "Content-Type", value: "application/vnd.apple.mpegurl" }]);
  await tick();
  assert.equal(menuItems.get("download-playing").visible, true);
  const click = async (menuItemId, srcUrl) => {
    chrome.contextMenus.onClicked.fire({ menuItemId, srcUrl }, { id: 7, url: "https://site.example/watch" });
    for (let i = 0; i < 5; i++) await tick();
  };

  // Streamed video has a blob: address; the only stream on the page is it.
  await click("download-media", "blob:https://site.example/1234");
  let m = sent.at(-1);
  assert.equal(m.type, "download");
  assert.equal(m.media.url, "https://cdn.example/v/master.m3u8");
  assert.equal(notes.at(-1).title, "Downloading");

  // A plain file the page didn't list is sent as it is.
  await click("download-media", "https://other.example/clip.mp4");
  m = sent.at(-1);
  assert.deepEqual([m.type, m.media.url, m.media.kind, m.media.referer], ["download", "https://other.example/clip.mp4", "video", "https://site.example/watch"]);

  // Two streams loading at once: ask instead of guessing.
  request("21", "https://cdn.example/w/other.m3u8", [], [{ name: "Content-Type", value: "application/vnd.apple.mpegurl" }]);
  await tick();
  const before = sent.length;
  await click("download-playing", undefined);
  assert.equal(popups, 1);
  assert.equal(sent.length, before);

  // Segments of the first stream keep loading: that's the playing one.
  await new Promise((r) => setTimeout(r, 30));
  const list = await ask({ type: "list", tabId: 7 });
  for (const it of list) it.seenAt -= 10000; // both loaded a while ago
  request("22", "https://cdn.example/v/seg5.ts", [], [{ name: "Content-Type", value: "video/mp2t" }]);
  await tick();
  await click("download-playing", undefined);
  assert.equal(sent.length, before + 1);
  assert.equal(sent.at(-1).media.url, "https://cdn.example/v/master.m3u8");

  // Nothing on a new page.
  wr.onBeforeRequest.fire({ tabId: 7, type: "main_frame" });
  await tick();
  await click("download-media", "blob:https://site.example/1");
  assert.equal(notes.at(-1).title, "Nothing to download yet");
  assert.equal(menuItems.get("download-playing").visible, false);
});
