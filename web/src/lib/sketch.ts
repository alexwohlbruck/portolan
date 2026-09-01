// The drawn ground-truth model: bezier anchors the human edits, plus the
// baked polyline every consumer reads.
//
// Four rules the file format imposes and this module must never break:
//
//  1. `coords` IS the contract. internal/sketch reads coords, never
//     anchors; anchors and ids are editor state. Every mutation bakes.
//  2. An anchor's handle state is three-valued, not two: null means
//     "auto-smooth, derive a Catmull-Rom tangent at bake time", handles
//     equal to the anchor point mean "hard corner", anything else is
//     user-placed. Collapsing null into an explicit tangent would freeze
//     every curve the first time a neighbour moved.
//  3. A CLOSED curve (a yard boundary) bakes its wrap-around span and
//     repeats its first vertex last, so a consumer can read coords as a
//     ring with no re-closing.
//  4. There is no colour. The drawing states where track goes, and that
//     is all it is graded on.
//
// Geometry is kept in raw lon/lat. At yard and city zooms the distortion
// is far below a drawn line's width, and the alternative — projecting for
// every bezier sample — would change the saved coordinates of every
// existing sketch.

export type LL = [number, number] // [lon, lat]

export interface Anchor {
  p: LL
  hin: LL | null
  hout: LL | null
}

export interface Curve {
  id: string
  label: string
  closed?: boolean
  anchors: Anchor[]
  coords: LL[]
}

export interface Yard {
  id: string
  label: string
  boundary: Curve
  centerlines: Curve[]
}

export interface Network {
  feed: string
  updated?: string
  lines: Curve[]
  yards: Yard[]
}

export const uid = (p = 'l') => p + Math.random().toString(36).slice(2, 9)

export const newCurve = (id = uid(), closed = false): Curve => ({
  id,
  label: '',
  ...(closed ? { closed: true } : {}),
  anchors: [],
  coords: [],
})

/** Samples per anchor span when baking. Matches the original editor, so a
 *  sketch re-saved here is coordinate-compatible with one saved before. */
const SAMPLES = 16

/** autoHandles: the Catmull-Rom tangent used when a handle is null. On a
 *  closed curve the neighbours WRAP — clamping at the ends instead would
 *  put a corner at the ring's arbitrary first anchor. */
export function autoHandles(anchors: Anchor[], i: number, closed = false): { hin: LL; hout: LL } {
  const n = anchors.length
  const p = anchors[i].p
  const prev = anchors[closed ? (i - 1 + n) % n : Math.max(0, i - 1)].p
  const next = anchors[closed ? (i + 1) % n : Math.min(n - 1, i + 1)].p
  const tx = (next[0] - prev[0]) / 6
  const ty = (next[1] - prev[1]) / 6
  return { hout: [p[0] + tx, p[1] + ty], hin: [p[0] - tx, p[1] - ty] }
}

export function handlesOf(c: Curve, i: number): { hin: LL; hout: LL } {
  const a = c.anchors[i]
  const auto = autoHandles(c.anchors, i, !!c.closed)
  return { hin: a.hin ?? auto.hin, hout: a.hout ?? auto.hout }
}

/** isCorner: handles pinned to the anchor itself — drawn with no handle
 *  grips, and the reason a pen click produces a hard vertex. */
export function isCorner(a: Anchor): boolean {
  return !!a.hin && !!a.hout && same(a.hin, a.p) && same(a.hout, a.p)
}

const same = (a: LL, b: LL) => Math.abs(a[0] - b[0]) < 1e-9 && Math.abs(a[1] - b[1]) < 1e-9

export function bake(c: Curve): Curve {
  const A = c.anchors
  const closed = !!c.closed && A.length > 2
  if (A.length < 2) {
    c.coords = A.map((a) => [...a.p] as LL)
    return c
  }
  const out: LL[] = [[...A[0].p] as LL]
  const spans = closed ? A.length : A.length - 1
  for (let i = 0; i < spans; i++) {
    const j = (i + 1) % A.length
    const p0 = A[i].p
    const p3 = A[j].p
    const p1 = handlesOf(c, i).hout
    const p2 = handlesOf(c, j).hin
    for (let s = 1; s <= SAMPLES; s++) {
      const t = s / SAMPLES
      const u = 1 - t
      out.push([
        u * u * u * p0[0] + 3 * u * u * t * p1[0] + 3 * u * t * t * p2[0] + t * t * t * p3[0],
        u * u * u * p0[1] + 3 * u * u * t * p1[1] + 3 * u * t * t * p2[1] + t * t * t * p3[1],
      ])
    }
  }
  c.coords = out
  return c
}

export const bakeAll = (net: Network) => {
  net.lines.forEach(bake)
  for (const y of net.yards) {
    y.boundary.closed = true
    bake(y.boundary)
    y.centerlines.forEach(bake)
  }
  return net
}

export const curveLabel = (c: Curve) => c.label || c.id
export const yardLabel = (y: Yard) => y.label || y.id

/** reverse: anchors flip AND each anchor's handles swap. The original
 *  editor's merge forgot the swap, which kinked every reversed curve. */
export function reverseLine(c: Curve): Curve {
  c.anchors = reversedAnchors(c.anchors)
  return bake(c)
}

function reversedAnchors(anchors: Anchor[]): Anchor[] {
  return anchors
    .slice()
    .reverse()
    .map((a) => ({ p: a.p, hin: a.hout, hout: a.hin }))
}

/** splitAt: the split anchor is duplicated into both halves, so the two
 *  curves still meet exactly. Only interior anchors can split, and a ring
 *  cannot split at all — it would stop being a boundary. */
export function splitAt(list: Curve[], c: Curve, i: number): Curve | null {
  if (c.closed) return null
  if (i < 1 || i > c.anchors.length - 2) return null
  const b: Curve = {
    id: uid(),
    label: c.label,
    anchors: c.anchors.slice(i).map((a) => ({ ...a })),
    coords: [],
  }
  c.anchors = c.anchors.slice(0, i + 1)
  bake(c)
  bake(b)
  list.push(b)
  return b
}

const d2 = (p: LL, q: LL) => (p[0] - q[0]) ** 2 + (p[1] - q[1]) ** 2

/** mergeInto: joins b into a at whichever of the four end pairings is
 *  closest, reversing either side as needed (handles swapped with it —
 *  see reverseLine). */
export function mergeInto(list: Curve[], a: Curve, b: Curve): Curve {
  const aH = a.anchors[0].p
  const aT = a.anchors[a.anchors.length - 1].p
  const bH = b.anchors[0].p
  const bT = b.anchors[b.anchors.length - 1].p
  const opts: [number, number][] = [
    [d2(aT, bH), 0], // a then b
    [d2(aT, bT), 1], // a then reversed b
    [d2(aH, bH), 2], // reversed a then b
    [d2(aH, bT), 3], // reversed a then reversed b
  ]
  opts.sort((x, y) => x[0] - y[0])
  const kind = opts[0][1]
  let A = a.anchors
  let B = b.anchors
  if (kind === 1) B = reversedAnchors(B)
  if (kind === 2) A = reversedAnchors(A)
  if (kind === 3) {
    A = reversedAnchors(A)
    B = reversedAnchors(B)
  }
  // the joined ends are coincident by construction of the pairing; drop
  // the duplicate so the seam has one anchor, not two on top of each other
  if (A.length && B.length && d2(A[A.length - 1].p, B[0].p) < 1e-14) B = B.slice(1)
  a.anchors = A.concat(B)
  a.label = a.label || b.label
  const i = list.indexOf(b)
  if (i >= 0) list.splice(i, 1)
  return bake(a)
}

/** distance from p to segment ab, in lon/lat units — good enough to rank
 *  candidate spans, which is all it is used for. */
function segDist(p: LL, a: LL, b: LL): number {
  const vx = b[0] - a[0]
  const vy = b[1] - a[1]
  const len = vx * vx + vy * vy
  let t = len ? ((p[0] - a[0]) * vx + (p[1] - a[1]) * vy) / len : 0
  t = Math.max(0, Math.min(1, t))
  return Math.hypot(p[0] - (a[0] + t * vx), p[1] - (a[1] + t * vy))
}

/** insertAnchor: adds a smooth anchor on the span nearest the click.
 *  Nearest by PERPENDICULAR distance, not by midpoint — the original
 *  picked by midpoint and mis-assigned clicks on long or curved spans.
 *  A ring's closing span counts, or its last edge could never take one. */
export function insertAnchor(c: Curve, p: LL): number {
  let bi = 0
  let bd = Infinity
  const spans = c.closed ? c.anchors.length : c.anchors.length - 1
  for (let i = 0; i < spans; i++) {
    const j = (i + 1) % c.anchors.length
    const d = segDist(p, c.anchors[i].p, c.anchors[j].p)
    if (d < bd) {
      bd = d
      bi = i
    }
  }
  c.anchors.splice(bi + 1, 0, { p, hin: null, hout: null })
  bake(c)
  return bi + 1
}

/** deleteAnchor: returns false when the curve died with it. A line needs
 *  two anchors; a ring needs three. */
export function deleteAnchor(list: Curve[], c: Curve, i: number): boolean {
  c.anchors.splice(i, 1)
  if (c.anchors.length < (c.closed ? 3 : 2)) {
    const k = list.indexOf(c)
    if (k >= 0) list.splice(k, 1)
    return false
  }
  bake(c)
  return true
}

/** toggleCorner: null handles (auto-smooth) ⇄ handles pinned to the point
 *  (hard corner). Two double-clicks return to auto-smooth. */
export function toggleCorner(a: Anchor) {
  if (a.hin || a.hout) {
    a.hin = null
    a.hout = null
  } else {
    a.hin = [...a.p] as LL
    a.hout = [...a.p] as LL
  }
}

/** trimDuplicateEnds: the double-click that finishes the pen leaves a
 *  coincident anchor behind. */
export function trimDuplicateEnds(c: Curve) {
  const near = (a: Anchor, b: Anchor) =>
    Math.abs(a.p[0] - b.p[0]) < 1e-7 && Math.abs(a.p[1] - b.p[1]) < 1e-7
  while (c.anchors.length > 1 && near(c.anchors[c.anchors.length - 1], c.anchors[c.anchors.length - 2]))
    c.anchors.pop()
  while (c.anchors.length > 1 && near(c.anchors[0], c.anchors[1])) c.anchors.shift()
}

// ── metric helpers ────────────────────────────────────────────────────
// A local projection mirroring internal/geo.Frame exactly, so a distance
// measured here is the distance the scorer measures.

const M_PER_DEG = 111320
export interface Frame {
  lon0: number
  lat0: number
  cos: number
}
export const frameAt = (p: LL): Frame => ({
  lon0: p[0],
  lat0: p[1],
  cos: Math.cos((p[1] * Math.PI) / 180),
})
export const toXY = (f: Frame, p: LL): [number, number] => [
  (p[0] - f.lon0) * M_PER_DEG * f.cos,
  (p[1] - f.lat0) * M_PER_DEG,
]
export const toLL = (f: Frame, p: [number, number]): LL => [
  f.lon0 + p[0] / (M_PER_DEG * f.cos),
  f.lat0 + p[1] / M_PER_DEG,
]

/** the ring form of a closed curve: closing vertex dropped. */
export function ring(c: Curve): LL[] {
  const n = c.coords.length
  if (n > 1 && same(c.coords[0], c.coords[n - 1])) return c.coords.slice(0, n - 1)
  return c.coords
}

/** even-odd ray casting, in whatever units the ring is given in. */
export function pointInRing(r: [number, number][] | LL[], p: [number, number] | LL): boolean {
  let inside = false
  for (let i = 0, j = r.length - 1; i < r.length; j = i, i++) {
    const a = r[j]
    const b = r[i]
    if (a[1] > p[1] !== b[1] > p[1]) {
      if (a[0] + ((p[1] - a[1]) * (b[0] - a[0])) / (b[1] - a[1]) > p[0]) inside = !inside
    }
  }
  return inside
}

function segIntersect(
  a1: [number, number], a2: [number, number],
  b1: [number, number], b2: [number, number],
): [number, number] | null {
  const rx = a2[0] - a1[0]
  const ry = a2[1] - a1[1]
  const sx = b2[0] - b1[0]
  const sy = b2[1] - b1[1]
  const den = rx * sy - ry * sx
  if (Math.abs(den) < 1e-15) return null
  const qx = b1[0] - a1[0]
  const qy = b1[1] - a1[1]
  const t = (qx * sy - qy * sx) / den
  const u = (qx * ry - qy * rx) / den
  if (t < -1e-12 || t > 1 + 1e-12 || u < -1e-12 || u > 1 + 1e-12) return null
  return [a1[0] + rx * t, a1[1] + ry * t]
}

export interface Entrance {
  at: LL
  heading: number // degrees CCW from east, pointing INTO the yard
  lines: string[]
}

/** ENTRANCE_CLUSTER_M / STEP_OFF_M mirror internal/sketch/yard.go. */
const ENTRANCE_CLUSTER_M = 30
const STEP_OFF_M = 0.5

/** entrancesOf: every crossing of a centerline with the boundary,
 *  single-link clustered so one throat is one entrance, heading turned
 *  inward.
 *
 *  This mirrors (*Yard).Entrances in internal/sketch/yard.go, which is
 *  what the scorer runs. If one changes, change both — an entrance the
 *  editor draws but the scorer cannot see is worse than none. */
export function entrancesOf(y: Yard): Entrance[] {
  const r = ring(y.boundary)
  if (r.length < 3) return []
  const f = frameAt(r[0])
  const mr = r.map((p) => toXY(f, p))
  type X = { p: [number, number]; d: [number, number]; line: string }
  const cs: X[] = []
  for (const c of y.centerlines) {
    const cp = c.coords.map((p) => toXY(f, p))
    for (let i = 0; i + 1 < cp.length; i++) {
      const a = cp[i]
      const b = cp[i + 1]
      for (let j = 0; j < mr.length; j++) {
        const hit = segIntersect(a, b, mr[j], mr[(j + 1) % mr.length])
        if (!hit) continue
        const len = Math.hypot(b[0] - a[0], b[1] - a[1]) || 1
        let d: [number, number] = [(b[0] - a[0]) / len, (b[1] - a[1]) / len]
        const off: [number, number] = [hit[0] + d[0] * STEP_OFF_M, hit[1] + d[1] * STEP_OFF_M]
        if (!pointInRing(mr, off)) d = [-d[0], -d[1]]
        cs.push({ p: hit, d, line: c.id })
      }
    }
  }
  if (!cs.length) return []
  cs.sort((a, b) => a.p[0] - b.p[0] || a.p[1] - b.p[1])
  const parent = cs.map((_, i) => i)
  const find = (i: number): number => {
    while (parent[i] !== i) i = parent[i] = parent[parent[i]]
    return i
  }
  for (let i = 0; i < cs.length; i++)
    for (let j = i + 1; j < cs.length; j++)
      if (Math.hypot(cs[i].p[0] - cs[j].p[0], cs[i].p[1] - cs[j].p[1]) <= ENTRANCE_CLUSTER_M)
        parent[find(i)] = find(j)

  const groups = new Map<number, X[]>()
  for (let i = 0; i < cs.length; i++) {
    const k = find(i)
    if (!groups.has(k)) groups.set(k, [])
    groups.get(k)!.push(cs[i])
  }
  const out: Entrance[] = []
  for (const ms of groups.values()) {
    let sx = 0, sy = 0, dx = 0, dy = 0
    const ids = new Set<string>()
    for (const m of ms) {
      sx += m.p[0]
      sy += m.p[1]
      dx += m.d[0]
      dy += m.d[1]
      ids.add(m.line)
    }
    out.push({
      at: toLL(f, [sx / ms.length, sy / ms.length]),
      heading: Math.round((Math.atan2(dy, dx) * 180) / Math.PI * 10) / 10,
      lines: [...ids].sort(),
    })
  }
  return out
}

/** yardAt: which drawn yard contains this point (innermost wins, so a
 *  yard drawn inside another still takes its own centerlines). */
export function yardAt(net: Network, p: LL): Yard | null {
  let best: Yard | null = null
  let bestArea = Infinity
  for (const y of net.yards) {
    const r = ring(y.boundary)
    if (r.length < 3 || !pointInRing(r, p)) continue
    let a = 0
    for (let i = 0; i < r.length; i++) {
      const q = r[(i + 1) % r.length]
      a += r[i][0] * q[1] - q[0] * r[i][1]
    }
    a = Math.abs(a / 2)
    if (a < bestArea) {
      bestArea = a
      best = y
    }
  }
  return best
}

/** History: whole-document JSON snapshots. Cheap at this size and immune
 *  to the aliasing bugs a diff-based stack would invite when anchors are
 *  shared between operations. */
export class History {
  private undoStack: string[] = []
  private redoStack: string[] = []
  constructor(private depth = 120) {}

  push(net: Network) {
    this.undoStack.push(JSON.stringify(net))
    if (this.undoStack.length > this.depth) this.undoStack.shift()
    this.redoStack = []
  }
  /** drop the most recent snapshot — for a gesture that turned out to be
   *  a click, so nothing actually changed. */
  discard() {
    this.undoStack.pop()
  }
  undo(net: Network): Network | null {
    if (!this.undoStack.length) return null
    this.redoStack.push(JSON.stringify(net))
    return JSON.parse(this.undoStack.pop()!)
  }
  redo(net: Network): Network | null {
    if (!this.redoStack.length) return null
    this.undoStack.push(JSON.stringify(net))
    return JSON.parse(this.redoStack.pop()!)
  }
  reset() {
    this.undoStack = []
    this.redoStack = []
  }
  get canUndo() {
    return this.undoStack.length > 0
  }
  get canRedo() {
    return this.redoStack.length > 0
  }
}
