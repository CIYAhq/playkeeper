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

const pages = {
  event: { address: 'ai.playkeeper.me', servers: [event] },
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
})
