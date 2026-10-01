import { defineConfig } from '@playwright/test'

// playkeeper.io in a browser: site.spec.ts, the free tools in
// site-tools*.spec.ts and the template directory in site-templates.spec.ts.
// The site is built from the repository and served as
// nginx would, with its Content-Security-Policy (go run ./cmd/site -serve).
// Locally, from this directory: npx playwright test -c
// playwright.site.config.ts. CI runs each project on a runner of its own
// ("playkeeper.io in a browser" in e2e.yml): the checks at phone size, which
// say so in their names, and the rest. Together they take most of a job's 20
// minutes on one runner, and the site gains pages every week.
const port = Number(process.env.PK_SITE_PORT ?? 4180)
const phone = /at phone size/

export default defineConfig({
  testDir: '.',
  testMatch: ['site.spec.ts', 'site-tools*.spec.ts', 'site-templates.spec.ts'],
  timeout: 3 * 60_000,
  fullyParallel: true,
  workers: 4,
  reporter: [['list']],
  projects: [
    { name: 'desktop', grepInvert: phone },
    { name: 'phone', grep: phone },
  ],
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    locale: 'en-GB',
    timezoneId: 'UTC',
    trace: 'retain-on-failure',
    // The analytics' host and the stats service don't resolve here, so no
    // visit or copy is counted from a test run.
    launchOptions: { args: ['--host-resolver-rules=MAP analytics-c.ciya.so ~NOTFOUND, MAP stats.playkeeper.io ~NOTFOUND'] },
  },
  webServer: {
    command: `sh -c 'PATH="$PWD/.tools/go/bin:$PATH" exec go run ./cmd/site -serve 127.0.0.1:${port}'`,
    cwd: '../../..',
    url: `http://127.0.0.1:${port}/`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
})
