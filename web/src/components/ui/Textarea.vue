<script setup lang="ts">
import { cn } from '@/lib/utils'
const props = withDefaults(defineProps<{ class?: any; modelValue?: string; rows?: number }>(), { rows: 4 })
defineEmits<{ (e: 'update:modelValue', v: string): void }>()
</script>

<template>
  <textarea
    :rows="props.rows"
    :value="props.modelValue"
    :class="cn('flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm font-mono shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50 resize-y', props.class)"
    @input="$emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)"
  />
</template>
