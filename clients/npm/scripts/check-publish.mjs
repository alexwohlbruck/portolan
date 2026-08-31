#!/usr/bin/env node
/**
 * Refuse to publish a package whose engines are missing or stale.
 *
 * The binaries are gitignored — they are staged from release archives, not
 * committed — so a checkout is one forgotten command away from publishing a
 * package that installs perfectly and has no engine in it. `files` would
 * happily ship a `bin/` that is empty or half full, and nothing downstream
 * notices until the first chart call.
 *
 * Two ways to get there, and both are checked:
 *
 *   1. MISSING — build-universal.mjs was never run, or ran against a dist
 *      that lacked a target, so some `bin/<platform>-<arch>/` is absent.
 *   2. DRIFT — VERSION was bumped without re-staging, so bin/ holds the
 *      previous engine while the manifest claims the new one.
 *
 * (2) is checked by asking each binary for its own version, which is the
 * only evidence that cannot be stale: a `--version` that disagrees with the
 * manifest is a mismatched engine no matter what any file says. Only the
 * binary for THIS host can be executed, so the other five are checked for
 * presence and size alone — which is exactly the guarantee CI needs, since
 * it stages and publishes in one run.
 *
 * Run: node scripts/check-publish.mjs   (wired to prepublishOnly)
 */

import { readFileSync, existsSync, statSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const pkgRoot = join(here, "..");
const repoRoot = join(pkgRoot, "..", "..");

const fail = (msg) => {
  console.error(`\n  publish blocked: ${msg}\n`);
  process.exit(1);
};

const version = readFileSync(join(repoRoot, "VERSION"), "utf8").trim();
const pkg = JSON.parse(readFileSync(join(pkgRoot, "package.json"), "utf8"));

if (pkg.version !== version) {
  fail(
    `VERSION says ${version}, package.json says ${pkg.version}.\n` +
      `  VERSION is the source of truth — run: node scripts/build-universal.mjs`,
  );
}

// An engine arriving a second way would shadow the bundled one at resolve
// time, and the two need not be the same build.
if (pkg.optionalDependencies) {
  fail(
    `package.json still declares optionalDependencies. The engines ship INSIDE\n` +
      `  this package now — re-run scripts/build-universal.mjs, which strips them.`,
  );
}

for (const dir of ["bin", "style"]) {
  if (!pkg.files?.includes(dir)) {
    fail(`"${dir}" is not in package.json "files", so it would not be published`);
  }
}

const TARGETS = [
  "darwin-arm64",
  "darwin-x64",
  "linux-arm64",
  "linux-x64",
  "win32-arm64",
  "win32-x64",
];

const self = `${process.platform}-${process.arch}`;
for (const key of TARGETS) {
  const exe = key.startsWith("win32-") ? "portolan.exe" : "portolan";
  const path = join(pkgRoot, "bin", key, exe);
  if (!existsSync(path)) {
    fail(
      `bin/${key}/${exe} is missing.\n` +
        `  Run: make dist && node scripts/build-universal.mjs`,
    );
  }
  // A zero-byte or truncated copy is a failed extraction, and it ships just
  // as happily as a real one.
  if (statSync(path).size < 1_000_000) {
    fail(`bin/${key}/${exe} is only ${statSync(path).size} bytes — a truncated extraction`);
  }
  if (key !== self) continue;

  let reported = "";
  try {
    reported = execFileSync(path, ["version"], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    }).trim();
  } catch (err) {
    fail(`bin/${key}/${exe} would not run: ${err.message}`);
  }
  if (!reported.includes(version)) {
    fail(
      `bin/${key}/${exe} reports "${reported}", which is not ${version}.\n` +
        `  bin/ is stale — run: make dist && node scripts/build-universal.mjs`,
    );
  }
}

if (!existsSync(join(pkgRoot, "style", "_default.json"))) {
  fail(`style/_default.json is missing — run scripts/build-universal.mjs`);
}

console.log(`  engine ${version}: all ${TARGETS.length} binaries staged, ${self} verified`);
