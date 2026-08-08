<script setup lang="ts">
import { Info, CheckCircle2, XCircle, AlertTriangle, X } from 'lucide-vue-next'
import { toasts, dismissToast, type ToastVariant } from '@/lib/toast'

const icons: Record<ToastVariant, any> = { default: Info, success: CheckCircle2, error: XCircle, warning: AlertTriangle }
const accent: Record<ToastVariant, string> = {
  default: 'text-info',
  success: 'text-[var(--success)]',
  error: 'text-destructive',
  warning: 'text-[var(--warning)]',
}
</script>

<template>
  <div class="pointer-events-none fixed bottom-4 right-4 z-[100] flex w-full max-w-sm flex-col gap-2">
    <TransitionGroup
      enter-active-class="transition duration-200 ease-out"
      enter-from-class="translate-x-4 opacity-0"
      enter-to-class="translate-x-0 opacity-100"
      leave-active-class="transition duration-150 ease-in absolute"
      leave-from-class="opacity-100"
      leave-to-class="opacity-0 translate-x-4"
    >
      <div
        v-for="t in toasts"
        :key="t.id"
        class="pointer-events-auto flex items-start gap-3 rounded-lg border border-border bg-card p-3.5 shadow-lg"
      >
        <component :is="icons[t.variant]" :class="['mt-0.5 size-5 shrink-0', accent[t.variant]]" />
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium">{{ t.title }}</p>
          <p v-if="t.description" class="mt-0.5 break-words text-xs text-muted-foreground">{{ t.description }}</p>
        </div>
        <button class="text-muted-foreground hover:text-foreground" @click="dismissToast(t.id)"><X class="size-4" /></button>
      </div>
    </TransitionGroup>
  </div>
</template>
