// All native messaging goes through the background script, so the popup and
// (in Phase 1) the stream sniffer share one path to the app.
const api = globalThis.browser ?? globalThis.chrome;
const HOST = "com.xoguyfawksxo.internet_downloader";

async function toApp(message) {
  try {
    return await api.runtime.sendNativeMessage(HOST, message);
  } catch (err) {
    // Thrown when the relay isn't registered for this browser or exits early.
    return { ok: false, error: "Can't reach the Internet Downloader app. Is it installed? (" + err.message + ")" };
  }
}

api.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg?.type === "ping") {
    toApp({ type: "ping" }).then(sendResponse);
    return true; // async response
  }
  return false;
});
