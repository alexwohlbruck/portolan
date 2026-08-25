<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, watch, computed, nextTick } from 'vue'
import {
  Pen, Undo2, Redo2, Scissors, Merge, ArrowLeftRight, Trash2, X, Save, Eye, EyeOff,
  PanelRight, Squircle, Spline,
} from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Input from '@/components/ui/Input.vue'
import Switch from '@/components/ui/Switch.vue'
import Spinner from '@/components/ui/Spinner.vue'
import GlobalNotice from '@/components/GlobalNotice.vue'
import { feed, currentFeed, isGlobal } from '@/lib/store'
import { basemap, BASEMAP_PROVIDERS, currentBasemap, isDark, PROVIDER_ORDER, providerTiles } from '@/lib/theme'
import { toast } from '@/lib/toast'
import {
  History, bake, bakeAll, curveLabel, deleteAnchor, entrancesOf, handlesOf, insertAnchor,
  isCorner, mergeInto, newCurve, reverseLine, ring, splitAt, toggleCorner, trimDuplicateEnds,
  uid, yardAt, yardLabel,
  type Anchor, type Curve, type LL, type Network, type Yard,
} from '@/lib/sketch'

declare const maplibregl: any

// Ink. The drawing is RED (the owner's pen), the detector's own output is
// BLUE underneath it — the whole point of this page is telling those two
// apart at a glance. Nothing here is per-curve: a sketch records where
// track goes, and that is all it is graded on.
const INK = '#e0453a'
const DET = '#2f80ed'
const ENT = '#f6bc26'

const el = ref<HTMLDivElement | null>(null)
const net = ref<Network>({ feed: '', lines: [], yards: [] })
const mode = ref<'network' | 'yards'>('network')
const selId = ref<string | null>(null)
const selAnchor = ref(-1)
const tool = ref<'idle' | 'draw' | 'merge'>('idle')
const status = ref('')
const loading = ref(true)
const saving = ref(false)
const showOverlay = ref(true)
const showDetected = ref(true)
const newLabel = ref('')
// open where it can sit beside the canvas, closed where it would cover it
const panelOpen = ref(window.matchMedia('(min-width: 1280px)').matches)

let map: any = null
let ro: ResizeObserver | null = null
const hist = new History()

const HIT_PX = 9

// ── the editable set ──────────────────────────────────────────────────
// One selection model over three kinds of curve. `list` is the array the
// curve lives in, so delete/split/merge need no per-kind special case.
type Ref_ = { c: Curve; list: Curve[]; yard: Yard | null; kind: 'line' | 'boundary' | 'centerline' }

function refs(): Ref_[] {
  const out: Ref_[] = []
  if (mode.value === 'network') {
    for (const c of net.value.lines) out.push({ c, list: net.value.lines, yard: null, kind: 'line' })
    return out
  }
  for (const y of net.value.yards) {
    out.push({ c: y.boundary, list: [y.boundary], yard: y, kind: 'boundary' })
    for (const c of y.centerlines) out.push({ c, list: y.centerlines, yard: y, kind: 'centerline' })
  }
  return out
}
const refOf = (id: string | null) => (id ? refs().find((r) => r.c.id === id) ?? null : null)
const sel = computed(() => refOf(selId.value))
const selected = computed(() => sel.value?.c ?? null)
const selYard = computed(() => sel.value?.yard ?? null)

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
    net.value = { feed: '', lines: [], yards: [] }
    reset()
    render()
    loading.value = false
    return
  }
  loading.value = true
  try {
    const r = await fetch('/api/network?feed=' + encodeURIComponent(feed.value))
    const doc = await r.json()
    net.value = {
      feed: feed.value,
      updated: doc.updated,
      lines: doc.lines ?? [],
      yards: (doc.yards ?? []).map((y: Yard) => ({ ...y, centerlines: y.centerlines ?? [] })),
    }
    bakeAll(net.value)
    reset()
    render()
    if (map?.isStyleLoaded?.()) fitFeed()
  } catch (e: any) {
    toast({ title: 'Could not load sketch', description: e.message, variant: 'error' })
  } finally {
    loading.value = false
  }
}

function reset() {
  hist.reset()
  selId.value = null
  selAnchor.value = -1
  tool.value = 'idle'
  drawTarget = null
  pull = null
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
const lineF = (coords: LL[], props: any) => ({
  type: 'Feature', properties: props, geometry: { type: 'LineString', coordinates: coords },
})
const pointF = (p: LL, props: any) => ({
  type: 'Feature', properties: props, geometry: { type: 'Point', coordinates: p },
})

function render() {
  if (!map || !map.getSource('sk-lines')) return

  // strokes: network lines and yard centerlines share a source; the
  // inactive mode's geometry stays visible but dimmed, because a
  // centerline drawn without its city context around it drifts.
  const strokes: any[] = []
  for (const c of net.value.lines)
    strokes.push(lineF(c.coords, {
      id: c.id, width: c.id === selId.value ? 5 : 3,
      dim: mode.value === 'network' ? 0 : 1,
    }))
  for (const y of net.value.yards)
    for (const c of y.centerlines)
      strokes.push(lineF(c.coords, {
        id: c.id, width: c.id === selId.value ? 5 : 3,
        dim: mode.value === 'yards' ? 0 : 1,
      }))
  map.getSource('sk-lines').setData(fc(strokes))

  const rings: any[] = []
  const ents: any[] = []
  for (const y of net.value.yards) {
    const r = ring(y.boundary)
    if (r.length >= 3) {
      rings.push({
        type: 'Feature',
        properties: {
          id: y.boundary.id, yard: y.id,
          width: y.boundary.id === selId.value ? 4 : 2.5,
          dim: mode.value === 'yards' ? 0 : 1,
        },
        geometry: { type: 'Polygon', coordinates: [[...r, r[0]]] },
      })
    }
    for (const e of entrancesOf(y)) ents.push(pointF(e.at, { yard: y.id, n: e.lines.length }))
  }
  map.getSource('sk-yards').setData(fc(rings))
  map.getSource('sk-ents').setData(fc(ents))

  // anchors and handles belong to the active mode only
  const anchors: any[] = []
  const handles: any[] = []
  const leaders: any[] = []
  for (const r of refs()) {
    const isSel = r.c.id === selId.value && tool.value !== 'draw'
    for (let i = 0; i < r.c.anchors.length; i++) {
      const last = r.c.anchors.length - 1
      anchors.push(pointF(r.c.anchors[i].p, {
        i, sel: isSel ? 1 : 0,
        active: isSel && i === selAnchor.value ? 1 : 0,
        end: !r.c.closed && (i === 0 || i === last) ? 1 : 0,
      }))
    }
    if (!isSel) continue
    for (let i = 0; i < r.c.anchors.length; i++) {
      if (isCorner(r.c.anchors[i])) continue
      const H = handlesOf(r.c, i)
      const kinds: ('hin' | 'hout')[] = []
      if (r.c.closed || i > 0) kinds.push('hin')
      if (r.c.closed || i < r.c.anchors.length - 1) kinds.push('hout')
      for (const kk of kinds) {
        handles.push(pointF(H[kk], { i, kk }))
        leaders.push(lineF([r.c.anchors[i].p, H[kk]], {}))
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
    coords && coords.length > 1 ? fc([lineF(coords, {})]) : fc([]),
  )
}

const entCount = (y: Yard) => entrancesOf(y).length

// ── hit testing ───────────────────────────────────────────────────────
type Hit =
  | { kind: 'handle'; r: Ref_; i: number; kk: 'hin' | 'hout' }
  | { kind: 'anchor'; r: Ref_; i: number }
  | { kind: 'dot'; r: Ref_; i: number }

function px(ll: LL) {
  return map.project({ lng: ll[0], lat: ll[1] })
}
function dist(a: { x: number; y: number }, b: { x: number; y: number }) {
  return Math.hypot(a.x - b.x, a.y - b.y)
}

/** Priority: the selected curve's handles, then its anchors, then any
 *  other curve's anchors. Handles first or you could never grab one that
 *  sits on top of its anchor. */
function hitTest(pt: { x: number; y: number }): Hit | null {
  const all = refs()
  const s = all.find((r) => r.c.id === selId.value)
  if (s && tool.value !== 'draw') {
    for (let i = 0; i < s.c.anchors.length; i++) {
      if (isCorner(s.c.anchors[i])) continue
      const H = handlesOf(s.c, i)
      const first = s.c.closed || i > 0
      const last = s.c.closed || i < s.c.anchors.length - 1
      if (first && dist(pt, px(H.hin)) <= HIT_PX) return { kind: 'handle', r: s, i, kk: 'hin' }
      if (last && dist(pt, px(H.hout)) <= HIT_PX) return { kind: 'handle', r: s, i, kk: 'hout' }
    }
    for (let i = 0; i < s.c.anchors.length; i++)
      if (dist(pt, px(s.c.anchors[i].p)) <= HIT_PX) return { kind: 'anchor', r: s, i }
  }
  for (const r of all) {
    if (r.c.id === selId.value) continue
    for (let i = 0; i < r.c.anchors.length; i++)
      if (dist(pt, px(r.c.anchors[i].p)) <= HIT_PX) return { kind: 'dot', r, i }
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
    selId.value = hit.r.c.id
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
    const a = hit.r.c.anchors[hit.i]
    const H = handlesOf(hit.r.c, hit.i)
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
  const a: Anchor = drag.r.c.anchors[drag.i]
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
  bake(drag.r.c)
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
let pull: { c: Curve; a: Anchor } | null = null

const HINT = {
  line: 'click = corner · click-drag = curve · Enter finishes',
  boundary: 'trace the outer tracks — Enter closes the loop',
  centerline: 'run it entrance to entrance — Enter finishes',
}

function beginDraw(c: Curve, kind: keyof typeof HINT) {
  selId.value = c.id
  selAnchor.value = -1
  drawTarget = { id: c.id, end: 'tail' }
  tool.value = 'draw'
  map?.dragPan.disable()
  status.value = HINT[kind]
  render()
}

function startLine() {
  push()
  const c = newCurve(uid())
  c.label = newLabel.value
  net.value.lines.push(c)
  beginDraw(c, 'line')
}

function startYard() {
  push()
  mode.value = 'yards'
  const y: Yard = {
    id: uid('y'),
    label: newLabel.value,
    boundary: newCurve(uid('b'), true),
    centerlines: [],
  }
  net.value.yards.push(y)
  beginDraw(y.boundary, 'boundary')
}

function startCenterline() {
  const y = selYard.value ?? net.value.yards[net.value.yards.length - 1]
  if (!y) {
    status.value = 'draw a yard boundary first'
    return
  }
  push()
  mode.value = 'yards'
  const c = newCurve(uid('c'))
  y.centerlines.push(c)
  beginDraw(c, 'centerline')
}

function startExtend(r: Ref_, end: 'head' | 'tail') {
  if (r.c.closed) return
  push()
  selId.value = r.c.id
  drawTarget = { id: r.c.id, end }
  tool.value = 'draw'
  map.dragPan.disable()
  status.value = `extending from ${end} — Enter finishes`
  render()
}

/** snap a new pen node to another curve's endpoint within ~10px, so two
 *  drawn lines that should meet actually share a coordinate. */
function snapPoint(p: LL): LL {
  const q = px(p)
  for (const r of refs()) {
    if (drawTarget && r.c.id === drawTarget.id) continue
    if (r.c.closed) continue
    for (const a of [r.c.anchors[0], r.c.anchors[r.c.anchors.length - 1]]) {
      if (!a) continue
      if (dist(q, px(a.p)) < 10) return [...a.p] as LL
    }
  }
  return p
}

function drawDown(e: any) {
  if (e.originalEvent.button !== 0 || !drawTarget) return
  const r = refOf(drawTarget.id)
  if (!r) return
  e.preventDefault()
  push()
  const p = snapPoint([e.lngLat.lng, e.lngLat.lat])
  const a: Anchor = { p, hin: [...p] as LL, hout: [...p] as LL } // corner until dragged
  if (drawTarget.end === 'tail') r.c.anchors.push(a)
  else r.c.anchors.unshift(a)
  pull = { c: r.c, a }
  bake(r.c)
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
    bake(pull.c)
    render()
    return
  }
  // rubber band that continues the previous curvature
  const r = refOf(drawTarget?.id ?? null)
  if (!r || !r.c.anchors.length) return setPreview(null)
  const li = drawTarget!.end === 'tail' ? r.c.anchors.length - 1 : 0
  const last = r.c.anchors[li]
  const H = handlesOf(r.c, li)
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

function drawUp() {
  if (!pull) return
  // a handle released within 4px of its anchor was a click, not a drag —
  // snap it back to a hard corner
  const a = pull.a
  const H = handlesOf(pull.c, pull.c.anchors.indexOf(a))
  if (dist(px(a.p), px(H.hout)) < 4) {
    a.hin = [...a.p] as LL
    a.hout = [...a.p] as LL
  }
  bake(pull.c)
  pull = null
  changed()
}

function endDraw(discard: boolean, record = true) {
  const r = refOf(drawTarget?.id ?? null)
  if (r) {
    trimDuplicateEnds(r.c)
    const need = r.c.closed ? 3 : 2
    if (r.c.anchors.length < need) {
      // too small to be anything: drop it, and drop the yard it was the
      // boundary of — a yard with no boundary has nothing to contain
      const i = r.list.indexOf(r.c)
      if (i >= 0) r.list.splice(i, 1)
      if (r.kind === 'boundary' && r.yard)
        net.value.yards = net.value.yards.filter((y) => y.id !== r.yard!.id)
      if (selId.value === r.c.id) selId.value = null
    } else {
      bake(r.c)
      if (r.kind === 'centerline') reassign(r)
    }
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

/** a centerline belongs to the yard its middle sits in, whichever yard
 *  happened to be selected when the pen started. */
function reassign(r: Ref_) {
  if (!r.yard) return
  const mid = r.c.coords[Math.floor(r.c.coords.length / 2)]
  if (!mid) return
  const y = yardAt(net.value, mid)
  if (!y || y.id === r.yard.id) return
  const i = r.yard.centerlines.indexOf(r.c)
  if (i >= 0) r.yard.centerlines.splice(i, 1)
  y.centerlines.push(r.c)
  status.value = `centerline moved to ${yardLabel(y)}`
}

// ── curve-level operations ────────────────────────────────────────────
function onStrokeClick(e: any) {
  const f = e.features?.[0]
  if (!f) return
  const r = refOf(f.properties.id)
  if (!r) return
  pickOrEdit(r, [e.lngLat.lng, e.lngLat.lat])
}

function onYardClick(e: any) {
  const f = e.features?.[0]
  if (!f) return
  if (mode.value !== 'yards') mode.value = 'yards'
  const r = refOf(f.properties.id)
  if (!r) return
  // clicking a yard's fill selects the YARD; only a click on its ring
  // while already selected inserts an anchor
  if (selId.value !== r.c.id) {
    selId.value = r.c.id
    selAnchor.value = -1
    return render()
  }
  pickOrEdit(r, [e.lngLat.lng, e.lngLat.lat])
}

function pickOrEdit(r: Ref_, at: LL) {
  if (tool.value === 'merge' && sel.value && r.c.id !== selId.value) {
    if (r.c.closed || sel.value.c.closed) {
      status.value = 'rings do not merge'
      return
    }
    push()
    mergeInto(sel.value.list, sel.value.c, r.c)
    tool.value = 'idle'
    status.value = ''
    changed()
    return
  }
  if (tool.value === 'idle' && r.c.id === selId.value) {
    push()
    selAnchor.value = insertAnchor(r.c, at)
    changed()
    return
  }
  selId.value = r.c.id
  selAnchor.value = -1
  render()
}

function doSplit() {
  const s = sel.value
  if (!s) return
  push()
  if (!splitAt(s.list, s.c, selAnchor.value)) {
    hist.discard()
    status.value = s.c.closed ? 'a boundary cannot be split' : 'select an interior anchor first'
    return
  }
  selAnchor.value = -1
  changed()
}

function doReverse() {
  const s = sel.value
  if (!s) return
  push()
  reverseLine(s.c)
  changed()
}

function deleteSelected() {
  const s = sel.value
  if (!s) return
  if (s.kind === 'boundary' && s.yard) return deleteYard(s.yard)
  push()
  const i = s.list.indexOf(s.c)
  if (i >= 0) s.list.splice(i, 1)
  selId.value = null
  changed()
}

function deleteYard(y: Yard) {
  push()
  net.value.yards = net.value.yards.filter((x) => x.id !== y.id)
  if (selId.value && !refOf(selId.value)) selId.value = null
  changed()
}

function selectCurve(r: Ref_) {
  selId.value = r.c.id
  selAnchor.value = -1
  const mid = r.c.coords[Math.floor(r.c.coords.length / 2)]
  if (mid) map?.panTo({ lng: mid[0], lat: mid[1] })
  render()
}

function selectYard(y: Yard) {
  selectCurve({ c: y.boundary, list: [y.boundary], yard: y, kind: 'boundary' })
}

function relabel(v: string) {
  const s = sel.value
  if (!s) return
  if (s.kind === 'boundary' && s.yard) s.yard.label = v
  else s.c.label = v
  changed(false)
}
const selLabel = computed(() =>
  sel.value?.kind === 'boundary' ? sel.value.yard?.label ?? '' : selected.value?.label ?? '',
)

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
  if ((k === 'd' || k === 'n') && tool.value !== 'draw')
    return mode.value === 'yards' ? startCenterline() : startLine()
  if (k === 'y' && tool.value !== 'draw') return startYard()
  if (k === 'c' && sel.value && tool.value !== 'draw')
    return startExtend(sel.value, selAnchor.value === 0 ? 'head' : 'tail')
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
  if ((e.key === 'Backspace' || e.key === 'Delete') && selected.value) return deleteSelected()
}
function onKeyUp(e: KeyboardEvent) {
  if (e.key === 'Alt') altHeld = false
}

// ── underlays ─────────────────────────────────────────────────────────
// The build (what the pipeline drew) and the yard detector's own regions.
// Tuning the detector to the drawing means seeing both at once.
async function loadOverlay() {
  if (!map?.getSource('sk-overlay')) return
  if (!showOverlay.value || !feed.value || isGlobal.value)
    return map.getSource('sk-overlay').setData(fc([]))
  const c = map.getCenter()
  try {
    const r = await fetch(`/api/features?lat=${c.lat}&lon=${c.lng}&r=1200&build=${encodeURIComponent(feed.value)}`)
    const gj = await r.json()
    map.getSource('sk-overlay').setData(fc(gj.features ?? []))
  } catch {
    map.getSource('sk-overlay').setData(fc([]))
  }
}

async function loadDetected() {
  if (!map?.getSource('sk-det')) return
  if (!showDetected.value || !feed.value || isGlobal.value)
    return map.getSource('sk-det').setData(fc([]))
  try {
    const r = await fetch('/api/yards.geojson?feed=' + encodeURIComponent(feed.value))
    const gj = await r.json()
    map.getSource('sk-det').setData(fc(gj.features ?? []))
  } catch {
    map.getSource('sk-det').setData(fc([]))
  }
}

function fitFeed() {
  const b = currentFeed.value?.bbox
  if (map && b?.length === 4) map.fitBounds([[b[0], b[1]], [b[2], b[3]]], { padding: 40, duration: 0 })
}

// The basemap is only ever a tracing underlay here, so it sits fainter
// than the map view's — but the light tiles still need more of it than
// the dark ones to be legible at all (lib/theme.ts).
const rasterOpacity = () => (isDark.value ? 0.5 : 0.9)
const providerOpacity = (id: any) => (BASEMAP_PROVIDERS[id as 'carto'].fade ? rasterOpacity() : 1)

// The basemap choice is global (lib/theme.ts), so picking one on the map
// page holds here too — tracing against blank or against the real track
// alignment is the same want in both places.
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
watch(isDark, applyBasemap)
watch(basemap, applyBasemap)

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
      layers: PROVIDER_ORDER.map((id) => ({
        id: `bm-${id}`,
        type: 'raster',
        source: `bm-${id}`,
        layout: { visibility: currentBasemap().show.includes(id) ? 'visible' : 'none' },
        paint: { 'raster-opacity': providerOpacity(id) },
      })),
    },
  })
  ro = new ResizeObserver(() => map?.resize())
  ro.observe(el.value!)

  map.on('load', () => {
    for (const id of ['sk-overlay', 'sk-det', 'sk-yards', 'sk-lines', 'sk-ents', 'sk-leaders', 'sk-anchors', 'sk-handles', 'sk-preview'])
      map.addSource(id, { type: 'geojson', data: fc([]) })

    map.addLayer({
      id: 'ov', type: 'line', source: 'sk-overlay',
      paint: { 'line-color': '#8a8f98', 'line-width': 3, 'line-opacity': 0.35 },
    })
    // the detector's own regions, under the drawing: fill, outline, its
    // member track, its entrance nodes
    map.addLayer({
      id: 'det-fill', type: 'fill', source: 'sk-det',
      filter: ['==', ['get', 'kind'], 'yard'],
      paint: { 'fill-color': DET, 'fill-opacity': 0.07 },
    })
    map.addLayer({
      id: 'det-ring', type: 'line', source: 'sk-det',
      filter: ['==', ['get', 'kind'], 'yard'],
      paint: { 'line-color': DET, 'line-width': 1.6, 'line-opacity': 0.85 },
    })
    map.addLayer({
      id: 'det-track', type: 'line', source: 'sk-det',
      filter: ['==', ['get', 'kind'], 'yard_track'],
      paint: { 'line-color': DET, 'line-width': 1, 'line-opacity': 0.4 },
    })
    map.addLayer({
      id: 'det-ent', type: 'circle', source: 'sk-det',
      filter: ['==', ['get', 'kind'], 'yard_entrance'],
      paint: {
        'circle-radius': 4, 'circle-color': DET,
        'circle-stroke-color': '#fff', 'circle-stroke-width': 1.5,
      },
    })

    map.addLayer({
      id: 'sk-yard-fill', type: 'fill', source: 'sk-yards',
      paint: { 'fill-color': INK, 'fill-opacity': ['case', ['==', ['get', 'dim'], 1], 0.03, 0.08] },
    })
    map.addLayer({
      id: 'sk-yard-ring', type: 'line', source: 'sk-yards',
      layout: { 'line-join': 'round' },
      paint: {
        'line-color': INK, 'line-width': ['get', 'width'],
        'line-opacity': ['case', ['==', ['get', 'dim'], 1], 0.35, 1],
      },
    })
    map.addLayer({
      id: 'sk-line', type: 'line', source: 'sk-lines',
      layout: { 'line-cap': 'round', 'line-join': 'round' },
      paint: {
        'line-color': INK, 'line-width': ['get', 'width'],
        'line-opacity': ['case', ['==', ['get', 'dim'], 1], 0.3, 0.95],
      },
    })
    map.addLayer({
      id: 'sk-ent', type: 'circle', source: 'sk-ents',
      paint: {
        'circle-radius': 6, 'circle-color': ENT,
        'circle-stroke-color': '#333', 'circle-stroke-width': 2,
      },
    })
    map.addLayer({
      id: 'sk-preview-l', type: 'line', source: 'sk-preview',
      paint: { 'line-color': ENT, 'line-width': 2, 'line-dasharray': [3, 3], 'line-opacity': 0.85 },
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
        'circle-color': ['case', ['==', ['get', 'active'], 1], ENT, '#fff'],
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
    map.on('mouseup', () => (tool.value === 'draw' ? drawUp() : onUp()))
    map.on('click', 'sk-line', onStrokeClick)
    map.on('click', 'sk-yard-ring', onYardClick)
    map.on('click', 'sk-yard-fill', onYardClick)
    map.on('dblclick', (e: any) => {
      if (tool.value === 'draw') return endDraw(false)
      const hit = hitTest(e.point)
      if (hit && hit.kind !== 'handle') {
        push()
        toggleCorner(hit.r.c.anchors[hit.i])
        changed()
      }
    })
    map.on('contextmenu', (e: any) => {
      const hit = hitTest(e.point)
      if (!hit) return
      e.preventDefault()
      push()
      const alive = deleteAnchor(hit.r.list, hit.r.c, hit.i)
      if (!alive) {
        if (hit.r.kind === 'boundary' && hit.r.yard) deleteYard(hit.r.yard)
        if (selId.value === hit.r.c.id) selId.value = null
      }
      selAnchor.value = -1
      changed()
    })
    map.on('moveend', loadOverlay)

    // the document is already loaded (see below) — draw it and fetch the
    // underlays now that there are layers to put them in
    render()
    fitFeed()
    loadOverlay()
    loadDetected()
  })

  // Loading the sketch does NOT wait on the map. render() no-ops until the
  // layers exist, so the curve list, history and saving all work even if
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

watch(feed, () => load().then(() => { loadOverlay(); loadDetected() }))
watch(showOverlay, loadOverlay)
watch(showDetected, loadDetected)
watch(selId, () => nextTick(render))
watch(mode, () => {
  if (tool.value === 'draw') endDraw(true)
  selId.value = null
  selAnchor.value = -1
  render()
})
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

      <div class="pointer-events-none absolute inset-x-0 top-0 flex flex-wrap items-start justify-between gap-2 p-2 sm:gap-3 sm:p-4">
        <div class="pointer-events-auto flex items-center gap-2 rounded-xl border border-border bg-card/90 px-3 py-2 shadow-sm backdrop-blur">
          <span class="text-sm font-medium">{{ currentFeed?.name || 'No feed' }}</span>
          <Badge v-if="loading" variant="info"><Spinner class="size-3" /> loading</Badge>
          <Badge v-else-if="saving" variant="info"><Spinner class="size-3" /> saving</Badge>
          <Badge v-else-if="status" variant="muted">{{ status }}</Badge>
        </div>
        <div class="pointer-events-auto flex items-center gap-1 rounded-xl border border-border bg-card/90 px-2 py-1.5 shadow-sm backdrop-blur">
          <template v-if="mode === 'network'">
            <Button :variant="tool === 'draw' ? 'default' : 'ghost'" size="sm"
                    @click="tool === 'draw' ? endDraw(false) : startLine()">
              <Pen class="size-4" /> {{ tool === 'draw' ? 'Finish' : 'Line' }}
            </Button>
          </template>
          <template v-else>
            <Button variant="ghost" size="sm" title="New yard boundary (y)" @click="startYard">
              <Squircle class="size-4" /> Yard
            </Button>
            <Button variant="ghost" size="sm" title="New centerline (d)" @click="startCenterline">
              <Spline class="size-4" /> Centerline
            </Button>
            <Button v-if="tool === 'draw'" variant="default" size="sm" @click="endDraw(false)">
              Finish
            </Button>
          </template>
          <span class="mx-1 h-5 w-px bg-border" />
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
        <div>click a selected curve to insert an anchor</div>
        <div class="mt-1">d new · y yard · c continue · x split · m merge · ⌫ delete</div>
        <div class="mt-1 flex items-center gap-3">
          <span class="flex items-center gap-1"><span class="inline-block h-0.5 w-4" :style="{ background: INK }" /> drawn</span>
          <span class="flex items-center gap-1"><span class="inline-block h-0.5 w-4" :style="{ background: DET }" /> detected</span>
          <span class="flex items-center gap-1"><span class="inline-block size-2 rounded-full" :style="{ background: ENT }" /> entrance</span>
        </div>
      </div>
    </div>

    <aside
      v-show="panelOpen"
      class="absolute right-0 top-0 z-20 flex h-full w-80 max-w-[85vw] flex-col overflow-y-auto border-l border-border bg-card shadow-xl xl:static xl:z-auto xl:bg-card/40 xl:shadow-none"
    >
      <div class="border-b border-border p-4">
        <div class="mb-3 grid grid-cols-2 gap-1 rounded-lg bg-muted p-1 text-xs">
          <button
            class="rounded-md px-2 py-1.5 font-medium transition-colors"
            :class="mode === 'network' ? 'bg-background shadow-sm' : 'text-muted-foreground'"
            @click="mode = 'network'"
          >Network <span class="text-muted-foreground">({{ net.lines.length }})</span></button>
          <button
            class="rounded-md px-2 py-1.5 font-medium transition-colors"
            :class="mode === 'yards' ? 'bg-background shadow-sm' : 'text-muted-foreground'"
            @click="mode = 'yards'"
          >Yards <span class="text-muted-foreground">({{ net.yards.length }})</span></button>
        </div>
        <div class="space-y-2">
          <label class="flex items-center justify-between text-xs text-muted-foreground">
            <span class="flex items-center gap-1.5">
              <component :is="showOverlay ? Eye : EyeOff" class="size-3.5" /> build underlay
            </span>
            <Switch :model-value="showOverlay" @update:model-value="(v) => (showOverlay = v)" />
          </label>
          <label class="flex items-center justify-between text-xs text-muted-foreground">
            <span class="flex items-center gap-1.5">
              <component :is="showDetected ? Eye : EyeOff" class="size-3.5" /> detected yards
            </span>
            <Switch :model-value="showDetected" @update:model-value="(v) => (showDetected = v)" />
          </label>
        </div>
      </div>

      <div v-if="selected" class="border-b border-border p-4">
        <div class="mb-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
          {{ sel?.kind === 'boundary' ? 'Selected yard' : sel?.kind === 'centerline' ? 'Selected centerline' : 'Selected line' }}
        </div>
        <Input
          :model-value="selLabel" placeholder="label" class="mb-3 h-8 text-xs"
          @update:model-value="(v: string) => relabel(v)"
        />
        <div v-if="sel?.kind === 'boundary' && sel.yard" class="mb-3 text-xs text-muted-foreground">
          {{ sel.yard.centerlines.length }} centerline(s) ·
          {{ entCount(sel.yard) }} entrance(s) computed
        </div>
        <div class="flex flex-wrap gap-1">
          <Button v-if="!selected.closed" variant="outline" size="sm" title="Split at the selected anchor (x)" @click="doSplit">
            <Scissors class="size-4" /> Split
          </Button>
          <Button v-if="!selected.closed" :variant="tool === 'merge' ? 'default' : 'outline'" size="sm" title="Merge (m)"
                  @click="tool = tool === 'merge' ? 'idle' : 'merge'">
            <Merge class="size-4" /> Merge
          </Button>
          <Button v-if="!selected.closed" variant="outline" size="sm" title="Reverse direction" @click="doReverse">
            <ArrowLeftRight class="size-4" />
          </Button>
          <Button variant="outline" size="sm" title="Delete (⌫)" @click="deleteSelected">
            <Trash2 class="size-4" />
          </Button>
        </div>
      </div>

      <div v-if="mode === 'network'" class="flex-1 p-2">
        <button
          v-for="l in net.lines" :key="l.id"
          class="flex w-full items-center gap-2 rounded-lg px-2 py-2 text-left text-sm transition-colors"
          :class="l.id === selId ? 'bg-accent text-accent-foreground' : 'hover:bg-accent/40'"
          @click="selectCurve({ c: l, list: net.lines, yard: null, kind: 'line' })"
        >
          <span class="min-w-0 flex-1 truncate">{{ curveLabel(l) }}</span>
          <span class="shrink-0 text-xs text-muted-foreground tabular-nums">{{ l.anchors.length }}</span>
        </button>
      </div>

      <div v-else class="flex-1 space-y-1 p-2">
        <div v-if="!net.yards.length" class="px-2 py-6 text-center text-xs text-muted-foreground">
          No yards drawn. Trace the outer tracks with <span class="font-medium">Yard</span>,
          then run centerlines entrance to entrance.
        </div>
        <div v-for="y in net.yards" :key="y.id" class="rounded-lg" :class="y.boundary.id === selId || y.centerlines.some((c) => c.id === selId) ? 'bg-accent/40' : ''">
          <button
            class="flex w-full items-center gap-2 rounded-lg px-2 py-2 text-left text-sm transition-colors hover:bg-accent/40"
            @click="selectYard(y)"
          >
            <Squircle class="size-3.5 shrink-0" :style="{ color: INK }" />
            <span class="min-w-0 flex-1 truncate">{{ yardLabel(y) }}</span>
            <span class="shrink-0 text-xs text-muted-foreground tabular-nums">
              {{ y.centerlines.length }}c · {{ entCount(y) }}e
            </span>
            <span class="shrink-0 text-muted-foreground hover:text-destructive" @click.stop="deleteYard(y)">
              <X class="size-3.5" />
            </span>
          </button>
          <button
            v-for="c in y.centerlines" :key="c.id"
            class="flex w-full items-center gap-2 rounded-lg py-1.5 pl-8 pr-2 text-left text-xs transition-colors"
            :class="c.id === selId ? 'bg-accent text-accent-foreground' : 'hover:bg-accent/40'"
            @click="selectCurve({ c, list: y.centerlines, yard: y, kind: 'centerline' })"
          >
            <Spline class="size-3 shrink-0" />
            <span class="min-w-0 flex-1 truncate">{{ curveLabel(c) }}</span>
            <span class="shrink-0 text-muted-foreground tabular-nums">{{ c.anchors.length }}</span>
          </button>
        </div>
      </div>
    </aside>
  </div>
</template>
