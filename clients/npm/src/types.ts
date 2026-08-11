/**
 * The wire types, mirroring `internal/serve`.Request and the emitters.
 *
 * These exist so a consumer never hand-writes the request shape or
 * re-derives a property name. Every field here is checked against the Go
 * struct by `test/contract.test.js`, which reads the Go source — the two
 * have drifted before and it was expensive both times.
 */

/** A GTFS feed held in memory: table name to its CSV text, header row included. */
export type GtfsTables = Record<string, string>;

/** A zoom band. `null` means every band, which is the default. */
export type Band = 15 | 14 | 13 | 0 | null;

export type Format = "geojson" | "bin";

export interface ChartRequest {
  /** GTFS zip or a directory of .txt tables. Required unless `gtfsInline`. */
  gtfs?: string;
  /** The feed itself, instead of a path — for a caller whose tables are live state. */
  gtfsInline?: GtfsTables;

  /** OSM rail extract. Portolan infers the corridors from it. */
  rail?: string;
  /** A corridor graph you already have, as a path. Alternative to `rail`. */
  corridors?: string;
  /** The corridor graph itself, as GeoJSON. Alternative to `corridors`. */
  corridorsInline?: unknown;
  /** The nodes half, when the graph arrives as two files. */
  corridorNodes?: string;

  stops?: string;
  streets?: string;
  /** `[w, s, e, n]` — LONGITUDE first, GeoJSON's order. */
  bbox?: [number, number, number, number];
  /** `[lat, lon]` — LATITUDE first, the order a map UI gives you. */
  anchor?: [number, number];

  city?: string;
  styleDir?: string;
  lineAgencies?: string[];
  scenario?: string;
  cover?: number;

  format?: Format;
  band?: Band;
}

/** What the engine says it is. Check this before drawing anything. */
export interface VersionInfo {
  /** Bare semver, no leading `v`. `"devel"` for an unstamped build. */
  version: string;
  /**
   * The PLNB binary layout version. THIS is the number to gate on — it
   * changes when a column moves, which is what a decoder depends on.
   * `version` tells you what you got; `plnb` tells you if you can read it.
   */
  plnb: number;
  formats: Format[];
  bands: number[];
  /** Whether the server requires a bearer token. */
  auth: boolean;
}

export interface ProgressEvent {
  /** Stage name: queued, load, gtfs, topology, traversal, order, fair, stations, caterpillars, emit, done. */
  stage?: string;
  /** Monotonic 0-100. */
  pct?: number;
  /** A line from the build log. */
  log?: string;
  /** Set on the single terminal frame. */
  done?: boolean;
  /** Present on the terminal frame when the build failed. */
  error?: string;
}

export type Artifact =
  | "segments"
  | "stations"
  | "style"
  | "trackcenter"
  | "nodes";

/** One decoded ribbon. Property arrays are parallel to feature index. */
export interface PlnbFeature {
  index: number;
  kind: "steady" | "transition" | "bridge" | "unknown";
  /** 0xRRGGBB. */
  color: number;
  offsetPx: number;
  offFromPx: number;
  offToPx: number;
  slot: number;
  nslots: number;
  bandMin: number;
  bandMax: number;
  routeType: number;
  label: string;
  mode: string;
  /** Route ids, split from the emitted CSV. Verbatim — never normalised. */
  routes: string[];
  /** Hex weekly masks, aligned with `routes`. Empty when there is no calendar. */
  acts: string[];
  /** `[lon, lat]` pairs in degrees. */
  coordinates: Array<[number, number]>;
}
