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
import { api, type StyleConfig, type StyleSet } from '@/lib/api'
import { feed, currentCity } from '@/lib/store'
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
const city = ref<StyleConfig>({ modes: {}, colors: {} })
const scope = ref<'city' | 'global'>('city')
const globalCfg = ref<StyleConfig>({ modes: {}, colors: {} })

const layer = computed(() => (scope.value === 'city' ? city.value : globalCfg.value))

async function load() {
  if (!feed.value) return
  loading.value = true
  try {
    const [res, cfg] = await Promise.all([api.style(feed.value), api.styleConfig(feed.value)])
    resolved.value = res
    city.value = { modes: cfg.city?.modes ?? {}, colors: cfg.city?.colors ?? {} }
    globalCfg.value = { modes: cfg.global?.modes ?? {}, colors: cfg.global?.colors ?? {} }
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

const colorRows = computed(() =>
  Object.entries(layer.value.colors ?? {}).map(([k, v]) => ({ key: k, hex: v.replace('#', '') })),
)

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
  ;(layer.value.colors ??= {})[key] = hex.replace('#', '').toUpperCase()
  addOpen.value = false
}
function removeOverride(key: string) {
  if (layer.value.colors) delete layer.value.colors[key]
}

async function save() {
  if (!feed.value) return
  saving.value = true
  try {
    await api.saveStyleConfig(feed.value, { global: globalCfg.value, city: city.value })
    await load()
    toast({
      title: 'Style saved',
      description: 'Rebuild the city for it to show on the map.',
      variant: 'success',
    })
  } catch (e: any) {
    toast({ title: 'Could not save style', description: e.message, variant: 'error' })
  } finally {
    saving.value = false
  }
}

const overrideCount = computed(
  () => Object.keys(layer.value.modes ?? {}).length + Object.keys(layer.value.colors ?? {}).length,
)
</script>

<template>
  <PageHeader
    title="Style"
    :subtitle="currentCity ? `${currentCity.name || currentCity.id} — colours, weights and trunking` : 'Pick a city'"
  >
    <template #actions>
      <div class="flex rounded-lg bg-muted p-1">
        <button
          v-for="s in (['city', 'global'] as const)"
          :key="s"
          class="rounded-md px-3 py-1 text-sm font-medium capitalize transition-all"
          :class="scope === s ? 'bg-background text-foreground shadow' : 'text-muted-foreground'"
          @click="scope = s"
        >
          {{ s }}
        </button>
      </div>
      <Button :disabled="saving || loading" @click="save">
        <Loader2 v-if="saving" class="size-4 animate-spin" /><Save v-else class="size-4" /> Save
      </Button>
    </template>
  </PageHeader>

  <div class="space-y-6 p-8">
    <div class="flex gap-2 rounded-lg border border-border bg-muted/40 p-3 text-xs text-muted-foreground">
      <Info class="mt-0.5 size-3.5 shrink-0" />
      <span v-if="scope === 'city'">
        Editing <strong>{{ currentCity?.name || feed }}</strong> only. These values layer on top of the global block,
        field by field — set one and the rest keep inheriting. Fields shown greyed are inherited, not set here.
      </span>
      <span v-else>
        Editing the <strong>global</strong> block — the baseline for every city. A city's own overrides still win.
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
              <Badge variant="muted" class="shrink-0 text-[10px]">
                {{ row.key.startsWith('agency:') ? 'agency' : 'route' }}
              </Badge>
              <span class="min-w-0 flex-1 truncate text-sm">{{ row.key.replace(/^(agency|route):/, '') }}</span>
              <Input
                class="h-8 w-28 font-mono text-xs"
                :model-value="row.hex"
                @update:model-value="(v) => ((layer.colors as any)[row.key] = v.replace('#', '').toUpperCase())"
              />
              <Button variant="ghost" size="icon" @click="removeOverride(row.key)"><Trash2 class="size-4" /></Button>
            </div>
          </div>
          <p v-if="colorRows.length" class="mt-3 text-xs text-muted-foreground">
            An agency trunk always takes the agency override — a route override inside one would repaint whichever
            member sorted first, so the shared ribbon would change colour at every membership seam.
          </p>
        </CardContent>
      </Card>

      <div class="text-xs text-muted-foreground">
        {{ overrideCount }} override{{ overrideCount === 1 ? '' : 's' }} in the {{ scope }} layer.
        Changes apply on the next build.
      </div>
    </template>
  </div>

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
</template>
