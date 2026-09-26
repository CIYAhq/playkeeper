import { defineConfig } from '@playwright/test'

// Specs that fake the panel's API in the browser (fake-panel.ts), so they need
// only the built UI from `make web`: no agent, Docker or Minecraft server.
// `vite preview` serves web/dist. CI runs them in the "Browser checks against a
// faked panel" job; locally, after `make web`, run from this directory:
// npx playwright test -c playwright.fake-panel.config.ts
const port = Number(process.env.PK_UI_PORT ?? 4173)

export default defineConfig({
  testDir: '.',
  testMatch: ['console-scroll.spec.ts', 'restore-upload.spec.ts'],
  timeout: 3 * 60_000,
  workers: 1,
  reporter: [['list']],
  use: { baseURL: `http://127.0.0.1:${port}`, locale: 'en-GB', timezoneId: 'UTC', trace: 'retain-on-failure' },
  webServer: {
    command: `npx vite preview --host 127.0.0.1 --port ${port} --strictPort`,
    cwd: '../../../web',
    url: `http://127.0.0.1:${port}/`,
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
  },
})
