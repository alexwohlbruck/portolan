<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, watch, computed } from 'vue'
import { Layers, Crosshair, RefreshCw } from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Switch from '@/components/ui/Switch.vue'
import Select from '@/components/ui/Select.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { api, buildURL, type Scenario, type StyleSet } from '@/lib/api'
import { feed, currentCity } from '@/lib/store'
import { toast } from '@/lib/toast'

// MapLibre comes from the FORK the server exposes at /vendor — it carries
// variable line-offset along line-progress, which is what draws a ribbon
// sliding between slots. The npm build cannot render portolan's output.
declare const maplibregl: any

const el = ref<HTMLDivElement | null>(null)
const loading = ref(true)
const scenarios = ref<Scenario[]>([])
const scenario = ref('')
const styleSet = ref<StyleSet | null>(null)
const inspect = ref<Record<string, any> | null>(null)
let map: any = null
let ro: ResizeObserver | null = null

// Zoom bands: FAIR emits one copy of the map per band, and exactly one
// band may be visible at a time or every ribbon doubles.
const BANDS = [
  { min: 15, max: 24 },
  { min: 14, max: 15 },
  { min: 13, max: 14 },
  { min: 0, max: 13 },
]
const debug = ref({ paths: false, trackcenter: false, nodes: false, rail: false })

const scenarioOptions = computed(() => [
  { value: '', label: 'All service' },
  ...scenarios.value.filter((s) => s.built).map((s) => ({ value: s.id, label: s.label })),
])

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

function addLayers() {
  const { w, o } = modeExprs(styleSet.value)
  const COLOR = ['concat', '#', ['get', 'route_color']]
  const off = [
    'interpolate', ['linear'], ['line-progress'],
    0, ['get', 'off_from_px'], 1, ['get', 'off_to_px'],
  ]
  for (const b of BANDS) {
    const id = `ribbon-${b.min}`
    map.addLayer({
      id,
      type: 'line',
      source: 'build',
      minzoom: b.min,
      maxzoom: b.max,
      filter: ['all', ['==', ['get', 'band_min'], b.min], ['==', ['get', 'band_max'], b.max]],
      layout: { 'line-cap': 'round', 'line-join': 'round' },
      paint: { 'line-color': COLOR, 'line-width': widthExpr(w), 'line-opacity': o, 'line-offset': off },
    })
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
    const src = map.getSource('build')
    if (src) src.setData(buildURL(feed.value, scenario.value || undefined))
    const { w, o } = modeExprs(styleSet.value)
    for (const b of BANDS) {
      const id = `ribbon-${b.min}`
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
  scenario.value = ''
  if (!feed.value) return
  try {
    const r = await api.scenarios(feed.value)
    if (r.available && r.scenarios) scenarios.value = r.scenarios
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
    map.addSource('build', { type: 'geojson', data: empty, lineMetrics: true })
    for (const n of ['rail', 'paths', 'trackcenter', 'nodes']) {
      map.addSource(n, { type: 'geojson', data: empty })
    }
    styleSet.value = feed.value ? await api.style(feed.value).catch(() => null) : null
    addLayers()
    await loadScenarios()
    await reload()
  })
})

onBeforeUnmount(() => {
  ro?.disconnect()
  map?.remove()
})

watch(feed, async () => {
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

      <div class="pointer-events-auto flex items-center gap-2">
        <Select
          v-if="scenarioOptions.length > 1"
          v-model="scenario"
          :options="scenarioOptions"
          class="w-56 bg-card/90 backdrop-blur"
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
