<script setup lang="ts">
// The empty state a per-feed page shows in the global context. One
// component so every parked page reads identically — and the feed buttons
// set the store feed, which is exactly what the sidebar picker does.
import { Globe } from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import { feeds, feed } from '@/lib/store'

defineProps<{ title: string }>()
</script>

<template>
  <div class="p-8">
    <div class="mx-auto max-w-2xl rounded-xl border border-dashed border-border py-16 text-center">
      <div class="mx-auto flex size-12 items-center justify-center rounded-xl bg-muted text-muted-foreground">
        <Globe class="size-6" />
      </div>
      <p class="mt-3 font-medium">{{ title }} is per-feed</p>
      <p class="mt-1 text-sm text-muted-foreground">Pick a feed:</p>
      <div class="mt-4 flex flex-wrap justify-center gap-2 px-8">
        <Button v-for="c in feeds" :key="c.id" variant="outline" size="sm" @click="feed = c.id">
          {{ c.name || c.id }}
        </Button>
      </div>
    </div>
  </div>
</template>
