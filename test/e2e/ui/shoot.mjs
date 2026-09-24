// Screenshot the given routes at desktop and narrow sizes after signing in.
// Usage: PK_URL=... PK_PASSWORD=... node shoot.mjs <name> [route...]
import { chromium } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

const [name, ...routes] = process.argv.slice(2)
const dir = process.env.PK_SHOTS ?? path.resolve('out/screenshots')
fs.mkdirSync(dir, { recursive: true })
const browser = await chromium.launch()
for (const [vp, size] of [['desktop', { width: 1440, height: 900 }], ['narrow', { width: 390, height: 844 }]]) {
  const ctx = await browser.newContext({ viewport: size, ignoreHTTPSErrors: true, locale: 'en-GB', timezoneId: 'UTC' })
  const page = await ctx.newPage()
  await page.goto(`${process.env.PK_URL}/login`)
  if (process.env.PK_PASSWORD && (await page.getByLabel('Username').count())) {
    await page.getByLabel('Username').fill('admin')
    await page.getByLabel('Password').fill(process.env.PK_PASSWORD)
    await page.getByRole('button', { name: 'Sign in' }).click()
    await page.waitForTimeout(1500)
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
