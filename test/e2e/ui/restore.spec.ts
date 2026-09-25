import { expect, test } from '@playwright/test'
import path from 'node:path'
import { login, shot, shotsDir } from './helpers'

test.use({ video: { mode: 'on', size: { width: 390, height: 844 } } })

// On a fresh second host: restore a downloaded Playkeeper backup as the
// host's first server, entirely in the browser, at phone width.
test('second host: restore a backup as a new server', async ({ page }) => {
  const archive = process.env.PK_ARCHIVE ?? ''
  expect(archive, 'PK_ARCHIVE').not.toBe('')
  await page.setViewportSize({ width: 390, height: 844 })
  await login(page)
  await expect(page.getByRole('heading', { name: 'No servers yet' })).toBeVisible()
  await shot(page, 'restore-1-empty-narrow')

  await page.goto('/servers/new')
  await page.getByRole('button', { name: 'Restore it as a new server' }).click()
  const pick = page.getByRole('dialog', { name: 'Start from a backup instead' })
  await expect(pick).toBeVisible()
  await pick.getByLabel('Drop a backup file here').setInputFiles(archive)
  const review = page.getByRole('dialog', { name: 'Restore this backup as a new server?' })
  await expect(review).toBeVisible({ timeout: 120_000 })
  await expect(review.getByText(/Nothing has changed yet/)).toBeVisible()
  await shot(page, 'restore-2-review-narrow')
  await review.getByLabel('Name for the new server').fill('Restored')
  await review.getByRole('checkbox', { name: /Minecraft End User License Agreement/ }).click()
  await review.getByRole('button', { name: 'Restore as a new server' }).click()

  await expect(page.getByRole('heading', { name: 'Home', level: 1 })).toBeVisible()
  const card = page.getByRole('article').filter({ hasText: 'Restored' })
  await expect(card).toBeVisible({ timeout: 60_000 })
  await page.waitForTimeout(3000)
  await shot(page, 'restore-3-starting-narrow')
  await expect(card.getByText('Online', { exact: true })).toBeVisible({ timeout: 20 * 60_000 })
  await shot(page, 'restore-4-online-narrow')
  await page.waitForTimeout(2000)
  const video = page.video()
  await page.close()
  if (video) await video.saveAs(path.join(shotsDir, 'restore-walkthrough-narrow.webm'))
})
