import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'
import { demoBuild, noDemo } from './src/demo/vite.ts'
import { firstLoadBudget } from './src/lib/first-load.ts'

// `npm run dev` proxies the API to a local `playkeeper dev` panel (self-signed).
// `--mode demo` makes the live demo instead: the dashboard with sample data
// answered in the browser, served from /demo/ (see src/demo).

// The panel takes changes only from an HTTPS page on its own host, so the dev
// server serves HTTPS with the certificate of `make dev`'s panel once it exists.
// `vite preview`, which the fake-panel browser checks use, stays on HTTP.
function devCertificate() {
  const dir = fileURLToPath(new URL('../.dev/data/panel/tls/', import.meta.url))
  const [cert, key] = [`${dir}cert.pem`, `${dir}key.pem`]
  return existsSync(cert) && existsSync(key) ? { cert: readFileSync(cert), key: readFileSync(key) } : undefined
}

export default defineConfig(({ command, mode, isPreview }) => {
  const demo = mode === 'demo'
  return {
    base: demo ? '/demo/' : '/',
    plugins: [react(), tailwindcss(), ...(demo ? [demoBuild()] : [noDemo(), firstLoadBudget()])],
    resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
    // Game addresses in the demo name a made-up server, not the site serving it.
    // Only in the build: the dev server's own client reads the same name.
    define: demo && command === 'build' ? { 'window.location.hostname': JSON.stringify('demo.playkeeper.io') } : {},
    build: { outDir: demo ? 'dist-demo' : 'dist', emptyOutDir: true, sourcemap: false, target: 'es2022', assetsInlineLimit: 0 },
    server: {
      port: 5173,
      https: command === 'serve' && !isPreview && !demo ? devCertificate() : undefined,
      proxy: demo ? undefined : { '/api': { target: 'https://localhost:8443', secure: false, changeOrigin: false } },
    },
    test: { environment: 'node', include: ['src/**/*.test.ts', 'src/**/*.test.tsx'] },
  }
})
