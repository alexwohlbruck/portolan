<script setup lang="ts">
import { TabsRoot, TabsList, TabsTrigger } from 'reka-ui'
const props = defineProps<{ modelValue: string; tabs: { value: string; label: string }[] }>()
defineEmits<{ (e: 'update:modelValue', v: string): void }>()
</script>

<template>
  <TabsRoot :model-value="props.modelValue" @update:model-value="$emit('update:modelValue', $event as string)">
    <TabsList class="inline-flex h-9 items-center justify-center rounded-lg bg-muted p-1 text-muted-foreground">
      <TabsTrigger
        v-for="t in props.tabs"
        :key="t.value"
        :value="t.value"
        class="inline-flex items-center justify-center whitespace-nowrap rounded-md px-3 py-1 text-sm font-medium transition-all cursor-pointer focus-visible:outline-none disabled:pointer-events-none disabled:opacity-50 data-[state=active]:bg-background data-[state=active]:text-foreground data-[state=active]:shadow"
      >
        {{ t.label }}
      </TabsTrigger>
    </TabsList>
    <slot />
  </TabsRoot>
</template>
