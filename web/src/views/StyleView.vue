<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { Save, Plus, Trash2, RotateCcw, Loader2, Info, Search } from 'lucide-vue-next'
import PageHeader from '@/components/PageHeader.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Card from '@/components/ui/Card.vue'
import CardHeader from '@/components/ui/CardHeader.vue'
import CardTitle from '@/components/ui/CardTitle.vue'
import CardDescription from '@/components/ui/CardDescription.vue'
import CardContent from '@/components/ui/CardContent.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import Spinner from '@/components/ui/Spinner.vue'
import Dialog from '@/components/ui/Dialog.vue'
import GlobalNotice from '@/components/GlobalNotice.vue'
import { api, type StyleConfig, type StyleSet } from '@/lib/api'
import { feed, currentFeed, isGlobal } from '@/lib/store'
import { toast } from '@/lib/toast'

const CLASSES = [
  'metro', 'tram', 'regional', 'monorail', 'ferry', 'bus', 'aerial', 'funicular', 'cable',
] as const
const TRUNKS = [
  { value: 'color', label: 'color — same colour, one ribbon' },
  { value: 'agency', label: 'agency — one ribbon per operator' },
  { value: 'route', label: 'route — never merge' },
  { value: 'none', label: 'none — path match only, no layout' },
]

const loading = ref(false)
const saving = ref(false)
const resolved = ref<StyleSet | null>(null)
const feedCfg = ref<StyleConfig>({ modes: {}, agencies: {}, routes: {}, stops: {}, options: {} })
const scope = ref<'feed' | 'global'>('feed')
const globalCfg = ref<StyleConfig>({ modes: {}, agencies: {}, routes: {}, stops: {}, options: {} })

const layer = computed(() => (scope.value === 'feed' ? feedCfg.value : globalCfg.value))

async function load() {
  // /api/style/config needs a real feed — the _default layer is only
  // reachable through one, so the global context parks this page
  if (!feed.value || isGlobal.value) return
  loading.value = true
  try {
    const [res, cfg] = await Promise.all([api.style(feed.value), api.styleConfig(feed.value)])
    resolved.value = res
    const norm = (d: any): StyleConfig => ({
      modes: d?.modes ?? {}, agencies: d?.agencies ?? {}, routes: d?.routes ?? {},
      stops: d?.stops ?? {}, options: d?.options ?? {},
    })
    feedCfg.value = norm(cfg.city) // `city` is the wire name for the per-feed layer
    globalCfg.value = norm(cfg.global)
  } catch (e: any) {
    toast({ title: 'Could not load style', description: e.message, variant: 'error' })
  } finally {
    loading.value = false
  }
}
watch(feed, load, { immediate: true })

/** The effective value shown in a field: the override if this layer sets
 *  one, else the resolved value (which already folds in defaults and the
 *  other layer). Placeholder-style, so "inherited" stays visible. */
function eff(cls: string, key: 'color' | 'width' | 'opacity' | 'band_floor' | 'trunk') {
  const o = layer.value.modes?.[cls] as any
  if (o && o[key] !== undefined && o[key] !== '') return o[key]
  return (resolved.value?.modes?.[cls] as any)?.[key]
}
function isOverridden(cls: string, key: string) {
  const o = layer.value.modes?.[cls] as any
  return !!o && o[key] !== undefined && o[key] !== ''
}
function setField(cls: string, key: string, val: any) {
  const modes = (layer.value.modes ??= {})
  const m = (modes[cls] ??= {} as any)
  if (val === '' || val === undefined || val === null) delete (m as any)[key]
  else (m as any)[key] = val
  if (!Object.keys(m).length) delete modes[cls]
}
function clearClass(cls: string) {
  if (layer.value.modes) delete layer.value.modes[cls]
}

// ── colour overrides ──────────────────────────────────────────────────
const addOpen = ref(false)
const routes = ref<any[]>([])
const routeQuery = ref('')
const newHex = ref('')
const newKey = ref('')

/** every subject that carries a colour, flattened for the table */
const KINDS = ['agency', 'route', 'stop'] as const
type Kind = (typeof KINDS)[number]
const bucket = (k: Kind) =>
  k === 'agency' ? 'agencies' : k === 'route' ? 'routes' : 'stops'

function rowsWith(field: 'color' | 'name') {
  const out: { kind: Kind; key: string; value: string }[] = []
  for (const k of KINDS) {
    const m = (layer.value as any)[bucket(k)] ?? {}
    for (const [name, ent] of Object.entries(m as Record<string, any>)) {
      if (ent?.[field]) out.push({ kind: k, key: name, value: String(ent[field]).replace('#', '') })
    }
  }
  return out
}
function setField2(kind: Kind, key: string, field: 'color' | 'name', v: string) {
  const doc = layer.value as any
  const m = (doc[bucket(kind)] ??= {})
  const ent = (m[key] ??= {})
  if (!v) delete ent[field]
  else ent[field] = field === 'color' ? v.replace('#', '').toUpperCase() : v
  if (!Object.keys(ent).length) delete m[key]
}
function dropField(kind: Kind, key: string, field: 'color' | 'name') {
  setField2(kind, key, field, '')
}

const colorRows = computed(() => rowsWith('color').map((r) => ({ ...r, hex: r.value })))

async function openAdd() {
  addOpen.value = true
  newKey.value = ''
  newHex.value = ''
  if (!routes.value.length && feed.value) {
    try {
      routes.value = await api.routes(feed.value)
    } catch {
      /* the picker is a convenience; typing a key still works */
    }
  }
}

const agencies = computed(() => {
  const m = new Map<string, string>()
  for (const r of routes.value) if (r.agency_name) m.set(r.agency_name, r.mode)
  return [...m.entries()].map(([name, mode]) => ({ name, mode }))
})

const filteredRoutes = computed(() => {
  const q = routeQuery.value.trim().toLowerCase()
  const list = routes.value
  if (!q) return list.slice(0, 40)
  return list
    .filter((r) =>
      [r.short_name, r.long_name, r.id, r.agency_name].some((s: string) => s?.toLowerCase().includes(q)),
    )
    .slice(0, 40)
})

function addOverride(key: string, hex: string) {
  if (!key || !/^#?[0-9a-fA-F]{6}$/.test(hex)) return
  const [kind, name] = splitKey(key)
  setField2(kind, name, 'color', hex)
  addOpen.value = false
}
/** accepts either a bare subject with an explicit kind, or "route:501" */
function splitKey(key: string): [Kind, string] {
  const i = key.indexOf(':')
  if (i > 0) {
    const k = key.slice(0, i) as Kind
    if (KINDS.includes(k)) return [k, key.slice(i + 1)]
  }
  return ['route', key]
}

// ── name overrides ────────────────────────────────────────────────────
// Same shape as colours, same reason: the feed is what got it wrong.
// CTA files its lines as "Red Line" and the bullet wants "Red"; CATS
// files the LYNX Blue Line as route 501 and a rider calls it Blue.
const nameAddOpen = ref(false)
const newNameKey = ref('')
const newNameKind = ref('route')
const newName = ref('')

const nameRows = computed(() => rowsWith('name').map((r) => ({ ...r, name: r.value })))

async function openNameAdd() {
  nameAddOpen.value = true
  newNameKey.value = ''
  newName.value = ''
  newNameKind.value = 'route'
  if (!routes.value.length && feed.value) {
    try {
      routes.value = await api.routes(feed.value)
    } catch {
      /* the picker is a convenience; typing a key still works */
    }
  }
}
function addName(key: string, name: string) {
  if (!key || !name.trim()) return
  const [kind, subj] = splitKey(key)
  setField2(kind, subj, 'name', name.trim())
  nameAddOpen.value = false
}

// ── direction labels ──────────────────────────────────────────────────
// GTFS carries direction_id — an opaque 0/1 — and nothing that says what it
// means. A rider at Grand Central is reading a sign that says UPTOWN, not one
// that says Woodlawn, so the name is curation like colour. Set it on an
// agency to cover its whole network, on a route to say something truer for
// one line. It rides out in the corrected feed's directions.txt.
const dirAddOpen = ref(false)
const newDirKey = ref('')
const newDirKind = ref('route')
const newDirId = ref('0')
const newDirLabel = ref('')

/** every subject that names a direction, one row per direction_id */
const directionRows = computed(() => {
  const out: { kind: Kind; key: string; dir: string; label: string }[] = []
  for (const k of KINDS) {
    const m = (layer.value as any)[bucket(k)] ?? {}
    for (const [name, ent] of Object.entries(m as Record<string, any>)) {
      const d = ent?.directions
      if (!d) continue
      for (const dir of Object.keys(d).sort()) {
        if (d[dir]) out.push({ kind: k, key: name, dir, label: String(d[dir]) })
      }
    }
  }
  return out
})

function setDirection(kind: Kind, key: string, dir: string, label: string) {
  const doc = layer.value as any
  const m = (doc[bucket(kind)] ??= {})
  const ent = (m[key] ??= {})
  const d = (ent.directions ??= {})
  if (!label.trim()) delete d[dir]
  else d[dir] = label.trim()
  if (!Object.keys(d).length) delete ent.directions
  if (!Object.keys(ent).length) delete m[key]
}
function dropDirection(kind: Kind, key: string, dir: string) {
  setDirection(kind, key, dir, '')
}

async function openDirAdd() {
  dirAddOpen.value = true
  newDirKey.value = ''
  newDirLabel.value = ''
  newDirId.value = '0'
  newDirKind.value = 'route'
  if (!routes.value.length && feed.value) {
    try {
      routes.value = await api.routes(feed.value)
    } catch {
      /* the picker is a convenience; typing a key still works */
    }
  }
}
function addDirection(key: string, dir: string, label: string) {
  if (!key || !label.trim()) return
  const [kind, subj] = splitKey(key)
  setDirection(kind, subj, dir.trim() || '0', label)
  dirAddOpen.value = false
}

/** caterpillars tri-state for the active layer: '' = inherit, 'on', 'off' */
const catState = computed<string>(() => {
  const v = layer.value.options?.caterpillars
  return v === undefined ? '' : v ? 'on' : 'off'
})
const catResolved = computed(() => (resolved.value as any)?.caterpillars !== false)
function setCaterpillars(v: string) {
  const o = (layer.value.options ??= {})
  if (v === '') delete o.caterpillars
  else o.caterpillars = v === 'on'
}
const osmState = computed<string>(() => {
  const v = layer.value.options?.osm_stop_names
  return v === undefined ? '' : v ? 'on' : 'off'
})
const osmResolved = computed(() => (resolved.value as any)?.osm_stop_names !== false)
function setOSMStopNames(v: string) {
  const o = (layer.value.options ??= {})
  if (v === '') delete o.osm_stop_names
  else o.osm_stop_names = v === 'on'
}

async function save() {
  if (!feed.value || isGlobal.value) return
  saving.value = true
  try {
    await api.saveStyleConfig(feed.value, { global: globalCfg.value, city: feedCfg.value })
    await load()
    toast({
      title: 'Style saved',
      description: 'Rebuild the feed for it to show on the map.',
      variant: 'success',
    })
  } catch (e: any) {
    toast({ title: 'Could not save style', description: e.message, variant: 'error' })
  } finally {
    saving.value = false
  }
}

const overrideCount = computed(
  () =>
    Object.keys(layer.value.modes ?? {}).length +
    Object.keys(layer.value.agencies ?? {}).length +
    Object.keys(layer.value.routes ?? {}).length +
    Object.keys(layer.value.stops ?? {}).length,
)
</script>

<template>
  <PageHeader
    title="Style"
    :subtitle="currentFeed ? `${currentFeed.name || currentFeed.id} — colours, weights and trunking` : 'Pick a feed'"
  >
    <template #actions>
      <div class="flex rounded-lg bg-muted p-1">
        <button
          v-for="s in (['feed', 'global'] as const)"
          :key="s"
          class="rounded-md px-3 py-1 text-sm font-medium capitalize transition-all"
          :class="scope === s ? 'bg-background text-foreground shadow' : 'text-muted-foreground'"
          @click="scope = s"
        >
          {{ s }}
        </button>
      </div>
      <Button :disabled="saving || loading || isGlobal" @click="save">
        <Loader2 v-if="saving" class="size-4 animate-spin" /><Save v-else class="size-4" /> Save
      </Button>
    </template>
  </PageHeader>

  <GlobalNotice v-if="isGlobal" title="Style" />
  <div v-else class="space-y-6 p-4 sm:p-8">
    <div class="flex gap-2 rounded-lg border border-border bg-muted/40 p-3 text-xs text-muted-foreground">
      <Info class="mt-0.5 size-3.5 shrink-0" />
      <span v-if="scope === 'feed'">
        Editing <strong>{{ currentFeed?.name || feed }}</strong> only. These values layer on top of the global block,
        field by field — set one and the rest keep inheriting. Fields shown greyed are inherited, not set here.
      </span>
      <span v-else>
        Editing the <strong>global</strong> block — the baseline for every feed. A feed's own overrides still win.
      </span>
    </div>

    <div v-if="loading" class="flex justify-center py-16"><Spinner class="size-6" /></div>

    <template v-else>
      <Card>
        <CardHeader class="pb-3">
          <CardTitle class="text-sm">Transit classes</CardTitle>
          <CardDescription>
            How each class is drawn and how its routes merge into shared ribbons.
          </CardDescription>
        </CardHeader>
        <CardContent class="overflow-x-auto">
          <table class="w-full min-w-[720px] text-sm">
            <thead class="text-xs text-muted-foreground">
              <tr class="border-b border-border">
                <th class="pb-2 text-left font-medium">Class</th>
                <th class="pb-2 text-left font-medium">Colour</th>
                <th class="pb-2 text-left font-medium">Trunking</th>
                <th class="pb-2 text-right font-medium">Width</th>
                <th class="pb-2 text-right font-medium">Opacity</th>
                <th class="pb-2 text-right font-medium">Zoom floor</th>
                <th class="pb-2 text-right font-medium">Hidden</th>
                <th class="pb-2"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="cls in CLASSES" :key="cls" class="border-b border-border/50 last:border-0">
                <td class="py-2 font-medium capitalize">{{ cls }}</td>
                <td class="py-2">
                  <div class="flex items-center gap-2">
                    <span
                      class="size-5 shrink-0 rounded border border-border"
                      :style="{ background: eff(cls, 'color') ? `#${eff(cls, 'color')}` : 'transparent' }"
                    />
                    <Input
                      class="h-8 w-28 font-mono text-xs"
                      :model-value="isOverridden(cls, 'color') ? (layer.modes as any)[cls].color : ''"
                      :placeholder="eff(cls, 'color') || 'per route'"
                      @update:model-value="(v) => setField(cls, 'color', v.replace('#', ''))"
                    />
                  </div>
                </td>
                <td class="py-2">
                  <Select
                    class="h-8 w-56 text-xs"
                    :model-value="eff(cls, 'trunk')"
                    :options="TRUNKS"
                    @update:model-value="(v) => setField(cls, 'trunk', v)"
                  />
                </td>
                <td class="py-2 text-right">
                  <Input
                    class="h-8 w-16 text-right text-xs tabular-nums"
                    :model-value="isOverridden(cls, 'width') ? (layer.modes as any)[cls].width : ''"
                    :placeholder="String(eff(cls, 'width') ?? '')"
                    @update:model-value="(v) => setField(cls, 'width', v === '' ? '' : Number(v))"
                  />
                </td>
                <td class="py-2 text-right">
                  <Input
                    class="h-8 w-16 text-right text-xs tabular-nums"
                    :model-value="isOverridden(cls, 'opacity') ? (layer.modes as any)[cls].opacity : ''"
                    :placeholder="String(eff(cls, 'opacity') ?? '')"
                    @update:model-value="(v) => setField(cls, 'opacity', v === '' ? '' : Number(v))"
                  />
                </td>
                <td class="py-2 text-right">
                  <Input
                    class="h-8 w-16 text-right text-xs tabular-nums"
                    :model-value="isOverridden(cls, 'band_floor') ? (layer.modes as any)[cls].band_floor : ''"
                    :placeholder="String(eff(cls, 'band_floor') ?? '')"
                    @update:model-value="(v) => setField(cls, 'band_floor', v === '' ? '' : Number(v))"
                  />
                </td>
                <td class="py-2 text-right">
                  <Switch
                    :model-value="!!(layer.modes as any)?.[cls]?.hidden"
                    @update:model-value="(v) => setField(cls, 'hidden', v ? true : '')"
                  />
                </td>
                <td class="py-2 text-right">
                  <Button
                    v-if="(layer.modes as any)?.[cls]"
                    variant="ghost"
                    size="icon"
                    title="Clear overrides for this class"
                    @click="clearClass(cls)"
                  >
                    <RotateCcw class="size-4" />
                  </Button>
                </td>
              </tr>
            </tbody>
          </table>
          <p class="mt-3 text-xs text-muted-foreground">
            Width and opacity are relative to a metro's 1.0. Zoom floor is the lowest band the class draws in —
            raise it to declutter low zooms.
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader class="pb-3">
          <CardTitle class="text-sm">Map features</CardTitle>
          <CardDescription>Optional layers a feed can opt out of (or into, over a global off).</CardDescription>
        </CardHeader>
        <CardContent>
          <div class="flex items-center justify-between gap-4">
            <div>
              <div class="text-sm font-medium">Route bullets on lines</div>
              <div class="text-xs text-muted-foreground">
                Caterpillar chains riding the ribbons between stations. Currently
                <strong>{{ catResolved ? 'on' : 'off' }}</strong> for this feed.
              </div>
            </div>
            <select
              class="h-9 rounded-md border border-border bg-background px-3 text-sm"
              :value="catState"
              @change="setCaterpillars(($event.target as HTMLSelectElement).value)"
            >
              <option value="">inherit</option>
              <option value="on">on</option>
              <option value="off">off</option>
            </select>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader class="flex-row items-center justify-between pb-3">
          <div>
            <CardTitle class="text-sm">Colour overrides</CardTitle>
            <CardDescription>
              Force a hue for one agency or route. These beat the feed, the class colour and the agency majority.
            </CardDescription>
          </div>
          <Button variant="outline" size="sm" @click="openAdd"><Plus class="size-4" /> Add</Button>
        </CardHeader>
        <CardContent>
          <div v-if="!colorRows.length" class="flex flex-col items-center gap-2 py-10 text-muted-foreground">
            <span class="text-sm">No overrides — every route uses its feed colour.</span>
          </div>
          <div v-else class="grid gap-2">
            <div
              v-for="row in colorRows"
              :key="row.key"
              class="flex items-center gap-3 rounded-lg border border-border px-3 py-2"
            >
              <span class="size-6 shrink-0 rounded border border-border" :style="{ background: `#${row.hex}` }" />
              <Badge variant="muted" class="shrink-0 text-[10px]">{{ row.kind }}</Badge>
              <span class="min-w-0 flex-1 truncate text-sm">{{ row.key }}</span>
              <Input
                class="h-8 w-28 font-mono text-xs"
                :model-value="row.hex"
                @update:model-value="(v) => setField2(row.kind, row.key, 'color', v)"
              />
              <Button variant="ghost" size="icon" @click="dropField(row.kind, row.key, 'color')"><Trash2 class="size-4" /></Button>
            </div>
          </div>
          <p v-if="colorRows.length" class="mt-3 text-xs text-muted-foreground">
            An agency trunk always takes the agency override — a route override inside one would repaint whichever
            member sorted first, so the shared ribbon would change colour at every membership seam.
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader class="flex-row items-center justify-between pb-3">
          <div>
            <CardTitle class="text-sm">Name overrides</CardTitle>
            <CardDescription>
              Rename an agency, a route or a stop. A route override replaces the bullet text and the trunk label; a
              stop override replaces the station's identity, so the pill and the complex it belongs to always agree.
            </CardDescription>
          </div>
          <Button variant="outline" size="sm" @click="openNameAdd"><Plus class="size-4" /> Add</Button>
        </CardHeader>
        <CardContent>
          <div v-if="!nameRows.length" class="flex flex-col items-center gap-2 py-10 text-muted-foreground">
            <span class="text-sm">No overrides — every name comes from the feed.</span>
          </div>
          <div v-else class="grid gap-2">
            <div
              v-for="row in nameRows"
              :key="row.key"
              class="flex items-center gap-3 rounded-lg border border-border px-3 py-2"
            >
              <Badge variant="muted" class="shrink-0 text-[10px]">{{ row.kind }}</Badge>
              <span class="min-w-0 flex-1 truncate text-sm text-muted-foreground">{{ row.key }}</span>
              <span class="shrink-0 text-muted-foreground">→</span>
              <Input
                class="h-8 w-40 text-xs"
                :model-value="row.name"
                @update:model-value="(v) => setField2(row.kind, row.key, 'name', v)"
              />
              <Button variant="ghost" size="icon" @click="dropField(row.kind, row.key, 'name')"><Trash2 class="size-4" /></Button>
            </div>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader class="flex-row items-center justify-between pb-3">
          <div>
            <CardTitle class="text-sm">Direction labels</CardTitle>
            <CardDescription>
              Name each direction the way the signs do — the 4 train runs Uptown and Downtown, not 0 and 1. GTFS
              carries direction_id and nothing that says what it means. An agency label covers every route it runs;
              a route label beats it, so the L can say 8 Av and Canarsie while the rest say Uptown and Downtown.
            </CardDescription>
          </div>
          <Button variant="outline" size="sm" @click="openDirAdd"><Plus class="size-4" /> Add</Button>
        </CardHeader>
        <CardContent>
          <div v-if="!directionRows.length" class="flex flex-col items-center gap-2 py-10 text-muted-foreground">
            <span class="text-sm">No labels — boards show the headsign alone.</span>
          </div>
          <div v-else class="grid gap-2">
            <div
              v-for="row in directionRows"
              :key="`${row.kind}:${row.key}:${row.dir}`"
              class="flex items-center gap-3 rounded-lg border border-border px-3 py-2"
            >
              <Badge variant="muted" class="shrink-0 text-[10px]">{{ row.kind }}</Badge>
              <span class="min-w-0 flex-1 truncate text-sm text-muted-foreground">{{ row.key }}</span>
              <Badge variant="secondary" class="shrink-0 font-mono text-[10px]">dir {{ row.dir }}</Badge>
              <span class="shrink-0 text-muted-foreground">→</span>
              <Input
                class="h-8 w-40 text-xs"
                :model-value="row.label"
                @update:model-value="(v) => setDirection(row.kind, row.key, row.dir, v)"
              />
              <Button variant="ghost" size="icon" @click="dropDirection(row.kind, row.key, row.dir)"><Trash2 class="size-4" /></Button>
            </div>
          </div>
        </CardContent>
      </Card>

      <div class="text-xs text-muted-foreground">
        {{ overrideCount }} override{{ overrideCount === 1 ? '' : 's' }} in the {{ scope }} layer.
        Changes apply on the next build.
      </div>
    </template>
  </div>

  <Dialog
    :open="dirAddOpen"
    title="Add direction label"
    description="Pick an agency or route, choose which direction_id it names, and type what the signs say."
    @update:open="(v) => (dirAddOpen = v)"
  >
    <div class="flex flex-col gap-4">
      <div class="flex gap-2">
        <div class="relative flex-1">
          <Search class="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input v-model="routeQuery" placeholder="Search routes and agencies" class="pl-9" />
        </div>
        <select
          v-model="newDirId"
          class="h-9 w-24 rounded-md border border-border bg-background px-2 font-mono text-sm"
          aria-label="direction_id"
        >
          <option value="0">dir 0</option>
          <option value="1">dir 1</option>
        </select>
        <Input v-model="newDirLabel" placeholder="Uptown" class="w-40" />
      </div>

      <div class="max-h-72 overflow-y-auto rounded-lg border border-border">
        <div v-if="agencies.length" class="border-b border-border bg-muted/40 px-3 py-1.5 text-xs font-medium text-muted-foreground">
          Agencies
        </div>
        <button
          v-for="a in agencies"
          :key="a.name"
          class="flex w-full items-center gap-2 border-b border-border/50 px-3 py-2 text-left text-sm last:border-0 hover:bg-accent/40"
          @click="addDirection(`agency:${a.name}`, newDirId, newDirLabel)"
        >
          <Badge variant="secondary" class="text-[10px] capitalize">{{ a.mode }}</Badge>
          <span class="truncate">{{ a.name }}</span>
        </button>

        <div class="border-y border-border bg-muted/40 px-3 py-1.5 text-xs font-medium text-muted-foreground">
          Routes
        </div>
        <button
          v-for="r in filteredRoutes"
          :key="r.id"
          class="flex w-full items-center gap-2 border-b border-border/50 px-3 py-2 text-left text-sm last:border-0 hover:bg-accent/40"
          @click="addDirection(`route:${r.short_name || r.id}`, newDirId, newDirLabel)"
        >
          <span class="w-14 shrink-0 truncate font-medium">{{ r.short_name || r.id }}</span>
          <span class="min-w-0 flex-1 truncate text-muted-foreground">{{ r.long_name }}</span>
          <Badge variant="muted" class="shrink-0 text-[10px] capitalize">{{ r.mode }}</Badge>
        </button>
      </div>

      <div class="flex items-end gap-2">
        <div class="flex w-32 flex-col gap-1.5">
          <Label for="dirkind">Kind</Label>
          <select
            id="dirkind"
            v-model="newDirKind"
            class="h-9 rounded-md border border-border bg-background px-2 text-sm"
          >
            <option value="route">route</option>
            <option value="agency">agency</option>
          </select>
        </div>
        <div class="flex flex-1 flex-col gap-1.5">
          <Label for="rawdirkey">Or type a key</Label>
          <Input id="rawdirkey" v-model="newDirKey" placeholder="MTA NYCT" class="font-mono" />
        </div>
        <Button
          :disabled="!newDirKey || !newDirLabel.trim()"
          @click="addDirection(`${newDirKind}:${newDirKey}`, newDirId, newDirLabel)"
        >
          Add
        </Button>
      </div>
    </div>
  </Dialog>

  <Dialog
    :open="addOpen"
    title="Add colour override"
    description="Pick an agency or route, or type any key. Names work as well as ids."
    class="max-w-xl"
    @update:open="(v) => (addOpen = v)"
  >
    <div class="flex flex-col gap-4">
      <div class="flex gap-2">
        <div class="relative flex-1">
          <Search class="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input v-model="routeQuery" placeholder="Search routes and agencies" class="pl-9" />
        </div>
        <Input v-model="newHex" placeholder="#0033A0" class="w-32 font-mono" />
      </div>

      <div class="max-h-72 overflow-y-auto rounded-lg border border-border">
        <div v-if="agencies.length" class="border-b border-border bg-muted/40 px-3 py-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
          Agencies
        </div>
        <button
          v-for="a in agencies"
          :key="a.name"
          class="flex w-full items-center gap-2 border-b border-border/50 px-3 py-2 text-left text-sm last:border-0 hover:bg-accent/40"
          @click="addOverride(`agency:${a.name}`, newHex)"
        >
          <Badge variant="secondary" class="text-[10px] capitalize">{{ a.mode }}</Badge>
          <span class="truncate">{{ a.name }}</span>
        </button>

        <div class="border-y border-border bg-muted/40 px-3 py-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
          Routes
        </div>
        <button
          v-for="r in filteredRoutes"
          :key="r.id"
          class="flex w-full items-center gap-2 border-b border-border/50 px-3 py-2 text-left text-sm last:border-0 hover:bg-accent/40"
          @click="addOverride(`route:${r.short_name || r.id}`, newHex)"
        >
          <span class="size-4 shrink-0 rounded border border-border" :style="{ background: r.color ? `#${r.color}` : 'transparent' }" />
          <span class="w-14 shrink-0 truncate font-medium">{{ r.short_name || r.id }}</span>
          <span class="min-w-0 flex-1 truncate text-muted-foreground">{{ r.long_name }}</span>
          <Badge variant="muted" class="shrink-0 text-[10px] capitalize">{{ r.mode }}</Badge>
        </button>
      </div>

      <div class="flex items-end gap-2">
        <div class="flex flex-1 flex-col gap-1.5">
          <Label for="rawkey">Or type a key</Label>
          <Input id="rawkey" v-model="newKey" placeholder="agency:Metra" class="font-mono" />
        </div>
        <Button :disabled="!newKey || !/^#?[0-9a-fA-F]{6}$/.test(newHex)" @click="addOverride(newKey, newHex)">
          Add
        </Button>
      </div>
    </div>
  </Dialog>

  <Dialog
    :open="nameAddOpen"
    title="Add name override"
    description="Pick a route, or type any key. Ids, short names and long names all match."
    class="max-w-xl"
    @update:open="(v) => (nameAddOpen = v)"
  >
    <div class="flex flex-col gap-4">
      <div class="flex gap-2">
        <div class="relative flex-1">
          <Search class="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input v-model="routeQuery" placeholder="Search routes and agencies" class="pl-9" />
        </div>
        <Input v-model="newName" placeholder="Blue" class="w-40" />
      </div>

      <div class="max-h-72 overflow-y-auto rounded-lg border border-border">
        <div v-if="agencies.length" class="border-b border-border bg-muted/40 px-3 py-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
          Agencies
        </div>
        <button
          v-for="a in agencies"
          :key="a.name"
          class="flex w-full items-center gap-2 border-b border-border/50 px-3 py-2 text-left text-sm last:border-0 hover:bg-accent/40"
          @click="addName(`agency:${a.name}`, newName)"
        >
          <Badge variant="secondary" class="text-[10px] capitalize">{{ a.mode }}</Badge>
          <span class="truncate">{{ a.name }}</span>
        </button>

        <div class="border-y border-border bg-muted/40 px-3 py-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
          Routes
        </div>
        <button
          v-for="r in filteredRoutes"
          :key="r.id"
          class="flex w-full items-center gap-2 border-b border-border/50 px-3 py-2 text-left text-sm last:border-0 hover:bg-accent/40"
          @click="addName(`route:${r.long_name || r.short_name || r.id}`, newName)"
        >
          <span class="w-14 shrink-0 truncate font-medium">{{ r.short_name || r.id }}</span>
          <span class="min-w-0 flex-1 truncate text-muted-foreground">{{ r.long_name }}</span>
          <Badge variant="muted" class="shrink-0 text-[10px] capitalize">{{ r.mode }}</Badge>
        </button>
      </div>

      <div class="flex items-end gap-2">
        <div class="flex w-32 flex-col gap-1.5">
          <Label for="namekind">Kind</Label>
          <select
            id="namekind"
            v-model="newNameKind"
            class="h-9 rounded-md border border-border bg-background px-2 text-sm"
          >
            <option value="route">route</option>
            <option value="agency">agency</option>
            <option value="stop">stop</option>
          </select>
        </div>
        <div class="flex flex-1 flex-col gap-1.5">
          <Label for="rawnamekey">Or type a key</Label>
          <Input id="rawnamekey" v-model="newNameKey" placeholder="Red Line" class="font-mono" />
        </div>
        <Button :disabled="!newNameKey || !newName.trim()" @click="addName(`${newNameKind}:${newNameKey}`, newName)">
          Add
        </Button>
      </div>
    </div>
  </Dialog>
</template>
