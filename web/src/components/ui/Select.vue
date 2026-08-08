<script setup lang="ts">
import { SelectRoot, SelectTrigger, SelectValue, SelectPortal, SelectContent, SelectViewport, SelectItem, SelectItemText, SelectItemIndicator } from 'reka-ui'
import { ChevronDown, Check } from 'lucide-vue-next'
import { cn } from '@/lib/utils'
const props = defineProps<{
  modelValue?: string
  options: { value: string; label: string }[]
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
        <SelectViewport class="p-1 w-[var(--reka-select-trigger-width)]">
          <SelectItem
            v-for="o in props.options"
            :key="o.value"
            :value="o.value"
            class="relative flex w-full cursor-pointer select-none items-center rounded-sm py-1.5 pl-8 pr-2 text-sm outline-none data-[highlighted]:bg-accent data-[highlighted]:text-accent-foreground"
          >
            <span class="absolute left-2 flex size-4 items-center justify-center">
              <SelectItemIndicator><Check class="size-4" /></SelectItemIndicator>
            </span>
            <SelectItemText>{{ o.label }}</SelectItemText>
          </SelectItem>
        </SelectViewport>
      </SelectContent>
    </SelectPortal>
  </SelectRoot>
</template>
