import { defineConfig } from '@playwright/test'

// playkeeper.io in a browser: site.spec.ts. The site is built from the
// repository and served as nginx would (go run ./cmd/site -serve). CI runs it
// in the "Browser checks against a faked panel" job; locally, from this
// directory: npx playwright test -c playwright.site.config.ts
const port = Number(process.env.PK_SITE_PORT ?? 4180)

export default defineConfig({
  testDir: '.',
  testMatch: ['site.spec.ts'],
  timeout: 3 * 60_000,
  workers: 1,
  reporter: [['list']],
  use: { baseURL: `http://127.0.0.1:${port}`, locale: 'en-GB', timezoneId: 'UTC', trace: 'retain-on-failure' },
  webServer: {
    command: `sh -c 'PATH="$PWD/.tools/go/bin:$PATH" exec go run ./cmd/site -serve 127.0.0.1:${port}'`,
    cwd: '../../..',
    url: `http://127.0.0.1:${port}/`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
})
