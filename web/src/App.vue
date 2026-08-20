<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { Menu, Compass } from 'lucide-vue-next'
import AppSidebar from './components/AppSidebar.vue'
import Toaster from './components/ui/Toaster.vue'
import { refreshFeeds, startRunPoll, stopRunPoll } from './lib/store'

onMounted(() => {
  refreshFeeds()
  startRunPoll()
})
onUnmounted(stopRunPoll)

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
  </div>
  <Toaster />
</template>
