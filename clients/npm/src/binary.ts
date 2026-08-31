import { existsSync, readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

/**
 * Finding the engine.
 *
 * All six binaries ship INSIDE this package, under `bin/<platform>-<arch>/`.
 * One package, one publish, one thing to install — and the engine can never
 * disagree with the wrapper about its version, because they are the same
 * artifact.
 *
 * WHY NOT THE optionalDependency SPLIT (which this replaced): six sibling
 * packages with `os`/`cpu` gates is the esbuild shape, and it is genuinely
 * leaner per install — npm resolves the one matching the host and skips the
 * rest. It costs a chain of failure modes that all look like success. npm
 * SKIPS an optional dependency it cannot resolve and still calls the install
 * clean, so a half-finished publish, an `--omit=optional`, or an install on a
 * different platform than the one that runs all produce a working install
 * that dies at the first chart call. Publishing needs the six to go out
 * before the wrapper, and each is a separate registry write. For an engine
 * this size, paying ~22MB per install to make every one of those failures
 * impossible is the better trade.
 *
 * Deliberately NOT a postinstall download: that breaks offline installs,
 * air-gapped CI, `--ignore-scripts`, and — the case that matters here —
 * signed and notarized application bundles, where a binary that appeared
 * after signing is exactly what the OS refuses to run.
 *
 * Resolution order, first hit wins:
 *   1. an explicit path passed by the caller
 *   2. $PORTOLAN_BIN                     — dev override, or a bundled copy
 *   3. bin/<platform>-<arch>/ inside this package
 */

/**
 * Node's spelling of every target that has a build. Note the naming seam:
 * Node says `x64` where Go says `amd64`, for the same architecture. These
 * keys are NODE's, because `process.platform`/`process.arch` are what
 * choose between them; the release archives use Go's, because GOOS/GOARCH
 * are what built them. The mapping lives in scripts/build-universal.mjs
 * and nowhere else, which is the point.
 */
const SUPPORTED = [
  "darwin-arm64",
  "darwin-x64",
  "linux-arm64",
  "linux-x64",
  "win32-arm64",
  "win32-x64",
];

export class BinaryNotFoundError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "BinaryNotFoundError";
  }
}

/** This package's own root — `bin/` and `style/` sit beside `dist/`. */
function packageRoot(): string {
  return join(dirname(fileURLToPath(import.meta.url)), "..");
}

/** This package's own version, or "" if the manifest cannot be read. */
function selfVersion(): string {
  try {
    return JSON.parse(readFileSync(join(packageRoot(), "package.json"), "utf8")).version ?? "";
  } catch {
    return "";
  }
}

/** `darwin-arm64` etc. Node's names, not Go's — see the note on SUPPORTED. */
export function platformKey(): string {
  return `${process.platform}-${process.arch}`;
}

/**
 * The curation documents that ship with the engine.
 *
 * The engine reads `./style` relative to its WORKING DIRECTORY, not to the
 * binary, so it does not find this on its own — pass it as `styleDir` (or
 * `cwd`) if you want the shipped curation rather than raw feed colours.
 */
export function styleDir(): string {
  return join(packageRoot(), "style");
}

/** Locate the portolan executable for this host. */
export function resolveBinary(explicit?: string): string {
  if (explicit) {
    if (!existsSync(explicit)) {
      throw new BinaryNotFoundError(`no portolan executable at ${explicit}`);
    }
    return explicit;
  }
  const env = process.env.PORTOLAN_BIN;
  if (env) {
    if (!existsSync(env)) {
      throw new BinaryNotFoundError(`PORTOLAN_BIN is set to ${env}, which does not exist`);
    }
    return env;
  }

  const key = platformKey();
  if (!SUPPORTED.includes(key)) {
    throw new BinaryNotFoundError(
      `portolan has no build for ${key}. Supported: ${SUPPORTED.join(", ")}. ` +
        `Point PORTOLAN_BIN at a binary you built yourself if you have one.`,
    );
  }

  const exe = process.platform === "win32" ? "portolan.exe" : "portolan";
  const path = join(packageRoot(), "bin", key, exe);
  if (!existsSync(path)) {
    // The binaries are committed to the PUBLISHED package, not to the repo,
    // so the ordinary way to see this is running from a source checkout that
    // has not staged them yet. A published package missing one is a broken
    // publish — the `files` field or the staging step, not the consumer.
    throw new BinaryNotFoundError(
      `@alexwohlbruck/portolan ${selfVersion()} has no bin/${key}/${exe}. ` +
        `In a source checkout, stage the binaries first: ` +
        `make dist && node clients/npm/scripts/build-universal.mjs. ` +
        `In an installed package this is a broken publish — reinstall, or set PORTOLAN_BIN.`,
    );
  }
  return path;
}
