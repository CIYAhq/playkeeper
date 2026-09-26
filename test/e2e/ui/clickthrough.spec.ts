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
//
// Writes go to realistic fakes (fakes.ts), so nothing is restarted, deleted or
// downloaded. The only real write is one AI agent token, made before the
// crawl so Settings › AI agents has a token to open and revoke (revoking goes
// to the fakes too). There is no list of exceptions: a control that should do nothing
// right now must be disabled and say why (aria-describedby or a title). The
// selected tab or option of a group may stay selected. Each page gets a fresh
// load before a control is pressed unless the page is provably unchanged.

test.describe.configure({ mode: 'parallel' })

const sizes = {
  desktop: { viewport: { width: 1440, height: 900 } },
  phone: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true },
} as const

async function routes(page: Page, phone: boolean): Promise<string[]> {
  const servers = (await (await page.request.get('/api/servers')).json()) as { slug: string }[]
  const machines = (await (await page.request.get('/api/machines')).json()) as { id: string; kind: string }[]
  const out = ['/']
  for (const s of servers) for (const tab of ['', '/console', '/players', '/world', '/settings']) out.push(`/servers/${s.slug}${tab}`)
  out.push('/servers/new')
  // A joined machine's page is in Settings › Machines; the dashboard's own has its own page.
  for (const m of machines) out.push(m.kind === 'remote' ? `/settings/machines/${m.id}` : `/machines/${m.id}`)
  out.push('/settings', '/settings/ai-agents', '/settings/machines')
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

for (const [name, size] of Object.entries(sizes)) {
  test(`every control does something on ${name}`, async ({ browser, baseURL }) => {
    const base = baseURL ?? ''
    const options = { ...size, ignoreHTTPSErrors: true, locale: 'en-GB', timezoneId: 'UTC' }
    const report: CrawlReport = { results: [], notes: [] }
    const log = (line: string) => console.log(line)

    const signedOut = await browser.newContext(options)
    const outPage = await signedOut.newPage()
    const outCrawler = new Crawler(outPage, name, base, log)
    await outCrawler.init()
    await outCrawler.crawl('/login')
    report.results.push(...outCrawler.results)
    report.notes.push(...outCrawler.notes)
    await signedOut.close()

    const context = await browser.newContext(options)
    const page = await context.newPage()
    await login(page)
    await seedAiToken(page)
    const crawler = new Crawler(page, name, base, log)
    await crawler.init()
    for (const route of await routes(page, name === 'phone')) await crawler.crawl(route)
    report.results.push(...crawler.results)
    report.notes.push(...crawler.notes)
    await context.close()

    fs.mkdirSync(outDir, { recursive: true })
    fs.writeFileSync(path.join(outDir, `clickthrough-${name}.json`), JSON.stringify(report, null, 2))
    console.log(`${name}: ${report.results.length} controls: ${summary(report)}`)
    for (const n of report.notes) console.log(`note: ${n}`)
    expect(report.results.length, 'controls found').toBeGreaterThan(20)
    expect(report.results.filter((r) => failing.includes(r.status)).length, failureList(report.results)).toBe(0)
  })
}
