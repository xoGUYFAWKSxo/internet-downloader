// Builds the unpacked extension for each browser into dist/<target>.
// Chrome and Edge share the "chrome" build. The only differences are in the
// manifest: Chrome needs a service worker and a fixed "key" (for a stable
// extension ID); Firefox needs background scripts and an add-on ID.
import { cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(fileURLToPath(import.meta.url));
export const targets = ["chrome", "firefox"];

// version, when given, replaces the manifest's: browsers take 1 to 4
// numbers with dots, so a release's "1.2.0" fits but "1.2.0-beta" doesn't.
export function manifestFor(target, version) {
  const base = JSON.parse(readFileSync(join(root, "manifest.base.json"), "utf8"));
  if (!targets.includes(target)) throw new Error(`unknown target ${target}`);
  const overrides = base[target];
  for (const t of targets) delete base[t];
  if (version !== undefined) {
    if (!/^\d+(\.\d+){0,3}$/.test(version)) throw new Error(`bad version ${version}`);
    base.version = version;
  }
  return { ...base, ...overrides };
}

export function build(target, version) {
  const out = join(root, "dist", target);
  rmSync(out, { recursive: true, force: true });
  mkdirSync(out, { recursive: true });
  cpSync(join(root, "src"), out, { recursive: true });
  writeFileSync(join(out, "manifest.json"), JSON.stringify(manifestFor(target, version), null, 2));
  return out;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2);
  const version = args.find((a) => a.startsWith("--version="))?.slice("--version=".length);
  const names = args.filter((a) => !a.startsWith("--"));
  for (const t of names.length ? names : targets) {
    console.log(`${t}: ${build(t, version)}`);
  }
}
