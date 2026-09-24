import { expect, type Locator, type Page } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

export const shotsDir = process.env.PK_SHOTS ?? path.resolve('out/screenshots')
export const outDir = process.env.PK_OUT ?? path.resolve('out')
export const password = process.env.PK_PASSWORD ?? ''

export async function shot(page: Page, name: string) {
  fs.mkdirSync(shotsDir, { recursive: true })
  await page.screenshot({ path: path.join(shotsDir, `${name}.png`), fullPage: true })
}

/** Presses Tab until `target` has focus, like a keyboard-only user. */
export async function tabTo(page: Page, target: Locator, max = 60) {
  for (let i = 0; i < max; i++) {
    if (await target.evaluate((el) => el === document.activeElement).catch(() => false)) return
    await page.keyboard.press('Tab')
  }
  throw new Error(`could not reach ${target} with Tab`)
}

const sessionFile = path.join(outDir, 'browser-session.json')

/** Signs in once per run and reuses the session cookie (logins are rate limited). */
export async function login(page: Page) {
  if (fs.existsSync(sessionFile)) {
    await page.context().addCookies(JSON.parse(fs.readFileSync(sessionFile, 'utf8')))
    await page.goto('/')
    const ok = await page
      .locator('.sidebar, .wizard')
      .first()
      .waitFor({ state: 'visible', timeout: 10_000 })
      .then(() => true)
      .catch(() => false)
    if (ok) return
    await page.context().clearCookies()
  }
  await page.goto('/login')
  await page.getByLabel('Username').fill('admin')
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()
  // Signed in: the dashboard, or the setup wizard when no server exists yet.
  await expect(page.locator('.sidebar, .wizard').first()).toBeVisible()
  fs.mkdirSync(outDir, { recursive: true })
  fs.writeFileSync(sessionFile, JSON.stringify(await page.context().cookies()))
}
