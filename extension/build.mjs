// Builds the unpacked extension for each browser into dist/<target>.
// Chrome and Edge share the "chrome" build. The only differences are in the
// manifest: Chrome needs a service worker and a fixed "key" (for a stable
// extension ID); Firefox needs background scripts and an add-on ID.
import { cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(fileURLToPath(import.meta.url));
export const targets = ["chrome", "firefox"];

export function manifestFor(target) {
  const base = JSON.parse(readFileSync(join(root, "manifest.base.json"), "utf8"));
  if (!targets.includes(target)) throw new Error(`unknown target ${target}`);
  const overrides = base[target];
  for (const t of targets) delete base[t];
  return { ...base, ...overrides };
}

export function build(target) {
  const out = join(root, "dist", target);
  rmSync(out, { recursive: true, force: true });
  mkdirSync(out, { recursive: true });
  cpSync(join(root, "src"), out, { recursive: true });
  writeFileSync(join(out, "manifest.json"), JSON.stringify(manifestFor(target), null, 2));
  return out;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  for (const t of process.argv.slice(2).length ? process.argv.slice(2) : targets) {
    console.log(`${t}: ${build(t)}`);
  }
}
