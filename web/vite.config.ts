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
    proxy: { '/api': 'http://127.0.0.1:8765', '/vendor': 'http://127.0.0.1:8765' },
  },
})
