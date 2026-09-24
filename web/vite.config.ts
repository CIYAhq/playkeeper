import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// `npm run dev` proxies the API to a local `playkeeper dev` panel (self-signed).
export default defineConfig({
  plugins: [react()],
  build: { outDir: 'dist', emptyOutDir: true, sourcemap: false, target: 'es2022' },
  server: {
    port: 5173,
    proxy: { '/api': { target: 'https://localhost:8443', secure: false, changeOrigin: false } },
  },
  test: { environment: 'node', include: ['src/**/*.test.ts'] },
})
