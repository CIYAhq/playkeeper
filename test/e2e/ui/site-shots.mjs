// Screenshots of playkeeper.io pages at the designs' sizes: 1440 wide for
// desktop and 390 for phones. Scrolls each page first, so the parts that fade
// in on scroll are shown, with reduced motion, so the landing page shows the
// design's still rather than a moment of its loop. Usage:
//   node site-shots.mjs <base-url> <out-dir> <path>...
// For example: node site-shots.mjs http://127.0.0.1:8080 /tmp/shots / /pricing
import { chromium } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

const [base, out, ...paths] = process.argv.slice(2)
fs.mkdirSync(out, { recursive: true })
const browser = await chromium.launch()
const sizes = [['desktop', { width: 1440, height: 900 }], ['phone', { width: 390, height: 844 }]]
for (const [name, viewport] of sizes) {
  const ctx = await browser.newContext({ viewport, deviceScaleFactor: 1, locale: 'en-GB', timezoneId: 'UTC', hasTouch: name === 'phone', isMobile: name === 'phone', reducedMotion: 'reduce' })
  const page = await ctx.newPage()
  for (const p of paths.length ? paths : ['/']) {
    await page.goto(base + p, { waitUntil: 'networkidle' })
    await page.evaluate(async () => {
      for (let y = 0; y < document.body.scrollHeight; y += 400) {
        window.scrollTo(0, y)
        await new Promise((r) => setTimeout(r, 60))
      }
      window.scrollTo(0, 0)
    })
    await page.waitForTimeout(900)
    const slug = p === '/' ? 'landing' : p.replace(/#.*/, '-template').replace(/^\//, '').replace(/[/?=&]+/g, '-').replace(/-$/, '')
    const file = path.join(out, `${slug}-${name}.png`)
    await page.screenshot({ path: file, fullPage: true })
    console.log(file)
  }
  await ctx.close()
}
await browser.close()
