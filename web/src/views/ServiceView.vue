<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import { Clock, Play, CheckCircle2, Loader2 } from 'lucide-vue-next'
import PageHeader from '@/components/PageHeader.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Card from '@/components/ui/Card.vue'
import CardHeader from '@/components/ui/CardHeader.vue'
import CardTitle from '@/components/ui/CardTitle.vue'
import CardDescription from '@/components/ui/CardDescription.vue'
import CardContent from '@/components/ui/CardContent.vue'
import Spinner from '@/components/ui/Spinner.vue'
import GlobalNotice from '@/components/GlobalNotice.vue'
import { api, type Scenario } from '@/lib/api'
import { feed, currentFeed, isGlobal, run } from '@/lib/store'
import { toast } from '@/lib/toast'

const DAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun']
const loading = ref(false)
const scenarios = ref<Scenario[]>([])
const grid = ref<string[][]>([])
const error = ref('')
const hovered = ref('')
const selected = ref('')
const building = ref('')

// A stable colour per scenario so the week grid reads as blocks. Hue only —
// the palette is decorative, and the console's own colours mean status.
const hueOf = (id: string) => {
  let h = 0
  for (const ch of id) h = (h * 31 + ch.charCodeAt(0)) % 360
  return h
}
const cellStyle = (id: string) => {
  if (!id) return { background: 'transparent' }
  const active = !selected.value || selected.value === id || hovered.value === id
  return {
    background: `oklch(0.62 0.13 ${hueOf(id)} / ${active ? 0.85 : 0.25})`,
  }
}

const byId = computed(() => Object.fromEntries(scenarios.value.map((s) => [s.id, s])))
const current = computed(() => (selected.value ? byId.value[selected.value] : null))

async function load() {
  scenarios.value = []
  grid.value = []
  error.value = ''
  selected.value = ''
  if (!feed.value || isGlobal.value) return // scenarios are per-feed
  loading.value = true
  try {
    const r = await api.scenarios(feed.value)
    if (!r.available) {
      error.value = r.error || 'This feed has no usable calendar.'
      return
    }
    scenarios.value = r.scenarios ?? []
    grid.value = r.grid ?? []
  } catch (e: any) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}
watch(feed, load, { immediate: true })

async function build(id: string) {
  if (!feed.value || isGlobal.value) return
  building.value = id
  try {
    await api.run(feed.value, 'chart', id)
    toast({ title: 'Scenario build started', description: byId.value[id]?.label, variant: 'success' })
  } catch (e: any) {
    toast({ title: 'Could not start build', description: e.message, variant: 'error' })
  } finally {
    building.value = ''
  }
}

watch(
  () => run.value.done,
  (done) => done && load(),
)
</script>

<template>
  <PageHeader
    title="Service"
    :subtitle="currentFeed ? `${currentFeed.name || currentFeed.id} — distinct maps across the week` : 'Pick a feed'"
  />

  <GlobalNotice v-if="isGlobal" title="Service" />
  <div v-else class="space-y-6 p-8">
    <div v-if="loading" class="flex justify-center py-16"><Spinner class="size-6" /></div>

    <Card v-else-if="error">
      <CardContent class="flex flex-col items-center gap-2 py-14 text-center">
        <Clock class="size-8 text-muted-foreground opacity-40" />
        <p class="font-medium">No service scenarios</p>
        <p class="max-w-md text-sm text-muted-foreground">{{ error }}</p>
      </CardContent>
    </Card>

    <template v-else>
      <Card>
        <CardHeader class="pb-3">
          <CardTitle class="text-sm">The week</CardTitle>
          <CardDescription>
            Every hour of the week maps to the scenario that draws it. Blocks of one colour are hours that draw
            identical ink — click one to select it.
          </CardDescription>
        </CardHeader>
        <CardContent class="overflow-x-auto">
          <div class="min-w-[720px]">
            <div class="flex gap-px pl-10 pb-1">
              <div v-for="h in 24" :key="h" class="w-full text-center text-[10px] tabular-nums text-muted-foreground">
                {{ (h - 1) % 6 === 0 ? String(h - 1).padStart(2, '0') : '' }}
              </div>
            </div>
            <div v-for="(row, d) in grid" :key="d" class="flex items-center gap-px">
              <div class="w-10 shrink-0 text-xs text-muted-foreground">{{ DAYS[d] }}</div>
              <button
                v-for="(id, h) in row"
                :key="h"
                class="h-6 w-full rounded-[2px] transition-opacity"
                :style="cellStyle(id)"
                :title="id ? `${DAYS[d]} ${String(h).padStart(2, '0')}:00 — ${byId[id]?.label ?? id}` : 'no service'"
                @click="selected = selected === id ? '' : id"
                @mouseenter="hovered = id"
                @mouseleave="hovered = ''"
              />
            </div>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader class="flex-row items-center justify-between pb-3">
          <div>
            <CardTitle class="text-sm">Scenarios</CardTitle>
            <CardDescription>
              {{ scenarios.filter((s) => s.built).length }} of {{ scenarios.length }} built. Each is a complete
              re-layout, not a filter.
            </CardDescription>
          </div>
        </CardHeader>
        <CardContent class="p-0">
          <div
            v-for="s in scenarios"
            :key="s.id"
            class="flex items-center gap-4 border-b border-border px-5 py-3 transition-colors last:border-0"
            :class="selected === s.id ? 'bg-accent/40' : 'hover:bg-accent/20'"
            @mouseenter="hovered = s.id"
            @mouseleave="hovered = ''"
          >
            <span class="size-3 shrink-0 rounded-full" :style="{ background: `oklch(0.62 0.13 ${hueOf(s.id)})` }" />
            <div class="min-w-0 flex-1">
              <div class="truncate text-sm font-medium">{{ s.label }}</div>
              <div class="font-mono text-xs text-muted-foreground">{{ s.id }} · {{ s.patterns }} patterns</div>
            </div>
            <Badge v-if="s.built" variant="success"><CheckCircle2 class="size-3" /> built</Badge>
            <Badge v-else variant="muted">not built</Badge>
            <Button
              variant="outline"
              size="sm"
              :disabled="run.running || building === s.id"
              @click="build(s.id)"
            >
              <Loader2 v-if="building === s.id" class="size-4 animate-spin" /><Play v-else class="size-4" />
              {{ s.built ? 'Rebuild' : 'Build' }}
            </Button>
          </div>
        </CardContent>
      </Card>

      <div v-if="current" class="text-xs text-muted-foreground">
        Selected: <span class="font-medium text-foreground">{{ current.label }}</span> —
        {{ current.patterns }} patterns.
      </div>
    </template>
  </div>
</template>
