import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'

// The stats dashboard against the service itself (playwright.stats.config.ts):
// signing in keeps the read token in this browser alone, every request stays
// on the service, and a few reports read as they should, on a desktop and a
// phone.
const token = 'playwright-stats-token-00000000000000'
const key = 'playkeeper-stats-token'

const id = (n: number) => n.toString(16).padStart(32, '0')
const system = (source: string, more: Record<string, string> = {}) => ({ version: '0.4.4', os: 'ubuntu', osVersion: '24.04', arch: 'amd64', source, kind: 'dashboard', ...more })

test.beforeAll(async ({ playwright }, info) => {
  const request = await playwright.request.newContext({ baseURL: info.project.use.baseURL })
  const debian = { os: 'debian', osVersion: '13', arch: 'arm64' }
  const reports: [string, object][] = [
    ['/v1/install', { id: id(1), event: 'started', ...system('playkeeper.io', { channel: 'hn' }) }],
    ['/v1/install', { id: id(1), event: 'succeeded', ...system('playkeeper.io', { channel: 'hn' }) }],
    ['/v1/heartbeat', { id: id(1), ...system('playkeeper.io', { channel: 'hn' }), address: 'free', servers: 2, running: 1 }],
    ['/v1/install', { id: id(2), event: 'started', ...system('github', debian) }],
    ['/v1/install', { id: id(2), event: 'failed', step: 'docker', ...system('github', debian) }],
    ['/v1/install', { id: id(3), event: 'refused', step: 'memory+port', ...system('github') }],
    ['/v1/heartbeat', { id: id(4), ...system('', { osVersion: '22.04' }), address: 'ip', servers: 1, running: 1 }],
    ['/v1/heartbeat', { id: id(5), ...system('tarball', debian), address: 'own', servers: 0, running: 0 }],
  ]
  for (const [path, report] of reports) {
    const res = await request.post(path, { data: report })
    expect(res.status(), `${path} ${JSON.stringify(report)}`).toBe(204)
  }
  await request.dispose()
})

const kept = (page: Page) => page.evaluate((k) => ({ local: localStorage.getItem(k), session: sessionStorage.getItem(k), cookie: document.cookie }), key)

async function signIn(page: Page, secret: string) {
  await page.getByLabel('Read token').fill(secret)
  await page.getByRole('button', { name: 'Sign in' }).click()
}

async function axe(page: Page, where: string) {
  const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
  const bad = result.violations.filter((x) => x.impact === 'serious' || x.impact === 'critical')
  expect.soft(bad.map((x) => `${x.id}: ${x.nodes.map((n) => n.target.join(' ')).join(', ')}`), where).toEqual([])
}

test('signs in with the read token, keeps it in this browser alone and shows the counts', async ({ page, baseURL }) => {
  const requests: { path: string; origin: string; token: boolean }[] = []
  page.on('request', (r) => {
    const url = new URL(r.url())
    requests.push({ path: url.pathname, origin: url.origin, token: r.headers().authorization !== undefined })
  })
  await page.goto('/dashboard')
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
  await axe(page, 'the sign-in')

  await signIn(page, 'not-the-token-not-the-token-not-the-token')
  await expect(page.getByRole('alert')).toContainText('That token didn’t open the counts')
  expect(await kept(page), 'what a wrong token left behind').toEqual({ local: null, session: null, cookie: '' })

  await signIn(page, token)
  await expect(page.locator('#dashboard')).toBeVisible()
  expect(await kept(page), 'where the token is kept').toEqual({ local: token, session: null, cookie: '' })
  await expect(page.getByLabel('Read token')).toHaveValue('')

  await expect(page.locator('#active-1d')).toHaveText('3')
  await expect(page.locator('#active-30d-sub')).toHaveText('3 servers · 2 running')
  await expect(page.locator('#installs-totals')).toHaveText('Last 30 days: 2 started · 1 succeeded · 1 failed · 1 refused')
  await expect(page.locator('#installs-day')).toContainText('2 started · 1 succeeded · 1 failed · 1 refused')
  await expect(page.locator('#installs-day')).toContainText('playkeeper.io command 1 · GitHub get.sh 2')
  await expect(page.locator('#failed-steps li')).toHaveText([/^Installing Docker\s*1$/])
  await expect(page.locator('#refused-checks li')).toHaveText([/^Memory\s*1$/, /^Port in use\s*1$/])
  await expect(page.locator('#domain-legend li')).toHaveText([/^On our domain.* 1$/, /^Off our domain.* 1$/, /^Installed before 0\.4\.4 1$/])
  await expect(page.locator('#by-os li')).toHaveText([/^Debian 13\s*1$/, /^Ubuntu 22\.04\s*1$/, /^Ubuntu 24\.04\s*1$/])
  await expect(page.locator('#by-arch li')).toHaveText([/^x86 \(amd64\)\s*2$/, /^ARM \(arm64\)\s*1$/])
  await expect(page.locator('#by-address li')).toHaveText([/^Free playkeeper\.me name\s*1$/, /^IP address only\s*1$/, /^Own domain\s*1$/])
  await expect(page.locator('#servers-per li')).toHaveText([/^No servers\s*1$/, /^1 server\s*1$/, /^2 servers\s*1$/, /^3 to 5\s*0$/, /^6 to 10\s*0$/, /^11 or more\s*0$/])
  await expect(page.locator('#by-source li')).toHaveText([/^playkeeper\.io command\s*1$/, /^Release tarball\s*1$/, /^Before 0\.4\.4\s*1$/])
  await expect(page.locator('#by-channel li')).toHaveText([/^hn\s*1$/])
  await expect(page.locator('#foot')).toContainText('0 started in 30 days, 0 running in 7 days')
  await axe(page, 'the dashboard')

  await page.getByRole('button', { name: 'Source' }).click()
  await expect(page.locator('#installs-legend li')).toHaveText([/^playkeeper\.io command 1$/, /^GitHub get\.sh 2$/])

  await page.reload()
  await expect(page.locator('#active-1d'), 'still signed in after a reload').toHaveText('3')

  const origin = new URL(baseURL!).origin
  expect(requests.filter((r) => r.origin !== origin), 'requests to anywhere but the service').toEqual([])
  expect([...new Set(requests.filter((r) => r.token).map((r) => r.path))], 'where the token went').toEqual(['/v1/summary'])

  // Signing out while the counts are on their way keeps them from showing.
  await page.route('**/v1/summary', async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 1000))
    await route.continue()
  })
  await page.reload()
  await page.getByRole('button', { name: 'Sign out' }).click()
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
  await page.waitForTimeout(1500)
  await expect(page.getByRole('heading', { name: 'Sign in' }), 'counts that arrived after signing out').toBeVisible()
  await expect(page.locator('#dashboard')).toBeHidden()
  expect(await kept(page), 'what signing out left behind').toEqual({ local: null, session: null, cookie: '' })
})

test('fits a phone, and keeps the token for the tab alone when it isn’t to be remembered', async ({ browser, baseURL }) => {
  const context = await browser.newContext({ baseURL, viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, locale: 'en-GB', timezoneId: 'UTC' })
  const page = await context.newPage()
  await page.goto('/dashboard')
  await page.getByLabel('Remember on this device').uncheck()
  await signIn(page, token)
  await expect(page.locator('#dashboard')).toBeVisible()
  expect(await kept(page), 'where the token is kept').toEqual({ local: null, session: token, cookie: '' })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'nothing wider than the screen').toBe(true)
  for (const w of ['1d', '7d', '30d']) await expect(page.locator(`#active-${w}`)).toBeInViewport()
  await page.getByRole('button', { name: '7 days' }).click()
  await expect(page.locator('#machines-sub')).toContainText('3 machines ran in the last 7 days')
  const today = page.locator('#installs-chart .col').last()
  await expect(today).toHaveAttribute('aria-pressed', 'true')
  await page.locator('#installs-chart .col').first().tap()
  await expect(page.locator('#installs-day')).toContainText('0 started')
  await axe(page, 'the dashboard on a phone')
  await context.close()
})
