// Watches each tab's network requests for media streams and, when the user
// asks, sends one to the app with everything needed to fetch it the way the
// browser did. All native messaging goes through here.
import { classify, headerMap, totalSize, replayHeaders, worthListing, toCookie, dedupeCookies } from "./detect.js";

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
    return { ok: false, error: "Can't reach the Internet Downloader app. Is it installed? (" + err.message + ")" };
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
  if (!kind) return;
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
  if (known) item.size ??= known.size;
  items.set(item.url, item);
  if (items.size > MAX_PER_TAB) items.delete(items.keys().next().value);
  save(tabId);
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

async function send(tabId, url) {
  await ready;
  const item = tabs.get(tabId)?.get(url);
  if (!item) return { ok: false, error: "That stream isn't in this tab's list any more. Reload the page." };
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
  return toApp({ type: "media", media });
}

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
