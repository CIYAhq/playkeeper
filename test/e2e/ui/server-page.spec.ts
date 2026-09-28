import AxeBuilder from '@axe-core/playwright'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { faceIndex, faceSvg } from '../../../web/src/demo/faces'
import { iconSvg } from '../../../web/src/demo/icons'
import { FakeConsole, fakePanel, settingsReads } from './fake-panel'

// The public page at a machine's address, as the panel serves it on ports 443
// and 80: index.html with the root marked, and the page's calls under
// /api/public/server-page. Each state at desktop and phone sizes: nothing
// wider than the screen, no serious accessibility violations, Copy copies the
// address, and a page that is off names no server. With PK_SHOTS set to a
// folder, it saves a full-page screenshot of each.

const sizes = {
  desktop: { viewport: { width: 1440, height: 900 } },
  phone: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true },
} as const

interface Server {
  about?: string
  stream?: { site: 'twitch' | 'youtube'; channel: string; url: string }
  board?: { headline?: string; live?: boolean; next?: string; stats?: { label: string; value: string }[]; checklist?: { label: string; done: boolean }[]; updatedAt: string }
  slug: string
  name: string
  motd: string
  address: string
  state: 'online' | 'starting' | 'sleeping' | 'offline'
  players?: { online: number; max: number; names?: string[] }
  minecraftVersion: string
  type: string
  modpack?: { name: string; version: string }
  map?: string
  pack?: string
  inviteOnly: boolean
  hasIcon: boolean
}

const event: Server = {
  slug: 'claude',
  name: 'Claude tries to beat Minecraft',
  motd: 'Join to help or sabotage. Live every evening from 20:00 CEST',
  address: 'ai.playkeeper.me',
  state: 'online',
  players: { online: 64, max: 80 },
  minecraftVersion: '1.21.10',
  type: 'paper',
  map: 'https://ai.playkeeper.me:8443/map/Xq3pL9sKd2Wm8Rt4Vn6bYc',
  inviteOnly: false,
  hasIcon: true,
}

const modded: Server = {
  slug: 'atm10',
  name: 'Siya’s ATM10',
  motd: 'All the Mods 10 with friends. Bring snacks.',
  address: 'siya.playkeeper.me',
  state: 'online',
  players: { online: 5, max: 10, names: ['mara_k', 'tobi2009', 'JunoFox', 'Steve_Builds', 'pixelpaws'] },
  minecraftVersion: '1.21.1',
  type: 'neoforge',
  modpack: { name: 'All the Mods 10', version: '4.2' },
  pack: 'https://siya.playkeeper.me:8443/packs/Pk7uYt2wQz9mN4bV6cX1aL',
  map: 'https://siya.playkeeper.me:8443/map/Mz1qW8eR4tY6uI2oP9aS3d',
  inviteOnly: true,
  hasIcon: true,
}

const asleep: Server = {
  slug: 'survival',
  name: 'Survival',
  motd: 'A Minecraft server',
  address: 'alex.playkeeper.me',
  state: 'sleeping',
  minecraftVersion: '26.1.2',
  type: 'vanilla',
  inviteOnly: false,
  hasIcon: false,
}

// The flagship's owner blocks: its rules, its stream and what the harness posts.
const rules = [
  'Help Claude or sabotage it: pick a side at spawn.',
  'Players can hurt Claude only in the sabotage windows.',
  'No TNT, lava or fire near Claude.',
  'No slurs, spam or offensive builds: moderators ban on sight.',
  'Nothing here is for sale.',
  '',
  'Unofficial. Not affiliated with Anthropic or Mojang.',
].join('\n')
const milestones = ['Crafting table', 'Stone mined', 'Stone pickaxe', 'Iron smelted', 'Iron pickaxe', 'Iron armour', 'Lava bucket', 'Diamonds found', 'Obsidian', 'Nether reached', 'Fortress found', 'Blaze rod', 'Stronghold found', 'The End reached', 'Ender Dragon killed']
const board = {
  headline: 'Day 3 · Nether reached',
  live: true,
  stats: [
    { label: 'Deaths', value: '5' },
    { label: 'Spent', value: '$64.10' },
    { label: 'Tokens', value: '19.2M' },
    { label: 'Helpers', value: '612' },
    { label: 'Saboteurs', value: '488' },
    { label: 'Players', value: '1,204' },
  ],
  checklist: milestones.map((label, i) => ({ label, done: i < 10 })),
  updatedAt: new Date(Date.now() - 40_000).toISOString(),
}
const stream = { site: 'twitch' as const, channel: 'example_channel', url: 'https://www.twitch.tv/example_channel' }

const pages = {
  event: { address: 'ai.playkeeper.me', servers: [event] },
  eventLive: { address: 'ai.playkeeper.me', servers: [{ ...event, about: rules, stream, board }] },
  eventBetween: { address: 'ai.playkeeper.me', servers: [{ ...event, state: 'offline', players: undefined, about: rules, stream, board: { ...board, live: false, next: new Date(Date.now() + (3 * 60 + 12) * 60_000 + 30_000).toISOString() } }] },
  modded: { address: 'siya.playkeeper.me', servers: [modded] },
  asleep: { address: 'alex.playkeeper.me', servers: [asleep] },
  offline: { address: 'alex.playkeeper.me', servers: [{ ...asleep, state: 'offline' }] },
  several: {
    address: 'alex.playkeeper.me',
    servers: [
      { ...asleep, state: 'online', players: { online: 2, max: 10 }, type: 'paper', hasIcon: true },
      { ...asleep, slug: 'creative', name: 'Creative', motd: 'Build anything', address: 'alex.playkeeper.me:25566', state: 'offline', type: 'fabric' },
    ],
  },
} satisfies Record<string, { address: string; servers: Server[] }>

type Scene = keyof typeof pages | 'gone'

/** Serves the built UI as the panel's public listener does, with the page's calls faked. */
async function serve(page: Page, scene: Scene) {
  await page.route(/\/$/, async (route) => {
    const res = await route.fetch()
    const html = (await res.text()).replace('<div id="root"></div>', '<div id="root" data-page="server"></div>')
    await route.fulfill({ response: res, body: html })
  })
  await page.route('**/api/public/server-page', (route) =>
    scene === 'gone' ? route.fulfill({ status: 404, contentType: 'text/plain', body: '404 page not found\n' }) : route.fulfill({ json: pages[scene] }),
  )
  await page.route('**/api/public/server-page/icons/*', (route) => {
    const slug = new URL(route.request().url()).pathname.split('/').pop() ?? ''
    route.fulfill({ contentType: 'image/svg+xml', body: iconSvg(slug.length % 16) })
  })
  await page.route('**/api/public/server-page/faces/*', (route) => {
    const name = decodeURIComponent(new URL(route.request().url()).pathname.split('/').pop() ?? '')
    route.fulfill({ contentType: 'image/svg+xml', body: faceSvg(faceIndex(name)) })
  })
}

async function axe(page: Page, where: string) {
  const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
  const bad = result.violations.filter((x) => x.impact === 'serious' || x.impact === 'critical')
  expect.soft(bad.map((x) => `${x.id}: ${x.nodes.map((n) => n.target.join(' ')).join(', ')}`), where).toEqual([])
}

const shots = process.env.PK_SHOTS

for (const [size, device] of Object.entries(sizes)) {
  for (const scene of [...(Object.keys(pages) as Scene[]), 'gone' as const]) {
    test(`the server page (${scene}) at ${size} size: fits the screen, no serious accessibility violations`, async ({ browser, baseURL }) => {
      const ctx = await browser.newContext({ baseURL, ...device })
      const page = await ctx.newPage()
      const errors: string[] = []
      page.on('pageerror', (e) => errors.push(e.message))
      await serve(page, scene)
      await page.goto('/', { waitUntil: 'networkidle' })
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
      const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
      expect.soft(wide, `wider than the screen by ${wide}px`).toBeLessThanOrEqual(0)
      await axe(page, `${scene} at ${size}`)
      await expect(page.getByRole('link', { name: 'Start your own server' })).toHaveAttribute('href', 'https://playkeeper.io/?ref=server-page')
      if (scene === 'gone') {
        await expect(page.getByRole('heading', { level: 1 })).toHaveText('There’s no server page here')
        for (const p of Object.values(pages)) for (const s of p.servers) await expect(page.getByText(s.name)).toHaveCount(0)
      }
      if (shots) {
        mkdirSync(shots, { recursive: true })
        await page.screenshot({ path: join(shots, `${scene}-${size}.png`), fullPage: true })
      }
      expect(errors, 'errors on the page').toEqual([])
      await ctx.close()
    })
  }
}

test('Copy copies the address, and names shown by the owner get faces', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL, permissions: ['clipboard-read', 'clipboard-write'] })
  const page = await ctx.newPage()
  await serve(page, 'modded')
  await page.goto('/', { waitUntil: 'networkidle' })
  await page.getByRole('button', { name: 'Copy address' }).click()
  await expect(page.getByRole('button', { name: 'Copied' })).toBeVisible()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('siya.playkeeper.me')
  await expect(page.getByRole('img', { name: /JunoFox/ })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Open the map' })).toHaveAttribute('href', modded.map ?? '')
  await expect(page.getByRole('link', { name: /Get the All the Mods 10 modpack/ })).toHaveAttribute('href', modded.pack ?? '')
  await ctx.close()
})

test('a page without names shown lists no players', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL })
  const page = await ctx.newPage()
  await serve(page, 'event')
  await page.goto('/', { waitUntil: 'networkidle' })
  await expect(page.getByText('64 of 80 playing')).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Playing now' })).toHaveCount(0)
  await ctx.close()
})

test('the stream loads nothing from Twitch until Watch live, then Twitch’s player for this page', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL })
  const page = await ctx.newPage()
  const here = new URL(baseURL ?? '').hostname
  const elsewhere: string[] = []
  page.on('request', (r) => {
    if (new URL(r.url()).hostname !== here) elsewhere.push(r.url())
  })
  await page.route('https://player.twitch.tv/**', (route) => route.fulfill({ contentType: 'text/html', body: '<!doctype html><title>Player</title>' }))
  await serve(page, 'eventLive')
  await page.goto('/', { waitUntil: 'networkidle' })
  await expect(page.getByText('Live now')).toBeVisible()
  await expect(page.getByRole('link', { name: /Follow on Twitch/ })).toHaveAttribute('href', stream.url)
  expect(elsewhere, 'requests to other sites before Watch live').toEqual([])
  await page.getByRole('button', { name: 'Watch live' }).click()
  const player = page.locator('iframe')
  await expect(player).toHaveAttribute('src', `https://player.twitch.tv/?channel=example_channel&parent=${here}&autoplay=true`)
  await expect(player).toHaveAttribute('referrerpolicy', 'origin')
  await ctx.close()
})

test('between sessions the stream’s card counts down to the next', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL })
  const page = await ctx.newPage()
  await serve(page, 'eventBetween')
  await page.goto('/', { waitUntil: 'networkidle' })
  await expect(page.getByText('Next session in')).toBeVisible()
  await expect(page.getByText('3 h 12 min')).toBeVisible()
  await expect(page.getByText('Live now')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Watch live' })).toHaveCount(0)
  await expect(page.locator('iframe')).toHaveCount(0)
  await ctx.close()
})

test.describe('the Public page group in a server’s Settings', () => {
  for (const [size, device] of Object.entries(sizes)) {
    test(`at ${size} size: both switches, the page’s link, and a port another program uses`, async ({ browser, baseURL }) => {
      const ctx = await browser.newContext({ baseURL, ...device })
      const page = await ctx.newPage()
      const { unexpected } = await fakePanel(page, new FakeConsole())
      const reads = settingsReads()
      const view = { enabled: true, players: false, host: 'alex.playkeeper.me', ports: { https: { port: 443, state: 'open' }, http: { port: 80, state: 'open' } } }
      let current: unknown = view
      await page.route('**/api/**', (route) => {
        const url = new URL(route.request().url())
        if (/\/public-page$/.test(url.pathname) && route.request().method() === 'GET') return route.fulfill({ json: current })
        const hit = route.request().method() === 'GET' ? reads[url.pathname] : undefined
        return hit === undefined ? route.fallback() : route.fulfill({ json: hit })
      })
      await page.goto('/servers/survival/settings#page')
      const group = page.locator('#page')
      await expect(group.getByRole('switch', { name: 'Show this server' })).toBeChecked()
      await expect(group.getByRole('switch', { name: 'Show who’s playing' })).not.toBeChecked()
      await expect(group.getByRole('link', { name: /alex\.playkeeper\.me/ })).toHaveAttribute('href', 'https://alex.playkeeper.me')
      await axe(page, `Settings at ${size}`)
      if (shots) {
        mkdirSync(shots, { recursive: true })
        await group.scrollIntoViewIfNeeded()
        await group.screenshot({ path: join(shots, `settings-${size}.png`) })
      }
      current = { ...view, ports: { https: { port: 443, state: 'busy', holder: 'nginx' }, http: { port: 80, state: 'busy', holder: 'nginx' } } }
      await page.reload()
      await expect(group.getByText('nginx uses port 443, so Playkeeper leaves it alone.')).toBeVisible()
      await expect(group.getByRole('button', { name: 'Try again' })).toBeVisible()
      if (shots) {
        await group.scrollIntoViewIfNeeded()
        await group.screenshot({ path: join(shots, `settings-port-busy-${size}.png`) })
      }
      expect(unexpected.filter((k) => !k.includes('/public-page')), 'API calls the fake panel does not answer').toEqual([])
      await ctx.close()
    })
  }

  test('the page’s words and stream are saved, a link that isn’t a stream says why, and the board comes off', async ({ browser, baseURL }) => {
    const ctx = await browser.newContext({ baseURL })
    const page = await ctx.newPage()
    const { unexpected } = await fakePanel(page, new FakeConsole())
    const reads = settingsReads()
    let current: Record<string, unknown> = {
      enabled: true,
      players: false,
      about: '',
      stream: '',
      host: 'alex.playkeeper.me',
      board: { headline: 'Day 3 · Nether reached', live: true, updatedAt: new Date(Date.now() - 5 * 60_000).toISOString() },
      ports: { https: { port: 443, state: 'open' }, http: { port: 80, state: 'open' } },
    }
    const posted: unknown[] = []
    let cleared = 0
    await page.route('**/api/**', (route) => {
      const req = route.request()
      const url = new URL(req.url())
      if (/\/public-page$/.test(url.pathname)) {
        if (req.method() === 'GET') return route.fulfill({ json: current })
        const body = req.postDataJSON() as { about?: string; stream?: string }
        posted.push(body)
        const login = body.stream?.match(/^(?:https?:\/\/)?(?:www\.)?twitch\.tv\/(\w{4,25})$/)?.[1]
        if (body.stream !== undefined && !login) {
          return route.fulfill({ status: 400, json: { error: 'That isn’t a Twitch or YouTube channel link.', code: 'invalid_request', hint: 'Paste your Twitch channel (twitch.tv/yourname).' } })
        }
        current = { ...current, ...body, ...(login ? { stream: `https://www.twitch.tv/${login.toLowerCase()}` } : {}) }
        return route.fulfill({ json: current })
      }
      if (/\/public-page\/board$/.test(url.pathname) && req.method() === 'DELETE') {
        cleared++
        current = { ...current, board: undefined }
        return route.fulfill({ json: { cleared: true } })
      }
      const hit = req.method() === 'GET' ? reads[url.pathname] : undefined
      return hit === undefined ? route.fallback() : route.fulfill({ json: hit })
    })
    await page.goto('/servers/survival/settings#page')
    const group = page.locator('#page')
    const saveAbout = group.getByRole('button', { name: 'Save About' })
    const saveStream = group.getByRole('button', { name: 'Save stream' })
    await expect(saveAbout).toBeVisible()
    await expect(saveStream).toBeVisible()
    // Each Save says what it saves. A bare "Save [disabled]" is the file
    // editor's, and the click-through counts a control once, on the first
    // page that has it, so the editor on a phone fell under its minimum.
    await expect(group.getByRole('button', { name: 'Save', exact: true })).toHaveCount(0)

    await group.getByRole('textbox', { name: 'About' }).fill('Be kind.\nUnofficial. Not affiliated with Anthropic or Mojang.')
    await expect(group.getByText('61/600')).toBeVisible()
    await saveAbout.click()
    await expect(page.getByText('Saved to Survival’s public page')).toBeVisible()
    expect(posted).toEqual([{ about: 'Be kind.\nUnofficial. Not affiliated with Anthropic or Mojang.' }])

    const link = group.getByRole('textbox', { name: 'Live stream' })
    await link.fill('https://evil.example/claude')
    await saveStream.click()
    await expect(group.getByRole('alert')).toContainText('That isn’t a Twitch or YouTube channel link.')
    await link.fill('twitch.tv/Example_Channel')
    await expect(group.getByRole('alert')).toHaveCount(0)
    await saveStream.click()
    await expect(link).toHaveValue('https://www.twitch.tv/example_channel')

    await expect(group.getByText('“Day 3 · Nether reached”, posted 5 min ago.')).toBeVisible()
    await group.getByRole('button', { name: 'Take it off' }).click()
    await expect(page.getByText('The status board is off Survival’s page')).toBeVisible()
    await expect(group.getByText(/Your tools can post a headline/)).toBeVisible()
    expect(cleared).toBe(1)
    expect(unexpected.filter((k) => !k.includes('/public-page')), 'API calls the fake panel does not answer').toEqual([])
    await ctx.close()
  })
})
