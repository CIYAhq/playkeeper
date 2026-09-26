import { defineConfig } from '@playwright/test'

// The panel uses a certificate generated on the host; the harness verifies its
// fingerprint separately (curl/python with the pinned certificate), so the
// test browser accepts it without a trust store entry.
export default defineConfig({
  testDir: '.',
  timeout: 45 * 60_000,
  workers: 1,
  reporter: [['list']],
  use: { baseURL: process.env.PK_URL, ignoreHTTPSErrors: true, locale: 'en-GB', timezoneId: 'UTC', actionTimeout: 30_000, navigationTimeout: 60_000 },
})
