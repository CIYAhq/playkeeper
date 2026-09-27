import { expect, test, type Page } from '@playwright/test'
import { dashboard, password, shot } from './helpers'

// The core flows at desktop and phone sizes, on the installed panel the
// steps before set up: sign in through the form, make a server in New server
// and see it start, then back it up from its World tab. Pull requests crawl
// only the pages they touch (clickthrough.spec.ts), so these run on every
// one. Each server made here is deleted after, so the machine is left as
// the other steps expect it.

const sizes = {
  desktop: { viewport: { width: 1440, height: 900 } },
  phone: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true },
} as const

interface Made {
  id: string
  name: string
  slug: string
}

async function servers(page: Page): Promise<Made[]> {
  return (await (await page.request.get('/api/servers')).json()) as Made[]
}

async function deleteServer(page: Page, s: Made) {
  const { csrfToken } = (await (await page.request.get('/api/auth/me')).json()) as { csrfToken: string }
  const res = await page.request.post(`/api/servers/${s.id}/delete`, { data: { confirm: s.name }, headers: { 'X-Requested-With': 'playkeeper', 'X-CSRF-Token': csrfToken } })
  expect(res.ok(), `deleting ${s.name} answered ${res.status()}`).toBe(true)
  await expect.poll(async () => (await servers(page)).some((x) => x.id === s.id), { timeout: 120_000 }).toBe(false)
}

for (const [name, size] of Object.entries(sizes)) {
  test(`sign in, create a server, see it start and back it up on ${name}`, async ({ browser, baseURL }) => {
    test.setTimeout(20 * 60_000)
    const context = await browser.newContext({ ...size, baseURL, ignoreHTTPSErrors: true, locale: 'en-GB', timezoneId: 'UTC' })
    const page = await context.newPage()
    const serverName = `Smoke ${name}`
    let made: Made | undefined
    try {
      await page.goto('/login')
      await page.getByLabel('Username').fill('admin')
      await page.getByLabel('Password', { exact: true }).fill(password)
      await page.getByRole('button', { name: 'Sign in' }).click()
      await expect(dashboard(page)).toBeVisible()

      await page.goto('/servers/new')
      await expect(page.getByRole('heading', { name: 'New server', level: 1 })).toBeVisible()
      const nameField = page.getByLabel('Name', { exact: true })
      for (let step = 0; step < 6 && !(await nameField.isVisible()); step++) {
        await page.getByRole('button', { name: /^Continue( to .+)?$/ }).click()
      }
      await nameField.fill(serverName)
      await page.getByRole('checkbox', { name: /Minecraft End User License Agreement/ }).check()
      await page.getByRole('button', { name: `Create and start ${serverName}` }).click()
      await expect.poll(async () => (made = (await servers(page)).find((s) => s.name === serverName)), { timeout: 60_000 }).toBeTruthy()

      await page.goto('/')
      const card = page.getByRole('article').filter({ hasText: serverName })
      await expect(card.getByText('Online', { exact: true })).toBeVisible({ timeout: 15 * 60_000 })
      await shot(page, `smoke-online-${name}`)

      // The World tab shows the job itself, so no toast says it finished: the backup's row does.
      await page.goto(`/servers/${made?.slug}/world`)
      await page.getByRole('button', { name: 'Make my first backup' }).click()
      await expect(page.getByRole('region', { name: 'Backups' }).getByRole('link', { name: 'Download' }).first()).toBeVisible({ timeout: 10 * 60_000 })
      await expect
        .poll(async () => ((await (await page.request.get(`/api/servers/${made?.id}/backups`)).json()) as { verified?: boolean }[]).some((b) => b.verified), { timeout: 120_000 })
        .toBe(true)
      await shot(page, `smoke-backup-${name}`)
    } finally {
      if (made) await deleteServer(page, made)
      await context.close()
    }
  })
}
