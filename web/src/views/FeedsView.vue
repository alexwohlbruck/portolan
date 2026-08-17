<script setup lang="ts">
import { ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import {
  Plus, MapPin, CheckCircle2, AlertTriangle, Trash2, Pencil, Hammer, Loader2, Map as MapIcon,
} from 'lucide-vue-next'
import PageHeader from '@/components/PageHeader.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Card from '@/components/ui/Card.vue'
import CardContent from '@/components/ui/CardContent.vue'
import Dialog from '@/components/ui/Dialog.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { api, type Feed } from '@/lib/api'
import { feeds, feedsLoading, refreshFeeds, feed } from '@/lib/store'
import { toast } from '@/lib/toast'
import { formatNumber } from '@/lib/utils'

const router = useRouter()
const editing = ref<Feed | null>(null)
const isNew = ref(false)
const saving = ref(false)
const confirmDelete = ref<Feed | null>(null)
const bboxText = ref('')

const blank = (): Feed => ({
  id: '', name: '', gtfs: '', rail: '', streets: '', out: '', network: '', bbox: undefined,
})

function openNew() {
  editing.value = blank()
  bboxText.value = ''
  isNew.value = true
}

function openEdit(c: Feed) {
  editing.value = JSON.parse(JSON.stringify(c))
  bboxText.value = c.bbox?.join(', ') ?? ''
  isNew.value = false
}

// Paths are typed by hand and mistyping one is the single most common way
// a build fails, so derive the conventional ones from the id the moment
// it's entered rather than making someone remember the layout.
function autofill() {
  const c = editing.value
  if (!c || !isNew.value || !c.id) return
  if (!c.out) c.out = `build/${c.id}.geojson`
  if (!c.rail) c.rail = `build/${c.id}-rail.geojson`
  if (!c.network) c.network = `sketches/network-${c.id}.json`
}

const bboxError = computed(() => {
  const t = bboxText.value.trim()
  if (!t) return ''
  const parts = t.split(',').map((s) => Number(s.trim()))
  if (parts.length !== 4 || parts.some((n) => Number.isNaN(n))) return 'Needs four numbers: west, south, east, north'
  if (parts[0] >= parts[2] || parts[1] >= parts[3]) return 'West must be less than east, south less than north'
  return ''
})

const canSave = computed(
  () => !!editing.value?.id.trim() && !!editing.value?.gtfs.trim() && !!editing.value?.rail.trim() && !bboxError.value,
)

async function save() {
  const c = editing.value
  if (!c || !canSave.value) return
  saving.value = true
  try {
    const t = bboxText.value.trim()
    c.bbox = t ? (t.split(',').map((s) => Number(s.trim())) as any) : undefined
    if (!c.out) c.out = `build/${c.id}.geojson`
    await api.saveFeed(c.id, c)
    await refreshFeeds()
    toast({ title: isNew.value ? 'Feed added' : 'Feed saved', description: c.name || c.id, variant: 'success' })
    editing.value = null
  } catch (e: any) {
    toast({ title: 'Could not save feed', description: e.message, variant: 'error' })
  } finally {
    saving.value = false
  }
}

async function doDelete() {
  const c = confirmDelete.value
  if (!c) return
  try {
    await api.deleteFeed(c.id)
    await refreshFeeds()
    toast({ title: `Removed ${c.name || c.id}`, variant: 'success' })
  } catch (e: any) {
    toast({ title: 'Could not remove feed', description: e.message, variant: 'error' })
  } finally {
    confirmDelete.value = null
  }
}

function openBuild(c: Feed) {
  feed.value = c.id
  router.push('/build')
}

function openMap(c: Feed) {
  feed.value = c.id
  router.push('/map')
}

const missing = (c: Feed) => {
  const s = c.status
  if (!s) return []
  const out: string[] = []
  s.gtfs.filter((f) => !f.ok).forEach((f) => out.push(`GTFS ${f.path}`))
  if (!s.rail.ok) out.push(`rail ${s.rail.path}`)
  if (s.streets && !s.streets.ok) out.push(`streets ${s.streets.path}`)
  return out
}
const mb = (n?: number) => (n ? `${(n / 1e6).toFixed(1)} MB` : '')
</script>

<template>
  <PageHeader title="Feeds" subtitle="GTFS feeds, their extracts and builds">
    <template #actions>
      <Button @click="openNew"><Plus class="size-4" /> Add feed</Button>
    </template>
  </PageHeader>

  <div class="p-8">
    <div v-if="feedsLoading && !feeds.length" class="flex justify-center py-16"><Spinner class="size-6" /></div>

    <div v-else-if="!feeds.length" class="rounded-xl border border-dashed border-border py-16 text-center">
      <div class="mx-auto flex size-12 items-center justify-center rounded-xl bg-muted text-muted-foreground">
        <MapPin class="size-6" />
      </div>
      <p class="mt-3 font-medium">No feeds configured</p>
      <p class="mt-1 text-sm text-muted-foreground">Add one to point portolan at a GTFS feed and an OSM extract.</p>
      <Button class="mt-4" @click="openNew"><Plus class="size-4" /> Add your first feed</Button>
    </div>

    <div v-else class="grid gap-3">
      <Card v-for="c in feeds" :key="c.id">
        <CardContent class="flex items-center gap-4 py-4">
          <div
            class="flex size-9 shrink-0 items-center justify-center rounded-lg"
            :class="missing(c).length ? 'bg-[var(--warning)]/15 text-[var(--warning)]' : 'bg-muted text-muted-foreground'"
          >
            <MapPin class="size-4" />
          </div>

          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <span class="truncate font-medium">{{ c.name || c.id }}</span>
              <Badge variant="muted" class="font-mono text-[10px]">{{ c.id }}</Badge>
              <Badge v-if="c.streets" variant="secondary" class="text-[10px]">buses</Badge>
              <Badge v-if="c.status?.scenarios_built" variant="info" class="text-[10px]">
                {{ c.status.scenarios_built }} scenarios
              </Badge>
            </div>
            <div class="mt-0.5 truncate font-mono text-xs text-muted-foreground">
              {{ c.status?.gtfs.length ?? 0 }} zip{{ (c.status?.gtfs.length ?? 0) === 1 ? '' : 's' }} ·
              {{ c.out }}
            </div>
            <div v-if="missing(c).length" class="mt-1 flex items-center gap-1.5 text-xs text-[var(--warning)]">
              <AlertTriangle class="size-3.5 shrink-0" />
              <span class="truncate">missing {{ missing(c).join(', ') }}</span>
            </div>
          </div>

          <div class="hidden shrink-0 text-right sm:block">
            <div v-if="c.status?.build?.ok" class="flex items-center justify-end gap-1.5 text-xs text-[var(--success)]">
              <CheckCircle2 class="size-3.5" /> built
            </div>
            <div v-else class="text-xs text-muted-foreground">not built</div>
            <div class="text-xs text-muted-foreground tabular-nums">{{ mb(c.status?.build?.size) }}</div>
          </div>

          <div class="flex shrink-0 items-center gap-1">
            <Button variant="ghost" size="icon" title="View map" @click="openMap(c)"><MapIcon class="size-4" /></Button>
            <Button variant="ghost" size="icon" title="Build" @click="openBuild(c)"><Hammer class="size-4" /></Button>
            <Button variant="ghost" size="icon" title="Edit" @click="openEdit(c)"><Pencil class="size-4" /></Button>
            <Button variant="ghost" size="icon" title="Remove" @click="confirmDelete = c">
              <Trash2 class="size-4" />
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  </div>

  <!-- editor -->
  <Dialog
    :open="!!editing"
    :title="isNew ? 'Add feed' : `Edit ${editing?.name || editing?.id}`"
    description="Paths are relative to the repo root. The id names the config row and the build outputs."
    class="max-w-2xl"
    @update:open="(v) => !v && (editing = null)"
  >
    <div v-if="editing" class="flex max-h-[70vh] flex-col gap-4 overflow-y-auto pr-1">
      <div class="grid grid-cols-2 gap-4">
        <div class="flex flex-col gap-1.5">
          <Label for="cid">Feed id</Label>
          <Input id="cid" v-model="editing.id" :disabled="!isNew" placeholder="bvg" @blur="autofill" />
          <p class="text-xs text-muted-foreground">Used in filenames and URLs. Cannot change later.</p>
        </div>
        <div class="flex flex-col gap-1.5">
          <Label for="cname">Display name</Label>
          <Input id="cname" v-model="editing.name" placeholder="BVG" />
        </div>
      </div>

      <div class="flex flex-col gap-1.5">
        <Label for="cgtfs">GTFS zips</Label>
        <Input id="cgtfs" v-model="editing.gtfs" placeholder="data/gtfs/bvg.zip" />
        <p class="text-xs text-muted-foreground">
          Comma list. The first is primary — service scenarios are derived from it; later zips overlay.
        </p>
      </div>

      <div class="grid grid-cols-2 gap-4">
        <div class="flex flex-col gap-1.5">
          <Label for="crail">Rail extract</Label>
          <Input id="crail" v-model="editing.rail" placeholder="build/bvg-rail.geojson" />
        </div>
        <div class="flex flex-col gap-1.5">
          <Label for="cstreets">Street extract</Label>
          <Input id="cstreets" v-model="editing.streets" placeholder="build/bvg-streets.geojson" />
          <p class="text-xs text-muted-foreground">Optional — enables bus routes.</p>
        </div>
      </div>

      <div class="flex flex-col gap-1.5">
        <Label for="cbbox">Bounding box</Label>
        <Input id="cbbox" v-model="bboxText" placeholder="13.10, 52.35, 13.65, 52.65" />
        <p v-if="bboxError" class="text-xs text-destructive">{{ bboxError }}</p>
        <p v-else class="text-xs text-muted-foreground">
          west, south, east, north. Sets the Overpass window and clips pattern shapes to the feed's area.
        </p>
      </div>

      <div class="grid grid-cols-2 gap-4">
        <div class="flex flex-col gap-1.5">
          <Label for="cout">Build output</Label>
          <Input id="cout" v-model="editing.out" placeholder="build/bvg.geojson" />
        </div>
        <div class="flex flex-col gap-1.5">
          <Label for="cnet">Drawn network</Label>
          <Input id="cnet" v-model="editing.network" placeholder="sketches/network-bvg.json" />
          <p class="text-xs text-muted-foreground">Ground truth for scoring. Optional.</p>
        </div>
      </div>
    </div>

    <div class="flex justify-end gap-2">
      <Button variant="ghost" @click="editing = null">Cancel</Button>
      <Button :disabled="!canSave || saving" @click="save">
        <Loader2 v-if="saving" class="size-4 animate-spin" />
        {{ isNew ? 'Add feed' : 'Save' }}
      </Button>
    </div>
  </Dialog>

  <Dialog
    :open="!!confirmDelete"
    title="Remove feed?"
    :description="`${confirmDelete?.name || confirmDelete?.id} will be removed from portolan.json. GTFS zips, extracts and builds on disk are left alone.`"
    @update:open="(v) => !v && (confirmDelete = null)"
  >
    <div class="flex justify-end gap-2">
      <Button variant="ghost" @click="confirmDelete = null">Cancel</Button>
      <Button variant="destructive" @click="doDelete"><Trash2 class="size-4" /> Remove</Button>
    </div>
  </Dialog>
</template>
