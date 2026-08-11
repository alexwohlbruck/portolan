/**
 * @alexwohlbruck/portolan — automatic transit line maps.
 *
 * Spawns the portolan engine, speaks its build API, and decodes its
 * binary output. The engine ships inside the package, one per platform,
 * so a version bump here ships a new engine.
 *
 *   import { portolan } from "@alexwohlbruck/portolan";
 *
 *   await using p = await portolan();
 *   const job = await p.chart({
 *     gtfsInline: { "routes.txt": "...", "stops.txt": "..." },
 *     corridorsInline: graph,
 *     format: "bin",
 *     band: 15,
 *   }, { onProgress: e => console.log(e.stage, e.pct) });
 *
 *   const plnb = await job.plnb();
 */

export { PortolanServer, PortolanStartupError, IncompatibleEngineError } from "./server.js";
export type { StartOptions } from "./server.js";
export { PortolanClient, PortolanError, Job } from "./client.js";
export type { ChartOptions } from "./client.js";
export { Plnb, decodePlnb, PlnbError, SUPPORTED_PLNB } from "./plnb.js";
export { resolveBinary, platformKey, BinaryNotFoundError } from "./binary.js";
export type {
  ChartRequest,
  GtfsTables,
  Band,
  Format,
  VersionInfo,
  ProgressEvent,
  Artifact,
  PlnbFeature,
} from "./types.js";

import { PortolanServer, type StartOptions } from "./server.js";
import { PortolanClient } from "./client.js";
import type { ChartOptions, Job } from "./client.js";
import type { ChartRequest, VersionInfo } from "./types.js";

/**
 * A started engine and a client for it, as one object.
 *
 * Disposable: `await using p = await portolan()` stops the process when
 * the scope ends, which is the failure this most wants to prevent — an
 * orphaned engine per rebuild, each holding a port.
 */
export interface Portolan extends AsyncDisposable {
  readonly server: PortolanServer;
  readonly client: PortolanClient;
  readonly version: VersionInfo;
  chart(req: ChartRequest, opts?: ChartOptions): Promise<Job>;
  stop(): Promise<void>;
}

/** Start an engine and return a client bound to it. */
export async function portolan(opts: StartOptions = {}): Promise<Portolan> {
  const server = await PortolanServer.start(opts);
  const client = PortolanClient.from(server);
  return {
    server,
    client,
    version: server.version,
    chart: (req, o) => client.chart(req, o),
    stop: () => server.stop(),
    async [Symbol.asyncDispose]() {
      await server.stop();
    },
  };
}
