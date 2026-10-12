const api = globalThis.browser ?? globalThis.chrome;
const status = document.getElementById("status");

document.getElementById("check").addEventListener("click", async () => {
  status.className = "";
  status.textContent = "Checking…";
  const r = await api.runtime.sendMessage({ type: "ping" });
  if (r?.ok) {
    status.className = "ok";
    status.textContent = "Connected. App version " + (r.payload?.version ?? "unknown") + ".";
  } else {
    status.className = "bad";
    status.textContent = r?.error ?? "No answer from the app.";
  }
});
