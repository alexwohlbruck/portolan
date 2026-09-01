<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Menu, Compass } from 'lucide-vue-next'
import AppSidebar from './components/AppSidebar.vue'
import Toaster from './components/ui/Toaster.vue'
import { api } from './lib/api'
import { feed, refreshFeeds, startRunPoll, stopRunPoll } from './lib/store'
import { toast } from './lib/toast'

// ── .metro drag-and-drop ─────────────────────────────────────────────
// Drop a Subway Builder save anywhere on the console and it becomes a
// feed: the server hands it to the game repo's importer (the game's own
// exportFeed adapter), registers it, and we chart it right away. The
// listeners live on window so every view is a target — including the map.
const dragDepth = ref(0)
const importing = ref(false)
const router = useRouter()

const hasFiles = (e: DragEvent) => Array.from(e.dataTransfer?.types ?? []).includes('Files')
const onDragEnter = (e: DragEvent) => {
  if (hasFiles(e)) {
    e.preventDefault()
    dragDepth.value++
  }
}
// continuous preventDefault is what makes the drop event fire at all
const onDragOver = (e: DragEvent) => hasFiles(e) && e.preventDefault()
const onDragLeave = (e: DragEvent) => {
  if (hasFiles(e)) dragDepth.value = Math.max(0, dragDepth.value - 1)
}
const onDrop = async (e: DragEvent) => {
  if (!hasFiles(e)) return
  e.preventDefault()
  dragDepth.value = 0
  const file = Array.from(e.dataTransfer?.files ?? []).find((f) =>
    f.name.toLowerCase().endsWith('.metro'),
  )
  if (!file) {
    toast({ title: 'Not a .metro save', description: 'Drop a Subway Builder save file.', variant: 'warning' })
    return
  }
  if (importing.value) return
  importing.value = true
  try {
    const res = await api.importMetro(file)
    await refreshFeeds()
    feed.value = res.key
    try {
      await api.run(res.key, 'chart')
      toast({
        title: `Imported ${res.name}`,
        description: `Charting ${res.features} track features — the map updates when the run finishes.`,
        variant: 'success',
      })
      router.push('/build')
    } catch {
      // 409: another run holds the single job slot — the feed is in, so
      // this is a detour, not a failure
      toast({
        title: `Imported ${res.name}`,
        description: 'A run is already in progress — chart it from the Build page when it finishes.',
        variant: 'warning',
      })
    }
  } catch (err: any) {
    toast({ title: 'Import failed', description: err.message, variant: 'error', duration: 12000 })
  } finally {
    importing.value = false
  }
}

onMounted(() => {
  refreshFeeds()
  startRunPoll()
  window.addEventListener('dragenter', onDragEnter)
  window.addEventListener('dragover', onDragOver)
  window.addEventListener('dragleave', onDragLeave)
  window.addEventListener('drop', onDrop)
})
onUnmounted(() => {
  stopRunPoll()
  window.removeEventListener('dragenter', onDragEnter)
  window.removeEventListener('dragover', onDragOver)
  window.removeEventListener('dragleave', onDragLeave)
  window.removeEventListener('drop', onDrop)
})

// On a phone the sidebar is a drawer; picking a destination is what closes
// it, so navigation itself dismisses — no close button to hunt for.
const sidebarOpen = ref(false)
const route = useRoute()
watch(() => route.fullPath, () => (sidebarOpen.value = false))
</script>

<template>
  <!-- dvh, not vh: mobile browser chrome overlaps 100vh and the bottom of
       the map (and every bottom-anchored overlay) ends up under the URL bar -->
  <div class="flex h-dvh flex-col overflow-hidden md:flex-row">
    <header class="flex shrink-0 items-center gap-3 border-b border-border bg-card/40 px-4 py-2.5 md:hidden">
      <button
        type="button"
        class="flex size-9 items-center justify-center rounded-lg text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
        aria-label="Open navigation"
        @click="sidebarOpen = true"
      >
        <Menu class="size-5" />
      </button>
      <div class="flex items-center gap-2">
        <div class="flex size-7 items-center justify-center rounded-lg bg-primary text-primary-foreground">
          <Compass class="size-4" />
        </div>
        <span class="text-sm font-semibold">Portolan</span>
      </div>
    </header>

    <div
      v-if="sidebarOpen"
      class="fixed inset-0 z-40 bg-black/50 md:hidden"
      @click="sidebarOpen = false"
    />
    <AppSidebar
      :class="[
        'max-md:fixed max-md:inset-y-0 max-md:left-0 max-md:z-50 max-md:bg-card max-md:shadow-xl max-md:transition-transform max-md:duration-200',
        sidebarOpen ? 'max-md:translate-x-0' : 'max-md:-translate-x-full',
      ]"
    />
    <main class="min-h-0 flex-1 overflow-y-auto app-grid-bg"><RouterView /></main>

    <!-- pointer-events-none: the drop must land on window, not the veil -->
    <div
      v-if="dragDepth > 0 || importing"
      class="pointer-events-none fixed inset-0 z-[70] flex items-center justify-center bg-black/60 backdrop-blur-sm"
    >
      <div class="rounded-xl border-2 border-dashed border-primary bg-card px-10 py-8 text-center shadow-2xl">
        <div class="text-lg font-semibold">
          {{ importing ? 'Importing save…' : 'Drop the .metro save' }}
        </div>
        <div class="mt-1 text-sm text-muted-foreground">
          {{ importing ? 'Running the game exporter, then charting' : 'It becomes a feed and charts automatically' }}
        </div>
      </div>
    </div>
  </div>
  <Toaster />
</template>
