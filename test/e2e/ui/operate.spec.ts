import { expect, test } from '@playwright/test'
import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { firstServer, login, outDir, shot } from './helpers'

// Day-to-day operation without a terminal: a console command, a backup and
// its download, all in the browser.
test('browser only: console command, backup and download', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await login(page)
  const s = await firstServer(page)

  await page.goto(`/servers/${s.slug}/console`)
  await expect(page.getByRole('heading', { name: s.name, level: 1 })).toBeVisible()
  const input = page.getByLabel('Minecraft command')
  await expect(input).toBeEnabled({ timeout: 60_000 })
  await input.fill('list')
  await page.getByRole('button', { name: 'Send' }).click()
  await expect(page.getByRole('log', { name: 'Server output' })).toContainText('There are')
  await shot(page, 'operate-console-desktop')

  await page.goto(`/servers/${s.slug}/world`)
  await expect(page.getByRole('heading', { name: s.name, level: 1 })).toBeVisible()
  const note = page.getByLabel('Note for this backup')
  if (await note.isVisible()) {
    await note.fill('browser backup')
    await page.getByRole('button', { name: 'Back up now' }).click()
  } else {
    await page.getByRole('button', { name: 'Make my first backup' }).click()
  }
  await expect(page.getByText('Backup finished')).toBeVisible({ timeout: 10 * 60_000 })
  const row = page.getByRole('row').nth(1)
  await expect(row.getByText('Verified')).toBeVisible()
  await shot(page, 'operate-backup-finished-desktop')

  const [download] = await Promise.all([page.waitForEvent('download'), row.getByRole('link', { name: 'Download' }).click()])
  fs.mkdirSync(outDir, { recursive: true })
  const file = path.join(outDir, download.suggestedFilename())
  await download.saveAs(file)
  const sha = crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex')
  const backups = (await (await page.request.get(`/api/servers/${s.id}/backups`)).json()) as { fileName: string; sha256: string }[]
  const recorded = backups.find((b) => b.fileName === download.suggestedFilename())?.sha256
  expect(sha, 'downloaded file matches the SHA-256 recorded for the backup').toBe(recorded)
  fs.writeFileSync(path.join(outDir, 'browser-download.txt'), `${download.suggestedFilename()} sha256 ${sha} (matches the backup record)\n`)
})
