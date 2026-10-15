// Watches each tab's network requests for media streams and, when the user
// asks, sends one to the app with everything needed to fetch it the way the
// browser did. All native messaging goes through here.
import { classify, isSegment, headerMap, totalSize, replayHeaders, worthListing, toCookie, dedupeCookies } from "./detect.js";
import { parseHLS, parseDASH, sniffMP4, findMoov, fileInfo, pick, playlistFor, describe, SNIFF_BYTES, MAX_MOOV } from "./describe.js";

const api = globalThis.browser ?? globalThis.chrome;
const HOST = "com.xoguyfawksxo.internet_downloader";
const MAX_PER_TAB = 50;
const MAX_MANIFEST = 8 << 20; // matches the engine's limit
const FILTER = { urls: ["<all_urls>"], types: ["media", "xmlhttprequest", "object", "other"] };

async function toApp(message) {
  try {
    return await api.runtime.sendNativeMessage(HOST, message);
  } catch (err) {
    // Thrown when the relay isn't registered for this browser or exits early.
    return { ok: false, error: "Can't reach the Galactic Downloader app. Is it installed? (" + err.message + ")" };
  }
}

// tabId -> Map(url -> item). Chrome stops an idle service worker, so the
// lists are mirrored to session storage (memory only, cleared when the
// browser closes) and reloaded on start. Cookies are never stored here.
const tabs = new Map();
const ready = api.storage.session.get(null).then((all) => {
  for (const [key, items] of Object.entries(all)) {
    if (key.startsWith("tab:")) tabs.set(Number(key.slice(4)), new Map(items.map((i) => [i.url, i])));
  }
}, () => {});

function save(tabId) {
  const items = tabs.get(tabId);
  const key = "tab:" + tabId;
  if (items?.size) api.storage.session.set({ [key]: [...items.values()] }).catch(() => {});
  else api.storage.session.remove(key).catch(() => {});
  api.action.setBadgeText({ tabId, text: items?.size ? String(items.size) : "" }).catch(() => {});
}

async function clearTab(tabId) {
  await ready;
  if (tabs.delete(tabId)) save(tabId);
  updateMenu();
}

// touch notes that a stream's playlist is playing: its segments are loading.
// Saved at most every few seconds, as segments arrive often.
async function touch(tabId, segmentUrl) {
  await ready;
  const items = tabs.get(tabId);
  const it = items && playlistFor([...items.values()], segmentUrl);
  if (!it) return;
  const now = Date.now();
  const stale = now - (it.activeAt ?? 0) > 2000;
  it.activeAt = now;
  if (stale) save(tabId);
}

// Request headers arrive before the response; keep them until we know
// whether the response is media.
const pending = new Map();

// Chrome only shows Cookie, Referer and similar headers with "extraHeaders";
// Firefox shows them anyway and rejects the option.
const extra = api.webRequest.OnSendHeadersOptions?.EXTRA_HEADERS ? ["requestHeaders", "extraHeaders"] : ["requestHeaders"];

api.webRequest.onSendHeaders.addListener((d) => {
  if (d.tabId >= 0) pending.set(d.requestId, headerMap(d.requestHeaders));
}, FILTER, extra);

api.webRequest.onHeadersReceived.addListener((d) => {
  const reqHeaders = pending.get(d.requestId);
  pending.delete(d.requestId);
  if (d.tabId < 0 || d.statusCode >= 400) return;
  const resHeaders = headerMap(d.responseHeaders);
  const kind = classify(d.url, resHeaders["content-type"]);
  if (!kind) {
    if (isSegment(d.url, resHeaders["content-type"])) touch(d.tabId, d.url);
    return;
  }
  const size = totalSize(resHeaders);
  if (!worthListing(kind, size)) return;
  const headers = replayHeaders(reqHeaders ?? {});
  record(d.tabId, {
    url: d.url,
    kind,
    contentType: resHeaders["content-type"] ?? "",
    size,
    referer: headers.referer ?? d.documentUrl ?? d.initiator ?? "",
    headers,
    seenAt: Date.now(),
  });
}, FILTER, ["responseHeaders"]);

api.webRequest.onErrorOccurred.addListener((d) => pending.delete(d.requestId), FILTER);

// A new page in the tab starts a new list.
api.webRequest.onBeforeRequest.addListener((d) => {
  if (d.tabId >= 0) clearTab(d.tabId);
}, { urls: ["<all_urls>"], types: ["main_frame"] });

api.tabs.onRemoved.addListener((tabId) => clearTab(tabId));

async function record(tabId, item) {
  await ready;
  let items = tabs.get(tabId);
  if (!items) tabs.set(tabId, (items = new Map()));
  const known = items.get(item.url);
  if (known) {
    item.size ??= known.size;
    item.info ??= known.info;
  }
  items.set(item.url, item);
  if (items.size > MAX_PER_TAB) items.delete(items.keys().next().value);
  save(tabId);
  updateMenu();
  if (!item.info && !reading.has(item.url)) {
    reading.add(item.url);
    const info = (await inspect(item).catch(() => null)) ?? { type: "none" };
    reading.delete(item.url);
    const now = tabs.get(tabId)?.get(item.url);
    if (now) {
      now.info = info;
      save(tabId);
    }
  }
}

// Items being read for their description, so a playlist the player reloads
// every few seconds is only fetched once.
const reading = new Set();

// inspect reads a playlist, or the start of an MP4 file, so the popup can say
// what it is. It returns null when it can't tell.
async function inspect(item) {
  const signal = AbortSignal.timeout(15000);
  if (item.kind === "hls" || item.kind === "dash") {
    const r = await fetch(item.url, { credentials: "include", signal });
    if (!r.ok) return null;
    const text = await r.text();
    if (text.length > MAX_MANIFEST) return null;
    return item.kind === "hls" ? parseHLS(text, item.url) : parseDASH(text, item.url);
  }
  if (!/\.(mp4|m4v|m4a|mov)$/i.test(new URL(item.url).pathname) && !/mp4|quicktime/.test(item.contentType)) return null;
  // Find the track list: usually near the start, sometimes after the media
  // data. Each step asks only for the bytes it needs.
  let offset = 0;
  for (let step = 0; step < 4; step++) {
    const chunk = await readRange(item.url, offset, SNIFF_BYTES, signal);
    if (!chunk) return null;
    const found = findMoov(chunk, offset);
    if (found?.next !== undefined) {
      offset = found.next;
      continue;
    }
    if (!found?.moov) return null;
    const [start, end] = found.moov;
    if (end - start > MAX_MOOV) return null;
    const moov = end <= offset + chunk.length ? chunk.subarray(start - offset, end - offset) : await readRange(item.url, start, end - start, signal);
    const tracks = moov && sniffMP4(moov);
    return tracks && fileInfo(tracks);
  }
  return null;
}

// readRange fetches n bytes from offset, or null when the server can't send
// just that part.
async function readRange(url, offset, n, signal) {
  const r = await fetch(url, { credentials: "include", headers: { range: `bytes=${offset}-${offset + n - 1}` }, signal });
  if (!r.body || !(r.status === 206 || (r.status === 200 && offset === 0))) {
    r.body?.cancel().catch(() => {});
    return null;
  }
  return readStart(r.body, n);
}

// readStart reads up to n bytes, then stops: a server that ignores the range
// would otherwise send the whole file.
async function readStart(body, n) {
  const reader = body.getReader();
  const buf = new Uint8Array(n);
  let got = 0;
  try {
    while (got < n) {
      const { done, value } = await reader.read();
      if (done) break;
      const take = Math.min(value.length, n - got);
      buf.set(value.subarray(0, take), got);
      got += take;
    }
  } finally {
    reader.cancel().catch(() => {});
  }
  return buf.subarray(0, got);
}

// cookiesFor asks every cookie store the browser has for this URL: the
// default one, partitioned ("third-party") cookies, and Firefox's
// first-party-isolation stores. Unsupported options throw, so try in turn.
async function cookiesFor(url) {
  for (const q of [{ url, partitionKey: {} }, { url }, { url, firstPartyDomain: null }]) {
    try {
      return dedupeCookies((await api.cookies.getAll(q)).map(toCookie));
    } catch {}
  }
  return [];
}

async function fetchManifest(url) {
  try {
    const r = await fetch(url, { credentials: "include", signal: AbortSignal.timeout(15000) });
    if (!r.ok) return { manifestError: `The server answered ${r.status} when asked for the playlist.` };
    const text = await r.text();
    if (text.length > MAX_MANIFEST) return { manifestError: "The playlist is too large." };
    return { manifest: text };
  } catch (err) {
    return { manifestError: "Couldn't fetch the playlist: " + err.message };
  }
}

// send passes an item to the app. type "media" lists it in the window;
// "download" (from the right-click menu) starts it as well.
async function send(tabId, url, type = "media") {
  await ready;
  const item = tabs.get(tabId)?.get(url);
  if (!item) return { ok: false, error: "That stream isn't in this tab's list any more. Reload the page." };
  return sendItem(tabId, item, type);
}

async function sendItem(tabId, item, type) {
  const tab = await api.tabs.get(tabId).catch(() => null);
  const media = {
    url: item.url,
    kind: item.kind,
    contentType: item.contentType,
    pageUrl: tab?.url ?? "",
    pageTitle: tab?.title ?? "",
    referer: item.referer,
    headers: item.headers,
    cookies: await cookiesFor(item.url),
  };
  if (item.kind === "hls" || item.kind === "dash") Object.assign(media, await fetchManifest(item.url));
  return toApp({ type, media });
}

// The right-click menu. "Download this video" shows on video and audio; a
// second entry shows anywhere on a page that has something to download, for
// players that cover their video with their own controls.
const menus = api.contextMenus ?? api.menus;
const MENU_MEDIA = "download-media";
const MENU_PAGE = "download-playing";

// Menus survive the background stopping, so they're made on install and
// browser start. Promises throughout: Firefox's browser.* takes no callbacks.
async function createMenu() {
  if (!menus) return;
  await menus.removeAll();
  menus.create({ id: MENU_MEDIA, title: "Download this with Galactic Downloader", contexts: ["video", "audio"] });
  menus.create({ id: MENU_PAGE, title: "Download the playing video with Galactic Downloader",
    contexts: ["page", "frame", "link", "image"], visible: false });
  menuShown = false;
  updateMenu();
}
api.runtime.onInstalled?.addListener(() => createMenu().catch(() => {}));
api.runtime.onStartup?.addListener(() => createMenu().catch(() => {}));

let menuShown = false;
// updateMenu shows the page entry only on a tab with something to download.
async function updateMenu() {
  if (!menus) return;
  const [tab] = await api.tabs.query({ active: true, lastFocusedWindow: true }).catch(() => []);
  const items = tab ? [...(tabs.get(tab.id)?.values() ?? [])] : [];
  const show = describe(items).some((g) => !/^(Copy-protected|Live)/.test(g.title));
  if (show === menuShown) return;
  menuShown = show;
  Promise.resolve(menus.update(MENU_PAGE, { visible: show })).catch(() => {});
}
api.tabs.onActivated?.addListener(() => updateMenu());

function notify(title, message) {
  api.notifications?.create({ type: "basic", iconUrl: api.runtime.getURL("icon.png"), title, message })?.catch(() => {});
}

async function menuClicked(info, tab) {
  if (!tab || tab.id < 0) return;
  await ready;
  const items = [...(tabs.get(tab.id)?.values() ?? [])];
  const choice = pick(items, info.menuItemId === MENU_MEDIA ? info.srcUrl : "");
  if (choice.ambiguous) {
    // Several videos loaded at the same time: let the person choose.
    const opened = await api.action.openPopup?.().then(() => true, () => false);
    if (!opened) notify("Which video?", "Several videos are loading on this page. Click the Galactic Downloader button and choose one.");
    return;
  }
  let item = choice.item;
  if (!item && choice.url) {
    item = { url: choice.url, kind: classify(choice.url, "") ?? (info.mediaType === "audio" ? "audio" : "video"),
      contentType: "", referer: tab.url ?? "", headers: {} };
  }
  if (!item) {
    notify("Nothing to download yet", "Start playing the video, then right-click it again.");
    return;
  }
  const r = await sendItem(tab.id, item, "download");
  // The app may show its Download File Info box instead of starting at once.
  if (r?.ok) { if (!r.payload?.asked) notify("Downloading", (choice.title ?? "Video") + ". Progress shows in the Galactic Downloader window."); }
  else notify("Couldn't download", r?.error ?? "No answer from the app.");
}
menus?.onClicked.addListener((info, tab) => { menuClicked(info, tab); });

api.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  switch (msg?.type) {
    case "ping":
      toApp({ type: "ping" }).then(sendResponse);
      return true; // async response
    case "list":
      ready.then(() => sendResponse([...(tabs.get(msg.tabId)?.values() ?? [])].reverse()));
      return true;
    case "send":
      send(msg.tabId, msg.url).then(sendResponse);
      return true;
  }
  return false;
});
