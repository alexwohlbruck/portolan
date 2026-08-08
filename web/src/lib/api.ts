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
}

/** Editable style layer — only the fields a city (or the global block)
 *  actually overrides. Absent fields inherit. */
export interface StyleConfig {
  modes?: Record<string, Partial<Omit<ModeStyle, 'hidden'>> & { hidden?: boolean }>
  colors?: Record<string, string>
}

export interface City {
  id: string
  name: string
  gtfs: string
  rail: string
  streets?: string
  out: string
  network?: string
  bbox?: BBox
  line_agencies?: string[]
  modes?: StyleConfig['modes']
  colors?: StyleConfig['colors']
  /** server-computed: which inputs actually exist on disk */
  status?: CityStatus
}

export interface CityStatus {
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
  city?: string
  feed?: string
  label?: string
  note?: string
  c?: [number, number]
  z?: number
  [k: string]: unknown
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
  cities: () => req<City[]>('/api/cities'),
  city: (id: string) => req<City>(`/api/cities/${encodeURIComponent(id)}`),
  saveCity: (id: string, c: Partial<City>) => req<City>(`/api/cities/${encodeURIComponent(id)}`, json(c)),
  deleteCity: (id: string) =>
    req<null>(`/api/cities/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  style: (feed: string) => req<StyleSet>(`/api/style?feed=${encodeURIComponent(feed)}`),
  styleConfig: (feed: string) =>
    req<{ global: StyleConfig; city: StyleConfig }>(`/api/style/config?feed=${encodeURIComponent(feed)}`),
  saveStyleConfig: (feed: string, body: { global?: StyleConfig; city?: StyleConfig }) =>
    req<null>(`/api/style/config?feed=${encodeURIComponent(feed)}`, json(body)),

  scenarios: (feed: string) => req<ScenariosResp>(`/api/scenarios?feed=${encodeURIComponent(feed)}`),

  run: (feed: string, cmd: 'chart' | 'sound', scenario?: string) =>
    req<null>(
      `/api/run?cmd=${cmd}&feed=${encodeURIComponent(feed)}${scenario ? `&scenario=${scenario}` : ''}`,
      { method: 'POST' },
    ),
  runStatus: () => req<RunStatus>('/api/run/status'),

  score: (feed: string) => req<any>(`/api/score?feed=${encodeURIComponent(feed)}`),
  areas: () => req<Area[]>('/api/locations'),
  saveAreas: (areas: Area[]) => req<null>('/api/locations', json(areas)),

  routes: (feed: string) =>
    req<{ id: string; short_name: string; long_name: string; color: string; mode: string; agency: string; agency_name: string }[]>(
      `/api/routes?feed=${encodeURIComponent(feed)}`,
    ),
}

export const buildURL = (feed: string, scenario?: string, band?: number) =>
  `/api/build.geojson?feed=${encodeURIComponent(feed)}` +
  (scenario ? `&scenario=${scenario}` : '') +
  (band === undefined ? '' : `&band=${band}`) +
  `&t=${Date.now()}`
