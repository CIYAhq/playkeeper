// Screenshots of the Map tab's area setting on a running dev panel.
// Usage: PK_URL=... PK_PASSWORD=... PK_SHOTS=dir node map-area-shots.mjs <slug> <step...>
// Steps: map (the live map), dialog (Map area open), choose (a size picked),
// sheet (the phone's Map settings), start (a size picked and started, on
// desktop only: it changes the server). Each step is taken at desktop and
// phone sizes; the file names end in -desktop and -phone.
import { chromium } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

const [slug, ...steps] = process.argv.slice(2)
const dir = process.env.PK_SHOTS ?? path.resolve('out/screenshots')
const prefix = process.env.PK_PREFIX ?? 'map-area'
const size = process.env.PK_SIZE ?? '2,500 blocks'
const sessionFile = path.join(process.env.PK_OUT ?? path.resolve('out'), 'browser-session.json')
fs.mkdirSync(dir, { recursive: true })

const sizes = {
  desktop: { viewport: { width: 1440, height: 900 } },
  phone: { viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true },
}

async function signIn(ctx, page) {
  if (fs.existsSync(sessionFile)) await ctx.addCookies(JSON.parse(fs.readFileSync(sessionFile, 'utf8')))
  await page.goto(`${process.env.PK_URL}/login`)
  const form = page.getByRole('heading', { name: 'Sign in' })
  const dashboard = page.getByRole('navigation', { name: 'Main' }).or(page.getByRole('navigation', { name: 'Server pages' })).or(page.getByRole('heading', { name: 'Home', level: 1 })).first()
  await Promise.race([form.waitFor({ timeout: 20_000 }), dashboard.waitFor({ timeout: 20_000 })]).catch(() => {})
  if (await form.isVisible()) {
    await page.getByLabel('Username').fill('admin')
    await page.getByLabel('Password', { exact: true }).fill(process.env.PK_PASSWORD ?? '')
    await page.getByRole('button', { name: 'Sign in' }).click()
    await dashboard.waitFor({ timeout: 20_000 })
    fs.mkdirSync(path.dirname(sessionFile), { recursive: true })
    fs.writeFileSync(sessionFile, JSON.stringify(await ctx.cookies()))
  }
}

async function openMap(page) {
  await page.goto(`${process.env.PK_URL}/servers/${slug}/map`)
  await page.waitForFunction(() => ![...document.querySelectorAll('[data-slot=skeleton]')].some((s) => s.checkVisibility()), null, { timeout: 30_000 }).catch(() => {})
  // PK_ZOOM_OUT zooms the map out that many steps, with its keyboard shortcut.
  const out = Number(process.env.PK_ZOOM_OUT ?? 0)
  if (out > 0) {
    await page.getByLabel(/^Map of /).first().focus()
    for (let i = 0; i < out; i++) {
      await page.keyboard.press('-')
      await page.waitForTimeout(400)
    }
    await page.evaluate(() => (document.activeElement instanceof HTMLElement ? document.activeElement.blur() : undefined))
  }
  await page.waitForTimeout(Number(process.env.PK_WAIT ?? 4000))
}

async function openArea(page, phone) {
  if (phone) {
    await page.getByRole('button', { name: 'Map settings' }).click()
    await page.waitForTimeout(700)
    await page.getByRole('button', { name: /^Map area/ }).click()
  } else {
    await page.getByRole('button', { name: 'Map options' }).click()
    await page.getByRole('menuitem', { name: 'Map area…' }).click()
  }
  await page.getByRole('dialog', { name: 'Map area' }).waitFor()
  await page.waitForTimeout(900)
}

const browser = await chromium.launch()
for (const [name, opts] of Object.entries(sizes)) {
  const phone = name === 'phone'
  const ctx = await browser.newContext({ ...opts, ignoreHTTPSErrors: true, locale: 'en-GB', timezoneId: 'UTC' })
  const page = await ctx.newPage()
  await signIn(ctx, page)
  for (const step of steps) {
    await openMap(page)
    if (step === 'sheet') {
      if (!phone) continue
      await page.getByRole('button', { name: 'Map settings' }).click()
      await page.waitForTimeout(900)
    }
    if (step === 'start' && phone) continue
    if (step === 'dialog' || step === 'choose' || step === 'start') await openArea(page, phone)
    if (step === 'choose' || step === 'start') {
      await page.getByRole('dialog', { name: 'Map area' }).locator('label', { hasText: size }).first().click()
      await page.waitForTimeout(900)
    }
    if (step === 'start') {
      await page.getByRole('dialog', { name: 'Map area' }).getByRole('button', { name: 'Start' }).click()
      await page.getByRole('dialog', { name: 'Map area' }).waitFor({ state: 'hidden' })
      await page.waitForTimeout(3000)
    }
    const file = path.join(dir, `${prefix}-${step}-${name}.png`)
    await page.screenshot({ path: file })
    console.log(file)
  }
  await ctx.close()
}
await browser.close()
