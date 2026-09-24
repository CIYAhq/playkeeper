// Screenshot the given routes at desktop and narrow sizes after signing in.
// Usage: PK_URL=... PK_PASSWORD=... [PK_OUT=dir] node shoot.mjs <name> [route...]
// Reuses the browser session saved in $PK_OUT by the Playwright tests, because
// sign-ins are rate limited per address.
import { chromium } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

const [name, ...routes] = process.argv.slice(2)
const dir = process.env.PK_SHOTS ?? path.resolve('out/screenshots')
const sessionFile = path.join(process.env.PK_OUT ?? path.resolve('out'), 'browser-session.json')
fs.mkdirSync(dir, { recursive: true })
const browser = await chromium.launch()
for (const [vp, size] of [['desktop', { width: 1440, height: 900 }], ['narrow', { width: 390, height: 844 }]]) {
  const ctx = await browser.newContext({ viewport: size, ignoreHTTPSErrors: true, locale: 'en-GB', timezoneId: 'UTC' })
  if (process.env.PK_PASSWORD && fs.existsSync(sessionFile)) await ctx.addCookies(JSON.parse(fs.readFileSync(sessionFile, 'utf8')))
  const page = await ctx.newPage()
  await page.goto(`${process.env.PK_URL}/login`)
  const form = page.getByRole('heading', { name: 'Sign in' })
  await Promise.race([form.waitFor({ timeout: 20_000 }), page.locator('.sidebar, .wizard').first().waitFor({ timeout: 20_000 })]).catch(() => {})
  if (process.env.PK_PASSWORD && (await form.isVisible())) {
    await page.getByLabel('Username').fill('admin')
    await page.getByLabel('Password').fill(process.env.PK_PASSWORD)
    await page.getByRole('button', { name: 'Sign in' }).click()
    await page.locator('.sidebar, .wizard').first().waitFor({ timeout: 20_000 })
    fs.mkdirSync(path.dirname(sessionFile), { recursive: true })
    fs.writeFileSync(sessionFile, JSON.stringify(await ctx.cookies()))
  }
  for (const r of routes.length ? routes : ['/']) {
    await page.goto(`${process.env.PK_URL}${r}`)
    await page.waitForTimeout(Number(process.env.PK_WAIT ?? 3500))
    const file = path.join(dir, `${name}${routes.length > 1 ? r.replace(/\//g, '-') : ''}-${vp}.png`)
    await page.screenshot({ path: file, fullPage: true })
    console.log(file)
  }
  await ctx.close()
}
await browser.close()
