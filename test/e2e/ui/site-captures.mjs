// Screenshots for playkeeper.io (site/static/shots), taken from the dashboard
// itself: the live demo built from <web-dir> (a checkout of the release the
// site describes), with its sample servers. A few screens need sample data
// the demo doesn't have, so the captures add it: the live map drawn (map
// tiles from site-map.mjs), the modpack library and voice chat from the
// recorded fixtures (fixtures/), sharing a modded server's pack and the page
// friends open. The build is the demo's own with two changes: the demo's
// panel asks the captures first, and New server offers modpacks and
// templates, as the dashboard does.
//
// Each is taken at 2 to 4 times its pixels, wide enough for the widest file
// site/tools/shots.py makes of it. What only the demo shows (its line "Live
// demo · resets every hour", its sample world) is hidden, and its made-up
// address reads as the site's example, alex.playkeeper.me. The sizing
// guide's answer comes from the site itself, at <site-url>.
//
// Usage, from test/e2e/ui:
//   node site-captures.mjs <web-dir> <site-url> <out-dir> [name...]
// for example, for the 0.4.0 release:
//   git worktree add /tmp/pk-0.4.0 v0.4.0 && (cd /tmp/pk-0.4.0/web && npm ci)
//   go run ./cmd/site -serve 127.0.0.1:8080 &
//   node site-captures.mjs /tmp/pk-0.4.0/web http://127.0.0.1:8080 /tmp/captures
import { chromium } from '@playwright/test'
import fs from 'node:fs'
import http from 'node:http'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { mapTile, mapZoom, tileSize } from './site-map.mjs'

const [web, site, out, ...only] = process.argv.slice(2)
if (!web || !site || !out) {
  console.error('usage: node site-captures.mjs <web-dir> <site-url> <out-dir> [name...]')
  process.exit(2)
}

const here = path.dirname(fileURLToPath(import.meta.url))
const phone = { width: 390, height: 844 }
const desktop = { width: 1280, height: 800 }
// The Overview's join address card breaks an address anywhere to fit; from
// 1320 pixels wide survival.alex.playkeeper.me fits on one line. Same shape
// as desktop, which the landing page's product loop is framed for.
const wide = { width: 1344, height: 840 }
const survival = 'h4k8v2m9qa'
const packToken = 'Pk7Friends0Pack0Link0A'

// The demo's panel answers from the captures' sample data first. `answer` is
// the one function the demo's API client calls (web/src/demo/client.ts).
const answerFn = 'export async function answer('
const hook = `
/** The site captures' sample data first (test/e2e/ui/site-captures.mjs), then the demo's. */
export async function answer(method: string, path: string, body?: unknown, raw?: Blob): Promise<unknown> {
  const extra = (globalThis as unknown as { pkCaptures?: { before(m: string, p: string, b: unknown, demo: (m: string, p: string) => Promise<unknown>): unknown; after(m: string, p: string, r: unknown): unknown } }).pkCaptures
  const shown = await extra?.before(method, path, body, (m, p) => demoAnswer(m, p))
  if (shown !== undefined) return structuredClone(shown)
  const result = await demoAnswer(method, path, body, raw)
  return extra ? extra.after(method, path, result) : result
}
`

// The demo leaves out starting a server from a modpack or a template, which
// it has no data for (web/src/demo/parts.tsx); the dashboard offers both.
const noTemplates = 'templates: false'

async function buildDemo(dist) {
  const { build } = await import(pathToFileURL(path.join(web, 'node_modules/vite/dist/node/index.js')).href)
  const engine = path.join(web, 'src/demo/engine.ts')
  const parts = path.join(web, 'src/demo/parts.tsx')
  await build({
    root: web,
    configFile: path.join(web, 'vite.config.ts'),
    mode: 'demo',
    logLevel: 'warn',
    build: { outDir: dist, emptyOutDir: true },
    plugins: [
      {
        name: 'site-captures',
        enforce: 'pre',
        transform(code, id) {
          const file = id.split('?')[0]
          if (file === engine) {
            if (!code.includes(answerFn)) this.error(`${engine} has no "${answerFn}" for the captures to answer from`)
            return code.replace(answerFn, 'async function demoAnswer(') + hook
          }
          if (file === parts) {
            if (!code.includes(noTemplates)) this.error(`${parts} has no "${noTemplates}" to switch on`)
            return code.replace(noTemplates, 'templates: true')
          }
          return null
        },
      },
    ],
  })
}

const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2', '.json': 'application/json' }

/** Serves the demo at /demo/ as the site's nginx does: a path that isn't a file is one of its pages. */
function serve(dist) {
  const server = http.createServer((req, res) => {
    const u = new URL(req.url ?? '/', 'http://demo')
    if (!u.pathname.startsWith('/demo/')) {
      res.writeHead(404).end()
      return
    }
    let file = path.join(dist, decodeURIComponent(u.pathname.slice('/demo/'.length)))
    if (!file.startsWith(dist) || !fs.existsSync(file) || fs.statSync(file).isDirectory()) file = path.join(dist, 'index.html')
    res.writeHead(200, { 'Content-Type': types[path.extname(file)] ?? 'application/octet-stream' })
    fs.createReadStream(file).pipe(res)
  })
  return new Promise((resolve) => server.listen(0, '127.0.0.1', () => resolve(server)))
}

const fixtures = (p) => JSON.parse(fs.readFileSync(path.join(here, 'fixtures', p), 'utf8'))
const ago = (ms) => new Date(Date.now() - ms).toISOString()

/** The sample data the demo doesn't have, for the page (see pkCaptures). */
function sampleData() {
  const modrinth = fixtures('modpacks/modrinth.json')
  // The library's most downloaded packs as recorded, less those named after
  // other companies' games, which the site doesn't show.
  const listed = ['5FFgwNNP', '1ocGzRHv', '8yKzGJOG', 'C9FChyZ3', 'q9aTk4Re', 'yAkEipq1']
  const voice = fixtures('addons/details.json')['modrinth:9eGKb6K1']
  const curated = fixtures('addons/curated.json').find((c) => c.id === 'voice-chat')
  const world = (name, dimension, label) => ({ name, dimension, label, spawn: { x: 0, z: 0 }, zoom: mapZoom, refreshSeconds: 60 })
  const player = (name, uuid, x, z, kind, text) => ({ name, uuid, world: 'world', dimension: 'overworld', x, z, place: { kind, text } })
  return {
    survival,
    map: {
      info: { supported: true, enabled: true, state: 'ready', message: 'The map is up to date', areas: 4800, bytes: 190_000_000, lastDrawn: ago(8 * 60_000), plugin: 'squaremap', pluginVersion: '1.3.4', estimatedMinutes: 10, estimatedMegabytes: 200, public: false, publicPlayers: false, path: '', restartWhenEmpty: false },
      worlds: { worlds: [world('world', 'overworld', 'Overworld'), world('world_nether', 'nether', 'Nether'), world('world_the_end', 'end', 'The End')], tileSize },
      players: {
        players: [
          player('JunoFox', '3f2b8c1e-6d4a-4e5f-9a7b-1c2d3e4f5a6b', -3, -47, 'near_spawn', 'Near spawn'),
          player('tobi2009', '7a1c9e2d-4b3f-4c5e-8d6f-2e3f4a5b6c7d', 14, 25, 'near_spawn', 'Near spawn'),
          player('mara_k', 'c4d5e6f7-1a2b-4c3d-9e8f-3a4b5c6d7e8f', 24, -10, 'near_spawn', 'Near spawn'),
        ],
      },
    },
    modpacks: {
      total: modrinth.searches.downloads.total,
      cards: listed.map((id) => modrinth.cards[id]),
      details: modrinth.details,
      previews: modrinth.previews,
    },
    voice: {
      pick: { id: 'voice-chat', card: voice.card, permission: curated.permission, ports: curated.ports },
      details: { ...voice, ports: curated.ports },
    },
  }
}

/** Runs in the page before the dashboard: answers what the demo has no data for, and adds to what it has. */
function install({ data, voiceChat, packToken }) {
  localStorage.setItem('playkeeper-demo-welcomed', '1')
  localStorage.setItem('playkeeper-demo-prompted', '1')
  const now = () => new Date().toISOString()
  const shared = new Set()
  window.pkCaptures = {
    before(method, where, body, demo) {
      const url = new URL(where, 'http://panel')
      const p = url.pathname
      // Sharing a modded server's pack page, which the demo doesn't do.
      let m = /^\/api\/servers\/(\w+)\/mods\/share$/.exec(p)
      if (m && method === 'POST' && body && typeof body.public === 'boolean') {
        if (body.public) shared.add(m[1])
        else shared.delete(m[1])
        return demo('GET', where).then((s) => (body.public ? { ...s, public: true, token: packToken } : s))
      }
      if (m && method === 'GET' && shared.has(m[1])) return demo('GET', where).then((s) => ({ ...s, public: true, token: packToken }))
      if (method !== 'GET') return undefined
      m = /^\/api\/servers\/(\w+)\/map(?:\/(worlds|players))?$/.exec(p)
      if (m && m[1] === data.survival) return m[2] === 'players' ? { ...data.map.players, updatedAt: now() } : m[2] ? data.map.worlds : { ...data.map.info, checkedAt: now() }
      // CurseForge is offered too, as the release's built-in key does.
      if (/^\/api\/machines\/\w+\/modpacks$/.test(p)) {
        const limit = Number(url.searchParams.get('limit') || 20)
        return { cards: data.modpacks.cards.slice(0, limit), total: data.modpacks.total, offset: 0, limit, sources: ['modrinth', 'curseforge'] }
      }
      m = /^\/api\/machines\/\w+\/modpacks\/modrinth\/([^/]+)(?:\/versions\/([^/]+)\/preview)?$/.exec(p)
      if (m) {
        const project = decodeURIComponent(m[1])
        const d = data.modpacks.details[project] ?? Object.values(data.modpacks.details).find((x) => x.slug === project)
        if (!d) return undefined
        return m[2] ? data.modpacks.previews[`${d.projectId}/${decodeURIComponent(m[2])}`] : d
      }
      if (voiceChat && /^\/api\/servers\/\w+\/addons\/project\/modrinth\/9eGKb6K1$/.test(p)) return data.voice.details
      return undefined
    },
    after(method, where, result) {
      if (voiceChat && method === 'GET' && /^\/api\/servers\/\w+\/addons\/curated$/.test(new URL(where, 'http://panel').pathname) && result && Array.isArray(result.picks)) {
        return { ...result, picks: [data.voice.pick, ...result.picks.filter((x) => x.id !== 'voice-chat')] }
      }
      return result
    },
  }
}

/** The page friends open from a modded server's pack link (GET /packs/<token>/page), for the Cobblemon server. */
function packPage() {
  const text = (key, english, params) => ({ key, text: english, params })
  const needed = text('share.need.required', 'Friends need it')
  const optional = text('share.need.optional', 'Optional for friends')
  const file = 'cobblemon.mrpack'
  const launcher = (id, name, siteUrl, steps) => ({ id, name, site: siteUrl, steps: steps.map((k) => text(`share.launcher.${id.replace('-', '_')}.${k}`, '', { file })) })
  return {
    server: 'Cobblemon',
    minecraftVersion: '1.21.1',
    loader: 'fabric',
    loaderName: 'Fabric',
    loaderVersion: '0.19.5',
    pack: { name: 'Cobblemon Official Modpack [Fabric]', version: '1.8.1', source: 'modrinth', page: 'https://modrinth.com/modpack/cobblemon-fabric', need: 'required', label: needed },
    notice: text('share.notice.pack_one', 'Friends need the pack plus Waystones', { pack: 'Cobblemon Official Modpack [Fabric]', mod: 'Waystones' }),
    steps: [],
    launchers: [launcher('modrinth-app', 'Modrinth App', 'https://modrinth.com/app', ['add', 'pick', 'play']), launcher('prism', 'Prism Launcher', 'https://prismlauncher.org', ['add', 'pick', 'launch'])],
    mods: [
      { name: 'Balm', version: '21.0.20', from: 'user', need: 'required', label: needed, inFile: true, neededBy: 'Waystones' },
      { name: 'Waystones', version: '21.1.4', from: 'user', need: 'required', label: needed, inFile: true },
      { name: 'Chunky', version: '1.4.40', from: 'user', need: 'optional', label: optional, inFile: true },
      { name: 'Cobblemon', version: '1.7.1', from: 'pack', need: 'required', label: needed, inFile: true },
      { name: 'Sodium', version: '0.9.2', from: 'pack', need: 'optional', label: optional, inFile: true },
    ],
    yourself: [],
    download: { url: `/packs/${packToken}/${file}`, name: file, size: 38_912, type: 'application/x-modrinth-modpack+zip' },
    address: 'cobblemon.alex.playkeeper.me',
    hasIcon: false,
  }
}

function iconNumber(s) {
  let h = 2166136261
  for (const ch of s) h = Math.imul(h ^ ch.charCodeAt(0), 16777619) >>> 0
  return h % 16
}

/** The demo's sample players, whose faces are the demo's drawings 0, 1, 2… (web/src/demo/data.ts, faceOf). */
const samplePlayers = ['JunoFox', 'tobi2009', 'mara_k', 'PixelPia', 'Brickbert', 'Kestrel_7']

/** What the pages fetch themselves rather than through the API client: map tiles and faces, modpack icons and the pack page. Nothing leaves the machine. */
async function routes(ctx, base, dist) {
  const origin = new URL(base).origin
  await ctx.route(
    () => true,
    async (route) => {
      const u = new URL(route.request().url())
      if (u.origin !== origin) return route.fulfill({ status: 404, body: '' })
      const tile = /^\/api\/servers\/\w+\/map\/tiles\/([^/]+)\/(\d+)\/(-?\d+)_(-?\d+)\.png$/.exec(u.pathname)
      if (tile) {
        const png = mapTile(decodeURIComponent(tile[1]), Number(tile[2]), Number(tile[3]), Number(tile[4]))
        return png ? route.fulfill({ status: 200, contentType: 'image/png', body: png }) : route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"Not drawn yet.","code":"not_found"}' })
      }
      const head = /^\/api\/players\/([^/]+)\/head$/.exec(u.pathname)
      if (head && samplePlayers.includes(decodeURIComponent(head[1]))) {
        return route.fulfill({ status: 200, contentType: 'image/svg+xml', body: fs.readFileSync(path.join(dist, 'faces', `${samplePlayers.indexOf(decodeURIComponent(head[1]))}.svg`)) })
      }
      if (/^\/api\/machines\/\w+\/modpacks\/icon$/.test(u.pathname)) {
        return route.fulfill({ status: 200, contentType: 'image/svg+xml', body: fs.readFileSync(path.join(dist, 'icons', `${iconNumber(u.searchParams.get('url') ?? '')}.svg`)) })
      }
      if (u.pathname === `/packs/${packToken}/page`) return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(packPage()) })
      if (u.pathname.startsWith('/demo/')) return route.continue()
      return route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"Not in the captures.","code":"not_found"}' })
    },
  )
}

/** An open dialog or sheet; the demo's toasts are dialogs too, without a slot. */
const popup = '[role=dialog][data-slot]'

// Steps some shots take first.

/** Starts a stopped modded server, as the Mods tab asks, so its screens show it running. */
async function startServer(page) {
  const start = page.getByRole('button', { name: /^Start again$/ })
  if (await start.count()) {
    await start.first().click()
    await page.getByText('Online', { exact: true }).first().waitFor({ timeout: 30_000 })
  }
}

/** Zooms the live map in on spawn with its + key, as far as it goes, so blocks are a few pixels each. */
async function zoomMap(page) {
  const map = page.locator('div[tabindex="0"]:has(> canvas)').first()
  await map.waitFor()
  await map.focus()
  for (let i = 0; i < mapZoom.extra; i++) {
    await page.keyboard.press('+')
    await page.waitForTimeout(400)
  }
  await map.blur()
  // Every tile in view drawn.
  await page.waitForTimeout(1500)
}

/** New server's steps with what it picks, then the new server setting up, ms milliseconds in. */
function createServer(ms) {
  return async (page) => {
    for (let step = 0; step < 8; step++) {
      const next = page.getByRole('button', { name: /^(Continue to|Create and start)/ }).filter({ visible: true })
      if (!(await next.count())) break
      const eula = page.getByRole('checkbox', { name: /I accept/ })
      if ((await eula.count()) && !(await eula.first().isChecked())) await eula.first().click()
      await next.first().click({ timeout: 10_000 })
      await page.waitForTimeout(700)
    }
    await page.getByText(/^Setting up /).first().waitFor({ timeout: 15_000 })
    await page.waitForTimeout(ms)
  }
}

/** New server, starting from a modpack: the library, then (open) the first pack's details. */
function modpacks(open) {
  return async (page) => {
    await page.getByRole('button', { name: /^(A )?modpack$/i }).click()
    await page.getByText('Cobblemon Official Modpack [Fabric]').first().waitFor()
    await page.waitForTimeout(600)
    if (open) {
      await page.getByText('Cobblemon Official Modpack [Fabric]').first().click()
      await page.locator(popup).waitFor()
      await page.waitForTimeout(1200)
    }
  }
}

/** New server, starting from a world that's on Aternos: its steps, then (file) the world's upload, ms milliseconds in, or (check) what's in it. */
function worldFromAternos({ file = false, ms = 0, check = false } = {}) {
  return async (page) => {
    await page.getByRole('button', { name: /^(A )?world$/i }).click()
    // Phones pick where the world is from a list; wider screens show the choices.
    const where = page.getByRole('button', { name: 'Where is it now?' })
    if (await where.count()) {
      await where.click()
      await page.getByRole('option', { name: 'Aternos' }).click()
    } else {
      await page.getByText('Aternos', { exact: true }).first().click()
    }
    await page.waitForTimeout(500)
    if (!file) return
    const zip = path.join(os.tmpdir(), 'site-captures', 'Survival-2024.zip')
    fs.mkdirSync(path.dirname(zip), { recursive: true })
    // A sparse file: the demo's upload counts bytes it never reads.
    fs.closeSync(fs.openSync(zip, 'w'))
    fs.truncateSync(zip, 2_468_000_000)
    await page.locator('input[type=file]').first().setInputFiles(zip)
    if (!check) {
      await page.waitForTimeout(ms)
      return
    }
    await page.getByRole('button', { name: /^Check the world/ }).click({ timeout: 20_000 })
    await page.getByText(/what's inside|what’s inside/i).first().waitFor({ timeout: 20_000 })
    await page.waitForTimeout(800)
  }
}

/** The Mods tab's Share with friends, open, and (link) sharing its page. */
function share(link) {
  return async (page) => {
    await startServer(page)
    await page.getByRole('button', { name: /Share with friends/ }).first().click()
    const dialog = page.getByRole('dialog', { name: 'Send friends what they need' })
    await dialog.waitFor()
    if (link) await dialog.getByRole('button', { name: 'Share a link' }).click()
    await page.waitForTimeout(1200)
  }
}

/** Voice chat from Picked by Playkeeper, asking about its port. */
async function voiceChat(page) {
  await page.getByRole('button', { name: 'Install Simple Voice Chat' }).click()
  await page.getByRole('dialog', { name: 'Add proximity voice chat' }).waitFor()
  await page.getByText(/UDP 24454/).first().waitFor()
  await page.waitForTimeout(600)
}

// Each shot: the page, its size and pixel ratio, what to do first, and the
// part to keep: the top of the screen (top: its height in CSS pixels), the
// cards whose headings are given (and what's between them), an element (the
// open dialog), the dashboard's main panel without its sidebar, or the whole
// view.
const shots = [
  // The landing page's product loop and live demo window: the whole view.
  { name: 'loop-overview', route: '/servers/survival', size: wide, dpr: 2 },
  { name: 'loop-new-server', route: '/servers/new', size: wide, dpr: 2 },
  { name: 'loop-setting-up', route: '/servers/new', size: wide, dpr: 2, before: createServer(900) },
  { name: 'loop-setting-up-2', route: '/servers/new', size: wide, dpr: 2, before: createServer(7000) },
  { name: 'demo-home', route: '/', size: desktop, dpr: 2, keepDemo: true },
  // Phones: the hero's live map, whole, and the top of a screen for each feature card.
  { name: 'hero-map-phone', route: '/servers/survival/map', size: phone, dpr: 3, before: zoomMap },
  { name: 'feat-types', route: '/servers/new', size: phone, dpr: 3, top: 300 },
  { name: 'feat-address', route: '/machines/q7m2vk9xpd/settings', size: phone, dpr: 3, top: 300 },
  { name: 'feat-backups', route: '/servers/survival/world', size: phone, dpr: 3, top: 300 },
  { name: 'feat-crash', route: '/servers/cobblemon', size: phone, dpr: 3, top: 300 },
  { name: 'feat-friends', route: '/servers/survival/players', size: phone, dpr: 3, top: 300 },
  { name: 'feat-map', route: '/servers/survival/map', size: phone, dpr: 3, top: 300, before: zoomMap },
  { name: 'feat-automation', route: '/servers/survival/settings/schedules', size: phone, dpr: 3, top: 300 },
  { name: 'feat-agents', route: '/settings/ai-agents', size: phone, dpr: 3, top: 300 },
  // Browser windows: the main panel from its top.
  { name: 'feature-mods', route: '/servers/cobblemon/mods', size: desktop, dpr: 2, panel: 560, before: startServer },
  { name: 'ptero-hero', route: '/servers/survival', size: wide, dpr: 2, panel: 560 },
  { name: 'ptero-world', route: '/servers/survival/world', size: desktop, dpr: 2, panel: 560 },
  // Crops of one part of a screen, from its heading.
  { name: 'post-backups', route: '/servers/survival/world', size: desktop, dpr: 3, cards: ['Make a backup', 'World'] },
  { name: 'post-address', route: '/servers/survival', size: desktop, dpr: 3, cards: ['Join address', 'Playing now'] },
  { name: 'post-mods', route: '/servers/cobblemon/mods', size: desktop, dpr: 3, cards: ['Fabric API', 'Lithium'], from: 'Friends need', before: startServer },
  { name: 'guide-mods-tab', route: '/servers/cobblemon/mods', size: desktop, dpr: 3, cards: ['Fabric API', 'Lithium'], from: 'Friends need', before: startServer },
  { name: 'guide-crash', route: '/servers/cobblemon', size: desktop, dpr: 3, cards: ['What happened', 'How to fix it'] },
  { name: 'guide-address', route: '/servers/survival', size: wide, dpr: 3, cards: ['Join address', 'Playing now'] },
  { name: 'guide-invite', route: '/servers/survival/players', size: { width: 1280, height: 1000 }, dpr: 3, cards: ['School friends'], from: 'Invite links' },
  { name: 'guide-files', route: '/servers/cobblemon/files/mods', size: desktop, dpr: 3, panel: 444, before: startServer },
  { name: 'detail-browse', route: '/servers/survival/plugins/browse', size: desktop, dpr: 3, cards: ['CoreProtect', 'Chunky', 'ViaVersion'], from: 'Picked by Playkeeper' },
  { name: 'detail-updates', route: '/servers/survival/plugins', size: desktop, dpr: 3, cards: ['BlueMap', 'ViaVersion'], from: 'Plugins on Survival' },
  { name: 'step-type', route: '/servers/new', size: desktop, dpr: 3, element: '[role=radiogroup][aria-label="Server type"]', height: 300 },
  { name: 'tool-server-list', route: '/servers/survival/settings', size: desktop, dpr: 3, before: serverList, element: '#list' },
  { name: 'tool-plugin-config', route: '/servers/survival/file/plugins/FriendsWelcome/config.yml', size: desktop, dpr: 3, area: ['FriendsWelcome', 610, 206, -153, -6] },
  // Dialogs and pages the demo has no data for.
  { name: 'detail-voice', route: '/servers/survival/plugins/browse', size: desktop, dpr: 4, voiceChat: true, before: voiceChat, element: popup },
  { name: 'detail-modpack', route: '/servers/new', size: desktop, dpr: 4, before: modpacks(true), element: popup, height: 520, left: 180 },
  { name: 'step-modpack', route: '/servers/new', size: desktop, dpr: 3, before: modpacks(false), area: ['A modpack picks the type, version and mods for you.', 666, 333, 0, 15] },
  { name: 'guide-modpack-details', route: '/servers/new', size: desktop, dpr: 3, before: modpacks(true), element: popup, height: 520 },
  { name: 'guide-modpacks', route: '/servers/new', size: { width: 1280, height: 960 }, dpr: 3, before: modpacks(false), panel: 900 },
  { name: 'step-share', route: '/servers/cobblemon/mods', size: desktop, dpr: 3, before: share(false), element: popup, plain: true },
  { name: 'guide-share', route: '/servers/cobblemon/mods', size: desktop, dpr: 3, before: share(true), element: popup, height: 395, plain: true },
  { name: 'detail-pack', route: `/packs/${packToken}`, size: desktop, dpr: 3, area: ['Get the mods for Cobblemon', 800, 430] },
  { name: 'guide-pack-page', route: `/packs/${packToken}`, size: { width: 560, height: 900 }, dpr: 3, top: 373 },
  { name: 'move-aternos', route: '/servers/new', size: { width: 1280, height: 1000 }, dpr: 3, before: worldFromAternos({ file: true, ms: 3300 }), panel: 820 },
  { name: 'move-aternos-phone', route: '/servers/new', size: phone, dpr: 4, before: worldFromAternos(), top: 680 },
  { name: 'move-inside', route: '/servers/new', size: desktop, dpr: 3, before: worldFromAternos({ file: true, check: true }), panel: 730 },
]

/** Settings' Server list card in view, with the card above it hidden so its edge stays out of the crop. */
async function serverList(page) {
  await page.locator('#list').scrollIntoViewIfNeeded()
  await page.locator('#game').evaluate((el) => { el.style.visibility = 'hidden' })
}

// cardClip is the rectangle around the cards with the given headings, and
// from the element with the text "from" when there's one, with a margin.
async function cardClip(page, headings, from) {
  return page.evaluate(({ headings, from }) => {
    const all = [...document.querySelectorAll('body *')]
    const byText = (text) => all.find((el) => el.children.length === 0 && el.textContent.trim() === text && el.getBoundingClientRect().width > 0)
    const card = (el) => {
      for (let n = el; n && n !== document.body; n = n.parentElement) {
        const cs = getComputedStyle(n)
        if (parseFloat(cs.borderTopLeftRadius) >= 12 && cs.borderTopWidth !== '0px' && n.getBoundingClientRect().width > 160) return n
      }
      return el
    }
    const boxes = headings.map((h) => {
      const el = byText(h)
      if (!el) throw new Error('no ' + h)
      return card(el).getBoundingClientRect()
    })
    if (from) {
      const el = all.find((e) => e.children.length === 0 && e.textContent.trim().startsWith(from) && e.getBoundingClientRect().width > 0)
      if (!el) throw new Error('no ' + from)
      boxes.push(el.getBoundingClientRect())
    }
    const x = Math.min(...boxes.map((b) => b.left)) - 12
    const y = Math.min(...boxes.map((b) => b.top)) - 12
    const r = Math.max(...boxes.map((b) => b.right)) + 12
    const bottom = Math.max(...boxes.map((b) => b.bottom)) + 12
    return { x: Math.max(0, x), y: Math.max(0, y), width: r - Math.max(0, x), height: bottom - Math.max(0, y) }
  }, { headings, from })
}

/** The dashboard's main panel, without the sidebar, from the top. */
async function panelClip(page, height) {
  return page.evaluate((height) => {
    window.scrollTo(0, 0)
    const main = document.querySelector('main') || document.body
    const b = main.getBoundingClientRect()
    return { x: b.left, y: 0, width: Math.min(window.innerWidth, b.right + 8) - b.left, height: Math.min(height, window.innerHeight) }
  }, height)
}

/**
 * An element with a margin, inside the view: only its top `height` CSS
 * pixels when that's given, and `left` more of the page beside it, for a
 * sheet over the page it opened from.
 */
async function elementClip(page, selector, height, left = 0) {
  const box = await page.locator(selector).last().boundingBox()
  if (!box) throw new Error('no ' + selector)
  const m = 16
  const vw = page.viewportSize()
  const x = Math.max(0, box.x - m - left)
  const y = Math.max(0, box.y - m)
  const bottom = height ? y + height : box.y + box.height + m
  return { x, y, width: Math.min(vw.width, box.x + box.width + m) - x, height: Math.min(vw.height, bottom) - y }
}

/** A width × height area from the element with the given text, moved by dx and dy, for the small pictures of three steps. */
async function areaClip(page, [text, width, height, dx = 0, dy = 0]) {
  const box = await page.getByText(text, { exact: true }).filter({ visible: true }).first().boundingBox()
  if (!box) throw new Error('no ' + text)
  return { x: Math.max(0, box.x - 16 + dx), y: Math.max(0, box.y - 16 + dy), width, height }
}

// Hides what only the demo shows, and gives its made-up address the site's
// name. plain: an open dialog sits on the page's own colour rather than over
// the dimmed page, for a small picture.
async function dress(page, keepDemo, plain) {
  await page.evaluate(({ keep, plain }) => {
    if (plain) for (const el of document.querySelectorAll('[data-slot=dialog-backdrop]')) el.style.background = getComputedStyle(document.body).backgroundColor
    // A release from before free names moved to playkeeper.me names the demo
    // under playkeeper.io.
    const demoAddress = /demo\.playkeeper\.(io|me)/
    const demoAddresses = new RegExp(demoAddress.source, 'g')
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
    const texts = []
    while (walker.nextNode()) texts.push(walker.currentNode)
    for (const t of texts) {
      // The demo's own line, and its sample world for visitors without one.
      if (!keep && /Live demo · resets every hour|Try a sample world/.test(t.textContent)) {
        const line = t.parentElement.closest('p, div')
        if (line) line.style.display = 'none'
      }
      if (demoAddress.test(t.textContent)) t.textContent = t.textContent.replace(demoAddresses, 'alex.playkeeper.me')
    }
    for (const input of document.querySelectorAll('input')) {
      if (demoAddress.test(input.value)) input.value = input.value.replace(demoAddresses, 'alex.playkeeper.me')
    }
    // The demo's own toasts ("That was a demo start") stay out of the shots.
    for (const el of document.querySelectorAll('[data-demo-toast]')) {
      let n = el
      while (n && n !== document.body && getComputedStyle(n).position !== 'fixed') n = n.parentElement
      if (n && n !== document.body) n.style.display = 'none'
    }
  }, { keep: keepDemo, plain })
}

/** The sizing guide's answer for 5 to 10 friends on Vanilla or Paper, from the site. */
async function sizingAnswer(browser) {
  // At this width the answer sits beside the questions, at its narrowest.
  const ctx = await browser.newContext({ viewport: { width: 1024, height: 900 }, deviceScaleFactor: 3, locale: 'en-GB', timezoneId: 'UTC', reducedMotion: 'reduce' })
  const page = await ctx.newPage()
  await page.goto(new URL('/sizing', site).href, { waitUntil: 'networkidle' })
  const answer = page.locator('.sizing .answer').first()
  await answer.scrollIntoViewIfNeeded()
  await page.waitForTimeout(400)
  // Its heading and the first reasons, in the shape of a step's picture.
  const box = await answer.boundingBox()
  const file = path.join(out, 'step-size.png')
  await page.screenshot({ path: file, clip: { x: box.x, y: box.y, width: box.width, height: Math.min(box.height, Math.round(box.width / 2)) }, animations: 'disabled' })
  console.log(file)
  await ctx.close()
}

fs.mkdirSync(out, { recursive: true })
const dist = fs.mkdtempSync(path.join(os.tmpdir(), 'site-captures-demo-'))
await buildDemo(dist)
const server = await serve(dist)
const base = `http://127.0.0.1:${server.address().port}/demo`
const data = sampleData()
const browser = await chromium.launch()
try {
  for (const shot of shots.filter((s) => !only.length || only.includes(s.name))) {
    const isPhone = shot.size === phone
    const ctx = await browser.newContext({ viewport: shot.size, deviceScaleFactor: shot.dpr, isMobile: isPhone, hasTouch: isPhone, locale: 'en-GB', timezoneId: 'UTC', reducedMotion: 'reduce' })
    await routes(ctx, base, dist)
    await ctx.addInitScript(install, { data, voiceChat: !!shot.voiceChat, packToken })
    const page = await ctx.newPage()
    await page.goto(base + shot.route, { waitUntil: 'networkidle' })
    await page.waitForTimeout(600)
    if (shot.before) {
      await shot.before(page).catch(async (err) => {
        await page.screenshot({ path: path.join(out, `_failed-${shot.name}.png`), fullPage: true })
        throw err
      })
    }
    await page.waitForTimeout(600)
    await dress(page, shot.keepDemo, shot.plain)
    await page.waitForTimeout(200)
    let clip
    if (shot.top) clip = { x: 0, y: 0, width: shot.size.width, height: shot.top }
    if (shot.cards) clip = await cardClip(page, shot.cards, shot.from)
    if (shot.panel) clip = await panelClip(page, shot.panel)
    if (shot.element) clip = await elementClip(page, shot.element, shot.height, shot.left)
    if (shot.area) clip = await areaClip(page, shot.area)
    const file = path.join(out, `${shot.name}.png`)
    await page.screenshot({ path: file, clip, animations: 'disabled' })
    if (process.env.PK_CAPTURE_DEBUG) await page.screenshot({ path: path.join(out, `_view-${shot.name}.png`), fullPage: true, animations: 'disabled' })
    console.log(file)
    await ctx.close()
  }
  if (!only.length || only.includes('step-size')) await sizingAnswer(browser)
} finally {
  await browser.close()
  server.close()
  fs.rmSync(dist, { recursive: true, force: true })
}
