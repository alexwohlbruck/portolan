<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, watch, computed, nextTick } from 'vue'
import {
  Pen, Undo2, Redo2, Scissors, Merge, ArrowLeftRight, Trash2, Plus, X, Save, Eye, EyeOff, PanelRight,
} from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Input from '@/components/ui/Input.vue'
import Switch from '@/components/ui/Switch.vue'
import Spinner from '@/components/ui/Spinner.vue'
import GlobalNotice from '@/components/GlobalNotice.vue'
import { feed, currentFeed, isGlobal } from '@/lib/store'
import { basemapTiles, isDark } from '@/lib/theme'
import { toast } from '@/lib/toast'
import {
  History, bake, bakeAll, deleteAnchor, displayColor, handlesOf, insertAnchor, isCorner,
  lineLabel, mergeInto, reverseLine, splitAt, toggleCorner, trimDuplicateEnds, uid,
  type Anchor, type LL, type Network, type SketchLine,
} from '@/lib/sketch'

declare const maplibregl: any

const el = ref<HTMLDivElement | null>(null)
const net = ref<Network>({ feed: '', lines: [] })
const selId = ref<string | null>(null)
const selAnchor = ref(-1)
const tool = ref<'idle' | 'draw' | 'merge'>('idle')
const status = ref('')
const loading = ref(true)
const saving = ref(false)
const showOverlay = ref(true)
const newColor = ref('D82233')
const newLabel = ref('')
const addLabel = ref('')
const addHex = ref('')
const panelOpen = ref(true)

let map: any = null
let ro: ResizeObserver | null = null
const hist = new History()

const HIT_PX = 9
const selected = computed(() => net.value.lines.find((l) => l.id === selId.value) ?? null)

// ── persistence ───────────────────────────────────────────────────────
let saveTimer: number | undefined
function changed(rebake = true) {
  if (rebake) bakeAll(net.value)
  render()
  window.clearTimeout(saveTimer)
  saveTimer = window.setTimeout(save, 900)
}

async function save() {
  saving.value = true
  try {
    const r = await fetch('/api/network', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(net.value),
    })
    if (!r.ok) throw new Error(await r.text())
    status.value = 'saved ' + new Date().toLocaleTimeString()
  } catch (e: any) {
    status.value = 'SAVE FAILED'
    toast({ title: 'Could not save sketch', description: e.message, variant: 'error' })
  } finally {
    saving.value = false
  }
}

async function load() {
  if (!feed.value || isGlobal.value) {
    // sketches are per-feed; clear the canvas so a stray edit cannot
    // autosave into the previously selected feed's document
    window.clearTimeout(saveTimer)
    net.value = { feed: '', lines: [] }
    hist.reset()
    selId.value = null
    selAnchor.value = -1
    tool.value = 'idle'
    render()
    loading.value = false
    return
  }
  loading.value = true
  try {
    const r = await fetch('/api/network?feed=' + encodeURIComponent(feed.value))
    const doc = await r.json()
    net.value = { feed: feed.value, updated: doc.updated, lines: doc.lines ?? [] }
    bakeAll(net.value)
    hist.reset()
    selId.value = null
    selAnchor.value = -1
    tool.value = 'idle'
    render()
    if (map?.isStyleLoaded?.()) fitFeed()
  } catch (e: any) {
    toast({ title: 'Could not load sketch', description: e.message, variant: 'error' })
  } finally {
    loading.value = false
  }
}

function push() {
  hist.push(net.value)
}
function undo() {
  const n = hist.undo(net.value)
  if (n) afterHistory(n)
}
function redo() {
  const n = hist.redo(net.value)
  if (n) afterHistory(n)
}
function afterHistory(n: Network) {
  net.value = n
  bakeAll(net.value)
  selAnchor.value = -1
  if (tool.value === 'draw') endDraw(true, false)
  render()
  window.clearTimeout(saveTimer)
  saveTimer = window.setTimeout(save, 900)
}

// ── rendering ─────────────────────────────────────────────────────────
// Everything is GeoJSON: MapLibre has no marker-per-vertex model, and at
// 400 anchors a source swap is far cheaper than 400 DOM markers anyway.
const fc = (features: any[]) => ({ type: 'FeatureCollection', features })

function render() {
  if (!map || !map.getSource('sk-lines')) return
  const lines = net.value.lines.map((l) => ({
    type: 'Feature',
    properties: {
      id: l.id,
      color: displayColor(l),
      width: l.id === selId.value ? 6 : 4,
      label: lineLabel(l),
    },
    geometry: { type: 'LineString', coordinates: l.coords },
  }))
  map.getSource('sk-lines').setData(fc(lines))

  const anchors: any[] = []
  const handles: any[] = []
  const leaders: any[] = []
  const l = selected.value
  for (const ln of net.value.lines) {
    const isSel = ln.id === selId.value && tool.value !== 'draw'
    for (let i = 0; i < ln.anchors.length; i++) {
      const a = ln.anchors[i]
      anchors.push({
        type: 'Feature',
        properties: {
          line: ln.id,
          i,
          sel: isSel ? 1 : 0,
          active: isSel && i === selAnchor.value ? 1 : 0,
          end: i === 0 || i === ln.anchors.length - 1 ? 1 : 0,
        },
        geometry: { type: 'Point', coordinates: a.p },
      })
    }
  }
  if (l && tool.value !== 'draw') {
    for (let i = 0; i < l.anchors.length; i++) {
      if (isCorner(l.anchors[i])) continue
      const H = handlesOf(l, i)
      const kinds: ('hin' | 'hout')[] = []
      if (i > 0) kinds.push('hin')
      if (i < l.anchors.length - 1) kinds.push('hout')
      for (const kk of kinds) {
        handles.push({
          type: 'Feature',
          properties: { i, kk },
          geometry: { type: 'Point', coordinates: H[kk] },
        })
        leaders.push({
          type: 'Feature',
          properties: {},
          geometry: { type: 'LineString', coordinates: [l.anchors[i].p, H[kk]] },
        })
      }
    }
  }
  map.getSource('sk-anchors').setData(fc(anchors))
  map.getSource('sk-handles').setData(fc(handles))
  map.getSource('sk-leaders').setData(fc(leaders))
}

function setPreview(coords: LL[] | null) {
  if (!map?.getSource('sk-preview')) return
  map.getSource('sk-preview').setData(
    coords && coords.length > 1
      ? fc([{ type: 'Feature', properties: {}, geometry: { type: 'LineString', coordinates: coords } }])
      : fc([]),
  )
}

// ── hit testing ───────────────────────────────────────────────────────
type Hit =
  | { kind: 'handle'; l: SketchLine; i: number; kk: 'hin' | 'hout' }
  | { kind: 'anchor'; l: SketchLine; i: number }
  | { kind: 'dot'; l: SketchLine; i: number }

function px(ll: LL) {
  return map.project({ lng: ll[0], lat: ll[1] })
}
function dist(a: { x: number; y: number }, b: { x: number; y: number }) {
  return Math.hypot(a.x - b.x, a.y - b.y)
}

/** Priority: the selected line's handles, then its anchors, then any
 *  other line's anchors. Handles first or you could never grab one that
 *  sits on top of its anchor. */
function hitTest(pt: { x: number; y: number }): Hit | null {
  const l = selected.value
  if (l && tool.value !== 'draw') {
    for (let i = 0; i < l.anchors.length; i++) {
      if (isCorner(l.anchors[i])) continue
      const H = handlesOf(l, i)
      if (i > 0 && dist(pt, px(H.hin)) <= HIT_PX) return { kind: 'handle', l, i, kk: 'hin' }
      if (i < l.anchors.length - 1 && dist(pt, px(H.hout)) <= HIT_PX)
        return { kind: 'handle', l, i, kk: 'hout' }
    }
    for (let i = 0; i < l.anchors.length; i++)
      if (dist(pt, px(l.anchors[i].p)) <= HIT_PX) return { kind: 'anchor', l, i }
  }
  for (const ln of net.value.lines) {
    if (ln.id === selId.value) continue
    for (let i = 0; i < ln.anchors.length; i++)
      if (dist(pt, px(ln.anchors[i].p)) <= HIT_PX) return { kind: 'dot', l: ln, i }
  }
  return null
}

// ── dragging ──────────────────────────────────────────────────────────
let drag: any = null
let altHeld = false

function onDown(e: any) {
  if (tool.value === 'draw') return drawDown(e)
  if (e.originalEvent.button !== 0) return
  const hit = hitTest(e.point)
  if (!hit) return
  e.preventDefault()
  map.dragging?.disable?.()
  map.dragPan.disable()

  if (hit.kind === 'dot') {
    selId.value = hit.l.id
    selAnchor.value = hit.i
    render()
    map.dragPan.enable()
    return
  }
  push()
  drag = { ...hit, moved: false, alt: altHeld || e.originalEvent.altKey }
  if (hit.kind === 'anchor') {
    selAnchor.value = hit.i
    render()
  } else {
    // "linked" = the two handles are near-opposite, i.e. the anchor reads
    // as smooth. Dragging one then rotates the other to stay opposite,
    // keeping ITS OWN length (captured now, not recomputed per move).
    const a = hit.l.anchors[hit.i]
    const H = handlesOf(hit.l, hit.i)
    const v1 = [H.hin[0] - a.p[0], H.hin[1] - a.p[1]]
    const v2 = [H.hout[0] - a.p[0], H.hout[1] - a.p[1]]
    const l1 = Math.hypot(v1[0], v1[1])
    const l2 = Math.hypot(v2[0], v2[1])
    drag.linked =
      l1 > 1e-12 && l2 > 1e-12 && (v1[0] * v2[0] + v1[1] * v2[1]) / (l1 * l2) < -0.985 && !drag.alt
    drag.otherLen = hit.kk === 'hin' ? l2 : l1
  }
}

function onMove(e: any) {
  if (tool.value === 'draw') return drawMove(e)
  if (!drag) return
  const np: LL = [e.lngLat.lng, e.lngLat.lat]
  const a: Anchor = drag.l.anchors[drag.i]
  drag.moved = true
  if (altHeld || e.originalEvent.altKey) drag.alt = true

  if (drag.kind === 'anchor') {
    if (drag.alt) {
      // alt-drag on an anchor pulls fresh mirrored handles out of it
      a.hout = np
      a.hin = [2 * a.p[0] - np[0], 2 * a.p[1] - np[1]]
    } else {
      const dp = [np[0] - a.p[0], np[1] - a.p[1]]
      if (a.hin) a.hin = [a.hin[0] + dp[0], a.hin[1] + dp[1]]
      if (a.hout) a.hout = [a.hout[0] + dp[0], a.hout[1] + dp[1]]
      a.p = np
    }
  } else {
    if (drag.alt) drag.linked = false
    ;(a as any)[drag.kk] = np
    if (drag.linked) {
      const vx = np[0] - a.p[0]
      const vy = np[1] - a.p[1]
      const vlen = Math.hypot(vx, vy)
      if (vlen > 1e-12) {
        const other = drag.kk === 'hin' ? 'hout' : 'hin'
        ;(a as any)[other] = [
          a.p[0] - (vx / vlen) * drag.otherLen,
          a.p[1] - (vy / vlen) * drag.otherLen,
        ]
      }
    }
  }
  bake(drag.l)
  render()
}

function onUp() {
  if (!drag) return
  if (!drag.moved) hist.discard()
  else changed()
  drag = null
  map.dragPan.enable()
  render()
}

// ── pen ───────────────────────────────────────────────────────────────
let drawTarget: { id: string; end: 'head' | 'tail' } | null = null
let pull: { l: SketchLine; a: Anchor } | null = null

function startDraw() {
  push()
  const l: SketchLine = {
    id: uid(),
    routes: [{ label: newLabel.value, color: newColor.value.replace('#', '') }],
    anchors: [],
    coords: [],
  }
  net.value.lines.push(l)
  selId.value = l.id
  selAnchor.value = -1
  drawTarget = { id: l.id, end: 'tail' }
  tool.value = 'draw'
  map.dragPan.disable()
  status.value = 'click = corner · click-drag = curve · space = pan · Enter finishes'
  render()
}

function startExtend(l: SketchLine, end: 'head' | 'tail') {
  push()
  selId.value = l.id
  drawTarget = { id: l.id, end }
  tool.value = 'draw'
  map.dragPan.disable()
  status.value = `extending from ${end} — Enter finishes`
  render()
}

/** snap a new pen node to another line's endpoint within ~10px, so two
 *  drawn lines that should meet actually share a coordinate. */
function snapPoint(p: LL): LL {
  const q = px(p)
  for (const l of net.value.lines) {
    if (drawTarget && l.id === drawTarget.id) continue
    for (const a of [l.anchors[0], l.anchors[l.anchors.length - 1]]) {
      if (!a) continue
      if (dist(q, px(a.p)) < 10) return [...a.p] as LL
    }
  }
  return p
}

function drawDown(e: any) {
  if (e.originalEvent.button !== 0 || !drawTarget) return
  const l = net.value.lines.find((x) => x.id === drawTarget!.id)
  if (!l) return
  e.preventDefault()
  push()
  const p = snapPoint([e.lngLat.lng, e.lngLat.lat])
  const a: Anchor = { p, hin: [...p] as LL, hout: [...p] as LL } // corner until dragged
  if (drawTarget.end === 'tail') l.anchors.push(a)
  else l.anchors.unshift(a)
  pull = { l, a }
  bake(l)
  render()
}

function drawMove(e: any) {
  const cur: LL = [e.lngLat.lng, e.lngLat.lat]
  if (pull) {
    const a = pull.a
    if (drawTarget!.end === 'tail') {
      a.hout = cur
      a.hin = [2 * a.p[0] - cur[0], 2 * a.p[1] - cur[1]]
    } else {
      a.hin = cur
      a.hout = [2 * a.p[0] - cur[0], 2 * a.p[1] - cur[1]]
    }
    bake(pull.l)
    render()
    return
  }
  // rubber band that continues the previous curvature
  const l = net.value.lines.find((x) => x.id === drawTarget?.id)
  if (!l || !l.anchors.length) return setPreview(null)
  const li = drawTarget!.end === 'tail' ? l.anchors.length - 1 : 0
  const last = l.anchors[li]
  const H = handlesOf(l, li)
  const h = drawTarget!.end === 'tail' ? H.hout : H.hin
  const pts: LL[] = []
  for (let s = 0; s <= 14; s++) {
    const t = s / 14
    const u = 1 - t
    pts.push([
      u * u * u * last.p[0] + 3 * u * u * t * h[0] + 3 * u * t * t * cur[0] + t * t * t * cur[0],
      u * u * u * last.p[1] + 3 * u * u * t * h[1] + 3 * u * t * t * cur[1] + t * t * t * cur[1],
    ])
  }
  setPreview(pts)
}

function drawUp(e: any) {
  if (!pull) return
  // a handle released within 4px of its anchor was a click, not a drag —
  // snap it back to a hard corner
  const a = pull.a
  const H = handlesOf(pull.l, pull.l.anchors.indexOf(a))
  if (dist(px(a.p), px(H.hout)) < 4) {
    a.hin = [...a.p] as LL
    a.hout = [...a.p] as LL
  }
  bake(pull.l)
  pull = null
  changed()
}

function endDraw(discard: boolean, record = true) {
  const l = net.value.lines.find((x) => x.id === drawTarget?.id)
  if (l) {
    trimDuplicateEnds(l)
    if (l.anchors.length < 2) {
      net.value.lines = net.value.lines.filter((x) => x.id !== l.id)
      if (selId.value === l.id) selId.value = null
    } else bake(l)
  }
  drawTarget = null
  pull = null
  setPreview(null)
  tool.value = 'idle'
  map?.dragPan.enable()
  status.value = ''
  if (record && !discard) changed()
  else render()
}

// ── line-level operations ─────────────────────────────────────────────
function onLineClick(e: any) {
  const f = e.features?.[0]
  if (!f) return
  const l = net.value.lines.find((x) => x.id === f.properties.id)
  if (!l) return
  if (tool.value === 'merge' && selected.value && l.id !== selId.value) {
    push()
    mergeInto(net.value, selected.value, l)
    tool.value = 'idle'
    status.value = ''
    changed()
    return
  }
  if (tool.value === 'idle' && l.id === selId.value) {
    push()
    selAnchor.value = insertAnchor(l, [e.lngLat.lng, e.lngLat.lat])
    changed()
    return
  }
  selId.value = l.id
  selAnchor.value = -1
  render()
}

function doSplit() {
  const l = selected.value
  if (!l) return
  push()
  if (!splitAt(net.value, l, selAnchor.value)) {
    hist.discard()
    status.value = 'select an interior anchor first'
    return
  }
  selAnchor.value = -1
  changed()
}

function doReverse() {
  const l = selected.value
  if (!l) return
  push()
  reverseLine(l)
  changed()
}

function deleteLine(l: SketchLine) {
  push()
  net.value.lines = net.value.lines.filter((x) => x.id !== l.id)
  if (selId.value === l.id) selId.value = null
  changed()
}

function addRoute() {
  const l = selected.value
  if (!l) return
  push()
  l.routes.push({ label: addLabel.value, color: (addHex.value || '888888').replace('#', '') })
  addLabel.value = ''
  addHex.value = ''
  changed(false)
}

function removeRoute(i: number) {
  const l = selected.value
  if (!l) return
  push()
  l.routes.splice(i, 1)
  if (!l.routes.length) l.routes.push({ label: '', color: '888888' })
  changed(false)
}

function selectLine(l: SketchLine) {
  selId.value = l.id
  selAnchor.value = -1
  const mid = l.coords[Math.floor(l.coords.length / 2)]
  if (mid) map.panTo({ lng: mid[0], lat: mid[1] })
  render()
}

// ── keyboard ──────────────────────────────────────────────────────────
function onKey(e: KeyboardEvent) {
  if (e.key === 'Alt') altHeld = true
  const t = e.target as HTMLElement
  if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA')) return
  const mod = e.metaKey || e.ctrlKey

  if ((e.key === 'z' && !mod) || (mod && e.key === 'z' && !e.shiftKey)) return void undo()
  if ((mod && e.key === 'z' && e.shiftKey) || (mod && e.key === 'y') || (!mod && e.key === 'Z'))
    return void redo()
  if (mod) return // never steal Cmd+C and friends
  const k = e.key.toLowerCase()
  if ((k === 'd' || k === 'n') && tool.value !== 'draw') return startDraw()
  if (k === 'c' && selected.value && tool.value !== 'draw') {
    const l = selected.value
    const end = selAnchor.value === 0 ? 'head' : 'tail'
    return startExtend(l, end)
  }
  if (k === 'x') return doSplit()
  if (k === 'm' && selected.value) {
    tool.value = 'merge'
    status.value = 'click the other line'
    return
  }
  if (e.key === 'Enter') return void (tool.value === 'draw' && endDraw(false))
  if (e.key === 'Escape') {
    if (tool.value === 'draw') return endDraw(true)
    selId.value = null
    selAnchor.value = -1
    tool.value = 'idle'
    return render()
  }
  if ((e.key === 'Backspace' || e.key === 'Delete') && selected.value) return deleteLine(selected.value)
}
function onKeyUp(e: KeyboardEvent) {
  if (e.key === 'Alt') altHeld = false
}

// ── overlay (the built map, for tracing against) ──────────────────────
async function loadOverlay() {
  if (!map?.getSource('sk-overlay')) return
  // the built-map underlay is per-feed too — never fetch with build=global
  if (!showOverlay.value || !feed.value || isGlobal.value) return setOverlay([])
  const c = map.getCenter()
  try {
    const r = await fetch(`/api/features?lat=${c.lat}&lon=${c.lng}&r=1200&build=${encodeURIComponent(feed.value)}`)
    const gj = await r.json()
    setOverlay(gj.features ?? [])
  } catch {
    setOverlay([])
  }
}
const setOverlay = (features: any[]) => map.getSource('sk-overlay').setData(fc(features))

function fitFeed() {
  const b = currentFeed.value?.bbox
  if (map && b?.length === 4) map.fitBounds([[b[0], b[1]], [b[2], b[3]]], { padding: 40, duration: 0 })
}

// The basemap is only ever a tracing underlay here, so it sits fainter
// than the map view's — but the light tiles still need more of it than
// the dark ones to be legible at all (lib/theme.ts).
const rasterOpacity = () => (isDark.value ? 0.5 : 0.9)
watch(isDark, () => {
  if (!map) return
  map.getSource('osm')?.setTiles([basemapTiles()])
  map.setPaintProperty('osm', 'raster-opacity', rasterOpacity())
})

onMounted(() => {
  if (typeof maplibregl === 'undefined') {
    toast({ title: 'MapLibre not loaded', variant: 'error' })
    loading.value = false
    return
  }
  map = new maplibregl.Map({
    container: el.value!,
    center: [-73.98, 40.75],
    zoom: 14,
    doubleClickZoom: false,
    style: {
      version: 8,
      sources: {
        osm: {
          type: 'raster',
          tiles: [basemapTiles()],
          tileSize: 256,
          attribution: '© OpenStreetMap © CARTO',
        },
      },
      layers: [{ id: 'osm', type: 'raster', source: 'osm', paint: { 'raster-opacity': rasterOpacity() } }],
    },
  })
  ro = new ResizeObserver(() => map?.resize())
  ro.observe(el.value!)

  map.on('load', () => {
    for (const id of ['sk-overlay', 'sk-lines', 'sk-leaders', 'sk-anchors', 'sk-handles', 'sk-preview'])
      map.addSource(id, { type: 'geojson', data: fc([]) })

    map.addLayer({
      id: 'ov', type: 'line', source: 'sk-overlay',
      paint: {
        'line-color': ['concat', '#', ['coalesce', ['get', 'color'], '888888']],
        'line-width': 3, 'line-opacity': 0.4,
      },
    })
    map.addLayer({
      id: 'sk-line', type: 'line', source: 'sk-lines',
      layout: { 'line-cap': 'round', 'line-join': 'round' },
      paint: { 'line-color': ['get', 'color'], 'line-width': ['get', 'width'], 'line-opacity': 0.95 },
    })
    map.addLayer({
      id: 'sk-preview-l', type: 'line', source: 'sk-preview',
      paint: { 'line-color': '#f6bc26', 'line-width': 2, 'line-dasharray': [3, 3], 'line-opacity': 0.85 },
    })
    map.addLayer({
      id: 'sk-leader', type: 'line', source: 'sk-leaders',
      paint: { 'line-color': '#8ad', 'line-width': 1.2, 'line-opacity': 0.9 },
    })
    // end anchors read as round + larger, interior as small squares — the
    // pen extends from ends, so they must be distinguishable at a glance
    map.addLayer({
      id: 'sk-anchor', type: 'circle', source: 'sk-anchors',
      paint: {
        'circle-radius': ['case', ['==', ['get', 'sel'], 1], ['case', ['==', ['get', 'end'], 1], 6.5, 5], 3.5],
        'circle-color': ['case', ['==', ['get', 'active'], 1], '#f6bc26', '#fff'],
        'circle-stroke-color': '#333',
        'circle-stroke-width': ['case', ['==', ['get', 'sel'], 1], 2, 1.5],
      },
    })
    map.addLayer({
      id: 'sk-handle', type: 'circle', source: 'sk-handles',
      paint: { 'circle-radius': 5, 'circle-color': '#8ad', 'circle-stroke-color': '#fff', 'circle-stroke-width': 1.5 },
    })

    map.on('mousedown', onDown)
    map.on('mousemove', onMove)
    map.on('mouseup', (e: any) => (tool.value === 'draw' ? drawUp(e) : onUp()))
    map.on('click', 'sk-line', onLineClick)
    map.on('dblclick', (e: any) => {
      if (tool.value === 'draw') return endDraw(false)
      const hit = hitTest(e.point)
      if (hit && hit.kind !== 'handle') {
        push()
        toggleCorner(hit.l.anchors[hit.i])
        changed()
      }
    })
    map.on('contextmenu', (e: any) => {
      const hit = hitTest(e.point)
      if (!hit) return
      e.preventDefault()
      push()
      if (!deleteAnchor(net.value, hit.l, hit.i) && selId.value === hit.l.id) selId.value = null
      selAnchor.value = -1
      changed()
    })
    map.on('moveend', loadOverlay)

    // the document is already loaded (see below) — draw it and fetch the
    // build underlay now that there are layers to put them in
    render()
    fitFeed()
    loadOverlay()
  })

  // Loading the sketch does NOT wait on the map. render() no-ops until the
  // layers exist, so the line list, history and saving all work even if
  // WebGL never comes up — which is exactly when you most want to see that
  // your drawing is still there.
  load()
  window.addEventListener('keydown', onKey)
  window.addEventListener('keyup', onKeyUp)
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKey)
  window.removeEventListener('keyup', onKeyUp)
  window.clearTimeout(saveTimer)
  ro?.disconnect()
  map?.remove()
})

watch(feed, () => load().then(loadOverlay))
watch(showOverlay, loadOverlay)
watch(selId, () => nextTick(render))
</script>

<template>
  <div class="relative flex h-full">
    <!-- the editor stays mounted (WebGL init is not re-entrant here); the
         global context covers it with the shared notice instead -->
    <div
      v-if="isGlobal"
      class="absolute inset-0 z-20 flex items-center justify-center bg-background/80 backdrop-blur-sm"
    >
      <GlobalNotice title="Sketch" class="w-full max-w-2xl" />
    </div>
    <div class="relative min-w-0 flex-1">
      <div ref="el" class="h-full w-full" :class="tool === 'draw' ? 'cursor-crosshair' : ''" />

      <div class="pointer-events-none absolute inset-x-0 top-0 flex items-start justify-between gap-3 p-4">
        <div class="pointer-events-auto flex items-center gap-2 rounded-xl border border-border bg-card/90 px-3 py-2 shadow-sm backdrop-blur">
          <span class="text-sm font-medium">{{ currentFeed?.name || 'No feed' }}</span>
          <Badge v-if="loading" variant="info"><Spinner class="size-3" /> loading</Badge>
          <Badge v-else-if="saving" variant="info"><Spinner class="size-3" /> saving</Badge>
          <Badge v-else-if="status" variant="muted">{{ status }}</Badge>
        </div>
        <div class="pointer-events-auto flex items-center gap-1 rounded-xl border border-border bg-card/90 px-2 py-1.5 shadow-sm backdrop-blur">
          <Button :variant="tool === 'draw' ? 'default' : 'ghost'" size="sm" @click="tool === 'draw' ? endDraw(false) : startDraw()">
            <Pen class="size-4" /> {{ tool === 'draw' ? 'Finish' : 'Draw' }}
          </Button>
          <Button variant="ghost" size="icon" title="Undo (z)" @click="undo"><Undo2 class="size-4" /></Button>
          <Button variant="ghost" size="icon" title="Redo (Z)" @click="redo"><Redo2 class="size-4" /></Button>
          <Button variant="ghost" size="icon" title="Save now" @click="save"><Save class="size-4" /></Button>
          <Button variant="ghost" size="icon" title="Toggle panel" @click="panelOpen = !panelOpen">
            <PanelRight class="size-4" />
          </Button>
        </div>
      </div>

      <div class="absolute bottom-4 left-4 hidden rounded-xl border border-border bg-card/90 p-3 text-xs text-muted-foreground shadow-sm backdrop-blur lg:block">
        <div class="mb-1 font-medium text-foreground">Gestures</div>
        <div>click = corner · click-drag = curve · Enter finishes</div>
        <div>drag anchor to move · alt-drag to pull handles</div>
        <div>right-click deletes an anchor · dbl-click toggles corner</div>
        <div>click a selected line to insert an anchor</div>
        <div class="mt-1">d new · c continue · x split · m merge · ⌫ delete</div>
      </div>
    </div>

    <aside
      v-show="panelOpen"
      class="absolute right-0 top-0 z-20 flex h-full w-80 flex-col overflow-y-auto border-l border-border bg-card shadow-xl xl:static xl:z-auto xl:bg-card/40 xl:shadow-none"
    >
      <div class="border-b border-border p-4">
        <div class="mb-2 flex items-center justify-between">
          <span class="text-sm font-medium">Lines <span class="text-muted-foreground">({{ net.lines.length }})</span></span>
          <label class="flex items-center gap-1.5 text-xs text-muted-foreground">
            <component :is="showOverlay ? Eye : EyeOff" class="size-3.5" />
            build
            <Switch :model-value="showOverlay" @update:model-value="(v) => (showOverlay = v)" />
          </label>
        </div>
        <div class="flex gap-2">
          <Input v-model="newLabel" placeholder="label" class="h-8 text-xs" />
          <Input v-model="newColor" placeholder="hex" class="h-8 w-24 font-mono text-xs" />
        </div>
      </div>

      <div v-if="selected" class="border-b border-border p-4">
        <div class="mb-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">Selected line</div>
        <div class="mb-2 flex flex-wrap gap-1">
          <span
            v-for="(r, ri) in selected.routes"
            :key="ri"
            class="inline-flex items-center gap-1.5 rounded-md border border-border px-2 py-0.5 text-xs"
          >
            <span class="size-3 rounded-sm" :style="{ background: '#' + r.color }" />
            {{ r.label || r.color }}
            <button class="text-muted-foreground hover:text-foreground" @click="removeRoute(ri)"><X class="size-3" /></button>
          </span>
        </div>
        <div class="mb-3 flex gap-1">
          <Input v-model="addLabel" placeholder="label" class="h-8 text-xs" />
          <Input v-model="addHex" placeholder="hex" class="h-8 w-20 font-mono text-xs" />
          <Button variant="outline" size="sm" @click="addRoute"><Plus class="size-4" /></Button>
        </div>
        <div class="flex gap-1">
          <Button variant="outline" size="sm" title="Split at the selected anchor (x)" @click="doSplit">
            <Scissors class="size-4" /> Split
          </Button>
          <Button :variant="tool === 'merge' ? 'default' : 'outline'" size="sm" title="Merge (m)"
                  @click="tool = tool === 'merge' ? 'idle' : 'merge'">
            <Merge class="size-4" /> Merge
          </Button>
          <Button variant="outline" size="sm" title="Reverse direction" @click="doReverse">
            <ArrowLeftRight class="size-4" />
          </Button>
        </div>
      </div>

      <div class="flex-1 p-2">
        <button
          v-for="l in net.lines"
          :key="l.id"
          class="flex w-full items-center gap-2 rounded-lg px-2 py-2 text-left text-sm transition-colors"
          :class="l.id === selId ? 'bg-accent text-accent-foreground' : 'hover:bg-accent/40'"
          @click="selectLine(l)"
        >
          <span class="size-3 shrink-0 rounded-sm" :style="{ background: displayColor(l) }" />
          <span class="min-w-0 flex-1 truncate">{{ lineLabel(l) }}</span>
          <span class="shrink-0 text-xs text-muted-foreground tabular-nums">{{ l.anchors.length }}</span>
          <span class="shrink-0 text-muted-foreground hover:text-destructive" @click.stop="deleteLine(l)">
            <Trash2 class="size-3.5" />
          </span>
        </button>
      </div>
    </aside>
  </div>
</template>
