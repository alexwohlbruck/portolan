// Module-singleton state, barrelman-style: plain refs, no Pinia.
import { ref, computed, watch } from 'vue'
import { api, type City, type RunStatus } from './api'

export const cities = ref<City[]>([])
export const citiesLoading = ref(false)

const LAST_FEED = 'portolan.feed'
export const feed = ref<string>(localStorage.getItem(LAST_FEED) ?? '')
watch(feed, (v) => v && localStorage.setItem(LAST_FEED, v))

export const currentCity = computed(() => cities.value.find((c) => c.id === feed.value))

export async function refreshCities() {
  citiesLoading.value = true
  try {
    cities.value = await api.cities()
    if (!cities.value.some((c) => c.id === feed.value)) feed.value = cities.value[0]?.id ?? ''
  } finally {
    citiesLoading.value = false
  }
}

// ── pipeline run state ────────────────────────────────────────────────
// The server runs one job at a time and exposes a cumulative log. Poll
// fast while it runs, slowly when idle — same shape as barrelman's job
// poller, minus the job queue portolan doesn't have.
export const run = ref<RunStatus>({ running: false, done: false, ok: false, cmd: '', log: [] })
let timer: number | undefined

export function startRunPoll() {
  if (timer) return
  const tick = async () => {
    try {
      run.value = await api.runStatus()
    } catch {
      /* server restarting — keep the last state */
    }
    timer = window.setTimeout(tick, run.value.running ? 700 : 4000)
  }
  tick()
}

export function stopRunPoll() {
  if (timer) window.clearTimeout(timer)
  timer = undefined
}
