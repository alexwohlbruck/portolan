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
export const basemapTiles = () => (isDark.value ? BASEMAP_TILES.dark : BASEMAP_TILES.light)
