import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { join } from "node:path";
import { createRequire } from "node:module";

// Packaging tests. Everything here failed for a real consumer at
// install time while every functional test stayed green, which is the
// class of bug this file exists to catch: the code is correct and
// unreachable.

const pkgRoot = fileURLToPath(new URL("..", import.meta.url));
const pkg = JSON.parse(readFileSync(join(pkgRoot, "package.json"), "utf8"));
const repoRoot = join(pkgRoot, "..", "..");

/**
 * The renderer half of the Electron split imports the client and the
 * decoder directly, because an Electron renderer with context isolation
 * has no Node and a bundler fails the build on `node:child_process`.
 *
 * The barrel legitimately pulls it (it exports PortolanServer), so the
 * subpaths are the only way that split can exist — and they are only
 * real if BOTH the export map lists them AND the modules behind them
 * stay Node-free. Assert both: one without the other is useless.
 */
test("./client and ./plnb are exported and free of node: imports", () => {
  for (const sub of ["./client", "./plnb"]) {
    assert.ok(pkg.exports[sub], `package.json exports is missing ${sub}`);
  }

  // walk the static import graph from each subpath entry
  const seen = new Set();
  const walk = (file) => {
    if (seen.has(file)) return;
    seen.add(file);
    const src = readFileSync(file, "utf8");
    for (const m of src.matchAll(/from\s+"([^"]+)"/g)) {
      const spec = m[1];
      assert.ok(
        !spec.startsWith("node:"),
        `${file} imports ${spec} — that breaks the renderer, which has no Node`,
      );
      if (spec.startsWith(".")) walk(join(file, "..", spec));
    }
  };
  for (const sub of ["./client", "./plnb"]) {
    walk(join(pkgRoot, pkg.exports[sub].default));
  }

  // and the barrel is expected to pull Node — if it ever stops, the
  // subpaths are no longer load-bearing and this test is telling you so
  const barrel = new Set();
  const walkAll = (file) => {
    if (barrel.has(file)) return;
    barrel.add(file);
    const src = readFileSync(file, "utf8");
    for (const m of src.matchAll(/from\s+"([^"]+)"/g)) {
      if (m[1].startsWith(".")) walkAll(join(file, "..", m[1]));
      else barrel.add(m[1]);
    }
  };
  walkAll(join(pkgRoot, pkg.exports["."].default));
  assert.ok(
    [...barrel].some((s) => typeof s === "string" && s.startsWith("node:")),
    "the barrel no longer imports node: — re-check whether the subpaths are still needed",
  );
});

test("every export subpath resolves to a file that exists", () => {
  const require = createRequire(join(pkgRoot, "package.json"));
  for (const [sub, entry] of Object.entries(pkg.exports)) {
    const targets = typeof entry === "string" ? [entry] : Object.values(entry);
    for (const t of targets) {
      assert.ok(
        require("node:fs").existsSync(join(pkgRoot, t)),
        `exports["${sub}"] points at ${t}, which does not exist — run npm run build`,
      );
    }
  }
});

/**
 * The engine packages are optionalDependencies, so npm skips one it
 * cannot resolve WITHOUT failing the install. A wrapper pinned to a
 * version the engine packages do not have installs clean and dies at
 * the first chart call.
 */
test("VERSION, the wrapper, and every pinned engine agree", () => {
  const version = readFileSync(join(repoRoot, "VERSION"), "utf8").trim();
  assert.equal(pkg.version, version, "package.json version has drifted from VERSION");
  const deps = Object.entries(pkg.optionalDependencies ?? {});
  assert.ok(deps.length > 0, "no engine packages pinned");
  for (const [name, want] of deps) {
    assert.equal(want, version, `${name} is pinned to ${want}, not ${version}`);
  }
});

/**
 * Staged output under platforms/ is gitignored, so it is only as fresh
 * as the last generator run — and publishing it stale is precisely the
 * failure above. Skips when nothing is staged (a checkout that has not
 * run `make dist`); asserts agreement when something is.
 */
test("staged platform packages, if present, are at this version", () => {
  const version = pkg.version;
  let dirs = [];
  try {
    dirs = readdirSync(join(pkgRoot, "platforms"));
  } catch {
    return; // nothing staged
  }
  for (const d of dirs) {
    if (d.startsWith(".")) continue;
    const staged = JSON.parse(
      readFileSync(join(pkgRoot, "platforms", d, "package.json"), "utf8"),
    );
    assert.equal(
      staged.version,
      version,
      `platforms/${d} is staged at ${staged.version}, not ${version} — ` +
        `re-run scripts/build-platform-packages.mjs before publishing`,
    );
  }
});

/** The guard itself has to actually block; a check that always passes is worse than none. */
test("check-publish rejects a stale platform stage", () => {
  const run = (env) =>
    execFileSync("node", [join(pkgRoot, "scripts", "check-publish.mjs")], {
      cwd: pkgRoot,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
      env: { ...process.env, ...env },
    });
  // no network in the harness, and the registry leg is covered by the
  // release runbook — this asserts the local-drift leg
  try {
    run({ PORTOLAN_SKIP_REGISTRY_CHECK: "1" });
  } catch (err) {
    assert.fail(`check-publish rejected a good tree:\n${err.stderr || err.message}`);
  }
});
