import { expect, type Locator, type Page } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

export const shotsDir = process.env.PK_SHOTS ?? path.resolve('out/screenshots')
export const outDir = process.env.PK_OUT ?? path.resolve('out')
export const password = process.env.PK_PASSWORD ?? ''

/**
 * Saves a screenshot. A full-page one stretches the viewport to the page's
 * height for the capture: Chromium's own full-page capture shrinks the
 * viewport to 1×1 for a moment, which crosses the phone breakpoint, remounts
 * the page and restarts its fade-in, and briefly takes focus from the page.
 * A screenshot of an open dialog that keyboard steps follow still uses
 * `fullPage: false`.
 */
export async function shot(page: Page, name: string, { fullPage = true } = {}) {
  fs.mkdirSync(shotsDir, { recursive: true })
  const file = path.join(shotsDir, `${name}.png`)
  const viewport = page.viewportSize()
  const height = fullPage && viewport ? await page.evaluate(() => Math.max(document.body.scrollHeight, document.documentElement.scrollHeight)) : 0
  if (!viewport || height <= viewport.height) {
    await page.screenshot({ path: file })
    return
  }
  await page.setViewportSize({ width: viewport.width, height })
  try {
    await page.screenshot({ path: file })
  } finally {
    await page.setViewportSize(viewport)
  }
}

/** Presses Tab until `target` has focus, like a keyboard-only user. */
export async function tabTo(page: Page, target: Locator, max = 60) {
  for (let i = 0; i < max; i++) {
    if (await target.evaluate((el) => el === document.activeElement).catch(() => false)) return
    await page.keyboard.press('Tab')
  }
  throw new Error(`could not reach ${target} with Tab`)
}

/** The signed-in dashboard: the sidebar on desktop, the Home or server header on phones. */
export function dashboard(page: Page): Locator {
  return page.getByRole('navigation', { name: 'Main' }).or(page.getByRole('navigation', { name: 'Server pages' })).or(page.getByRole('heading', { name: 'Home', level: 1 })).first()
}

const sessionFile = path.join(outDir, 'browser-session.json')

/** Signs in once per run and reuses the session cookie (logins are rate limited). */
export async function login(page: Page) {
  if (fs.existsSync(sessionFile)) {
    await page.context().addCookies(JSON.parse(fs.readFileSync(sessionFile, 'utf8')))
    await page.goto('/')
    const ok = await dashboard(page)
      .waitFor({ state: 'visible', timeout: 10_000 })
      .then(() => true)
      .catch(() => false)
    if (ok) return
    await page.context().clearCookies()
  }
  await page.goto('/login')
  await page.getByLabel('Username').fill('admin')
  await page.getByLabel('Password', { exact: true }).fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(dashboard(page)).toBeVisible()
  fs.mkdirSync(outDir, { recursive: true })
  fs.writeFileSync(sessionFile, JSON.stringify(await page.context().cookies()))
}

export interface ServerInfo {
  id: string
  name: string
  slug: string
}

/** The first server, as the dashboard's own API lists it. */
export async function firstServer(page: Page): Promise<ServerInfo> {
  const res = await page.request.get('/api/servers')
  expect(res.ok(), `GET /api/servers: ${res.status()}`).toBe(true)
  const list = (await res.json()) as ServerInfo[]
  expect(list.length, 'a server exists').toBeGreaterThan(0)
  return list[0] as ServerInfo
}
