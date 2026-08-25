import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwind from '@tailwindcss/vite'
import { fileURLToPath, URL } from 'node:url'

// The console is served by the Go atlas binary under /console/. MapLibre is
// NOT bundled: portolan renders on a FORK (variable line-offset along
// line-progress) that the server exposes at /vendor/maplibre-gl.js, so the
// map view reads it off window rather than from npm.
export default defineConfig({
  base: '/console/',
  plugins: [vue(), tailwind()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: { target: 'es2022', outDir: 'dist', emptyOutDir: true },
  server: {
    port: 5180,
    proxy: {
      '/api': 'http://127.0.0.1:8765',
      '/vendor': 'http://127.0.0.1:8765',
      // The dev server prepends `base` to the absolute URLs in
      // index.html, so the page asks for /console/vendor/maplibre-gl.js
      // while the BUILT page asks for /vendor/maplibre-gl.js. Unproxied,
      // that path falls through to the SPA fallback and answers 200 with
      // index.html — the browser then parses HTML as JavaScript, the
      // global never defines, and every map dies with "MapLibre not
      // loaded". A 404 would have been kinder.
      '/console/vendor': {
        target: 'http://127.0.0.1:8765',
        rewrite: (p) => p.replace(/^\/console/, ''),
      },
    },
  },
})
