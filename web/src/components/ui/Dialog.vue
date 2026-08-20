<script setup lang="ts">
import {
  DialogRoot, DialogPortal, DialogOverlay, DialogContent,
  DialogTitle, DialogDescription, DialogClose,
} from 'reka-ui'
import { X } from 'lucide-vue-next'
import { cn } from '@/lib/utils'

const props = defineProps<{ open?: boolean; title?: string; description?: string; class?: any }>()
defineEmits<{ (e: 'update:open', v: boolean): void }>()
</script>

<template>
  <DialogRoot :open="props.open" @update:open="$emit('update:open', $event)">
    <DialogPortal>
      <DialogOverlay
        class="fixed inset-0 z-50 bg-black/60 backdrop-blur-sm data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0"
      />
      <DialogContent
        :class="cn(
          'fixed left-1/2 top-1/2 z-50 grid w-[calc(100%-2rem)] max-w-lg -translate-x-1/2 -translate-y-1/2 gap-4 border border-border bg-card p-4 shadow-lg rounded-xl sm:p-6',
          'data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95',
          props.class)"
      >
        <div v-if="props.title || props.description" class="pr-6">
          <DialogTitle v-if="props.title" class="text-lg font-semibold leading-none tracking-tight">
            {{ props.title }}
          </DialogTitle>
          <DialogDescription v-if="props.description" class="mt-1.5 text-sm text-muted-foreground">
            {{ props.description }}
          </DialogDescription>
        </div>
        <slot />
        <DialogClose
          class="absolute right-4 top-4 rounded-sm opacity-70 transition-opacity hover:opacity-100 focus:outline-none"
        >
          <X class="size-4" /><span class="sr-only">Close</span>
        </DialogClose>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>
