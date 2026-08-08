<script setup lang="ts">
defineProps<{ title: string; subtitle?: string }>()
</script>

<template>
  <header class="sticky top-0 z-20 border-b border-border bg-background/80 px-8 py-5 backdrop-blur">
    <div class="flex items-center justify-between gap-4">
      <div class="min-w-0">
        <h1 class="text-xl font-semibold tracking-tight">{{ title }}</h1>
        <p v-if="subtitle" class="mt-0.5 text-sm text-muted-foreground">{{ subtitle }}</p>
      </div>
      <div class="flex items-center gap-2"><slot name="actions" /></div>
    </div>
  </header>
</template>
