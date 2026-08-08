import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

const nf = new Intl.NumberFormat()
export const formatNumber = (n: number | null | undefined) =>
  n === null || n === undefined ? '—' : nf.format(n)

export function formatDuration(ms: number | null | undefined) {
  if (ms === null || ms === undefined) return '—'
  if (ms < 1000) return `${Math.round(ms)}ms`
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(1)}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${Math.round(s % 60)}s`
  return `${Math.floor(m / 60)}h ${m % 60}m`
}

export function timeAgo(t: number | string | null | undefined) {
  if (!t) return '—'
  const d = typeof t === 'string' ? Date.parse(t) : t
  const s = Math.max(0, (Date.now() - d) / 1000)
  if (s < 60) return `${Math.round(s)}s ago`
  if (s < 3600) return `${Math.round(s / 60)}m ago`
  if (s < 86400) return `${Math.round(s / 3600)}h ago`
  return `${Math.round(s / 86400)}d ago`
}

export const formatClock = (t: number | string) => {
  const d = new Date(t)
  return d.toTimeString().slice(0, 8)
}

/** km with one decimal, or an em-dash. Distances are the console's most
 *  common number and they always want the same treatment. */
export const formatKm = (m: number | null | undefined) =>
  m === null || m === undefined ? '—' : `${(m / 1000).toFixed(1)} km`
