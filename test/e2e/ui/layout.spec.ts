import { expect, test, type Locator, type Page, type Route } from '@playwright/test'
import { FakeConsole, fakePanel, paperLines, settingsReads } from './fake-panel'

// A server's page at desktop and phone sizes: its header stays at the top
// while only the content below it scrolls, a section pressed in Settings'
// list glides into place below the header (in one jump with reduced motion),
// a tab shows at once without fading in, everything that can be pressed
// shows a pointer, and the Map tab's map takes the height the page has left.

const sizes = {
  desktop: { viewport: { width: 1440, height: 900 } },
  phone: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true },
} as const

// What the Settings tab and a phone's Overview read, on top of fake-panel.ts.
const reads = settingsReads()

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

/** The map turned on, drawn or still drawing (`map.state`, read at each request), its tiles not drawn yet. */
async function mapPanel(page: Page) {
  const faked = await panel(page)
  const map = { state: 'ready' as 'ready' | 'drawing' }
  const base = `/api/servers/${faked.server.id}/map`
  const world = (name: string, dimension: string, label: string) => ({ name, dimension, label, spawn: { x: 0, z: 0 }, zoom: { max: 3, default: 3, extra: 2 }, refreshSeconds: 60 })
  const player = (name: string, x: number, z: number) => ({ name, uuid: '', world: 'world', dimension: 'overworld', x, z })
  const answers: Record<string, () => unknown> = {
    [base]: () => ({
      supported: true,
      enabled: true,
      state: map.state,
      message: '',
      areas: 4800,
      bytes: 190_000_000,
      lastDrawn: map.state === 'ready' ? new Date(Date.now() - 3_600_000).toISOString() : undefined,
      progress: map.state === 'drawing' ? { done: 1200, total: 4800, percent: 25, secondsLeft: 420 } : undefined,
      plugin: 'squaremap',
      estimatedMinutes: 10,
      estimatedMegabytes: 200,
      public: true,
      publicPlayers: false,
      path: '/map/Pk7Map0Link0Token0Abcd',
      restartWhenEmpty: false,
      checkedAt: new Date().toISOString(),
    }),
    [`${base}/worlds`]: () => ({ worlds: [world('world', 'overworld', 'Overworld'), world('world_nether', 'nether', 'Nether'), world('world_the_end', 'end', 'The End')], tileSize: 512 }),
    [`${base}/players`]: () => ({ players: [player('mara_k', 24, -10), player('tobi2009', 14, 25), player('JunoFox', -3, -47)], updatedAt: new Date().toISOString() }),
    [`${base}/area`]: () => ({ area: 'explored', options: [], fill: { state: 'idle', world: 'world', chunks: 0, total: 0, percent: 0, etaSeconds: -1, pauseForPlayers: true, installed: true, presets: [] } }),
  }
  await page.route('**/api/**', (route: Route) => {
    const url = new URL(route.request().url())
    if (url.pathname.startsWith(`${base}/tiles/`)) return route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"Not drawn yet.","code":"not_found"}' })
    const answer = route.request().method() === 'GET' ? answers[url.pathname] : undefined
    if (!answer) return route.fallback()
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(answer()) })
  })
  return { ...faked, map }
}

/** Where an element is on the screen: its edges. */
async function edges(target: Locator) {
  const box = await target.boundingBox({ timeout: 10_000 })
  if (!box) throw new Error('not on the page')
  return { top: box.y, bottom: box.y + box.height, left: box.x, right: box.x + box.width }
}

const pageScrolls = (page: Page) => page.evaluate(() => document.documentElement.scrollHeight > window.innerHeight)

/** Where the page's sticky header is: its top and bottom edges. */
async function headerEdges(page: Page) {
  const box = await page.locator('header[data-sticky-header]').boundingBox({ timeout: 10_000 })
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

  test('the Map tab’s map reaches down to the footer beside its panel, drawn or still drawing, in a window of any height', async ({ page }) => {
    const { unexpected, map } = await mapPanel(page)
    const canvas = page.getByRole('application', { name: /^Map of / })
    const footer = page.getByText('Not an official Minecraft product')
    for (const state of ['ready', 'drawing'] as const) {
      map.state = state
      // The panel's first card: who's playing, or how far the drawing is.
      const card = page.getByText(state === 'ready' ? 'Playing now' : 'Drawing the map', { exact: true })
      await page.setViewportSize(sizes.desktop.viewport)
      await page.goto('/servers/survival/map')
      await expect(card).toBeVisible()
      await expect(page.getByText('Share with a link', { exact: true })).toBeVisible({ visible: state === 'ready' })
      for (const height of [sizes.desktop.viewport.height, 1200]) {
        await page.setViewportSize({ width: sizes.desktop.viewport.width, height })
        // The page's own bottom padding is all that's between them.
        await expect.poll(async () => Math.round((await edges(footer)).top - (await edges(canvas)).bottom), { message: `${state}, ${height} pixels high: the map ends just above the footer` }).toBe(24)
        expect(await pageScrolls(page), 'the page fits the window').toBe(false)
        const [box, side] = [await edges(canvas), await edges(card)]
        expect(side.left, 'its panel stays beside it').toBeGreaterThan(box.right)
        expect(side.top - box.top, 'at its top').toBeLessThan(24)
      }
    }
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

  test('the Map tab’s map takes the screen down to its line of players or its drawing card', async ({ page }) => {
    const { unexpected, map } = await mapPanel(page)
    for (const state of ['ready', 'drawing'] as const) {
      map.state = state
      await page.goto('/servers/survival/map')
      const box = await edges(page.getByRole('application', { name: /^Map of / }))
      const under = await edges(state === 'ready' ? page.getByText('3 playing', { exact: true }) : page.locator('[aria-live=polite]', { hasText: 'Drawing the map' }))
      const tabBar = await edges(page.getByRole('navigation', { name: 'Server pages' }))
      expect(under.top - box.bottom, `${state}: the line or card sits right under the map`).toBeLessThan(20)
      expect(tabBar.top - under.bottom, `${state}: with no gap left above the tab bar`).toBeLessThan(40)
      expect(await pageScrolls(page), 'the page fits the screen').toBe(false)
    }
    expect(unexpected, 'API calls the fake panel does not answer').toEqual([])
  })
})
