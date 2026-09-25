import { fileURLToPath } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

// `npm run dev` proxies the API to a local `playkeeper dev` panel (self-signed).
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  build: { outDir: 'dist', emptyOutDir: true, sourcemap: false, target: 'es2022', assetsInlineLimit: 0 },
  server: {
    port: 5173,
    proxy: { '/api': { target: 'https://localhost:8443', secure: false, changeOrigin: false } },
  },
  test: { environment: 'node', include: ['src/**/*.test.ts', 'src/**/*.test.tsx'] },
})
