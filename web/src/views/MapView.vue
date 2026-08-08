<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, watch, computed } from 'vue'
import { Layers, Crosshair, RefreshCw, Clock } from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Switch from '@/components/ui/Switch.vue'
import Select from '@/components/ui/Select.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { api, fetchBuild, type Scenario, type StyleSet } from '@/lib/api'
import { applyDynamic, activePredicate } from '@/lib/dynamic'
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
const scenario = ref('__all')
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

// ALL is a sentinel, not the empty string: reka-ui reads '' as "nothing
// selected", so an empty-valued option shows the placeholder instead of
// its label and can never be re-selected — switching back to the union
// map would be impossible.
const ALL = '__all'
const scenarioOptions = computed(() => [
  { value: ALL, label: 'All service' },
  ...scenarios.value.filter((s) => s.built).map((s) => ({ value: s.id, label: s.label })),
])
const scenarioId = computed(() => (scenario.value === ALL ? undefined : scenario.value))

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
watch(when, (v) => {
  if (v && scenario.value !== ALL) scenario.value = ALL // dynamic runs on the union
  else applyBands()
})

const activeAt = computed(() => {
  if (!when.value) return null
  const d = new Date(when.value)
  if (Number.isNaN(d.getTime())) return null
  return activePredicate(masks.value, d)
})

// how much of the network is running right now — the banner's summary
const runningCount = computed(() => {
  const pred = activeAt.value
  if (!pred) return null
  const ids = Object.keys(masks.value)
  return { on: ids.filter((r) => pred([r])).length, total: ids.length }
})

async function loadMasks() {
  masks.value = feed.value ? await api.activity(feed.value).catch(() => ({})) : {}
}

// Choosing a map directly and choosing a time are two ways to say the
// same thing, so picking from the list drops the timestamp rather than
// leaving the two controls disagreeing on screen.
function pickScenario(id: string) {
  when.value = ''
  scenario.value = id
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
    const { data, stats } = await fetchBuild(feed.value, key, scenarioId.value)
    bandRaw.set(key, data)
    applyBand(key)
    transferred.value = stats
  } catch {
    loadedBands.delete(key) // let a later zoom retry
  }
}

/** push one band to the map, through the dynamic filter when a time is set */
function applyBand(key: number) {
  const raw = bandRaw.get(key)
  if (!raw || !map) return
  const pred = activeAt.value
  map.getSource(`build-${key}`)?.setData(pred && !scenarioId.value ? applyDynamic(raw, pred) : raw)
}

function applyBands() {
  for (const key of bandRaw.keys()) applyBand(key)
}

// time changes re-filter the cached bands in memory — instant, no fetch
watch([activeAt, masks], applyBands)

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
  scenario.value = '__all'
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
    for (const n of ['rail', 'paths', 'trackcenter', 'nodes']) {
      map.addSource(n, { type: 'geojson', data: empty })
    }
    styleSet.value = feed.value ? await api.style(feed.value).catch(() => null) : null
    addLayers()
    // crossing a band boundary pulls that band in the first time
    map.on('zoomend', ensureBand)
    await reload()
  })
  // the scenario list does not wait on the map: it is a plain API call,
  // and burying it in the load handler meant the picker stayed empty
  // whenever WebGL was slow or unavailable
  loadScenarios()
  loadMasks()
})

onBeforeUnmount(() => {
  ro?.disconnect()
  map?.remove()
})

watch(feed, async () => {
  loadMasks()
  await loadScenarios()
  await reload()
})
watch(scenario, reload)
</script>

<template>
  <div class="relative h-full">
    <!-- sized, not positioned: maplibre-gl.css sets .maplibregl-map to
         position:relative, which beats an `absolute inset-0` here and
         collapses the container to zero height. -->
    <div ref="el" class="h-full w-full" />

    <!-- floating controls: the map is the page, so chrome overlays it -->
    <div class="pointer-events-none absolute inset-x-0 top-0 z-10 flex items-start justify-between gap-3 p-4">
      <div class="pointer-events-auto flex items-center gap-2 rounded-xl border border-border bg-card/90 px-3 py-2 shadow-sm backdrop-blur">
        <span class="text-sm font-medium">{{ currentCity?.name || 'No city' }}</span>
        <Badge v-if="loading" variant="info"><Spinner class="size-3" /> loading</Badge>
        <Button variant="ghost" size="icon" title="Reload" @click="reload"><RefreshCw class="size-4" /></Button>
        <Button variant="ghost" size="icon" title="Fit to city" @click="fitCity"><Crosshair class="size-4" /></Button>
      </div>

      <div class="pointer-events-auto flex flex-col items-end gap-2">
        <div class="flex items-center gap-2 rounded-xl border border-border bg-card/90 px-3 py-2 shadow-sm backdrop-blur">
          <Clock class="size-4 shrink-0 text-muted-foreground" />
          <input
            v-model="when"
            type="datetime-local"
            class="h-8 rounded-md border border-input bg-transparent px-2 text-sm [color-scheme:dark]"
          />
          <Button variant="ghost" size="sm" title="Now" @click="when = localNow()">now</Button>
          <Button
            v-if="when"
            variant="ghost"
            size="sm"
            title="Clear the time — show every service that ever runs"
            @click="when = ''"
          >
            all service
          </Button>
        </div>

        <div
          v-if="!when"
          class="rounded-xl border border-border bg-card/90 px-3 py-2 text-xs shadow-sm backdrop-blur"
        >
          <div class="flex items-center gap-2">
            <Badge variant="muted">all service</Badge>
            <span class="text-muted-foreground">every pattern that ever runs — set a time to narrow it</span>
          </div>
        </div>

        <div
          v-else
          class="max-w-sm rounded-xl border border-border bg-card/90 px-3 py-2 text-xs shadow-sm backdrop-blur"
        >
          <div v-if="runningCount && runningCount.on > 0" class="flex items-center gap-2">
            <Badge variant="success">dynamic</Badge>
            <span class="font-medium tabular-nums">{{ runningCount.on }} of {{ runningCount.total }} routes running</span>
          </div>
          <div v-else-if="runningCount" class="text-muted-foreground">No service at this hour.</div>
          <div v-else class="text-muted-foreground">Loading service calendar…</div>
          <div v-if="resolved" class="mt-1 truncate text-muted-foreground">{{ resolved.label }}</div>
        </div>

        <Select
          v-if="scenarioOptions.length > 1"
          :model-value="scenario"
          :options="scenarioOptions"
          class="w-56 bg-card/90 backdrop-blur"
          @update:model-value="pickScenario"
        />
      </div>
    </div>

    <div class="pointer-events-auto absolute bottom-4 left-4 z-10 w-56 rounded-xl border border-border bg-card/90 p-3 shadow-sm backdrop-blur">
      <div class="mb-2 flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        <Layers class="size-3.5" /> Debug layers
      </div>
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
