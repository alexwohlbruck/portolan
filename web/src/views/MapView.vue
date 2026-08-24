<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, watch, computed } from 'vue'
import { Layers, Crosshair, RefreshCw, Clock, Copy, Check, X } from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Switch from '@/components/ui/Switch.vue'
import Spinner from '@/components/ui/Spinner.vue'
import Select from '@/components/ui/Select.vue'
import { api, fetchBuild, type Scenario, type StyleSet } from '@/lib/api'
import { applyDynamic, activePredicate, activeRouteIdx, bulletIdsOf, markerIconAt, maskActive, stationVisible, type BundleRow } from '@/lib/dynamic'
import { feed, currentFeed, isGlobal, run } from '@/lib/store'
import { basemap, BASEMAPS, BASEMAP_PROVIDERS, currentBasemap, isDark, PROVIDER_ORDER, providerTiles } from '@/lib/theme'
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

// ── theme (lib/theme.ts) ───────────────────────────────────────────────
// WebGL inherits no CSS: the basemap and every label colour are switched
// by hand when the theme flips.
// CARTO's light tiles are already pale enough to sit under the ribbons at
// full strength; dimming them the way the dark tiles need washes the
// street grid out entirely.
const basemapOptions = BASEMAPS.map((b) => ({ value: b.id, label: b.label }))
const rasterOpacity = () => (isDark.value ? 0.55 : 1)
// A base recedes so the drawn network reads on top of it; a transparent
// overlay must not, or it fades to nothing.
const providerOpacity = (id: any) => (BASEMAP_PROVIDERS[id as 'carto'].fade ? rasterOpacity() : 1)

// Both the theme and the basemap pick land here: tiles follow the theme,
// visibility follows the basemap, and neither needs the style rebuilt.
function applyBasemap() {
  if (!map) return
  const show = currentBasemap().show
  for (const id of PROVIDER_ORDER) {
    if (!map.getLayer(`bm-${id}`)) continue
    map.getSource(`bm-${id}`)?.setTiles([providerTiles(id)])
    map.setLayoutProperty(`bm-${id}`, 'visibility', show.includes(id) ? 'visible' : 'none')
    map.setPaintProperty(`bm-${id}`, 'raster-opacity', providerOpacity(id))
  }
}
watch(basemap, applyBasemap)
// Station names are drawn OVER coloured ribbons in both themes, so the
// halo does the separating work and has to invert with the basemap; the
// text just needs enough contrast against its own halo.
const LABEL = {
  dark: { color: '#e8e8ee', halo: 'rgba(12,12,16,0.9)' },
  light: { color: '#1b1b22', halo: 'rgba(255,255,255,0.92)' },
}
const labelPaint = () => {
  const t = isDark.value ? LABEL.dark : LABEL.light
  return { 'text-color': t.color, 'text-halo-color': t.halo, 'text-halo-width': 1.4 }
}
// tile mode draws labels through these same layers (hydrateSymbols), so
// this fixed set is the whole re-theme surface in both modes
const LABEL_LAYERS = ['station-labels', 'station-labels-hi']

// ── viewport readout ──────────────────────────────────────────────────
// Where am I, and at what zoom? Every tuning conversation about label and
// dot density needs both, and reading them out of the URL bar is not a
// thing here — the map owns its own camera. Click to copy "lat,lon,zoom"
// so a problem area can be pasted straight back.
const view = ref({ lon: 0, lat: 0, zoom: 0, bearing: 0, pitch: 0 })
const viewCopied = ref(false)
const viewText = computed(
  () => `${view.value.lat.toFixed(5)},${view.value.lon.toFixed(5)},${view.value.zoom.toFixed(2)}`,
)
async function copyView() {
  try {
    await navigator.clipboard.writeText(viewText.value)
  } catch {
    // clipboard is permission-gated; a selectable fallback beats failing
    const ta = document.createElement('textarea')
    ta.value = viewText.value
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    ta.remove()
  }
  viewCopied.value = true
  setTimeout(() => (viewCopied.value = false), 1200)
}
function syncView() {
  if (!map) return
  const c = map.getCenter()
  view.value = {
    lon: c.lng, lat: c.lat, zoom: map.getZoom(),
    bearing: map.getBearing(), pitch: map.getPitch(),
  }
}

// The marker images (dots, pills, bullets) are NOT re-themed: every one
// of them sits on top of a route's own colour, so they read the same
// against either basemap — and their ids are content-addressed, so a
// theme-dependent fill would need every cached image evicted.
watch(isDark, () => {
  if (!map) return
  applyBasemap()
  const p = labelPaint()
  if (map.getLayer('cat-text')) {
    map.setPaintProperty('cat-text', 'text-halo-color',
      isDark.value ? 'rgba(12,12,16,0.92)' : 'rgba(255,255,255,0.95)')
  }
  for (const id of LABEL_LAYERS) {
    if (!map.getLayer(id)) continue
    for (const [k, v] of Object.entries(p)) map.setPaintProperty(id, k, v)
  }
  if (map.getLayer('dbg-nodes')) map.setPaintProperty('dbg-nodes', 'circle-stroke-color', nodeStroke())
})
// debug node halo: the one debug layer whose colour is a contrast choice
// rather than an identity
const nodeStroke = () => (isDark.value ? '#fff' : '#1b1b22')

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
// each layer's STRUCTURAL filter (band_min/kind/ftype), recorded at
// creation: the tile-mode time filter combines with it via ['all', …]
// and detaches by restoring exactly this, so recombination is lossless
const structuralFilter = new Map<string, any>()
const debug = ref({ paths: false, trackcenter: false, nodes: false, rail: false, yards: false })

// The layer picker starts folded to a single button: expanded it covers a
// third of a phone screen, and the toggles are an occasional tool, not
// something worth paying map area for on every visit.
const layersOpen = ref(false)

// per-class visibility. Stored as the DISABLED set so the default —
// everything on — is an empty set and new classes appearing in a build
// are visible without migration. Hiding a class routes through the same
// dynamic filter as time, so surviving bundles re-center instead of
// keeping a gap where the hidden class sat. Persisted per feed.
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
  // tile mode never materializes bands, so there is nothing to prefetch —
  // and global has no whole-band documents at all
  if (!feed.value || isGlobal.value || tileMode.value || bandRaw.has(15)) return
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
  // tile mode materializes no bands, so the class list comes from the
  // hydrated symbols' aligned modes instead
  if (tileMode.value && stationsRaw.value?.features) {
    for (const f of stationsRaw.value.features) {
      for (const m of String(f.properties?.modes ?? '').split(',')) if (m) seen.add(m)
    }
  }
  modesPresent.value = CLASS_ORDER.filter((m) => seen.has(m))
}
// every class gets a row, always: the panel is the taxonomy, not a
// summary of this build. Absent classes render dimmed — their toggle
// still works and persists, so switching a class off in one feed carries
// to a feed that has it.
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
  const count = (m: Record<string, string>) => {
    const ids = Object.keys(m)
    return { on: ids.filter((r) => maskActive(m[r], day, hour)).length, total: ids.length }
  }
  if (isGlobal.value) {
    // summed PER FEED, never merged into one map — route ids collide
    // across feeds (every metro has a "1"), and a merge would silently
    // drop the collisions from both counts
    let on = 0
    let total = 0
    for (const m of Object.values(globalMasks.value)) {
      const c = count(m)
      on += c.on
      total += c.total
    }
    return total ? { on, total } : null
  }
  if (!Object.keys(masks.value).length) return null
  return count(masks.value)
})

// Global time semantics: acts are FEED-LOCAL time, so one hour-of-week
// applied across every region means "each network at its own local
// 9 PM" — the intended reading for a global service view (there is no
// shared wall clock worth rendering).
//
// per-feed activity masks for the global sum, keyed by feed and never
// merged; the fetch is cached — activity is static per feed
const globalMasks = ref<Record<string, Record<string, string>>>({})
const activityCache = new Map<string, Record<string, string>>()

async function loadMasks() {
  if (isGlobal.value) {
    // symbol gating must NOT see per-feed masks here: route ids collide
    // across feeds, and stationVisible/activePredicate treat a missing
    // route-level mask as always-active — the safe global semantics
    // (feature-level acts still dominate wherever they exist)
    masks.value = {}
    await tilesProbe // the region list names the feeds worth summing
    if (!isGlobal.value) return // switched away while the probe ran
    const out: Record<string, Record<string, string>> = {}
    await Promise.all(
      tileRegions.value.map(async (r) => {
        let m = activityCache.get(r.feed)
        if (!m) {
          m = await api.activity(r.feed).catch(() => ({}))
          activityCache.set(r.feed, m)
        }
        out[r.feed] = m
      }),
    )
    globalMasks.value = out
    return
  }
  globalMasks.value = {}
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

// Hydrated transition/bridge features may carry per-feed RESOLVED
// width/opacity (_w/_o, baked in hydrateTransitions from the owning
// feed's styleSet): the ribbon twins are shared across every feed in
// global, so a per-layer expression cannot split by feed there — the
// value rides on the feature instead. GeoJSON-mode features carry
// neither and fall through to the class match untouched.
const perFeedW = (w: any) => ['coalesce', ['get', '_w'], w]
const perFeedO = (o: any) => ['coalesce', ['get', '_o'], o]

const ribbonIds: string[] = []
// which ribbon layers belong to which band — needed to stretch a loaded
// band over a zoom whose own band has not arrived yet (see holdBands).
const bandLayers = new Map<number, string[]>()

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

async function loadBand(key: number) {
  // never with feed=global: there is no whole-band document for the world
  if (!map || !feed.value || isGlobal.value || loadedBands.has(key)) return
  loadedBands.add(key)
  try {
    const { data, stats } = await fetchBuild(feed.value, key)
    bandRaw.set(key, data)
    refreshModes()
    applyBand(key)
    transferred.value = stats
    holdBands()
  } catch {
    loadedBands.delete(key) // let a later zoom retry
  }
}

async function ensureBand() {
  // the pyramid carries every band itself; global only ever draws pyramids
  if (!map || tileMode.value || isGlobal.value) return
  const key = bandForZoom(map.getZoom())
  holdBands()
  await loadBand(key)
  // Prefetch the neighbours. Crossing a band boundary used to blank the
  // map until the next band's GeoJSON arrived — the ribbons vanished for
  // a beat on every step of a zoom, which across several boundaries
  // reads as the map flashing. Fetching ahead means the data is almost
  // always already there.
  const i = BANDS.findIndex((b) => b.key === key)
  for (const n of [BANDS[i - 1], BANDS[i + 1]]) {
    if (n) loadBand(n.key)
  }
}

/** Keep SOMETHING drawn at every zoom. A band whose data has not arrived
 *  yet would otherwise leave its zoom range empty, so the nearest loaded
 *  band is stretched to cover it and snaps back the moment the real one
 *  lands. Only one band ever covers a given zoom, so nothing double-draws. */
let heldAs = ''
function holdBands() {
  if (!map || tileMode.value) return // no bands to stretch over each other
  const z = map.getZoom()
  const want = bandForZoom(z)
  const have = bandRaw.has(want)
  // setLayerZoomRange re-lays out the layer, so only touch it when the
  // decision actually changes — this runs on every frame of a zoom.
  const sig = `${want}|${have}|${have ? '' : Math.round(z)}|${[...bandRaw.keys()].sort().join(',')}`
  if (sig === heldAs) return
  heldAs = sig
  for (const b of BANDS) {
    const ids = bandLayers.get(b.key)
    if (!ids) continue
    let lo = b.min === 0 ? 0 : b.min
    let hi = b.max === 24 ? 24 : b.max
    if (!have && bandRaw.has(b.key)) {
      // nearest loaded band by key distance takes the orphaned zoom
      const nearest = BANDS.filter((x) => bandRaw.has(x.key)).sort(
        (x, y) => Math.abs(x.key - want) - Math.abs(y.key - want),
      )[0]
      if (nearest && nearest.key === b.key) {
        lo = Math.min(lo, Math.floor(z))
        hi = Math.max(hi, Math.ceil(z) + 1)
      }
    }
    for (const id of ids) {
      if (map.getLayer(id)) map.setLayerZoomRange(id, lo, hi)
    }
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
  const out = pred ? applyDynamic(raw, pred) : raw
  map.getSource(`build-${key}`)?.setData(out)
  // Record which routes actually SURVIVED into the drawn output. A
  // station's own activity mask is not enough to decide whether to draw
  // its dot: the two masks are computed from different evidence (stop
  // membership vs the drawn segment) and they disagree — at Tue 02:00
  // the NYC feed leaves 44 L stations "active" while the L has no live
  // segment anywhere, which drew a string of dots along an invisible
  // line. What the rider sees has to agree with itself, so a station is
  // only drawn when one of its lines is.
  if (pred) {
    const live = new Set<string>()
    for (const f of out.features as any[]) {
      for (const r of String(f.properties.routes ?? '').split(',')) {
        if (r) live.add(r)
      }
    }
    liveRoutes.set(key, live)
  } else {
    liveRoutes.delete(key)
  }
  if (key === 15) {
    // markers re-derive their icons against this band's bundles
    markerBundles = null
  }
  applyStations()
}

// routes with at least one drawn segment, per band; empty when no filter
// is active (everything is drawn, so nothing needs suppressing).
const liveRoutes = new Map<number, Set<string>>()

/** The live-route set for the band on screen. Falls back to the union of
 *  every loaded band: while a band is still fetching, suppressing on a
 *  set that does not know about it yet would blink stations out. */
function liveNow(): Set<string> | null {
  if (!map || !liveRoutes.size) return null
  const key = bandForZoom(map.getZoom())
  const own = liveRoutes.get(key)
  if (own) return own
  const all = new Set<string>()
  for (const s of liveRoutes.values()) for (const r of s) all.add(r)
  return all.size ? all : null
}

function applyBands() {
  for (const key of bandRaw.keys()) applyBand(key)
}

// ── stations (docs/STOP-LABELS.md) ─────────────────────────────────────
// Points with per-route metadata; fetched independently of WebGL (the
// map applies them whenever both are ready). Same dynamic rule as
// ribbons: time and class toggles hide, via stationVisible.
const stationsRaw = ref<any | null>(null)

// how many lines a name wraps to: simulate the wrap instead of counting
// characters (length/20 called "Bedford-Nostrand Avs" one line and the
// bullets overlapped the wrapped text). This mirrors MapLibre's shaping
// rather than approximating it, because BOTH directions of error show:
// undercount and the strip lands ON a wrapped line, overcount and it
// floats in a hole below the name.
//
// Two things the greedy word-packer that lived here got wrong:
//
//   * its break set was whitespace + hyphen, but MapLibre breaks on a
//     solidus, ampersand, plus, parens and the dashes too — so
//     "Washington/Wabash" shaped to two lines while this said one, and
//     the Brown/Green/Orange/Pink strip drew across "Wabash".
//   * greedy packing is not what MapLibre does. determineLineBreaks
//     picks the LEAST-BAD breaks against a target width of
//     totalWidth / ceil(totalWidth / maxWidth), so it fills lines evenly
//     — three 0.6 em-wide tokens are two balanced lines there and three
//     greedy ones here.
//
// So take MapLibre's own line count: ceil(total advance / max width),
// bounded by how many break opportunities the name actually offers (an
// unbreakable name overruns its max width on one line rather than
// splitting mid-word).
const BREAKABLE = new Set([
  0x0a, 0x20, 0x26, 0x29, 0x2b, 0x2d, 0x2f, 0xad, 0xb7, 0x200b, 0x2010, 0x2013, 0x2027,
])
const BREAKABLE_BEFORE = new Set([0x28]) // a break may precede "("
const MAX_ROWS = 4 // as far as bulletOffset's table reaches
let measureCtx: CanvasRenderingContext2D | null = null
function estRows(name: string): number {
  if (!measureCtx) {
    measureCtx = document.createElement('canvas').getContext('2d')!
    measureCtx.font = '500 100px Montserrat, system-ui, sans-serif'
  }
  const maxW = 10 * 100 // text-max-width, 10 em, measured at 1 em = 100 px
  const text = name.trim()
  if (!text) return 1
  let breaks = 0
  for (let i = 0; i < text.length - 1; i++) {
    if (BREAKABLE.has(text.charCodeAt(i)) || BREAKABLE_BEFORE.has(text.charCodeAt(i + 1))) breaks++
  }
  // MapLibre sums every glyph's advance, spaces included, then divides
  const rows = Math.ceil(measureCtx.measureText(text).width / maxW)
  return Math.max(1, Math.min(MAX_ROWS, rows, breaks + 1))
}

/** Normalize a stations FeatureCollection and make it the live symbol
 *  data. The input is either the per-feed stations.geojson artifact or
 *  the same features hydrated back out of tile symbols — the tiler cuts
 *  its symbol layers from that artifact, so the properties are one
 *  vocabulary. Everything downstream (applyStations, time gating, class
 *  toggles, styleimagemissing icons) runs identically in both modes
 *  from here. */
function prepareStations(fc: any | null) {
  if (fc?.features) {
    for (const f of fc.features) {
      const p = f.properties
      if (p.ftype === 'cat') {
        // caterpillar bullets: normalize singular route/mode into the
        // aligned-array props so stationVisible and the class toggles
        // treat a bullet exactly like a one-route station
        p.routes = p.route
        p.modes = p.mode
        continue
      }
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
  markerBundles = null // fresh stations — bundle links rebuild lazily
  refreshModes() // tile mode derives the class list from the symbols
  applyStations()
}

async function loadStations() {
  // stations.geojson is a per-feed artifact — feed=global must not fire
  const fc =
    feed.value && !isGlobal.value ? await api.stations(feed.value).catch(() => null) : null
  prepareStations(fc)
}

// ── marker + bullet images, drawn on demand ────────────────────────────
// All images render at 2× (pixelRatio 2). Sizes below are CSS px at full
// zoom (z14+, where the slot pitch is its full 6 px).
const DOT_D = 7 // dot diameter; also the pill height and its corner radius ×2

// one bullet as a canvas: MTA-style circle for 1–2 char labels, a
// rounded-corner word pill (the Chicago 'Red'/'Brown' shape) for longer
// Bullet OUTLINES. Systems brand these and the feed says nothing about
// them, so the shape is curation (style docs, `shape:` on a route or
// agency) with circle as the default. The set is what transit systems
// actually use:
//
//   circle    NYC, WMATA, Boston, Seoul, Tokyo Metro — the default
//   square    Vienna (its U1 badge is a 283x283 square)
//   rounded   Barcelona TMB, Amsterdam metro, Santiago, Berlin BVG, Paris
//   notch     square with the top-right corner rounded — Mexico City
//   diamond   NYC EXPRESS variants (6◇, 7◇), Madrid's rhombus logo
//   hexagon   several Japanese private railways
//   octagon   a few Chinese systems
//   triangle  rare, and only ever for special services
//
// Berlin is the caveat: its badge is a rounded RECTANGLE (151x90), wider
// than tall, and `rounded` renders 1:1 for one- and two-character labels.
// The corner treatment is right, the proportion is not.
//
// Non-circular outlines need a wider box to hold the same glyphs — a
// diamond's inscribed rectangle is barely half its width — so each shape
// declares how much it has to grow.
const SHAPE_PAD: Record<string, number> = {
  circle: 1, square: 1, rounded: 1, notch: 1,
  hexagon: 1.18, octagon: 1.06, diamond: 1.42, triangle: 1.6,
}

function shapePath(ctx: CanvasRenderingContext2D, shape: string, w: number, h: number) {
  const cx = w / 2
  const cy = h / 2
  const poly = (pts: [number, number][]) => {
    ctx.moveTo(pts[0][0], pts[0][1])
    for (let i = 1; i < pts.length; i++) ctx.lineTo(pts[i][0], pts[i][1])
    ctx.closePath()
  }
  switch (shape) {
    case 'square':
      ctx.rect(0, 0, w, h)
      return
    case 'rounded':
      ctx.roundRect(0, 0, w, h, Math.min(4, h / 3))
      return
    case 'notch':
      // three square corners and the TOP-RIGHT rounded — Mexico City's
      // house style. Radii run [top-left, top-right, bottom-right,
      // bottom-left].
      ctx.roundRect(0, 0, w, h, [0, Math.min(6, h / 2), 0, 0])
      return
    case 'diamond':
      poly([[cx, 0], [w, cy], [cx, h], [0, cy]])
      return
    case 'triangle':
      poly([[cx, 0], [w, h], [0, h]])
      return
    case 'hexagon': {
      const i = w * 0.25
      poly([[i, 0], [w - i, 0], [w, cy], [w - i, h], [i, h], [0, cy]])
      return
    }
    case 'octagon': {
      const i = Math.min(w, h) * 0.29
      poly([[i, 0], [w - i, 0], [w, i], [w, h - i], [w - i, h], [i, h], [0, h - i], [0, i]])
      return
    }
    default:
      ctx.arc(cx, cy, Math.min(w, h) / 2, 0, Math.PI * 2)
  }
}

function bulletCanvas(id: string): HTMLCanvasElement | null {
  const m = id.match(/^blt-([0-9a-fA-F]{6})-([a-z]*)-(.+)$/)
  if (!m) return null
  const hex = m[1]
  const shape = m[2] || 'circle'
  const label = m[3]
  const h = 14
  const cv = document.createElement('canvas')
  cv.width = 2
  cv.height = 2
  let ctx = cv.getContext('2d')!
  ctx.font = '600 9.5px system-ui, sans-serif'
  const tw = ctx.measureText(label).width
  // 1:1 for one or two glyphs, a pill once it is a word — and whatever
  // the outline needs on top of that
  const compact = label.length <= 2
  const pad = SHAPE_PAD[shape] ?? 1
  const w = Math.ceil((compact ? h : Math.ceil(tw) + 9) * pad)
  const hh = Math.ceil(h * (shape === 'triangle' ? 1.15 : 1))
  cv.width = w * 2
  cv.height = hh * 2
  ctx = cv.getContext('2d')!
  ctx.scale(2, 2)
  ctx.fillStyle = '#' + hex
  ctx.beginPath()
  if (!compact && (shape === 'circle' || !SHAPE_PAD[shape])) ctx.roundRect(0, 0, w, hh, 3.5)
  else shapePath(ctx, shape, w, hh)
  ctx.fill()
  ctx.fillStyle = lumaOf(hex) > 160 ? '#111111' : '#ffffff'
  ctx.font = '600 9.5px system-ui, sans-serif'
  ctx.textAlign = 'center'
  ctx.textBaseline = 'middle'
  // a triangle's usable area sits low; everything else centres
  ctx.fillText(label, w / 2, hh / 2 + (shape === 'triangle' ? 2.5 : 0.5))
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
    // a whole bullet strip composed into one image (see loadStations).
    // Past 8 bullets the strip wraps into balanced centered rows —
    // Atlantic Av-Barclays' ten lines all appear (never truncate a
    // strip; that lies about who stops here) without out-spanning the
    // name. The icon anchors 'top', so extra rows grow downward.
    const parts = m[1].split('|').map(bulletCanvas).filter(Boolean) as HTMLCanvasElement[]
    if (!parts.length) return null
    const gap = 3 * 2
    const nrows = Math.ceil(parts.length / 8)
    const per = Math.ceil(parts.length / nrows)
    const rows: HTMLCanvasElement[][] = []
    for (let i = 0; i < parts.length; i += per) rows.push(parts.slice(i, i + per))
    const rowW = (r: HTMLCanvasElement[]) =>
      r.reduce((a, c) => a + c.width, 0) + gap * (r.length - 1)
    const w = Math.max(...rows.map(rowW))
    const rowH = Math.max(...parts.map((c) => c.height))
    const vgap = 2 * 2
    cv.width = w
    cv.height = rowH * rows.length + vgap * (rows.length - 1)
    const ctx = cv.getContext('2d')!
    let y = 0
    for (const r of rows) {
      let x = Math.round((w - rowW(r)) / 2)
      for (const c of r) {
        ctx.drawImage(c, x, y)
        x += c.width + gap
      }
      y += rowH + vgap
    }
    return ctx.getImageData(0, 0, cv.width, cv.height)
  }
  const single = bulletCanvas(id)
  return single ? single.getContext('2d')!.getImageData(0, 0, single.width, single.height) : null
}
// perceived luminance — yellow bullets (N/Q/R/W) need dark glyphs
const lumaOf = (hex: string) => {
  const n = parseInt(hex, 16)
  return 0.299 * (n >> 16) + 0.587 * ((n >> 8) & 255) + 0.114 * (n & 255)
}

// marker → its union ribbon bundle, resolved once per data load: the
// nearest band-15 ribbon carrying one of the marker's routes names the
// corridor, and every feature sharing its centerline hash is the
// bundle (passers-by included — a pill must know about the express
// lines that survive it).
let markerBundles: Map<any, BundleRow[]> | null = null

function buildMarkerBundles() {
  const band = bandRaw.get(15)
  const sts = stationsRaw.value
  if (!band || !sts) return
  const byRoute = new Map<string, any[]>()
  const byG = new Map<string, BundleRow[]>()
  for (const f of band.features) {
    const p = f.properties
    if ((p.kind !== 'steady' && p.kind !== 'bridge') || !f._g) continue
    const row: BundleRow = {
      g: f._g, color: p.color, off: +p.offset_px,
      routes: String(p.routes ?? '').split(',').filter(Boolean), props: p,
    }
    if (!byG.has(f._g)) byG.set(f._g, [])
    byG.get(f._g)!.push(row)
    for (const r of row.routes) {
      if (!byRoute.has(r)) byRoute.set(r, [])
      byRoute.get(r)!.push(f)
    }
  }
  // point-to-SEGMENT distance: FAIR vertices sit >60 m apart on
  // straights, so vertex distance alone misses ribbons the marker is
  // sitting right on top of
  const distToLine = (mx: number, my: number, kx: number, cs: number[][]) => {
    let best = Infinity
    for (let i = 1; i < cs.length; i++) {
      const ax = (cs[i - 1][0] - mx) * kx, ay = (cs[i - 1][1] - my) * 111320
      const bx = (cs[i][0] - mx) * kx, by = (cs[i][1] - my) * 111320
      const dx = bx - ax, dy = by - ay
      const n2 = dx * dx + dy * dy
      const t = n2 > 1e-12 ? Math.max(0, Math.min(1, -(ax * dx + ay * dy) / n2)) : 0
      best = Math.min(best, Math.hypot(ax + t * dx, ay + t * dy))
    }
    return best
  }
  markerBundles = new Map()
  for (const f of sts.features) {
    const p = f.properties
    if (p.ftype !== 'marker') continue
    const [mx, my] = f.geometry.coordinates
    const kx = 111320 * Math.cos((my * Math.PI) / 180)
    // per-color extents differ at junctions (the J reaches the fork,
    // the M stops at Essex), so ONE nearest feature under-collects:
    // take the union of each ROUTE's nearest ribbon's centerline group
    const gs = new Set<string>()
    for (const r of String(p.routes ?? '').split(',')) {
      let best: any = null
      let bestD = 40
      for (const cf of byRoute.get(r) ?? []) {
        const d = distToLine(mx, my, kx, cf.geometry.coordinates)
        if (d < bestD) {
          bestD = d
          best = cf
        }
      }
      if (best) gs.add(best._g)
    }
    if (gs.size) {
      const rows: BundleRow[] = []
      for (const g of gs) rows.push(...(byG.get(g) ?? []))
      markerBundles.set(f, rows)
    }
  }
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
  if ((date || off.size) && !markerBundles) buildMarkerBundles()
  const live = liveNow()
  const drawn = (p: any) => {
    if (!live) return true
    const rs = String(p.routes ?? '').split(',').filter(Boolean)
    return rs.length === 0 || rs.some((r) => live.has(r))
  }
  const feats =
    date || off.size
      ? raw.features
          .filter((f: any) => stationVisible(f.properties, masks.value, date, off) && drawn(f.properties))
          .map((f: any) => timeFilteredBullets(f, date, off))
      : raw.features
  src.setData({ type: 'FeatureCollection', features: feats })
}

/** A surviving station's bullet strip shows only the routes awake at
 *  the chosen time (and in enabled classes) — the 2am map must not
 *  advertise the B and C at stations they stopped serving at midnight.
 *  Pure: filtered features are copies, the cached data stays the union. */
function timeFilteredBullets(f: any, date: Date | null, off: Set<string>): any {
  const p = f.properties
  let props: Record<string, any> | null = null
  // markers re-derive their icon against the bundle at this instant —
  // a two-line pill whose second line sleeps becomes that line's dot
  if (p.ftype === 'marker') {
    const bundle = markerBundles?.get(f)
    if (bundle) {
      const icon = markerIconAt(p, bundle, masks.value, date, off)
      if (icon && icon !== p.icon) props = { ...p, icon }
    }
  }
  const labeled = p.ftype === 'station' || (p.ftype === 'marker' && p.nmarkers > 1)
  if (labeled) {
    const idx = activeRouteIdx(p, masks.value, date, off)
    if (idx) {
      const pick = (s: string) => {
        const all = String(s ?? '').split(',')
        return idx.map((i) => all[i]).join(',')
      }
      const ids = bulletIdsOf({
        labels: pick(p.labels),
        route_colors: pick(p.route_colors),
        modes: pick(p.modes),
        shapes: pick(p.shapes),
      })
      const np = props ?? { ...p }
      if (ids.length) np.brow = 'row-' + ids.join('|')
      else delete np.brow
      props = np
    }
  }
  return props ? { ...f, properties: props } : f
}

// time and class changes re-filter the cached data in memory — no fetch.
// In tile mode the same trigger routes ribbons to layer filters instead
// (applyStations covers the hydrated symbols either way).
watch([activeAt, masks, classesOff], () => {
  applyBands()
  applyStations()
  applyTileFilters()
})

function addLayers() {
  const { w, o } = modeExprs(styleSet.value)
  const COLOR = ['concat', '#', ['get', 'route_color']]
  for (const b of BANDS) {
    for (const [kind, off] of KINDS) {
      const id = `ribbon-${b.key}-${kind}`
      const filter = ['all', ['==', ['get', 'band_min'], b.key], ['==', ['get', 'kind'], kind]]
      structuralFilter.set(id, filter)
      map.addLayer({
        id,
        type: 'line',
        source: `build-${b.key}`,
        minzoom: b.min === 0 ? 0 : b.min,
        maxzoom: b.max === 24 ? 24 : b.max,
        filter,
        // round caps: at a transition/steady seam the eased line arrives
        // with lateral slope while the steady leaves flat, and butt caps
        // cut at those two angles leave a wedge notch at every seam
        layout: { 'line-cap': 'round', 'line-join': 'round' },
        paint: { 'line-color': COLOR, 'line-width': widthExpr(perFeedW(w)), 'line-opacity': perFeedO(o), 'line-offset': off },
      })
      ribbonIds.push(id)
      ;(bandLayers.get(b.key) ?? bandLayers.set(b.key, []).get(b.key)!).push(id)
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
  // Importance is the PERCENTILE the pipeline computes per feed (routes +
  // distinct lines + transfers.txt degree), not a raw route count. Route
  // count cannot gate a small system: every Charlotte station has one
  // route, so a threshold of 2 showed exactly one label at z13 and a
  // threshold of 6 showed none at z11. A percentile asks the question the
  // gate actually means — "the top n% of THIS feed".
  //
  // The thresholds are deliberately generous: MapLibre's collision is
  // what decides whether a label FITS, and symbol-sort-key places the
  // important ones first, so anything that survives collision was worth
  // drawing. The gate exists to bound the work, not to ration labels.
  const imp = ['coalesce', ['get', 'imp'], 0]
  const isMarker = ['==', ['get', 'ftype'], 'marker']
  const isStation = ['==', ['get', 'ftype'], 'station']
  map.addLayer({
    // EVERY dot appears at once, at z10. Ranking them in was a mistake:
    // a half-drawn set of stops does not read as "the important ones", it
    // reads as missing data — you cannot tell a skipped stop from a gap
    // in the feed. Labels are the scarce resource and get the ranking;
    // dots are a few pixels with no collision box, so they are all-or-
    // nothing. They still shrink at the outer end so they never swamp
    // the ribbons.
    id: 'station-markers', type: 'symbol', source: 'stations', minzoom: 11,
    filter: isMarker as any,
    layout: {
      // the icon id is precomputed per feature (dot-<hex>-<off> or
      // pill-<span>) and DRAWN on demand by styleimagemissing. A dot's
      // slot offset is baked into its image so icon-rotate carries it to
      // the correct side of the corridor; icon-size then scales image
      // AND offset together, exactly matching zoomScaledOffset.
      'icon-image': ['get', 'icon'],
      'icon-size': ['interpolate', ['linear'], ['zoom'], 11, 0.38, 12, 0.5, 14, 1],
      'icon-rotate': ['get', 'bearing'],
      'icon-rotation-alignment': 'map',
      'icon-allow-overlap': true,
      'icon-ignore-placement': true,
    },
  })
  // caterpillars: inline route bullets riding the ribbons. Each bullet is
  // a point at the chain's anchor carrying a map-aligned px vector (fork
  // symbol-anchor-offset) that lands it on its own ribbon's slot offset —
  // the chain rotates with the camera, glyphs stay upright, and the
  // pixel-space group never stretches with zoom. Band 14 bullets show
  // z14-15, band 15 bullets take over above (offsets match zoomScaledOffset
  // only at z14+ where the slot pitch is fixed, so no cats below 14).
  // Each cat rides the band that DRAWS at its zoom, so the bullet's
  // lateral offset always matches the ribbon under it.
  const isCat = ['==', ['get', 'ftype'], 'cat']
  const catBand = (b: number, text: boolean) =>
    ['all', isCat, ['==', ['get', 'band'], b], ['==', ['coalesce', ['get', 'text'], false], text]]
  const catBandStep = (text: boolean) => ['step', ['zoom'],
    catBand(0, text), 13, catBand(13, text), 14, catBand(14, text), 15, catBand(15, text)] as any
  const catAnchorOffset = ['interpolate', ['linear'], ['zoom'],
    11, ['get', 'veclo'], 14, ['get', 'vec']] as any
  map.addLayer({
    id: 'cats', type: 'symbol', source: 'stations', minzoom: 12,
    filter: catBandStep(false),
    layout: {
      'icon-image': ['concat', 'blt-', ['get', 'hex'], '-',
        ['coalesce', ['get', 'shape'], ''], '-', ['get', 'label']],
      // real collision, junior to everything: placement runs top layer
      // first, and the station layers sit above this one, so stop
      // labels always win — a bullet under a label yields. ignore-
      // placement keeps bullets from ever suppressing anything else.
      // (The fork shifts collision boxes by the anchor offset, so the
      // test happens where the bullet actually draws.)
      'icon-allow-overlap': false,
      'icon-ignore-placement': true,
      // The bundle NARROWS as you zoom out (zoomScaledOffset halves the
      // slot pitch by z11), so a fixed pixel vector would walk the bullet
      // off its ribbon. veclo carries the same along-track stagger with
      // the lateral half scaled to 0.5, and interpolating between them
      // reproduces the ribbons' own curve exactly.
      'symbol-anchor-offset': catAnchorOffset,
      'symbol-anchor-offset-alignment': 'map',
    } as any,
  })

  // WORD labels are not bullets. A system whose lines are called "Orange
  // Line" cannot put that in a 1:1 disc — it becomes a blob — so those
  // routes are set as TEXT RUNNING ALONG the ribbon, the way a road map
  // labels a highway (Apple does exactly this for the CTA and Amtrak).
  // The classification is automatic and lives in the pipeline; here we
  // only have to draw the two kinds differently.
  //
  // Rotation comes baked as `ang` (kept upright at build time) and the
  // same anchor offset puts the label on its own ribbon, so a word label
  // sits on the trunk exactly where its bullet would have.
  map.addLayer({
    id: 'cat-text', type: 'symbol', source: 'stations', minzoom: 12,
    filter: catBandStep(true),
    layout: {
      'text-field': ['get', 'label'],
      // ITALIC, the way a road map sets a route name — it separates a
      // line's identity from the upright station names around it at a
      // glance. Matches the weight of the rest of the map's type;
      // CARTO's glyph CDN serves this stack.
      'text-font': ['Montserrat Medium Italic'],
      'text-size': ['interpolate', ['linear'], ['zoom'], 12, 10, 16, 13],
      'text-rotate': ['get', 'ang'],
      'text-rotation-alignment': 'map',
      'text-pitch-alignment': 'viewport',
      'text-allow-overlap': false,
      'text-ignore-placement': true,
      'text-padding': 3,
      'symbol-anchor-offset': catAnchorOffset,
      'symbol-anchor-offset-alignment': 'map',
    } as any,
    paint: {
      // the line's own colour, haloed against the basemap — the label IS
      // the line's identity, so it must not read as a place name
      'text-color': ['concat', '#', ['get', 'hex']],
      'text-halo-color': isDark.value ? 'rgba(12,12,16,0.92)' : 'rgba(255,255,255,0.95)',
      'text-halo-width': 1.6,
    },
  })

  const rankBump = ['case', ['>=', ['get', 'rank'], 8], 2.5, ['>=', ['get', 'rank'], 4], 1, 0]
  // the merged complex label yields to per-corridor labels at z15 —
  // stations with one marker keep their label at every zoom (coalesce:
  // builds predating nmarkers read as solo)
  const rk = ['get', 'rank']
  // The strip hangs below the LAST line of the name, so its offset is
  // the height of the shaped text block — which is measured in ems and
  // therefore moves with text-size, i.e. with both zoom and rank tier.
  // The old hand-tuned pixel ladder was read off z16 labels only, so it
  // opened a hole under the smaller names every zoom below that.
  //
  // Text block bottom = text-offset (0.5 em) + nrows × MapLibre's
  // default 1.2 em text-line-height, all × the text size in px. GAP is
  // the air under that block, in ems too so it holds its proportion as
  // the label grows — a px gap that reads right at z16 is a squeeze at
  // z11. It sits on top of the line box's own slack, so the visible gap
  // is a little wider than GAP alone.
  const TEXT_TOP_EM = 0.5 // the layer's text-offset, in ems
  const LINE_EM = 1.2 // MapLibre's default text-line-height
  const GAP_EM = 0.3
  // the text-size ramp both label layers draw with — the strip's offset
  // is derived from these, so they must be read from one place
  const [Z_LO, Z_HI, SIZE_LO, SIZE_HI] = [11, 16, 10, 13]
  const textSize = ['interpolate', ['linear'], ['zoom'],
    Z_LO, ['+', SIZE_LO, rankBump], Z_HI, ['+', SIZE_HI, rankBump]] as any
  const stripY = (size: number, rows: number) =>
    ['literal', [0, Math.round(10 * size * (TEXT_TOP_EM + LINE_EM * rows + GAP_EM)) / 10]]
  // one table per zoom stop; ["zoom"] is only legal as the input to a
  // top-level interpolate, so the composite goes this way around and the
  // stops interpolate as arrays. Both size and offset are linear in
  // zoom, so the interpolated offset tracks the text exactly, not just
  // at the stops.
  const bulletOffsetAt = (base: number) => {
    const byRows = (size: number) => ['match', ['get', 'nrows'],
      2, stripY(size, 2), 3, stripY(size, 3), 4, stripY(size, 4), stripY(size, 1)]
    // the same rank tiers rankBump gives text-size
    return ['case',
      ['>=', rk, 8], byRows(base + 2.5),
      ['>=', rk, 4], byRows(base + 1),
      byRows(base)]
  }
  const bulletOffset = ['interpolate', ['linear'], ['zoom'],
    Z_LO, bulletOffsetAt(SIZE_LO), Z_HI, bulletOffsetAt(SIZE_HI)] as any
  // Labels reach much further out too. They DO carry collision boxes, so
  // a loose gate cannot overdraw — MapLibre drops what will not fit and
  // symbol-sort-key means the ones it keeps are the important ones. The
  // gate's job is to bound work, and the previous thresholds were doing
  // the renderer's rationing for it.
  // Density is COLLISION's job, not the filter's. Every station is a
  // candidate at every zoom; what changes with zoom is how much clear
  // space each label demands (`text-padding` below). That is the right
  // mechanism for three reasons: the thinning is SPATIAL, so labels
  // spread evenly instead of clustering wherever the high-ranked
  // stations happen to be; a stop on an empty stretch is never deleted
  // just for scoring low; and there is one dial to turn instead of a
  // ladder of per-zoom rank cut-offs that has to be retuned for every
  // feed. Importance survives as `symbol-sort-key`, which is exactly
  // where it belongs — it decides who WINS a contested spot.
  //
  // From z15 the merged complex label yields to the per-corridor labels
  // below, so only the solo-station test remains.
  const solo = ['<', ['coalesce', ['get', 'nmarkers'], 1], 2]
  const labelGate = ['step', ['zoom'],
    isStation,
    15, ['all', isStation, solo]] as any
  // Clear space demanded around each label, in px. Big when zoomed out
  // (few labels survive), shrinking to normal as there is room for them.
  const labelPadding = ['interpolate', ['linear'], ['zoom'],
    11, 34, 12, 22, 13, 13, 14, 6, 16, 2] as any
  map.addLayer({
    id: 'station-labels', type: 'symbol', source: 'stations', minzoom: 11,
    filter: labelGate,
    layout: {
      'text-field': ['get', 'name'],
      'text-font': ['Montserrat Medium'],
      'symbol-sort-key': ['*', -1, imp],
      // fixed top anchor: name under the marker, the bullet strip under
      // the name. (Variable anchors are off for now — the icon does not
      // follow the text's variable anchor, so the strip would detach.)
      'text-anchor': 'top',
      'text-offset': [0, TEXT_TOP_EM],
      'text-padding': labelPadding,
      // rank bump INSIDE the zoom stops: ["zoom"] is only legal as input
      // to a top-level interpolate/step, so the composite goes this way
      // around (same shape as zoomScaledOffset)
      'text-size': textSize,
      // the bullet strip appears once there is room for it; its distance
      // below the anchor follows the name's estimated wrap count
      'icon-image': ['step', ['zoom'], '', 13.5, ['coalesce', ['get', 'brow'], '']],
      'icon-anchor': 'top',
      'icon-offset': bulletOffset,
      'icon-optional': true,
    },
    paint: labelPaint(),
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
      'symbol-sort-key': ['*', -1, imp],
      'text-anchor': 'top',
      'text-offset': [0, TEXT_TOP_EM],
      'text-padding': labelPadding,
      'text-size': textSize,
      'icon-image': ['coalesce', ['get', 'brow'], ''],
      'icon-anchor': 'top',
      'icon-offset': bulletOffset,
      'icon-optional': true,
    },
    paint: labelPaint(),
  })
  for (const id of ['station-markers', ...LABEL_LAYERS]) {
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
      'circle-opacity': 0.8, 'circle-stroke-color': nodeStroke(), 'circle-stroke-width': 1,
    },
    layout: { visibility: 'none' },
  })
  // yard oracle overlay (<out>.yards.geojson): region outlines, the spine
  // skeleton the substitution rides, pair spines, entrance points — all
  // colored per region so adjacent complexes read apart
  const regionColor = ['match', ['%', ['get', 'region'], 6],
    0, '#e6550d', 1, '#3182bd', 2, '#31a354', 3, '#756bb1', 4, '#d6616b', 5, '#8c6d31',
    '#888888'] as any
  map.addLayer({
    id: 'dbg-yards', type: 'fill', source: 'yards',
    filter: ['==', ['get', 'kind'], 'yard'],
    paint: { 'fill-color': regionColor, 'fill-opacity': 0.14 },
    layout: { visibility: 'none' },
  })
  map.addLayer({
    id: 'dbg-yards-outline', type: 'line', source: 'yards',
    filter: ['==', ['get', 'kind'], 'yard'],
    paint: { 'line-color': regionColor, 'line-width': 1.5, 'line-opacity': 0.8 },
    layout: { visibility: 'none' },
  })
  map.addLayer({
    id: 'dbg-yards-spine', type: 'line', source: 'yards',
    filter: ['==', ['get', 'kind'], 'yard_spine'],
    paint: {
      'line-color': regionColor, 'line-width': 1.2, 'line-opacity': 0.7,
      'line-dasharray': [2, 2],
    },
    layout: { visibility: 'none' },
  })
  map.addLayer({
    id: 'dbg-yards-skel', type: 'line', source: 'yards',
    filter: ['==', ['get', 'kind'], 'yard_skel'],
    paint: { 'line-color': regionColor, 'line-width': 2.5, 'line-opacity': 0.9 },
    layout: { visibility: 'none' },
  })
  map.addLayer({
    id: 'dbg-yards-ent', type: 'circle', source: 'yards',
    filter: ['==', ['get', 'kind'], 'yard_entrance'],
    paint: {
      'circle-radius': 4.5, 'circle-color': regionColor,
      'circle-stroke-color': '#fff', 'circle-stroke-width': 1.5,
    },
    layout: { visibility: 'none' },
  })
}

// ── tile mode (docs: `portolan tiles`) ─────────────────────────────────
// Region-scale feeds ship as a z/x/y MVT pyramid instead of whole-band
// GeoJSON, and /api/tiles/<feed>/tiles.json existing is the whole mode
// switch: 200 → stream tiles, 404 → the band machinery above, untouched.
// Tile mode is deliberately the STATIC all-service picture for now — the
// time dial and class toggles need materialized features to re-filter,
// and a pyramid never hands the client the whole document.
// Generalized to N regions: the global context draws every feed with a
// cut pyramid at once — one vector source and one set of cloned layers
// per region, all ids suffixed with the region's feed. A single tiled
// feed is simply the one-region case.
interface TileRegion {
  feed: string
  tiles: string[]
  minzoom: number
  maxzoom: number
  bounds?: number[] // [w,s,e,n]
}
const tileRegions = ref<TileRegion[]>([])
const tileMode = computed(() => tileRegions.value.length > 0)
// The dynamic controls work everywhere data draws: GeoJSON feeds through
// the JS re-filter, tiled feeds AND global through ribbon layer filters
// plus the hydrated symbol pipeline (hydrateSymbols sweeps every
// region's tiles, so global symbols gate the same way). Only a global
// with no cut pyramids parks them — nothing is on screen to filter.
const parked = computed(() => isGlobal.value && !tileMode.value)
// reload() awaits this so the mode is decided before anything draws
let tilesProbe: Promise<void> = Promise.resolve()

const tileBase = (f: string) => `${window.location.origin}/api/tiles/${encodeURIComponent(f)}/`

async function probeTiles() {
  if (isGlobal.value) {
    // the index alone carries everything a source needs (bounds, maxzoom;
    // the tile template is fixed), so global costs one round trip — no
    // per-region tiles.json fetches
    const idx = await api.tilesIndex().catch(() => [])
    tileRegions.value = idx.map((e) => ({
      feed: e.feed,
      tiles: [tileBase(e.feed) + '{z}/{x}/{y}.mvt'],
      minzoom: 0,
      maxzoom: e.maxzoom ?? 15, // the renderer overzooms above the pyramid top
      bounds: e.bounds,
    }))
  } else if (feed.value) {
    const tj = await api.tilejson(feed.value).catch(() => null)
    tileRegions.value = tj
      ? [
          {
            feed: feed.value,
            // tiles.json carries a relative template so one document works
            // from any origin; resolve it against the pyramid's directory
            tiles: tj.tiles.map((t) => (/^https?:/.test(t) ? t : tileBase(feed.value) + t)),
            minzoom: tj.minzoom ?? 0,
            maxzoom: tj.maxzoom ?? 15,
            bounds: tj.bounds,
          },
        ]
      : []
  } else {
    tileRegions.value = []
  }
  // the dial rides layer filters wherever tiles draw — single feeds and
  // global alike — so a linked ?t= survives every context switch now
}

// Per-feed resolved style sets for tile mode. Global's own
// api.style('global') is only the _default layer, so every region's doc
// is fetched alongside and each feed's tile clones / hydrated features
// style themselves from their OWNING feed's set. Single-feed tile mode
// flows through the same mechanism as a map of one (where it resolves
// to the same set styleSet already holds). Cached persistently like
// activityCache; styleSet stays the fallback for feeds without a doc.
const feedStyles = ref<Record<string, StyleSet>>({})
const styleCache = new Map<string, StyleSet | null>()
const styleForFeed = (f: string) => feedStyles.value[f] ?? styleSet.value

async function loadFeedStyles() {
  if (!tileMode.value) {
    feedStyles.value = {}
    return
  }
  const out: Record<string, StyleSet> = {}
  await Promise.all(
    tileRegions.value.map(async (r) => {
      let s = styleCache.get(r.feed)
      if (s === undefined) {
        s = await api.style(r.feed).catch(() => null)
        styleCache.set(r.feed, s)
      }
      if (s) out[r.feed] = s
    }),
  )
  feedStyles.value = out
}

// everything the tile path added, in add order — the teardown lists
const tileLayerIds: string[] = []
const tileSourceIds: string[] = []
// delegated listeners are keyed by layer id and survive re-adds, so each
// id registers exactly once ever, across every rebuild and region set
const tileHandlerIds = new Set<string>()

/** Clone a live layer definition and retarget it at a vector source.
 *  Cloning off the style rather than restating the definition is the
 *  point: the tile layers inherit exactly the paint/layout/filter the
 *  GeoJSON twin carries right now, so the two modes cannot drift. */
function tileClone(srcId: string, id: string, source: string, sourceLayer: string) {
  const def = JSON.parse(JSON.stringify(map.getStyle().layers.find((l: any) => l.id === srcId)))
  def.id = id
  def.source = source
  def['source-layer'] = sourceLayer
  // the twin's LIVE filter may carry a time clause; clones start from the
  // recorded structural filter so applyTileFilters never doubles it up
  if (structuralFilter.has(srcId)) def.filter = structuralFilter.get(srcId)
  structuralFilter.set(id, def.filter)
  return def
}

/** (Re)build the vector sources and their layers for the current region
 *  list, or tear them down when there are none. Runs inside reload()
 *  AFTER the GeoJSON ribbons' paints are refreshed, so the clones pick
 *  up the new context's style. */
function syncTileLayers() {
  if (!map) return
  for (const id of tileLayerIds) {
    if (map.getLayer(id)) map.removeLayer(id)
    structuralFilter.delete(id)
  }
  tileLayerIds.length = 0
  for (const id of tileSourceIds) {
    if (map.getSource(id)) map.removeSource(id)
  }
  tileSourceIds.length = 0
  if (!tileMode.value) {
    // leaving tile mode: the transition twins may still carry a time or
    // class clause — detach it, the JS path owns filtering from here
    applyTileFilters()
    return
  }
  // The GeoJSON twins keep DRAWING in tile mode: hydrateTransitions
  // materializes junction transitions into their sources. Any zoom
  // stretch holdBands left behind (it no-ops in tile mode, so it cannot
  // undo itself) must come off, or a stretched band's transition twin
  // would double-draw against another band's canonical range.
  heldAs = ''
  clearHeldTransitions() // these sources belong to the outgoing regions
  for (const b of BANDS) {
    for (const id of bandLayers.get(b.key) ?? []) {
      if (map.getLayer(id)) {
        map.setLayerZoomRange(id, b.min === 0 ? 0 : b.min, b.max === 24 ? 24 : b.max)
      }
    }
  }
  const add = (def: any, beforeId: string) => {
    map.addLayer(def, beforeId)
    tileLayerIds.push(def.id)
  }
  // Steady clones must sit BELOW every ribbon twin: at a junction the
  // hydrated transition's eased ramp has to draw OVER the steady ribbons
  // it crosses — the per-band interleaving gives GeoJSON mode this for
  // free, and anchoring clones above the twins buried transitions under
  // crossing lines (Clark/Lake). The first ribbon twin in the live style
  // is the bottom of the whole ribbon stack (clones are torn down above,
  // so /^ribbon-\d/ can only match a twin); the empty steady twins left
  // above the clones are harmless in tile mode. Order bottom→top:
  // steady clones < transition/bridge twins < symbols.
  const anchor =
    map.getStyle().layers.find((l: any) => /^ribbon-\d/.test(l.id))?.id ?? 'station-markers'
  for (const r of tileRegions.value) {
    const src = `tiles-${r.feed}`
    map.addSource(src, {
      type: 'vector',
      tiles: r.tiles,
      minzoom: r.minzoom,
      maxzoom: r.maxzoom,
      ...(r.bounds?.length === 4 ? { bounds: r.bounds } : {}),
    })
    tileSourceIds.push(src)
    // the clone inherits the twin's paint, which in global is the
    // _default layer's — restate width/opacity from the owning feed's
    // own styleSet so per-feed mode overrides survive
    const { w, o } = modeExprs(styleForFeed(r.feed))
    for (const b of BANDS) {
      for (const [kind] of KINDS) {
        // transitions ease their offset over line-progress, which MapLibre
        // only computes for GeoJSON sources with lineMetrics — a vector-
        // source clone draws them un-eased (offset jumps at every fork).
        // They render through hydrateTransitions + the GeoJSON twins
        // instead; bridges ride along so both hydrated kinds have exactly
        // one drawing path.
        if (kind === 'transition' || kind === 'bridge') continue
        const def = tileClone(
          `ribbon-${b.key}-${kind}`,
          `ribbon-t-${b.key}-${kind}-${r.feed}`,
          src,
          'ribbons',
        )
        // holdBands stretches the GeoJSON twins' zoom ranges while a band
        // is in flight; the pyramid always has every band, so the clone
        // gets the canonical range back
        def.minzoom = b.min === 0 ? 0 : b.min
        def.maxzoom = b.max === 24 ? 24 : b.max
        def.paint = { ...def.paint, 'line-width': widthExpr(w), 'line-opacity': o }
        add(def, anchor)
      }
    }
    // symbols are NOT cloned: hydrateSymbols materializes them into the
    // stations source, where the standard pipeline (prepareStations'
    // icon/brow/nrows, applyStations' gating, styleimagemissing) draws
    // them exactly as in GeoJSON mode — bullet strips included
  }
  for (const id of tileLayerIds) {
    if (tileHandlerIds.has(id)) continue
    tileHandlerIds.add(id)
    map.on('click', id, (e: any) => {
      inspect.value = e.features?.[0]?.properties ?? null
    })
    map.on('mouseenter', id, () => (map.getCanvas().style.cursor = 'pointer'))
    map.on('mouseleave', id, () => (map.getCanvas().style.cursor = ''))
  }
  hydrateSymbols()
  hydrateTransitions()
  applyTileFilters() // fresh clones carry structural filters only
}

// ── tile-mode time dial + class toggles ────────────────────────────────
// Tile mode renders a timestamp and the class toggles as LAYER FILTERS
// on the RIBBONS (`acts` bit test, scalar `mode`) instead of the GeoJSON
// path's re-materialization; symbols go through hydrateSymbols into the
// ordinary applyStations pipeline, which gates them in JS exactly as in
// GeoJSON mode — so these filters must NEVER touch a symbol layer, or
// symbols would be gated twice. What the GPU cannot do is re-center a
// thinned bundle, so surviving ribbons keep their union offsets — the
// honest trade for controls that work without the whole document. (The
// acts expression could subsume the JS path later; for now GeoJSON mode
// is untouched.)

// hex digits with bit b set — the bit test as a match label set
const HEX_BIT = [
  ['1', '3', '5', '7', '9', 'b', 'd', 'f'],
  ['2', '3', '6', '7', 'a', 'b', 'e', 'f'],
  ['4', '5', '6', '7', 'c', 'd', 'e', 'f'],
  ['8', '9', 'a', 'b', 'c', 'd', 'e', 'f'],
]
// acts is per-ROUTE: a semicolon-joined list of 42-char masks aligned
// with the routes CSV (one bare mask for single-route features and
// cats), so the test is ANY route slot awake — slots ride at stride 43
const ACTS_MAX_ROUTES = 16

/** MapLibre filter for "any member route awake at `date`", or null when
 *  no time is set. Bit order matches maskActive EXACTLY: 7 days ×
 *  6 hex digits, Monday first, each day a big-endian 24-bit word with
 *  hour 0 at the LSB — so an hour's digit sits at day*6 + (5 -
 *  floor(hour/4)), and bit hour%4 of that digit (bit 0 = the EARLIEST
 *  hour of its 4-hour block). Empty/missing acts renders always-active:
 *  the JS path falls back to route-level masks there, which a filter
 *  cannot index — always-on matches it whenever no mask exists, and
 *  visible is the honest default (activePredicate's own rule 3). */
function actsFilterExpr(date: Date | null): any | null {
  if (!date || Number.isNaN(date.getTime())) return null
  const day = (date.getDay() + 6) % 7 // JS Sunday=0 → our Monday=0
  const hour = date.getHours()
  const digit = day * 6 + (5 - Math.floor(hour / 4))
  const acts = ['coalesce', ['get', 'acts'], '']
  const routeOn = (j: number) => {
    const at = j * 43 + digit
    // a slot beyond the actual route count slices to '' → false, so the
    // fixed fan-out is inert past the feature's own routes
    return ['match', ['slice', acts, at, at + 1], HEX_BIT[hour % 4], true, false]
  }
  const tests = Array.from({ length: ACTS_MAX_ROUTES }, (_, j) => routeOn(j))
  return ['case', ['==', acts, ''], true, ['any', ...tests]]
}

/** Attach (or detach) the time and class clauses on every RIBBON layer
 *  that draws tile-fed data: the steady tile clones and the hydrated
 *  transition/bridge GeoJSON twins — exactly what structuralFilter
 *  holds; symbol layers are absent by design (applyStations gates
 *  those). Each layer gets ['all', structural, …clauses]; no clauses
 *  (or no tile mode) restores the structural filter. */
function applyTileFilters() {
  if (!map) return
  const clauses: any[] = []
  if (tileMode.value) {
    const acts = actsFilterExpr(when.value ? new Date(when.value) : null)
    if (acts) clauses.push(acts)
    if (classesOff.value.size) {
      clauses.push(['!', ['in', ['get', 'mode'], ['literal', [...classesOff.value]]]])
    }
  }
  for (const [id, structural] of structuralFilter) {
    if (!map.getLayer(id)) continue
    map.setFilter(
      id,
      clauses.length ? ['all', ...(structural ? [structural] : []), ...clauses] : structural,
    )
  }
}

type HeldFeat = { feat: any; box: [number, number, number, number]; fp: string }

/** What has been hydrated, per band, and what each band's source was
 *  last SET to. Both exist for the same reason: querySourceFeatures only
 *  sees the tiles that are renderable at this instant, and a zoom churns
 *  that set — the old level's tiles go as the new level's arrive, and in
 *  global every region source fires its own load event, so one zoom used
 *  to run the whole cross-source sweep dozens of times over. Rebuilding
 *  the twins' data from scratch on each sweep blinked a junction's ramps
 *  out whenever one landed mid-churn; the steady ribbons beside them
 *  never blinked, because they come straight off the vector source with
 *  no round trip through a worker to lose.
 *
 *  The whole-document viewer never drops a feature it has loaded, and
 *  these are the tiled equivalent. A sweep that comes up short can only
 *  fail to REFRESH a transition now, never erase it — what is hydrated
 *  is held until it scrolls a viewport away — and a sweep that finds
 *  exactly what is already drawn does not touch the source at all. */
const heldTransitions = new Map<number, Map<string, HeldFeat>>(
  BANDS.map((b) => [b.key, new Map<string, HeldFeat>()]),
)
const hydratedSig = new Map<number, string>()

function clearHeldTransitions() {
  for (const m of heldTransitions.values()) m.clear()
  hydratedSig.clear()
}

/** lon/lat bounds of a hydrated line, and a cheap fingerprint standing in
 *  for its geometry: vertex count plus the two endpoints. Two copies of
 *  one transition off different zoom levels differ in both. */
function heldShape(g: any): { box: [number, number, number, number]; fp: string } {
  const parts: any[] = g?.type === 'MultiLineString' ? g.coordinates : [g?.coordinates ?? []]
  let w = Infinity, s = Infinity, e = -Infinity, n = -Infinity
  let count = 0
  let first: any = null
  let last: any = null
  for (const part of parts) {
    for (const c of part) {
      count++
      if (!first) first = c
      last = c
      if (c[0] < w) w = c[0]
      if (c[0] > e) e = c[0]
      if (c[1] < s) s = c[1]
      if (c[1] > n) n = c[1]
    }
  }
  const at = (c: any) => (c ? `${c[0].toFixed(6)},${c[1].toFixed(6)}` : '')
  return { box: [w, s, e, n], fp: `${count}:${at(first)}:${at(last)}` }
}

/** Junction transitions cannot render straight off the vector source
 *  either: their line-offset eases over line-progress, and MapLibre only
 *  computes line-progress for GeoJSON sources with lineMetrics — never
 *  for vector tiles — so an MVT transition draws at a fixed offset and
 *  the ribbon jumps at every fork. syncTileLayers therefore clones no
 *  transition/bridge layers; instead the loaded tile features are
 *  materialized into the (otherwise idle) build-<band> GeoJSON sources,
 *  where the existing ribbon-<band>-transition/-bridge twins draw them
 *  with full easing. The tiler ships each transition WHOLE into every
 *  tile it touches (so line-progress spans the true segment), which
 *  makes duplicates a fact of life — folded by segment identity. All the
 *  properties the twins read (off_from_px/off_to_px/offset_px, nslots,
 *  kind, band_min, colors, routes, acts) are scalars, so nothing needs
 *  the JSON decode cats do. */
function hydrateTransitions() {
  if (!map || !tileMode.value) return
  const fresh = new Set<string>()
  for (const sid of tileSourceIds) {
    if (!map.getSource(sid)) continue
    const modes = styleForFeed(sid.slice('tiles-'.length))?.modes
    for (const f of map.querySourceFeatures(sid, { sourceLayer: 'ribbons' })) {
      const p = f.properties
      if (p.kind !== 'transition' && p.kind !== 'bridge') continue
      // seg names the segment but is only unique within one feed's
      // pyramid, so the source id keys regions apart; band_min + routes
      // guard against seg reuse inside a feed
      const key = `${sid}|${p.seg}|${p.band_min}|${p.routes}`
      if (fresh.has(key)) continue
      fresh.add(key)
      const held = heldTransitions.get(+p.band_min)
      if (!held) continue
      const props: any = { ...p }
      // the twins are shared across feeds, so the owning feed's resolved
      // class width/opacity rides ON the feature — the twin paint
      // coalesces _w/_o ahead of its default class match (perFeedW/O)
      const m = modes?.[p.mode]
      if (m) {
        props._w = m.width
        props._o = m.opacity
      }
      // last write wins: the same transition rides every tile it touches
      // AND the zoom either side of its band, so a later sweep is the one
      // that carries the current level's vertex density
      const { box, fp } = heldShape(f.geometry)
      held.set(key, { feat: { type: 'Feature', properties: props, geometry: f.geometry }, box, fp })
    }
  }
  // Eviction is by POSITION, not by absence from this sweep. One viewport
  // of slack in every direction, so a transition just off screen survives
  // the pan that is about to bring it back.
  const b = map.getBounds()
  const w = b.getWest(), e = b.getEast(), s = b.getSouth(), n = b.getNorth()
  const dx = e - w, dy = n - s
  // a wrapped or world-wide viewport has no meaningful outside; keep all
  const bounded = dx > 0 && dx < 120 && dy > 0
  for (const [band, held] of heldTransitions) {
    if (bounded) {
      for (const [key, h] of held) {
        if (fresh.has(key)) continue
        if (h.box[2] < w - dx || h.box[0] > e + dx || h.box[3] < s - dy || h.box[1] > n + dy) {
          held.delete(key)
        }
      }
    }
    const keys = [...held.keys()].sort()
    const sig = keys.map((k) => `${k}@${held.get(k)!.fp}`).join(';')
    if (hydratedSig.get(band) === sig) continue
    hydratedSig.set(band, sig)
    map.getSource(`build-${band}`)?.setData({
      type: 'FeatureCollection',
      features: keys.map((k) => held.get(k)!.feat),
    })
  }
}

/** One sweep per frame, never one per event. Every region source fires
 *  its own load event and every one of them used to run the full
 *  cross-source sweep — in global that is dozens of sweeps for a single
 *  zoom, each one re-tiling four GeoJSON sources in the worker. */
let hydrateQueued = 0
function requestHydrate() {
  if (!map || !tileMode.value || hydrateQueued) return
  hydrateQueued = requestAnimationFrame(() => {
    hydrateQueued = 0
    hydrateSymbols()
    hydrateTransitions()
  })
}

/** ONE hydration for every tiled symbol kind. Symbols cannot render
 *  straight off the vector source: cats carry vec/veclo anchor offsets
 *  as ARRAYS (MVT values are scalar, so the tiler ships them as JSON
 *  text and expressions have no parse), and stations/markers need the
 *  client-computed properties — icon ids, bullet strips (brow), wrap
 *  counts (nrows) — that only prepareStations makes. So the loaded
 *  symbol tiles are materialized into the stations GeoJSON source
 *  through the SAME prepareStations/applyStations pipeline GeoJSON mode
 *  uses, and the standard symbol layers draw them unchanged. Re-run as
 *  tiles come and go; the tiler writes each symbol into one owning tile
 *  per zoom, so the only duplicates to fold are across cached zoom
 *  levels. */
function hydrateSymbols() {
  if (!map || !tileMode.value) return
  const seen = new Set<string>()
  const feats: any[] = []
  for (const sid of tileSourceIds) {
    if (!map.getSource(sid)) continue
    for (const sl of ['stations', 'markers', 'cat']) {
      for (const f of map.querySourceFeatures(sid, { sourceLayer: sl })) {
        const p = { ...f.properties }
        // the owning feed rides along for anything styleSet-derived
        // downstream. Today symbol rendering is fully baked-prop-driven
        // (shapes/colors/fonts/bordered are resolved at BUILD time into
        // the stations artifact), so this is identity/debug — but it is
        // the hook per-feed render styling would key on.
        p._feed = sid.slice('tiles-'.length)
        // Cats: the anchor is part of the identity — a route repeats the
        // same vec at every single-bullet chain along its line, so a
        // props-only key would fold distinct bullets together.
        // Stations/markers: name+routes at one coordinate IS the symbol.
        // Coordinates keep same-named symbols from different regions
        // apart in both keys.
        const key =
          sl === 'cat'
            ? `${f.geometry?.coordinates}|${p.route}|${p.band}|${p.label}|${p.vec}`
            : `${sl}|${f.geometry?.coordinates}|${p.name ?? ''}|${p.routes ?? ''}`
        if (seen.has(key)) continue
        seen.add(key)
        if (sl === 'cat') {
          try {
            if (typeof p.vec === 'string') p.vec = JSON.parse(p.vec)
            if (typeof p.veclo === 'string') p.veclo = JSON.parse(p.veclo)
          } catch {
            continue // a bullet with no offset would sit on the centerline
          }
        }
        feats.push({ type: 'Feature', properties: p, geometry: f.geometry })
      }
    }
  }
  prepareStations({ type: 'FeatureCollection', features: feats })
}

watch(debug, (d) => {
  if (!map) return
  for (const [k, on] of Object.entries(d)) {
    // a toggle owns every dbg-<k>* layer (yards has fill/outline/spine/…)
    for (const suffix of ['', '-outline', '-spine', '-skel', '-ent']) {
      const id = `dbg-${k}${suffix}`
      if (map.getLayer(id)) map.setLayoutProperty(id, 'visibility', on ? 'visible' : 'none')
    }
  }
}, { deep: true })

async function reload() {
  if (!map || !feed.value) return
  loading.value = true
  try {
    await tilesProbe // mode decides everything below
    styleSet.value = await api.style(feed.value)
    await loadFeedStyles() // per-feed sets must precede the clone rebuild
    if (tileMode.value) {
      // symbols re-hydrate from this feed's tiles; drop the old set now
      // so nothing from the previous feed lingers while tiles stream in
      stationsRaw.value = null
      markerBundles = null
      map.getSource('stations')?.setData({ type: 'FeatureCollection', features: [] })
    } else {
      loadStations() // a rebuild may have refreshed the stations artifact
    }
    loadedBands.clear()
    bandRaw.clear()
    for (const b of BANDS) {
      map.getSource(`build-${b.key}`)?.setData({ type: 'FeatureCollection', features: [] })
    }
    await ensureBand()
    const { w, o } = modeExprs(styleSet.value)
    for (const id of ribbonIds) {
      if (!map.getLayer(id)) continue
      map.setPaintProperty(id, 'line-width', widthExpr(perFeedW(w)))
      map.setPaintProperty(id, 'line-opacity', perFeedO(o))
    }
    // after the paint refresh, so tile clones inherit the new style
    syncTileLayers()
    // debug overlays are per-feed artifacts. In global they empty rather
    // than fetch — no /api/*.geojson?feed=global may ever fire.
    if (isGlobal.value) {
      for (const name of ['rail', 'paths', 'trackcenter', 'nodes', 'yards']) {
        map.getSource(name)?.setData({ type: 'FeatureCollection', features: [] })
      }
    } else {
      for (const [name, url] of [
        ['rail', `/api/rail.geojson?feed=${feed.value}`],
        ['paths', `/api/paths.geojson?feed=${feed.value}`],
        ['trackcenter', `/api/trackcenter.geojson?feed=${feed.value}`],
        ['nodes', `/api/nodes.geojson?feed=${feed.value}`],
        ['yards', `/api/yards.geojson?feed=${feed.value}`],
      ] as const) {
        map.getSource(name)?.setData(url)
      }
    }
    fitFeed()
  } finally {
    loading.value = false
  }
}

function fitFeed() {
  if (!map) return
  // tiled regions' own bounds beat any feed bbox — but a camera already
  // parked inside one of them stays put, so a reload never yanks the view
  // away from the corner someone is studying
  const bs = tileRegions.value
    .map((r) => r.bounds)
    .filter((b): b is number[] => b?.length === 4)
  if (tileMode.value && bs.length) {
    const c = map.getCenter()
    const inside = bs.some((b) => c.lng >= b[0] && c.lat >= b[1] && c.lng <= b[2] && c.lat <= b[3])
    if (!inside) {
      // the enclosing box of every region — global fits the world it has
      const u = [
        Math.min(...bs.map((b) => b[0])),
        Math.min(...bs.map((b) => b[1])),
        Math.max(...bs.map((b) => b[2])),
        Math.max(...bs.map((b) => b[3])),
      ]
      map.fitBounds([[u[0], u[1]], [u[2], u[3]]], { padding: 40, duration: 0 })
    }
    return
  }
  const b = currentFeed.value?.bbox
  if (b?.length === 4) {
    map.fitBounds([[b[0], b[1]], [b[2], b[3]]], { padding: 40, duration: 0 })
  }
}

async function loadScenarios() {
  scenarios.value = []
  grid.value = []
  if (!feed.value || isGlobal.value) return // scenarios are per-feed
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
      sources: Object.fromEntries(
        PROVIDER_ORDER.map((id) => [
          `bm-${id}`,
          {
            type: 'raster',
            tiles: [providerTiles(id)],
            tileSize: 256,
            attribution: BASEMAP_PROVIDERS[id].attribution,
          },
        ]),
      ),
      // all three are added, only the chosen ones are visible — the
      // order here is the draw order, so an overlay lands over its base
      layers: PROVIDER_ORDER.map((id) => ({
        id: `bm-${id}`,
        type: 'raster',
        source: `bm-${id}`,
        layout: { visibility: currentBasemap().show.includes(id) ? 'visible' : 'none' },
        paint: { 'raster-opacity': providerOpacity(id) },
      })),
    },
  })
  map.addControl(new maplibregl.NavigationControl({ showCompass: false }), 'bottom-right')
  // marker dots, bundle pills and route bullets are canvas-drawn the
  // first time a layer asks for them — any feed's colors and labels work
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
    for (const n of ['rail', 'paths', 'trackcenter', 'nodes', 'stations', 'yards']) {
      map.addSource(n, { type: 'geojson', data: empty })
    }
    styleSet.value = feed.value ? await api.style(feed.value).catch(() => null) : null
    addLayers()
    applyStations() // the fetch may have finished before WebGL did
    // crossing a band boundary pulls that band in the first time
    map.on('zoomend', ensureBand)
    // during the gesture, not just at the end: the boundary is crossed
    // mid-zoom, which is exactly when the gap would show
    map.on('zoom', () => {
      holdBands()
      ensureBand()
    })
    map.on('move', syncView)
    // tile mode: symbols and junction transitions re-materialize as
    // tiles come and go (the handlers no-op outside it, and both
    // hydrators dedupe)
    map.on('moveend', requestHydrate)
    map.on('sourcedata', (e: any) => {
      if (typeof e.sourceId === 'string' && e.sourceId.startsWith('tiles-') && e.isSourceLoaded) {
        requestHydrate()
      }
    })
    syncView()
    await reload()
  })
  // the scenario list does not wait on the map: it is a plain API call,
  // and burying it in the load handler meant the picker stayed empty
  // whenever WebGL was slow or unavailable
  loadClassesOff()
  loadScenarios()
  // the whole-document fetches stay parked while a pyramid serves this
  // feed, so they wait on the probe; reload() awaits the same promise
  tilesProbe = probeTiles()
  loadMasks() // awaits tilesProbe in global — must follow its assignment
  tilesProbe.then(() => {
    if (!tileMode.value) {
      loadStations()
      prefetchModes()
    }
  })
})

onBeforeUnmount(() => {
  ro?.disconnect()
  map?.remove()
})

watch(feed, async () => {
  loadClassesOff()
  // re-probe first: the new feed may cross the tile/GeoJSON divide
  tilesProbe = probeTiles()
  loadMasks() // awaits tilesProbe in global — must follow its assignment
  tilesProbe.then(() => {
    if (!tileMode.value) {
      loadStations()
      prefetchModes()
    }
  })
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
         bar — feed and view actions, then time, then a status word. The
         scenario dropdown is gone: a timestamp IS the scenario selection
         now (dynamic rendering), and the prebuilt-scenario QA controls
         live on the Service page. -->
    <div class="pointer-events-none absolute inset-x-0 top-0 z-10 flex justify-center p-2 sm:p-4">
      <div class="pointer-events-auto flex max-w-full flex-wrap items-center gap-1 rounded-xl border border-border bg-card/90 px-2 py-1.5 shadow-sm backdrop-blur">
        <span class="max-w-[40vw] truncate px-2 text-sm font-medium sm:max-w-none">{{ isGlobal ? 'Global' : currentFeed?.name || 'No feed' }}</span>
        <Badge v-if="loading" variant="info"><Spinner class="size-3" /></Badge>
        <Button variant="ghost" size="icon" title="Reload" @click="reload"><RefreshCw class="size-4" /></Button>
        <Button variant="ghost" size="icon" title="Fit to feed" @click="fitFeed"><Crosshair class="size-4" /></Button>

        <span class="mx-1 h-5 w-px bg-border" />

        <Clock class="ml-1 size-4 shrink-0 text-muted-foreground" />
        <!-- the dial works everywhere tiles or bands draw — in global,
             each network renders at its own local hour (acts are
             feed-local time); it parks only when nothing is on screen -->
        <input
          v-model="when"
          type="datetime-local"
          :disabled="parked"
          :title="parked ? 'No tiled feeds to filter' : ''"
          class="h-8 rounded-md border border-input bg-transparent px-2 text-sm disabled:cursor-not-allowed disabled:opacity-50"
          :class="isDark ? '[color-scheme:dark]' : '[color-scheme:light]'"
        />
        <Button
          variant="ghost" size="sm" :disabled="parked"
          :title="parked ? 'No tiled feeds to filter' : 'Jump to the current time'"
          @click="when = localNow()"
        >now</Button>

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

    <!-- collapsed until asked for: one button holds the corner, the full
         panel replaces it in place and folds back from its header -->
    <button
      v-if="!layersOpen"
      class="pointer-events-auto absolute bottom-4 left-4 z-10 flex items-center gap-2 rounded-xl border border-border bg-card/90 px-3 py-2 text-xs font-medium uppercase tracking-wide text-muted-foreground shadow-sm backdrop-blur transition-colors hover:bg-accent hover:text-foreground"
      title="Basemap, class and debug toggles"
      @click="layersOpen = true"
    >
      <Layers class="size-3.5" /> Layers
    </button>
    <div
      v-else
      class="pointer-events-auto absolute bottom-4 left-4 z-10 max-h-[calc(100%-6rem)] w-56 overflow-y-auto rounded-xl border border-border bg-card/90 p-3 shadow-sm backdrop-blur"
    >
      <button
        class="mb-2 flex w-full items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground transition-colors hover:text-foreground"
        @click="layersOpen = false"
      >
        <Layers class="size-3.5" /> Layers
        <X class="ml-auto size-3.5" />
      </button>
      <!-- the basemap is what the drawn network is read against, so it
           sits above the class toggles: blank to judge geometry alone,
           OpenRailwayMap to check a ribbon against the real alignment -->
      <Select
        :model-value="basemap"
        :options="basemapOptions"
        class="mb-3 h-8"
        @update:model-value="(v) => (basemap = v)"
      />
      <!-- class toggles work everywhere too — tile-mode ribbons gate on
           their scalar mode via layer filters, symbols through
           stationVisible; they park only with nothing on screen -->
      <label
        v-for="m in CLASS_ORDER"
        :key="m"
        class="flex items-center justify-between py-1 text-sm"
        :class="inBuild(m) && !parked ? '' : 'opacity-45'"
        :title="parked ? 'No tiled feeds to filter' : inBuild(m) ? '' : 'No ' + m + ' routes in this build'"
      >
        <span class="flex items-center gap-2">
          <span class="size-2.5 shrink-0 rounded-full" :style="classDot(m)" />
          <span class="capitalize">{{ m }}</span>
        </span>
        <Switch :model-value="!classesOff.has(m)" :disabled="parked" @update:model-value="(v) => toggleClass(m, v)" />
      </label>
      <div class="mb-1 mt-3 text-[10px] font-medium uppercase tracking-wider text-muted-foreground/70">Debug</div>
      <label v-for="(_, k) in debug" :key="k" class="flex items-center justify-between py-1 text-sm">
        <span class="capitalize">{{ k }}</span>
        <Switch :model-value="debug[k]" @update:model-value="(v) => (debug[k] = v)" />
      </label>
    </div>

    <button
      class="pointer-events-auto absolute z-10 flex items-center gap-2 rounded-lg border border-border bg-card/90 px-2.5 py-1.5 font-mono text-xs shadow-sm backdrop-blur transition-colors hover:bg-accent"
      :class="inspect ? 'bottom-4 right-4 max-sm:hidden sm:right-[19.5rem]' : 'bottom-4 right-4'"
      :title="`Copy ${viewText} to the clipboard`"
      @click="copyView"
    >
      <span class="tabular-nums text-muted-foreground">z</span>
      <span class="tabular-nums font-medium">{{ view.zoom.toFixed(2) }}</span>
      <span class="h-3.5 w-px bg-border" />
      <span class="tabular-nums">{{ view.lat.toFixed(5) }}, {{ view.lon.toFixed(5) }}</span>
      <template v-if="view.bearing || view.pitch">
        <span class="h-3.5 w-px bg-border" />
        <span class="tabular-nums text-muted-foreground">{{ view.bearing.toFixed(0) }}° / {{ view.pitch.toFixed(0) }}°</span>
      </template>
      <Check v-if="viewCopied" class="size-3.5 text-[var(--success)]" />
      <Copy v-else class="size-3.5 text-muted-foreground" />
    </button>

    <div
      v-if="inspect"
      class="pointer-events-auto absolute bottom-4 right-4 z-10 max-h-[calc(100%-6rem)] w-72 overflow-y-auto rounded-xl border border-border bg-card/95 p-3 shadow-lg backdrop-blur max-sm:left-4 max-sm:w-auto"
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
