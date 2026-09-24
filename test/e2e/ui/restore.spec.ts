import { expect, test } from '@playwright/test'
import path from 'node:path'
import { login, shot, shotsDir, tabTo } from './helpers'

test.use({ video: { mode: 'on', size: { width: 390, height: 844 } } })

// On a fresh second host: restore a downloaded Playkeeper backup entirely in
// the browser, at phone width.
test('second host: restore a backup through onboarding', async ({ page }) => {
  const archive = process.env.PK_ARCHIVE ?? ''
  expect(archive, 'PK_ARCHIVE').not.toBe('')
  await page.setViewportSize({ width: 390, height: 844 })
  await login(page)
  await expect(page.getByRole('heading', { name: 'Check your server' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Continue' })).toBeEnabled({ timeout: 60_000 })
  await shot(page, 'restore-1-check-narrow')
  await page.getByRole('button', { name: 'Continue' }).click()

  await expect(page.getByRole('heading', { name: 'Accept the Minecraft EULA' })).toBeVisible()
  await shot(page, 'restore-2-eula-narrow')
  await tabTo(page, page.getByRole('checkbox'))
  await page.keyboard.press('Space')
  await page.getByRole('button', { name: 'Continue' }).click()

  await expect(page.getByRole('heading', { name: 'Your Minecraft server' })).toBeVisible()
  await page.getByRole('radio', { name: /Restore a backup/ }).check()
  await page.locator('#archive').setInputFiles(archive)
  await shot(page, 'restore-3-choose-narrow')
  await page.getByRole('button', { name: 'Upload and check' }).click()
  await expect(page.getByText('The backup file is intact and can be restored')).toBeVisible({ timeout: 120_000 })
  await shot(page, 'restore-4-preview-narrow')
  await page.getByRole('button', { name: 'Restore this world' }).click()

  await expect(page.getByRole('heading', { name: /Starting your server|Your server is ready/ })).toBeVisible()
  await page.waitForTimeout(3000)
  await shot(page, 'restore-5-starting-narrow')
  await expect(page.getByRole('heading', { name: 'Your server is ready' })).toBeVisible({ timeout: 20 * 60_000 })
  await shot(page, 'restore-6-ready-narrow')
  await page.waitForTimeout(2000)
  const video = page.video()
  await page.close()
  if (video) await video.saveAs(path.join(shotsDir, 'restore-walkthrough-narrow.webm'))
})
