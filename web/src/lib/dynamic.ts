// Dynamic service rendering (docs/DYNAMIC-SERVICE.md): render ANY
// timestamp from the union layout, client-side, by hiding inactive
// ribbons and re-centering the survivors within the fixed union slot
// order.
//
// The measurement that licenses this: across 821 shared steady edges
// between the NYC union build and a real scenario re-layout, the union
// slot order was preserved on every single one — a timetable only ever
// REMOVES lines from a bundle, it never reorders the survivors. So the
// slot optimizer's answer on the union is its answer on every subset,
// and what remains is arithmetic, not optimization. No layout code runs
// in the client; this file is a filter and a re-centering rule.

export interface FeatureLike {
  type: string
  /** content hash from /api/build-delta — identical hash = identical
   *  centerline = one corridor bundle. */
  _g?: string
  properties: Record<string, any>
  geometry: { type: string; coordinates: number[][] }
}

export interface FCLike {
  type: string
  features: FeatureLike[]
}

// ── activity masks ─────────────────────────────────────────────────────
// 168 bits: 7 days × 24 hours, Monday first, hex as 7×6 chars, hour 0 =
// LSB of each day's word. Must match routeMasks in internal/atlas.

export function maskActive(mask: string, day: number, hour: number): boolean {
  const bits = parseInt(mask.slice(day * 6, day * 6 + 6), 16)
  return ((bits >>> hour) & 1) === 1
}

export const routesOf = (props: any): string[] =>
  String(props.routes ?? '')
    .split(',')
    .filter(Boolean)

/** Predicate for one instant, per FEATURE. Precedence per member route:
 *
 *  1. the segment's own `acts` mask — the hours this route rides THIS
 *     segment. This is where short-turns live: the tail beyond a
 *     short-turn terminal carries only the full-length pattern's hours,
 *     so it goes dark while the core stays lit.
 *  2. the route-level mask — the hours the route runs anywhere (builds
 *     made before acts existed).
 *  3. visible — the build knows routes the calendar pass may not, and
 *     failing visible is the honest default.
 */
export function activePredicate(masks: Record<string, string>, date: Date) {
  const day = (date.getDay() + 6) % 7 // JS Sunday=0 → our Monday=0
  const hour = date.getHours()
  return (f: FeatureLike) => {
    const routes = routesOf(f.properties)
    if (routes.length === 0) return true
    const acts = String(f.properties.acts ?? '').split(';')
    return routes.some((r, i) => {
      const a = acts[i]
      if (a && a.length === 42) return maskActive(a, day, hour)
      const m = masks[r]
      return !m || maskActive(m, day, hour)
    })
  }
}

/** A station is visible while ANY member route is (a) in an enabled
 *  class and (b) awake AT THIS STATION at the given instant. Stations
 *  carry per-route `acts` sampled from their snapped segments (stop-
 *  granular after terminal cuts); the route-level mask is the fallback
 *  for entries without one. `date` null means the all-service map
 *  (time never hides). Aligned arrays: routes[i] ↔ modes[i] ↔ acts[i]. */
export function stationVisible(
  props: Record<string, any>,
  masks: Record<string, string>,
  date: Date | null,
  classesOff: Set<string>,
): boolean {
  const routes = routesOf(props)
  if (routes.length === 0) return true
  const modes = String(props.modes ?? '').split(',')
  const acts = String(props.acts ?? '').split(';')
  const day = date ? (date.getDay() + 6) % 7 : 0
  const hour = date ? date.getHours() : 0
  return routes.some((r, i) => {
    if (classesOff.has(modes[i])) return false
    if (!date) return true
    const a = acts[i]
    if (a && a.length === 42) return maskActive(a, day, hour)
    const m = masks[r]
    return !m || maskActive(m, day, hour)
  })
}

/** Indices of a feature's routes that are awake at `date` AND in an
 *  enabled class — the subset a bullet strip should show. A station
 *  that stays open at 2am must not advertise routes that stopped
 *  calling there at midnight: per-station `acts` decide first (the
 *  night M keeps its bullet at Myrtle Av, loses it at Flushing Av),
 *  the route-level mask is the fallback. Returns null when nothing is
 *  filtered (reuse the original feature; no churn). */
export function activeRouteIdx(
  props: Record<string, any>,
  masks: Record<string, string>,
  date: Date | null,
  classesOff: Set<string>,
): number[] | null {
  const routes = routesOf(props)
  if (!routes.length || (!date && !classesOff.size)) return null
  const modes = String(props.modes ?? '').split(',')
  const acts = String(props.acts ?? '').split(';')
  const day = date ? (date.getDay() + 6) % 7 : 0
  const hour = date ? date.getHours() : 0
  const idx: number[] = []
  routes.forEach((r, i) => {
    if (classesOff.has(modes[i])) return
    if (date) {
      const a = acts[i]
      if (a && a.length === 42) {
        if (!maskActive(a, day, hour)) return
      } else {
        const m = masks[r]
        if (m && !maskActive(m, day, hour)) return
      }
    }
    idx.push(i)
  })
  return idx.length === routes.length ? null : idx
}

// ── bullet strips ──────────────────────────────────────────────────────

/** bullet image id for one route; the image itself is generated on
 *  demand by the styleimagemissing handler, so any feed's bullets exist
 *  the moment a label asks for them. */
export const bulletId = (label: string, hex: string, shape = '') =>
  `blt-${hex || '888888'}-${shape}-${label}`

// "FX"/"6X"/"7X" are express variants of a line the set already shows —
// Apple never bullets them separately, and neither do we
export const isVariantLabel = (l: string, all: string[]) =>
  l.length >= 2 && l.endsWith('X') && all.includes(l.slice(0, -1))

/** bullets a station label shows: EVERY distinct (label, color) pair,
 *  and only for classes where a bullet means something — a commuter
 *  branch's identity is its agency, not a 20-character pill. No count
 *  cap: Atlantic Av-Barclays' merged label owns all ten of its lines
 *  (the strip renderer wraps long sets into rows instead of lying by
 *  truncation — a cap here silently dropped the 4 and 5). */
export function bulletIdsOf(p: any): string[] {
  const labels = String(p.labels ?? '').split(',')
  const colors = String(p.route_colors ?? '').split(',')
  const modes = String(p.modes ?? '').split(',')
  // shapes are aligned with routes like every other array here; absent
  // means the default circle
  const shapes = String(p.shapes ?? '').split(',')
  const seen = new Set<string>()
  const out: string[] = []
  labels.forEach((l, i) => {
    if (!l || l.length > 8) return
    if (modes[i] === 'regional' || modes[i] === 'bus') return
    if (isVariantLabel(l, labels)) return
    const id = bulletId(l, colors[i], shapes[i] ?? '')
    if (seen.has(id)) return
    seen.add(id)
    out.push(id)
  })
  return out
}

/** One ribbon of a marker's bundle, in union terms. `g` is the
 *  centerline hash the ribbon re-centers within (applyDynamic's
 *  grouping) — offsets here must move exactly as the ribbons move,
 *  residuals included, or dots detach from their lines. */
export interface BundleRow {
  g: string
  color: string
  off: number
  routes: string[]
  props: Record<string, any> // the ribbon feature's props (acts etc.)
}

/** The marker icon for one instant, re-derived against the marker's
 *  ribbon bundle: hide sleeping ribbons, re-center the survivors with
 *  applyDynamic's exact per-centerline rule, and draw a pill only while
 *  the station's ACTIVE lines still fill the surviving bundle — a J/M
 *  pill becomes a lone brown dot at 3am when the M sleeps. Returns the
 *  icon id (dots-… / pill-…), or null to keep the static union icon. */
export function markerIconAt(
  props: Record<string, any>,
  bundle: BundleRow[],
  masks: Record<string, string>,
  date: Date | null,
  classesOff: Set<string>,
): string | null {
  if (!bundle.length || (!date && !classesOff.size)) return null
  const pred = date ? activePredicate(masks, date) : () => true
  const rowOn = (r: BundleRow) =>
    !classesOff.has(r.props.mode) && pred({ properties: r.props } as any)

  // re-centered offset per row, mirroring applyDynamic: within each
  // centerline group, survivors of a thinned group re-pack at the
  // group's own pitch; singleton groups and untouched groups keep
  // their union offsets
  const byG = new Map<string, BundleRow[]>()
  for (const r of bundle) {
    if (!byG.has(r.g)) byG.set(r.g, [])
    byG.get(r.g)!.push(r)
  }
  const newOff = new Map<BundleRow, number>()
  const alive = new Map<BundleRow, boolean>()
  for (const rows of byG.values()) {
    const vis = rows.filter(rowOn)
    for (const r of rows) {
      alive.set(r, vis.includes(r))
      newOff.set(r, r.off)
    }
    if (rows.length < 2 || !vis.length || vis.length === rows.length) continue
    const offs = rows.map((r) => r.off).sort((a, b) => a - b)
    let pitch = Infinity
    for (let i = 1; i < offs.length; i++) {
      const d = offs[i] - offs[i - 1]
      if (d > 0.01 && d < pitch) pitch = d
    }
    if (!isFinite(pitch)) pitch = 6
    vis.sort((a, b) => a.off - b.off)
    vis.forEach((r, i) => newOff.set(r, (i - (vis.length - 1) / 2) * pitch))
  }
  const survivors = bundle.filter((r) => alive.get(r))
  if (!survivors.length) return null // station hides anyway

  // which surviving ribbons still have an ACTIVE stopping route here?
  const idx = activeRouteIdx(props, masks, date, classesOff)
  const routes = routesOf(props)
  const active = new Set(idx ? idx.map((i) => routes[i]) : routes)
  const stopRows = survivors.filter((r) => r.routes.some((x) => active.has(x)))
  if (!stopRows.length) return null
  if (stopRows.length === survivors.length && survivors.length > 1) {
    const offs = survivors.map((r) => newOff.get(r)!)
    const span = Math.max(...offs) - Math.min(...offs)
    return `pill-${Math.round(span * 10) / 10}`
  }
  const parts = stopRows
    .map((r) => ({ hex: r.color, off: newOff.get(r)! }))
    .sort((a, b) => a.off - b.off)
  return 'dots-' + parts.map((p) => `${p.hex}@${Math.round(p.off * 10) / 10}`).join(';')
}

// ── the dynamic render rule ────────────────────────────────────────────

const endKey = (c: number[]) => c[0].toFixed(6) + ',' + c[1].toFixed(6)

/** Travel direction along coordinate order at a feature end (lon-scaled
 *  unit vector). offset_px is SIGNED relative to this direction — the
 *  renderer offsets to the right of travel — so two features meeting at
 *  a point with opposite directions carry opposite signs for the same
 *  screen position. Anything that copies an offset across features must
 *  compare directions and flip. */
function endDir(cs: number[][], atStart: boolean): [number, number] {
  const a = atStart ? cs[0] : cs[cs.length - 1]
  const b = atStart ? cs[1] : cs[cs.length - 2]
  const kx = Math.cos((a[1] * Math.PI) / 180)
  const dx = (atStart ? b[0] - a[0] : a[0] - b[0]) * kx
  const dy = atStart ? b[1] - a[1] : a[1] - b[1]
  const n = Math.hypot(dx, dy) || 1
  return [dx / n, dy / n]
}

/**
 * applyDynamic filters a union-band FeatureCollection to one instant and
 * re-centers what survives. Pure: the input FC and its features are not
 * mutated, so the caller can keep the union band cached and re-apply for
 * every time change.
 *
 * Rules, in order:
 *  - a ribbon is visible when any of its routes is active;
 *  - bundles (features sharing a geometry hash) that lost members
 *    re-pack: survivors keep the UNION slot order and re-center at the
 *    union pitch (inferred per bundle from its own offsets);
 *  - single-ribbon bundles keep their original offset — a lone ribbon at
 *    ±3px is one half of a twin-edge corridor, and centering it would
 *    detach it from its pair;
 *  - transitions whose endpoint touches a moved ribbon of the same color
 *    get their ramp endpoints updated to the moved offset, so ramps land
 *    where the steady now sits.
 */
export function applyDynamic(fc: FCLike, isActive: (f: FeatureLike) => boolean): FCLike {
  const hidden = new Set<FeatureLike>()
  for (const f of fc.features) {
    if (!isActive(f)) hidden.add(f)
  }

  // bundle = features sharing one centerline
  const groups = new Map<string, FeatureLike[]>()
  for (const f of fc.features) {
    const kind = f.properties.kind
    if (kind !== 'steady' && kind !== 'bridge') continue
    const g =
      f._g ??
      endKey(f.geometry.coordinates[0]) +
        '|' +
        endKey(f.geometry.coordinates[f.geometry.coordinates.length - 1]) +
        '|' +
        f.geometry.coordinates.length
    const arr = groups.get(g)
    if (arr) arr.push(f)
    else groups.set(g, [f])
  }

  const newProps = new Map<FeatureLike, Record<string, any>>()
  // "endpoint|color" → the moved steady's new offset AND its travel
  // direction at that end: the sign of an offset is direction-relative,
  // so a transition attaching against the steady's grain takes -off
  const moved = new Map<string, { off: number; dir: [number, number] }>()

  for (const rows of groups.values()) {
    if (rows.length < 2) continue // singletons keep their offset (twin edges)
    const vis = rows.filter((f) => !hidden.has(f))
    if (vis.length === 0 || vis.length === rows.length) continue

    // union pitch, from this bundle's own offsets
    const offs = rows.map((f) => +f.properties.offset_px).sort((a, b) => a - b)
    let pitch = Infinity
    for (let i = 1; i < offs.length; i++) {
      const d = offs[i] - offs[i - 1]
      if (d > 0.01 && d < pitch) pitch = d
    }
    if (!isFinite(pitch)) pitch = 6

    vis.sort((a, b) => a.properties.slot - b.properties.slot)
    vis.forEach((f, i) => {
      const off = (i - (vis.length - 1) / 2) * pitch
      newProps.set(f, { ...f.properties, offset_px: off, slot: i, nslots: vis.length })
      if (Math.abs(off - f.properties.offset_px) > 0.01) {
        const cs = f.geometry.coordinates
        moved.set(endKey(cs[0]) + '|' + f.properties.color, { off, dir: endDir(cs, true) })
        moved.set(endKey(cs[cs.length - 1]) + '|' + f.properties.color, { off, dir: endDir(cs, false) })
      }
    })
  }

  const out: FeatureLike[] = []
  for (const f of fc.features) {
    if (hidden.has(f)) continue
    let props = newProps.get(f)
    if (!props && f.properties.kind === 'transition') {
      const cs = f.geometry.coordinates
      const from = moved.get(endKey(cs[0]) + '|' + f.properties.color)
      const to = moved.get(endKey(cs[cs.length - 1]) + '|' + f.properties.color)
      if (from !== undefined || to !== undefined) {
        // copy the offset in the TRANSITION's frame: flip the sign when
        // the ramp runs against the steady's direction at the joint
        const signed = (m: { off: number; dir: [number, number] }, atStart: boolean) => {
          const d = endDir(cs, atStart)
          return (m.dir[0] * d[0] + m.dir[1] * d[1] >= 0 ? 1 : -1) * m.off
        }
        props = {
          ...f.properties,
          off_from_px: from ? signed(from, true) : f.properties.off_from_px,
          off_to_px: to ? signed(to, false) : f.properties.off_to_px,
        }
      }
    }
    out.push(props ? { ...f, properties: props } : f)
  }
  return { type: 'FeatureCollection', features: out }
}
