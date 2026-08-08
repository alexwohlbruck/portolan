<script setup lang="ts">
const props = withDefaults(
  defineProps<{ value?: number; indeterminate?: boolean; size?: 'sm' | 'md'; variant?: 'default' | 'destructive' }>(),
  { value: 0, size: 'sm', variant: 'default' },
)
</script>

<template>
  <div class="relative w-full overflow-hidden rounded-full bg-muted" :class="props.size === 'md' ? 'h-2' : 'h-1.5'">
    <div
      v-if="props.indeterminate"
      class="progress-indeterminate absolute inset-y-0 w-1/3 rounded-full"
      :class="props.variant === 'destructive' ? 'bg-destructive' : 'bg-primary'"
    />
    <div
      v-else
      class="h-full rounded-full transition-[width] duration-700 ease-out"
      :class="props.variant === 'destructive' ? 'bg-destructive' : 'bg-primary'"
      :style="{ width: `${Math.max(0, Math.min(100, props.value * 100))}%` }"
    />
  </div>
</template>

<style scoped>
@keyframes progress-slide {
  0% { left: -33%; }
  100% { left: 100%; }
}
.progress-indeterminate { animation: progress-slide 1.2s ease-in-out infinite; }
</style>
