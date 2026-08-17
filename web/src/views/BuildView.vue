<script setup lang="ts">
import { ref, computed, watch, nextTick, onMounted } from 'vue'
import { Play, Terminal, CheckCircle2, XCircle, Gauge, Layers, AlertTriangle } from 'lucide-vue-next'
import PageHeader from '@/components/PageHeader.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Card from '@/components/ui/Card.vue'
import CardHeader from '@/components/ui/CardHeader.vue'
import CardTitle from '@/components/ui/CardTitle.vue'
import CardContent from '@/components/ui/CardContent.vue'
import Progress from '@/components/ui/Progress.vue'
import Spinner from '@/components/ui/Spinner.vue'
import Select from '@/components/ui/Select.vue'
import GlobalNotice from '@/components/GlobalNotice.vue'
import { api, type Scenario } from '@/lib/api'
import { feed, currentFeed, isGlobal, run } from '@/lib/store'
import { toast } from '@/lib/toast'

const scenarios = ref<Scenario[]>([])
const scenario = ref('__all')
const starting = ref(false)
const logEl = ref<HTMLElement | null>(null)
const stick = ref(true)

// The pipeline announces each stage on its own log line ("match: … (12.3s)").
// Those five words are the only progress signal there is, so the bar is
// driven off which of them have appeared rather than a fake timer.
const STAGES = ['chart', 'match', 'split', 'order', 'fair'] as const
const stageState = computed(() => {
  const seen = new Set<string>()
  for (const line of run.value.log) {
    const m = /^(chart|match|split|order|fair|direct|scenario|bus):/.exec(line)
    if (m) seen.add(m[1] === 'direct' ? 'fair' : m[1])
  }
  return STAGES.map((s) => ({ name: s, done: seen.has(s) }))
})
const fraction = computed(() => stageState.value.filter((s) => s.done).length / STAGES.length)

const lastError = computed(() => {
  if (run.value.running || run.value.ok) return ''
  return run.value.log.filter((l) => /portolan:|error|panic|FAIL/i.test(l)).slice(-1)[0] ?? ''
})

// sentinel rather than '': see MapView — reka-ui cannot represent an
// empty-valued option as a selection.
const ALL = '__all'
const scenarioOptions = computed(() => [
  { value: ALL, label: 'All service (union)' },
  ...scenarios.value.map((s) => ({ value: s.id, label: `${s.label}${s.built ? ' ✓' : ''}` })),
])

async function loadScenarios() {
  scenarios.value = []
  if (!feed.value || isGlobal.value) return // builds are per-feed
  try {
    const r = await api.scenarios(feed.value)
    if (r.available && r.scenarios) scenarios.value = r.scenarios
  } catch {
    /* a feed without a usable calendar simply has none */
  }
}
watch(feed, loadScenarios, { immediate: true })
onMounted(loadScenarios)

watch(
  () => run.value.log.length,
  async () => {
    if (!stick.value) return
    await nextTick()
    if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight
  },
)

function onScroll() {
  const el = logEl.value
  if (el) stick.value = el.scrollHeight - el.scrollTop - el.clientHeight < 40
}

async function start(cmd: 'chart' | 'sound') {
  if (!feed.value || isGlobal.value) return
  starting.value = true
  try {
    await api.run(feed.value, cmd, cmd === 'chart' && scenario.value !== ALL ? scenario.value : undefined)
    stick.value = true
    toast({ title: cmd === 'chart' ? 'Build started' : 'Scoring started', variant: 'success' })
  } catch (e: any) {
    toast({ title: 'Could not start', description: e.message, variant: 'error' })
  } finally {
    starting.value = false
  }
}

const missingInputs = computed(() => {
  const s = currentFeed.value?.status
  if (!s) return []
  const out: string[] = []
  s.gtfs.filter((f) => !f.ok).forEach((f) => out.push(f.path))
  if (!s.rail.ok) out.push(s.rail.path)
  if (s.streets && !s.streets.ok) out.push(s.streets.path)
  return out
})
</script>

<template>
  <PageHeader title="Build" :subtitle="currentFeed ? `${currentFeed.name || currentFeed.id} — run the pipeline` : 'Pick a feed'">
    <template #actions>
      <Button variant="outline" :disabled="!feed || isGlobal || run.running" @click="start('sound')">
        <Gauge class="size-4" /> Score
      </Button>
      <Button :disabled="!feed || isGlobal || run.running || starting" @click="start('chart')">
        <Spinner v-if="run.running || starting" class="size-4" />
        <Play v-else class="size-4" />
        Build
      </Button>
    </template>
  </PageHeader>

  <GlobalNotice v-if="isGlobal" title="Build" />
  <div v-else class="space-y-6 p-8">
    <div v-if="missingInputs.length" class="flex gap-2 rounded-lg border border-[var(--warning)]/40 bg-[var(--warning)]/10 p-3 text-xs text-[var(--warning)]">
      <AlertTriangle class="mt-0.5 size-3.5 shrink-0" />
      <span>Missing inputs — the build will fail: <span class="font-mono">{{ missingInputs.join(', ') }}</span></span>
    </div>

    <div class="grid gap-4 md:grid-cols-[280px_1fr]">
      <Card>
        <CardHeader class="pb-3"><CardTitle class="text-sm">Service scenario</CardTitle></CardHeader>
        <CardContent class="space-y-3">
          <Select v-model="scenario" :options="scenarioOptions" placeholder="All service (union)" />
          <p class="text-xs text-muted-foreground">
            A scenario builds a complete layout for just the service that runs then — lines re-centre and re-pack
            when others vanish, which no client-side filter could do.
          </p>
          <div v-if="scenarios.length" class="text-xs text-muted-foreground">
            {{ scenarios.filter((s) => s.built).length }} of {{ scenarios.length }} built
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader class="flex-row items-center justify-between pb-3">
          <CardTitle class="text-sm">Pipeline</CardTitle>
          <Badge v-if="run.running" variant="info"><Spinner class="size-3" /> {{ run.cmd || 'running' }}</Badge>
          <Badge v-else-if="run.done && run.ok" variant="success"><CheckCircle2 class="size-3" /> succeeded</Badge>
          <Badge v-else-if="run.done" variant="destructive"><XCircle class="size-3" /> failed</Badge>
          <Badge v-else variant="muted">idle</Badge>
        </CardHeader>
        <CardContent class="space-y-3">
          <Progress
            size="md"
            :indeterminate="run.running && fraction === 0"
            :value="fraction"
            :variant="run.done && !run.ok ? 'destructive' : 'default'"
          />
          <ol class="flex flex-wrap gap-x-4 gap-y-1 text-xs">
            <li
              v-for="s in stageState"
              :key="s.name"
              class="flex items-center gap-1.5"
              :class="s.done ? 'text-foreground' : 'text-muted-foreground'"
            >
              <CheckCircle2 v-if="s.done" class="size-3 text-[var(--success)]" />
              <span v-else class="size-1.5 rounded-full bg-muted-foreground/40" />
              {{ s.name }}
            </li>
          </ol>
          <p v-if="lastError" class="font-mono text-xs text-destructive">{{ lastError }}</p>
        </CardContent>
      </Card>
    </div>

    <Card>
      <CardHeader class="flex-row items-center justify-between pb-3">
        <CardTitle class="flex items-center gap-1.5 text-sm"><Terminal class="size-4" /> Output</CardTitle>
        <span class="font-mono text-xs text-muted-foreground">{{ run.cmd }}</span>
      </CardHeader>
      <CardContent>
        <div
          ref="logEl"
          class="h-[calc(100vh-30rem)] min-h-64 overflow-auto rounded-lg border border-border bg-[#0b0b0c] p-3 font-mono text-xs leading-relaxed"
          @scroll="onScroll"
        >
          <div v-if="!run.log.length" class="flex h-full items-center justify-center text-muted-foreground">
            <Layers class="mr-2 size-4" /> No output yet — start a build.
          </div>
          <div
            v-for="(line, i) in run.log"
            :key="i"
            class="whitespace-pre-wrap break-all"
            :class="/portolan:|panic|FAIL/i.test(line) ? 'text-destructive' : 'text-foreground/90'"
          >
            {{ line }}
          </div>
          <div v-if="!stick && run.log.length" class="pointer-events-none sticky bottom-0 flex justify-center">
            <button
              class="pointer-events-auto rounded-full border border-border bg-card px-3 py-1 text-[11px] text-muted-foreground shadow hover:text-foreground"
              @click="stick = true"
            >
              ↓ Jump to latest
            </button>
          </div>
        </div>
      </CardContent>
    </Card>
  </div>
</template>
