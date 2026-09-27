import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Browser, type Page } from '@playwright/test'
import fs from 'node:fs'

// Every page of playkeeper.io, from its sitemap, plus the share page, its
// template state and the 404, at the designs' two sizes: nothing wider than
// the screen, and no serious accessibility violations, with and without
// reduced motion. Each page is checked even when one before it fails.
// playwright.site.config.ts builds and serves the site.
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
  expect.soft(bad.map((x) => `${x.id}: ${x.nodes.map((n) => n.target.join(' ')).join(', ')}`), where).toEqual([])
}

for (const size of sizes) {
  for (const motion of ['no-preference', 'reduce'] as const) {
    test(`every page at ${size.name} size (${motion === 'reduce' ? 'reduced motion' : 'with motion'}): nothing wider than the screen, no serious accessibility violations`, async ({ browser, baseURL }) => {
      const ctx = await browser.newContext({ baseURL, viewport: { width: size.width, height: size.height }, isMobile: size.mobile, hasTouch: size.mobile, reducedMotion: motion })
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
        expect.soft(wide, `${path} at ${size.name} is wider than the screen by ${wide}px`).toBeLessThanOrEqual(0)
        await axe(page, `${path} at ${size.name}`)
      }
      // The GitHub star count is fetched from api.github.com, which may be
      // unreachable here; nothing else should log an error.
      expect(errors.filter((e) => !/api\.github\.com|Failed to load resource/.test(e)), 'errors in the browser console').toEqual([])
      await ctx.close()
    })
  }
}

test("the modded guide's Copy copies the whole script of the tab that's showing", async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL })
  const page = await ctx.newPage()
  await page.goto('/guides/modded-minecraft-server', { waitUntil: 'networkidle' })
  const copy = page.locator('.codeblock .code-copy')
  const script = (id: string) => page.locator(`#${id}`).evaluate((el) => (el.textContent ?? '').trim())
  expect(await script('code-neoforge')).toContain('./run.sh nogui')
  await expect(copy).toHaveAttribute('data-copy', await script('code-neoforge'))
  await page.getByRole('tab', { name: 'Fabric' }).click()
  await expect(copy).toHaveAttribute('data-copy', await script('code-fabric'))
  await ctx.close()
})

test("the Pterodactyl page's hero on a phone: its terminal under the words, and no browser frame", async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL, viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })
  const page = await ctx.newPage()
  await page.goto('/alternatives/pterodactyl', { waitUntil: 'networkidle' })
  await expect(page.locator('.alt-art-ptero .browser')).toBeHidden()
  const link = await page.locator('.alt-hero-row .link-arrow').first().boundingBox()
  const art = await page.locator('.alt-art-ptero').boundingBox()
  const terminal = await page.locator('.alt-art-ptero .art-terminal').boundingBox()
  expect(link && art && terminal, 'the hero link, its picture and the terminal are on the page').toBeTruthy()
  expect(terminal!.y, 'the terminal starts inside its picture, not pulled up towards the words').toBeGreaterThanOrEqual(art!.y - 1)
  expect(terminal!.y, 'the terminal starts below the hero link').toBeGreaterThanOrEqual(link!.y + link!.height)
  await ctx.close()
})

/** Where the header's Install left the visitor: the page, its hash and state, and whether install instructions are in view. */
async function afterInstall(page: Page, click = true) {
  if (click) await page.locator('header .btn-install').first().click()
  await page.waitForLoadState('networkidle')
  await page.waitForTimeout(700)
  return page.evaluate(() => {
    const own = document.getElementById('install')
    const box = own && !own.closest('[hidden]') ? own.getBoundingClientRect() : null
    return {
      path: location.pathname,
      hash: location.hash,
      state: document.body.getAttribute('data-state'),
      inView: !!box && box.width > 0 && box.height > 0 && box.top < window.innerHeight && box.bottom > 0,
    }
  })
}

type Landing = Awaited<ReturnType<typeof afterInstall>>

/** Install worked: install instructions in view, and on a share page with a template, the template still there. */
function landed(o: Landing, from: 'empty' | 'template', hash = ''): boolean {
  return o.inView && (from === 'empty' ? o.path === '/' : o.path === '/t' && o.state === 'ready' && o.hash === hash)
}

for (const size of sizes) {
  test(`the share page's Install lands on install instructions you can see, at ${size.name} size`, async ({ browser, baseURL }) => {
    const ctx = await browser.newContext({ baseURL, viewport: { width: size.width, height: size.height }, isMobile: size.mobile, hasTouch: size.mobile })
    const page = await ctx.newPage()
    // No template, as with a bad or cut-off link: the landing page's install command.
    await page.goto('/t', { waitUntil: 'networkidle' })
    expect(landed(await afterInstall(page), 'empty'), 'Install on /t with no template').toBe(true)
    // A template: the share page's own install command, with the template kept.
    const hash = '#' + fs.readFileSync('../../../internal/templates/testdata/share-link.txt', 'utf8').trim().split('#')[1]
    await page.goto('/t' + hash, { waitUntil: 'networkidle' })
    expect(landed(await afterInstall(page), 'template', hash), 'Install on /t with a template').toBe(true)
    // Negative controls, the old #install link: on the empty page it reads
    // "install" as a template and says the link is damaged; with a template,
    // it replaces the template the same way. The check catches both.
    await page.goto('/t', { waitUntil: 'networkidle' })
    await page.evaluate(() => document.querySelectorAll('[data-install-link]').forEach((a) => a.setAttribute('href', '#install')))
    const oldEmpty = await afterInstall(page)
    expect(oldEmpty.state, 'the old link on the empty page').toBe('damaged')
    expect(landed(oldEmpty, 'empty'), 'the check catches the old link on the empty page').toBe(false)
    await page.goto('/t' + hash, { waitUntil: 'networkidle' })
    await page.evaluate(() => { location.hash = '#install' })
    expect(landed(await afterInstall(page, false), 'template', hash), 'the check catches the old link replacing the template').toBe(false)
    await ctx.close()
  })
}

/**
 * The landing page's product loop, which shows a new frame 2.6 s, 3.9 s and
 * 5.2 s into each 9 s run: its frame once it's on its second and scrolled
 * until 8% of it shows, then scrolled off and 3 s later, then scrolled back.
 */
async function loopAcrossScrolls(page: Page) {
  const loop = page.locator('[data-loop]')
  const frame = () => loop.getAttribute('data-frame')
  // Scrolls down until the given share of the loop shows at the top; less than none puts it off screen.
  const showing = (share: number) =>
    page.evaluate((share) => {
      const r = document.querySelector('[data-product]')!.getBoundingClientRect()
      window.scrollBy({ top: r.bottom - r.height * share, behavior: 'instant' })
    }, share)
  await expect(loop).toHaveAttribute('data-frame', '2', { timeout: 10_000 })
  await showing(0.08)
  await page.waitForTimeout(400)
  const partly = await frame()
  await showing(-0.5)
  await page.waitForTimeout(300)
  const off = await frame()
  await page.waitForTimeout(3000)
  const offLater = await frame()
  await page.evaluate(() => window.scrollTo({ top: 0, behavior: 'instant' }))
  await page.waitForTimeout(400)
  return { partly, off, offLater, back: await frame() }
}

/** The loop kept going while some of it showed, paused off screen and started over when it came back. */
function keptGoing(o: Awaited<ReturnType<typeof loopAcrossScrolls>>): boolean {
  return ['2', '3'].includes(o.partly ?? '') && o.off === o.offLater && o.back === '1'
}

/** Whether the landing page's terminal is typing once 30% of it shows at the bottom of the screen, then once all of it does. */
async function terminalAcrossScrolls(page: Page) {
  const typing = () => page.locator('[data-terminal]').evaluate((el) => el.classList.contains('is-typing'))
  const showing = (share: number) =>
    page.evaluate((share) => {
      const r = document.querySelector('[data-terminal]')!.getBoundingClientRect()
      window.scrollBy({ top: r.top - (window.innerHeight - r.height * share), behavior: 'instant' })
    }, share)
  await showing(0.3)
  await page.waitForTimeout(400)
  const partly = await typing()
  await showing(1)
  await page.waitForTimeout(400)
  return { partly, whole: await typing() }
}

// IntersectionObserver as the spec, Firefox and Safari have it: an element
// counts as intersecting while any of it shows, and coming into or out of
// view is reported whatever the thresholds. Chromium does both only from the
// lowest threshold, which hid the loop starting over; a threshold at 0 makes
// it behave as the spec does.
function specIntersecting() {
  const Native = window.IntersectionObserver
  window.IntersectionObserver = class extends Native {
    constructor(callback: IntersectionObserverCallback, options: IntersectionObserverInit = {}) {
      const asked = options.threshold ?? 0
      super(callback, { ...options, threshold: [...new Set([0, ...(Array.isArray(asked) ? asked : [asked])])] })
    }
  }
}

/** The landing page at desktop size with motion; spec makes observers behave as the spec has it, and old puts old code back into landing.js. */
async function openLanding(browser: Browser, baseURL: string | undefined, spec: boolean, old?: [RegExp, string]) {
  const ctx = await browser.newContext({ baseURL, viewport: { width: 1440, height: 900 }, reducedMotion: 'no-preference' })
  if (spec) await ctx.addInitScript(specIntersecting)
  let swapped = false
  if (old) {
    await ctx.route(/\/assets\/js\/landing\.[0-9a-f]+\.js$/, async (route) => {
      const response = await route.fetch()
      const js = await response.text()
      const body = js.replace(old[0], old[1])
      swapped = body !== js
      await route.fulfill({ response, body })
    })
  }
  const page = await ctx.newPage()
  await page.goto('/', { waitUntil: 'networkidle' })
  expect(swapped, 'the old code was put back').toBe(!!old)
  return { page, close: () => ctx.close() }
}

const as = (spec: boolean) => (spec ? 'as the spec has it' : 'as Chromium has it')

test("the landing page's product loop keeps going while some of it shows, and pauses off screen", async ({ browser, baseURL }) => {
  const watch = async (spec: boolean, old?: [RegExp, string]) => {
    const { page, close } = await openLanding(browser, baseURL, spec, old)
    const o = await loopAcrossScrolls(page)
    await close()
    return o
  }
  for (const spec of [false, true]) {
    const now = await watch(spec)
    expect(keptGoing(now), `${as(spec)}, the loop's frames: ${JSON.stringify(now)}`).toBe(true)
  }
  // Negative control, the observer before it kept track of whether the loop
  // runs: as the spec has it, crossing 15% on the way out started it over.
  const old = await watch(true, [
    /var running = false;\s*new IntersectionObserver[\s\S]*?\.observe\(product\);/,
    `new IntersectionObserver(function (entries) {
        if (entries[0].isIntersecting) run();
        else stop();
      }, { threshold: 0.15 }).observe(product);`,
  ])
  expect(old.partly, 'the old observer starts the loop over at 15% on the way out').toBe('1')
  expect(keptGoing(old), 'the check catches the old observer').toBe(false)
})

test("the landing page's terminal starts typing once most of it shows", async ({ browser, baseURL }) => {
  const watch = async (spec: boolean, old?: [RegExp, string]) => {
    const { page, close } = await openLanding(browser, baseURL, spec, old)
    const o = await terminalAcrossScrolls(page)
    await close()
    return o
  }
  for (const spec of [false, true]) {
    expect(await watch(spec), as(spec)).toEqual({ partly: false, whole: true })
  }
  // Negative control, the old observer: as the spec has it, it started typing as soon as any of the terminal showed.
  const old = await watch(true, [/if \(entries\[entries\.length - 1\]\.intersectionRatio < 0\.6\) return;/, 'if (!entries[0].isIntersecting) return;'])
  expect(old.partly, 'the old observer starts typing once any of the terminal shows').toBe(true)
})
