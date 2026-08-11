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

/**
 * A curation document: colours, names, bullet shapes and fonts.
 *
 * SUBJECT-KEYED — name a route or agency once and hang everything known
 * about it off that name. The flat `route:<id>` tables that appear in
 * the engine's own source are its INTERNAL form and are rejected here,
 * loudly, because a curation document that parses and applies nothing is
 * the worst failure this format can have.
 */
export interface StyleDoc {
  routes?: Record<string, StyleEntity>;
  agencies?: Record<string, StyleEntity>;
  stops?: Record<string, StyleEntity>;
  modes?: Record<string, StyleClass>;
  options?: {
    bullet_order?: "color" | "feed" | "natural";
    caterpillars?: boolean;
    osm_stop_names?: boolean;
  };
}

export interface StyleEntity {
  /** Display name — the bullet text and the trunk label. */
  name?: string;
  /** Drawn colour, hex without `#`. */
  color?: string;
  /** Bullet outline. */
  shape?: "circle" | "square" | "rounded" | "notch" | "diamond" | "hexagon" | "octagon" | "triangle";
  font?: "default" | "mono" | "bolder" | "lighter" | "italic";
  /** A contrasting ring — what a white or near-white bullet needs to have an edge. */
  bordered?: boolean;
  /**
   * Merge policy for this subject alone, in practice `"route"` to keep
   * it out of a colour trunk. Trunking is colour-based, which is right
   * where colour carries meaning and wrong where it is assigned freely.
   */
  trunk?: "color" | "agency" | "route" | "none";
  /** Agencies only: these route colours are line identities, not branch decoration. */
  line_colors?: boolean;
}

export interface StyleClass {
  color?: string;
  width?: number;
  opacity?: number;
  band_floor?: number;
  trunk?: "color" | "agency" | "route" | "none";
  hidden?: boolean;
}

/** A place. Prefer this over the tuple — `[lat, lon]` is easy to swap. */
export interface LatLon {
  lat: number;
  lon: number;
}

/** A window. Prefer this over the tuple — the tuple is longitude-first. */
export interface BBox {
  w: number;
  s: number;
  e: number;
  n: number;
}

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
  /**
   * Clip window. The object form is unambiguous; the tuple is
   * `[w, s, e, n]` — LONGITUDE first, GeoJSON's order — and sits next to
   * an `anchor` that is latitude-first, so prefer the object.
   */
  bbox?: BBox | [number, number, number, number];
  /**
   * Pin the projection origin. Object form preferred; the tuple is
   * `[lat, lon]`, LATITUDE first, which is what a map UI hands you when
   * you copy a location — and the opposite order to `bbox`.
   *
   * PIN IT TO SOMETHING THAT DOES NOT MOVE. Every measurement happens in
   * a local metric frame, so moving the origin re-rounds every float in
   * the build. Deriving it from the current extent of what has been
   * drawn means it shifts the moment the network grows past that extent,
   * which costs exactly the locality guarantee the engine otherwise
   * gives you. A city centre is right; the bounding-box centre of the
   * user's current stations is not.
   */
  anchor?: LatLon | [number, number];

  city?: string;
  styleDir?: string;
  /**
   * The curation document itself, instead of a directory.
   *
   * Symmetrical with `gtfsInline` and `corridorsInline`, and for the
   * same reason: colours and bullet shapes are live state for an editor,
   * so a path forces a file write per rebuild — and somewhere writable
   * to put it. Replaces `styleDir`/`city` when set; the class defaults
   * are compiled into the engine, so a document alone is complete.
   */
  styleInline?: StyleDoc;
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
