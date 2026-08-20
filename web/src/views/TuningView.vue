<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { RotateCcw, Play, Info, Loader2 } from 'lucide-vue-next'
import PageHeader from '@/components/PageHeader.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Card from '@/components/ui/Card.vue'
import CardHeader from '@/components/ui/CardHeader.vue'
import CardTitle from '@/components/ui/CardTitle.vue'
import CardDescription from '@/components/ui/CardDescription.vue'
import CardContent from '@/components/ui/CardContent.vue'
import Input from '@/components/ui/Input.vue'
import Spinner from '@/components/ui/Spinner.vue'
import GlobalNotice from '@/components/GlobalNotice.vue'
import { feed, currentFeed, isGlobal, run } from '@/lib/store'
import { toast } from '@/lib/toast'

type Dials = Record<string, number>

const defaults = ref<Dials>({})
const values = ref<Dials>({})
const loading = ref(true)
const starting = ref(false)

// Dials are per feed: a tolerance tuned on Berlin's trams is meaningless
// on NYC's subway, and losing a tuning session by switching feeds was
// the old panel's sharpest edge.
const storeKey = () => `portolan.dials.${feed.value}`

async function load() {
  loading.value = true
  try {
    const d: Dials = await (await fetch('/api/params')).json()
    defaults.value = d
    let saved: Dials = {}
    try {
      saved = JSON.parse(localStorage.getItem(storeKey()) || '{}')
    } catch {
      /* a corrupt entry should not cost the defaults */
    }
    values.value = { ...d, ...saved }
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(feed, load)

function persist() {
  const diff: Dials = {}
  for (const [k, v] of Object.entries(values.value)) if (v !== defaults.value[k]) diff[k] = v
  if (Object.keys(diff).length) localStorage.setItem(storeKey(), JSON.stringify(diff))
  else localStorage.removeItem(storeKey())
}

function setVal(k: string, raw: string) {
  const n = Number(raw)
  if (Number.isNaN(n)) return
  values.value[k] = n
  persist()
}

function resetAll() {
  values.value = { ...defaults.value }
  persist()
  toast({ title: 'Dials reset to defaults', variant: 'success' })
}
function resetOne(k: string) {
  values.value[k] = defaults.value[k]
  persist()
}

// Keys are stage-prefixed by construction (match_reach, split_merge_dist);
// the prefix IS the grouping, so a new dial in the Go struct appears here
// under its stage with no UI change.
const STAGES = ['match', 'split', 'fair'] as const
const groups = computed(() => {
  const g: Record<string, string[]> = { match: [], split: [], fair: [], general: [] }
  for (const k of Object.keys(defaults.value)) {
    const pre = k.split('_')[0]
    if ((STAGES as readonly string[]).includes(pre)) g[pre].push(k)
    else g.general.push(k)
  }
  return g
})

const modified = computed(() =>
  Object.keys(defaults.value).filter((k) => values.value[k] !== defaults.value[k]),
)
const isMod = (k: string) => values.value[k] !== defaults.value[k]
const shortName = (k: string, group: string) => (group === 'general' ? k : k.slice(group.length + 1))

/** Build with the current dials — they ride in the run request body, so
 *  nothing is written to config and a tuning run never disturbs a
 *  teammate's build. */
async function buildWith() {
  if (!feed.value || isGlobal.value) return
  starting.value = true
  try {
    const r = await fetch(`/api/run?cmd=chart&feed=${encodeURIComponent(feed.value)}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(values.value),
    })
    if (!r.ok) throw new Error(await r.text())
    toast({
      title: 'Build started',
      description: modified.value.length
        ? `${modified.value.length} dial${modified.value.length === 1 ? '' : 's'} off default`
        : 'default dials',
      variant: 'success',
    })
  } catch (e: any) {
    toast({ title: 'Could not start build', description: e.message, variant: 'error' })
  } finally {
    starting.value = false
  }
}
</script>

<template>
  <PageHeader
    title="Tuning"
    :subtitle="currentFeed ? `${currentFeed.name || currentFeed.id} — pipeline dials` : 'Pick a feed'"
  >
    <template #actions>
      <Badge v-if="modified.length" variant="warning">{{ modified.length }} modified</Badge>
      <Button variant="outline" :disabled="!modified.length" @click="resetAll">
        <RotateCcw class="size-4" /> Reset
      </Button>
      <Button :disabled="!feed || isGlobal || run.running || starting" @click="buildWith">
        <Loader2 v-if="starting || run.running" class="size-4 animate-spin" /><Play v-else class="size-4" />
        Build with these
      </Button>
    </template>
  </PageHeader>

  <GlobalNotice v-if="isGlobal" title="Tuning" />
  <div v-else class="space-y-6 p-4 sm:p-8">
    <div class="flex gap-2 rounded-lg border border-border bg-muted/40 p-3 text-xs text-muted-foreground">
      <Info class="mt-0.5 size-3.5 shrink-0" />
      <span>
        Dials are kept per feed in this browser and travel with the build request — they are never written to
        <span class="font-mono">portolan.json</span>, so a tuning run cannot disturb anyone else's build. Values
        off default are highlighted.
      </span>
    </div>

    <div v-if="loading" class="flex justify-center py-16"><Spinner class="size-6" /></div>

    <template v-else>
      <Card v-for="(keys, group) in groups" :key="group" v-show="keys.length">
        <CardHeader class="pb-3">
          <CardTitle class="text-sm capitalize">{{ group }}</CardTitle>
          <CardDescription v-if="group === 'match'">
            Viterbi costs and reach — how a pattern picks its way through the OSM graph.
          </CardDescription>
          <CardDescription v-else-if="group === 'split'">
            Junction detection and parallel-edge merging — what counts as one corridor.
          </CardDescription>
          <CardDescription v-else-if="group === 'fair'">
            Drawing: cut lengths, gaps, turn limits and fillet radius.
          </CardDescription>
          <CardDescription v-else>Loading and coverage.</CardDescription>
        </CardHeader>
        <CardContent>
          <!-- label above the field, not beside it: dial names are long
               ("bonus_color", "co_merge_dist") and a side-by-side row
               truncated them to "b…" at anything under a wide window. -->
          <div class="grid gap-x-4 gap-y-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
            <div v-for="k in keys" :key="k" class="flex flex-col gap-1">
              <label :for="k" class="flex items-center gap-1 text-xs text-muted-foreground" :title="k">
                <span class="truncate">{{ shortName(k, group as string) }}</span>
                <button
                  v-if="isMod(k)"
                  class="shrink-0 text-[var(--warning)] hover:text-foreground"
                  :title="`Reset to ${defaults[k]}`"
                  @click="resetOne(k)"
                >
                  <RotateCcw class="size-3" />
                </button>
              </label>
              <Input
                :id="k"
                type="number"
                step="any"
                class="h-8 text-sm tabular-nums"
                :class="isMod(k) ? 'border-[var(--warning)] text-[var(--warning)]' : ''"
                :model-value="values[k]"
                @update:model-value="(v) => setVal(k, v)"
              />
            </div>
          </div>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
