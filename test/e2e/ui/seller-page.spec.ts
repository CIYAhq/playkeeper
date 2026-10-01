import AxeBuilder from '@axe-core/playwright'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'

// A seller's page inside Whop, at /whop/seller/<business>, with its calls to
// /api/public/whop/seller faked as the panel answers them. Each step at
// desktop and phone sizes: one heading, which has the focus, nothing wider
// than the screen, and no serious accessibility violations. With PK_SHOTS
// set to a folder, it saves a full-page screenshot of each.

const sizes = {
  desktop: { viewport: { width: 1440, height: 900 } },
  phone: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true },
} as const

const store = 'biz_joeshosting'
const base = `/api/public/whop/seller/${store}`

interface Plan {
  id: string
  title: string
  servers: number
  memoryMB: number
  price: number
  floor: number
  suggested: number
  monthly: boolean
  trial: boolean
  product: string
}

const plan = (id: string, title: string, servers: number, gb: number, price: number, more: Partial<Plan> = {}): Plan => ({
  id,
  title,
  servers,
  memoryMB: gb * 1024,
  price,
  floor: gb * 300,
  suggested: gb * 375,
  monthly: true,
  trial: false,
  product: `prod_${id}`,
  ...more,
})

/** The store as the fake panel holds it: its plans, whether it's open, and what it earned. */
interface Store {
  plans: Plan[]
  open: boolean
  approved: boolean
  earnings: { month: string; currency: string; sales: number; share: number; kept: number }[]
  customers: { handle: string; plan: string; status: 'active' | 'starting' | 'paused' | 'suspended' | 'ended' }[]
}

const scenes = {
  // A copy fresh from the gallery: its plans priced under the floor.
  prices: () => ({ plans: [plan('starter', 'Starter', 1, 4, 1000), plan('plus', 'Plus', 2, 8, 2000)], open: false, approved: true, earnings: [], customers: [] }),
  // One plan renews yearly, the other has a free trial.
  fix: () => ({
    plans: [plan('starter', 'Starter', 1, 4, 1500, { monthly: false }), plan('plus', 'Plus', 2, 8, 3000, { trial: true })],
    open: false,
    approved: true,
    earnings: [],
    customers: [],
  }),
  // Both plans on one product, which only the seller can change in Whop.
  shared: () => ({
    plans: [plan('starter', 'Starter', 1, 4, 1500, { product: 'prod_mc' }), plan('plus', 'Plus', 2, 8, 3000, { product: 'prod_mc' })],
    open: false,
    approved: true,
    earnings: [],
    customers: [],
  }),
  // A store that's been open a while.
  earning: () => ({
    plans: [plan('starter', 'Starter', 1, 4, 1500), plan('plus', 'Plus', 2, 8, 3000)],
    open: true,
    approved: true,
    earnings: [
      { month: '2026-09', currency: 'usd', sales: 10500, share: 5950, kept: 4550 },
      { month: '2026-08', currency: 'usd', sales: 4500, share: 2550, kept: 1950 },
    ],
    customers: [
      { handle: 'mara_k', plan: 'Plus', status: 'active' },
      { handle: 'tobi2009', plan: 'Starter', status: 'active' },
      { handle: 'JunoFox', plan: 'Starter', status: 'starting' },
      { handle: 'pixelpaws', plan: 'Starter', status: 'paused' },
    ],
  }),
  // A business that hasn't approved the app.
  approve: () => ({ plans: [], open: false, approved: false, earnings: [], customers: [] }),
} satisfies Record<string, () => Store>

/** The seller's prices as the panel answers them (sellerPricesFrom). */
function pricesOf(s: Store) {
  const byProduct = new Map<string, Plan[]>()
  for (const p of s.plans) byProduct.set(p.product, [...(byProduct.get(p.product) ?? []), p])
  const blocked = [...byProduct.values()].filter((ps) => ps.length > 1).map((ps) => `${ps.map((p) => p.title).join(' and ')} are on the same product. Give each its own product in Whop.`)
  return {
    plans: s.plans.map((p) => ({ id: p.id, title: p.title, servers: p.servers, memoryMB: p.memoryMB, price: p.price, currency: 'usd', floor: p.floor, share: (p.memoryMB / 4096) * 850, suggested: p.suggested, settable: p.monthly })),
    canOpen: !s.open,
    canUpdate: s.open,
    fixable: s.plans.some((p) => !p.monthly || p.trial),
    ...(blocked.length > 0 && { blocked }),
  }
}

/** Serves the built UI with the seller's calls faked against s, and says which calls nothing answers. */
async function serve(page: Page, s: Store) {
  const unexpected: string[] = []
  const calls: string[] = []
  await page.route('**/api/**', async (route) => {
    const req = route.request()
    const path = new URL(req.url()).pathname
    calls.push(`${req.method()} ${path}`)
    if (req.method() === 'POST' && path === `${base}/open`) {
      if (!s.approved) {
        return route.fulfill({
          status: 409,
          json: { error: 'This business hasn’t approved everything Playkeeper Cloud asks for.', code: 'whop_not_approved', params: { installUrl: 'https://whop.com/apps/app_6oyNYgGluUMTx4/install' } },
        })
      }
      return route.fulfill({ json: { store: { id: store, title: 'Joe’s Hosting', route: 'joes-hosting' }, new: false } })
    }
    if (req.method() === 'GET' && path === `${base}/prices`) return route.fulfill({ json: pricesOf(s) })
    if (req.method() === 'POST' && path === `${base}/prices`) {
      const body = req.postDataJSON() as { plan: string; price: string }
      const p = s.plans.find((x) => x.id === body.plan)
      if (p) p.price = Math.round(Number(body.price) * 100)
      return route.fulfill({ json: pricesOf(s) })
    }
    if (req.method() === 'POST' && path === `${base}/fix`) {
      const changed = s.plans
        .filter((p) => !p.monthly || p.trial)
        .map((p) => `${p.title} now ${[!p.monthly && 'renews every month', p.trial && 'has no free trial'].filter(Boolean).join(' and ')}.`)
        .join(' ')
      for (const p of s.plans) {
        p.monthly = true
        p.trial = false
      }
      return route.fulfill({ json: { changed, prices: pricesOf(s) } })
    }
    if (req.method() === 'POST' && path === `${base}/sell`) {
      s.open = true
      return route.fulfill({ json: { open: true } })
    }
    if (req.method() === 'GET' && path === base) {
      return route.fulfill({
        json: { store: { id: store, title: 'Joe’s Hosting', state: s.open ? 'selling' : 'closed', why: s.open ? undefined : 'Not open yet' }, plans: [], customers: s.customers, earnings: s.earnings },
      })
    }
    unexpected.push(`${req.method()} ${path}`)
    return route.fulfill({ status: 404, contentType: 'text/plain', body: '404 page not found\n' })
  })
  return { unexpected, calls }
}

async function axe(page: Page, where: string) {
  const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
  const bad = result.violations.filter((x) => x.impact === 'serious' || x.impact === 'critical')
  expect.soft(bad.map((x) => `${x.id}: ${x.nodes.map((n) => n.target.join(' ')).join(', ')}`), where).toEqual([])
}

const shots = process.env.PK_SHOTS

/** Checks the step on screen: its one heading has the focus, it fits the screen, and it passes axe; then shoots it. */
async function check(page: Page, heading: string, where: string, name: string) {
  const h1 = page.getByRole('heading', { level: 1 })
  await expect(h1).toHaveText(heading)
  await expect(h1).toHaveCount(1)
  await expect(h1).toBeFocused()
  const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect.soft(wide, `${where} is wider than the screen by ${wide}px`).toBeLessThanOrEqual(0)
  await axe(page, where)
  if (shots) {
    mkdirSync(shots, { recursive: true })
    await page.screenshot({ path: join(shots, `${name}.png`), fullPage: true })
  }
}

for (const [size, device] of Object.entries(sizes)) {
  test(`a new seller checks their prices, opens their store and goes live, at ${size} size`, async ({ browser, baseURL }) => {
    const ctx = await browser.newContext({ baseURL, ...device })
    const page = await ctx.newPage()
    const errors: string[] = []
    page.on('pageerror', (e) => errors.push(e.message))
    const { unexpected, calls } = await serve(page, scenes.prices())
    await page.goto(`/whop/seller/${store}`)
    await check(page, 'Check your prices', `prices at ${size}`, `3-check-your-prices-${size}`)
    await expect(page.getByLabel('Starter')).toHaveValue('15.00')
    await expect(page.getByLabel('Plus')).toHaveValue('30.00')
    await page.getByRole('button', { name: 'Looks good' }).click()
    await check(page, 'Open your store', `open at ${size}`, `4-open-the-store-${size}`)
    await page.getByRole('button', { name: 'Open the store' }).click()
    await check(page, 'You’re live', `live at ${size}`, `5-youre-live-${size}`)
    await expect(page.getByRole('link', { name: /Visit your store/ })).toHaveAttribute('href', 'https://whop.com/joes-hosting')
    expect(calls.filter((c) => c.startsWith('POST'))).toEqual([`POST ${base}/open`, `POST ${base}/prices`, `POST ${base}/prices`, `POST ${base}/sell`])
    expect(unexpected, 'calls nothing answered').toEqual([])
    expect(errors, 'errors on the page').toEqual([])
    await ctx.close()
  })

  test(`Fix my plans puts right a plan’s renewal and free trial, then says what it changed, at ${size} size`, async ({ browser, baseURL }) => {
    const ctx = await browser.newContext({ baseURL, ...device })
    const page = await ctx.newPage()
    const { unexpected } = await serve(page, scenes.fix())
    await page.goto(`/whop/seller/${store}`)
    await check(page, 'Check your prices', `fix at ${size}`, `3a-fix-my-plans-${size}`)
    await expect(page.getByRole('textbox')).toHaveCount(0)
    await page.getByRole('button', { name: 'Fix my plans' }).click()
    await expect(page.getByRole('status')).toHaveText('Done. Starter now renews every month. Plus now has no free trial.')
    await check(page, 'Check your prices', `fixed at ${size}`, `3b-plans-fixed-${size}`)
    await expect(page.getByRole('button', { name: 'Looks good' })).toBeVisible()
    expect(unexpected, 'calls nothing answered').toEqual([])
    await ctx.close()
  })

  test(`plans sharing a product say plainly what to change in Whop, at ${size} size`, async ({ browser, baseURL }) => {
    const ctx = await browser.newContext({ baseURL, ...device })
    const page = await ctx.newPage()
    await serve(page, scenes.shared())
    await page.goto(`/whop/seller/${store}`)
    await check(page, 'Check your prices', `shared at ${size}`, `3c-change-in-whop-${size}`)
    await expect(page.getByText('Starter and Plus are on the same product. Give each its own product in Whop.')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Check again' })).toBeVisible()
    await ctx.close()
  })

  test(`an open store shows what it earned and its customers, and changes its prices, at ${size} size`, async ({ browser, baseURL }) => {
    const ctx = await browser.newContext({ baseURL, ...device })
    const page = await ctx.newPage()
    await serve(page, scenes.earning())
    await page.goto(`/whop/seller/${store}`)
    await check(page, 'You’re live', `earning at ${size}`, `6-earnings-and-customers-${size}`)
    await expect(page.getByText('September 2026')).toBeVisible()
    await expect(page.getByText('4 customers')).toBeVisible()
    await page.getByRole('button', { name: 'Change prices' }).click()
    await check(page, 'Your prices', `change prices at ${size}`, `8-change-prices-${size}`)
    await page.getByRole('button', { name: 'Back' }).click()
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('You’re live')
    await ctx.close()
  })

  test(`a business that hasn’t approved the app is sent to approve it, at ${size} size`, async ({ browser, baseURL }) => {
    const ctx = await browser.newContext({ baseURL, ...device })
    const page = await ctx.newPage()
    await serve(page, scenes.approve())
    await page.goto(`/whop/seller/${store}`)
    await check(page, 'Approve Playkeeper Cloud', `approve at ${size}`, `7-approve-the-app-${size}`)
    await expect(page.getByRole('link', { name: /Approve in Whop/ })).toHaveAttribute('href', 'https://whop.com/apps/app_6oyNYgGluUMTx4/install')
    await ctx.close()
  })
}
