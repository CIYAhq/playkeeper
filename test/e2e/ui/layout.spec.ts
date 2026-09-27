import { expect, test, type Page, type Route } from '@playwright/test'
import { FakeConsole, fakePanel, machine, paperLines, server } from './fake-panel'

// A server's page at desktop and phone sizes: its header stays at the top
// while only the content below it scrolls, a section pressed in Settings'
// list glides into place below the header (in one jump with reduced motion),
// a tab shows at once without fading in, and everything that can be pressed
// shows a pointer.

const sizes = {
  desktop: { viewport: { width: 1440, height: 900 } },
  phone: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true },
} as const

const s = server()
const now = new Date().toISOString()

// What the Settings tab and a phone's Overview read, on top of fake-panel.ts.
const reads: Record<string, unknown> = {
  [`/api/machines/${machine.id}/catalog`]: {
    type: 'paper',
    types: [{ id: 'paper', name: 'Paper', available: true }],
    versions: [{ id: 'paper-26.1.2-74', label: '26.1.2', minecraftVersion: '26.1.2', paperBuild: 74, jarSha256: 'cd'.repeat(32), java: 25, recommended: true, notes: '', channel: 'STABLE', experimental: false, supported: true }],
    memoryOptionsMB: [2048, 3072, 4096, 6144],
    recommendedMemoryMB: 4096,
    hostMemoryMB: machine.live.memoryTotalMB,
    maxMemoryMB: 8192,
    systemReserveMB: machine.live.systemReserveMB,
    memoryFreeMB: machine.live.memoryFreeMB,
    servers: [{ id: s.id, name: s.name, memoryMB: 4096, running: true }],
    suggestedPort: 25566,
    image: 'itzg/minecraft-server:2026.9.0-java25',
  },
  [`/api/servers/${s.id}/memory`]: {
    verdict: 'keep',
    params: { budget_mb: 4096, heap_mb: 3072, peak_mb: 2560, days: 14, reason: 'fits' },
    title: 'Its memory fits',
    explanation: 'It needed up to 2.5 GB in the last 14 days.',
    evidence: [],
    actions: [],
    budgetMB: 4096,
    heapMB: 3072,
    recommendedMB: 4096,
    days: Array.from({ length: 14 }, (_, i) => ({ date: `2026-09-${String(12 + i).padStart(2, '0')}`, peakMB: 2200 + i * 20 })),
    options: [2048, 3072, 4096, 6144].map((memoryMB) => ({ memoryMB, heapMB: memoryMB * 0.75, fits: true })),
  },
  [`/api/servers/${s.id}/sleep`]: { enabled: false, idleMinutes: 15, listening: false, defaultIdleMinutes: 15, minIdleMinutes: 5, maxIdleMinutes: 240, today: { count: 0, seconds: 0 } },
  [`/api/servers/${s.id}/schedules`]: { schedules: [] },
  [`/api/servers/${s.id}/schedules/runs`]: { runs: [] },
  [`/api/servers/${s.id}/backups`]: [],
  [`/api/servers/${s.id}/activity`]: [{ ts: now, serverId: s.id, kind: 'player_joined', actor: 'mara_k' }],
  [`/api/servers/${s.id}/players/sessions`]: { range: '24h', sessions: [] },
  [`/api/servers/${s.id}/metrics`]: { from: new Date(Date.now() - 86_400_000).toISOString(), to: now, bucketSeconds: 3600, sampleIntervalSeconds: 60, buckets: [], gaps: [], source: 'agent' },
}

// A player's face: an 8 × 8 grey square.
const face = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><rect width="8" height="8" fill="#9a9e94"/></svg>`

async function panel(page: Page) {
  const log = new FakeConsole()
  log.append(paperLines(200, 3))
  const faked = await fakePanel(page, log)
  await page.route('**/api/**', (route: Route) => {
    const url = new URL(route.request().url())
    if (/^\/api\/players\/[^/]+\/head$/.test(url.pathname)) return route.fulfill({ status: 200, contentType: 'image/svg+xml', body: face })
    const hit = route.request().method() === 'GET' ? reads[url.pathname] : undefined
    if (hit === undefined) return route.fallback()
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(hit) })
  })
  return faked
}

/** Where the page's sticky header is: its top and bottom edges. */
async function headerEdges(page: Page) {
  const box = await page.locator('header[data-sticky-header]').boundingBox()
  if (!box) throw new Error('no sticky header')
  return { top: box.y, bottom: box.y + box.height }
}

test.describe('desktop', () => {
  test.use(sizes.desktop)

  test('the server header stays at the top while the settings scroll under it, and a section pressed in the list glides into place below it', async ({ page }) => {
    const { unexpected } = await panel(page)
    await page.goto('/servers/survival/settings')
    const sections = page.getByRole('navigation', { name: 'Settings sections' })
    await expect(page.getByRole('heading', { name: 'Minecraft version', level: 2 })).toBeVisible()
    const rest = await headerEdges(page)
    expect(rest.top, 'the header is the top of the card').toBe(8)

    await page.mouse.move(900, 600)
    await page.mouse.wheel(0, 600)
    await expect.poll(() => page.evaluate(() => window.scrollY), { message: 'the page itself scrolls' }).toBeGreaterThan(300)
    expect(await headerEdges(page), 'the header stays put').toEqual(rest)
    await expect(page.getByRole('navigation', { name: 'Server pages' }).getByRole('link', { name: 'Overview' })).toBeInViewport()
    const padding = await page.evaluate(() => Number.parseFloat(getComputedStyle(document.documentElement).scrollPaddingTop))
    expect(Math.abs(padding - rest.bottom), 'jumps and focus land below the header').toBeLessThanOrEqual(1)

    // A press glides: the section is current at once, and the page moves over several frames.
    await page.evaluate(() => window.scrollTo({ top: 0, behavior: 'instant' }))
    const path = await sections.getByRole('link', { name: 'Danger zone' }).evaluate(async (link) => {
      const ys: number[] = []
      const current = new Set<string>()
      ;(link as HTMLAnchorElement).click()
      for (let i = 0; i < 12; i++) {
        await new Promise((resolve) => requestAnimationFrame(resolve))
        ys.push(window.scrollY)
        current.add(document.querySelector('nav[aria-label="Settings sections"] [aria-current="location"]')?.textContent ?? '')
      }
      return { current: [...current], ys }
    })
    expect(path.current, 'current from the first frame on, all the way there').toEqual(['Danger zone'])
    expect(new Set(path.ys).size, `the page glides (${path.ys.join(', ')})`).toBeGreaterThan(4)
    await expect(page).toHaveURL(/#danger$/)
    await expect(page.locator('#danger')).toBeFocused()
    await expect(sections.getByRole('link', { name: 'Danger zone' })).toHaveAttribute('aria-current', 'location')

    await sections.getByRole('link', { name: 'Memory', exact: true }).click()
    await expect.poll(async () => Math.round((await page.locator('#memory').boundingBox())?.y ?? 0), { message: 'the section lands just below the header' }).toBe(Math.round(rest.bottom + 24))
    const title = await page.getByRole('heading', { name: 'Memory', exact: true, level: 2 }).boundingBox()
    expect(title?.y ?? 0, 'its title isn’t under the header').toBeGreaterThan(rest.bottom)
    await expect(sections.getByRole('link', { name: 'Memory', exact: true })).toHaveAttribute('aria-current', 'location')

    // With reduced motion the same press is one jump.
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.evaluate(() => window.scrollTo({ top: 0, behavior: 'instant' }))
    const jumped = await sections.getByRole('link', { name: 'Minecraft version' }).evaluate(async (link) => {
      ;(link as HTMLAnchorElement).click()
      await new Promise((resolve) => requestAnimationFrame(resolve))
      const first = window.scrollY
      await new Promise((resolve) => setTimeout(resolve, 300))
      return { first, last: window.scrollY }
    })
    expect(jumped.first, 'the page moved in the first frame').toBeGreaterThan(0)
    expect(jumped.first).toBe(jumped.last)
    expect(unexpected, 'API calls the fake panel does not answer').toEqual([])
  })

  test('a tab shows at once, and everything that can be pressed shows a pointer', async ({ page }) => {
    const { unexpected } = await panel(page)
    await page.goto('/servers/survival/settings')
    await expect(page.getByRole('heading', { name: 'Minecraft version', level: 2 })).toBeVisible()

    // Every frame for half a second after the press, while the tab's code and log arrive.
    const entering = await page.getByRole('navigation', { name: 'Server pages' }).getByRole('link', { name: 'Console' }).evaluate(async (tab) => {
      const seen = new Set<string>()
      ;(tab as HTMLAnchorElement).click()
      for (let i = 0; i < 30; i++) {
        await new Promise((resolve) => requestAnimationFrame(resolve))
        for (const a of document.getAnimations()) {
          const name = (a as CSSAnimation).animationName
          if (a.playState === 'running' && ['fade', 'enter', 'page'].includes(name)) seen.add(`${name} on ${String((a.effect as KeyframeEffect | null)?.target?.className).slice(0, 60)}`)
        }
      }
      return [...seen]
    })
    await expect(page.getByRole('log', { name: 'Server output' })).toBeVisible()
    expect(entering, 'nothing on the new tab fades or slides in').toEqual([])

    await page.getByRole('navigation', { name: 'Server pages' }).getByRole('link', { name: 'Settings' }).click()
    await expect(page.getByRole('heading', { name: 'Minecraft version', level: 2 })).toBeVisible()
    const cursors = () =>
      page.evaluate(() => {
        const controls = 'a[href], button, summary, [role=button], [role=link], [role=tab], [role=switch], [role=checkbox], [role=radio], [role=option], [role=menuitem], [role=menuitemradio], [role=menuitemcheckbox], [data-slot=slider-control]'
        return [...document.querySelectorAll(controls)]
          .filter((el) => el.checkVisibility())
          .map((el) => {
            const disabled = el.matches(':disabled, [aria-disabled="true"], [data-disabled]')
            const { cursor, pointerEvents } = getComputedStyle(el)
            // A disabled control shows not-allowed, or takes no pointer at all.
            const ok = disabled ? cursor !== 'pointer' || pointerEvents === 'none' : cursor === 'pointer'
            return { name: `${el.tagName.toLowerCase()} ${el.getAttribute('role') ?? ''} "${(el.getAttribute('aria-label') ?? el.textContent ?? '').trim().slice(0, 30)}"`, cursor, ok }
          })
      })
    const page1 = await cursors()
    expect(page1.length).toBeGreaterThan(20)
    expect(page1.filter((c) => !c.ok)).toEqual([])
    expect(await page.getByRole('switch').first().evaluate((el) => getComputedStyle(el.closest('label') ?? el).cursor)).toBe('pointer')

    await page.getByRole('button', { name: 'More actions' }).click()
    await expect(page.getByRole('menuitem').first()).toBeVisible()
    const menu = (await cursors()).filter((c) => c.name.startsWith('div menuitem'))
    expect(menu.length).toBeGreaterThan(1)
    expect(menu.filter((c) => !c.ok)).toEqual([])
    await page.keyboard.press('Escape')

    await page.getByRole('combobox', { name: 'Difficulty' }).click()
    await expect(page.getByRole('option').first()).toBeVisible()
    const options = (await cursors()).filter((c) => c.name.startsWith('div option'))
    expect(options.length).toBe(4)
    expect(options.filter((c) => !c.ok)).toEqual([])
    await page.keyboard.press('Escape')
    expect(unexpected, 'API calls the fake panel does not answer').toEqual([])
  })
})

test.describe('phone', () => {
  test.use(sizes.phone)

  test('a compact header stays at the top while the page scrolls, with one scroll area and the tab bar as it was', async ({ page }) => {
    const { unexpected } = await panel(page)
    await page.goto('/servers/survival')
    await expect(page.getByRole('heading', { name: 'Recent activity' })).toBeVisible()
    const header = page.locator('header[data-sticky-header]')
    const tabs = page.getByRole('navigation', { name: 'Server pages' })
    const rest = await headerEdges(page)
    const tabBar = await tabs.boundingBox()
    expect(rest.bottom - rest.top, 'a compact bar').toBeLessThanOrEqual(60)
    await expect(header).not.toHaveAttribute('data-scrolled')

    await page.evaluate(() => window.scrollTo({ top: 400, behavior: 'instant' }))
    await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(100)
    await expect(header).toHaveAttribute('data-scrolled', '')
    expect((await headerEdges(page)).top, 'the header sticks to the top of the screen').toBe(0)
    await expect(header.getByRole('button', { name: 'Search or jump to…' })).toBeInViewport()
    expect(await tabs.boundingBox(), 'the tab bar is untouched').toEqual(tabBar)
    const scrollAreas = await page.evaluate(() =>
      [...document.querySelectorAll('body *')].filter((el) => {
        const style = getComputedStyle(el)
        return /auto|scroll/.test(style.overflowY) && el.scrollHeight > el.clientHeight + 1
      }).length,
    )
    expect(scrollAreas, 'only the page scrolls').toBe(0)
    expect(unexpected, 'API calls the fake panel does not answer').toEqual([])
  })
})
