import { expect, test, type Page } from '@playwright/test'
import fs from 'node:fs'
import http from 'node:http'
import type { AddressInfo } from 'node:net'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { answerRead, installJob, isAddonRead, recordedFolder, updateJob, type World } from './addon-fixtures'
import { apiReach, calls, goDecls, webCalls } from './clickthrough-api'
import {
  closure,
  costOf,
  forPullRequest,
  freshSetup,
  importGraph,
  keysChanged,
  named,
  pageMapProblems,
  pageModules,
  pageOf,
  partition,
  preludesOf,
  pullRequestRunners,
  pullRequestShardSeconds,
  pullRequestShare,
  selects,
  shardsFor,
  type Costs,
  type CrawlerName,
  type Selection,
  type Size,
  type Unit,
} from './clickthrough-plan'
import { at, found, gate, ownProblems, places, type Negative, type Pressed, type Rules, type ShardReport } from './clickthrough-rules'
import { Crawler, failing, type Result } from './crawl'
import { installPageHelpers } from './crawl-page'
import { addonJobMs, addonOpAt, lay, type View } from './fakes'
import { login, outDir } from './helpers'
import { answerModpackRead, isModpackRead, type PackWorld } from './modpack-fixtures'
import { answerBuildsRead, isRecordedBuildsRead, layRecordedCatalog, recordedTypes } from './software-fixtures'

// Every control works. The click-through opens every page of the seeded
// dashboard at desktop and phone sizes, finds every button, link, switch, tab,
// slider, menu item and list option by role (including the ones inside menus,
// dialogs and sheets, in a dialog that replaces the menu it came from, and in
// later steps of a form), presses each one and checks that something a person
// could notice happened: the page changed, a dialog, menu or sheet opened or
// closed, the control's own state changed, focus moved, the page scrolled,
// something was copied, a toast appeared or a request went out. It fills in
// forms first, typing the phrase a typed confirmation asks for.
//
// A fresh install has one running server with players and backups, so the
// pages that change are crawled again in states it isn't in, laid over the
// panel's real answers (View in fakes.ts): the server stopped, crashed and
// busy, no players or backups, no servers at all (Home's empty page and
// /welcome), a Playkeeper update to install, space to free on the machine's
// disk, first-run setup, set up to look after itself or asleep, and the
// states the other views in fakes.ts describe.
//
// The sign-in page, first-run setup, a friends' pack link that opens nothing
// and the shared map pages (a shared map, and a link no map has) are opened
// signed out. A world file picker gets a small archive, so the world upload's
// later steps are pressed too.
//
// Writes go to realistic fakes (fakes.ts), so nothing is restarted, deleted or
// downloaded. A server's add-on reads (its folder, the library and its picks,
// details, plans and icons) and the create flow's modpack reads (the library,
// a pack's details and versions, a version's preview and icons) go to
// recorded fixtures (addon-fixtures.ts, modpack-fixtures.ts), so the crawl
// never waits on Modrinth, Hangar or CurseForge. The only real write is one
// AI agent token, made before the crawl so Settings › AI agents has a token
// to open and revoke (revoking goes to the fakes too). There is no list of
// exceptions: a control that should do nothing right now must be disabled
// and say why
// (aria-describedby or a title). The selected tab or option of a group may
// stay selected. A link another app opens (an authenticator's otpauth:,
// mailto:, tel:) counts as working, since a headless browser has no app to
// open. Each page gets a fresh load before a control is pressed unless the
// page is provably unchanged.
//
// It fails when a control does nothing visible, answers with an error, is
// disabled without a reason or has no name; when it can't get back to a state
// it found; when a page has fewer controls than its minimum; when an add-on or
// modpack read reaches the panel or has no recorded answer; and when it didn't press
// the control of one of `places`. For each place, a negative control then
// breaks that control and presses it again: the crawl must report it.
//
// CI splits a run between runners (PK_SHARD=k/n): each works out the run's
// pages from the panel alike, crawls its share (clickthrough-plan.ts keeps a
// page's faked states with it) and writes its report, and the gate
// (clickthrough-gate.spec.ts) checks that together they crawled every page,
// counting each page's controls in the order of a run on one runner. On pull
// requests PK_SELECTION names the pages a change touches (plan.ts). Run on one
// runner, as locally, it checks the gate itself.

test.describe.configure({ mode: 'parallel' })


const sizes = {
  desktop: { viewport: { width: 1440, height: 900 } },
  phone: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true },
} as const

// The add-on tab each server type has (web/src/lib/addons.ts); Vanilla has none.
const addonTabs: Record<string, string> = { paper: '/plugins', purpur: '/plugins', fabric: '/mods', quilt: '/mods', neoforge: '/mods', forge: '/mods' }

// A well-formed share link that no map has, for the "isn't available" page.
const unknownMapLink = '/map/Zz9xWv8uTs7rQp6oNm5lKj'
// A friends' pack link that opens nothing: the page every unavailable link gets.
const unknownPackLink = '/packs/Pk0Unknown0Link0Abcdef'

/** Where the dashboard machine's CurseForge key comes from, as Settings › Add-on sources reads it: "build" when the build carries one. */
async function curseForgeKey(page: Page): Promise<string | undefined> {
  const machines = (await (await page.request.get('/api/machines')).json()) as { id: string; kind: string }[]
  const local = machines.find((m) => m.kind === 'local')
  if (!local) return undefined
  const res = await page.request.get(`/api/machines/${local.id}/addon-sources`)
  return res.ok() ? ((await res.json()) as { curseforge?: { key?: string } }).curseforge?.key : undefined
}

/** The pages to open signed in, and the shared maps anyone can open without signing in. */
async function routes(page: Page, phone: boolean): Promise<{ live: string[]; shared: string[] }> {
  const servers = (await (await page.request.get('/api/servers')).json()) as { id: string; slug: string; type?: string }[]
  const machines = (await (await page.request.get('/api/machines')).json()) as { id: string; kind: string }[]
  const out = ['/']
  const shared: string[] = []
  for (const s of servers) {
    const res = await page.request.get(`/api/servers/${s.id}/map`)
    const map = (res.ok() ? await res.json() : {}) as { supported?: boolean; public?: boolean; path?: string }
    if (map.public && map.path) shared.push(map.path)
    const addons = addonTabs[s.type ?? '']
    for (const tab of ['', '/console', '/players', '/world', ...(map.supported ? ['/map'] : []), ...(addons ? [addons] : []), '/settings']) out.push(`/servers/${s.slug}${tab}`)
    // On desktop, copies are part of the backup rules page and Schedules is a section of Settings.
    out.push(`/servers/${s.slug}/world/backup-rules`)
    if (phone) out.push(`/servers/${s.slug}/world/backup-rules/copies`, `/servers/${s.slug}/settings/schedules`)
    const listed: unknown = await (await page.request.get(`/api/servers/${s.id}/whitelist`)).json().catch(() => [])
    const player = Array.isArray(listed) ? (listed[0] as { name?: string } | undefined)?.name : undefined
    if (player) out.push(`/servers/${s.slug}/players/${encodeURIComponent(player)}`)
  }
  // The World tab's pages of their own, as a fresh install has them.
  if (servers[0]) out.push(`/servers/${servers[0].slug}/world/pregen`, `/servers/${servers[0].slug}/world/packs`)
  // The first server's server.properties in the editor. Its folders are crawled with a few files (fakedCrawls):
  // a fresh server's folder has dozens, each with a menu of dialogs, and a move dialog opens every folder in it.
  if (servers[0]) out.push(`/servers/${servers[0].slug}/file/server.properties`)
  out.push('/servers/new', '/servers/new#world')
  // The add-on library with Playkeeper's picks, for the first server that
  // has one (each library takes minutes), and a template someone shared.
  const library = servers.find((s) => addonTabs[s.type ?? ''])
  if (library) out.push(`/servers/${library.slug}${addonTabs[library.type ?? '']}/browse`)
  const first = servers[0]
  if (first) {
    const exported = (await (await page.request.get(`/api/servers/${first.id}/template`)).json()) as { link?: string }
    const payload = exported.link?.split('#')[1]
    if (payload) out.push(`/servers/new#template=${payload}`)
  }
  // A joined machine's page is in Settings › Machines, and it has no Machine
  // settings; the dashboard's own machine has both pages. Every machine has a
  // Disk space page.
  for (const m of machines) out.push(...(m.kind === 'remote' ? [`/settings/machines/${m.id}`] : [`/machines/${m.id}`, `/machines/${m.id}/settings`]), `/machines/${m.id}/disk`)
  out.push('/settings', '/settings/team', '/settings/addon-sources', '/settings/discord', '/settings/whop', '/settings/ai-agents', '/settings/machines', '/account', '/account/two-factor', '/recover')
  if (phone) out.push('/more')
  return { live: out, shared }
}

const seedToken = 'Claude on my laptop'

/** Makes the AI agent token the crawl opens, once; the other size's run may have made it already. */
async function seedAiToken(page: Page) {
  const tokens = (await (await page.request.get('/api/tokens')).json()) as { name: string }[]
  if (tokens.some((tk) => tk.name === seedToken)) return
  const { csrfToken } = (await (await page.request.get('/api/auth/me')).json()) as { csrfToken: string }
  const res = await page.request.post('/api/tokens', {
    data: { name: seedToken, role: 'viewer', allServers: true, servers: [], days: 30 },
    headers: { 'X-Requested-With': 'playkeeper', 'X-CSRF-Token': csrfToken },
  })
  expect([201, 409], `POST /api/tokens answered ${res.status()}`).toContain(res.status())
}

interface Crawl {
  route: string
  view: View
}

/** After the live pages: the pages each faked state changes, for the first server in `live`. */
function fakedCrawls(live: string[], phone: boolean): Crawl[] {
  const first = live.find((r) => /^\/servers\/(?!new$)[^/]+$/.test(r))
  const plugins = live.find((r) => /^\/servers\/[^/]+\/plugins$/.test(r))
  const map = live.find((r) => /^\/servers\/[^/]+\/map$/.test(r))
  const disk = live.find((r) => /^\/machines\/[^/]+\/disk$/.test(r))
  const byView: [View, string[]][] = [
    ['stopped', first ? ['/', first, `${first}/console`, `${first}/settings`] : []],
    ['crashed', first ? ['/', first] : []],
    // A phone's server header has no Restart or Stop to disable.
    ['busy', first ? [...(phone ? [] : [first]), `${first}/world`] : []],
    ['empty lists', first ? [`${first}/players`, `${first}/world`] : []],
    ['no servers', ['/', '/welcome']],
    ['update available', phone ? ['/settings', '/more'] : ['/settings']],
    ['space to free', disk ? [disk] : []],
    // Desktop's Settings holds its schedules among every other switch, and each schedule's switch changes its row, so crawling
    // every combination there would take hours; the phone's Schedules page has the same rows on their own.
    ['looks after itself', first ? [`${first}/world/backup-rules`, `${first}/world`, ...(phone ? [`${first}/settings`, `${first}/settings/schedules`, `${first}/world/backup-rules/copies`] : [])] : []],
    ['asleep', first ? ['/', first] : []],
    ['in use', [...(plugins ? [plugins] : []), ...(first ? [`${first}/world`, `${first}/world/packs`, `${first}/world/pregen`] : [])]],
    ['paused', first ? [`${first}/world/pregen`] : []],
    ['friends and team', [...(first ? [`${first}/players`] : []), '/settings/team', '/settings/discord', '/settings/whop']],
    ['map on', map ? [map] : []],
    ['map restart', map ? [map] : []],
    ['a few files', first ? [`${first}/files`, `${first}/files/plugins`] : []],
  ]
  return byView.flatMap(([view, pages]) => pages.map((route) => ({ route, view })))
}

/** Views crawled signed out, with a crawler of their own. */
const signedOutViews = new Set<View | undefined>(['first run', 'second step'])

/**
 * The add-on and modpack reads of a crawler's whole run, page loads
 * included: a note of how many the recorded fixtures answered, and a problem
 * for each that reached the panel or had no recorded answer.
 */
function fixtureReadCheck(crawler: Crawler, who: string): { note: string; problems: string[] } {
  const reads = crawler.fixtureReads()
  const note = `${who}: ${reads.answered.addons} add-on and ${reads.answered.modpacks} modpack reads answered from recorded fixtures, ${reads.live.length} reached the panel, ${reads.unrecorded.length} had no recorded answer`
  const live = [...new Set(reads.live)].map((r) => `${who}: ${r} reached the panel; add-on and modpack reads come from the recorded fixtures`)
  return { note, problems: [...live, ...[...new Set(reads.unrecorded)].map((r) => `${who}: no recorded answer for ${r}`)] }
}

/**
 * Breaks the control of each place this crawler reached and presses it
 * again. `unitOf` names the page each of the crawler's results was pressed
 * on, which is charged the seconds its negative control takes.
 */
async function negativeControls(crawler: Crawler, size: Size, signedIn: boolean, unitOf: string[], charge: (unit: string, seconds: number) => void): Promise<Negative[]> {
  const out: Negative[] = []
  for (const place of places) {
    if (!place.sizes.includes(size) || signedOutViews.has(place.view) === signedIn) continue
    const hit = found(crawler.results, place, size)
    if (!hit) continue
    const started = Date.now()
    const r = await crawler.breakAndPress(hit, hit.status === 'disabled with a reason' ? 'unexplained' : 'does nothing')
    charge(unitOf[crawler.results.indexOf(hit)] ?? '', (Date.now() - started) / 1000)
    const verdict = typeof r === 'string' ? r : r.status
    out.push({ place: place.what, key: at(hit), verdict, caught: typeof r !== 'string' && failing.includes(r.status) })
  }
  return out
}

/** The seconds each page took in the last full run, which split the pages between runners. */
const costs = JSON.parse(fs.readFileSync(fileURLToPath(new URL('./clickthrough-costs.json', import.meta.url)), 'utf8')) as Costs

/** This runner, of how many (PK_SHARD, k/n), and the pages the run crawls (PK_SELECTION, every page when unset). */
function thisRun(): { shard: number; of: number; selection: Selection } {
  const m = /^(\d+)\/(\d+)$/.exec(process.env.PK_SHARD ?? '1/1')
  const shard = Number(m?.[1])
  const of = Number(m?.[2])
  if (!m || shard < 1 || shard > of) throw new Error(`PK_SHARD is "${process.env.PK_SHARD}", not k/n with 1 ≤ k ≤ n`)
  const selection = process.env.PK_SELECTION ? (JSON.parse(process.env.PK_SELECTION) as Selection) : 'all'
  return { shard, of, selection }
}

function summary(results: Result[]): string {
  const counts = new Map<string, number>()
  for (const r of results) counts.set(r.status, (counts.get(r.status) ?? 0) + 1)
  return [...counts].map(([s, n]) => `${n} ${s}`).join(', ')
}

for (const [name, size] of Object.entries(sizes)) {
  test(`every control does something on ${name}`, async ({ browser, baseURL }) => {
    const sz = name as Size
    const base = baseURL ?? ''
    const options = { ...size, ignoreHTTPSErrors: true, locale: 'en-GB', timezoneId: 'UTC' }
    const log = (line: string) => console.log(line)
    const { shard, of, selection } = thisRun()

    // The signed-in pages come from the panel's answers, so every runner signs in first and works out the run's pages alike.
    const context = await browser.newContext(options)
    const page = await context.newPage()
    await login(page)
    const { live, shared } = await routes(page, sz === 'phone')
    const every = named([
      { crawler: 'signed out', route: '/login', view: 'live', counted: true },
      { crawler: 'signed out', route: '/setup', view: 'first run', counted: true },
      // A friends' pack link and a map link that open nothing: the page every unavailable
      // link gets. They have no controls, so they aren't pages with a minimum.
      { crawler: 'signed out', route: unknownPackLink, view: 'live', counted: false },
      { crawler: 'signed out', route: unknownMapLink, view: 'live', counted: false },
      // Two-factor sign-in's second step, with a crawler of its own: the signed-out one
      // presses Sign in, where the fake answers that the password is wrong.
      { crawler: 'second step', route: '/login', view: 'second step', counted: true },
      ...[...live.map((route): Crawl => ({ route, view: 'live' })), ...fakedCrawls(live, sz === 'phone')].map((c) => ({ crawler: 'signed in' as const, ...c, counted: true })),
      // Shared maps open signed out; the signed-in pages said which there are.
      ...shared.map((route) => ({ crawler: 'shared maps' as const, route, view: 'live', counted: false })),
    ])
    const chosen = every.filter((u) => selects(selection, u))
    const shardOf = partition(chosen, costOf(costs, sz), of)
    const mine = chosen.filter((_, i) => shardOf[i] === shard)
    const report: ShardReport = {
      size: sz,
      shard,
      of,
      selection,
      plan: chosen.map((u, i) => ({ name: u.name, crawler: u.crawler, counted: u.counted, shard: shardOf[i] ?? 0 })),
      crawled: [],
      results: [],
      notes: [],
      unreached: [],
      negatives: [],
      fixtureProblems: [],
      curseforge: await curseForgeKey(page),
    }
    const charge = (unit: string, seconds: number) => {
      const c = report.crawled.find((x) => x.name === unit)
      if (c) c.seconds += seconds
    }
    console.log(`${name}: runner ${shard} of ${of} crawls ${mine.length} of the run's ${chosen.length} pages`)

    /** Crawls a crawler's pages of this runner in the run's order, then breaks its places' controls on purpose. */
    const crawlAll = async (crawler: Crawler, units: Unit[], signedIn: boolean | undefined) => {
      await crawler.init()
      const unitOf: string[] = []
      try {
        for (const u of units) {
          const started = Date.now()
          const before = crawler.results.length
          await crawler.crawl(u.route, u.view as View)
          for (let i = before; i < crawler.results.length; i++) unitOf[i] = u.name
          report.results.push(...crawler.results.slice(before).map((r): Pressed => ({ ...r, unit: u.name })))
          report.crawled.push({ name: u.name, seconds: (Date.now() - started) / 1000 })
        }
        if (signedIn !== undefined) report.negatives.push(...(await negativeControls(crawler, sz, signedIn, unitOf, charge)))
      } finally {
        report.notes.push(...crawler.notes)
        report.unreached.push(...crawler.unreached)
      }
    }
    const fixtureChecks: { note: string; problems: string[] }[] = []
    /** A crawler of its own, signed out, in a browser context of its own. */
    const signedOut = async (units: Unit[], negatives: boolean, check?: string) => {
      if (!units.length) return
      const ctx = await browser.newContext(options)
      try {
        const crawler = new Crawler(await ctx.newPage(), name, base, log)
        await crawlAll(crawler, units, negatives ? false : undefined)
        if (check) fixtureChecks.push(fixtureReadCheck(crawler, check))
      } finally {
        await ctx.close()
      }
    }
    const unitsOf = (crawler: CrawlerName) => mine.filter((u) => u.crawler === crawler)
    try {
      await signedOut(unitsOf('signed out'), true, `[${name}] signed out`)
      await signedOut(unitsOf('second step'), true)
      if (unitsOf('signed in').length) {
        await seedAiToken(page)
        const crawler = new Crawler(page, name, base, log)
        await crawlAll(crawler, unitsOf('signed in'), true)
        fixtureChecks.push(fixtureReadCheck(crawler, `[${name}] signed in`))
      }
      await signedOut(unitsOf('shared maps'), false)
    } finally {
      await context.close()
      report.notes.push(...fixtureChecks.map((a) => a.note))
      report.fixtureProblems.push(...fixtureChecks.flatMap((a) => a.problems))
      fs.mkdirSync(outDir, { recursive: true })
      fs.writeFileSync(path.join(outDir, of > 1 ? `clickthrough-${name}-${shard}of${of}.json` : `clickthrough-${name}.json`), JSON.stringify(report, null, 2))
    }

    console.log(`${name}: ${report.results.length} controls: ${summary(report.results)}`)
    for (const n of report.notes) console.log(`note: ${n}`)
    for (const n of report.negatives) console.log(`negative control: ${n.caught ? 'caught' : 'MISSED'} ${n.place}: ${n.key} broken → ${n.verdict}`)
    // On one runner this is the whole run, so it answers to the gate too; split, the gate job does.
    const problems = of === 1 ? gate(sz, [report], 1).problems : ownProblems(report)
    expect(problems, problems.join('\n')).toEqual([])
  })
}

test('a combobox choice that differs from the last only in its digits still changes the page', async ({ page }) => {
  await page.setContent('<div id="root"><h1>New server</h1><button role="combobox" aria-expanded="false">2 GB</button></div>')
  await page.evaluate(installPageHelpers)
  const combobox = () => page.evaluate(() => window.__pk.snapshot().fingerprint.filter((f) => f.startsWith('s:')))
  const before = await combobox()
  await page.evaluate(() => {
    document.querySelector('[role=combobox]')!.textContent = '4 GB'
  })
  expect(await combobox()).not.toEqual(before)
})

test('a choice in a phone’s sheet that differs from the last only in its digits works through its combobox, however fast the sheet goes', async ({ browser }) => {
  // A phone's select as ChoiceSelect draws it, its sheet gone before the
  // crawl can look, as with reduced motion or a slow runner. Only what opened
  // the sheet shows the choice then: a combobox keeps its digits for the
  // crawl, while a button's are dropped, so its choices look alike.
  const page = (trigger: string) => `<!doctype html><title>Machines</title><div id="root"><h1>Machines</h1><section><h2>Hetzner stock</h2>${trigger}</section></div>
    <script>
      const box = document.querySelector('[aria-label="Server type"]')
      const expands = box.hasAttribute('aria-expanded')
      box.addEventListener('click', () => {
        const sheet = document.createElement('div')
        sheet.setAttribute('role', 'dialog')
        sheet.setAttribute('aria-label', 'Server type')
        sheet.innerHTML = '<div role="listbox" aria-label="Server type">' + ['CX23', 'CX33', 'CX43', 'CX53'].map((t) => '<button type="button" role="option" aria-selected="' + (t === box.textContent) + '">' + t + '</button>').join('') + '</div>'
        for (const option of sheet.querySelectorAll('[role=option]')) {
          option.addEventListener('click', () => {
            box.textContent = option.textContent
            if (expands) box.setAttribute('aria-expanded', 'false')
            sheet.remove()
          })
        }
        if (expands) box.setAttribute('aria-expanded', 'true')
        document.body.append(sheet)
      })
    </script>`
  const pages: Record<string, string> = {
    '/combobox': page('<button type="button" role="combobox" aria-label="Server type" aria-haspopup="dialog" aria-expanded="false">CX53</button>'),
    '/button': page('<button type="button" aria-label="Server type" aria-haspopup="dialog">CX53</button>'),
  }
  const server = http.createServer((req, res) => {
    const body = pages[req.url ?? '']
    res.writeHead(body ? 200 : 404, { 'Content-Type': 'text/html' })
    res.end(body ?? '')
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`
  const crawl = async (route: string) => {
    const context = await browser.newContext({ baseURL: base })
    const crawler = new Crawler(await context.newPage(), 'phone', base)
    await crawler.init()
    await crawler.crawl(route)
    return { crawler, context, options: crawler.results.filter((r) => r.key.startsWith('option ')) }
  }
  const verdicts = (options: Result[]) => options.map((r) => `${r.key}: ${r.status}`)
  try {
    const combobox = await crawl('/combobox')
    try {
      expect(verdicts(combobox.options)).toEqual([
        'option "CX#" in listbox "Server type": works',
        'option "CX#" in listbox "Server type" #2: works',
        'option "CX#" in listbox "Server type" #3: works',
        'option "CX#" in listbox "Server type" #4: stays selected',
      ])
      expect(combobox.options.slice(0, 3).map((r) => /text=(CX\d+)/.exec(r.effects.join(' '))?.[1])).toEqual(['CX23', 'CX33', 'CX43'])
      const broken = await combobox.crawler.breakAndPress(combobox.options[1]!)
      expect(typeof broken === 'string' ? broken : broken.status).toBe('dead')
    } finally {
      await combobox.context.close()
    }
    const button = await crawl('/button')
    try {
      expect(verdicts(button.options)).toEqual([
        'option "CX#" in listbox "Server type": dead',
        'option "CX#" in listbox "Server type" #2: dead',
        'option "CX#" in listbox "Server type" #3: dead',
        'option "CX#" in listbox "Server type" #4: stays selected',
      ])
    } finally {
      await button.context.close()
    }
  } finally {
    server.close()
  }
})

test('a dialog the crawl gets back to is waited for while its code loads, not taken for gone', async ({ browser }) => {
  // The command palette's code loads anew with each page load, so on a busy
  // runner its dialog shows well after the press that opens it has settled.
  // Here it shows at once on the page's first two loads and a second late
  // after that, and each of its options goes to another page, so the crawl
  // gets back to the dialog for every option.
  let loads = 0
  const survival = (late: number) => `<!doctype html><title>Survival</title><div id="root"><h1>Survival</h1><button type="button" id="search">Search or jump to…</button></div>
    <script>
      document.getElementById('search').addEventListener('click', () => setTimeout(() => {
        const dialog = document.createElement('div')
        dialog.setAttribute('role', 'dialog')
        dialog.setAttribute('aria-label', 'Search or jump to')
        dialog.innerHTML = '<div role="listbox">' + ['Home', 'Console', 'Players', 'World', 'Settings'].map((p) => '<a role="option" href="/' + p.toLowerCase() + '">' + p + '</a>').join('') + '</div>'
        document.body.append(dialog)
      }, ${late}))
    </script>`
  const server = http.createServer((req, res) => {
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' })
    if (req.url === '/servers/survival') res.end(survival(++loads > 2 ? 1000 : 0))
    else res.end(`<!doctype html><title>Elsewhere</title><div id="root"><h1>${req.url}</h1></div>`)
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`
  const context = await browser.newContext({ baseURL: base })
  try {
    const crawler = new Crawler(await context.newPage(), 'phone', base)
    await crawler.init()
    await crawler.crawl('/servers/survival')
    expect(crawler.results.filter((r) => r.via[0] === 'button "Search or jump to…"').map((r) => `${r.key}: ${r.status}`)).toEqual(
      ['Home', 'Console', 'Players', 'World', 'Settings'].map((p) => `option "${p}" in listbox "": works`),
    )
    expect(crawler.notes.filter((n) => n.includes('went away'))).toEqual([])
    expect(crawler.unreached).toEqual([])
  } finally {
    await context.close()
    server.close()
  }
})

test('a download that starts late still counts, and a download link that does nothing is dead', async ({ browser }) => {
  // The browser fetches a download link itself, past page.route, so a real
  // server answers: a download link and a plain link to an attachment 9 s
  // late, as CI's panel once did, and a link whose script starts its
  // download 3 s after the press, with nothing loading meanwhile.
  const attach = (res: http.ServerResponse) => {
    res.writeHead(200, { 'Content-Type': 'application/gzip', 'Content-Disposition': 'attachment; filename="world.tar.gz"' })
    res.end('a backup')
  }
  const server = http.createServer((req, res) => {
    if (req.url === '/') {
      res.writeHead(200, { 'Content-Type': 'text/html' })
      res.end(`<!doctype html><title>Backups</title><div id="root"><h1>Backups</h1>
        <a href="/backups/1/download" download="world.tar.gz">Download</a>
        <a href="/backups/2/file">Notes</a>
        <a href="/export" onclick="event.preventDefault(); setTimeout(() => { location.href = '/backups/3/file' }, 3000)">Export</a></div>`)
    } else if (req.url === '/backups/1/download' || req.url === '/backups/2/file') {
      setTimeout(() => attach(res), 9000)
    } else if (req.url === '/backups/3/file') {
      attach(res)
    } else {
      res.writeHead(404)
      res.end()
    }
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`
  const context = await browser.newContext({ baseURL: base })
  try {
    const crawler = new Crawler(await context.newPage(), 'desktop', base)
    await crawler.init()
    await crawler.crawl('/')
    const download = crawler.results.find((r) => r.key === 'link "Download"')
    for (const key of ['link "Download"', 'link "Notes"', 'link "Export"']) {
      const r = crawler.results.find((x) => x.key === key)
      expect(r?.status, key).toBe('works')
      expect(r?.effects, key).toContain('started a download')
    }
    expect(crawler.notes.join('\n')).toContain('› link "Download": its download started')
    const broken = await crawler.breakAndPress(download!)
    expect(typeof broken === 'string' ? broken : broken.status).toBe('dead')
  } finally {
    await context.close()
    server.close()
  }
})

test('a modpack read is answered from the fixtures or breaks the control that made it, and never reaches the panel', async ({ browser }) => {
  let reached = 0
  const server = http.createServer((req, res) => {
    if (req.url?.startsWith('/api/')) {
      reached++
      res.writeHead(500)
      res.end()
      return
    }
    res.writeHead(200, { 'Content-Type': 'text/html' })
    res.end(`<!doctype html><title>New server</title><div id="root"><h1>New server</h1><p id="out"></p>
      <button onclick="fetch('/api/machines/m1/modpacks?source=modrinth&sort=downloads&limit=12').then((r) => r.json()).then((j) => { out.textContent = j.cards[0].name })">Browse</button>
      <button onclick="fetch('/api/machines/m1/modpacks/modrinth/AAAAAAAA').then((r) => { out.textContent = 'Answered ' + r.status })">Open</button></div>`)
  })
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve))
  const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`
  const context = await browser.newContext({ baseURL: base })
  try {
    const crawler = new Crawler(await context.newPage(), 'desktop', base)
    await crawler.init()
    await crawler.crawl('/')
    expect(crawler.results.find((r) => r.key === 'button "Browse"')?.status).toBe('works')
    const open = crawler.results.find((r) => r.key === 'button "Open"')
    expect(failing).toContain(open?.status)
    expect(open?.problems.join('\n')).toContain('no recorded answer for GET /api/machines/m1/modpacks/modrinth/AAAAAAAA')
    expect(crawler.fixtureReads()).toEqual({ answered: { addons: 0, modpacks: 1 }, live: [], unrecorded: ['GET /api/machines/m1/modpacks/modrinth/AAAAAAAA'] })
    expect(reached).toBe(0)
  } finally {
    await context.close()
    server.close()
  }
})

/** A control that worked, on the unit of a run named after its route and view. */
const pressed = (route: string, key: string, view?: View): Pressed => ({ viewport: 'desktop', route, view, via: [], key, status: 'works', effects: ['changed the page'], problems: [], unit: named([{ crawler: 'signed in', route, view: view ?? 'live', counted: true }])[0]?.name ?? '' })

/** A runner's report of a run at desktop size, crawling the units of `plan` given to `shard`. */
function shardReport(shard: number, of: number, plan: [string, number][], results: Pressed[], more: Partial<ShardReport> = {}): ShardReport {
  const units = plan.map(([name, k]) => ({ name, crawler: 'signed in' as const, counted: true, shard: k }))
  return { size: 'desktop', shard, of, selection: 'all', plan: units, crawled: units.filter((u) => u.shard === shard).map((u) => ({ name: u.name, seconds: 1 })), results, notes: [], unreached: [], negatives: [], fixtureProblems: [], ...more }
}

test('the gate fails a failing control, a state it could not get back to, a page under its minimum and a place it missed', () => {
  const rules: Rules = { minimums: { '/': 2, '/servers/* (stopped)': 1 }, places: [{ what: 'a slider', sizes: ['desktop'], page: '/', key: /^slider / }] }
  const plan: [string, number][] = [
    ['/', 1],
    ['/servers/* (stopped)', 1],
    ['/settings', 1],
  ]
  const results = [pressed('/', 'button "Home"'), pressed('/', 'slider "Memory"'), pressed('/servers/survival', 'button "Start"', 'stopped'), pressed('/settings', 'button "Sign out"')]
  const negatives: Negative[] = [{ place: 'a slider', key: '/ › slider "Memory"', verdict: 'dead', caught: true }]
  const run = (r: Pressed[], more: Partial<ShardReport> = {}, units = plan) => gate('desktop', [shardReport(1, 1, units, r, { negatives, ...more })], 1, rules).problems
  expect(run(results)).toEqual([])

  const dead: Pressed = { ...pressed('/settings', 'button "Check now"'), status: 'dead' }
  expect(run([...results, dead]).join('\n')).toContain('button "Check now": pressing it did nothing visible')
  expect(run(results, { unreached: ['[desktop] /: could not reach button "Menu" again'] })).toEqual(['[desktop] /: could not reach button "Menu" again'])
  expect(run(results.filter((r) => r.key !== 'button "Home"'))).toEqual(['desktop /: 1 control pressed, fewer than its minimum of 2'])
  expect(
    run(
      results.filter((r) => r.view !== 'stopped'),
      {},
      plan.filter(([n]) => n !== '/servers/* (stopped)'),
    ),
  ).toEqual(['desktop /servers/* (stopped): not crawled (its minimum is 1)'])
  expect(run(results.filter((r) => r.key !== 'button "Sign out"'))).toEqual(['desktop /settings: 0 controls pressed, fewer than its minimum of 1'])
  expect(run([...results.filter((r) => !r.key.startsWith('slider')), pressed('/', 'button "More"')], { negatives: [] })).toEqual(['desktop: never pressed a slider on / (/^slider /)'])
  expect(run(results, { negatives: [] })).toEqual(['desktop: pressed a slider but didn\'t break it on purpose to check the crawl notices'])
  expect(run(results, { negatives: [{ ...negatives[0]!, verdict: 'works', caught: false }] })).toEqual(['desktop: with a slider broken, the crawl said "works" (/ › slider "Memory")'])
})

test("a build that carries CurseForge's key may press nothing of its own on a page that then has nothing to press; any other build must", () => {
  const rules: Rules = { minimums: { '/settings': 1 }, keyMinimums: { '/settings/addon-sources': 0 }, places: [] }
  const plan: [string, number][] = [
    ['/settings', 1],
    ['/settings/addon-sources', 1],
  ]
  const results = [pressed('/settings', 'button "Sign out"')]
  const run = (curseforge?: string) => gate('desktop', [shardReport(1, 1, plan, results, { curseforge })], 1, rules).problems
  expect(run('build')).toEqual([])
  expect(run('none')).toEqual(['desktop /settings/addon-sources: 0 controls pressed, fewer than its minimum of 1'])
  expect(run(undefined)).toEqual(['desktop /settings/addon-sources: 0 controls pressed, fewer than its minimum of 1'])
  expect(gate('phone', [{ ...shardReport(1, 1, plan, results, { curseforge: 'build' }), size: 'phone' }], 1).problems).not.toContainEqual(expect.stringContaining('/settings/addon-sources'))
})

test('split between runners, the gate counts controls in the order of one runner and fails a runner that went missing, a page nobody crawled and runners that disagree', () => {
  const rules: Rules = { minimums: { '/': 2, '/servers/*': 1, '/servers/* (stopped)': 1 }, places: [] }
  const plan: [string, number][] = [
    ['/', 1],
    ['/servers/*', 2],
    ['/servers/* (stopped)', 2],
  ]
  // The second runner presses the sidebar's link again, since it didn't crawl Home; it counts on Home, as on one runner.
  const one = shardReport(1, 2, plan, [pressed('/', 'button "Home"'), pressed('/', 'link "Settings"')])
  const two = shardReport(2, 2, plan, [pressed('/servers/survival', 'link "Settings"'), pressed('/servers/survival', 'button "Restart"'), pressed('/servers/survival', 'button "Start"', 'stopped')])
  const verdict = gate('desktop', [one, two], 2, rules)
  expect(verdict.problems).toEqual([])
  expect(Object.fromEntries(verdict.counts)).toEqual({ '/': 2, '/servers/*': 1, '/servers/* (stopped)': 1 })
  expect(gate('desktop', [one, two], 2, { ...rules, minimums: { ...rules.minimums, '/servers/*': 2 } }).problems).toEqual(['desktop /servers/*: 1 control pressed, fewer than its minimum of 2'])

  expect(gate('desktop', [one], 2, rules).problems).toContain('desktop: runner 2 of 2 sent no report, so its pages weren\'t all crawled')
  expect(gate('desktop', [one, { ...two, crawled: two.crawled.slice(0, 1) }], 2, rules).problems).toContain('desktop: runner 2 of 2 didn\'t crawl /servers/* (stopped)')
  const other = { ...two, plan: two.plan.map((p) => (p.name === '/servers/*' ? { ...p, shard: 1 } : p)) }
  expect(gate('desktop', [one, other], 2, rules).problems.join('\n')).toContain('runner 2 worked out other pages than runner 1')
})

test('a run of the pages a change touches holds those pages to their minimums and places, and only those', () => {
  const selection: Selection = { pages: ['/servers/*'], preludes: ['/'] }
  const rules: Rules = {
    minimums: { '/': 1, '/servers/*': 1, '/servers/* (stopped)': 1, '/settings': 3 },
    places: [
      { what: 'Start on a stopped server', sizes: ['desktop'], view: 'stopped', page: '/servers/*', key: /^button "Start"$/ },
      { what: 'a switch in Settings', sizes: ['desktop'], page: '/settings', key: /^switch / },
    ],
  }
  const plan: [string, number][] = [
    ['/', 1],
    ['/servers/*', 1],
    ['/servers/* (stopped)', 1],
  ]
  const start = pressed('/servers/survival', 'button "Start"', 'stopped')
  const results = [pressed('/', 'button "Home"'), pressed('/servers/survival', 'button "Restart"'), start]
  const negatives: Negative[] = [{ place: 'Start on a stopped server', key: at(start), verdict: 'dead', caught: true }]
  expect(gate('desktop', [shardReport(1, 1, plan, results, { selection, negatives })], 1, rules).problems).toEqual([])
  expect(gate('desktop', [shardReport(1, 1, plan, results.slice(0, 2), { selection })], 1, rules).problems).toEqual(['desktop /servers/* (stopped): 0 controls pressed, fewer than its minimum of 1', 'desktop: never pressed Start on a stopped server on /servers/* (stopped) (/^button "Start"$/)'])
  // Pressed only on another page, a place isn't reached, and breaking it there would prove nothing.
  const elsewhere = { ...start, route: '/servers/survival/console', unit: '/servers/*/console (stopped)' }
  expect(gate('desktop', [shardReport(1, 1, plan, [...results.slice(0, 2), elsewhere], { selection })], 1, rules).problems).toEqual([
    'desktop /servers/* (stopped): 0 controls pressed, fewer than its minimum of 1',
    'desktop: never pressed Start on a stopped server on /servers/* (stopped) (/^button "Start"$/), only on /servers/*/console (stopped): if it moved, change its page in clickthrough-rules.ts',
  ])
  expect(selects(selection, { route: '/servers/survival/console', view: 'live' })).toBe(false)
  expect(selects(selection, { route: '/', view: 'stopped' })).toBe(false)
  expect(selects(selection, { route: '/servers/survival', view: 'crashed' })).toBe(true)
})

test('a run of New server’s world step counts on that page, not on the Overview', () => {
  const selection: Selection = { pages: ['/servers/new#world'], preludes: [] }
  const rules: Rules = { minimums: { '/servers/*': 21, '/servers/new': 36 }, places: [] }
  const every = named([
    { crawler: 'signed in', route: '/servers/survival', view: 'live', counted: true },
    { crawler: 'signed in', route: '/servers/new', view: 'live', counted: true },
    { crawler: 'signed in', route: '/servers/new#world', view: 'live', counted: true },
  ])
  const chosen = every.filter((u) => selects(selection, u))
  expect(chosen.map((u) => u.name)).toEqual(['/servers/new#world'])
  const plan = chosen.map((u): [string, number] => [u.name, 1])
  const world = pressed('/servers/new#world', 'button "Choose a world file"')
  expect(world.unit).toBe('/servers/new#world')
  const verdict = gate('desktop', [shardReport(1, 1, plan, [world], { selection })], 1, rules)
  expect(verdict.problems).toEqual([])
  expect(Object.fromEntries(verdict.counts)).toEqual({ '/servers/new#world': 1 })
  expect(gate('desktop', [shardReport(1, 1, plan, [], { selection })], 1, rules).problems).toEqual(['desktop /servers/new#world: 0 controls pressed, fewer than its minimum of 1'])
})

test('runners get whole groups of pages, a page with its faked states, the costliest first, alike on every runner', () => {
  const units = named([
    { crawler: 'signed out', route: '/login', view: 'live', counted: true },
    { crawler: 'signed out', route: '/setup', view: 'first run', counted: true },
    { crawler: 'second step', route: '/login', view: 'second step', counted: true },
    { crawler: 'signed in', route: '/', view: 'live', counted: true },
    { crawler: 'signed in', route: '/servers/survival', view: 'live', counted: true },
    { crawler: 'signed in', route: '/servers/new', view: 'live', counted: true },
    { crawler: 'signed in', route: '/servers/new#world', view: 'live', counted: true },
    { crawler: 'signed in', route: '/settings', view: 'live', counted: true },
    { crawler: 'signed in', route: '/', view: 'stopped', counted: true },
    { crawler: 'signed in', route: '/servers/survival', view: 'stopped', counted: true },
  ])
  const seconds: Record<string, number> = { '/servers/new': 500, '/servers/new#world': 100, '/servers/*': 300, '/servers/* (stopped)': 50, '/': 80, '/ (stopped)': 40 }
  const cost = (u: Unit) => seconds[u.name] ?? 10
  const shards = partition(units, cost, 3)
  const shardOf = (name: string) => shards[units.findIndex((u) => u.name === name)]
  expect(shards.every((k) => k >= 1 && k <= 3)).toBe(true)
  expect(shardOf('/servers/new#world')).toBe(shardOf('/servers/new'))
  expect(shardOf('/servers/* (stopped)')).toBe(shardOf('/servers/*'))
  expect(shardOf('/ (stopped)')).toBe(shardOf('/'))
  expect(shardOf('/setup (first run)')).toBe(shardOf('/login'))
  expect(new Set([shardOf('/servers/new'), shardOf('/servers/*'), shardOf('/')]).size).toBe(3)
  expect(partition(units, cost, 3)).toEqual(shards)
  expect(partition(units, cost, 1).every((k) => k === 1)).toBe(true)
  expect(pageOf('/servers/new#world')).toBe('/servers/new#world')
  expect(pageOf('/servers/new#template=eyJhIjoxfQ')).toBe('/servers/new#template=*')
  expect(pageOf('/servers/survival/players/PkBotFriend')).toBe('/servers/*/players/*')
  expect(pageOf('/machines/rmk4ybqrck/disk')).toBe('/machines/*/disk')
  expect(pageOf('/map/Zz9xWv8uTs7rQp6oNm5lKj')).toBe('/map/*')
})

test('a change to a page crawls the pages its modules draw, after the pages before them; one to no page, none', () => {
  const graph = importGraph(fileURLToPath(new URL('../../../web/src', import.meta.url)))
  expect(pageMapProblems(graph)).toEqual([])
  const reach = (changed: string[]) => forPullRequest(changed, graph)
  expect(reach(['web/src/pages/server/console.tsx'])).toMatchObject({ mode: 'pages', pages: ['/servers/*/console'], preludes: ['/', '/servers/*'], views: ['/servers/*/console'] })
  // The World tab shows pre-generation's row too, and the Map area uses its texts.
  expect(reach(['web/src/pages/server/world-pregen.tsx'])).toMatchObject({ mode: 'pages', pages: ['/servers/*/world', '/servers/*/world/pregen', '/servers/*/map'], preludes: ['/', '/servers/*'] })
  const twoFactor = reach(['web/src/pages/two-factor.tsx'])
  expect(twoFactor.pages).toEqual(expect.arrayContaining(['/account', '/account/two-factor']))
  expect(twoFactor.pages).not.toContain('/servers/*')
  // Settings draws one section on each of its pages.
  expect(reach(['web/src/pages/team.tsx'])).toMatchObject({ mode: 'pages', pages: ['/settings/team'], preludes: ['/', '/settings'] })
  expect(reach(['web/src/pages/settings.tsx']).pages).toEqual(['/settings', '/settings/team', '/settings/addon-sources', '/settings/discord', '/settings/whop', '/settings/ai-agents', '/settings/machines', '/settings/machines/*'])
  expect(reach(['web/src/pages/server/map.test.tsx', 'internal/panel/server.go', 'docs/ARCHITECTURE.md']).mode).toBe('none')
  // The invite page and How it's running aren't crawled, but their accessibility and width are checked;
  // onboarding's pages are crawled, but views.spec.ts has no view of them.
  expect(reach(['web/src/pages/join.tsx'])).toMatchObject({ mode: 'pages', pages: [], preludes: [], views: ['/join/*'] })
  expect(reach(['web/src/pages/onboarding.tsx'])).toMatchObject({ mode: 'pages', pages: ['/setup', '/welcome'], views: [] })
  expect(reach(['web/src/components/app/password-field.tsx']).views).toContain('/join/*')
  expect(reach(['web/src/pages/server/running.tsx'])).toMatchObject({ mode: 'pages', pages: [], views: ['/servers/*/running'] })
  expect(reach(['web/src/pages/server-page.tsx']).mode).toBe('none')
  // A page module the page map doesn't know yet is sampled with Home until it's added.
  expect(reach(['web/src/pages/a-new-page.tsx']).pages).toEqual(['/'])
  expect([...closure(graph, ['pages/server/index.tsx'])]).not.toContain('pages/server/console.tsx')
  expect(pageOf('/join/AbCdEf123')).toBe('/join/*')
  expect(preludesOf('/servers/*/world/pregen')).toEqual(['/', '/servers/*'])
  expect(preludesOf('/login')).toEqual([])

  // A page is crawled with its states on one runner, so two pages have no use for a third.
  const costs: Costs = { desktop: { '/': 100, '/servers/*': 1100, '/servers/* (stopped)': 1100 }, phone: { '/': 100, '/more': 60 } }
  expect(shardsFor(costs, 'all')).toEqual([
    { size: 'desktop', shard: 1, of: 2 },
    { size: 'desktop', shard: 2, of: 2 },
    { size: 'phone', shard: 1, of: 1 },
  ])
  expect(shardsFor(costs, { pages: ['/more'], preludes: ['/'] })).toEqual([{ size: 'phone', shard: 1, of: 1 }])
  expect(shardsFor(costs, 'all', 300).filter((s) => s.size === 'phone')).toEqual([{ size: 'phone', shard: 1, of: 1 }])
})

test('a pull request crawls every page its change reaches, one page for those a module many share runs on, on no more than its runners', () => {
  const graph = importGraph(fileURLToPath(new URL('../../../web/src', import.meta.url)))
  const costs = JSON.parse(fs.readFileSync(new URL('./clickthrough-costs.json', import.meta.url), 'utf8')) as Costs
  const plan = (changed: string[], keys?: string[], users: Record<string, string[]> = {}) => forPullRequest(changed, graph, keys, (k) => users[k] ?? [])
  expect(plan(['web/src/pages/server/console.tsx'])).toMatchObject({ mode: 'pages', pages: ['/servers/*/console'], preludes: ['/', '/servers/*'] })
  // A module every page runs on, the crawler and the build take Home for them all.
  expect(plan(['web/src/components/ui/button.tsx'])).toMatchObject({ mode: 'pages', pages: ['/'], preludes: [] })
  expect(plan(['test/e2e/ui/crawl.ts']).pages).toEqual(['/'])
  expect(plan(['web/vite.config.ts']).pages).toEqual(['/'])
  // A server's pages all run its page module; its Overview stands for them.
  expect(plan(['web/src/pages/server/index.tsx']).pages).toEqual(['/servers/*'])
  // The app's shell and the stylesheet are above the page map; Home stands for the pages they reach.
  expect(plan(['web/src/App.tsx']).pages).toEqual(['/'])
  expect(plan(['web/src/main.tsx']).pages).toEqual(['/'])
  expect(plan(['web/src/styles.css']).pages).toEqual(['/'])
  expect(plan(['web/src/demo/client.ts']).mode).toBe('none')
  // A changed string takes the pages whose modules use it, not every page that loads the table.
  const pregen = plan(['web/src/i18n/en.ts'], ['pregen.unknown'], { 'pregen.unknown': ['pages/server/world-pregen.tsx'] })
  expect(pregen.mode).toBe('pages')
  expect(pregen.pages).toContain('/servers/*/world/pregen')
  expect(pregen.pages).not.toContain('/')
  expect(plan(['web/src/i18n/en.ts'], ['nobody.uses.this']).mode).toBe('none')
  // Without the keys, as plan.ts --files gives none, the table is a module every page runs on.
  expect(plan(['web/src/i18n/en.ts']).pages).toEqual(['/'])
  expect(plan(['web/src/pages/server/map.test.tsx', 'internal/panel/server.go', 'docs/ARCHITECTURE.md']).mode).toBe('none')
  // Every page module at once: every page, on no more runners than a pull request gets, each taking longer.
  const every = forPullRequest([...new Set(Object.values(pageModules).flat())].map((m) => `web/src/${m}`), graph)
  expect(every.pages).toEqual(Object.keys(pageModules))
  const all = { pages: every.pages, preludes: every.preludes }
  expect(shardsFor(costs, all, pullRequestShardSeconds).length).toBeGreaterThan(pullRequestRunners)
  expect(pullRequestShare(costs, all)).toBeGreaterThan(pullRequestShardSeconds)
  expect(shardsFor(costs, all, pullRequestShare(costs, all)).length).toBeLessThanOrEqual(pullRequestRunners)
  expect(pullRequestShare(costs, { pages: ['/servers/*/console'], preludes: ['/', '/servers/*'] })).toBe(pullRequestShardSeconds)
})

test("a change to the Go code behind the API takes the pages of the modules that call what it reaches, and one page for a route many pages' modules call", () => {
  const graph = importGraph(fileURLToPath(new URL('../../../web/src', import.meta.url)))
  const api = (routes: Record<string, { path: string; users: string[] }[]>) => (file: string) => routes[file] ?? []
  const plan = (changed: string[], routes: Record<string, { path: string; users: string[] }[]>) => forPullRequest(changed, graph, undefined, undefined, api(routes))
  const invites = plan(['internal/panel/team.go'], { 'internal/panel/team.go': [{ path: '/api/team/invites', users: ['pages/team.tsx'] }] })
  expect(invites).toMatchObject({ mode: 'pages', pages: ['/settings/team'], preludes: ['/', '/settings'], views: ['/settings/team'] })
  expect(invites.why).toEqual(['internal/panel/team.go changes /api/team/invites: /settings/team'])
  // A route the dashboard's own data calls on every page takes the first page listing a module that calls it.
  const everywhere = plan(['internal/agent/handlers.go'], {
    'internal/agent/handlers.go': [
      { path: '/api/machines/{mid}', users: ['api/workspace.tsx', 'pages/machine.tsx'] },
      { path: '/api/machines/{mid}/network-guard', users: ['pages/machine-settings/guard.tsx'] },
    ],
  })
  expect(everywhere.pages).toEqual(['/machines/*', '/machines/*/settings'])
  expect(everywhere.why[0]).toMatch(/^internal\/agent\/handlers\.go changes \/api\/machines\/\{mid\}, which modules on more than a third of the pages call, so \/machines\/\* stands for them$/)
  expect(plan(['internal/usage/usage.go'], {}).mode).toBe('none')
})

test('the API a Go change reaches: the handlers that use what it changed, the routes that run them or pass requests on to them, and the modules that call those', () => {
  const root = fs.mkdtempSync(path.join(fs.mkdtempSync('/tmp/pk-api-'), 'repo-'))
  const write = (file: string, text: string) => {
    fs.mkdirSync(path.dirname(path.join(root, file)), { recursive: true })
    fs.writeFileSync(path.join(root, file), text)
  }
  write(
    'internal/panel/server.go',
    [
      'package panel',
      'import "example.com/playkeeper/internal/invites"',
      'func (s *Server) Routes() []Route {',
      '\tsg := func(p, agentPath string) Route { return Route{"GET", p, needSession, actView, s.serverProxy("GET", agentPath)} }',
      '\treturn []Route{',
      '\t\t{"GET", "/api/team", needSession, actView, s.hTeam},',
      '\t\t{"POST", "/api/team/invites", needSessionCSRF, actManageTeam, s.hInvite},',
      '\t\tsg("/api/servers/{id}/logs", "/v1/servers/{id}/logs"),',
      '\t\t{"GET", "/api/servers/{id}/icon", needSession, actView, s.rawGet("/v1/servers/{id}/icon")},',
      '\t}',
      '}',
      '',
      'func (s *Server) hTeam(w http.ResponseWriter, r *http.Request) { s.teamList(w) }',
      '',
      'func (s *Server) hInvite(w http.ResponseWriter, r *http.Request) {',
      '\tc, err := invites.NewMember(spec)',
      '\ts.teamList(w)',
      '}',
      '',
      '// teamList writes the team.',
      'func (s *Server) teamList(w http.ResponseWriter) {',
      '\tw.Start()',
      '}',
      '',
      'func (s *Server) rawGet(agentPath string) http.HandlerFunc {',
      '\treturn nil',
      '}',
      '',
    ].join('\n'),
  )
  write(
    'internal/agent/agent.go',
    [
      'package agent',
      '',
      'type server struct {',
      '\tid string',
      '\tlog *console',
      '}',
      '',
      'func (a *Agent) routeTable() []Route {',
      '\tsrv := a.withServer',
      '\treturn []Route{',
      '\t\t{"GET", "/v1/servers/{id}/logs", srv((*server).hLogs)},',
      '\t\t{"GET", "/v1/servers/{id}/icon", srv((*server).hIcon)},',
      '\t}',
      '}',
      '',
      'func (s *server) hLogs(w http.ResponseWriter, r *http.Request) { s.log.since(limit) }',
      '',
      'func (s *server) hIcon(w http.ResponseWriter, r *http.Request) { s.icon.Start() }',
      '',
      'func (c *console) since(n int) []string {',
      '\treturn nil',
      '}',
      '',
      '// Start starts the agent.',
      'func (a *Agent) Start() {',
      '}',
      '',
    ].join('\n'),
  )
  write('internal/invites/invites.go', ['package invites', '', 'func NewMember(spec MemberSpec) (Created, error) {', '\treturn newCreator(spec)', '}', '', 'func newCreator(spec MemberSpec) (Created, error) {', '\treturn Created{}, nil', '}', ''].join('\n'))
  write('web/src/pages/team.tsx', "export const load = () => get('/api/team').then(() => post('/api/team/invites', {}))\n")
  write('web/src/pages/console.tsx', "export const logs = (id: string) => get(serverApi(id, `/logs?after=${n}`))\n")
  write('web/src/pages/icon.tsx', "export const icon = (id: string) => serverApi(id, '/icon')\nexport const any = (id: string, list: string, who: string) => serverApi(id, `/${list}/${who}`)\n")
  const graph = importGraph(path.join(root, 'web/src'))
  const reached = (file: string, lines?: number[]) => apiReach(root, graph, new Map([[file, lines]]))(file)
  // A helper two handlers call reaches both; the modules calling their routes come with each.
  expect(reached('internal/panel/server.go', [21, 22])).toEqual([
    { path: '/api/team', users: ['pages/team.tsx'] },
    { path: '/api/team/invites', users: ['pages/team.tsx'] },
  ])
  // A handler reaches its own route only, and a changed route line reaches that route.
  expect(reached('internal/panel/server.go', [15, 16]).map((r) => r.path)).toEqual(['/api/team/invites'])
  expect(reached('internal/panel/server.go', [9]).map((r) => r.path)).toEqual(['/api/servers/{id}/icon'])
  // The agent's handler reaches the panel's route that passes requests on to it; its receiver's
  // unexported method on another value counts, a method of the same name on another package's type doesn't.
  expect(reached('internal/agent/agent.go', [20, 21])).toEqual([{ path: '/api/servers/{id}/logs', users: ['pages/console.tsx'] }])
  expect(reached('internal/agent/agent.go', [25, 26])).toEqual([])
  // A changed field reaches what uses it, not every use of its type.
  expect(reached('internal/agent/agent.go', [5]).map((r) => r.path)).toEqual(['/api/servers/{id}/logs'])
  // Another package reaches the handlers that use what it exports.
  expect(reached('internal/invites/invites.go', [8]).map((r) => r.path)).toEqual(['/api/team/invites'])
  expect(reached('internal/panel/server_test.go')).toEqual([])
  expect(webCalls(path.join(root, 'web/src'), graph).get('pages/icon.tsx')).toEqual(['/api/servers/{}/icon'])
  expect(calls('/api/servers/{}/logs', '/api/servers/{id}/logs')).toBe(true)
  expect(calls('/api/servers/{}/files{}', '/api/servers/{id}/files')).toBe(true)
  expect(calls('/api/servers/{}/files{}', '/api/servers/{id}/files/move')).toBe(false)
  expect(calls('/api/servers/{}/players/{}', '/api/servers/{id}/players/profile')).toBe(true)
  expect(calls('/api/team/invites/{}', '/api/team/invites')).toBe(false)
  expect(calls('/api/public/map/{}/tiles/a/b/c', '/api/public/map/{token}/tiles/{rest...}')).toBe(true)
  expect(goDecls('type T struct {\n\tA, B int\n\tc string\n}\n').map((d) => [...d.byLine])).toEqual([
    [
      [2, ['T.A', 'T.B']],
      [3, ['T.c']],
    ],
  ])
})

test("the network guard's Go code reaches Machine settings, and not a server's Console", () => {
  const root = fileURLToPath(new URL('../../..', import.meta.url))
  const graph = importGraph(path.join(root, 'web/src'))
  const api = apiReach(root, graph, new Map([['internal/agent/guard.go', undefined]]))
  const guard = forPullRequest(['internal/agent/guard.go'], graph, undefined, undefined, api)
  expect(guard.pages).toContain('/machines/*/settings')
  expect(guard.pages).not.toContain('/servers/*/console')
})

test('a change to the string table names the keys whose lines it changed, in values spread over lines too, and not comments', () => {
  const before = ['export const en = {', '  // Shared', "  'a.one': 'One',", "  'a.two': {", "    one: '1 thing',", "    other: '{count} things',", '  },', "  'a.three': 'Three',", '} as const'].join('\n')
  const after = ['export const en = {', '  // Shared, and more', "  'a.one': 'One!',", "  'a.two': {", "    one: '1 thing',", "    other: '{count} things now',", '  },', "  'a.three': 'Three',", "  'a.four': 'Four',", '} as const'].join('\n')
  const diff = ['@@ -2 +2 @@', '-  // Shared', '+  // Shared, and more', '@@ -3 +3 @@', "-  'a.one': 'One',", "+  'a.one': 'One!',", '@@ -6 +6 @@', "-    other: '{count} things',", "+    other: '{count} things now',", '@@ -8,0 +9 @@', "+  'a.four': 'Four',"].join('\n')
  expect(keysChanged(diff, before, after)).toEqual(['a.four', 'a.one', 'a.two'])
  expect(keysChanged(['@@ -3 +2,0 @@', "-  'a.one': 'One',"].join('\n'), before, after)).toEqual(['a.one'])
})

test('a change to how the state the pages are crawled in is made crawls after the onboarding and the bots; any other, after a saved played state', () => {
  expect(freshSetup(['web/src/pages/server/console.tsx', 'internal/panel/server.go', 'test/e2e/ui/crawl.ts'])).toBeUndefined()
  expect(freshSetup([])).toBeUndefined()
  expect(freshSetup(['web/src/pages/login.tsx', 'test/e2e/bot/bot.js'])).toBe('test/e2e/bot/bot.js changes how the state the pages are crawled in is made')
  for (const file of ['test/e2e/ui/onboarding.spec.ts', 'test/e2e/scenario.py', 'test/e2e/pkclient.py']) expect(freshSetup([file]), file).toBeDefined()
  // A change to how the state is saved or restored starts from a saved one, which tries it.
  expect(freshSetup(['scripts/e2e/played-state.sh', '.github/actions/played-install/action.yml'])).toBeUndefined()
  const graph = importGraph(fileURLToPath(new URL('../../../web/src', import.meta.url)))
  expect(forPullRequest(['scripts/e2e/played-state.sh'], graph).pages).toEqual(['/'])
  expect(forPullRequest(['.github/actions/played-install/action.yml'], graph).pages).toEqual(['/'])
})

test('the add-on fixtures answer as the panel would, work out plans against the folder and have no answer for what was never recorded', () => {
  const empty: World = { addons: recordedFolder(), running: true }
  const viaVersion = { source: 'modrinth', projectId: 'P1OZGk5p', name: 'ViaVersion', versionId: 'FaishMnD', versionNumber: '5.12.0', published: '2026-09-18T15:01:59.741758Z', fileName: 'ViaVersion-5.12.0.jar' }
  const withVia: World = { addons: { ...recordedFolder(), files: [{ fileName: viaVersion.fileName, size: 6_503_775, status: 'managed', addon: viaVersion }] }, running: false }
  const get = (rest: string, world = empty) => answerRead('GET', new URL(`https://panel/api/servers/abc/addons${rest}`), undefined, world)
  const plan = (body: unknown, world = empty) => answerRead('POST', new URL('https://panel/api/servers/abc/addons/update/plan'), body, world)

  type Cards = { cards: { source: string; name: string; downloads: number; installed: boolean }[]; more: boolean }
  const library = get('/search')?.body as Cards
  expect(new Set(library.cards.map((c) => c.source))).toEqual(new Set(['modrinth', 'hangar']))
  expect(library.cards.map((c) => c.downloads)).toEqual(library.cards.map((c) => c.downloads).sort((a, b) => b - a))
  expect(library.more).toBe(false)
  expect((get('/search?category=protection')?.body as Cards).cards.every((c) => c.source === 'hangar')).toBe(true)
  expect((get('/search?q=via', withVia)?.body as Cards).cards.filter((c) => c.installed).map((c) => c.name)).toEqual(['ViaVersion'])
  expect(get('/search?sort=newest')).toMatchObject({ status: 400, body: { code: 'invalid_request' } })

  type Details = { plan: { steps: { name: string }[]; fingerprint: string } }
  const recorded = (get('/project/hangar/12')?.body as Details).plan
  expect(recorded.steps.map((s) => s.name)).toEqual(['ViaBackwards', 'ViaVersion'])
  const install = (fingerprint: unknown, world = empty) => installJob({ source: 'hangar', projectId: '12', fingerprint }, world)
  expect(install(recorded.fingerprint)).toMatchObject({ ends: { status: 'succeeded', detail: { restartNeeded: true, files: [{ name: 'ViaBackwards' }, { name: 'ViaVersion', neededBy: 'ViaBackwards' }] } } })
  expect(install(undefined)).toMatchObject({ refused: { status: 400, body: { code: 'invalid_request' } } })
  // ViaVersion from Modrinth is the same add-on, so the plan leaves it out and has another fingerprint.
  const planned = (get('/project/hangar/12', withVia)?.body as Details).plan
  expect(planned.steps.map((s) => s.name)).toEqual(['ViaBackwards'])
  expect(install(recorded.fingerprint, withVia)).toMatchObject({ ends: { status: 'failed', detail: { notice: { kind: 'plan_changed' } } } })
  expect(install(planned.fingerprint, withVia)).toMatchObject({ ends: { status: 'succeeded', detail: { restartNeeded: false } } })

  expect(get('/project/modrinth/P1OZGk5p/removal', withVia)).toMatchObject({ status: 200, body: { addon: { name: 'ViaVersion' } } })
  expect(get('/project/hangar/12/removal', withVia)).toMatchObject({ status: 404, body: { code: 'not_managed' } })
  expect(plan({})).toMatchObject({ status: 409, body: { code: 'up_to_date', error: 'Every add-on is up to date.' } })
  expect(plan({ addons: [{ source: 'hangar', projectId: '12' }] }, withVia)).toMatchObject({ status: 404, body: { code: 'not_managed' } })
  expect(plan({ addons: 'all' })).toMatchObject({ status: 400, body: { code: 'invalid_request' } })

  const icon = get(`/icon?url=${encodeURIComponent('https://cdn.modrinth.com/data/P1OZGk5p/icon.png')}`)
  expect(icon?.headers['Content-Type']).toBe('image/png')
  expect((icon?.body as Buffer).subarray(1, 4).toString()).toBe('PNG')
  expect(get(`/icon?url=${encodeURIComponent('https://example.com/icon.png')}`)).toMatchObject({ status: 400, body: { code: 'host_not_allowed' } })

  expect(get('/project/modrinth/AAAAAAAA')).toBeUndefined()
  expect(isAddonRead('GET', '/api/servers/abc/addons/curated')).toBe(true)
  expect(isAddonRead('POST', '/api/servers/abc/addons/install')).toBe(false)
})

test("the add-on fixtures list Playkeeper's picks, and installs take Wave 3's start and Wave 4's openPorts", () => {
  const stopped: World = { addons: recordedFolder(), running: false }
  const viaVersion = { source: 'modrinth', projectId: 'P1OZGk5p', name: 'ViaVersion', versionId: 'FaishMnD', versionNumber: '5.12.0', published: '2026-09-18T15:01:59.741758Z', fileName: 'ViaVersion-5.12.0.jar' }
  const withVia: World = { addons: { ...recordedFolder(), files: [{ fileName: viaVersion.fileName, size: 6_503_775, status: 'managed', addon: viaVersion }] }, running: true }
  const get = (rest: string, world = stopped) => answerRead('GET', new URL(`https://panel/api/servers/abc/addons${rest}`), undefined, world)

  type Picks = { picks: { id: string; card: { name: string; installed: boolean }; permission?: string; ports?: { protocol: string; port: number }[] }[] }
  const picks = get('/curated')?.body as Picks
  expect(picks.picks.map((p) => p.id)).toEqual(['voice-chat', 'rollback', 'discord-chat', 'newer-clients', 'pregenerate', 'essentials', 'permissions'])
  expect(picks.picks[0]).toMatchObject({ card: { name: 'Simple Voice Chat', installed: false }, permission: 'https://modrepo.de/minecraft/voicechat/faq', ports: [{ protocol: 'udp', port: 24454 }] })
  expect((get('/curated', withVia)?.body as Picks).picks.filter((p) => p.card.installed).map((p) => p.id)).toEqual(['newer-clients'])
  const voice = get('/project/modrinth/9eGKb6K1')?.body as { ports?: unknown; plan: { fingerprint: string } }
  expect(voice.ports).toEqual([{ protocol: 'udp', port: 24454 }])
  expect((get('/project/modrinth/P1OZGk5p')?.body as { ports?: unknown }).ports).toBeUndefined()

  // Voice chat installs only with leave to open its port, which its job opens before restarting the server.
  const install = (body: Record<string, unknown>, world = stopped) => installJob({ source: 'modrinth', projectId: '9eGKb6K1', fingerprint: voice.plan.fingerprint, ...body }, world)
  expect(install({})).toMatchObject({ refused: { status: 400, body: { error: 'Voice chat needs a UDP port of its own, so Playkeeper installs it only when it may open that port too.' } } })
  expect(install({ openPorts: false })).toMatchObject({ refused: { status: 400 } })
  expect(install({ openPorts: true }, withVia)).toMatchObject({ ends: { status: 'succeeded', detail: { voiceChatPort: 24454, restartNeeded: false, files: [{ name: 'Simple Voice Chat' }] } } })
  expect(install({ openPorts: 'yes' })).toMatchObject({ refused: { status: 400, body: { error: 'Invalid request body.' } } })
  // The crash screen's fix installs with start: a stopped server starts instead of waiting for a restart.
  const essentials = get('/project/modrinth/hXiIvTyT')?.body as { plan: { fingerprint: string } }
  const withStart = installJob({ source: 'modrinth', projectId: 'hXiIvTyT', fingerprint: essentials.plan.fingerprint, start: true }, stopped)
  expect(withStart).toMatchObject({ ends: { status: 'succeeded' } })
  expect((withStart as { ends: { detail: Record<string, unknown> } }).ends.detail.restartNeeded).toBeUndefined()
  expect(installJob({ source: 'modrinth', projectId: 'hXiIvTyT', fingerprint: essentials.plan.fingerprint, start: 1 }, stopped)).toMatchObject({ refused: { status: 400 } })
  expect(installJob({ source: 'modrinth', projectId: 'hXiIvTyT', fingerprint: essentials.plan.fingerprint, keepConfig: true }, stopped)).toMatchObject({ refused: { body: { error: 'Invalid request body: json: unknown field "keepConfig"' } } })

  // An update takes start too, and what it would do doesn't.
  const outdated: World = { addons: { ...recordedFolder(), files: [{ fileName: 'ViaVersion-5.11.0.jar', size: 6_400_000, status: 'managed', addon: { ...viaVersion, versionId: 'older', versionNumber: '5.11.0', published: '2026-08-01T00:00:00Z', fileName: 'ViaVersion-5.11.0.jar' } }] }, running: false }
  const plan = answerRead('POST', new URL('https://panel/api/servers/abc/addons/update/plan'), {}, outdated)?.body as { fingerprint: string; steps: unknown[] }
  expect(plan.steps).toHaveLength(1)
  expect(updateJob({ fingerprint: plan.fingerprint, start: true }, outdated)).toMatchObject({ ends: { status: 'succeeded', detail: { files: [{ name: 'ViaVersion', was: '5.11.0' }] } } })
  expect(answerRead('POST', new URL('https://panel/api/servers/abc/addons/update/plan'), { start: true }, outdated)).toMatchObject({ status: 400, body: { error: 'Invalid request body: json: unknown field "start"' } })
})

test('the modpack fixtures answer every pack the library lists, from Modrinth and from CurseForge once there is a key', () => {
  const noKey: PackWorld = { curseforge: false }
  const keyed: PackWorld = { curseforge: true }
  const read = (rest: string, world = noKey) => answerModpackRead(new URL(`https://panel/api/machines/m1/modpacks${rest}`), world)
  type Results = { cards: { source: string; projectId: string; name: string }[]; total: number; limit: number; sources: string[] }
  type Detail = { projectId: string; newest?: string; versions: { id: string }[] }

  // What the picker asks: each sort's first page, then each pack's details and its newest version's preview.
  for (const source of ['modrinth', 'curseforge']) {
    for (const sort of ['downloads', 'relevance', 'updated', 'newest']) {
      const res = read(`?${new URLSearchParams({ source, sort, limit: '12' }).toString()}`, keyed)?.body as Results
      expect(res.cards.length, `${source} ${sort}`).toBeGreaterThan(0)
      expect(res.sources).toEqual(['modrinth', 'curseforge'])
      for (const c of res.cards) {
        expect(c.source).toBe(source)
        const d = read(`/${source}/${c.projectId}`, keyed)
        expect(d?.status, `${c.name}'s details`).toBe(200)
        const { newest, versions } = d?.body as Detail
        expect(versions.some((v) => v.id === newest), `${c.name}'s newest version is listed`).toBe(true)
        expect(read(`/${source}/${c.projectId}/versions/${newest}/preview`, keyed)?.status, `${c.name}'s preview`).toBe(200)
      }
    }
  }
  expect((read('?source=modrinth&sort=downloads&limit=12')?.body as Results).sources).toEqual(['modrinth'])
  expect((read('?sort=downloads&limit=12')?.body as Results).cards.map((c) => c.name)).toContain('Vanilla Perfected')
  const cobblemon = read('?q=cobblemon')?.body as Results
  expect(cobblemon.cards.map((c) => c.name)).toContain('Cobblemon Official Modpack [Fabric]')
  expect(cobblemon.cards.map((c) => c.name)).not.toContain('Vanilla Perfected')
  expect(cobblemon.total).toBe(cobblemon.cards.length)

  // CurseForge takes a key, which this machine may not have.
  const unavailable = { status: 409, body: { code: 'curseforge_unavailable' } }
  expect(read('?source=curseforge')).toMatchObject(unavailable)
  expect(read('/curseforge/9100001')).toMatchObject(unavailable)
  expect(read('/curseforge/9100001/versions/9200002/preview')).toMatchObject(unavailable)
  expect(read('/curseforge/9100001/versions/9200002/preview', keyed)).toMatchObject({ status: 200, body: { type: 'fabric', minecraftVersion: '26.2', ready: true } })
  expect(read('/curseforge/example-fabric-pack', keyed)).toMatchObject({ status: 400, body: { error: "The pack's project is not valid." } })

  // The agent's checks.
  expect(read('?source=hangar')).toMatchObject({ status: 400, body: { error: 'Packs come from Modrinth or CurseForge, not "hangar".' } })
  expect(read('?sort=popular')).toMatchObject({ status: 400, body: { code: 'invalid_request' } })
  expect(read('?limit=26')).toMatchObject({ status: 400, body: { error: 'That page of results is out of range.' } })
  expect(read('?offset=x')).toMatchObject({ status: 400, body: { error: 'offset must be a number.' } })
  expect(read('?type=paper')).toMatchObject({ status: 400 })
  expect(read('?version=1.20')).toMatchObject({ status: 400 })
  expect(read('?version=1.20.1')).toMatchObject({ status: 200 })
  expect(read('/hangar/abc')).toMatchObject({ status: 400, body: { error: 'Modpacks come from Modrinth or CurseForge.' } })
  expect(read('/modrinth/bad!id')).toMatchObject({ status: 400, body: { error: 'That is not a valid modpack id.' } })

  // Both sources' pack icons are drawn; CurseForge's file host serves no icons.
  for (const url of ['https://cdn.modrinth.com/data/1ocGzRHv/icon.png', 'https://media.forgecdn.net/avatars/thumbnails/900/1/256/256/logo.png']) {
    expect(read(`/icon?url=${encodeURIComponent(url)}`)?.headers['Content-Type'], url).toBe('image/png')
  }
  expect(read(`/icon?url=${encodeURIComponent('https://edge.forgecdn.net/files/9200/2/logo.png')}`)).toMatchObject({ status: 400, body: { code: 'host_not_allowed' } })

  // Nothing recorded, no answer.
  expect(read('/modrinth/AAAAAAAA')).toBeUndefined()
  expect(read('/modrinth/1ocGzRHv/versions/AAAAAAAA/preview')).toBeUndefined()
  expect(isModpackRead('GET', '/api/machines/m1/modpacks/modrinth/1ocGzRHv')).toBe(true)
  expect(isModpackRead('POST', '/api/machines/m1/modpacks')).toBe(false)
  expect(isModpackRead('GET', '/api/machines/m1/templates')).toBe(false)
})

test("the software fixtures answer NeoForge's and Forge's builds for every version they record, and lay those versions over the panel's catalog", () => {
  const at = (p: string) => new URL(p, 'https://127.0.0.1:8443')
  expect(recordedTypes).toEqual(['neoforge', 'forge'])
  for (const type of recordedTypes) {
    // As the panel answers when the type's Maven failed: its own memory, and no versions.
    const panel = { type, memoryOptionsMB: [4096, 6144], versions: [], versionsError: 'The type’s Maven does not have its version list.' }
    const catalog = layRecordedCatalog(at(`/api/machines/abcdefghjk/catalog?type=${type}&mods=3`), panel)
    expect(catalog).toMatchObject({ type, memoryOptionsMB: [4096, 6144] })
    expect(catalog).not.toHaveProperty('versionsError')
    const versions = (catalog?.versions ?? []) as { minecraftVersion: string; recommended: boolean }[]
    expect(versions.filter((v) => v.recommended), type).toHaveLength(1)
    for (const { minecraftVersion } of versions) {
      const read = at(`/api/machines/abcdefghjk/catalog/builds?type=${type}&version=${minecraftVersion}`)
      expect(isRecordedBuildsRead('GET', read)).toBe(true)
      expect(answerBuildsRead(read)).toMatchObject({ status: 200, body: { type, minecraftVersion } })
    }
  }
  expect(answerBuildsRead(at('/api/machines/abcdefghjk/catalog/builds?type=neoforge&version=1.7.10'))).toBeUndefined()
  expect(isRecordedBuildsRead('GET', at('/api/machines/abcdefghjk/catalog/builds?type=fabric&version=26.2'))).toBe(false)
  expect(layRecordedCatalog(at('/api/machines/abcdefghjk/catalog?type=paper'), { type: 'paper', versions: [] })).toBeUndefined()
  expect(layRecordedCatalog(at('/api/machines/abcdefghjk/catalog'), { type: 'paper', versions: [] })).toBeUndefined()
})

test('the view that looks after itself names each copy somewhere else the same on every read, as a real copy is', async () => {
  // The World tab keys a copy's row by its name, so a name that changed between reads would make the row new on every poll.
  const names = () => (lay('looks after itself', '/api/servers/abcdefghjk/offsite/copies', {}, 'https://127.0.0.1:8443') as { copies: { name: string }[] }).copies.map((c) => c.name)
  const first = names()
  expect(first).toHaveLength(3)
  await new Promise((resolve) => setTimeout(resolve, 1100))
  expect(names()).toEqual(first)
})

test('a faked add-on job downloads for a few seconds before it ends as the fixtures say, and other faked jobs have ended at once', () => {
  // The dialog polls once a second; a job that had ended by the first poll swapped its buttons under the crawl's press.
  const start = Date.parse('2026-09-27T00:00:00Z')
  const started = { id: 'fake-op-1', kind: 'addon-install', status: 'running', startedAt: new Date(start).toISOString() }
  const ends = { ...started, status: 'succeeded', detail: { files: [], restartNeeded: false } }
  for (const after of [0, 1000, 3000, addonJobMs - 1]) expect(addonOpAt(started, ends, start + after)?.status, `${after} ms in`).toBe('running')
  expect(addonOpAt(started, ends, start + addonJobMs)).toBe(ends)
  expect(addonJobMs).toBeGreaterThanOrEqual(3000)
  const backup = { id: 'fake-op-2', kind: 'backup', status: 'running', startedAt: new Date(start).toISOString() }
  expect(addonOpAt(backup, undefined, start)).toMatchObject({ status: 'succeeded' })
  expect(addonOpAt(undefined, undefined, start)).toBeUndefined()
})
