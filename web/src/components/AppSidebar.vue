<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { Map, MapPin, Hammer, Clock, Palette, Crosshair, Compass, PenTool, SlidersHorizontal } from 'lucide-vue-next'
import Badge from './ui/Badge.vue'
import Select from './ui/Select.vue'
import Spinner from './ui/Spinner.vue'
import { cities, feed, run } from '@/lib/store'

const route = useRoute()
const nav = [
  { to: '/map', label: 'Map', icon: Map },
  { to: '/build', label: 'Build', icon: Hammer, badge: 'run' },
  { to: '/service', label: 'Service', icon: Clock },
  { to: '/sketch', label: 'Sketch', icon: PenTool },
  { to: '/tuning', label: 'Tuning', icon: SlidersHorizontal },
]
const config = [
  { to: '/cities', label: 'Cities', icon: MapPin },
  { to: '/style', label: 'Style', icon: Palette },
  { to: '/areas', label: 'Problem areas', icon: Crosshair },
]
const isActive = (to: string) => route.path === to || route.path.startsWith(`${to}/`)
const options = computed(() => cities.value.map((c) => ({ value: c.id, label: c.name || c.id })))
</script>

<template>
  <aside class="flex h-full w-60 shrink-0 flex-col border-r border-border bg-card/40">
    <div class="flex items-center gap-2.5 px-5 py-5">
      <div class="flex size-9 items-center justify-center rounded-lg bg-primary text-primary-foreground">
        <Compass class="size-5" />
      </div>
      <div class="leading-tight">
        <div class="text-sm font-semibold">Portolan</div>
        <div class="text-xs text-muted-foreground">Transit cartography</div>
      </div>
    </div>

    <!-- The city is global state, not a per-page control: every view below
         is about one city, and re-picking it on each page was the old
         workbench's most-repeated click. -->
    <div class="px-3 pb-3">
      <Select v-model="feed" :options="options" placeholder="Pick a city…" />
    </div>

    <nav class="flex flex-1 flex-col gap-1 overflow-y-auto px-3 py-2">
      <RouterLink
        v-for="item in nav"
        :key="item.to"
        :to="item.to"
        :class="[
          'group flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-colors',
          isActive(item.to)
            ? 'bg-accent text-accent-foreground'
            : 'text-muted-foreground hover:bg-accent/50 hover:text-foreground',
        ]"
      >
        <component :is="item.icon" class="size-4" />
        <span class="flex-1">{{ item.label }}</span>
        <Badge v-if="item.badge === 'run' && run.running" variant="info" class="px-1.5">
          <Spinner class="size-3" />
        </Badge>
      </RouterLink>

      <div class="mt-4 px-3 pb-1 text-xs font-medium uppercase tracking-wider text-muted-foreground/70">
        Configuration
      </div>
      <RouterLink
        v-for="item in config"
        :key="item.to"
        :to="item.to"
        :class="[
          'group flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-colors',
          isActive(item.to)
            ? 'bg-accent text-accent-foreground'
            : 'text-muted-foreground hover:bg-accent/50 hover:text-foreground',
        ]"
      >
        <component :is="item.icon" class="size-4" />
        <span class="flex-1">{{ item.label }}</span>
      </RouterLink>
    </nav>

    <div class="border-t border-border px-3 py-3">
      <a
        href="/map"
        class="flex w-full items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
      >
        <Compass class="size-4" /> Old workbench
      </a>
    </div>
  </aside>
</template>
