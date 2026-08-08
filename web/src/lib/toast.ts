import { ref } from 'vue'

export type ToastVariant = 'default' | 'success' | 'error' | 'warning'
export interface Toast {
  id: number
  title: string
  description?: string
  variant: ToastVariant
}

export const toasts = ref<Toast[]>([])
let nextId = 1

export function toast(t: { title: string; description?: string; variant?: ToastVariant; duration?: number }) {
  const item: Toast = { id: nextId++, title: t.title, description: t.description, variant: t.variant ?? 'default' }
  toasts.value.push(item)
  window.setTimeout(() => dismissToast(item.id), t.duration ?? 5000)
  return item.id
}

export function dismissToast(id: number) {
  toasts.value = toasts.value.filter((t) => t.id !== id)
}
