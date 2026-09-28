import { defineConfig } from '@playwright/test'

// The stats service's dashboard (internal/usage/service/dashboard) in a
// browser: stats-dashboard.spec.ts, against the service itself, started on
// an empty database that the spec sends a few reports to. CI runs it in the
// "Browser checks against a faked panel" job; locally, from this directory:
// npx playwright test -c playwright.stats.config.ts
const port = Number(process.env.PK_STATS_PORT ?? 4190)

export default defineConfig({
  testDir: '.',
  testMatch: ['stats-dashboard.spec.ts'],
  timeout: 2 * 60_000,
  workers: 1,
  reporter: [['list']],
  use: { baseURL: `http://127.0.0.1:${port}`, locale: 'en-GB', timezoneId: 'UTC', trace: 'retain-on-failure' },
  webServer: {
    // The same read token is in stats-dashboard.spec.ts.
    command: `sh -c 'd=$(mktemp -d) && PATH="$PWD/.tools/go/bin:$PATH" STATS_DATA_DIR="$d" STATS_LISTEN=127.0.0.1:${port} STATS_READ_TOKEN=playwright-stats-token-00000000000000 exec go run ./cmd/playkeeper-stats serve'`,
    cwd: '../../..',
    url: `http://127.0.0.1:${port}/healthz`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
})
