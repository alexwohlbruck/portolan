<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, watch, computed } from 'vue'
import { Layers, Crosshair, RefreshCw, Clock } from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Switch from '@/components/ui/Switch.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { api, fetchBuild, type Scenario, type StyleSet } from '@/lib/api'
import { applyDynamic, activePredicate, maskActive, stationVisible } from '@/lib/dynamic'
import { feed, currentCity, run } from '@/lib/store'
import { toast } from '@/lib/toast'

// MapLibre comes from the FORK the server exposes at /vendor — it carries
// variable line-offset along line-progress, which is what draws a ribbon
// sliding between slots. The npm build cannot render portolan's output.
declare const maplibregl: any

const el = ref<HTMLDivElement | null>(null)
const loading = ref(true)
const scenarios = ref<Scenario[]>([])
// grid[dayOfWeek][hour] -> scenario id. Day 0 = Monday, matching the
// pipeline's convention, NOT JavaScript's Sunday-first getDay().
const grid = ref<string[][]>([])
// datetime-local value. EMPTY IS A VALUE: it means the all-service union
// map. Seeded from ?t= so a moment can be linked.
const when = ref(new URL(window.location.href).searchParams.get('t') ?? '')
// route id -> 168-bit weekly activity mask (docs/DYNAMIC-SERVICE.md).
// This is what makes ANY timestamp renderable with no prebuilt layout.
const masks = ref<Record<string, string>>({})
const styleSet = ref<StyleSet | null>(null)
const inspect = ref<Record<string, any> | null>(null)
let map: any = null
let ro: ResizeObserver | null = null

// Zoom bands: FAIR emits one copy of the map per band, and exactly one
// band may be visible at a time or every ribbon doubles.
const BANDS = [
  { min: 15, max: 24, key: 15 },
  { min: 14, max: 15, key: 14 },
  { min: 13, max: 14, key: 13 },
  { min: 0, max: 13, key: 0 },
]

// Offsets live in DIFFERENT properties depending on the segment kind, and
// getting this wrong is invisible in the data and glaring on the map: a
// steady segment carries its slot offset in `offset_px` (off_from/off_to
// are 0), a transition carries the ease endpoints in off_from_px/
// off_to_px (offset_px is 0). Reading the transition pair for everything
// draws every steady ribbon at offset 0 — the whole bundle collapses onto
// one line and only the topmost colour is visible.
const zoomScaledOffset = (e: any) => ['interpolate', ['linear'], ['zoom'], 11, ['*', e, 0.5], 14, e]
const STEADY_OFFSET = zoomScaledOffset(['get', 'offset_px'])
const TRANSITION_OFFSET = zoomScaledOffset([
  'interpolate', ['cubic-bezier', 0.4, 0, 0.6, 1], ['line-progress'],
  0, ['get', 'off_from_px'], 1, ['get', 'off_to_px'],
])
// bridges render exactly like steady ribbons — the gap-bridge distinction
// is pipeline bookkeeping, not something a rider sees.
const KINDS: [string, any][] = [
  ['steady', STEADY_OFFSET],
  ['transition', TRANSITION_OFFSET],
  ['bridge', STEADY_OFFSET],
]
const debug = ref({ paths: false, trackcenter: false, nodes: false, rail: false })

// per-class visibility. Stored as the DISABLED set so the default —
// everything on — is an empty set and new classes appearing in a build
// are visible without migration. Hiding a class routes through the same
// dynamic filter as time, so surviving bundles re-center instead of
// keeping a gap where the hidden class sat. Persisted per city.
const CLASS_ORDER = ['metro', 'tram', 'regional', 'monorail', 'funicular', 'cable', 'aerial', 'ferry', 'bus']
const classesOff = ref<Set<string>>(new Set())
const modesPresent = ref<string[]>([])

const classKey = () => `portolan.classes-off.${feed.value}`
function loadClassesOff() {
  try {
    classesOff.value = new Set(JSON.parse(localStorage.getItem(classKey()) ?? '[]'))
  } catch {
    classesOff.value = new Set()
  }
}
function toggleClass(m: string, on: boolean) {
  const next = new Set(classesOff.value)
  if (on) next.delete(m)
  else next.add(m)
  classesOff.value = next // replace, not mutate — Set contents aren't reactive
  localStorage.setItem(classKey(), JSON.stringify([...next]))
}
// prefetch band 15 without waiting for the map: the Layers panel and the
// class list are DATA, and coupling them to WebGL coming up is the same
// mistake the sketch editor and scenario picker already made once. The
// delta cache makes the map's own later fetch of the same band ~free.
async function prefetchModes() {
  if (!feed.value || bandRaw.has(15)) return
  try {
    const { data } = await fetchBuild(feed.value, 15)
    if (!bandRaw.has(15)) {
      bandRaw.set(15, data)
      loadedBands.add(15)
      refreshModes()
      applyBand(15)
    }
  } catch {
    /* the map's own load path will retry */
  }
}

function refreshModes() {
  const seen = new Set<string>()
  for (const raw of bandRaw.values())
    for (const f of raw.features) if (f.properties?.mode) seen.add(f.properties.mode)
  modesPresent.value = CLASS_ORDER.filter((m) => seen.has(m))
}
// every class gets a row, always: the panel is the taxonomy, not a
// summary of this build. Absent classes render dimmed — their toggle
// still works and persists, so switching a class off in one city carries
// to a city that has it.
const inBuild = (m: string) => modesPresent.value.includes(m)
const classDot = (m: string) => {
  const hex = styleSet.value?.modes?.[m]?.color
  return { background: hex ? `#${hex}` : 'var(--muted-foreground)' }
}

const byId = computed(() => Object.fromEntries(scenarios.value.map((s) => [s.id, s])))

/** The scenario that draws a given instant. Resolution is by weekday and
 *  hour because that is the structure GTFS calendars actually carry — any
 *  date in the year works, and a date resolves to its weekday's service.
 *  Holidays deliberately follow regular service (see docs). */
function scenarioAt(iso: string): { id: string; label: string; built: boolean } | null {
  if (!iso || !grid.value.length) return null
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return null
  const day = (d.getDay() + 6) % 7 // JS Sunday=0 -> our Monday=0
  const id = grid.value[day]?.[d.getHours()] ?? ''
  if (!id) return null
  const s = byId.value[id]
  return { id, label: s?.label ?? id, built: !!s?.built }
}

const resolved = computed(() => scenarioAt(when.value))

const localNow = () => {
  const d = new Date()
  d.setMinutes(d.getMinutes() - d.getTimezoneOffset())
  return d.toISOString().slice(0, 16)
}

// A timestamp renders DYNAMICALLY from the union layout: hide the
// ribbons whose routes are dark at that instant, re-center the rest
// within the fixed union slot order (lib/dynamic.ts). No prebuilt file
// is involved, so every timestamp works immediately — the prebuilt
// scenarios remain reachable through the picker as QA references.
//
// No time set is a meaningful state, not a missing one: it means the
// all-service union map. Clearing the field goes back to it.
watch(when, applyBands)

const activeAt = computed(() => {
  if (!when.value) return null
  const d = new Date(when.value)
  if (Number.isNaN(d.getTime())) return null
  return activePredicate(masks.value, d)
})

// how much of the network is running right now — the toolbar's summary.
// Route-level on purpose: it answers "how many routes run", while the
// per-segment acts on each feature decide what actually draws.
const runningCount = computed(() => {
  if (!when.value) return null
  const d = new Date(when.value)
  if (Number.isNaN(d.getTime())) return null
  const day = (d.getDay() + 6) % 7
  const hour = d.getHours()
  const ids = Object.keys(masks.value)
  if (!ids.length) return null
  return { on: ids.filter((r) => maskActive(masks.value[r], day, hour)).length, total: ids.length }
})

async function loadMasks() {
  masks.value = feed.value ? await api.activity(feed.value).catch(() => ({})) : {}
}

// ?t=<local ISO> makes a moment linkable; no parameter is the union map.
function syncURL(v: string) {
  const u = new URL(window.location.href)
  if (v) u.searchParams.set('t', v)
  else u.searchParams.delete('t')
  window.history.replaceState({}, '', u)
}
watch(when, syncURL)

// a finished run may have produced new scenarios or a new union build
watch(() => run.value.done, (d) => d && loadScenarios())

function widthExpr(w: any) {
  return ['interpolate', ['linear'], ['zoom'], 10, ['*', 1.0, w], 13, ['*', 2.0, w], 15, ['*', 3.0, w], 16, ['*', 3.6, w]]
}

function modeExprs(st: StyleSet | null) {
  const w: any[] = ['match', ['coalesce', ['get', 'mode'], '']]
  const o: any[] = ['match', ['coalesce', ['get', 'mode'], '']]
  for (const [name, m] of Object.entries(st?.modes ?? {})) {
    if (name === 'unknown') continue
    w.push(name, m.width ?? 1)
    o.push(name, m.opacity ?? 1)
  }
  w.push(1)
  o.push(1)
  return { w, o }
}

const ribbonIds: string[] = []

// which bands have their data in already. A band is fetched the first time
// the viewport enters it and then kept — flipping back and forth across a
// boundary should not re-download.
const loadedBands = new Set<number>()
// raw (union or explicitly-picked scenario) FCs per band. Dynamic mode
// re-filters these in memory on every time change — no refetch.
const bandRaw = new Map<number, any>()
const transferred = ref<{ geometries_sent: number; geometries_reused: number; bytes: number } | null>(null)

const bandForZoom = (z: number) =>
  (BANDS.find((b) => z >= b.min && z < b.max) ?? BANDS[BANDS.length - 1]).key

async function ensureBand() {
  if (!map || !feed.value) return
  const key = bandForZoom(map.getZoom())
  if (loadedBands.has(key)) return
  loadedBands.add(key)
  try {
    const { data, stats } = await fetchBuild(feed.value, key)
    bandRaw.set(key, data)
    refreshModes()
    applyBand(key)
    transferred.value = stats
  } catch {
    loadedBands.delete(key) // let a later zoom retry
  }
}

/** push one band to the map, through the dynamic filter when a time is
 *  set or a class is hidden — both are the same operation (hide + the
 *  survivors re-center), so they compose in one predicate. */
function applyBand(key: number) {
  const raw = bandRaw.get(key)
  if (!raw || !map) return
  const timePred = activeAt.value
  const off = classesOff.value
  const pred =
    timePred || off.size
      ? (f: any) => !off.has(f.properties.mode) && (!timePred || timePred(f))
      : null
  map.getSource(`build-${key}`)?.setData(pred ? applyDynamic(raw, pred) : raw)
}

function applyBands() {
  for (const key of bandRaw.keys()) applyBand(key)
}

// ── stations (docs/STOP-LABELS.md) ─────────────────────────────────────
// Points with per-route metadata; fetched independently of WebGL (the
// map applies them whenever both are ready). Same dynamic rule as
// ribbons: time and class toggles hide, via stationVisible.
const stationsRaw = ref<any | null>(null)

// bullet image id for one route. The image itself is generated on demand
// by the styleimagemissing handler (drawMarkerImage), so any city's
// bullets exist the moment a label asks for them.
const bulletId = (label: string, hex: string) => `blt-${hex || '888888'}-${label}`

/** bullets a station label shows: distinct (label, color) pairs, capped,
 *  and only for classes where a bullet means something — a commuter
 *  branch's identity is its agency, not a 20-character pill. */
function bulletIdsOf(p: any): string[] {
  const labels = String(p.labels ?? '').split(',')
  const colors = String(p.route_colors ?? '').split(',')
  const modes = String(p.modes ?? '').split(',')
  const seen = new Set<string>()
  const out: string[] = []
  labels.forEach((l, i) => {
    if (!l || l.length > 8) return
    if (modes[i] === 'regional' || modes[i] === 'bus') return
    if (isVariantLabel(l, labels)) return
    const id = bulletId(l, colors[i])
    if (seen.has(id)) return
    seen.add(id)
    if (out.length < 8) out.push(id)
  })
  return out
}

// "FX"/"6X"/"7X" are express variants of a line the set already shows —
// Apple never bullets them separately, and neither do we
const isVariantLabel = (l: string, all: string[]) =>
  l.length >= 2 && l.endsWith('X') && all.includes(l.slice(0, -1))

// how many lines a name wraps to: simulate the wrap instead of counting
// characters (length/20 called "Bedford-Nostrand Avs" one line and the
// bullets overlapped the wrapped text). Greedy-pack the words — plus
// hyphen break points, which MapLibre also uses — against the layer's
// 10 em text-max-width, measured with canvas at 1 em = font size.
let measureCtx: CanvasRenderingContext2D | null = null
function estRows(name: string): number {
  if (!measureCtx) {
    measureCtx = document.createElement('canvas').getContext('2d')!
    measureCtx.font = '500 100px Montserrat, system-ui, sans-serif'
  }
  const maxW = 10 * 100 // 10 em
  const tokens = name.split(/\s+/).flatMap((w) => w.split(/(?<=-)/))
  const space = measureCtx.measureText(' ').width
  let rows = 1
  let line = 0
  tokens.forEach((t, i) => {
    const w = measureCtx!.measureText(t).width
    const glue = i > 0 && !tokens[i - 1].endsWith('-') ? space : 0
    if (line > 0 && line + glue + w > maxW) {
      rows++
      line = w
    } else {
      line += glue + w
    }
  })
  return Math.min(3, rows)
}

async function loadStations() {
  const fc = feed.value ? await api.stations(feed.value).catch(() => null) : null
  if (fc?.features) {
    for (const f of fc.features) {
      const p = f.properties
      if (p.ftype === 'marker') {
        // marker rule: lines that fill the whole bundle → a white pill
        // lying ACROSS it; anything less → one borderless dot per
        // stopping line, each baked at its ribbon's slot offset so the
        // group rotates with the corridor bearing as a unit
        p.icon = p.dots ? `dots-${p.dots}` : `pill-${p.span_px || 0}`
        // a complex's markers each get their OWN label at high zoom
        // (this corridor's name + bullets), while the merged station
        // label bows out — Apple's Fulton St behaviour
        if (p.nmarkers > 1) {
          const ids = bulletIdsOf(p)
          if (ids.length) p.brow = 'row-' + ids.join('|')
          p.nrows = estRows(String(p.name ?? ''))
        }
      } else {
        // the whole bullet strip is ONE composed image rendered as the
        // symbol's icon. Bullets must NOT ride inside the text-field:
        // the fork corrupts the per-tile glyph/image atlas when format
        // sections mix images into text (dense tiles rendered random
        // glyphs where bullets belonged), and the icon pipeline never
        // touches the glyph atlas.
        const ids = bulletIdsOf(p)
        if (ids.length) p.brow = 'row-' + ids.join('|')
        // wrapped-name estimate drives how far below the anchor the
        // bullet strip sits (MapLibre wraps at ~10em ≈ 20 chars)
        p.nrows = estRows(String(p.name ?? ''))
      }
    }
  }
  stationsRaw.value = fc
  applyStations()
}

// ── marker + bullet images, drawn on demand ────────────────────────────
// All images render at 2× (pixelRatio 2). Sizes below are CSS px at full
// zoom (z14+, where the slot pitch is its full 6 px).
const DOT_D = 7 // dot diameter; also the pill height and its corner radius ×2

// one bullet as a canvas: MTA-style circle for 1–2 char labels, a
// rounded-corner word pill (the Chicago 'Red'/'Brown' shape) for longer
function bulletCanvas(id: string): HTMLCanvasElement | null {
  const m = id.match(/^blt-([0-9a-fA-F]{6})-(.+)$/)
  if (!m) return null
  const hex = m[1]
  const label = m[2]
  const h = 14
  const cv = document.createElement('canvas')
  cv.width = 2
  cv.height = 2
  let ctx = cv.getContext('2d')!
  ctx.font = '600 9.5px system-ui, sans-serif'
  const tw = ctx.measureText(label).width
  const circle = label.length <= 2
  const w = circle ? h : Math.ceil(tw) + 9
  cv.width = w * 2
  cv.height = h * 2
  ctx = cv.getContext('2d')!
  ctx.scale(2, 2)
  ctx.fillStyle = '#' + hex
  ctx.beginPath()
  if (circle) ctx.arc(w / 2, h / 2, h / 2, 0, Math.PI * 2)
  else ctx.roundRect(0, 0, w, h, 3.5)
  ctx.fill()
  ctx.fillStyle = lumaOf(hex) > 160 ? '#111111' : '#ffffff'
  ctx.font = '600 9.5px system-ui, sans-serif'
  ctx.textAlign = 'center'
  ctx.textBaseline = 'middle'
  ctx.fillText(label, w / 2, h / 2 + 0.5)
  return cv
}

function drawMarkerImage(id: string): ImageData | null {
  const cv = document.createElement('canvas')
  const draw = (w: number, h: number) => {
    cv.width = w * 2
    cv.height = h * 2
    const ctx = cv.getContext('2d')!
    ctx.scale(2, 2)
    return ctx
  }
  let m: RegExpMatchArray | null
  if ((m = id.match(/^dots-(.+)$/))) {
    // one dot per stopping line: "hex@off;hex@off…", each circle at its
    // ribbon's slot offset from the marker anchor
    const dots = m[1].split(';').map((s) => {
      const [hex, off] = s.split('@')
      return { hex: /^[0-9a-fA-F]{6}$/.test(hex) ? hex : '888888', off: parseFloat(off) || 0 }
    })
    const reach = Math.max(...dots.map((d) => Math.abs(d.off)))
    const w = DOT_D + 2 * reach
    const ctx = draw(w, DOT_D)
    for (const d of dots) {
      ctx.fillStyle = '#' + d.hex
      ctx.beginPath()
      ctx.arc(w / 2 + d.off, DOT_D / 2, DOT_D / 2, 0, Math.PI * 2)
      ctx.fill()
    }
    return ctx.getImageData(0, 0, cv.width, cv.height)
  }
  if ((m = id.match(/^pill-([\d.]+)$/))) {
    const span = parseFloat(m[1])
    const w = span + DOT_D + 2
    const h = DOT_D + 2
    const ctx = draw(w, h)
    ctx.fillStyle = '#ffffff'
    ctx.strokeStyle = 'rgba(10,10,16,0.55)'
    ctx.lineWidth = 1
    ctx.beginPath()
    ctx.roundRect(1, 1.5, w - 2, DOT_D, DOT_D / 2)
    ctx.fill()
    ctx.stroke()
    return ctx.getImageData(0, 0, cv.width, cv.height)
  }
  if ((m = id.match(/^row-(.+)$/))) {
    // a whole bullet strip composed into one image (see loadStations)
    const parts = m[1].split('|').map(bulletCanvas).filter(Boolean) as HTMLCanvasElement[]
    if (!parts.length) return null
    const gap = 3 * 2
    const w = parts.reduce((a, c) => a + c.width, 0) + gap * (parts.length - 1)
    const h = Math.max(...parts.map((c) => c.height))
    cv.width = w
    cv.height = h
    const ctx = cv.getContext('2d')!
    let x = 0
    for (const c of parts) {
      ctx.drawImage(c, x, 0)
      x += c.width + gap
    }
    return ctx.getImageData(0, 0, w, h)
  }
  const single = bulletCanvas(id)
  return single ? single.getContext('2d')!.getImageData(0, 0, single.width, single.height) : null
}
// perceived luminance — yellow bullets (N/Q/R/W) need dark glyphs
const lumaOf = (hex: string) => {
  const n = parseInt(hex, 16)
  return 0.299 * (n >> 16) + 0.587 * ((n >> 8) & 255) + 0.114 * (n & 255)
}

function applyStations() {
  const src = map?.getSource('stations')
  if (!src) return
  const raw = stationsRaw.value
  if (!raw) {
    src.setData({ type: 'FeatureCollection', features: [] })
    return
  }
  const d = when.value ? new Date(when.value) : null
  const date = d && !Number.isNaN(d.getTime()) ? d : null
  const off = classesOff.value
  const feats =
    date || off.size
      ? raw.features.filter((f: any) => stationVisible(f.properties, masks.value, date, off))
      : raw.features
  src.setData({ type: 'FeatureCollection', features: feats })
}

// time and class changes re-filter the cached data in memory — no fetch
watch([activeAt, masks, classesOff], () => {
  applyBands()
  applyStations()
})

function addLayers() {
  const { w, o } = modeExprs(styleSet.value)
  const COLOR = ['concat', '#', ['get', 'route_color']]
  for (const b of BANDS) {
    for (const [kind, off] of KINDS) {
      const id = `ribbon-${b.key}-${kind}`
      map.addLayer({
        id,
        type: 'line',
        source: `build-${b.key}`,
        minzoom: b.min === 0 ? 0 : b.min,
        maxzoom: b.max === 24 ? 24 : b.max,
        filter: ['all', ['==', ['get', 'band_min'], b.key], ['==', ['get', 'kind'], kind]],
        // round caps: at a transition/steady seam the eased line arrives
        // with lateral slope while the steady leaves flat, and butt caps
        // cut at those two angles leave a wedge notch at every seam
        layout: { 'line-cap': 'round', 'line-join': 'round' },
        paint: { 'line-color': COLOR, 'line-width': widthExpr(w), 'line-opacity': o, 'line-offset': off },
      })
      ribbonIds.push(id)
      map.on('click', id, (e: any) => {
        inspect.value = e.features?.[0]?.properties ?? null
      })
      map.on('mouseenter', id, () => (map.getCanvas().style.cursor = 'pointer'))
      map.on('mouseleave', id, () => (map.getCanvas().style.cursor = ''))
    }
  }

  // ── stations: markers then labels, above every ribbon ────────────────
  // Density is gated by rank per zoom (a top-level step on zoom — the
  // only place a filter may read zoom, so the ftype test rides INSIDE
  // each branch), and the LABEL economy is MapLibre's: symbol collision
  // never overlaps, symbol-sort-key places hubs first so locals are what
  // drop, variable anchors let a label take whichever side has room.
  const gate = (cond: any, base: number[]) =>
    ['step', ['zoom'],
      ['all', cond, ['>=', ['get', 'rank'], base[0]]],
      11, ['all', cond, ['>=', ['get', 'rank'], base[1]]],
      12, ['all', cond, ['>=', ['get', 'rank'], base[2]]],
      13, ['all', cond, ['>=', ['get', 'rank'], base[3]]],
      14, cond] as any
  const isMarker = ['==', ['get', 'ftype'], 'marker']
  const isStation = ['==', ['get', 'ftype'], 'station']
  map.addLayer({
    id: 'station-markers', type: 'symbol', source: 'stations', minzoom: 10,
    filter: gate(isMarker, [10, 6, 4, 2]),
    layout: {
      // the icon id is precomputed per feature (dot-<hex>-<off> or
      // pill-<span>) and DRAWN on demand by styleimagemissing. A dot's
      // slot offset is baked into its image so icon-rotate carries it to
      // the correct side of the corridor; icon-size then scales image
      // AND offset together, exactly matching zoomScaledOffset.
      'icon-image': ['get', 'icon'],
      'icon-size': ['interpolate', ['linear'], ['zoom'], 11, 0.5, 14, 1],
      'icon-rotate': ['get', 'bearing'],
      'icon-rotation-alignment': 'map',
      'icon-allow-overlap': true,
      'icon-ignore-placement': true,
    },
  })
  const rankBump = ['case', ['>=', ['get', 'rank'], 8], 2.5, ['>=', ['get', 'rank'], 4], 1, 0]
  // the merged complex label yields to per-corridor labels at z15 —
  // stations with one marker keep their label at every zoom (coalesce:
  // builds predating nmarkers read as solo)
  const rk = ['get', 'rank']
  // the strip sits just under the name, so its offset tracks the same
  // rank tiers text-size uses — a fixed offset reads as a hole under
  // small labels while looking right under hub-sized ones
  const off = (a: number, b: number, c: number) =>
    ['case', ['>=', rk, 8], ['literal', [0, c]], ['>=', rk, 4], ['literal', [0, b]], ['literal', [0, a]]]
  const bulletOffset = ['match', ['get', 'nrows'],
    2, off(36, 39, 43), 3, off(50, 54, 60), off(21, 23, 26)] as any
  const labelGate = ['step', ['zoom'],
    ['all', isStation, ['>=', rk, 10]],
    11, ['all', isStation, ['>=', rk, 6]],
    12, ['all', isStation, ['>=', rk, 4]],
    13, ['all', isStation, ['>=', rk, 2]],
    14, isStation,
    15, ['all', isStation, ['<', ['coalesce', ['get', 'nmarkers'], 1], 2]]] as any
  map.addLayer({
    id: 'station-labels', type: 'symbol', source: 'stations', minzoom: 11,
    filter: labelGate,
    layout: {
      'text-field': ['get', 'name'],
      'text-font': ['Montserrat Medium'],
      'symbol-sort-key': ['*', -1, ['get', 'rank']],
      // fixed top anchor: name under the marker, the bullet strip under
      // the name. (Variable anchors are off for now — the icon does not
      // follow the text's variable anchor, so the strip would detach.)
      'text-anchor': 'top',
      'text-offset': [0, 0.5],
      // rank bump INSIDE the zoom stops: ["zoom"] is only legal as input
      // to a top-level interpolate/step, so the composite goes this way
      // around (same shape as zoomScaledOffset)
      'text-size': ['interpolate', ['linear'], ['zoom'],
        11, ['+', 10, rankBump], 16, ['+', 13, rankBump]],
      // the bullet strip appears once there is room for it; its distance
      // below the anchor follows the name's estimated wrap count
      'icon-image': ['step', ['zoom'], '', 13.5, ['coalesce', ['get', 'brow'], '']],
      'icon-anchor': 'top',
      'icon-offset': bulletOffset,
      'icon-optional': true,
    },
    paint: {
      'text-color': '#e8e8ee',
      'text-halo-color': 'rgba(12,12,16,0.9)',
      'text-halo-width': 1.4,
    },
  })
  // per-corridor labels for complexes at z15+: this corridor's name and
  // ITS bullets (Fulton St splits into A·C / J·Z / 2·3 / 4·5 labels the
  // way Apple draws it; the merged label above takes over below z15)
  map.addLayer({
    id: 'station-labels-hi', type: 'symbol', source: 'stations', minzoom: 15,
    filter: ['all', isMarker, ['>=', ['coalesce', ['get', 'nmarkers'], 1], 2]],
    layout: {
      'text-field': ['get', 'name'],
      'text-font': ['Montserrat Medium'],
      'symbol-sort-key': ['*', -1, ['get', 'rank']],
      'text-anchor': 'top',
      'text-offset': [0, 0.5],
      'text-size': ['interpolate', ['linear'], ['zoom'],
        11, ['+', 10, rankBump], 16, ['+', 13, rankBump]],
      'icon-image': ['coalesce', ['get', 'brow'], ''],
      'icon-anchor': 'top',
      'icon-offset': bulletOffset,
      'icon-optional': true,
    },
    paint: {
      'text-color': '#e8e8ee',
      'text-halo-color': 'rgba(12,12,16,0.9)',
      'text-halo-width': 1.4,
    },
  })
  for (const id of ['station-markers', 'station-labels', 'station-labels-hi']) {
    map.on('click', id, (e: any) => {
      inspect.value = e.features?.[0]?.properties ?? null
    })
    map.on('mouseenter', id, () => (map.getCanvas().style.cursor = 'pointer'))
    map.on('mouseleave', id, () => (map.getCanvas().style.cursor = ''))
  }

  map.addLayer({
    id: 'dbg-rail', type: 'line', source: 'rail',
    paint: { 'line-color': '#888', 'line-width': 1, 'line-opacity': 0.5 },
    layout: { visibility: 'none' },
  })
  map.addLayer({
    id: 'dbg-paths', type: 'line', source: 'paths',
    paint: { 'line-color': '#0af', 'line-width': 1.2, 'line-opacity': 0.7 },
    layout: { visibility: 'none' },
  })
  map.addLayer({
    id: 'dbg-trackcenter', type: 'line', source: 'trackcenter',
    paint: { 'line-color': '#f0a', 'line-width': 1.2, 'line-opacity': 0.8 },
    layout: { visibility: 'none' },
  })
  map.addLayer({
    id: 'dbg-nodes', type: 'circle', source: 'nodes',
    paint: {
      'circle-radius': ['+', 2, ['get', 'degree']], 'circle-color': '#e33',
      'circle-opacity': 0.8, 'circle-stroke-color': '#fff', 'circle-stroke-width': 1,
    },
    layout: { visibility: 'none' },
  })
}

watch(debug, (d) => {
  if (!map) return
  for (const [k, on] of Object.entries(d)) {
    const id = `dbg-${k}`
    if (map.getLayer(id)) map.setLayoutProperty(id, 'visibility', on ? 'visible' : 'none')
  }
}, { deep: true })

async function reload() {
  if (!map || !feed.value) return
  loading.value = true
  try {
    styleSet.value = await api.style(feed.value)
    loadStations() // a rebuild may have refreshed the stations artifact
    loadedBands.clear()
    bandRaw.clear()
    for (const b of BANDS) {
      map.getSource(`build-${b.key}`)?.setData({ type: 'FeatureCollection', features: [] })
    }
    await ensureBand()
    const { w, o } = modeExprs(styleSet.value)
    for (const id of ribbonIds) {
      if (!map.getLayer(id)) continue
      map.setPaintProperty(id, 'line-width', widthExpr(w))
      map.setPaintProperty(id, 'line-opacity', o)
    }
    for (const [name, url] of [
      ['rail', `/api/rail.geojson?feed=${feed.value}`],
      ['paths', `/api/paths.geojson?feed=${feed.value}`],
      ['trackcenter', `/api/trackcenter.geojson?feed=${feed.value}`],
      ['nodes', `/api/nodes.geojson?feed=${feed.value}`],
    ] as const) {
      map.getSource(name)?.setData(url)
    }
    fitCity()
  } finally {
    loading.value = false
  }
}

function fitCity() {
  const b = currentCity.value?.bbox
  if (map && b?.length === 4) {
    map.fitBounds([[b[0], b[1]], [b[2], b[3]]], { padding: 40, duration: 0 })
  }
}

async function loadScenarios() {
  scenarios.value = []
  if (!feed.value) return
  try {
    const r = await api.scenarios(feed.value)
    if (r.available && r.scenarios) scenarios.value = r.scenarios
    grid.value = r.grid ?? []
  } catch {
    /* not every feed has a usable calendar */
  }
}

onMounted(async () => {
  if (typeof maplibregl === 'undefined') {
    toast({
      title: 'MapLibre fork not loaded',
      description: 'The server serves it at /vendor/maplibre-gl.js — check the --maplibre dist path.',
      variant: 'error',
    })
    loading.value = false
    return
  }
  map = new maplibregl.Map({
    container: el.value!,
    center: [-73.98, 40.75],
    zoom: 12,
    attributionControl: { compact: true },
    preserveDrawingBuffer: true,
    style: {
      version: 8,
      // labels are text: the style needs a glyph endpoint (ribbons never
      // did). CARTO's fonts pair with the CARTO raster basemap.
      glyphs: 'https://tiles.basemaps.cartocdn.com/fonts/{fontstack}/{range}.pbf',
      sources: {
        osm: {
          type: 'raster',
          tiles: ['https://a.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}@2x.png'],
          tileSize: 256,
          attribution: '© OpenStreetMap © CARTO',
        },
      },
      layers: [{ id: 'osm', type: 'raster', source: 'osm', paint: { 'raster-opacity': 0.55 } }],
    },
  })
  map.addControl(new maplibregl.NavigationControl({ showCompass: false }), 'bottom-right')
  // marker dots, bundle pills and route bullets are canvas-drawn the
  // first time a layer asks for them — any city's colors and labels work
  // with no sprite sheet
  map.on('styleimagemissing', (e: any) => {
    if (!e.id || map.hasImage(e.id)) return
    const image = drawMarkerImage(e.id)
    if (image) map.addImage(e.id, image, { pixelRatio: 2 })
  })
  ;(window as any).__map = map // debugging handle; the map is the whole page
  map.on('error', (e: any) => console.error('map error', e?.error?.message || e))
  // MapLibre measures the container once at construction and falls back to
  // 400x300 if it reads zero — which it does here, because the pane is
  // still being laid out on mount. Watch the box instead of guessing.
  ro = new ResizeObserver(() => map?.resize())
  ro.observe(el.value!)
  map.on('load', async () => {
    const empty = { type: 'FeatureCollection', features: [] }
    // one source per zoom band, filled on demand: FAIR emits a complete
    // copy of the map per band and only one is ever visible, so loading
    // all four means four times the bytes for nothing.
    for (const b of BANDS) {
      map.addSource(`build-${b.key}`, { type: 'geojson', data: empty, lineMetrics: true })
    }
    for (const n of ['rail', 'paths', 'trackcenter', 'nodes', 'stations']) {
      map.addSource(n, { type: 'geojson', data: empty })
    }
    styleSet.value = feed.value ? await api.style(feed.value).catch(() => null) : null
    addLayers()
    applyStations() // the fetch may have finished before WebGL did
    // crossing a band boundary pulls that band in the first time
    map.on('zoomend', ensureBand)
    await reload()
  })
  // the scenario list does not wait on the map: it is a plain API call,
  // and burying it in the load handler meant the picker stayed empty
  // whenever WebGL was slow or unavailable
  loadClassesOff()
  loadScenarios()
  loadMasks()
  loadStations()
  prefetchModes()
})

onBeforeUnmount(() => {
  ro?.disconnect()
  map?.remove()
})

watch(feed, async () => {
  loadClassesOff()
  loadMasks()
  loadStations()
  prefetchModes()
  await loadScenarios()
  await reload()
})
</script>

<template>
  <div class="relative h-full">
    <!-- sized, not positioned: maplibre-gl.css sets .maplibregl-map to
         position:relative, which beats an `absolute inset-0` here and
         collapses the container to zero height. -->
    <div ref="el" class="h-full w-full" />

    <!-- one toolbar: the map is the page, so all chrome lives in a single
         bar — city and view actions, then time, then a status word. The
         scenario dropdown is gone: a timestamp IS the scenario selection
         now (dynamic rendering), and the prebuilt-scenario QA controls
         live on the Service page. -->
    <div class="pointer-events-none absolute inset-x-0 top-0 z-10 flex justify-center p-4">
      <div class="pointer-events-auto flex max-w-full flex-wrap items-center gap-1 rounded-xl border border-border bg-card/90 px-2 py-1.5 shadow-sm backdrop-blur">
        <span class="px-2 text-sm font-medium">{{ currentCity?.name || 'No city' }}</span>
        <Badge v-if="loading" variant="info"><Spinner class="size-3" /></Badge>
        <Button variant="ghost" size="icon" title="Reload" @click="reload"><RefreshCw class="size-4" /></Button>
        <Button variant="ghost" size="icon" title="Fit to city" @click="fitCity"><Crosshair class="size-4" /></Button>

        <span class="mx-1 h-5 w-px bg-border" />

        <Clock class="ml-1 size-4 shrink-0 text-muted-foreground" />
        <input
          v-model="when"
          type="datetime-local"
          class="h-8 rounded-md border border-input bg-transparent px-2 text-sm [color-scheme:dark]"
        />
        <Button variant="ghost" size="sm" title="Jump to the current time" @click="when = localNow()">now</Button>

        <span class="mx-1 h-5 w-px bg-border" />

        <button
          v-if="when"
          class="rounded-md px-2 py-1 text-xs transition-colors hover:bg-accent"
          :title="(resolved?.label ? resolved.label + ' — ' : '') + 'click to show all service'"
          @click="when = ''"
        >
          <template v-if="runningCount && runningCount.on > 0">
            <span class="font-medium tabular-nums text-[var(--success)]">{{ runningCount.on }}</span>
            <span class="text-muted-foreground"> / {{ runningCount.total }} routes</span>
          </template>
          <span v-else-if="runningCount" class="text-[var(--warning)]">no service</span>
          <span v-else class="text-muted-foreground">…</span>
        </button>
        <span v-else class="px-2 text-xs text-muted-foreground" title="Every pattern that ever runs — set a time to narrow it">
          all service
        </span>
      </div>
    </div>

    <div class="pointer-events-auto absolute bottom-4 left-4 z-10 w-56 rounded-xl border border-border bg-card/90 p-3 shadow-sm backdrop-blur">
      <div class="mb-2 flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        <Layers class="size-3.5" /> Layers
      </div>
      <label
        v-for="m in CLASS_ORDER"
        :key="m"
        class="flex items-center justify-between py-1 text-sm"
        :class="inBuild(m) ? '' : 'opacity-45'"
        :title="inBuild(m) ? '' : 'No ' + m + ' routes in this build'"
      >
        <span class="flex items-center gap-2">
          <span class="size-2.5 shrink-0 rounded-full" :style="classDot(m)" />
          <span class="capitalize">{{ m }}</span>
        </span>
        <Switch :model-value="!classesOff.has(m)" @update:model-value="(v) => toggleClass(m, v)" />
      </label>
      <div class="mb-1 mt-3 text-[10px] font-medium uppercase tracking-wider text-muted-foreground/70">Debug</div>
      <label v-for="(_, k) in debug" :key="k" class="flex items-center justify-between py-1 text-sm">
        <span class="capitalize">{{ k }}</span>
        <Switch :model-value="debug[k]" @update:model-value="(v) => (debug[k] = v)" />
      </label>
    </div>

    <div
      v-if="inspect"
      class="pointer-events-auto absolute bottom-4 right-4 z-10 w-72 rounded-xl border border-border bg-card/95 p-3 shadow-lg backdrop-blur"
    >
      <div class="mb-2 flex items-center justify-between">
        <span class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Segment</span>
        <button class="text-xs text-muted-foreground hover:text-foreground" @click="inspect = null">close</button>
      </div>
      <div class="flex items-center gap-2 pb-2">
        <span class="size-4 rounded border border-border" :style="{ background: `#${inspect.route_color}` }" />
        <span class="text-sm font-medium">{{ inspect.label }}</span>
        <Badge variant="muted" class="text-[10px] capitalize">{{ inspect.mode }}</Badge>
      </div>
      <div class="space-y-1 text-xs">
        <div v-for="k in ['routes', 'kind', 'slot', 'nslots', 'offset_px', 'len_m', 'band_min', 'band_max']" :key="k"
             class="flex justify-between gap-4 border-b border-border/60 py-1 last:border-0">
          <span class="text-muted-foreground">{{ k }}</span>
          <span class="truncate font-mono tabular-nums">{{ inspect[k] }}</span>
        </div>
      </div>
    </div>
  </div>
</template>
