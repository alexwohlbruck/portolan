import { spawn, type ChildProcess } from "node:child_process";
import { randomBytes } from "node:crypto";
import { resolveBinary } from "./binary.js";
import { SUPPORTED_PLNB } from "./plnb.js";
import type { VersionInfo } from "./types.js";

export interface StartOptions {
  /** Path to the executable. Defaults to the bundled per-platform binary. */
  binary?: string;
  /** Listen host. Loopback by default, and you should keep it that way. */
  host?: string;
  /** Port. 0 (the default) lets the OS pick and the engine reports it back. */
  port?: number;
  /**
   * Bearer token. A random one is generated per launch by default.
   *
   * Do not turn this off casually. A chart request names files for the
   * engine to read, so an unauthenticated port is a file-read oracle for
   * every other process on the machine — loopback is not a boundary
   * between processes on one host. Pass `null` only if something else is
   * already isolating the port.
   */
  token?: string | null;
  /** Curation directory the engine reads when a request names a city. */
  styleDir?: string;
  /** Working directory. The engine reads `./style` relative to this. */
  cwd?: string;
  /** How long to wait for the port line before giving up. Default 10s. */
  startupTimeoutMs?: number;
  /**
   * Refuse to start when the engine's PLNB layout version is not the one
   * this package decodes. Default true — drawing against a layout you do
   * not understand produces plausible-but-wrong geometry rather than an
   * error, which is the failure this check exists to prevent.
   */
  requireCompatible?: boolean;
  /** Called with each stderr line — the engine's build log. */
  onLog?: (line: string) => void;
}

export class PortolanStartupError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "PortolanStartupError";
  }
}

export class IncompatibleEngineError extends Error {
  constructor(
    message: string,
    readonly info: VersionInfo,
  ) {
    super(message);
    this.name = "IncompatibleEngineError";
  }
}

/**
 * A running `portolan serve` process.
 *
 * The engine prints its bound port on stdout as the first line, which is
 * how a `port: 0` launch is usable: the OS picks a free port and there is
 * no race between choosing one and binding it.
 */
export class PortolanServer {
  private constructor(
    private readonly proc: ChildProcess,
    readonly port: number,
    readonly host: string,
    readonly token: string | null,
    readonly version: VersionInfo,
  ) {}

  get origin(): string {
    return `http://${this.host}:${this.port}`;
  }

  /** Headers every request needs. */
  get headers(): Record<string, string> {
    return this.token ? { Authorization: `Bearer ${this.token}` } : {};
  }

  static async start(opts: StartOptions = {}): Promise<PortolanServer> {
    const bin = resolveBinary(opts.binary);
    const host = opts.host ?? "127.0.0.1";
    const token = opts.token === null ? null : (opts.token ?? randomBytes(24).toString("hex"));

    const args = ["serve", "--addr", `${host}:${opts.port ?? 0}`];
    if (token) args.push("--token", token);
    if (opts.styleDir) args.push("--style-dir", opts.styleDir);

    const proc = spawn(bin, args, {
      cwd: opts.cwd,
      stdio: ["ignore", "pipe", "pipe"],
      // do not let the engine outlive an abrupt parent exit
      detached: false,
    });

    const port = await firstLinePort(proc, opts.startupTimeoutMs ?? 10_000, opts.onLog);
    const server = new PortolanServer(proc, port, host, token, {
      version: "unknown",
      plnb: -1,
      formats: [],
      bands: [],
      auth: false,
    });

    const info = await server.fetchVersion();
    const withInfo = new PortolanServer(proc, port, host, token, info);
    if ((opts.requireCompatible ?? true) && info.plnb !== SUPPORTED_PLNB) {
      await withInfo.stop();
      throw new IncompatibleEngineError(
        `engine ${info.version} emits PLNB layout ${info.plnb}, this client decodes ` +
          `${SUPPORTED_PLNB}. Match the versions, or pass requireCompatible: false and ` +
          `use format "geojson".`,
        info,
      );
    }
    return withInfo;
  }

  private async fetchVersion(): Promise<VersionInfo> {
    // /version is open even when a token is set, so a caller can tell
    // "not up yet" from "wrong token"
    const r = await fetch(`${this.origin}/version`);
    if (!r.ok) throw new PortolanStartupError(`GET /version returned ${r.status}`);
    return (await r.json()) as VersionInfo;
  }

  /** Whether the process is still alive. */
  get running(): boolean {
    return this.proc.exitCode === null && !this.proc.killed;
  }

  /** Stop the engine. Safe to call twice. */
  async stop(): Promise<void> {
    if (!this.running) return;
    await new Promise<void>((resolve) => {
      const done = () => resolve();
      this.proc.once("exit", done);
      this.proc.kill("SIGTERM");
      // it has no shutdown work to do; if it has not gone in a second
      // something is wedged and SIGKILL is the honest answer
      setTimeout(() => {
        if (this.running) this.proc.kill("SIGKILL");
      }, 1000).unref?.();
    });
  }

  /** `await using` support, so a server cannot outlive its scope. */
  async [Symbol.asyncDispose](): Promise<void> {
    await this.stop();
  }
}

/**
 * Read the bound port off stdout's first line.
 *
 * Rejects on early exit rather than hanging until the timeout — a
 * mis-specified binary or a port already in use should say so at once,
 * and the engine's stderr is the only place that says why.
 */
function firstLinePort(
  proc: ChildProcess,
  timeoutMs: number,
  onLog?: (line: string) => void,
): Promise<number> {
  return new Promise((resolve, reject) => {
    let stdout = "";
    let stderr = "";
    let settled = false;

    const finish = (fn: () => void) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      fn();
    };

    const timer = setTimeout(() => {
      finish(() => {
        proc.kill("SIGKILL");
        reject(
          new PortolanStartupError(
            `portolan serve printed no port within ${timeoutMs}ms` +
              (stderr ? `\n${stderr.trim()}` : ""),
          ),
        );
      });
    }, timeoutMs);
    timer.unref?.();

    proc.stdout?.on("data", (chunk: Buffer) => {
      stdout += chunk.toString();
      const nl = stdout.indexOf("\n");
      if (nl < 0) return;
      const port = Number.parseInt(stdout.slice(0, nl).trim(), 10);
      finish(() => {
        if (!Number.isInteger(port) || port <= 0) {
          proc.kill("SIGKILL");
          reject(
            new PortolanStartupError(
              `expected a port on the first stdout line, got ${JSON.stringify(stdout.slice(0, nl))}`,
            ),
          );
          return;
        }
        resolve(port);
      });
    });

    proc.stderr?.on("data", (chunk: Buffer) => {
      const text = chunk.toString();
      stderr += text;
      if (onLog) for (const line of text.split("\n")) if (line) onLog(line);
    });

    proc.once("error", (err) =>
      finish(() => reject(new PortolanStartupError(`could not spawn portolan: ${err.message}`))),
    );
    proc.once("exit", (code) =>
      finish(() =>
        reject(
          new PortolanStartupError(
            `portolan serve exited with code ${code} before printing a port` +
              (stderr ? `\n${stderr.trim()}` : ""),
          ),
        ),
      ),
    );
  });
}
