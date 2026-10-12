import { fileName, formatBytes } from "./detect.js";

const api = globalThis.browser ?? globalThis.chrome;
const status = document.getElementById("status");
const LABEL = { hls: "HLS", dash: "DASH", video: "Video", audio: "Audio" };
const ALL_SITES = { origins: ["<all_urls>"] };

function show(el, ok, text) {
  el.className = (el.classList.contains("msg") ? "msg " : "") + (ok ? "ok" : "bad");
  el.textContent = text;
}

document.getElementById("check").addEventListener("click", async () => {
  status.className = "";
  status.textContent = "Checking…";
  const r = await api.runtime.sendMessage({ type: "ping" });
  if (r?.ok) show(status, true, "Connected. App version " + (r.payload?.version ?? "unknown") + ".");
  else show(status, false, r?.error ?? "No answer from the app.");
});

// Firefox lets the user turn off site access, so ask for it again if needed.
async function checkPermission() {
  const has = await api.permissions.contains(ALL_SITES);
  document.getElementById("perm").hidden = has;
}
document.getElementById("grant").addEventListener("click", async () => {
  await api.permissions.request(ALL_SITES);
  checkPermission();
});

function render(tabId, items) {
  const list = document.getElementById("list");
  document.getElementById("empty").hidden = items.length > 0;
  list.replaceChildren(...items.map((it) => {
    const li = document.createElement("li");
    const row = document.createElement("div");
    row.className = "row";
    const kind = document.createElement("span");
    kind.className = "kind";
    kind.textContent = LABEL[it.kind] ?? it.kind;
    const name = document.createElement("span");
    name.className = "name";
    name.textContent = fileName(it.url);
    name.title = it.url;
    const button = document.createElement("button");
    button.textContent = "Send to app";
    row.append(kind, name, button);
    const meta = document.createElement("div");
    meta.className = "meta";
    meta.textContent = [new URL(it.url).host, formatBytes(it.size)].filter(Boolean).join(" · ");
    const msg = document.createElement("div");
    msg.className = "msg";
    button.addEventListener("click", async () => {
      button.disabled = true;
      msg.className = "msg";
      msg.textContent = "Sending…";
      const r = await api.runtime.sendMessage({ type: "send", tabId, url: it.url });
      if (r?.ok) show(msg, true, "Sent. The app will download it once downloading is built.");
      else show(msg, false, r?.error ?? "No answer from the app.");
      button.disabled = false;
    });
    li.append(row, meta, msg);
    return li;
  }));
}

const [tab] = await api.tabs.query({ active: true, currentWindow: true });
checkPermission();
if (tab) render(tab.id, (await api.runtime.sendMessage({ type: "list", tabId: tab.id })) ?? []);
