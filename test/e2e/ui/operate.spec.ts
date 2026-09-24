import { expect, test } from '@playwright/test'
import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { login, outDir, shot } from './helpers'

// Day-to-day operation without a terminal: a console command, a backup and
// its download, all in the browser.
test('browser only: console command, backup and download', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await login(page)

  await page.goto('/console')
  await expect(page.getByRole('heading', { name: 'Console', level: 1 })).toBeVisible()
  const input = page.getByLabel('Minecraft command')
  await expect(input).toBeEnabled({ timeout: 60_000 })
  await input.fill('list')
  await page.getByRole('button', { name: 'Send' }).click()
  await expect(page.locator('.console-output')).toContainText('There are')
  await shot(page, 'operate-console-desktop')

  await page.goto('/world')
  await expect(page.getByRole('heading', { name: 'World', level: 1 })).toBeVisible()
  await page.getByLabel('Note (optional)').fill('browser backup')
  await page.getByRole('button', { name: 'Back up now' }).click()
  await page.getByRole('dialog', { name: 'Back up now?' }).getByRole('button', { name: 'Stop, back up and restart' }).click()
  await expect(page.getByText('Backup finished')).toBeVisible({ timeout: 10 * 60_000 })
  const row = page.getByRole('row').filter({ hasText: 'browser backup' })
  await expect(row.getByText('Verified')).toBeVisible()
  await shot(page, 'operate-backup-finished-desktop')

  const [download] = await Promise.all([page.waitForEvent('download'), row.getByRole('link', { name: 'Download' }).click()])
  fs.mkdirSync(outDir, { recursive: true })
  const file = path.join(outDir, download.suggestedFilename())
  await download.saveAs(file)
  const sha = crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex')
  const recorded = await row.locator('div.muted.small[title]').getAttribute('title')
  expect(sha, 'downloaded file matches the SHA-256 shown for the backup').toBe(recorded)
  fs.writeFileSync(path.join(outDir, 'browser-download.txt'), `${download.suggestedFilename()} sha256 ${sha} (matches the backup record)\n`)
})
