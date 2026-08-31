import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, existsSync, statSync } from "node:fs";
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

const TARGETS = [
  "darwin-arm64",
  "darwin-x64",
  "linux-arm64",
  "linux-x64",
  "win32-arm64",
  "win32-x64",
];

test("VERSION and the manifest agree, and no engine arrives twice", () => {
  const version = readFileSync(join(repoRoot, "VERSION"), "utf8").trim();
  assert.equal(pkg.version, version, "package.json version has drifted from VERSION");
  // The engines used to be six pinned optionalDependencies. One surviving a
  // rebase would resolve a SECOND engine beside the bundled one, and npm
  // skips an optional dependency it cannot resolve without failing the
  // install — so the broken shape looks exactly like the working one.
  assert.equal(pkg.optionalDependencies, undefined, "engines ship inside this package now");
});

/**
 * `files` is the whole contract for what reaches the registry. dist/ has
 * always been there; bin/ and style/ are new, gitignored, and staged rather
 * than committed — precisely the combination that publishes an empty package
 * without one word of complaint.
 */
test("bin/ and style/ are in files, or the package ships with no engine", () => {
  for (const dir of ["dist", "bin", "style"]) {
    assert.ok(pkg.files.includes(dir), `"${dir}" is missing from package.json files`);
  }
});

/**
 * Staged binaries are gitignored, so they are only as fresh as the last
 * generator run. Skips when nothing is staged (a checkout that has not run
 * `make dist`); asserts every target is present and sane when anything is.
 */
test("staged binaries, if present, cover every target at this version", () => {
  let keys = [];
  try {
    keys = readdirSync(join(pkgRoot, "bin")).filter((d) => !d.startsWith("."));
  } catch {
    return; // nothing staged
  }
  if (keys.length === 0) return;

  for (const key of TARGETS) {
    assert.ok(keys.includes(key), `bin/ is staged but has nothing for ${key}`);
    const exe = key.startsWith("win32-") ? "portolan.exe" : "portolan";
    const path = join(pkgRoot, "bin", key, exe);
    assert.ok(existsSync(path), `bin/${key}/${exe} is missing`);
    // a truncated extraction ships as happily as a real binary
    assert.ok(statSync(path).size > 1_000_000, `bin/${key}/${exe} is implausibly small`);
  }

  // Only this host's engine can be executed, and it is the one that can
  // prove bin/ is not stale: a --version that disagrees with the manifest
  // is a mismatched engine whatever the files claim.
  const self = `${process.platform}-${process.arch}`;
  if (!TARGETS.includes(self)) return;
  const reported = execFileSync(join(pkgRoot, "bin", self, "portolan"), ["version"], {
    encoding: "utf8",
  }).trim();
  assert.ok(
    reported.includes(pkg.version),
    `bin/${self} reports "${reported}", not ${pkg.version} — re-run build-universal.mjs`,
  );
});

/** The guard itself has to actually block; a check that always passes is worse than none. */
test("check-publish accepts a staged tree and rejects an unstaged one", () => {
  const run = () =>
    execFileSync("node", [join(pkgRoot, "scripts", "check-publish.mjs")], {
      cwd: pkgRoot,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    });

  const stagedTree = existsSync(join(pkgRoot, "bin", `${process.platform}-${process.arch}`));
  if (stagedTree) {
    try {
      run();
    } catch (err) {
      assert.fail(`check-publish rejected a staged tree:\n${err.stderr || err.message}`);
    }
  } else {
    assert.throws(run, "check-publish passed a tree with no binaries staged");
  }
});
