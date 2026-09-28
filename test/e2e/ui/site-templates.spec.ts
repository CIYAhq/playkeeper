import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
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
  // A card the script draws lists what the template installs.
  const installs = await page.evaluate(() => (window as unknown as { playkeeperTemplates: { templates: { id: string; addons: string[] }[] } }).playkeeperTemplates.templates.find((t) => t.id === 'towny')!.addons.slice(0, 3))
  await expect(page.locator('[data-dir-grid] .dcard').first().locator('.dcard-installs li > span:last-child')).toHaveText(installs)
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

test('a crossplay template is marked on its card, and Bedrock or GeyserMC finds it', async ({ page }) => {
  // Which templates have crossplay comes from their checks; the test picks
  // two itself, Towny and the most popular other, in the index the page loads.
  const taxonomy = path.join(path.dirname(fileURLToPath(import.meta.url)), '../../../site/data/taxonomy.json')
  const crossplay = (JSON.parse(readFileSync(taxonomy, 'utf8')) as { tags: Record<string, { name: string; search: string }> }).tags.crossplay
  let marked: string[] = []
  let others: string[] = []
  // A template's own words, which find it too.
  const own = new Map<string, string>()
  await page.route(/\/assets\/js\/templates-index\.[^/]+\.js$/, async (route) => {
    const body = await (await route.fetch()).text()
    const data = JSON.parse(body.replace(/^window\.playkeeperTemplates = /, '').replace(/;\s*$/, '')) as {
      tags: Record<string, string>
      tagSearch: Record<string, string>
      templates: { id: string; name: string; desc: string; addons: string[]; tags: string[] }[]
    }
    const picked = new Set(['towny', data.templates.find((t) => t.id !== 'towny')!.id])
    for (const t of data.templates) {
      t.tags = t.tags.filter((g) => g !== 'crossplay')
      if (picked.has(t.id)) t.tags.push('crossplay')
      own.set(t.name, [t.name, t.desc, ...t.addons].join(' ').toLowerCase())
    }
    data.tags.crossplay = crossplay.name
    data.tagSearch.crossplay = crossplay.search
    marked = data.templates.filter((t) => picked.has(t.id)).map((t) => t.name)
    others = data.templates.filter((t) => !picked.has(t.id)).map((t) => t.name)
    await route.fulfill({ contentType: 'text/javascript', body: `window.playkeeperTemplates = ${JSON.stringify(data)};` })
  })
  await page.goto('/templates', { waitUntil: 'networkidle' })
  // Each word finds the same cards, so the test waits for the address, which
  // changes once the cards for the word are drawn.
  for (const word of ['crossplay', 'bedrock', 'geyser', 'GeyserMC']) {
    await page.locator('#dir-q').fill(word)
    await expect(page).toHaveURL(new RegExp(`/templates\\?q=${word}$`))
    const want = [...marked, ...others.filter((n) => own.get(n)!.includes(word.toLowerCase()))].sort()
    expect((await names(page)).sort(), word).toEqual(want)
  }
  for (const name of marked) {
    const card = page.locator(`[data-dir-grid] .dcard:has(.dcard-name a:text-is("${name}"))`)
    await expect(card.locator('.dcard-badge')).toHaveText('Crossplay')
    await expect(card.locator('.dcard-badge')).toBeVisible()
    await expect(card.locator('.dcard-tags li', { hasText: 'Crossplay' })).toHaveCount(0)
  }
  await page.locator('#dir-q').fill(others[0])
  await expect.poll(() => names(page)).toContain(others[0])
  await expect(page.locator(`[data-dir-grid] .dcard:has(.dcard-name a:text-is("${others[0]}")) .dcard-badge`)).toHaveCount(0)

  // The mark lets a click through to the card's link, to the template's page.
  await page.locator('#dir-q').fill('towny')
  await expect(page).toHaveURL(/\/templates\?q=towny$/)
  await page.locator('[data-dir-grid] .dcard:has(.dcard-name a:text-is("Towny")) .dcard-badge').click({ force: true })
  await expect(page).toHaveURL(/\/templates\/towny\/towny$/)
})

const DASHBOARD = 'https://siya.playkeeper.me:8443'
/** The template /t/<id> sends the browser on with, from its refresh. */
const templateOf = async (page: Page, id: string) => /url=\/t#([A-Za-z0-9_-]+)/.exec(await (await page.request.get(`/t/${id}`)).text())![1]

test('Open in my dashboard asks once where the dashboard is, then opens templates there in one click', async ({ page, context }) => {
  // The dashboard is a page this test serves at its address.
  await context.route(`${DASHBOARD}/**`, (route) => route.fulfill({ contentType: 'text/html', body: '<title>Dashboard</title>' }))
  await page.goto('/templates', { waitUntil: 'networkidle' })
  const first = page.locator('[data-dir-grid] .dcard').first()
  const name = (await first.locator('.dcard-name').textContent())!.trim()
  const open = first.locator('.dcard-open')
  await expect(open).toHaveAttribute('href', /^\/t#[A-D][A-Za-z0-9_-]+$/)
  const template = (await open.getAttribute('href'))!.slice(3)
  await expect(page.locator('[data-dashboard-line]').first()).toBeHidden()

  // The first time, it asks. Without a dashboard, the share page says what
  // the template sets up and how to get one.
  await open.click()
  const dialog = page.getByRole('dialog', { name: 'Where’s your dashboard?' })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByRole('textbox', { name: 'Your dashboard' })).toBeFocused()
  await dialog.getByRole('link', { name: 'See what the template sets up' }).click()
  await expect(page).toHaveURL(new RegExp(`/t#${template}$`))
  await expect(page.locator('#t-name')).toHaveText(name)

  // A name is enough; something that isn't an address is refused.
  await page.goto('/templates', { waitUntil: 'networkidle' })
  await open.click()
  await dialog.getByRole('textbox', { name: 'Your dashboard' }).fill('http://203.0.113.7')
  await dialog.getByRole('button', { name: 'Open' }).click()
  await expect(dialog.getByRole('status')).toContainText('like alex')
  await dialog.getByRole('textbox', { name: 'Your dashboard' }).fill('siya')
  await page.keyboard.press('Enter')
  await page.waitForURL(`${DASHBOARD}/servers/new#template=${template}`)

  // Remembered: every card opens there at once, a copied or middle-clicked
  // link too, and the page says where.
  await page.goto('/templates', { waitUntil: 'networkidle' })
  await expect(open).toHaveAttribute('href', `${DASHBOARD}/servers/new#template=${template}`)
  await expect(page.locator('.dir-toolbar [data-dashboard-line]')).toHaveText(/^Opens in siya\.playkeeper\.meChange/)
  await open.click()
  await page.waitForURL(`${DASHBOARD}/servers/new#template=${template}`)

  // A card the script draws links /t/<id>; it opens there too.
  await page.goto('/templates?q=towny', { waitUntil: 'networkidle' })
  const drawn = page.locator('[data-dir-grid] .dcard-open').first()
  await expect(drawn).toHaveAttribute('href', '/t/towny')
  await drawn.click()
  await page.waitForURL(`${DASHBOARD}/servers/new#template=${await templateOf(page, 'towny')}`)

  // Change, then Forget: templates ask again.
  await page.goto('/templates/towny/towny', { waitUntil: 'networkidle' })
  await expect(page.locator('.tpage-intro [data-dashboard-line]')).toContainText('siya.playkeeper.me')
  await page.locator('.tpage-intro [data-dashboard-line]').getByRole('button', { name: 'Change your dashboard' }).click()
  const change = page.getByRole('dialog', { name: 'Your dashboard' })
  await expect(change.getByRole('textbox', { name: 'Your dashboard' })).toHaveValue('siya.playkeeper.me')
  await change.getByRole('textbox', { name: 'Your dashboard' }).fill('203.0.113.7')
  await change.getByRole('button', { name: 'Save' }).click()
  await expect(page.locator('.tpage-actions .btn-primary')).toHaveAttribute('href', /^https:\/\/203\.0\.113\.7:8443\/servers\/new#template=[A-D]/)
  await page.locator('.tpage-intro [data-dashboard-line]').getByRole('button', { name: 'Change your dashboard' }).click()
  await change.getByRole('button', { name: 'Forget it' }).click()
  await expect(page.locator('.tpage-intro [data-dashboard-line]')).toBeHidden()
  await expect(page.locator('.tpage-actions .btn-primary')).toHaveAttribute('href', /^\/t#[A-D]/)
  expect(await page.evaluate(() => localStorage.getItem('playkeeper.dashboard'))).toBeNull()
})

test('the share page opens a template in the dashboard this browser knows in one click, or in another typed short', async ({ page, context }) => {
  await context.addInitScript((d) => localStorage.setItem('playkeeper.dashboard', d), DASHBOARD)
  for (const d of [DASHBOARD, 'https://203.0.113.7:8443']) await context.route(`${d}/**`, (route) => route.fulfill({ contentType: 'text/html', body: '<title>Dashboard</title>' }))
  const template = await templateOf(page, 'towny')
  await page.goto(`/t#${template}`, { waitUntil: 'networkidle' })
  await expect(page.locator('#t-name')).toHaveText('Towny')
  await expect(page.getByRole('textbox', { name: 'Your dashboard' })).toBeHidden()
  await page.getByRole('button', { name: 'Open in siya.playkeeper.me' }).click()
  await page.waitForURL(`${DASHBOARD}/servers/new#template=${template}`)

  await page.goto(`/t#${template}`, { waitUntil: 'networkidle' })
  await page.getByRole('button', { name: 'Use another dashboard' }).click()
  await expect(page.getByRole('textbox', { name: 'Your dashboard' })).toBeFocused()
  await page.getByRole('textbox', { name: 'Your dashboard' }).fill('203.0.113.7')
  await page.getByRole('button', { name: 'Open', exact: true }).click()
  await page.waitForURL(`https://203.0.113.7:8443/servers/new#template=${template}`)
})

test("the dashboard's Browse templates link leaves its address in this browser, never in the analytics, then shows the directory", async ({ page, context }) => {
  const seen: string[] = []
  await context.exposeFunction('sawAddress', (href: string) => void seen.push(href))
  await context.route('https://analytics-c.ciya.so/oa.js', (route) => route.fulfill({ contentType: 'text/javascript', body: 'sawAddress(location.href); window.oa = { track: function () { sawAddress(location.href) }, flush: function () {} }' }))
  await page.goto(`/t#dashboard=${encodeURIComponent(DASHBOARD)}`)
  await page.waitForURL(/\/templates$/)
  await expect(page.locator('.dir-toolbar [data-dashboard-line]')).toHaveText(/^Opens in siya\.playkeeper\.meChange/)
  expect(await page.evaluate(() => localStorage.getItem('playkeeper.dashboard'))).toBe(DASHBOARD)
  await expect.poll(() => seen.length, 'the analytics loaded on /templates').toBeGreaterThan(0)
  expect(seen.filter((href) => href.includes('siya') || href.includes('#')), 'addresses the analytics saw').toEqual([])

  // Only an https:// address is kept.
  await page.goto(`/t#dashboard=${encodeURIComponent('http://203.0.113.7:8443')}`)
  await page.waitForURL(/\/templates$/)
  expect(await page.evaluate(() => localStorage.getItem('playkeeper.dashboard'))).toBe(DASHBOARD)
})

test('a dashboard is found from its free name, a server address on it, or its address with or without https:// and the port', async ({ page }) => {
  await page.goto('/templates')
  const parse = (typed: string) => page.evaluate((s) => (window as unknown as { playkeeperSite: { dashboard: { parse: (s: string) => string } } }).playkeeperSite.dashboard.parse(s), typed)
  for (const [typed, want] of [
    ['siya', DASHBOARD],
    [' Siya ', DASHBOARD],
    ['siya.playkeeper.me', DASHBOARD],
    ['survival.siya.playkeeper.me', DASHBOARD],
    ['https://siya.playkeeper.me:8443/servers/new', DASHBOARD],
    ['203.0.113.7', 'https://203.0.113.7:8443'],
    ['203.0.113.7:9443', 'https://203.0.113.7:9443'],
    ['panel.example.com', 'https://panel.example.com:8443'],
    ['panel.example.com:443', 'https://panel.example.com'],
    ['https://panel.example.com', 'https://panel.example.com'],
    ['http://203.0.113.7:8443', ''],
    ['', ''],
  ]) {
    expect(await parse(typed), typed).toBe(want)
  }
  // This site is never a dashboard.
  expect(await parse(`https://${new URL(page.url()).host}`)).toBe('')
  // What a field shows for a saved dashboard reads back as the same one.
  for (const origin of [DASHBOARD, 'https://203.0.113.7:8443', 'https://203.0.113.7:9443', 'https://panel.example.com']) {
    const back = await page.evaluate((o) => {
      const d = (window as unknown as { playkeeperSite: { dashboard: { parse: (s: string) => string; typed: (s: string) => string } } }).playkeeperSite.dashboard
      return d.parse(d.typed(o))
    }, origin)
    expect(back, origin).toBe(origin)
  }
})

test("a card's name opens the template's page", async ({ page }) => {
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

test("the share page shows a directory template with its card's picture, and any other with its play style's", async ({ page }) => {
  await page.goto('/templates', { waitUntil: 'networkidle' })
  const card = page.locator('[data-dir-grid] .dcard:has(.dcard-name a:text-is("Lifesteal SMP"))')
  const art = await card.locator('.dcard-art img').getAttribute('src')
  const link = (await card.locator('.dcard-open').getAttribute('href'))!
  await page.goto(link, { waitUntil: 'networkidle' })
  await expect(page.locator('#t-name')).toHaveText('Lifesteal SMP')
  await expect(page.locator('.share-art img:visible')).toHaveCount(1)
  await expect(page.locator('.share-art img:visible')).toHaveAttribute('src', art!)

  // A template shared from someone's own server gets the scene of how it's played.
  const shared = readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), '../../../internal/templates/testdata/share-link.txt'), 'utf8').trim()
  await page.goto(`/t#${shared.split('#')[1]}`, { waitUntil: 'networkidle' })
  await expect(page.locator('#t-name')).toHaveText('Survival with friends')
  await expect(page.locator('.share-art img:visible')).toHaveCount(1)
  await expect(page.locator('.share-art img:visible')).toHaveAttribute('data-art', 'friends')
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
