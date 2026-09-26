import { expect, test, type Page } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { Crawler, failing, failureList, where, type CrawlReport, type Result, type Status } from './crawl'
import type { View } from './fakes'
import { login, outDir } from './helpers'

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
// /welcome), a Playkeeper update to install, and first-run setup.
//
// Writes go to realistic fakes (fakes.ts), so nothing is restarted, deleted or
// downloaded. The only real write is one AI agent token, made before the
// crawl so Settings › AI agents has a token to open and revoke (revoking goes
// to the fakes too). There is no list of exceptions: a control that should do nothing
// right now must be disabled and say why (aria-describedby or a title). The
// selected tab or option of a group may stay selected. A link another app
// opens (an authenticator's otpauth:, mailto:, tel:) counts as working, since
// a headless browser has no app to open. Each page gets a fresh load before a
// control is pressed unless the page is provably unchanged.
//
// It fails when a control does nothing visible, answers with an error, is
// disabled without a reason or has no name; when it can't get back to a state
// it found; when a page has fewer controls than its minimum; and when it
// didn't press the control of one of `places`. For each place, a negative
// control then breaks that control and presses it again: the crawl must
// report it.

test.describe.configure({ mode: 'parallel' })

const sizes = {
  desktop: { viewport: { width: 1440, height: 900 } },
  phone: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true },
} as const

// The add-on tab each server type has (web/src/lib/addons.ts); Vanilla has none.
const addonTabs: Record<string, string> = { paper: '/plugins', purpur: '/plugins', fabric: '/mods', quilt: '/mods', neoforge: '/mods' }

async function routes(page: Page, phone: boolean): Promise<string[]> {
  const servers = (await (await page.request.get('/api/servers')).json()) as { id: string; slug: string; type?: string }[]
  const machines = (await (await page.request.get('/api/machines')).json()) as { id: string; kind: string }[]
  const out = ['/']
  for (const s of servers) {
    const addons = addonTabs[s.type ?? '']
    for (const tab of ['', '/console', '/players', '/world', ...(addons ? [addons] : []), '/settings']) out.push(`/servers/${s.slug}${tab}`)
  }
  out.push('/servers/new')
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
  // A joined machine's page is in Settings › Machines; the dashboard's own has its own page.
  for (const m of machines) out.push(m.kind === 'remote' ? `/settings/machines/${m.id}` : `/machines/${m.id}`, `/machines/${m.id}/settings`)
  out.push('/settings', '/settings/ai-agents', '/settings/machines', '/account', '/account/two-factor')
  if (phone) out.push('/more')
  return out
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

function summary(report: CrawlReport): string {
  const counts = new Map<string, number>()
  for (const r of report.results) counts.set(r.status, (counts.get(r.status) ?? 0) + 1)
  return [...counts].map(([s, n]) => `${n} ${s}`).join(', ')
}

type Size = keyof typeof sizes

interface Crawl {
  route: string
  view: View
}

/** After the live pages: the pages each faked state changes, for the first server in `live`. */
function fakedCrawls(live: string[], phone: boolean): Crawl[] {
  const first = live.find((r) => /^\/servers\/(?!new$)[^/]+$/.test(r))
  const byView: [View, string[]][] = [
    ['stopped', first ? ['/', first, `${first}/console`, `${first}/settings`] : []],
    ['crashed', first ? ['/', first] : []],
    // A phone's server header has no Restart or Stop to disable.
    ['busy', first ? [...(phone ? [] : [first]), `${first}/world`] : []],
    ['empty lists', first ? [`${first}/players`, `${first}/world`] : []],
    ['no servers', ['/', '/welcome']],
    ['update available', phone ? ['/settings', '/more'] : ['/settings']],
  ]
  return byView.flatMap(([view, pages]) => pages.map((route) => ({ route, view })))
}

/** A page as the minimums name it: its route without a server's slug or a machine's id, and its view. */
function pageOf(c: { route: string; view?: View }): string {
  const route = c.route
    .replace(/^\/servers\/(?!new$)[^/]+/, '/servers/*')
    .replace(/^\/machines\/[^/]+/, '/machines/*')
    .replace(/^\/settings\/machines\/[^/]+/, '/settings/machines/*')
  return where(route, c.view)
}

/**
 * The fewest controls a page must have pressed, a little under what it has
 * now, so a page that stops showing its controls fails even when nothing on
 * it is broken. A page's count leaves out controls pressed on an earlier
 * page, such as the sidebar. A page that isn't listed needs one. A dev build
 * (make dev) can't update itself, so its /settings has no "Check for updates"
 * and one control fewer than an installed panel's.
 */
const minimums: Record<Size, Record<string, number>> = {
  desktop: {
    '/login': 3,
    '/setup (first run)': 1,
    '/': 26,
    '/servers/*': 21,
    '/servers/*/console': 12,
    '/servers/*/players': 9,
    '/servers/*/world': 14,
    '/servers/*/settings': 36,
    '/servers/new': 36,
    '/machines/*': 3,
    '/machines/*/settings': 6,
    '/settings': 2,
    '/account': 7,
    '/account/two-factor': 4,
    '/ (stopped)': 3,
    '/servers/* (stopped)': 1,
    '/servers/*/console (stopped)': 3,
    '/servers/*/settings (stopped)': 1,
    '/ (crashed)': 1,
    '/servers/* (crashed)': 3,
    '/servers/* (busy)': 2,
    '/servers/*/world (busy)': 1,
    '/servers/*/players (empty lists)': 1,
    '/servers/*/world (empty lists)': 1,
    '/ (no servers)': 1,
    '/welcome (no servers)': 11,
    '/settings (update available)': 5,
  },
  phone: {
    '/login': 3,
    '/setup (first run)': 1,
    '/': 5,
    '/servers/*': 26,
    '/servers/*/console': 9,
    '/servers/*/players': 5,
    '/servers/*/world': 9,
    '/servers/*/settings': 34,
    '/servers/new': 29,
    '/machines/*': 1,
    '/machines/*/settings': 6,
    '/settings': 1,
    '/account': 6,
    '/account/two-factor': 1,
    '/more': 5,
    '/ (stopped)': 1,
    '/servers/* (stopped)': 2,
    '/servers/*/console (stopped)': 3,
    '/servers/*/settings (stopped)': 1,
    '/ (crashed)': 1,
    '/servers/* (crashed)': 3,
    '/servers/*/world (busy)': 1,
    '/servers/*/players (empty lists)': 1,
    '/servers/*/world (empty lists)': 1,
    '/ (no servers)': 1,
    '/welcome (no servers)': 15,
    '/settings (update available)': 3,
    '/more (update available)': 1,
  },
}

interface Place {
  /** What it is, for the report. */
  what: string
  sizes: Size[]
  view?: View
  /** The control that must be pressed there. */
  key: RegExp
  /** How it must come out; the default is that it works. */
  status?: Status
}

/**
 * Places the click-through didn't reach before 0.3.1's audit. In each, one
 * control must come out as `status`, and a negative control breaks it and
 * presses it again (a disabled one loses its reason instead): the crawl must
 * then report it.
 */
const places: Place[] = [
  { what: '"Restore this backup?", a dialog that replaces the menu or sheet it opens from', sizes: ['desktop', 'phone'], key: /^button "Cancel" in dialog "Restore this backup\?"$/ },
  { what: 'the typed confirmation in "Restore this backup?"', sizes: ['desktop', 'phone'], key: /^button "Replace the world and restore" in dialog "Restore this backup\?"$/ },
  { what: '"Delete this backup?", which replaces its menu', sizes: ['desktop'], key: /^button "Delete backup" in dialog "Delete this backup\?"$/ },
  { what: 'the phone’s "Update to" sheet, which replaces its dialog', sizes: ['phone'], key: /^option ".+" in listbox "Update to"$/ },
  { what: 'the view distance slider', sizes: ['desktop'], key: /^slider "View distance"/ },
  { what: 'the memory slider in New server', sizes: ['desktop', 'phone'], key: /^slider "Memory for this server"/ },
  { what: 'the last step of New server', sizes: ['desktop', 'phone'], key: /^button "Create and start / },
  { what: 'Start on a stopped server', sizes: ['desktop', 'phone'], view: 'stopped', key: /^button "Start"$/ },
  { what: 'the fix on a crashed server', sizes: ['desktop', 'phone'], view: 'crashed', key: /^button "Save and start .+" in "How to fix it"$/ },
  { what: 'Restart while a backup runs', sizes: ['desktop'], view: 'busy', key: /^button "Restart" \[disabled\]$/, status: 'disabled with a reason' },
  { what: 'the empty Players page', sizes: ['desktop', 'phone'], view: 'empty lists', key: /^button "Add player"$/ },
  { what: 'the empty World page', sizes: ['desktop', 'phone'], view: 'empty lists', key: /^button "Make my first backup"$/ },
  { what: 'Home with no servers', sizes: ['desktop', 'phone'], view: 'no servers', key: /^link "(Next: )?Create your first server"$/ },
  { what: 'the end of onboarding (/welcome)', sizes: ['desktop', 'phone'], view: 'no servers', key: /^button "Create my server"$/ },
  { what: 'installing a Playkeeper update', sizes: ['desktop', 'phone'], view: 'update available', key: /^button "Update( now)?" in dialog "Update Playkeeper to .+"$/ },
  { what: 'first-run setup', sizes: ['desktop', 'phone'], view: 'first run', key: /^button "Create account and continue"$/ },
]

interface Rules {
  minimums: Record<string, number>
  places: Place[]
}

/** Everything that fails a run: failing controls, states it couldn't get back to, pages under their minimum and places it didn't reach. */
function passBar(size: Size, report: CrawlReport, pages: string[], rules: Rules = { minimums: minimums[size], places }): string[] {
  const problems: string[] = []
  const failures = failureList(report.results)
  if (failures) problems.push(failures)
  problems.push(...report.unreached)
  const counts = new Map<string, number>(pages.map((p) => [p, 0]))
  for (const r of report.results) counts.set(pageOf(r), (counts.get(pageOf(r)) ?? 0) + 1)
  for (const [p, n] of counts) {
    const min = rules.minimums[p] ?? 1
    if (n < min) problems.push(`${size} ${p}: ${n} control${n === 1 ? '' : 's'} pressed, fewer than its minimum of ${min}`)
  }
  for (const [p, min] of Object.entries(rules.minimums)) if (!counts.has(p)) problems.push(`${size} ${p}: not crawled (its minimum is ${min})`)
  for (const place of rules.places) if (place.sizes.includes(size) && !found(report.results, place)) problems.push(`${size}: never pressed ${place.what} (${place.key})`)
  return problems
}

function found(results: Result[], place: Place): Result | undefined {
  return results.find((r) => (r.view ?? 'live') === (place.view ?? 'live') && place.key.test(r.key) && r.status === (place.status ?? 'works'))
}

interface Negative {
  place: string
  /** Where the broken control was, and its key. */
  key: string
  /** What the crawl said about it once broken. */
  verdict: string
  caught: boolean
}

/** Breaks the control of each place this crawler reached and presses it again. */
async function negativeControls(crawler: Crawler, size: Size, signedIn: boolean): Promise<Negative[]> {
  const out: Negative[] = []
  for (const place of places) {
    if (!place.sizes.includes(size) || (place.view === 'first run') === signedIn) continue
    const hit = found(crawler.results, place)
    if (!hit) continue
    const r = await crawler.breakAndPress(hit, hit.status === 'disabled with a reason' ? 'unexplained' : 'does nothing')
    const verdict = typeof r === 'string' ? r : r.status
    out.push({ place: place.what, key: `${where(hit.route, hit.view)}${hit.via.length ? ` › ${hit.via.join(' › ')}` : ''} › ${hit.key}`, verdict, caught: typeof r !== 'string' && failing.includes(r.status) })
  }
  return out
}

for (const [name, size] of Object.entries(sizes)) {
  test(`every control does something on ${name}`, async ({ browser, baseURL }) => {
    const base = baseURL ?? ''
    const options = { ...size, ignoreHTTPSErrors: true, locale: 'en-GB', timezoneId: 'UTC' }
    const report: CrawlReport = { results: [], notes: [], unreached: [] }
    const log = (line: string) => console.log(line)
    const pages: string[] = []
    const negatives: Negative[] = []

    const signedOut = await browser.newContext(options)
    const outPage = await signedOut.newPage()
    const outCrawler = new Crawler(outPage, name, base, log)
    await outCrawler.init()
    for (const c of [{ route: '/login', view: 'live' }, { route: '/setup', view: 'first run' }] satisfies Crawl[]) {
      await outCrawler.crawl(c.route, c.view)
      pages.push(pageOf(c))
    }
    // A friends' pack link that opens nothing: the page every unavailable link gets. It has
    // no controls, so it isn't one of the pages with a minimum.
    await outCrawler.crawl('/packs/Pk0Unknown0Link0Abcdef')
    negatives.push(...(await negativeControls(outCrawler, name as Size, false)))
    report.results.push(...outCrawler.results)
    report.notes.push(...outCrawler.notes)
    report.unreached.push(...outCrawler.unreached)
    await signedOut.close()

    const context = await browser.newContext(options)
    const page = await context.newPage()
    await login(page)
    await seedAiToken(page)
    const crawler = new Crawler(page, name, base, log)
    await crawler.init()
    const live = await routes(page, name === 'phone')
    for (const c of [...live.map((route): Crawl => ({ route, view: 'live' })), ...fakedCrawls(live, name === 'phone')]) {
      await crawler.crawl(c.route, c.view)
      pages.push(pageOf(c))
    }
    negatives.push(...(await negativeControls(crawler, name as Size, true)))
    report.results.push(...crawler.results)
    report.notes.push(...crawler.notes)
    report.unreached.push(...crawler.unreached)
    await context.close()

    fs.mkdirSync(outDir, { recursive: true })
    fs.writeFileSync(path.join(outDir, `clickthrough-${name}.json`), JSON.stringify({ ...report, negatives }, null, 2))
    console.log(`${name}: ${report.results.length} controls: ${summary(report)}`)
    for (const n of report.notes) console.log(`note: ${n}`)
    for (const n of negatives) console.log(`negative control: ${n.caught ? 'caught' : 'MISSED'} ${n.place}: ${n.key} broken → ${n.verdict}`)
    const problems = passBar(name as Size, report, [...new Set(pages)])
    for (const n of negatives) if (!n.caught) problems.push(`${name}: with ${n.place} broken, the crawl said "${n.verdict}" (${n.key})`)
    expect(problems, problems.join('\n')).toEqual([])
  })
}

test('the pass bar fails a failing control, a state it could not get back to, a page under its minimum and a place it missed', () => {
  const works = (route: string, key: string): Result => ({ viewport: 'desktop', route, via: [], key, status: 'works', effects: ['changed the page'], problems: [] })
  const rules: Rules = { minimums: { '/': 2, '/servers/* (stopped)': 1 }, places: [{ what: 'a slider', sizes: ['desktop'], key: /^slider / }] }
  const pages = ['/', '/servers/* (stopped)', '/settings']
  const good: CrawlReport = { results: [works('/', 'button "Home"'), works('/', 'slider "Memory"'), { ...works('/servers/survival', 'button "Start"'), view: 'stopped' }, works('/settings', 'button "Sign out"')], notes: [], unreached: [] }
  expect(passBar('desktop', good, pages, rules)).toEqual([])

  const dead: Result = { ...works('/settings', 'button "Check now"'), status: 'dead' }
  expect(passBar('desktop', { ...good, results: [...good.results, dead] }, pages, rules).join('\n')).toContain('button "Check now": pressing it did nothing visible')
  expect(passBar('desktop', { ...good, unreached: ['[desktop] /: could not reach button "Menu" again'] }, pages, rules)).toEqual(['[desktop] /: could not reach button "Menu" again'])
  expect(passBar('desktop', { ...good, results: good.results.filter((r) => r.key !== 'button "Home"') }, pages, rules)).toEqual(['desktop /: 1 control pressed, fewer than its minimum of 2'])
  expect(passBar('desktop', { ...good, results: good.results.filter((r) => r.view !== 'stopped') }, ['/', '/settings'], rules)).toEqual(['desktop /servers/* (stopped): not crawled (its minimum is 1)'])
  expect(passBar('desktop', { ...good, results: good.results.filter((r) => r.key !== 'button "Sign out"') }, pages, rules)).toEqual(['desktop /settings: 0 controls pressed, fewer than its minimum of 1'])
  expect(passBar('desktop', { ...good, results: [...good.results.filter((r) => !r.key.startsWith('slider')), works('/', 'button "More"')] }, pages, rules)).toEqual(['desktop: never pressed a slider (/^slider /)'])
})
