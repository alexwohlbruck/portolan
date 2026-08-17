<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { Crosshair, Trash2, Plus, Loader2, MapPin } from 'lucide-vue-next'
import PageHeader from '@/components/PageHeader.vue'
import Button from '@/components/ui/Button.vue'
import Badge from '@/components/ui/Badge.vue'
import Card from '@/components/ui/Card.vue'
import CardContent from '@/components/ui/CardContent.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Dialog from '@/components/ui/Dialog.vue'
import Spinner from '@/components/ui/Spinner.vue'
import { api, type Area } from '@/lib/api'
import { feeds, feed, isGlobal } from '@/lib/store'
import { toast } from '@/lib/toast'

const areas = ref<Area[]>([])
const loading = ref(true)
const saving = ref(false)
const adding = ref(false)
const draft = ref<Area>({ id: '', label: '', note: '' })
const query = ref('')

async function load() {
  loading.value = true
  try {
    areas.value = await api.areas()
  } catch (e: any) {
    toast({ title: 'Could not load problem areas', description: e.message, variant: 'error' })
  } finally {
    loading.value = false
  }
}
onMounted(load)

const shown = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return areas.value
  return areas.value.filter((a) =>
    [a.id, a.label, a.note, a.feed, a.city].some((s) => String(s ?? '').toLowerCase().includes(q)),
  )
})

const feedName = (a: Area) => {
  const id = String(a.feed ?? a.city ?? '')
  return feeds.value.find((c) => c.id === id)?.name ?? id
}

async function persist(next: Area[]) {
  saving.value = true
  try {
    await api.saveAreas(next)
    areas.value = next
    return true
  } catch (e: any) {
    toast({ title: 'Could not save', description: e.message, variant: 'error' })
    return false
  } finally {
    saving.value = false
  }
}

async function add() {
  const d = draft.value
  if (!d.id.trim()) return
  // 'global' is a context, not a feed — an area added there carries no feed
  const next = [...areas.value, { ...d, feed: d.feed || (isGlobal.value ? '' : feed.value) }]
  if (await persist(next)) {
    toast({ title: 'Area added', description: d.id, variant: 'success' })
    adding.value = false
    draft.value = { id: '', label: '', note: '' }
  }
}

async function remove(a: Area) {
  if (await persist(areas.value.filter((x) => x !== a))) {
    toast({ title: `Removed ${a.id}`, variant: 'success' })
  }
}
</script>

<template>
  <PageHeader title="Problem areas" subtitle="Bookmarked spots to review after a change">
    <template #actions>
      <Button @click="adding = true"><Plus class="size-4" /> Add area</Button>
    </template>
  </PageHeader>

  <div class="space-y-4 p-8">
    <Input v-model="query" placeholder="Search areas" class="max-w-sm" />

    <div v-if="loading" class="flex justify-center py-16"><Spinner class="size-6" /></div>

    <div v-else-if="!shown.length" class="rounded-xl border border-dashed border-border py-16 text-center">
      <div class="mx-auto flex size-12 items-center justify-center rounded-xl bg-muted text-muted-foreground">
        <Crosshair class="size-6" />
      </div>
      <p class="mt-3 font-medium">No problem areas</p>
      <p class="mt-1 text-sm text-muted-foreground">
        Bookmark a spot when something looks wrong, then re-check it after every change.
      </p>
    </div>

    <div v-else class="grid gap-2">
      <Card v-for="(a, i) in shown" :key="i">
        <CardContent class="flex items-center gap-4 py-3">
          <div class="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground">
            <MapPin class="size-4" />
          </div>
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <span class="truncate font-medium">{{ a.label || a.id }}</span>
              <Badge v-if="feedName(a)" variant="muted" class="text-[10px]">{{ feedName(a) }}</Badge>
            </div>
            <div class="truncate text-xs text-muted-foreground">{{ a.note || a.id }}</div>
          </div>
          <a
            v-if="a.c"
            class="shrink-0 text-xs text-muted-foreground underline-offset-4 hover:underline"
            :href="`/console/map`"
          >open</a>
          <Button variant="ghost" size="icon" :disabled="saving" @click="remove(a)"><Trash2 class="size-4" /></Button>
        </CardContent>
      </Card>
    </div>
  </div>

  <Dialog :open="adding" title="Add problem area" description="A short id and a note about what looks wrong."
          @update:open="(v) => (adding = v)">
    <div class="flex flex-col gap-4">
      <div class="flex flex-col gap-1.5">
        <Label for="aid">Id</Label>
        <Input id="aid" v-model="draft.id" placeholder="clt_wtf" class="font-mono" />
      </div>
      <div class="flex flex-col gap-1.5">
        <Label for="alabel">Label</Label>
        <Input id="alabel" v-model="draft.label" placeholder="N Tryon sawtooth" />
      </div>
      <div class="flex flex-col gap-1.5">
        <Label for="anote">Note</Label>
        <Input id="anote" v-model="draft.note" placeholder="Freight parallel makes the tram median zigzag" />
      </div>
    </div>
    <div class="flex justify-end gap-2">
      <Button variant="ghost" @click="adding = false">Cancel</Button>
      <Button :disabled="!draft.id.trim() || saving" @click="add">
        <Loader2 v-if="saving" class="size-4 animate-spin" /> Add
      </Button>
    </div>
  </Dialog>
</template>
