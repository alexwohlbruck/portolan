import type { PlnbFeature } from "./types.js";

/**
 * PLNB decoder.
 *
 * This file exists because the layout is easy to misread and misreading
 * it fails SILENTLY. The property blocks are grouped by WIDTH and
 * interleaved within a block — `f32s` is
 * `[offsetPx₀, offFromPx₀, offToPx₀, offsetPx₁, …]`, stride 3 — not one
 * contiguous array per property. A decoder that assumes the latter reads
 * plausible-but-wrong values (nslots picking up bandMin, routes picking
 * up label) because every lane is a valid value of the right width.
 * That has already cost one consumer a debugging cycle against a comment
 * that described the opposite layout.
 *
 * So: decode once, here, and check the format version.
 *
 * The authority is the package comment at the top of
 * `internal/pipeline/binary.go`; the address formulas below are copied
 * from it verbatim.
 */

const MAGIC = 0x424e4c50; // "PLNB", little-endian
/** The layout version this decoder understands. */
export const SUPPORTED_PLNB = 1;
const HEADER = 32;
const COORD_SCALE = 1e7;

const KINDS = ["steady", "transition", "bridge"] as const;

const align4 = (n: number) => (n + 3) & ~3;

export class PlnbError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "PlnbError";
  }
}

/**
 * A decoded build. The typed arrays are VIEWS over the source buffer —
 * no copying — so `positions` can go straight into a GPU buffer and the
 * property columns can be read by index without allocating per feature.
 *
 * Use `feature(i)` only when you want a plain object; the whole point of
 * the format is that a renderer does not need one.
 */
export class Plnb {
  /** Zoom band this file holds, or `null` for the union of all bands. */
  readonly band: number | null;
  readonly featureCount: number;
  readonly positionCount: number;

  /**
   * `[lon, lat]` pairs as signed 1e-7 degrees, `positionCount * 2` long.
   * Integers, not floats: a float32 holds about seven significant digits
   * where a longitude needs nine, which would quantise vertices to
   * roughly two metres and visibly kink a ribbon.
   */
  readonly positions: Int32Array;
  /** `featureCount + 1` offsets into `positions` (in vertices, not bytes). */
  readonly starts: Uint32Array;

  readonly kinds: Uint8Array; // stride 1
  readonly colors: Uint32Array; // stride 1, 0x00RRGGBB
  readonly f32s: Float32Array; // stride 3: offsetPx, offFromPx, offToPx
  readonly i16s: Int16Array; // stride 5: slot, nslots, bandMin, bandMax, routeType
  readonly u32s: Uint32Array; // stride 4: label, mode, routes, acts
  readonly strings: string[];

  constructor(buf: ArrayBuffer) {
    const dv = new DataView(buf);
    if (buf.byteLength < HEADER) {
      throw new PlnbError(`buffer is ${buf.byteLength} bytes, too short for a header`);
    }
    if (dv.getUint32(0, true) !== MAGIC) {
      throw new PlnbError("not a PLNB buffer (bad magic)");
    }
    const version = dv.getUint16(4, true);
    if (version !== SUPPORTED_PLNB) {
      throw new PlnbError(
        `PLNB layout version ${version}, this decoder understands ${SUPPORTED_PLNB}. ` +
          `Upgrade @alexwohlbruck/portolan to match the engine.`,
      );
    }
    const nFeat = dv.getUint32(8, true);
    const nPos = dv.getUint32(12, true);
    const band = dv.getInt32(16, true);
    const strTabOff = dv.getUint32(20, true);

    this.featureCount = nFeat;
    this.positionCount = nPos;
    this.band = band < 0 ? null : band;

    // block addresses, exactly as binary.go states them
    const posOff = HEADER;
    const startsOff = posOff + nPos * 8;
    const kindsOff = startsOff + (nFeat + 1) * 4;
    const colorsOff = kindsOff + align4(nFeat);
    const f32Off = colorsOff + nFeat * 4;
    const i16Off = f32Off + nFeat * 12;
    const u32Off = i16Off + align4(nFeat * 10);

    if (strTabOff !== u32Off + nFeat * 16 || strTabOff > buf.byteLength) {
      throw new PlnbError(
        `string table at ${strTabOff} but the block formulas give ${u32Off + nFeat * 16} ` +
          `— the file is truncated or the layout has changed`,
      );
    }

    // Views, not copies. Every block is 4-byte aligned by construction
    // (binary.go pads kinds and i16s), so these never need a slice.
    this.positions = new Int32Array(buf, posOff, nPos * 2);
    this.starts = new Uint32Array(buf, startsOff, nFeat + 1);
    this.kinds = new Uint8Array(buf, kindsOff, nFeat);
    this.colors = new Uint32Array(buf, colorsOff, nFeat);
    this.f32s = new Float32Array(buf, f32Off, nFeat * 3);
    this.i16s = new Int16Array(buf, i16Off, nFeat * 5);
    this.u32s = new Uint32Array(buf, u32Off, nFeat * 4);

    // strings: u32 count, then u32 length + utf-8 bytes each
    const count = dv.getUint32(strTabOff, true);
    const dec = new TextDecoder();
    const strs = new Array<string>(count);
    let at = strTabOff + 4;
    for (let i = 0; i < count; i++) {
      const len = dv.getUint32(at, true);
      strs[i] = len === 0 ? "" : dec.decode(new Uint8Array(buf, at + 4, len));
      at += 4 + len;
    }
    this.strings = strs;
  }

  /**
   * Every position in DEGREES as one flat interleaved `Float64Array`
   * — `[lon₀, lat₀, lon₁, lat₁, …]`, `positionCount * 2` long.
   *
   * This is what a GPU consumer actually wants. `positions` is integer
   * 1e-7 degrees, which is right on the wire and wrong in a vertex
   * buffer: nothing off the shelf divides by 1e7, and projection happens
   * in the shader from lng/lat.
   *
   * f64 rather than f32 is not a preference. A float32 holds about seven
   * significant digits where a longitude needs nine, so an f32 buffer
   * quantises vertices to roughly two metres and visibly kinks a ribbon
   * — the same reason the wire format is i32 and not f32. deck.gl's
   * binary attribute path takes f64 and splits it into hi/lo f32 pairs
   * for exactly this.
   *
   * One pass, cached, so calling it per frame is free after the first.
   * Pair it with `starts` as deck.gl's `startIndices`.
   */
  degrees(): Float64Array {
    if (!this.#degrees) {
      const out = new Float64Array(this.positionCount * 2);
      for (let i = 0; i < out.length; i++) out[i] = this.positions[i] / COORD_SCALE;
      this.#degrees = out;
    }
    return this.#degrees;
  }
  #degrees: Float64Array | null = null;

  /** Vertex range of feature `i`: `[startVertex, endVertex)`. */
  vertexRange(i: number): [number, number] {
    return [this.starts[i], this.starts[i + 1]];
  }

  kind(i: number): PlnbFeature["kind"] {
    return KINDS[this.kinds[i]] ?? "unknown";
  }

  /** 0xRRGGBB. */
  color(i: number): number {
    return this.colors[i];
  }

  /** `#rrggbb`, for handing to a canvas or a style expression. */
  colorHex(i: number): string {
    return "#" + this.colors[i].toString(16).padStart(6, "0");
  }

  offsetPx(i: number): number {
    return this.f32s[i * 3];
  }
  offFromPx(i: number): number {
    return this.f32s[i * 3 + 1];
  }
  offToPx(i: number): number {
    return this.f32s[i * 3 + 2];
  }

  slot(i: number): number {
    return this.i16s[i * 5];
  }
  nslots(i: number): number {
    return this.i16s[i * 5 + 1];
  }
  bandMin(i: number): number {
    return this.i16s[i * 5 + 2];
  }
  /** EXCLUSIVE: the range is `[bandMin, bandMax)`, and each band's max is the next band's min. */
  bandMax(i: number): number {
    return this.i16s[i * 5 + 3];
  }
  routeType(i: number): number {
    return this.i16s[i * 5 + 4];
  }

  label(i: number): string {
    return this.strings[this.u32s[i * 4]];
  }
  mode(i: number): string {
    return this.strings[this.u32s[i * 4 + 1]];
  }
  /** Route ids, verbatim. Split from the CSV the emitter writes. */
  routes(i: number): string[] {
    const s = this.strings[this.u32s[i * 4 + 2]];
    return s === "" ? [] : s.split(",");
  }
  /** Weekly hex masks aligned with `routes`; empty when the feed had no calendar. */
  acts(i: number): string[] {
    const s = this.strings[this.u32s[i * 4 + 3]];
    return s === "" ? [] : s.split(";");
  }

  /** `[lon, lat]` in degrees. Allocates — prefer `positions` in a hot path. */
  coordinates(i: number): Array<[number, number]> {
    const [a, b] = this.vertexRange(i);
    const out = new Array<[number, number]>(b - a);
    for (let k = a; k < b; k++) {
      out[k - a] = [this.positions[k * 2] / COORD_SCALE, this.positions[k * 2 + 1] / COORD_SCALE];
    }
    return out;
  }

  /** A plain object for one feature. Convenience, not the fast path. */
  feature(i: number): PlnbFeature {
    return {
      index: i,
      kind: this.kind(i),
      color: this.color(i),
      offsetPx: this.offsetPx(i),
      offFromPx: this.offFromPx(i),
      offToPx: this.offToPx(i),
      slot: this.slot(i),
      nslots: this.nslots(i),
      bandMin: this.bandMin(i),
      bandMax: this.bandMax(i),
      routeType: this.routeType(i),
      label: this.label(i),
      mode: this.mode(i),
      routes: this.routes(i),
      acts: this.acts(i),
      coordinates: this.coordinates(i),
    };
  }

  *features(): Generator<PlnbFeature> {
    for (let i = 0; i < this.featureCount; i++) yield this.feature(i);
  }

  /** GeoJSON, for comparing against the `geojson` emit or for a quick look. */
  toGeoJSON(): unknown {
    return {
      type: "FeatureCollection",
      features: Array.from(this.features(), (f) => ({
        type: "Feature",
        properties: {
          kind: f.kind,
          color: this.colorHex(f.index).slice(1).toUpperCase(),
          routes: f.routes.join(","),
          label: f.label,
          route_type: f.routeType,
          mode: f.mode,
          slot: f.slot,
          nslots: f.nslots,
          offset_px: f.offsetPx,
          off_from_px: f.offFromPx,
          off_to_px: f.offToPx,
          band_min: f.bandMin,
          band_max: f.bandMax,
        },
        geometry: { type: "LineString", coordinates: f.coordinates },
      })),
    };
  }
}

/** Decode a PLNB buffer. */
export function decodePlnb(buf: ArrayBuffer | Uint8Array): Plnb {
  if (buf instanceof Uint8Array) {
    // a Node Buffer is usually a view into a larger pooled allocation,
    // so slice to its own bytes rather than handing the pool to a view
    return new Plnb(buf.buffer.slice(buf.byteOffset, buf.byteOffset + buf.byteLength) as ArrayBuffer);
  }
  return new Plnb(buf);
}
