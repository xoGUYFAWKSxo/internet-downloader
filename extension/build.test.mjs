import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { manifestFor } from "./build.mjs";

// The Chrome extension ID is derived from the manifest key; the registration
// script defaults to this ID, so the two must stay in sync.
const CHROME_ID = "akmjpphfkcdejdhhobbbnbddgdbajpem";

function idFromKey(key) {
  const hex = createHash("sha256").update(Buffer.from(key, "base64")).digest("hex").slice(0, 32);
  return [...hex].map((h) => String.fromCharCode(97 + parseInt(h, 16))).join("");
}

test("chrome manifest", () => {
  const m = manifestFor("chrome");
  assert.equal(m.manifest_version, 3);
  assert.equal(m.background.service_worker, "background.js");
  assert.equal(idFromKey(m.key), CHROME_ID);
  assert.ok(m.permissions.includes("nativeMessaging"));
  assert.equal(m.chrome, undefined);
  assert.equal(m.firefox, undefined);
  assert.equal(m.browser_specific_settings, undefined);
});

test("firefox manifest", () => {
  const m = manifestFor("firefox");
  assert.deepEqual(m.background.scripts, ["background.js"]);
  assert.equal(m.background.service_worker, undefined);
  assert.equal(m.browser_specific_settings.gecko.id, "internet-downloader@xoguyfawksxo");
  assert.equal(m.key, undefined);
  assert.ok(m.permissions.includes("nativeMessaging"));
});

test("both manifests can watch requests and use ES modules", () => {
  for (const t of ["chrome", "firefox"]) {
    const m = manifestFor(t);
    for (const p of ["webRequest", "cookies", "storage", "contextMenus", "notifications"]) assert.ok(m.permissions.includes(p), `${t} ${p}`);
    assert.deepEqual(m.host_permissions, ["<all_urls>"]);
    assert.equal(m.background.type, "module");
  }
});
