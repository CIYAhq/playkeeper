import { defineConfig } from '@playwright/test'

// The Whop store site (services/whop-store) as Whop hosting runs it: its
// build in workerd (wrangler dev), reading a make-believe Whop API
// (services/whop-store/test/fake-whop-server.ts). Three stores run side by
// side: Pip Hosting taking orders, a fresh copy of the blueprint, and one
// whose Whop can't be reached. CI builds the store first, in the "Whop store
// site" job; locally, after `npm ci && npm run build` in services/whop-store,
// from this directory: npx playwright test -c playwright.whop-store.config.ts
const fakeWhop = 4290
// whop-store.spec.ts visits these.
const stores = { open: 4291, fresh: 4292, down: 4293 } as const

function store(name: keyof typeof stores, origin: string, account: string) {
  return {
    command: `npx wrangler dev --config dist/server/wrangler.json --ip 127.0.0.1 --port ${stores[name]} --var WHOP_API_ORIGIN:${origin} --var WHOP_ACCOUNT_ID:${account} --show-interactive-dev-session=false`,
    cwd: '../../../services/whop-store',
    url: `http://127.0.0.1:${stores[name]}/robots.txt`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  }
}

export default defineConfig({
  testDir: '.',
  testMatch: ['whop-store.spec.ts'],
  timeout: 60_000,
  workers: 2,
  reporter: [['list']],
  use: {
    locale: 'en-GB',
    timezoneId: 'UTC',
    trace: 'retain-on-failure',
    // Whop's pixel doesn't resolve here, so no visit is counted from a test run.
    launchOptions: { args: ['--host-resolver-rules=MAP t.whop.tw ~NOTFOUND'] },
  },
  webServer: [
    {
      command: `node test/fake-whop-server.ts ${fakeWhop}`,
      cwd: '../../../services/whop-store',
      url: `http://127.0.0.1:${fakeWhop}/healthz`,
      reuseExistingServer: !process.env.CI,
      timeout: 30_000,
    },
    store('open', `http://127.0.0.1:${fakeWhop}/open`, 'biz_pip'),
    store('fresh', `http://127.0.0.1:${fakeWhop}/fresh`, 'biz_copy'),
    // Nothing listens on port 9, so this store never reaches Whop.
    store('down', 'http://127.0.0.1:9', 'biz_pip'),
  ],
})
