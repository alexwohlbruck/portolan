<script setup lang="ts">
import { cn } from '@/lib/utils'
const props = defineProps<{ label: string; value: string | number; sub?: string; class?: any }>()
</script>

<template>
  <div :class="cn('rounded-xl border border-border bg-card p-4', props.class)">
    <div class="flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-muted-foreground">
      <slot name="icon" />
      {{ props.label }}
    </div>
    <div class="mt-2 flex items-baseline gap-1.5">
      <span class="text-2xl font-semibold tabular-nums">{{ props.value }}</span>
    </div>
    <div v-if="props.sub" class="mt-1 text-xs text-muted-foreground">{{ props.sub }}</div>
  </div>
</template>
