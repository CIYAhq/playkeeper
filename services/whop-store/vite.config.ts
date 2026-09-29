import { cloudflare } from '@cloudflare/vite-plugin'
import { whop } from '@whop/cli/vite'
import { defineConfig } from 'vite'

// Whop hosting serves dist/client as it is and runs dist/server/index.js, a
// Worker; whop() packs both into dist/whop-build.zip for `whop apps deploy`.
export default defineConfig({
  plugins: [cloudflare({ viteEnvironment: { name: 'ssr' } }), whop()],
  environments: {
    ssr: { build: { outDir: 'dist/server' } },
  },
})
