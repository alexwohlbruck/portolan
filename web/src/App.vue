<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
import AppSidebar from './components/AppSidebar.vue'
import Toaster from './components/ui/Toaster.vue'
import { refreshCities, startRunPoll, stopRunPoll } from './lib/store'

onMounted(() => {
  refreshCities()
  startRunPoll()
})
onUnmounted(stopRunPoll)
</script>

<template>
  <div class="flex h-screen overflow-hidden">
    <AppSidebar />
    <main class="flex-1 overflow-y-auto app-grid-bg"><RouterView /></main>
  </div>
  <Toaster />
</template>
