// Light/dark, module-singleton like lib/store.ts. Three states, not two:
// 'system' follows the OS and is the default, so a machine in light mode
// gets a light console without anyone touching a switch — while an
// explicit pick still sticks across reloads.
//
// The class this drives (`.dark` on <html>) is ALSO set by an inline
// script in index.html. That duplication is deliberate: Vue mounts after
// first paint, so deciding the theme here alone flashes the wrong shell
// on every load. Keep the two in step.
import { ref, computed, watch } from 'vue'

export type Theme = 'light' | 'dark' | 'system'

const KEY = 'portolan.theme'
// Query LIGHT, not dark: an OS that reports neither (or a browser with no
// matchMedia support for the query) then falls through to dark, which is
// the console's historical look.
const media = window.matchMedia('(prefers-color-scheme: light)')

const stored = localStorage.getItem(KEY)
export const theme = ref<Theme>(stored === 'light' || stored === 'dark' ? stored : 'system')

const systemLight = ref(media.matches)
media.addEventListener('change', (e) => (systemLight.value = e.matches))

/** What is actually painted right now. Everything theme-aware — including
 *  the map, which has no CSS to inherit — watches this, not `theme`. */
export const isDark = computed(() => (theme.value === 'system' ? !systemLight.value : theme.value === 'dark'))

watch(
  isDark,
  (dark) => {
    const root = document.documentElement
    root.classList.toggle('dark', dark)
    // form controls (the datetime picker, scrollbars, native menus) read
    // this, not the class
    root.style.colorScheme = dark ? 'dark' : 'light'
  },
  { immediate: true },
)

// 'system' is the absence of a preference, so it is stored as the absence
// of a key — a browser whose OS setting later changes then follows it.
watch(theme, (v) => (v === 'system' ? localStorage.removeItem(KEY) : localStorage.setItem(KEY, v)))

export const THEMES: Theme[] = ['light', 'system', 'dark']

// WebGL inherits no CSS, so every map picks its basemap by hand. The URL
// pair is shared; the raster OPACITY is not, because it is what sets how
// far the city recedes and each map wants a different amount (the sketch
// editor traces against a fainter one). It has to differ by theme too:
// these tiles composite straight onto the page — the style carries no
// background layer — so a value that mutes the dark tiles over near-black
// washes the light tiles out to nothing over white.
export const BASEMAP_TILES = {
  dark: 'https://a.basemaps.cartocdn.com/dark_all/{z}/{x}/{y}@2x.png',
  light: 'https://a.basemaps.cartocdn.com/light_all/{z}/{x}/{y}@2x.png',
}

// Providers are the raster stacks a basemap can draw from; a basemap is a
// choice of which of them are visible, in this fixed bottom-to-top order.
// Keeping them as three standing sources rather than one whose URL swaps
// means each keeps its own attribution, and switching is a visibility
// flip with no tile refetch of the layer you came back to.
export type ProviderId = 'carto' | 'osm' | 'orm'

export const BASEMAP_PROVIDERS: Record<ProviderId, {
  light: string
  dark: string
  /** false for a transparent overlay: fading it the way a base is faded
   *  leaves nothing on screen at all. */
  fade: boolean
  attribution: string
}> = {
  carto: {
    dark: BASEMAP_TILES.dark,
    light: BASEMAP_TILES.light,
    fade: true,
    attribution: '© OpenStreetMap © CARTO',
  },
  osm: {
    dark: 'https://tile.openstreetmap.org/{z}/{x}/{y}.png',
    light: 'https://tile.openstreetmap.org/{z}/{x}/{y}.png',
    fade: true,
    attribution: '© OpenStreetMap',
  },
  // OpenRailwayMap ships transparent tiles meant to composite over a base,
  // which is how openrailwaymap.org itself draws them.
  orm: {
    dark: 'https://a.tiles.openrailwaymap.org/standard/{z}/{x}/{y}.png',
    light: 'https://a.tiles.openrailwaymap.org/standard/{z}/{x}/{y}.png',
    fade: false,
    attribution: '© OpenStreetMap © OpenRailwayMap',
  },
}

/** Bottom to top. Every map adds its raster layers in this order. */
export const PROVIDER_ORDER: ProviderId[] = ['carto', 'osm', 'orm']

export type Basemap = { id: string; label: string; show: ProviderId[] }

export const BASEMAPS: Basemap[] = [
  { id: 'carto', label: 'CARTO', show: ['carto'] },
  { id: 'osm', label: 'OpenStreetMap', show: ['osm'] },
  { id: 'orm', label: 'OpenRailwayMap', show: ['carto', 'orm'] },
  { id: 'orm-only', label: 'OpenRailwayMap only', show: ['orm'] },
  // Blank is not a cosmetic choice: it is the only way to judge drawn
  // geometry with nothing underneath to read alignment against.
  { id: 'blank', label: 'Blank', show: [] },
]

const BKEY = 'portolan.basemap'
const storedBasemap = localStorage.getItem(BKEY)
export const basemap = ref<string>(
  BASEMAPS.some((b) => b.id === storedBasemap) ? (storedBasemap as string) : 'carto',
)
watch(basemap, (v) => localStorage.setItem(BKEY, v))

export const currentBasemap = () =>
  BASEMAPS.find((b) => b.id === basemap.value) ?? BASEMAPS[0]

export const providerTiles = (id: ProviderId) =>
  isDark.value ? BASEMAP_PROVIDERS[id].dark : BASEMAP_PROVIDERS[id].light
