import { expect, test } from '@playwright/test'
import { login, shot } from './helpers'

// Every page of the dashboard, at desktop width: it shows a heading, throws
// nothing, logs no error, and no API call it makes fails with a server error
// or not at all. 4xx answers are listed but allowed: a page may ask for
// something that isn't set up yet. So are 502 and 503 answers whose code says
// a service is out of reach: scripts/e2e/vm-release.sh, which runs this,
// keeps the names service and Discord out of reach on purpose.
test('every page loads', async ({ page }) => {
  test.setTimeout(15 * 60_000)
  let at = ''
  const problems: string[] = []
  const notes: string[] = []
  const unreachable: string[] = []
  const pending: Promise<void>[] = []
  page.on('pageerror', (e) => problems.push(`${at}: uncaught ${e.message}`))
  page.on('console', (m) => {
    if (m.type() !== 'error') return
    if (m.text().startsWith('Failed to load resource')) notes.push(`${at}: ${m.text()}`)
    else problems.push(`${at}: console error: ${m.text()}`)
  })
  page.on('response', (r) => {
    const u = new URL(r.url())
    if (!u.pathname.startsWith('/api/')) return
    const what = `${at}: ${r.status()} ${r.request().method()} ${u.pathname}`
    if (r.status() >= 500) {
      pending.push(
        r
          .json()
          .catch(() => ({}))
          .then((b: { code?: string }) => {
            if (r.status() !== 500 && /_unreachable$/.test(b.code ?? '')) unreachable.push(`${what} (${b.code})`)
            else problems.push(what)
          }),
      )
    } else if (r.status() >= 400) notes.push(what)
  })
  page.on('requestfailed', (r) => {
    const why = r.failure()?.errorText ?? ''
    if (!why.includes('ERR_ABORTED')) problems.push(`${at}: ${r.method()} ${new URL(r.url()).pathname} failed: ${why}`)
  })

  await page.setViewportSize({ width: 1440, height: 900 })
  await login(page)
  const machine = ((await (await page.request.get('/api/machines')).json()) as { id: string }[])[0]
  const s = ((await (await page.request.get('/api/servers')).json()) as { id: string; slug: string }[])[0]
  const sessions = (await (await page.request.get(`/api/servers/${s.id}/players/sessions?range=7d`)).json()) as { sessions?: { player: string }[] }
  const player = sessions.sessions?.[0]?.player
  const server = `/servers/${s.slug}`
  const routes = [
    '/',
    '/servers/new',
    server,
    `${server}/running`,
    `${server}/console`,
    `${server}/players`,
    ...(player ? [`${server}/players/${encodeURIComponent(player)}`] : []),
    `${server}/world`,
    `${server}/world/pregen`,
    `${server}/world/packs`,
    `${server}/world/backup-rules`,
    `${server}/world/backup-rules/copies`,
    `${server}/map`,
    `${server}/plugins`,
    `${server}/plugins/browse`,
    `${server}/settings`,
    `${server}/settings/schedules`,
    `/machines/${machine.id}`,
    `/machines/${machine.id}/settings`,
    `/machines/${machine.id}/disk`,
    '/settings',
    '/settings/team',
    '/settings/addon-sources',
    '/settings/discord',
    '/settings/ai-agents',
    '/settings/machines',
    `/settings/machines/${machine.id}`,
    '/account',
    '/account/two-factor',
    '/recover',
  ]
  for (const route of routes) {
    at = route
    await page.goto(route)
    await expect.soft(page.getByRole('heading').first(), `${route} shows a heading`).toBeVisible({ timeout: 30_000 })
    await page.waitForTimeout(1500)
    await shot(page, `page${route.replace(/[^a-z0-9]+/gi, '-').replace(/-$/, '') || '-home'}`)
    console.log(`loaded ${route}`)
  }
  await Promise.all(pending)
  console.log(notes.length ? `4xx answers and failed loads the browser logged (allowed):\n${notes.join('\n')}` : 'no 4xx answers')
  console.log(unreachable.length ? `services out of reach in the lab (expected):\n${unreachable.join('\n')}` : 'no service was out of reach')
  expect(problems, problems.join('\n')).toEqual([])
})
