import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Browser, type BrowserContext, type Page } from '@playwright/test'
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

/**
 * Serves, in place of the analytics' oa.js, one that hands each custom event
 * (and each flush) to the test, and answers for the sites links go out to, so
 * nothing leaves the machine and no visit is counted. oa.js waits for
 * release().
 */
async function recordEvents(ctx: BrowserContext) {
  const events: unknown[][] = []
  let release = () => {}
  const released = new Promise<void>((resolve) => (release = resolve))
  await ctx.exposeFunction('recordEvent', (...event: unknown[]) => void events.push(event))
  await ctx.route('https://analytics-c.ciya.so/oa.js', async (route) => {
    await released
    await route.fulfill({ contentType: 'text/javascript', body: 'window.oa = { track: function (name, props) { recordEvent(name, props) }, flush: function () { recordEvent("flush") } }' })
  })
  await ctx.route(/^https:\/\/(github\.com|www\.hostinger\.com|www\.digitalocean\.com|www\.vultr\.com)\//, (route) => route.fulfill({ contentType: 'text/html', body: '' }))
  // The live demo isn't part of the site's build.
  await ctx.route(/^http:\/\/127\.0\.0\.1:\d+\/demo\//, (route) => route.fulfill({ contentType: 'text/html', body: '' }))
  return { events, release }
}

test('the analytics’ custom events: the install command copied, links out to GitHub, a VPS provider, Watch releases and the live demo, and none from /t', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL, viewport: { width: 1440, height: 900 }, permissions: ['clipboard-read', 'clipboard-write'] })
  const { events, release } = await recordEvents(ctx)
  const page = await ctx.newPage()
  const open = (path: string) => page.goto(path, { waitUntil: 'networkidle' })
  /** The events sent since the last call are these, in order. */
  const sent = async (...want: unknown[][]) => {
    await expect.poll(() => [...events]).toEqual(want)
    events.length = 0
  }

  // A copy before oa.js has loaded waits for it.
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await page.locator('#install .install-copy').click()
  await page.waitForTimeout(300)
  expect(events, 'nothing is sent before oa.js loads').toEqual([])
  release()
  await sent(['install_copied', { spot: 'box', where: '/' }])
  await page.locator('[data-closing] .install-copy').click()
  await page.locator('#install .install-line').selectText()
  await page.keyboard.press('ControlOrMeta+C')
  await page.locator('header .btn-star').click()
  await sent(['install_copied', { spot: 'closing', where: '/' }], ['install_copied', { spot: 'selection', where: '/' }], ['github_clicked', { link: 'repo', where: '/' }], ['flush'])
  await open('/')
  await page.locator('header .nav-demo').click()
  await page.waitForURL(/\/demo\/$/)
  await open('/pricing')
  await page.locator('[data-closing]').getByRole('link', { name: 'Try the live demo' }).click()
  await sent(['demo_opened', { spot: 'header', where: '/' }], ['flush'], ['demo_opened', { spot: 'closing', where: '/pricing' }], ['flush'])

  await open('/pricing')
  await page.getByRole('link', { name: 'Copy the install command' }).click()
  await page.getByRole('link', { name: 'Watch releases on GitHub' }).first().click()
  await sent(['install_copied', { spot: 'button', where: '/pricing' }], ['github_clicked', { link: 'repo', where: '/pricing' }], ['flush'], ['watch_releases_clicked', { plan: 'storage', where: '/pricing' }], ['flush'])

  await open('/sizing#friends=11-20&run=modpack')
  await page.locator('[data-provider="Hostinger"]').getByRole('link', { name: /^See today's price/ }).click()
  await sent(['provider_clicked', { provider: 'Hostinger', plan: 'KVM 8 · 8 vCPU · 32 GB', where: '/sizing' }], ['flush'])

  // A provider's setup guide stays on the site and counts nothing; the guide's Get a server counts the provider.
  await open('/sizing')
  await page.locator('[data-provider="Vultr"]').getByRole('link', { name: 'Setup guide for Vultr' }).click()
  await page.waitForURL(/\/guides\/vultr-minecraft-server$/)
  await page.getByRole('link', { name: 'Get a Vultr server' }).click()
  await sent(['provider_clicked', { provider: 'Vultr', plan: 'High Performance · 4 vCPU · 8 GB', where: '/guides/vultr-minecraft-server' }], ['flush'])

  // The free month under the landing page's install command counts the plan its words price.
  await open('/')
  await page.locator('.free-vps').getByRole('link', { name: 'Get $300 of credit at Vultr' }).click()
  await sent(['provider_clicked', { provider: 'Vultr', plan: 'High Performance · 4 vCPU · 8 GB', where: '/' }], ['flush'])

  // Both install commands in the docs, and a middle-click, which opens its link in a new tab.
  await open('/docs/install')
  await page.locator('.prose pre', { hasText: 'https://playkeeper.io/install | sudo sh' }).locator('.code-copy').click()
  await page.locator('.prose pre', { hasText: 'download/get.sh | sudo sh' }).locator('.code-copy').click()
  const tab = ctx.waitForEvent('page')
  await page.locator('.prose').getByRole('link', { name: 'latest release' }).first().click({ button: 'middle' })
  await (await tab).close()
  await page.locator('.docs-main .meta a').click()
  await sent(['install_copied', { spot: 'code', where: '/docs/install' }], ['install_copied', { spot: 'code', where: '/docs/install' }], ['github_clicked', { link: 'releases', where: '/docs/install' }], ['flush'], ['github_clicked', { link: 'file', where: '/docs/install' }], ['flush'])

  // A script that isn't the install command sends nothing; the guide's own Copy the install command does.
  await open('/guides/modded-minecraft-server')
  await page.locator('.codeblock .code-copy').click()
  await page.locator('.side-install a').click()
  await sent(['install_copied', { spot: 'button', where: '/guides/modded-minecraft-server' }])

  // The share page has no analytics: its Copy and its links send nothing.
  const link = fs.readFileSync('../../../internal/templates/testdata/share-link.txt', 'utf8').trim()
  await open('/t#' + link.split('#')[1])
  expect(await page.evaluate(() => 'oa' in window), '/t loads no analytics').toBe(false)
  await page.locator('#install .install-copy').click()
  await expect(page.locator('#install .install-copy')).toHaveClass(/is-copied/)
  await page.locator('header .btn-star').click()
  await page.waitForURL(/github\.com/)
  expect(events, 'events from /t').toEqual([])
  await ctx.close()
})

/**
 * Serves, in place of Whop's s.js, one that hands the test each call to the
 * pixel, queued before it loaded or made after, as [name, ...arguments], so
 * nothing reaches Whop. urls are the files the page asked Whop for.
 */
async function recordWhop(ctx: BrowserContext) {
  const calls: unknown[][] = []
  const urls: string[] = []
  await ctx.exposeFunction('recordWhop', (...call: unknown[]) => void calls.push(call))
  await ctx.route('https://t.whop.tw/**', (route) => {
    urls.push(route.request().url())
    return route.fulfill({ contentType: 'text/javascript', body: 'whop.q.splice(0).forEach(function (c) { recordWhop.apply(null, c.slice(1)) }); whop.track = function () { recordWhop.apply(null, arguments) }' })
  })
  return { calls, urls }
}

/**
 * A share sheet for headless Chromium, which has none: it hands the test what
 * the page shares, or is cancelled or refused when window.shareAnswer says so.
 */
async function fakeShareSheet(ctx: BrowserContext) {
  const shared: unknown[] = []
  await ctx.exposeFunction('recordShare', (data: unknown) => void shared.push(data))
  await ctx.addInitScript(() => {
    const w = window as unknown as { shareAnswer?: string; recordShare: (data: ShareData) => Promise<void> }
    Object.defineProperty(Navigator.prototype, 'share', {
      configurable: true,
      value: (data: ShareData) => {
        if (w.shareAnswer === 'cancel') return Promise.reject(new DOMException('Share canceled', 'AbortError'))
        if (w.shareAnswer === 'refuse') return Promise.reject(new DOMException('Not allowed', 'NotAllowedError'))
        return w.recordShare(data)
      },
    })
  })
  return shared
}

test('/start: the start channel’s command, Send to my computer on phones, Whop’s ad pixel on this page alone and not under Global Privacy Control, and our events too', async ({ browser, baseURL }) => {
  const phone = { baseURL, viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, permissions: ['clipboard-read', 'clipboard-write'] }
  const ctx = await browser.newContext(phone)
  const { events, release } = await recordEvents(ctx)
  release()
  const whop = await recordWhop(ctx)
  const shared = await fakeShareSheet(ctx)
  /** The events sent since the last call are these, in order. */
  const sent = async (...want: unknown[][]) => {
    await expect.poll(() => [...events]).toEqual(want)
    events.length = 0
  }
  const page = await ctx.newPage()
  const answer = (a: 'cancel' | 'refuse') => page.evaluate((a) => { (window as unknown as { shareAnswer: string }).shareAnswer = a }, a)
  const clipboard = () => page.evaluate(() => navigator.clipboard.readText())
  const ad = '/start?utm_campaign=pk01-launch&utm_source=fb&wacid=1'
  await page.goto(ad, { waitUntil: 'networkidle' })
  expect(page.url(), 'the query string stays, for the pixel to read the ad’s IDs').toBe(baseURL + ad)
  const start = 'curl -fsSL https://playkeeper.io/install/start | sudo sh'
  await expect(page.locator('#install .install-line')).toHaveText(start)
  await expect(page.locator('[data-closing] .install-copy')).toHaveAttribute('data-copy', start)
  expect(whop.urls).toEqual(['https://t.whop.tw/s.js'])
  // Nothing wider than the screen, and no serious accessibility violations: the page isn't in the sitemap the other checks visit.
  await page.evaluate(async () => {
    for (let y = 0; y < document.body.scrollHeight; y += 500) { window.scrollTo(0, y); await new Promise((r) => setTimeout(r, 30)) }
    window.scrollTo(0, 0)
  })
  await page.waitForTimeout(700)
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), '/start at phone size is wider than the screen').toBeLessThanOrEqual(0)
  await axe(page, '/start at phone size')
  await page.locator('#install .install-copy').click()
  await page.locator('[data-closing] .install-copy').click()
  await sent(['install_copied', { spot: 'box', channel: 'start', where: '/start' }], ['install_copied', { spot: 'closing', channel: 'start', where: '/start' }])

  // Send to my computer, beside both Copy buttons: the phone's share sheet
  // with the page's address, ad IDs and all; nothing when it's cancelled; the
  // address copied when sharing is refused or the browser can't share.
  const share = page.locator('#install .install-share')
  await expect(share).toHaveText('Send to my computer')
  await expect(page.locator('[data-closing] .install-share')).toBeVisible()
  await share.click()
  await sent(['install_shared', { spot: 'box', how: 'share', where: '/start' }], ['flush'])
  expect(shared).toEqual([{ title: await page.title(), url: baseURL + ad }])
  await answer('cancel')
  await share.click()
  await page.waitForTimeout(300)
  expect(events, 'nothing is sent when the share sheet is cancelled').toEqual([])
  expect(await clipboard(), 'nor copied').toBe(start)
  await answer('refuse')
  await page.locator('[data-closing] .install-share').click()
  await sent(['install_shared', { spot: 'closing', how: 'copy', where: '/start' }], ['flush'])
  expect(await clipboard()).toBe(baseURL + ad)
  await page.evaluate(() => navigator.clipboard.writeText(''))
  await page.evaluate(() => Reflect.deleteProperty(Navigator.prototype, 'share'))
  await share.click()
  await expect(share).toHaveText('Link copied')
  await expect(page.locator('[data-toast]')).toHaveText(/^Link copied\./)
  await sent(['install_shared', { spot: 'box', how: 'copy', where: '/start' }], ['flush'])
  expect(await clipboard()).toBe(baseURL + ad)
  expect(shared, 'only the first went through the share sheet').toHaveLength(1)

  // Try the live demo.
  await page.getByRole('link', { name: 'Try the live demo' }).first().click()
  await page.waitForURL(/\/demo\/$/)
  await sent(['demo_opened', { spot: 'page', where: '/start' }], ['flush'])

  // Whop heard the page view, then each copy, share and demo, with the visit's one id.
  await expect.poll(() => whop.calls.length).toBe(8)
  expect(whop.calls.slice(0, 2)).toEqual([['setScope', 'biz_bbmk63HMB3yZ4c'], ['page']])
  const conversions = whop.calls.slice(2) as [string, { event_id: string }][]
  expect(conversions.map(([name]) => name)).toEqual(['install_copied', 'install_copied', 'install_shared', 'install_shared', 'install_shared', 'demo_opened'])
  expect(conversions[0]?.[1].event_id, 'an id for the visit').toMatch(/^[0-9a-f-]{36}$/)
  expect(new Set(conversions.map(([, p]) => p.event_id)).size, 'one id for every event in a visit').toBe(1)

  // A desktop has no Send to my computer, and no other page loads the pixel.
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(ad, { waitUntil: 'networkidle' })
  await expect(page.locator('#install .install-copy')).toBeVisible()
  await expect(share).toBeHidden()
  await axe(page, '/start at desktop size')
  const loads = whop.urls.length
  await page.goto('/', { waitUntil: 'networkidle' })
  expect(await page.evaluate(() => 'whop' in window), 'the landing page has no ad pixel').toBe(false)
  expect(whop.urls).toHaveLength(loads)
  await ctx.close()

  // With Global Privacy Control, /start doesn't load it either.
  const gpc = await browser.newContext(phone)
  await recordEvents(gpc).then((r) => r.release())
  const gpcWhop = await recordWhop(gpc)
  await gpc.addInitScript(() => Object.defineProperty(Navigator.prototype, 'globalPrivacyControl', { get: () => true }))
  const private_ = await gpc.newPage()
  await private_.goto('/start', { waitUntil: 'networkidle' })
  expect(await private_.evaluate(() => 'whop' in window), 'no pixel under Global Privacy Control').toBe(false)
  expect(gpcWhop.urls).toEqual([])
  await gpc.close()
})

test('the landing page shows the install command of the channel a visitor came from, for the codes it lists', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL, viewport: { width: 1440, height: 900 }, permissions: ['clipboard-read', 'clipboard-write'] })
  const { events, release } = await recordEvents(ctx)
  release()
  const page = await ctx.newPage()
  const plain = 'curl -fsSL https://playkeeper.io/install | sudo sh'
  const cygnus = 'curl -fsSL https://playkeeper.io/install/cygnus | sudo sh'
  /** The install commands the page shows and copies: each box's line and Copy, its phone lines, and the terminal. */
  const shown = () =>
    page.evaluate(() => {
      const text = (sel: string) => [...document.querySelectorAll(sel)].map((el) => (el.textContent ?? '').replace(/\s+/g, ' ').trim())
      return {
        lines: text('[data-install] .install-line'),
        copies: [...document.querySelectorAll('[data-install] [data-copy]')].map((el) => el.getAttribute('data-copy')),
        phone: text('[data-install] .install-lines'),
        terminal: text('[data-terminal] [data-type]'),
      }
    })

  // Where /go/cygnus sends a visitor: the hero's box, the closing band's and the terminal all show the channel's command.
  await page.goto('/?utm_source=youtube&utm_medium=sponsor&utm_campaign=creators-oct26&utm_content=cygnus', { waitUntil: 'networkidle' })
  expect(await shown()).toEqual({
    lines: [cygnus, cygnus],
    copies: [cygnus, cygnus],
    phone: ['curl -fsSL \\ https://playkeeper.io/install/cygnus \\ | sudo sh', 'curl -fsSL \\ https://playkeeper.io/install/cygnus \\ | sudo sh'],
    terminal: ['$ curl -fsSL https://playkeeper.io/install/cygnus \\ | sudo sh'],
  })
  await page.locator('#install .install-copy').click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(cygnus)
  await expect.poll(() => [...events]).toEqual([['install_copied', { spot: 'box', channel: 'cygnus', where: '/' }]])

  // A code it doesn't list, one that tries to change the command, and any other page: the usual command.
  for (const path of ['/?utm_content=nope', '/?utm_content=cygnus%20%7C%20sh%20x', '/?utm_content=CYGNUS']) {
    await page.goto(path, { waitUntil: 'networkidle' })
    const o = await shown()
    expect([...o.lines, ...o.copies], path).toEqual([plain, plain, plain, plain])
    expect(o.terminal.join(), path).not.toContain('/install/')
  }
  await page.goto('/pricing?utm_content=cygnus', { waitUntil: 'networkidle' })
  await expect(page.getByRole('link', { name: 'Copy the install command' })).toHaveAttribute('data-copy', plain)
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

/** The screens the site's screenshots are checked on: the designs' sizes, at the pixel ratios of a sharp laptop and phone. */
const sharp = [
  { name: 'desktop', width: 1440, height: 900, dpr: 2, mobile: false },
  { name: 'phone', width: 390, height: 844, dpr: 3, mobile: true },
]

/**
 * Every screenshot the page shows, once each has loaded: the file the
 * browser picked, how many pixels wide it is (its name says, as
 * site/tools/shots.py writes it), and how many screen pixels its picture
 * covers, object-fit included.
 */
async function screenshots(page: Page) {
  await page.evaluate(async () => {
    for (let y = 0; y < document.body.scrollHeight; y += 400) { window.scrollTo(0, y); await new Promise((r) => setTimeout(r, 60)) }
    window.scrollTo(0, 0)
  })
  await page.waitForFunction(() => [...document.querySelectorAll<HTMLImageElement>('img.shot-img')].every((i) => !i.getClientRects().length || i.complete))
  return page.evaluate(() =>
    [...document.querySelectorAll<HTMLImageElement>('img.shot-img')].flatMap((img) => {
      const box = img.getBoundingClientRect()
      if (!box.width || !img.naturalWidth) return []
      const cover = getComputedStyle(img).objectFit === 'cover'
      const shown = cover ? Math.max(box.width, (box.height * img.naturalWidth) / img.naturalHeight) : box.width
      const file = img.currentSrc.replace(/^.*\/shots\//, '')
      return [{ file, pixels: Number(/-(\d+)w\.[0-9a-f]+\.(?:avif|webp)$/.exec(file)?.[1] ?? 0), needed: Math.round(shown * devicePixelRatio) }]
    }),
  )
}

for (const size of sharp) {
  test(`screenshots are never enlarged: each shows at least one pixel of its file per screen pixel, at ${size.name} size`, async ({ browser, baseURL }) => {
    const ctx = await browser.newContext({ baseURL, viewport: { width: size.width, height: size.height }, deviceScaleFactor: size.dpr, isMobile: size.mobile, hasTouch: size.mobile, reducedMotion: 'reduce' })
    const page = await ctx.newPage()
    let checked = 0
    for (const path of await pagesToVisit(page)) {
      await page.goto(path, { waitUntil: 'networkidle' })
      for (const s of await screenshots(page)) {
        checked++
        expect.soft(s.file, `${path}: the browser takes AVIF`).toMatch(/\.avif$/)
        expect.soft(s.pixels, `${path}: ${s.file} is ${s.pixels} pixels wide, shown across ${s.needed}`).toBeGreaterThanOrEqual(s.needed * 0.99)
      }
    }
    expect(checked, 'screenshots checked').toBeGreaterThan(30)
    // Negative control: offered only its narrowest file, a screenshot is enlarged, and the check says so.
    await ctx.route(/\/features\/mods-and-modpacks$/, async (route) => {
      const response = await route.fetch()
      const html = (await response.text()).replace(/(srcset="[^",]+?) \d+w,[^"]*"/g, '$1 1w"')
      await route.fulfill({ response, body: html })
    })
    await page.goto('/features/mods-and-modpacks', { waitUntil: 'networkidle' })
    const narrow = await screenshots(page)
    expect(narrow.some((s) => s.pixels < s.needed * 0.99), 'the check catches a screenshot shown larger than its file').toBe(true)
    await ctx.close()
  })
}

/**
 * How the page's screenshot frames sit: each phone's screen, with the top of
 * its screenshot against the status bar and its bottom against the screen's
 * edge; each step picture against its box; the modded guide's pair of pictures.
 */
async function frames(page: Page) {
  return page.evaluate(() => {
    const rect = (el: Element | null) => el?.getBoundingClientRect()
    const phones = [...document.querySelectorAll('.phone')].flatMap((p) => {
      const screen = rect(p.querySelector('.phone-screen'))
      const status = rect(p.querySelector('.phone-status'))
      const img = p.querySelector<HTMLImageElement>('img.shot-img')
      if (!screen?.width || !status || !img) return []
      const shot = img.getBoundingClientRect()
      const card = rect(p.closest('.feature-card'))
      return [{ cardTop: card ? Math.round(card.top + scrollY) : undefined, screenTop: screen.top + scrollY, gapAbove: shot.top - status.bottom, gapBelow: screen.bottom - shot.bottom, position: getComputedStyle(img).objectPosition }]
    })
    const steps = [...document.querySelectorAll('.step-card-art .cardshot img')].map((img) => {
      const art = img.closest('.step-card-art')!
      const box = art.getBoundingClientRect()
      const inset = art.clientTop + parseFloat(getComputedStyle(art).paddingTop)
      const shot = img.getBoundingClientRect()
      return { top: shot.top - box.top - inset, bottom: box.bottom - inset - shot.bottom }
    })
    const pair = [...document.querySelectorAll('.figure-pair img')].map((img) => img.getBoundingClientRect().height)
    return { phones, steps, pair }
  })
}

type Frames = Awaited<ReturnType<typeof frames>>

/** What's wrong with the frames: a phone's screenshot not starting at the top of its screen or not filling it, feature cards' phones out of line, a step picture not filling its box, or the pair's pictures of different heights. */
function frameProblems({ phones, steps, pair }: Frames): string[] {
  const out: string[] = []
  for (const p of phones) {
    if (Math.abs(p.gapAbove) > 0.5) out.push(`a phone's screenshot starts ${p.gapAbove}px from its status bar`)
    if (Math.abs(p.gapBelow) > 0.5) out.push(`a phone's screenshot ends ${p.gapBelow}px from its screen's edge`)
    if (!p.position.endsWith(' 0%')) out.push(`a phone's screenshot is placed at ${p.position}, not from its top`)
  }
  const rows = new Map<number, number[]>()
  for (const p of phones) if (p.cardTop !== undefined) rows.set(p.cardTop, [...(rows.get(p.cardTop) ?? []), p.screenTop])
  for (const tops of rows.values()) if (Math.max(...tops) - Math.min(...tops) > 0.5) out.push(`feature cards' phones in a row start at ${tops.join(', ')}`)
  for (const s of steps) if (Math.abs(s.top) > 0.5 || Math.abs(s.bottom) > 0.5) out.push(`a step picture is ${s.top}px from its box's top and ${s.bottom}px from its bottom`)
  if (pair.length && Math.max(...pair) - Math.min(...pair) > 0.5) out.push(`the pair's pictures are ${pair.join(' and ')}px tall`)
  return out
}

for (const size of sharp) {
  test(`screenshots start at the top of their screens and fill their frames, and cards in a row line up, at ${size.name} size`, async ({ browser, baseURL }) => {
    // bypassCSP: the negative control below adds a style the site's policy refuses.
    const ctx = await browser.newContext({ baseURL, viewport: { width: size.width, height: size.height }, deviceScaleFactor: size.dpr, isMobile: size.mobile, hasTouch: size.mobile, reducedMotion: 'reduce', bypassCSP: true })
    const page = await ctx.newPage()
    const seen = { phones: 0, steps: 0, pair: 0 }
    for (const path of ['/', '/features/mods-and-modpacks', '/guides/modded-minecraft-server']) {
      await page.goto(path, { waitUntil: 'networkidle' })
      await page.evaluate(async () => {
        for (let y = 0; y < document.body.scrollHeight; y += 400) { window.scrollTo(0, y); await new Promise((r) => setTimeout(r, 60)) }
      })
      const f = await frames(page)
      seen.phones += f.phones.length
      seen.steps += f.steps.length
      seen.pair += f.pair.length
      expect.soft(frameProblems(f), path).toEqual([])
    }
    expect(seen, 'frames checked').toEqual({ phones: 9, steps: 4, pair: 2 })
    // Negative control: a phone screen that isn't a grid leaves its screenshot as tall as the screen, pushed down by the status bar and cut off below.
    await page.goto('/', { waitUntil: 'networkidle' })
    await page.addStyleTag({ content: '.phone-screen { display: block !important; }' })
    expect(frameProblems(await frames(page)).some((p) => p.includes('from its screen')), 'the check catches a screenshot that overflows its screen').toBe(true)
    await ctx.close()
  })
}

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
