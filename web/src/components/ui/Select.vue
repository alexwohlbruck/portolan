<script setup lang="ts">
import { SelectRoot, SelectTrigger, SelectValue, SelectPortal, SelectContent, SelectViewport, SelectItem, SelectItemText, SelectItemIndicator } from 'reka-ui'
import { ChevronDown, Check } from 'lucide-vue-next'
import { cn } from '@/lib/utils'
const props = defineProps<{
  modelValue?: string
  /** icon: optional lucide component drawn before the label; pinned: draw a
   *  separator under this option (for entries that sit above the real list) */
  options: { value: string; label: string; icon?: any; pinned?: boolean }[]
  placeholder?: string
  class?: any
}>()
defineEmits<{ (e: 'update:modelValue', v: string): void }>()
</script>

<template>
  <SelectRoot :model-value="props.modelValue" @update:model-value="$emit('update:modelValue', $event as string)">
    <SelectTrigger
      :class="cn('flex h-9 w-full items-center justify-between gap-2 rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm focus:outline-none focus:ring-2 focus:ring-ring disabled:opacity-50 data-[placeholder]:text-muted-foreground', props.class)"
    >
      <SelectValue :placeholder="props.placeholder ?? 'Select…'" />
      <ChevronDown class="size-4 opacity-60" />
    </SelectTrigger>
    <SelectPortal>
      <SelectContent
        position="popper"
        :side-offset="4"
        class="relative z-50 min-w-[8rem] overflow-hidden rounded-md border border-border bg-popover text-popover-foreground shadow-md data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95"
      >
        <!-- cap to the popper's available height so a long feed roster
             scrolls instead of running off screen -->
        <SelectViewport
          class="p-1 w-[var(--reka-select-trigger-width)] overflow-y-auto max-h-[min(70vh,calc(var(--reka-select-content-available-height)-8px))]"
        >
          <SelectItem
            v-for="o in props.options"
            :key="o.value"
            :value="o.value"
            :class="[
              'relative flex w-full cursor-pointer select-none items-center rounded-sm py-1.5 pl-8 pr-2 text-sm outline-none data-[highlighted]:bg-accent data-[highlighted]:text-accent-foreground',
              o.pinned ? 'mb-1 border-b border-border pb-2 font-medium' : '',
            ]"
          >
            <span class="absolute left-2 flex size-4 items-center justify-center">
              <SelectItemIndicator><Check class="size-4" /></SelectItemIndicator>
            </span>
            <component :is="o.icon" v-if="o.icon" class="mr-2 size-4 shrink-0 text-muted-foreground" />
            <SelectItemText>{{ o.label }}</SelectItemText>
          </SelectItem>
        </SelectViewport>
      </SelectContent>
    </SelectPortal>
  </SelectRoot>
</template>
