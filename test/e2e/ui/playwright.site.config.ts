import { defineConfig } from '@playwright/test'

// playkeeper.io in a browser: site.spec.ts, and the free tools in
// site-tools*.spec.ts. The site is built from the repository and served as
// nginx would, with its Content-Security-Policy (go run ./cmd/site -serve). CI
// runs it in the "Browser checks against a faked panel" job; locally, from
// this directory: npx playwright test -c playwright.site.config.ts
const port = Number(process.env.PK_SITE_PORT ?? 4180)

export default defineConfig({
  testDir: '.',
  testMatch: ['site.spec.ts', 'site-tools*.spec.ts'],
  timeout: 3 * 60_000,
  fullyParallel: true,
  workers: 4,
  reporter: [['list']],
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    locale: 'en-GB',
    timezoneId: 'UTC',
    trace: 'retain-on-failure',
    // The analytics' host and Whop's pixel don't resolve here, so no visit or
    // conversion is counted from a test run.
    launchOptions: { args: ['--host-resolver-rules=MAP analytics-c.ciya.so ~NOTFOUND, MAP t.whop.tw ~NOTFOUND'] },
  },
  webServer: {
    command: `sh -c 'PATH="$PWD/.tools/go/bin:$PATH" exec go run ./cmd/site -serve 127.0.0.1:${port}'`,
    cwd: '../../..',
    url: `http://127.0.0.1:${port}/`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
})
