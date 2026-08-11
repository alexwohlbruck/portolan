import { createRequire } from "node:module";
import { existsSync } from "node:fs";
import { join } from "node:path";

/**
 * Finding the engine.
 *
 * The binary ships INSIDE the package, one npm package per platform,
 * pulled in as an optionalDependency — the same shape esbuild uses.
 * npm installs only the one matching the host, so a version bump of
 * `@alexwohlbruck/portolan` ships a new engine with no separate fetch
 * step and nothing to keep in sync.
 *
 * Deliberately NOT a postinstall download: that breaks offline installs,
 * air-gapped CI, and — the case that matters here — signed and notarized
 * application bundles, where a binary that appeared after signing is
 * exactly what the OS refuses to run.
 *
 * Resolution order, first hit wins:
 *   1. an explicit path passed by the caller
 *   2. $PORTOLAN_BIN                     — dev override, or a bundled copy
 *   3. the per-platform optional dependency
 */

const PLATFORMS: Record<string, string> = {
  "darwin-arm64": "@alexwohlbruck/portolan-darwin-arm64",
  "darwin-x64": "@alexwohlbruck/portolan-darwin-x64",
  "linux-arm64": "@alexwohlbruck/portolan-linux-arm64",
  "linux-x64": "@alexwohlbruck/portolan-linux-x64",
  "win32-arm64": "@alexwohlbruck/portolan-win32-arm64",
  "win32-x64": "@alexwohlbruck/portolan-win32-x64",
};

export class BinaryNotFoundError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "BinaryNotFoundError";
  }
}

/** `darwin-arm64` etc. Node's names, not Go's — see the note in resolveBinary. */
export function platformKey(): string {
  return `${process.platform}-${process.arch}`;
}

/**
 * Locate the portolan executable.
 *
 * Note the naming seam: Node says `x64` where Go says `amd64`, for the
 * same architecture. The per-platform package names use NODE's spelling
 * because npm resolves them by `process.platform`/`process.arch`; the
 * release archives use Go's because `GOOS`/`GOARCH` are what built them.
 * The mapping lives in scripts/build-platform-packages.mjs and nowhere
 * else, which is the point.
 */
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
  const pkg = PLATFORMS[key];
  if (!pkg) {
    throw new BinaryNotFoundError(
      `portolan has no build for ${key}. Supported: ${Object.keys(PLATFORMS).join(", ")}. ` +
        `Point PORTOLAN_BIN at a binary you built yourself if you have one.`,
    );
  }
  const exe = process.platform === "win32" ? "portolan.exe" : "portolan";
  const require = createRequire(import.meta.url);
  try {
    // resolve the package's manifest rather than a JS entry point: these
    // packages contain a binary and nothing importable
    const manifest = require.resolve(`${pkg}/package.json`);
    const path = join(manifest, "..", "bin", exe);
    if (!existsSync(path)) {
      throw new BinaryNotFoundError(`${pkg} is installed but has no bin/${exe}`);
    }
    return path;
  } catch (err) {
    if (err instanceof BinaryNotFoundError) throw err;
    throw new BinaryNotFoundError(
      `${pkg} is not installed. It is an optionalDependency, so this usually means ` +
        `the install ran with --no-optional, or on a different platform than it is ` +
        `running on (npm resolves optional deps at install time, not run time). ` +
        `Reinstall on this machine, or set PORTOLAN_BIN.`,
    );
  }
}
