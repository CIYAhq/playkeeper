import { fileURLToPath } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'
import { demoBuild, noDemo } from './src/demo/vite.ts'

// `npm run dev` proxies the API to a local `playkeeper dev` panel (self-signed).
// `--mode demo` makes the live demo instead: the dashboard with sample data
// answered in the browser, served from /demo/ (see src/demo).
export default defineConfig(({ mode }) => {
  const demo = mode === 'demo'
  return {
    base: demo ? '/demo/' : '/',
    plugins: [react(), tailwindcss(), demo ? demoBuild() : noDemo()],
    resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
    // Game addresses in the demo name a made-up server, not the site serving it.
    define: demo ? { 'window.location.hostname': JSON.stringify('demo.playkeeper.io') } : {},
    build: { outDir: demo ? 'dist-demo' : 'dist', emptyOutDir: true, sourcemap: false, target: 'es2022', assetsInlineLimit: 0 },
    server: {
      port: 5173,
      proxy: demo ? undefined : { '/api': { target: 'https://localhost:8443', secure: false, changeOrigin: false } },
    },
    test: { environment: 'node', include: ['src/**/*.test.ts', 'src/**/*.test.tsx'] },
  }
})
