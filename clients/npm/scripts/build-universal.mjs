#!/usr/bin/env node
/**
 * Stage the engine binaries into the package that ships them.
 *
 * `@alexwohlbruck/portolan` is universal: every target's binary lives in
 * `bin/<platform>-<arch>/`, and `resolveBinary()` picks one at RUN time from
 * `process.platform`/`process.arch`. One package, one publish, and an engine
 * that cannot disagree with its wrapper about anything, because they are the
 * same artifact. See the note at the top of src/binary.ts for why this
 * replaced six `optionalDependencies`.
 *
 *   node scripts/build-universal.mjs --dist ../../dist
 *
 * --dist points at the directory `make dist` fills (or a directory of
 * downloaded release archives). Reads the archives rather than shelling out
 * to the Go toolchain, so the published binary is byte-identical to the
 * released one.
 */

import { mkdirSync, rmSync, writeFileSync, readFileSync, existsSync, chmodSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const pkgRoot = join(here, "..");
const repoRoot = join(pkgRoot, "..", "..");

const args = process.argv.slice(2);
const distArg = args.indexOf("--dist");
const dist = distArg >= 0 ? args[distArg + 1] : join(repoRoot, "dist");
const binRoot = join(pkgRoot, "bin");
const styleOut = join(pkgRoot, "style");

const version = readFileSync(join(repoRoot, "VERSION"), "utf8").trim();
const pkg = JSON.parse(readFileSync(join(pkgRoot, "package.json"), "utf8"));

// goos/goarch -> node platform/arch. The only place this mapping exists.
const TARGETS = [
  { goos: "darwin", goarch: "arm64", os: "darwin", cpu: "arm64", ext: "tar.gz" },
  { goos: "darwin", goarch: "amd64", os: "darwin", cpu: "x64", ext: "tar.gz" },
  { goos: "linux", goarch: "arm64", os: "linux", cpu: "arm64", ext: "tar.gz" },
  { goos: "linux", goarch: "amd64", os: "linux", cpu: "x64", ext: "tar.gz" },
  { goos: "windows", goarch: "arm64", os: "win32", cpu: "arm64", ext: "zip" },
  { goos: "windows", goarch: "amd64", os: "win32", cpu: "x64", ext: "zip" },
];

if (!existsSync(dist)) {
  console.error(`no dist directory at ${dist} — run \`make dist\` first`);
  process.exit(1);
}

rmSync(binRoot, { recursive: true, force: true });
rmSync(styleOut, { recursive: true, force: true });
mkdirSync(binRoot, { recursive: true });

const staged = [];
for (const t of TARGETS) {
  const key = `${t.os}-${t.cpu}`;
  const archive = join(dist, `portolan_${version}_${t.goos}_${t.goarch}.${t.ext}`);
  if (!existsSync(archive)) {
    console.error(`missing archive ${archive}`);
    process.exit(1);
  }

  // the archives unpack to portolan_<v>_<goos>_<goarch>/, holding the binary
  // plus style/ and the docs
  const stage = join(binRoot, ".stage");
  rmSync(stage, { recursive: true, force: true });
  mkdirSync(stage, { recursive: true });
  if (t.ext === "tar.gz") {
    execFileSync("tar", ["-xzf", archive, "-C", stage]);
  } else {
    execFileSync("unzip", ["-qo", archive, "-d", stage]);
  }
  const unpacked = join(stage, `portolan_${version}_${t.goos}_${t.goarch}`);

  const exe = t.os === "win32" ? "portolan.exe" : "portolan";
  const dir = join(binRoot, key);
  mkdirSync(dir, { recursive: true });
  execFileSync("cp", [join(unpacked, exe), join(dir, exe)]);
  if (t.os !== "win32") chmodSync(join(dir, exe), 0o755);

  // The curation is identical in all six archives — the engine is what
  // differs — so it is copied ONCE, at the package root, instead of six
  // times. Six copies was a real cost of the old split and bought nothing.
  if (!existsSync(styleOut)) {
    execFileSync("cp", ["-R", join(unpacked, "style"), styleOut]);
  }

  rmSync(stage, { recursive: true, force: true });
  staged.push(key);
  console.log(`  bin/${key}/${exe}`);
}

// VERSION is the source of truth; the manifest follows it.
let dirty = false;
if (pkg.version !== version) {
  pkg.version = version;
  dirty = true;
}
// The engine used to arrive through six pinned optionalDependencies. If any
// survive a rebase they would resolve a SECOND engine beside the bundled one.
if (pkg.optionalDependencies) {
  delete pkg.optionalDependencies;
  dirty = true;
}
if (dirty) {
  writeFileSync(join(pkgRoot, "package.json"), JSON.stringify(pkg, null, 2) + "\n");
  console.log(`\nupdated package.json to ${version}`);
}

console.log(`\n${staged.length} engines + style/ staged in ${pkgRoot}`);
console.log(`publish with: npm publish --access public`);
