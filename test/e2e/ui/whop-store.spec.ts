import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'

// The Whop store site, built and run in workerd as Whop hosting runs it,
// against a make-believe Whop (playwright.whop-store.config.ts): what each
// store shows, and every page at the two sizes, with nothing wider than the
// screen and no serious accessibility violations.
// The three stores playwright.whop-store.config.ts runs.
const open = 'http://127.0.0.1:4291'
const fresh = 'http://127.0.0.1:4292'
const down = 'http://127.0.0.1:4293'

const sizes = [
  { name: 'desktop', width: 1440, height: 900, mobile: false },
  { name: 'phone', width: 390, height: 844, mobile: true },
]

async function check(page: Page, where: string) {
  const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect.soft(wide, `${where} is wider than the screen by ${wide}px`).toBeLessThanOrEqual(0)
  const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
  const bad = result.violations.filter((v) => v.impact === 'serious' || v.impact === 'critical')
  expect.soft(bad.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`), where).toEqual([])
}

test('a store taking orders shows its plans with Whop’s checkout, and the dashboard to sign in to', async ({ page }) => {
  await page.goto(`${open}/`)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Your own Minecraft server, and the dashboard to run it')
  await expect(page.getByRole('link', { name: 'Choose Starter' })).toHaveAttribute('href', 'https://whop.com/checkout/plan_starter')
  await expect(page.getByRole('link', { name: 'Choose Plus' })).toHaveAttribute('href', 'https://whop.com/checkout/plan_plus')
  await expect(page.getByRole('link', { name: 'Sign in' })).toHaveAttribute('href', 'https://beta.playkeeper.me/')
  await expect(page.getByText('Owner test')).toHaveCount(0)
  await page.getByText('What happens if I cancel?').click()
  await expect(page.getByText('Renew within 14 days and everything is back as it was.')).toBeVisible()
  await page.getByRole('link', { name: 'Terms' }).click()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Terms of service')
})

test('a fresh copy of the blueprint takes no orders until its Playkeeper is connected, and walks its owner through connecting it', async ({ page }) => {
  await page.goto(`${fresh}/`)
  await expect(page.getByRole('heading', { name: 'Not taking orders yet' })).toBeInViewport()
  await expect(page.locator('a[href^="https://whop.com/checkout/"]')).toHaveCount(0)
  await page.getByRole('link', { name: 'Set up my store' }).click()
  await expect(page).toHaveURL(`${fresh}/setup`)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Set up your store')
  const steps = page.getByRole('listitem').filter({ has: page.getByRole('heading', { level: 2 }) })
  await expect(steps.getByRole('heading', { level: 2 })).toHaveText(['Connect Playkeeper Cloud', 'Open Playkeeper Cloud', 'Open your store'])
  await expect(steps.nth(0).getByRole('link')).toHaveAttribute('href', /^https:\/\/whop\.com\/apps\/app_[A-Za-z0-9]+\/install$/)
  await expect(steps.nth(1).getByRole('link')).toHaveAttribute('href', 'https://whop.com/dashboard/biz_copy')
  await steps.nth(2).getByRole('link', { name: 'See my store' }).click()
  await expect(page).toHaveURL(`${fresh}/`)
})

test('a store taking orders sends its owner from the setup steps to its plans', async ({ page }) => {
  await page.goto(`${open}/setup`)
  await expect(page).toHaveURL(`${open}/`)
  await expect(page.getByRole('link', { name: 'Choose Starter' })).toBeVisible()
})

test('a store whose Whop can’t be reached says so', async ({ page }) => {
  const res = await page.goto(`${down}/`)
  expect(res?.status()).toBe(503)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Back in a minute')
})

for (const size of sizes) {
  test(`every page at ${size.name} size: nothing wider than the screen, no serious accessibility violations`, async ({ browser }) => {
    const ctx = await browser.newContext({ viewport: { width: size.width, height: size.height }, isMobile: size.mobile, hasTouch: size.mobile })
    const page = await ctx.newPage()
    for (const url of [`${open}/`, `${open}/terms`, `${open}/no-such-page`, `${fresh}/`, `${fresh}/setup`, `${down}/`]) {
      await page.goto(url)
      await check(page, `${url} at ${size.name}`)
    }
    await ctx.close()
  })
}
