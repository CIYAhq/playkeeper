import { expect, test, type Page } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { Crawler, failing, failureList, type CrawlReport } from './crawl'
import { login, outDir } from './helpers'

// Every control works. The click-through opens every page of the seeded
// dashboard at desktop and phone sizes, finds every button, link, switch, tab,
// menu item and list option by role (including the ones inside menus, dialogs,
// sheets and later steps), presses each one and checks that something a
// person could notice happened: the page changed, a dialog, menu or sheet
// opened or closed, the control's own state changed, focus moved, the page
// scrolled, something was copied, a toast appeared or a request went out.
// The sign-in page, a friends' pack link that opens nothing and the shared
// map pages (a shared map, and a link no map has) are opened signed out. A
// world file picker gets a small archive, so the world upload's later steps
// are pressed too.
//
// Writes go to realistic fakes (fakes.ts), so nothing is restarted, deleted or
// downloaded. There is no list of exceptions: a control that should do nothing
// right now must be disabled and say why (aria-describedby or a title). The
// selected tab or option of a group may stay selected. A link another app
// opens (an authenticator's otpauth:, mailto:, tel:) counts as working, since
// a headless browser has no app to open. Each page gets a fresh load before a
// control is pressed unless the page is provably unchanged.

test.describe.configure({ mode: 'parallel' })

const sizes = {
  desktop: { viewport: { width: 1440, height: 900 } },
  phone: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true },
} as const

// The add-on tab each server type has (web/src/lib/addons.ts); Vanilla has none.
const addonTabs: Record<string, string> = { paper: '/plugins', purpur: '/plugins', fabric: '/mods', quilt: '/mods', neoforge: '/mods' }

// A well-formed share link that no map has, for the "isn't available" page.
const unknownMapLink = '/map/Zz9xWv8uTs7rQp6oNm5lKj'
// A friends' pack link that opens nothing: the page every unavailable link gets.
const unknownPackLink = '/packs/Pk0Unknown0Link0Abcdef'

/** The pages to open signed in, and the ones anyone can open without signing in. */
async function routes(page: Page, phone: boolean): Promise<{ signedIn: string[]; signedOut: string[] }> {
  const servers = (await (await page.request.get('/api/servers')).json()) as { id: string; slug: string; type?: string }[]
  const machines = (await (await page.request.get('/api/machines')).json()) as { id: string }[]
  const out = ['/']
  const shared: string[] = []
  for (const s of servers) {
    const res = await page.request.get(`/api/servers/${s.id}/map`)
    const map = (res.ok() ? await res.json() : {}) as { supported?: boolean; public?: boolean; path?: string }
    if (map.public && map.path) shared.push(map.path)
    const addons = addonTabs[s.type ?? '']
    for (const tab of ['', '/console', '/players', '/world', ...(map.supported ? ['/map'] : []), ...(addons ? [addons] : []), '/settings']) out.push(`/servers/${s.slug}${tab}`)
  }
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
  for (const m of machines) out.push(`/machines/${m.id}`, `/machines/${m.id}/settings`)
  out.push('/settings', '/account', '/account/two-factor')
  if (phone) out.push('/more')
  return { signedIn: out, signedOut: ['/login', unknownPackLink, unknownMapLink, ...shared] }
}

function summary(report: CrawlReport): string {
  const counts = new Map<string, number>()
  for (const r of report.results) counts.set(r.status, (counts.get(r.status) ?? 0) + 1)
  return [...counts].map(([s, n]) => `${n} ${s}`).join(', ')
}

for (const [name, size] of Object.entries(sizes)) {
  test(`every control does something on ${name}`, async ({ browser, baseURL }) => {
    const base = baseURL ?? ''
    const options = { ...size, ignoreHTTPSErrors: true, locale: 'en-GB', timezoneId: 'UTC' }
    const report: CrawlReport = { results: [], notes: [] }
    const log = (line: string) => console.log(line)

    const context = await browser.newContext(options)
    const page = await context.newPage()
    await login(page)
    const { signedIn, signedOut } = await routes(page, name === 'phone')
    const crawler = new Crawler(page, name, base, log)
    await crawler.init()
    for (const route of signedIn) await crawler.crawl(route)
    report.results.push(...crawler.results)
    report.notes.push(...crawler.notes)
    await context.close()

    const outContext = await browser.newContext(options)
    const outPage = await outContext.newPage()
    const outCrawler = new Crawler(outPage, name, base, log)
    await outCrawler.init()
    for (const route of signedOut) await outCrawler.crawl(route)
    report.results.push(...outCrawler.results)
    report.notes.push(...outCrawler.notes)
    await outContext.close()

    fs.mkdirSync(outDir, { recursive: true })
    fs.writeFileSync(path.join(outDir, `clickthrough-${name}.json`), JSON.stringify(report, null, 2))
    console.log(`${name}: ${report.results.length} controls: ${summary(report)}`)
    for (const n of report.notes) console.log(`note: ${n}`)
    expect(report.results.length, 'controls found').toBeGreaterThan(20)
    expect(report.results.filter((r) => failing.includes(r.status)).length, failureList(report.results)).toBe(0)
  })
}
