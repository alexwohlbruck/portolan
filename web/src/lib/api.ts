// The console talks to the Go atlas server. Every response shape the UI
// depends on is declared here so a server change breaks typecheck rather
// than a screen.

export interface BBox extends Array<number> {} // [w,s,e,n]

export interface ModeStyle {
  color?: string
  width: number
  opacity: number
  band_floor: number
  trunk: string
  hidden: boolean
}

export interface StyleSet {
  modes: Record<string, ModeStyle>
  colors?: Record<string, string>
  names?: Record<string, string>
  /** resolved: inline route bullets riding the ribbons */
  caterpillars?: boolean
  /** resolved: adopt matched OSM stop names */
  osm_stop_names?: boolean
}

/** One curated subject — a route, an agency or a stop. */
export interface StyleEntity {
  name?: string
  color?: string
  /** agencies only: this operator's route colours are real line identities */
  line_colors?: boolean
}

/** One curation document — style/_default.json or style/<feed>.json.
 *  Subject-keyed: a route is named once and carries everything known
 *  about it. Absent fields inherit. */
export interface StyleConfig {
  modes?: Record<string, Partial<Omit<ModeStyle, 'hidden'>> & { hidden?: boolean }>
  agencies?: Record<string, StyleEntity>
  routes?: Record<string, StyleEntity>
  stops?: Record<string, StyleEntity>
  options?: {
    /** bullet ordering policy: 'color' (default) | 'feed' | 'natural' */
    bullet_order?: string
    /** inline route bullets riding the ribbons; absent inherits (default on) */
    caterpillars?: boolean
    /** adopt matched OSM stop names; absent inherits (default on) */
    osm_stop_names?: boolean
  }
}

/** One portolan.json entry — a single GTFS feed and its inputs/outputs. */
export interface Feed {
  id: string
  name: string
  gtfs: string
  rail: string
  streets?: string
  out: string
  network?: string
  bbox?: BBox
  line_agencies?: string[]
  stops?: string
  /** server-computed: which inputs actually exist on disk */
  status?: FeedStatus
}

export interface FeedStatus {
  gtfs: { path: string; ok: boolean; size?: number }[]
  rail: { path: string; ok: boolean; size?: number }
  streets?: { path: string; ok: boolean; size?: number }
  build?: { path: string; ok: boolean; size?: number; modified?: string }
  scenarios_built: number
}

export interface Scenario {
  id: string
  label: string
  patterns: number
  built: boolean
}

export interface ScenariosResp {
  available: boolean
  error?: string
  scenarios?: Scenario[]
  grid?: string[][]
}

export interface RunStatus {
  running: boolean
  done: boolean
  ok: boolean
  cmd: string
  log: string[]
}

export interface Area {
  id: string
  /** legacy key on areas saved before the feed rename; `feed` wins */
  city?: string
  feed?: string
  label?: string
  note?: string
  c?: [number, number]
  z?: number
  [k: string]: unknown
}

/** TileJSON 3.0 for a region pyramid cut by `portolan tiles`. The server
 *  answers /api/tiles/<feed>/tiles.json only when a pyramid exists —
 *  a 404 IS the mode switch, so the helper folds it into null rather
 *  than throwing like every other endpoint. */
export interface TileJSON {
  tilejson?: string
  name?: string
  /** relative template ("{z}/{x}/{y}.mvt"), resolved by the client */
  tiles: string[]
  minzoom: number
  maxzoom: number
  bounds?: number[] // [w,s,e,n]
  vector_layers?: { id: string; minzoom?: number; maxzoom?: number }[]
}

/** One row of /api/tiles/index.json — every feed with a cut pyramid.
 *  Bounds and maxzoom ride along so the global map can build all its
 *  sources from the index alone, without touching each tiles.json. */
export interface TilesIndexEntry {
  feed: string
  name?: string
  bounds: number[] // [w,s,e,n]
  maxzoom: number
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const r = await fetch(path, init)
  if (!r.ok) {
    const body = await r.text().catch(() => '')
    throw new Error(body.trim() || `${r.status} ${r.statusText}`)
  }
  const text = await r.text()
  return (text ? JSON.parse(text) : null) as T
}

const json = (body: unknown): RequestInit => ({
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(body),
})

export const api = {
  feeds: () => req<Feed[]>('/api/feeds'),
  feed: (id: string) => req<Feed>(`/api/feeds/${encodeURIComponent(id)}`),
  saveFeed: (id: string, c: Partial<Feed>) => req<Feed>(`/api/feeds/${encodeURIComponent(id)}`, json(c)),
  deleteFeed: (id: string) =>
    req<null>(`/api/feeds/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  style: (feed: string) => req<StyleSet>(`/api/style?feed=${encodeURIComponent(feed)}`),
  // the `city` key is the per-feed layer — the wire name predates the
  // feed-as-unit rename and the server still speaks it
  styleConfig: (feed: string) =>
    req<{ global: StyleConfig; city: StyleConfig }>(`/api/style/config?feed=${encodeURIComponent(feed)}`),
  saveStyleConfig: (feed: string, body: { global?: StyleConfig; city?: StyleConfig }) =>
    req<null>(`/api/style/config?feed=${encodeURIComponent(feed)}`, json(body)),

  scenarios: (feed: string) => req<ScenariosResp>(`/api/scenarios?feed=${encodeURIComponent(feed)}`),

  tilesIndex: () => req<TilesIndexEntry[]>('/api/tiles/index.json'),

  // null = no pyramid for this feed (404); the caller falls back to the
  // band-GeoJSON path. Any other failure still throws.
  tilejson: async (feed: string): Promise<TileJSON | null> => {
    const r = await fetch(`/api/tiles/${encodeURIComponent(feed)}/tiles.json`)
    if (r.status === 404) return null
    if (!r.ok) {
      const body = await r.text().catch(() => '')
      throw new Error(body.trim() || `${r.status} ${r.statusText}`)
    }
    return (await r.json()) as TileJSON
  },

  run: (feed: string, cmd: 'chart' | 'sound', scenario?: string) =>
    req<null>(
      `/api/run?cmd=${cmd}&feed=${encodeURIComponent(feed)}${scenario ? `&scenario=${scenario}` : ''}`,
      { method: 'POST' },
    ),
  runStatus: () => req<RunStatus>('/api/run/status'),

  score: (feed: string) => req<any>(`/api/score?feed=${encodeURIComponent(feed)}`),
  areas: () => req<Area[]>('/api/locations'),
  saveAreas: (areas: Area[]) => req<null>('/api/locations', json(areas)),

  activity: async (feed: string) => {
    const r = await req<{ available: boolean; masks?: Record<string, string> }>(
      `/api/activity?feed=${encodeURIComponent(feed)}`,
    )
    return r.available && r.masks ? r.masks : {}
  },

  // stations are a plain FeatureCollection of Points (docs/STOP-LABELS.md)
  stations: (feed: string) => req<any>(`/api/stations.geojson?feed=${encodeURIComponent(feed)}`),

  routes: (feed: string) =>
    req<{ id: string; short_name: string; long_name: string; color: string; mode: string; agency: string; agency_name: string }[]>(
      `/api/routes?feed=${encodeURIComponent(feed)}`,
    ),
}


// ── content-addressed build loading ───────────────────────────────────
// A scenario is mostly the union map with lines missing, so most of its
// geometry is geometry we already hold. The client keeps a coordinate
// cache keyed by content hash and tells the server what it has; the
// server returns the feature table plus only the missing coordinates.
// Switching back to a scenario already visited costs properties alone.
const geomCache = new Map<string, number[][]>()
// Content addressing makes the cache safe across rebuilds — a hash always
// means the same coordinates, so a stale entry can only ever be unused,
// never wrong. It only needs a ceiling so a long session browsing every
// feed does not grow it without bound.
const GEOM_CACHE_MAX = 20000

export interface DeltaStats {
  features: number
  geometries_sent: number
  geometries_reused: number
  bytes: number
}

/** Fetch one band of a build and assemble it back into a
 *  FeatureCollection identical to what /api/build.geojson would serve. */
export async function fetchBuild(
  feed: string,
  band: number,
  scenario?: string,
): Promise<{ data: any; stats: DeltaStats }> {
  const url =
    `/api/build-delta?feed=${encodeURIComponent(feed)}&band=${band}` +
    (scenario ? `&scenario=${encodeURIComponent(scenario)}` : '')
  const r = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ have: [...geomCache.keys()] }),
  })
  if (!r.ok) throw new Error(await r.text())
  const text = await r.text()
  const body = JSON.parse(text)
  if (geomCache.size > GEOM_CACHE_MAX) geomCache.clear()
  for (const [h, coords] of Object.entries(body.geom ?? {})) {
    geomCache.set(h, coords as number[][])
  }
  const features = (body.features ?? [])
    .map((f: any) => {
      const coords = geomCache.get(f.g)
      if (!coords) return null // geometry we claimed to have but dropped
      // _g rides along: identical hash = identical centerline = one
      // corridor bundle, which is exactly the grouping dynamic rendering
      // needs. MapLibre ignores foreign members.
      return { type: 'Feature', _g: f.g, properties: f.p, geometry: { type: 'LineString', coordinates: coords } }
    })
    .filter(Boolean)
  return {
    data: { type: 'FeatureCollection', features },
    stats: { ...(body.stats ?? {}), bytes: text.length } as DeltaStats,
  }
}

/** Drop cached geometry. Not needed for correctness — see above — but
 *  useful when measuring cold-cache transfer. */
export const clearGeomCache = () => geomCache.clear()
