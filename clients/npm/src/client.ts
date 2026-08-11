import { decodePlnb, type Plnb } from "./plnb.js";
import type { PortolanServer } from "./server.js";
import type { Artifact, ChartRequest, ProgressEvent, VersionInfo } from "./types.js";

export class PortolanError extends Error {
  constructor(
    message: string,
    readonly status?: number,
  ) {
    super(message);
    this.name = "PortolanError";
  }
}

/** camelCase in, snake_case on the wire — the mapping lives only here. */
function toWire(req: ChartRequest): Record<string, unknown> {
  const w: Record<string, unknown> = {};
  const set = (k: string, v: unknown) => {
    if (v !== undefined && v !== null) w[k] = v;
  };
  set("gtfs", req.gtfs);
  set("gtfs_inline", req.gtfsInline);
  set("rail", req.rail);
  set("corridors", req.corridors);
  set("corridors_inline", req.corridorsInline);
  set("corridor_nodes", req.corridorNodes);
  set("stops", req.stops);
  set("streets", req.streets);
  set("bbox", req.bbox);
  set("anchor", req.anchor);
  set("city", req.city);
  set("style_dir", req.styleDir);
  set("line_agencies", req.lineAgencies);
  set("scenario", req.scenario);
  set("cover", req.cover);
  set("format", req.format);
  // the engine takes the band as a string, and 0 is a REAL band — so it
  // must survive the falsy check that would drop it
  if (req.band !== undefined && req.band !== null) w["band"] = String(req.band);
  return w;
}

export interface ChartOptions {
  /** Called for each progress event, including log lines. */
  onProgress?: (e: ProgressEvent) => void;
  /** Abort the build. Cancels server-side, not just locally. */
  signal?: AbortSignal;
}

/** A build in flight, or finished. */
export class Job {
  constructor(
    readonly id: string,
    private readonly client: PortolanClient,
  ) {}

  /** Stream progress until the terminal frame. */
  watch(onProgress?: (e: ProgressEvent) => void, signal?: AbortSignal): Promise<void> {
    return this.client.watch(this.id, onProgress, signal);
  }
  /** Abandon the build. The engine stops between stages. */
  cancel(): Promise<void> {
    return this.client.cancel(this.id);
  }
  /** Fetch an artifact as bytes. */
  bytes(artifact?: Artifact): Promise<Uint8Array> {
    return this.client.artifactBytes(this.id, artifact);
  }
  /** Fetch and decode the binary segments. */
  plnb(): Promise<Plnb> {
    return this.client.plnb(this.id);
  }
  /** Fetch an artifact as parsed JSON. */
  json<T = unknown>(artifact?: Artifact): Promise<T> {
    return this.client.artifactJson<T>(this.id, artifact);
  }
}

export class PortolanClient {
  constructor(
    private readonly origin: string,
    private readonly headers: Record<string, string> = {},
  ) {}

  /** Point a client at an already-running server. */
  static at(origin: string, token?: string): PortolanClient {
    return new PortolanClient(origin, token ? { Authorization: `Bearer ${token}` } : {});
  }

  /** Wrap a server this process started. */
  static from(server: PortolanServer): PortolanClient {
    return new PortolanClient(server.origin, server.headers);
  }

  async version(): Promise<VersionInfo> {
    const r = await fetch(`${this.origin}/version`);
    if (!r.ok) throw new PortolanError(`GET /version: ${r.status}`, r.status);
    return (await r.json()) as VersionInfo;
  }

  /** Start a build. Returns as soon as the engine accepts it. */
  async start(req: ChartRequest): Promise<Job> {
    const r = await fetch(`${this.origin}/chart`, {
      method: "POST",
      headers: { ...this.headers, "Content-Type": "application/json" },
      body: JSON.stringify(toWire(req)),
    });
    // 202, not 200 — the build has been accepted, not completed
    if (r.status !== 202) {
      throw new PortolanError(`POST /chart: ${r.status} ${(await r.text()).trim()}`, r.status);
    }
    const { id } = (await r.json()) as { id: string };
    return new Job(id, this);
  }

  /**
   * Build and wait. The common case.
   *
   * Aborting cancels SERVER-side as well as locally: an interactive
   * caller supersedes builds constantly, and one that keeps running
   * after nobody wants it piles up behind the one they do.
   */
  async chart(req: ChartRequest, opts: ChartOptions = {}): Promise<Job> {
    const job = await this.start(req);
    const onAbort = () => void job.cancel().catch(() => {});
    opts.signal?.addEventListener("abort", onAbort, { once: true });
    try {
      await job.watch(opts.onProgress, opts.signal);
    } finally {
      opts.signal?.removeEventListener("abort", onAbort);
    }
    return job;
  }

  /** Follow a job's SSE stream until it ends. */
  async watch(
    id: string,
    onProgress?: (e: ProgressEvent) => void,
    signal?: AbortSignal,
  ): Promise<void> {
    const r = await fetch(`${this.origin}/chart/${id}/progress`, {
      headers: this.headers,
      signal,
    });
    if (!r.ok || !r.body) {
      throw new PortolanError(`GET progress: ${r.status}`, r.status);
    }
    const reader = r.body.getReader();
    const dec = new TextDecoder();
    let buf = "";
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        // SSE frames are separated by a blank line
        let sep: number;
        while ((sep = buf.indexOf("\n\n")) >= 0) {
          const frame = buf.slice(0, sep);
          buf = buf.slice(sep + 2);
          for (const line of frame.split("\n")) {
            if (!line.startsWith("data: ")) continue;
            const e = JSON.parse(line.slice(6)) as ProgressEvent;
            onProgress?.(e);
            if (e.done) {
              if (e.error) throw new PortolanError(`build failed: ${e.error}`);
              return;
            }
          }
        }
      }
    } finally {
      reader.cancel().catch(() => {});
    }
  }

  async cancel(id: string): Promise<void> {
    const r = await fetch(`${this.origin}/chart/${id}/cancel`, {
      method: "POST",
      headers: this.headers,
    });
    if (r.status !== 204) throw new PortolanError(`cancel: ${r.status}`, r.status);
  }

  async status(id: string): Promise<{ id: string; done: boolean; error?: string }> {
    const r = await fetch(`${this.origin}/chart/${id}`, { headers: this.headers });
    if (!r.ok) throw new PortolanError(`status: ${r.status}`, r.status);
    return (await r.json()) as { id: string; done: boolean; error?: string };
  }

  async artifactBytes(id: string, artifact: Artifact = "segments"): Promise<Uint8Array> {
    const q = artifact === "segments" ? "" : `?artifact=${artifact}`;
    const r = await fetch(`${this.origin}/chart/${id}/build${q}`, { headers: this.headers });
    if (!r.ok) {
      throw new PortolanError(`GET build: ${r.status} ${(await r.text()).trim()}`, r.status);
    }
    return new Uint8Array(await r.arrayBuffer());
  }

  async artifactJson<T = unknown>(id: string, artifact: Artifact = "segments"): Promise<T> {
    const bytes = await this.artifactBytes(id, artifact);
    return JSON.parse(new TextDecoder().decode(bytes)) as T;
  }

  async plnb(id: string): Promise<Plnb> {
    return decodePlnb(await this.artifactBytes(id, "segments"));
  }
}
