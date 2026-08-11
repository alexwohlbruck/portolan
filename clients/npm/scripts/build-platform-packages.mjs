#!/usr/bin/env node
/**
 * Build the per-platform npm packages that carry the engine.
 *
 * `@alexwohlbruck/portolan` declares each of these as an
 * optionalDependency with `os` and `cpu` set, so npm installs exactly
 * the one matching the host and skips the rest. That is how a version
 * bump of the wrapper ships a new engine with no fetch step.
 *
 * THE NAMING SEAM LIVES HERE AND NOWHERE ELSE. Go calls an architecture
 * `amd64`; Node calls the same one `x64`. The package names must use
 * Node's spelling because npm resolves them against
 * `process.platform`/`process.arch`, while the binaries arrive under
 * Go's because GOOS/GOARCH are what built them. Every other file is
 * spared the mapping.
 *
 *   node scripts/build-platform-packages.mjs --dist ../../dist
 *
 * --dist points at the directory `make dist` fills (or a directory of
 * downloaded release archives). Reads the archives rather than shelling
 * out to the Go toolchain, so the published binary is byte-identical to
 * the released one.
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
const outRoot = join(pkgRoot, "platforms");

const version = readFileSync(join(repoRoot, "VERSION"), "utf8").trim();
const wrapper = JSON.parse(readFileSync(join(pkgRoot, "package.json"), "utf8"));

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

rmSync(outRoot, { recursive: true, force: true });
mkdirSync(outRoot, { recursive: true });

const built = [];
for (const t of TARGETS) {
  const name = `@alexwohlbruck/portolan-${t.os}-${t.cpu}`;
  const archive = join(dist, `portolan_${version}_${t.goos}_${t.goarch}.${t.ext}`);
  if (!existsSync(archive)) {
    console.error(`missing archive ${archive}`);
    process.exit(1);
  }
  const dir = join(outRoot, `${t.os}-${t.cpu}`);
  const binDir = join(dir, "bin");
  mkdirSync(binDir, { recursive: true });

  // the archives unpack to portolan_<v>_<goos>_<goarch>/, holding the
  // binary plus style/ and the docs — take the binary and the curation,
  // because portolan reads ./style by default and a copy without it
  // draws every city in raw feed colours
  const stage = join(outRoot, ".stage");
  rmSync(stage, { recursive: true, force: true });
  mkdirSync(stage, { recursive: true });
  if (t.ext === "tar.gz") {
    execFileSync("tar", ["-xzf", archive, "-C", stage]);
  } else {
    execFileSync("unzip", ["-qo", archive, "-d", stage]);
  }
  const unpacked = join(stage, `portolan_${version}_${t.goos}_${t.goarch}`);
  const exe = t.os === "win32" ? "portolan.exe" : "portolan";
  execFileSync("cp", [join(unpacked, exe), join(binDir, exe)]);
  execFileSync("cp", ["-R", join(unpacked, "style"), join(dir, "style")]);
  if (t.os !== "win32") chmodSync(join(binDir, exe), 0o755);
  rmSync(stage, { recursive: true, force: true });

  writeFileSync(
    join(dir, "package.json"),
    JSON.stringify(
      {
        name,
        version,
        description: `portolan engine binary for ${t.os} ${t.cpu}.`,
        license: wrapper.license,
        repository: wrapper.repository,
        // npm installs an optional dependency only when os and cpu match
        // the host, which is what makes six packages cost one download
        os: [t.os],
        cpu: [t.cpu],
        files: ["bin", "style", "README.md"],
        preferUnplugged: true,
      },
      null,
      2,
    ) + "\n",
  );
  writeFileSync(
    join(dir, "README.md"),
    `# ${name}\n\nThe portolan engine binary for ${t.os} ${t.cpu}.\n\n` +
      `Not meant to be installed directly — it is an optional dependency of ` +
      `[\`@alexwohlbruck/portolan\`](https://www.npmjs.com/package/@alexwohlbruck/portolan), ` +
      `which picks the right one for your platform.\n`,
  );
  built.push({ name, dir });
  console.log(`  ${name}`);
}

// the wrapper's optionalDependencies must pin this exact version, or npm
// is free to resolve an engine that speaks a different PLNB layout
const deps = Object.fromEntries(built.map((b) => [b.name, version]));
if (JSON.stringify(wrapper.optionalDependencies) !== JSON.stringify(deps)) {
  wrapper.optionalDependencies = deps;
  wrapper.version = version;
  writeFileSync(join(pkgRoot, "package.json"), JSON.stringify(wrapper, null, 2) + "\n");
  console.log(`\nupdated package.json to ${version} and pinned all six`);
}

console.log(`\n${built.length} platform packages in ${outRoot}`);
console.log(`publish with: npm publish --access public (in each, then the wrapper)`);
