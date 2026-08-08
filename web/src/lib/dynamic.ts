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

/** Predicate for one instant. A route with no mask stays visible — the
 *  build knows routes the calendar pass may not (shapes without trips in
 *  the load window), and failing visible is the honest default. */
export function activePredicate(masks: Record<string, string>, date: Date) {
  const day = (date.getDay() + 6) % 7 // JS Sunday=0 → our Monday=0
  const hour = date.getHours()
  return (routes: string[]) =>
    routes.length === 0 ||
    routes.some((r) => {
      const m = masks[r]
      return !m || maskActive(m, day, hour)
    })
}

// ── the dynamic render rule ────────────────────────────────────────────

const endKey = (c: number[]) => c[0].toFixed(6) + ',' + c[1].toFixed(6)

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
export function applyDynamic(fc: FCLike, isActive: (routes: string[]) => boolean): FCLike {
  const hidden = new Set<FeatureLike>()
  for (const f of fc.features) {
    if (!isActive(routesOf(f.properties))) hidden.add(f)
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
  const moved = new Map<string, number>() // "endpoint|color" → new offset

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
        moved.set(endKey(cs[0]) + '|' + f.properties.color, off)
        moved.set(endKey(cs[cs.length - 1]) + '|' + f.properties.color, off)
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
        props = {
          ...f.properties,
          off_from_px: from ?? f.properties.off_from_px,
          off_to_px: to ?? f.properties.off_to_px,
        }
      }
    }
    out.push(props ? { ...f, properties: props } : f)
  }
  return { type: 'FeatureCollection', features: out }
}
