<script setup lang="ts">
import { cn } from '@/lib/utils'
const props = defineProps<{ class?: any; modelValue?: string | number }>()
defineEmits<{ (e: 'update:modelValue', v: string): void }>()
</script>

<template>
  <input
    :value="props.modelValue"
    :class="cn('flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50', props.class)"
    @input="$emit('update:modelValue', ($event.target as HTMLInputElement).value)"
  />
</template>
