#!/usr/bin/env node
/**
 * Refuse to publish a wrapper whose engine is missing or mismatched.
 *
 * The engine packages are `optionalDependencies`, which npm SKIPS
 * silently when it cannot resolve one — the install prints "added 1
 * package", the lockfile looks right, and the failure surfaces at the
 * first chart call as a missing binary. That is the worst shape a
 * packaging bug can take, so it is checked before publishing rather than
 * discovered downstream.
 *
 * Two ways to get there, and both are checked:
 *
 *   1. LOCAL DRIFT — VERSION was bumped without re-running
 *      build-platform-packages.mjs, so platforms/ still holds the
 *      previous engine.
 *   2. ORDER — the wrapper was published before the engine packages, so
 *      the pinned version does not exist on the registry yet.
 *
 * Run: node scripts/check-publish.mjs   (wired to prepublishOnly)
 * Set PORTOLAN_SKIP_REGISTRY_CHECK=1 to keep (1) and drop (2) offline.
 */

import { readFileSync, existsSync } from "node:fs";
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
const wrapper = JSON.parse(readFileSync(join(pkgRoot, "package.json"), "utf8"));

if (wrapper.version !== version) {
  fail(
    `VERSION says ${version}, package.json says ${wrapper.version}.\n` +
      `  VERSION is the source of truth — run: node scripts/build-platform-packages.mjs`,
  );
}

const deps = Object.entries(wrapper.optionalDependencies ?? {});
if (deps.length === 0) fail("no optionalDependencies — the wrapper would ship without an engine");

for (const [name, want] of deps) {
  if (want !== version) {
    fail(`${name} is pinned to ${want}, but this is ${version}`);
  }
  // the staged package this publish run is meant to have produced
  const dir = join(pkgRoot, "platforms", name.split("/")[1].replace("portolan-", ""));
  if (!existsSync(dir)) {
    fail(
      `platforms/ has nothing staged for ${name}.\n` +
        `  Run: make dist && node scripts/build-platform-packages.mjs`,
    );
  }
  const staged = JSON.parse(readFileSync(join(dir, "package.json"), "utf8")).version;
  if (staged !== version) {
    fail(
      `${name} is staged at ${staged} but this publish is ${version}.\n` +
        `  platforms/ is stale — run: make dist && node scripts/build-platform-packages.mjs`,
    );
  }
}

if (process.env.PORTOLAN_SKIP_REGISTRY_CHECK) {
  console.log("  skipping the registry check (PORTOLAN_SKIP_REGISTRY_CHECK)");
} else {
  for (const [name] of deps) {
    let found = "";
    try {
      found = execFileSync("npm", ["view", `${name}@${version}`, "version"], {
        encoding: "utf8",
        stdio: ["ignore", "pipe", "pipe"],
      }).trim();
    } catch {
      /* not published, or the registry is unreachable */
    }
    if (found !== version) {
      fail(
        `${name}@${version} is not on the registry.\n` +
          `  Publish the six engine packages FIRST — the wrapper depends on them\n` +
          `  optionally, so publishing it now installs cleanly and fails at the\n` +
          `  first chart call. See docs/RELEASING.md.`,
      );
    }
  }
}

console.log(`  engine ${version}: all ${deps.length} platform packages accounted for`);
