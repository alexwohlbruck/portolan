// Module-singleton state, barrelman-style: plain refs, no Pinia.
import { ref, computed, watch } from 'vue'
import { api, type Feed, type RunStatus } from './api'

export const feeds = ref<Feed[]>([])
export const feedsLoading = ref(false)

// The world context. Not a feed id: no entry in portolan.json may claim it,
// and every per-feed fetch has to check for it before putting it in a URL.
export const GLOBAL = 'global'

const LAST_FEED = 'portolan.feed'
export const feed = ref<string>(localStorage.getItem(LAST_FEED) ?? '')
watch(feed, (v) => v && localStorage.setItem(LAST_FEED, v))

export const isGlobal = computed(() => feed.value === GLOBAL)

// undefined in the global context — every consumer already handles the
// "no feed yet" shape, so global reuses it rather than inventing a fake feed
export const currentFeed = computed(() => feeds.value.find((c) => c.id === feed.value))

export async function refreshFeeds() {
  feedsLoading.value = true
  try {
    feeds.value = await api.feeds()
    // 'global' is a valid persisted selection even though no feed carries it.
    // Any other unknown persisted id (a retired bundle like '5' or
    // 'northeast') falls back to the first feed.
    if (feed.value !== GLOBAL && !feeds.value.some((c) => c.id === feed.value)) {
      feed.value = feeds.value[0]?.id ?? ''
    }
  } finally {
    feedsLoading.value = false
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
      const r = await api.runStatus()
      // a server that has never run a job reports log: null (a Go nil
      // slice) — normalize here so no consumer needs the guard
      r.log = r.log ?? []
      run.value = r
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
