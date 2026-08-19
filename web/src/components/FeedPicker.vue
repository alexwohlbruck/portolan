<script setup lang="ts">
// The feed picker. A plain <Select> was fine at a dozen cities and stopped
// being fine at 1499: there is no way to reach a feed by name without
// scrolling past a thousand transit agencies, so this one is a combobox
// with a search field.
//
// It also splits the roster by KIND rather than showing one flat list.
// Global is a context, not a feed. Metro areas are group builds — several
// feeds charted as one graph so their routes bundle on shared track — and
// they replace their members in the world map, so they belong above the
// agencies they absorb, not filed alphabetically among them.
import { computed, ref, watch } from 'vue'
import {
  ComboboxRoot, ComboboxAnchor, ComboboxTrigger, ComboboxInput, ComboboxPortal,
  ComboboxContent, ComboboxViewport, ComboboxGroup, ComboboxLabel, ComboboxItem,
  ComboboxItemIndicator, ComboboxEmpty,
} from 'reka-ui'
import { ChevronDown, Check, Search, Globe, Building2 } from 'lucide-vue-next'
import { feeds, feed, GLOBAL } from '@/lib/store'

// Rendering every match is what made the old list janky; past this many
// the answer is a better search term, not a longer list.
const CAP = 200

const open = ref(false)
const query = ref('')
// The input opens EMPTY. Left to itself a combobox seeds its field with
// the selected item's label, which with our own filtering on top means
// opening the picker immediately narrows the list to the one feed you
// already have — the roster looks like it lost 1498 entries.
const blank = () => ''
watch(open, () => { query.value = '' })

const metros = computed(() => feeds.value.filter((f) => f.members?.length))
const agencies = computed(() => feeds.value.filter((f) => !f.members?.length))

const label = (f: { id: string; name?: string }) => f.name || f.id
const hit = (f: { id: string; name?: string }, q: string) =>
  label(f).toLowerCase().includes(q) || f.id.toLowerCase().includes(q)

const q = computed(() => query.value.trim().toLowerCase())
const shownMetros = computed(() =>
  q.value ? metros.value.filter((f) => hit(f, q.value)) : metros.value)
const matched = computed(() =>
  q.value ? agencies.value.filter((f) => hit(f, q.value)) : agencies.value)
const shownAgencies = computed(() => matched.value.slice(0, CAP))
const hidden = computed(() => matched.value.length - shownAgencies.value.length)
const showGlobal = computed(() => !q.value || 'global'.includes(q.value))
const empty = computed(() =>
  !showGlobal.value && !shownMetros.value.length && !shownAgencies.value.length)

const current = computed(() => feeds.value.find((f) => f.id === feed.value))
const currentLabel = computed(() =>
  feed.value === GLOBAL ? 'Global' : current.value ? label(current.value) : 'Pick a feed…')

const itemClass =
  'relative flex w-full cursor-pointer select-none items-center rounded-sm py-1.5 pl-8 pr-2 text-sm outline-none data-[highlighted]:bg-accent data-[highlighted]:text-accent-foreground'
const labelClass =
  'px-2 pb-1 pt-2 text-xs font-medium uppercase tracking-wider text-muted-foreground/70'
</script>

<template>
  <ComboboxRoot
    v-model="feed"
    v-model:open="open"
    ignore-filter
    :reset-search-term-on-blur="false"
  >
    <ComboboxAnchor as-child>
      <ComboboxTrigger
        class="flex h-9 w-full items-center justify-between gap-2 rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-sm focus:outline-none focus:ring-2 focus:ring-ring"
      >
        <span class="truncate" :class="feed ? '' : 'text-muted-foreground'">{{ currentLabel }}</span>
        <ChevronDown class="size-4 shrink-0 opacity-60" />
      </ComboboxTrigger>
    </ComboboxAnchor>

    <ComboboxPortal>
      <ComboboxContent
        position="popper"
        :side-offset="4"
        class="z-50 w-[var(--reka-combobox-trigger-width)] min-w-[13rem] overflow-hidden rounded-md border border-border bg-popover text-popover-foreground shadow-md data-[state=open]:animate-in data-[state=open]:fade-in-0"
      >
        <div class="flex items-center gap-2 border-b border-border px-3">
          <Search class="size-4 shrink-0 opacity-50" />
          <ComboboxInput
            v-model="query"
            :display-value="blank"
            placeholder="Search feeds…"
            class="h-9 w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
          />
        </div>

        <!-- capped to the popper's available height so the roster scrolls
             instead of running off screen -->
        <ComboboxViewport
          class="max-h-[min(60vh,calc(var(--reka-combobox-content-available-height)-3rem))] overflow-y-auto p-1"
        >
          <ComboboxEmpty v-if="empty" class="px-2 py-6 text-center text-sm text-muted-foreground">
            No feed matches “{{ query }}”
          </ComboboxEmpty>

          <ComboboxItem
            v-if="showGlobal"
            :value="GLOBAL"
            :class="[itemClass, 'mb-1 border-b border-border pb-2 font-medium']"
          >
            <span class="absolute left-2 flex size-4 items-center justify-center">
              <ComboboxItemIndicator><Check class="size-4" /></ComboboxItemIndicator>
            </span>
            <Globe class="mr-2 size-4 shrink-0 text-muted-foreground" />
            Global
          </ComboboxItem>

          <ComboboxGroup v-if="shownMetros.length">
            <ComboboxLabel :class="labelClass">Metro areas</ComboboxLabel>
            <ComboboxItem
              v-for="f in shownMetros"
              :key="f.id"
              :value="f.id"
              :class="itemClass"
            >
              <span class="absolute left-2 flex size-4 items-center justify-center">
                <ComboboxItemIndicator><Check class="size-4" /></ComboboxItemIndicator>
              </span>
              <Building2 class="mr-2 size-4 shrink-0 text-muted-foreground" />
              <span class="truncate">{{ label(f) }}</span>
              <span class="ml-auto pl-2 text-xs text-muted-foreground">
                {{ f.members!.length + 1 }} feeds
              </span>
            </ComboboxItem>
          </ComboboxGroup>

          <ComboboxGroup v-if="shownAgencies.length">
            <ComboboxLabel :class="labelClass">Feeds</ComboboxLabel>
            <ComboboxItem
              v-for="f in shownAgencies"
              :key="f.id"
              :value="f.id"
              :class="itemClass"
            >
              <span class="absolute left-2 flex size-4 items-center justify-center">
                <ComboboxItemIndicator><Check class="size-4" /></ComboboxItemIndicator>
              </span>
              <span class="truncate">{{ label(f) }}</span>
            </ComboboxItem>
          </ComboboxGroup>

          <div v-if="hidden > 0" class="px-2 py-2 text-center text-xs text-muted-foreground">
            {{ hidden }} more — keep typing to narrow
          </div>
        </ComboboxViewport>
      </ComboboxContent>
    </ComboboxPortal>
  </ComboboxRoot>
</template>
