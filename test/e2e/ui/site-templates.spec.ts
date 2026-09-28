import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'

// The template directory on playkeeper.io/templates, in a browser with the
// site's Content-Security-Policy: search, filters and sorting over every
// template, kept in the address; the phone's filter sheet; a card opening
// its template; and the pages without JavaScript. playwright.site.config.ts
// builds and serves the site from the repository's templates.

const cards = (page: Page) => page.locator('[data-dir-grid] .dcard-name')
const names = async (page: Page) => (await cards(page).allTextContents()).map((s) => s.trim())
/** The names the index lists for a game mode, the most popular first: what the page should show. */
const inMode = (page: Page, mode: string) =>
  page.evaluate((m) => (window as unknown as { playkeeperTemplates: { templates: { name: string; cats: string[] }[] } }).playkeeperTemplates.templates.filter((t) => t.cats.includes(m)).map((t) => t.name).slice(0, 24), mode)

test('the directory searches, filters and sorts every template, and keeps what is chosen in the address', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.goto('/templates', { waitUntil: 'networkidle' })
  const all = await cards(page).count()
  expect(all, 'templates on /templates').toBeGreaterThan(5)
  await expect(page.locator('[data-dir-count]')).toHaveText(`${all} templates`)

  // A word searches names, descriptions, add-ons, game modes and tags, a
  // name that starts with it first.
  await page.locator('#dir-q').fill('towny')
  await expect.poll(async () => (await names(page))[0]).toBe('Towny')
  await expect(page).toHaveURL(/\/templates\?q=towny$/)
  await expect(page.locator('[data-sort-label]')).toHaveText('Best match')
  await page.locator('#dir-q').fill('luckperms')
  await expect.poll(() => names(page)).toContain('Towny')
  expect((await names(page)).length).toBeLessThan(all)
  // Best match chosen by hand goes back to Popular with the search.
  await page.locator('[data-sort-btn]').click()
  await page.locator('.dir-sort-opt[data-value="relevance"]').click()
  await expect(page).toHaveURL(/\/templates\?q=luckperms$/)
  await page.locator('.dir-chip', { hasText: 'luckperms' }).click()
  await expect(cards(page)).toHaveCount(all)
  await expect(page.locator('#dir-q')).toHaveValue('')
  await expect(page).toHaveURL(/\/templates$/)
  await expect(page.locator('[data-sort-label]')).toHaveText('Popular')

  // Game modes are any of those chosen, the most popular first, and a
  // reload keeps them.
  const smp = page.locator('.facet[data-facet="mode"] .facet-opt:has(input[value="smp"])')
  await smp.click()
  const popular = await inMode(page, 'smp')
  expect(popular.length, 'SMP templates').toBeGreaterThan(2)
  await expect.poll(() => names(page)).toEqual(popular)
  await expect(page).toHaveURL(/\/templates\?mode=smp$/)
  await page.reload({ waitUntil: 'networkidle' })
  await expect.poll(() => names(page)).toEqual(popular)
  await expect(smp.locator('input')).toBeChecked()

  // Sorting: A–Z, from a menu the keyboard can use too.
  await page.locator('[data-sort-btn]').click()
  await expect(page.locator('[data-sort-menu]')).toBeVisible()
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('Enter')
  await expect(page.locator('[data-sort-menu]')).toBeHidden()
  await expect(page.locator('[data-sort-label]')).toHaveText('A–Z')
  const az = [...popular].sort((a, b) => a.localeCompare(b, 'en', { sensitivity: 'base' }))
  expect(az, 'A–Z differs from Popular for this test to see it').not.toEqual(popular)
  await expect.poll(() => names(page)).toEqual(az)
  await expect(page).toHaveURL(/\/templates\?mode=smp&sort=name$/)

  // Features are all of those chosen; the counts say what each would show.
  await page.locator('.dir-chips-clear').click()
  await expect(cards(page)).toHaveCount(all)
  const features = page.locator('.facet[data-facet="tag"]')
  const pvp = features.locator('.facet-opt', { hasText: 'PvP' })
  if (!(await pvp.isVisible())) {
    await features.locator('[data-facet-more]').click()
    await expect(features.locator('[data-facet-more]')).toHaveText('Show fewer')
  }
  const n = Number(await pvp.locator('[data-count]').textContent())
  await pvp.click()
  await expect(cards(page)).toHaveCount(n)
  await page.locator('.facet[data-facet="memory"] .facet-opt').nth(1).click()
  await expect(page.locator('.dir-chip')).toHaveCount(2)

  // Nothing matches: say so, and one click shows everything again.
  await page.locator('#dir-q').fill('no template is called this')
  await expect(page.locator('[data-dir-empty]')).toBeVisible()
  await page.locator('[data-dir-empty] [data-clear]').click()
  await expect(cards(page)).toHaveCount(all)
  expect(errors, 'errors in the page').toEqual([])
})

test('a card opens its template in the share page, and a card the script draws goes there through /t/<id>', async ({ page }) => {
  await page.goto('/templates', { waitUntil: 'networkidle' })
  const first = page.locator('[data-dir-grid] .dcard').first()
  const name = (await first.locator('.dcard-name').textContent())!.trim()
  await expect(first.locator('.dcard-open')).toHaveAttribute('href', /^\/t#[A-D][A-Za-z0-9_-]+$/)
  await first.locator('.dcard-open').click()
  await expect(page).toHaveURL(/\/t#/)
  await expect(page.locator('#t-name')).toHaveText(name)

  await page.goto('/templates?q=towny', { waitUntil: 'networkidle' })
  const drawn = page.locator('[data-dir-grid] .dcard-open').first()
  await expect(drawn).toHaveAttribute('href', '/t/towny')
  await drawn.click()
  await page.waitForURL(/\/t#[A-D]/)
  await expect(page.locator('#t-name')).toHaveText('Towny')

  // The card's name opens the template's page, whose Copy link copies /t/<id>.
  await page.goto('/templates', { waitUntil: 'networkidle' })
  await page.locator('[data-dir-grid] .dcard-link').getByText('Towny', { exact: true }).click()
  await expect(page).toHaveURL(/\/templates\/towny\/towny$/)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Towny')
  await expect(page.locator('.tpage-actions .btn-primary')).toHaveAttribute('href', /^\/t#/)
})

test('on a phone the filters are a sheet from the bottom', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL, viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })
  const page = await ctx.newPage()
  await page.goto('/templates', { waitUntil: 'networkidle' })
  const sheet = page.locator('[data-filters]')
  await expect(sheet).not.toBeInViewport()
  await page.locator('[data-open-filters]').click()
  await expect(sheet).toBeInViewport()
  await sheet.locator('.facet-opt', { hasText: 'Towny' }).first().click()
  await expect(sheet.locator('[data-show-results]')).toHaveText('Show 1 template')
  await sheet.locator('[data-show-results]').click()
  await expect(sheet).not.toBeInViewport()
  await expect.poll(() => names(page)).toEqual(['Towny'])
  await expect(page.locator('[data-filter-n]')).toHaveText('1')
  await page.locator('[data-open-filters]').click()
  await expect(sheet).toBeInViewport()
  await page.keyboard.press('Escape')
  await expect(sheet).not.toBeInViewport()
  const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(wide, 'wider than the screen').toBeLessThanOrEqual(0)
  await ctx.close()
})

test('without JavaScript every template is a link away, and the controls that need it are not shown', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL, javaScriptEnabled: false })
  const page = await ctx.newPage()
  await page.goto('/templates')
  const all = await cards(page).count()
  expect(all).toBeGreaterThan(5)
  await expect(page.locator('#dir-q')).toBeHidden()
  await expect(page.locator('[data-filters]')).toBeHidden()
  await expect(page.locator('[data-sort-btn]')).toBeHidden()
  for (const href of await page.locator('[data-dir-grid] .dcard-link').evaluateAll((as) => as.map((a) => a.getAttribute('href')))) {
    expect(href).toMatch(/^\/templates\/[a-z0-9-]+\/[a-z0-9-]+$/)
  }
  // Without scripts Playwright's clicks never see the page settle, so the
  // link is followed by its address.
  const towny = await page.locator('.dir-mode', { hasText: 'Towny' }).getAttribute('href')
  expect(towny).toBe('/templates/towny')
  await page.goto(towny!)
  await expect(page.locator('.dcard-name')).toHaveText(['Towny'])
  await ctx.close()
})

test('Open in my dashboard counts template_opened, where it was', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL })
  const events: unknown[][] = []
  await ctx.exposeFunction('recordEvent', (...event: unknown[]) => void events.push(event))
  await ctx.route('https://analytics-c.ciya.so/oa.js', (route) => route.fulfill({ contentType: 'text/javascript', body: 'window.oa = { track: function (name, props) { recordEvent(name, props) }, flush: function () {} }' }))
  const page = await ctx.newPage()
  await page.goto('/templates', { waitUntil: 'networkidle' })
  await page.locator('[data-dir-grid] .dcard:has(.dcard-name a:text-is("Towny"))').locator('.dcard-open').click({ button: 'middle' })
  await page.goto('/templates/towny/towny', { waitUntil: 'networkidle' })
  await page.locator('.tpage-actions .btn-primary').click({ button: 'middle' })
  await expect.poll(() => events).toEqual([
    ['template_opened', { template: 'towny', spot: 'card', where: '/templates' }],
    ['template_opened', { template: 'towny', spot: 'page', where: '/templates/towny/towny' }],
  ])
  await ctx.close()
})

// Pages kept out of search engines aren't in the sitemap that site.spec.ts
// visits, so a template's page and /t/<id> are checked here.
for (const size of [{ name: 'desktop', width: 1440, height: 900, mobile: false }, { name: 'phone', width: 390, height: 844, mobile: true }]) {
  test(`a template's page at ${size.name} size: nothing wider than the screen, no serious accessibility violations`, async ({ browser, baseURL }) => {
    const ctx = await browser.newContext({ baseURL, viewport: { width: size.width, height: size.height }, isMobile: size.mobile, hasTouch: size.mobile })
    const page = await ctx.newPage()
    for (const path of ['/templates/lifesteal-smp/lifesteal-smp', '/templates/oneblock/oneblock', '/templates/skyblock', '/templates?mode=smp&sort=new']) {
      await page.goto(path, { waitUntil: 'networkidle' })
      const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
      expect.soft(wide, `${path} is wider than the screen by ${wide}px`).toBeLessThanOrEqual(0)
      const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
      const bad = result.violations.filter((x) => x.impact === 'serious' || x.impact === 'critical')
      expect.soft(bad.map((x) => `${x.id}: ${x.nodes.map((n) => n.target.join(' ')).join(', ')}`), path).toEqual([])
    }
    await ctx.close()
  })
}
