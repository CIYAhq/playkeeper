import { expect, test } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { outDir, password, shot, tabTo } from './helpers'

// First sign-in to a joinable server, using only the keyboard.
test('onboarding from first sign-in to joinable, keyboard only', async ({ page, context }) => {
  const code = process.env.PK_SETUP_CODE ?? ''
  expect(code, 'PK_SETUP_CODE').not.toBe('')
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await page.setViewportSize({ width: 1440, height: 900 })
  let screens = 0

  await page.goto('/setup')
  await expect(page.getByRole('heading', { name: 'Create your admin account' })).toBeVisible()
  await shot(page, 'onboarding-1-account-desktop')
  screens++
  await tabTo(page, page.getByLabel('Setup code'))
  await page.keyboard.type(code)
  await page.keyboard.press('Tab')
  await page.keyboard.press('Control+A')
  await page.keyboard.type('admin')
  await page.keyboard.press('Tab')
  await page.keyboard.type(password)
  await page.keyboard.press('Tab')
  await page.keyboard.type(password)
  await page.keyboard.press('Enter')

  await expect(page.getByRole('heading', { name: 'Check your server' })).toBeVisible()
  await expect(page.getByText('Docker', { exact: true })).toBeVisible()
  const cont = page.getByRole('button', { name: 'Continue' })
  await expect(cont).toBeEnabled({ timeout: 60_000 })
  await shot(page, 'onboarding-2-check-desktop')
  screens++
  await tabTo(page, cont)
  await page.keyboard.press('Enter')

  await expect(page.getByRole('heading', { name: 'Accept the Minecraft EULA' })).toBeVisible()
  const eula = page.getByRole('checkbox')
  await expect(eula).not.toBeChecked()
  await expect(page.getByRole('button', { name: 'Continue' })).toBeDisabled()
  await expect(page.getByRole('link', { name: /Minecraft End User License Agreement/ })).toHaveAttribute('href', 'https://www.minecraft.net/en-us/eula')
  await shot(page, 'onboarding-3-eula-desktop')
  screens++
  await tabTo(page, eula)
  await page.keyboard.press('Space')
  await tabTo(page, page.getByRole('button', { name: 'Continue' }))
  await page.keyboard.press('Enter')

  await expect(page.getByRole('heading', { name: 'Your Minecraft server' })).toBeVisible()
  await expect(page.getByRole('radio', { name: /Paper 26.1.2/ })).toBeChecked()
  await expect(page.getByLabel('Memory for Minecraft')).toHaveValue(/\d+/)
  await shot(page, 'onboarding-4-server-desktop')
  screens++
  await tabTo(page, page.getByRole('button', { name: 'Create and start server' }))
  await page.keyboard.press('Enter')

  await expect(page.getByRole('heading', { name: 'Starting your server' })).toBeVisible()
  await page.waitForTimeout(4000)
  await shot(page, 'onboarding-5-starting-desktop')
  await expect(page.getByRole('heading', { name: 'Your server is ready' })).toBeVisible({ timeout: 20 * 60_000 })
  screens++
  await shot(page, 'onboarding-5-ready-desktop')
  expect(screens, 'screens from first sign-in to joinable').toBeLessThanOrEqual(5)

  const address = (await page.locator('.join-address').innerText()).trim()
  await tabTo(page, page.getByRole('button', { name: 'Copy address' }))
  await page.keyboard.press('Enter')
  await expect(page.getByRole('button', { name: 'Copied' })).toBeVisible()
  const copied = await page.evaluate(() => navigator.clipboard.readText())
  expect(copied).toBe(address)
  fs.mkdirSync(outDir, { recursive: true })
  fs.writeFileSync(path.join(outDir, 'join-address.txt'), copied)

  for (const name of ['PkBuilder', 'PkFriend']) {
    await tabTo(page, page.getByLabel('Minecraft username'))
    await page.keyboard.type(name)
    await page.keyboard.press('Enter')
    await expect(page.getByText(`Added ${name} to the whitelist`)).toBeVisible()
  }
  await shot(page, 'onboarding-6-invited-desktop')
  await tabTo(page, page.getByRole('button', { name: 'Go to dashboard' }))
  await page.keyboard.press('Enter')
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
})
