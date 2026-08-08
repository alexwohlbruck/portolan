// The drawn ground-truth model: bezier anchors the human edits, plus the
// baked polyline the scorer reads.
//
// Two rules the file format imposes and this module must never break:
//
//  1. `coords` IS the contract. internal/sketch only reads `coords` and
//     `routes`; `anchors` and `id` are editor state. Every mutation bakes.
//  2. An anchor's handle state is three-valued, not two: null means
//     "auto-smooth, derive a Catmull-Rom tangent at render time", handles
//     equal to the anchor point mean "hard corner", anything else is
//     user-placed. Collapsing null into an explicit tangent would freeze
//     every curve the first time a neighbour moved.
//
// Geometry is kept in raw lon/lat. At city zooms the distortion is far
// below a drawn line's width, and the alternative — projecting for every
// bezier sample — would change the saved coordinates of every existing
// sketch.

export type LL = [number, number] // [lon, lat]

export interface Anchor {
  p: LL
  hin: LL | null
  hout: LL | null
}

export interface Route {
  label: string
  color: string // hex, no '#'
}

export interface SketchLine {
  id: string
  routes: Route[]
  anchors: Anchor[]
  coords: LL[]
}

export interface Network {
  feed: string
  updated?: string
  lines: SketchLine[]
}

export const uid = () => 'l' + Math.random().toString(36).slice(2, 9)

/** Samples per anchor span when baking. Matches the original editor, so a
 *  sketch re-saved here is coordinate-compatible with one saved before. */
const SAMPLES = 16

/** autoHandles: the Catmull-Rom tangent used when a handle is null. */
export function autoHandles(anchors: Anchor[], i: number): { hin: LL; hout: LL } {
  const n = anchors.length
  const p = anchors[i].p
  const prev = anchors[Math.max(0, i - 1)].p
  const next = anchors[Math.min(n - 1, i + 1)].p
  const tx = (next[0] - prev[0]) / 6
  const ty = (next[1] - prev[1]) / 6
  return { hout: [p[0] + tx, p[1] + ty], hin: [p[0] - tx, p[1] - ty] }
}

export function handlesOf(l: SketchLine, i: number): { hin: LL; hout: LL } {
  const a = l.anchors[i]
  const auto = autoHandles(l.anchors, i)
  return { hin: a.hin ?? auto.hin, hout: a.hout ?? auto.hout }
}

/** isCorner: handles pinned to the anchor itself — drawn with no handle
 *  grips, and the reason a pen click produces a hard vertex. */
export function isCorner(a: Anchor): boolean {
  return !!a.hin && !!a.hout && same(a.hin, a.p) && same(a.hout, a.p)
}

const same = (a: LL, b: LL) => Math.abs(a[0] - b[0]) < 1e-9 && Math.abs(a[1] - b[1]) < 1e-9

export function bake(l: SketchLine): SketchLine {
  const A = l.anchors
  if (A.length < 2) {
    l.coords = A.map((a) => [...a.p] as LL)
    return l
  }
  const out: LL[] = [[...A[0].p] as LL]
  for (let i = 0; i < A.length - 1; i++) {
    const p0 = A[i].p
    const p3 = A[i + 1].p
    const p1 = handlesOf(l, i).hout
    const p2 = handlesOf(l, i + 1).hin
    for (let s = 1; s <= SAMPLES; s++) {
      const t = s / SAMPLES
      const u = 1 - t
      out.push([
        u * u * u * p0[0] + 3 * u * u * t * p1[0] + 3 * u * t * t * p2[0] + t * t * t * p3[0],
        u * u * u * p0[1] + 3 * u * u * t * p1[1] + 3 * u * t * t * p2[1] + t * t * t * p3[1],
      ])
    }
  }
  l.coords = out
  return l
}

export const bakeAll = (net: Network) => {
  net.lines.forEach(bake)
  return net
}

export const displayColor = (l: SketchLine) => '#' + (l.routes[0]?.color || '888888')

export const lineLabel = (l: SketchLine) =>
  l.routes.map((r) => r.label || r.color).join(' · ')

/** reverse: anchors flip AND each anchor's handles swap. The original
 *  editor's merge forgot the swap, which kinked every reversed curve. */
export function reverseLine(l: SketchLine): SketchLine {
  l.anchors = l.anchors
    .slice()
    .reverse()
    .map((a) => ({ p: a.p, hin: a.hout, hout: a.hin }))
  return bake(l)
}

function reversedAnchors(anchors: Anchor[]): Anchor[] {
  return anchors
    .slice()
    .reverse()
    .map((a) => ({ p: a.p, hin: a.hout, hout: a.hin }))
}

/** splitAt: the split anchor is duplicated into both halves, so the two
 *  lines still meet exactly. Only interior anchors can split. */
export function splitAt(net: Network, l: SketchLine, i: number): SketchLine | null {
  if (i < 1 || i > l.anchors.length - 2) return null
  const b: SketchLine = {
    id: uid(),
    routes: l.routes.map((r) => ({ ...r })),
    anchors: l.anchors.slice(i).map((a) => ({ ...a })),
    coords: [],
  }
  l.anchors = l.anchors.slice(0, i + 1)
  bake(l)
  bake(b)
  net.lines.push(b)
  return b
}

const d2 = (p: LL, q: LL) => (p[0] - q[0]) ** 2 + (p[1] - q[1]) ** 2

/** mergeInto: joins b into a at whichever of the four end pairings is
 *  closest, reversing either side as needed (handles swapped with it —
 *  see reverseLine) and de-duplicating the routes. */
export function mergeInto(net: Network, a: SketchLine, b: SketchLine): SketchLine {
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

  const seen = new Set<string>()
  const routes: Route[] = []
  for (const r of a.routes.concat(b.routes)) {
    const k = r.label + '|' + r.color
    if (!seen.has(k)) {
      seen.add(k)
      routes.push(r)
    }
  }
  a.routes = routes
  net.lines = net.lines.filter((x) => x.id !== b.id)
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
 *  picked by midpoint and mis-assigned clicks on long or curved spans. */
export function insertAnchor(l: SketchLine, p: LL): number {
  let bi = 0
  let bd = Infinity
  for (let i = 0; i < l.anchors.length - 1; i++) {
    const d = segDist(p, l.anchors[i].p, l.anchors[i + 1].p)
    if (d < bd) {
      bd = d
      bi = i
    }
  }
  l.anchors.splice(bi + 1, 0, { p, hin: null, hout: null })
  bake(l)
  return bi + 1
}

/** deleteAnchor: returns false when the line died with it (a line needs
 *  two anchors to be a line). */
export function deleteAnchor(net: Network, l: SketchLine, i: number): boolean {
  l.anchors.splice(i, 1)
  if (l.anchors.length < 2) {
    net.lines = net.lines.filter((x) => x.id !== l.id)
    return false
  }
  bake(l)
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
export function trimDuplicateEnds(l: SketchLine) {
  const near = (a: Anchor, b: Anchor) =>
    Math.abs(a.p[0] - b.p[0]) < 1e-7 && Math.abs(a.p[1] - b.p[1]) < 1e-7
  while (l.anchors.length > 1 && near(l.anchors[l.anchors.length - 1], l.anchors[l.anchors.length - 2]))
    l.anchors.pop()
  while (l.anchors.length > 1 && near(l.anchors[0], l.anchors[1])) l.anchors.shift()
}

/** History: whole-document JSON snapshots. Cheap at this size (the NYC
 *  sketch is 16 lines) and immune to the aliasing bugs a diff-based stack
 *  would invite when anchors are shared between operations. */
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
