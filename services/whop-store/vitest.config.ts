import { defineConfig } from 'vitest/config'

// The tests run the Worker's code in Node, without the Cloudflare plugin
// vite.config.ts builds it with: it uses only the web's own fetch and HTML.
export default defineConfig({
  test: { include: ['test/**/*.test.ts'] },
})
