import { describe } from "./describe.js";

const api = globalThis.browser ?? globalThis.chrome;
const status = document.getElementById("status");
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
  document.getElementById("get-app").hidden = !!r?.ok;
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

function entry(tabId, it, title, detail, best) {
  const li = document.createElement("li");
  const row = document.createElement("div");
  row.className = "row";
  const name = document.createElement("span");
  name.className = "name";
  name.textContent = title;
  name.title = it.url;
  const button = document.createElement("button");
  button.textContent = "Send to app";
  if (best) button.className = "best";
  row.append(name, button);
  const meta = document.createElement("div");
  meta.className = "meta";
  meta.textContent = detail;
  const msg = document.createElement("div");
  msg.className = "msg";
  button.addEventListener("click", async () => {
    button.disabled = true;
    msg.className = "msg";
    msg.textContent = "Sending…";
    const r = await api.runtime.sendMessage({ type: "send", tabId, url: it.url });
    if (r?.ok) show(msg, true, "Sent. Press Download in the app's window.");
    else show(msg, false, r?.error ?? "No answer from the app.");
    button.disabled = false;
  });
  li.append(row, meta, msg);
  return li;
}

function render(tabId, items) {
  const list = document.getElementById("list");
  document.getElementById("empty").hidden = items.length > 0;
  list.replaceChildren(...describe(items).map((g) => {
    const li = entry(tabId, g.item, g.title, g.detail, g.title === "Video with sound");
    if (g.parts.length) {
      const note = document.createElement("div");
      note.className = "note";
      note.textContent = g.parts.some((p) => p.title === "Sound only") && g.title === "Video with sound"
        ? "Send this one: the app downloads the picture and the sound and joins them."
        : "Send this one for the whole thing.";
      const more = document.createElement("details");
      const summary = document.createElement("summary");
      summary.textContent = `Separate parts (${g.parts.length})`;
      const ul = document.createElement("ul");
      ul.append(...g.parts.map((p) => entry(tabId, p.item, p.title, p.detail, false)));
      more.append(summary, ul);
      li.append(note, more);
    }
    return li;
  }));
}

const [tab] = await api.tabs.query({ active: true, currentWindow: true });
checkPermission();
if (tab) render(tab.id, (await api.runtime.sendMessage({ type: "list", tabId: tab.id })) ?? []);
