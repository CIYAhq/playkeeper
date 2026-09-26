import { expect, test, type Page } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { shotsDir } from './helpers'

// The live demo as playkeeper.io serves it at /demo/: the dashboard with its
// sample data, all in the browser. It runs against the site image, so nginx
// and the site's Content-Security-Policy are the real ones:
//   scripts/site-check.sh   (leaves the image as playkeeper-site:check)
//   docker run -d --rm --name playkeeper-demo -p 127.0.0.1:8460:80 playkeeper-site:check
//   npx playwright test demo.spec.ts
// PK_DEMO_URL picks another address, PK_SHOTS the screenshots' folder.
const demoUrl = process.env.PK_DEMO_URL ?? 'http://127.0.0.1:8460/demo/'
const origin = new URL(demoUrl).origin

/** Collects what must not happen: a request to anywhere else, a script error, a console error (where the CSP reports what it blocks). */
function watch(page: Page): string[] {
  const problems: string[] = []
  page.on('request', (r) => {
    if (new URL(r.url()).origin !== origin) problems.push(`request to ${r.url()}`)
  })
  page.on('pageerror', (e) => problems.push(`page error: ${e.message}`))
  page.on('console', (m) => {
    if (m.type() === 'error') problems.push(`console error: ${m.text()}`)
  })
  return problems
}

/** A screenshot once transitions have finished, such as a toast sliding in. */
async function still(page: Page, name: string, { fullPage = false } = {}) {
  fs.mkdirSync(shotsDir, { recursive: true })
  await page.screenshot({ path: path.join(shotsDir, `${name}.png`), fullPage, animations: 'disabled' })
}

test('the live demo: Home, a server’s pages, Settings and a restart, without leaving the page', async ({ page }) => {
  const problems = watch(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(demoUrl)

  const main = page.getByRole('navigation', { name: 'Main' })
  await expect(page.getByRole('heading', { name: 'Home', level: 1 })).toBeVisible()
  await expect(page.getByText('Live demo · resets every hour')).toBeVisible()
  await expect(page.getByRole('link', { name: 'Install on your VPS' })).toHaveAttribute('href', '/#install')
  await expect(page.getByText('Like what you see?')).toBeVisible()
  await expect(page.getByText('curl -fsSL https://playkeeper.io/install')).toBeVisible()
  await expect(main.getByRole('link', { name: /Survival/ })).toBeVisible()
  await expect(main.getByRole('link', { name: /Creative/ })).toBeVisible()
  await expect(page.getByText('JunoFox joined Survival')).toBeVisible()
  await still(page, 'demo-home-desktop')

  await page.getByRole('complementary').getByRole('link', { name: 'Settings', exact: true }).click()
  await expect(page).toHaveURL((url) => url.pathname.startsWith(new URL('settings', demoUrl).pathname))
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  await main.getByRole('link', { name: 'Home' }).click()
  await expect(page).toHaveURL(demoUrl)
  await expect(page.getByRole('heading', { name: 'Home', level: 1 })).toBeVisible()

  await main.getByRole('link', { name: /Survival/ }).click()
  await expect(page).toHaveURL(`${demoUrl}servers/survival`)
  const tabs = page.getByRole('navigation', { name: 'Server pages' })

  await tabs.getByRole('link', { name: 'Console' }).click()
  await expect(page).toHaveURL(`${demoUrl}servers/survival/console`)
  const log = page.getByRole('log', { name: 'Server output' })
  await expect(log).toContainText('Done (')
  const lines = log.locator('div:has(> span)')
  const before = await lines.count()
  await expect.poll(() => lines.count(), { timeout: 30_000, message: 'new console lines keep arriving' }).toBeGreaterThan(before)
  await still(page, 'demo-console-desktop')

  await tabs.getByRole('link', { name: 'Players' }).click()
  await expect(page).toHaveURL(`${demoUrl}servers/survival/players`)
  await expect(page.getByText('JunoFox').first()).toBeVisible()
  const face = page.locator('img[src*="/demo/faces/"]').first()
  await expect(face).toBeVisible()
  await expect.poll(() => face.evaluate((img) => (img as HTMLImageElement).naturalWidth), { message: 'player faces load' }).toBeGreaterThan(0)

  await tabs.getByRole('link', { name: 'World' }).click()
  await expect(page).toHaveURL(`${demoUrl}servers/survival/world`)
  await expect(page.getByText('Before the nether hub').first()).toBeVisible()

  await tabs.getByRole('link', { name: 'Settings' }).click()
  await expect(page).toHaveURL(`${demoUrl}servers/survival/settings`)

  await tabs.getByRole('link', { name: 'Overview' }).click()
  await expect(page).toHaveURL(`${demoUrl}servers/survival`)
  await page.getByRole('button', { name: 'Restart', exact: true }).first().click()
  const toast = page.locator('[data-demo-toast="restart"]')
  await expect(toast).toBeVisible({ timeout: 30_000 })
  await expect(toast).toContainText('That was a demo restart')
  await expect(toast).toContainText('Nothing really restarted.')
  await still(page, 'demo-restart-toast-desktop')

  await page.reload()
  await expect(page.getByText('Live demo · resets every hour')).toBeVisible()
  await expect(tabs.getByRole('link', { name: 'Overview' })).toBeVisible()

  expect(problems).toEqual([])
})

test('the live demo’s machines pages: its one machine, and Connect says what it needs', async ({ page }) => {
  const problems = watch(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`${demoUrl}settings/machines`)
  await expect(page.getByRole('heading', { name: 'Machines' }).first()).toBeVisible()
  await expect(page.getByText('This dashboard runs here')).toBeVisible()
  await expect(page.getByRole('alert').filter({ hasText: 'The demo has just this one machine.' })).toBeVisible()
  await still(page, 'demo-machines-desktop')

  const main = page.getByRole('navigation', { name: 'Main' })
  await main.getByRole('link', { name: /my-vps/ }).click()
  await expect(page).toHaveURL(/\/demo\/machines\/[a-z2-9]{10}$/)
  await expect(page.getByRole('heading', { name: 'my-vps', level: 1 })).toBeVisible()
  const machinePage = page.url()

  await page.goto(machinePage.replace('/demo/machines/', '/demo/settings/machines/'))
  await expect(page).toHaveURL(machinePage)
  await page.goto(`${demoUrl}settings/machines/zzzzzzzzzz`)
  await expect(page.getByText('There’s no machine at this address')).toBeVisible()
  await page.goto(`${demoUrl}servers/new?machine=${machinePage.split('/').pop()}`)
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  await expect(page.getByText('Live demo · resets every hour')).toBeVisible()

  expect(problems).toEqual([])
})

/** Waits until every add-on icon on the page has loaded from the demo's own drawings. */
async function iconsLoaded(page: Page) {
  const icons = page.locator('img[src*="/demo/icons/"]')
  await expect(icons.first()).toBeVisible()
  await expect
    .poll(() => icons.evaluateAll((imgs) => imgs.every((img) => (img as HTMLImageElement).complete && (img as HTMLImageElement).naturalWidth > 0)), { message: 'add-on icons load' })
    .toBe(true)
}

test('the live demo’s plugins, map pre-generation and packs, where a change says it’s a demo', async ({ page }) => {
  const problems = watch(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`${demoUrl}servers/survival`)
  await page.getByRole('navigation', { name: 'Server pages' }).getByRole('link', { name: 'Plugins' }).click()
  await expect(page).toHaveURL(`${demoUrl}servers/survival/plugins`)
  await expect(page.getByRole('heading', { name: 'Plugins on Survival' })).toBeVisible()
  const luckPerms = page.getByRole('listitem').filter({ hasText: 'LuckPerms' })
  await expect(luckPerms).toContainText('Update available · 5.5.11')
  await expect(page.getByRole('listitem').filter({ hasText: 'FriendsWelcome' })).toContainText('Added by hand')
  await iconsLoaded(page)
  await still(page, 'demo-plugins-desktop')

  await luckPerms.getByRole('button', { name: /^LuckPerms 5\.5\.10/ }).click()
  await page.getByRole('dialog', { name: 'LuckPerms' }).getByRole('button', { name: 'Update to 5.5.11' }).click()
  await expect(page.getByText('The demo can’t do this one. On your own VPS it works.')).toBeVisible()
  await page.keyboard.press('Escape')

  await page.goto(`${demoUrl}servers/survival/plugins/browse`)
  await expect(page.getByText('Lets Bedrock players on phones and consoles join your Java server')).toBeVisible()
  await page.goto(`${demoUrl}servers/survival/world/pregen`)
  await expect(page.getByText(/ of 99,225 chunks$/)).toBeVisible()
  await page.goto(`${demoUrl}servers/survival/world/packs`)
  await expect(page.getByText('Cosy Blocks 32x')).toBeVisible()
  await expect(page.getByText('More Mob Heads')).toBeVisible()
  await expect(page.getByText('The demo has no sample data for this.')).toHaveCount(0)
  expect(problems).toEqual([])
})

test('the live demo’s plugins on a phone', async ({ page }) => {
  const problems = watch(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto(`${demoUrl}servers/survival/plugins`)
  await expect(page.getByRole('heading', { name: 'Plugins', level: 1 })).toBeVisible()
  await expect(page.getByText('5.5.10 · Modrinth')).toBeVisible()
  await expect(page.getByText('Added by hand')).toBeVisible()
  await iconsLoaded(page)
  await still(page, 'demo-plugins-phone')
  expect(problems).toEqual([])
})

test('the live demo on a phone: the brand line and the install card', async ({ page }) => {
  const problems = watch(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto(demoUrl)
  await expect(page.getByRole('heading', { name: 'Home', level: 1 })).toBeVisible()
  await expect(page.getByText('Live demo · resets every hour')).toBeVisible()
  await expect(page.getByText('Like what you see?')).toBeVisible()
  await expect(page.getByText('Survival').first()).toBeVisible()
  await expect(page.getByText('JunoFox joined Survival')).toBeVisible()
  await still(page, 'demo-home-phone', { fullPage: true })
  expect(problems).toEqual([])
})
