import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import fs from 'node:fs'

// Every page of playkeeper.io, from its sitemap, plus the share page, its
// template state and the 404, at the designs' two sizes: nothing wider than
// the screen, and no serious accessibility violations, with and without
// reduced motion. playwright.site.config.ts builds and serves the site.
const sizes = [
  { name: 'desktop', width: 1440, height: 900, mobile: false },
  { name: 'phone', width: 390, height: 844, mobile: true },
]

async function pagesToVisit(page: Page) {
  const xml = await (await page.request.get('/sitemap.xml')).text()
  const paths = [...xml.matchAll(/<loc>https:\/\/playkeeper\.io([^<]*)<\/loc>/g)].map((m) => m[1] || '/')
  expect(paths.length, 'pages in the sitemap').toBeGreaterThan(10)
  const link = fs.readFileSync('../../../internal/templates/testdata/share-link.txt', 'utf8').trim()
  return [...paths, '/t', '/t#' + link.split('#')[1], '/no-such-page']
}

async function axe(page: Page, where: string) {
  const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
  const bad = result.violations.filter((x) => x.impact === 'serious' || x.impact === 'critical')
  expect(bad.map((x) => `${x.id}: ${x.nodes.map((n) => n.target.join(' ')).join(', ')}`), where).toEqual([])
}

for (const size of sizes) {
  for (const motion of ['no-preference', 'reduce'] as const) {
    test(`every page at ${size.name} size (${motion === 'reduce' ? 'reduced motion' : 'with motion'}): nothing wider than the screen, no serious accessibility violations`, async ({ browser }) => {
      const ctx = await browser.newContext({ viewport: { width: size.width, height: size.height }, isMobile: size.mobile, hasTouch: size.mobile, reducedMotion: motion })
      const page = await ctx.newPage()
      const errors: string[] = []
      page.on('pageerror', (e) => errors.push(e.message))
      page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()) })
      for (const path of await pagesToVisit(page)) {
        await page.goto(path, { waitUntil: 'networkidle' })
        // Scroll through, so the parts that fade in are shown.
        await page.evaluate(async () => {
          for (let y = 0; y < document.body.scrollHeight; y += 500) { window.scrollTo(0, y); await new Promise((r) => setTimeout(r, 30)) }
          window.scrollTo(0, 0)
        })
        await page.waitForTimeout(700)
        const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
        expect(wide, `${path} at ${size.name} is wider than the screen by ${wide}px`).toBeLessThanOrEqual(0)
        await axe(page, `${path} at ${size.name}`)
      }
      // The GitHub star count is fetched from api.github.com, which may be
      // unreachable here; nothing else should log an error.
      expect(errors.filter((e) => !/api\.github\.com|Failed to load resource/.test(e)), 'errors in the browser console').toEqual([])
      await ctx.close()
    })
  }
}
