import { expect, test } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { outDir, password, shot, shotsDir, tabTo } from './helpers'

test.use({ video: { mode: 'on', size: { width: 1440, height: 900 } } })

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
  await expect(page.getByRole('meter', { name: 'Password strength' })).toBeVisible()
  await page.keyboard.press('Enter')

  await expect(page.getByRole('heading', { name: 'Checking this VPS' })).toBeVisible()
  await expect(page.getByText('Docker is running')).toBeVisible()
  await expect(page.getByText('Your provider’s firewall')).toBeVisible()
  const cont = page.getByRole('button', { name: 'Looks good, continue' })
  await expect(cont).toBeEnabled({ timeout: 60_000 })
  await shot(page, 'onboarding-2-check-desktop')
  screens++
  await tabTo(page, cont)
  await page.keyboard.press('Enter')

  await expect(page.getByRole('heading', { name: /is ready for Minecraft$/ })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Skip for now' })).toBeVisible()
  await shot(page, 'onboarding-3-first-server-desktop')
  screens++
  await tabTo(page, page.getByRole('button', { name: 'Create a server' }))
  await page.keyboard.press('Enter')

  await expect(page.getByRole('heading', { name: 'How will you play?' })).toBeVisible()
  await expect(page.getByRole('radio', { name: /With friends/ })).toBeChecked()
  const eula = page.getByRole('checkbox', { name: /Minecraft End User License Agreement/ })
  await expect(eula).not.toBeChecked()
  const create = page.getByRole('button', { name: 'Create my server' })
  await expect(create).toBeDisabled()
  await expect(page.getByRole('link', { name: 'Minecraft End User License Agreement' })).toHaveAttribute('href', 'https://www.minecraft.net/en-us/eula')

  // The protocol test bots speak Minecraft 26.1, so change the version to Paper 26.1.2.
  await tabTo(page, page.getByRole('button', { name: 'Change' }))
  await page.keyboard.press('Enter')
  const dialog = page.getByRole('dialog', { name: 'Change the details' })
  await expect(dialog).toBeVisible()
  const version = dialog.getByRole('combobox', { name: 'Minecraft version' })
  await tabTo(page, version)
  await page.keyboard.press('Enter')
  const forBots = page.getByRole('option', { name: /^26\.1\.2/ })
  await expect(page.getByRole('listbox')).toBeVisible()
  for (let i = 0; i < 60 && !(await forBots.evaluate((el) => el.hasAttribute('data-highlighted'))); i++) await page.keyboard.press('ArrowDown')
  await page.keyboard.press('Enter')
  await expect(version).toHaveText(/26\.1\.2/)
  await tabTo(page, dialog.getByRole('button', { name: 'Done' }))
  await page.keyboard.press('Enter')
  await expect(dialog).toBeHidden()
  await expect(page.getByText(/Paper 26\.1\.2 · /)).toBeVisible()

  await tabTo(page, eula)
  await page.keyboard.press('Space')
  await expect(eula).toBeChecked()
  await shot(page, 'onboarding-4-play-style-desktop')
  screens++
  await tabTo(page, create)
  await page.keyboard.press('Enter')

  await expect(page.getByRole('heading', { name: 'Setting up Survival' })).toBeVisible()
  await page.waitForTimeout(4000)
  await shot(page, 'onboarding-5-setting-up-desktop')
  await expect(page.getByRole('heading', { name: 'Survival is online!' })).toBeVisible({ timeout: 20 * 60_000 })
  screens++
  await shot(page, 'onboarding-5-online-desktop')
  expect(screens, 'screens from first sign-in to joinable').toBeLessThanOrEqual(5)

  const address = (await page.getByTestId('join-address').innerText()).trim()
  await tabTo(page, page.getByRole('button', { name: 'Copy', exact: true }))
  await page.keyboard.press('Enter')
  await expect(page.getByRole('button', { name: 'Copied' })).toBeVisible()
  const copied = await page.evaluate(() => navigator.clipboard.readText())
  expect(copied).toBe(address)
  fs.mkdirSync(outDir, { recursive: true })
  fs.writeFileSync(path.join(outDir, 'join-address.txt'), copied)

  for (const name of ['PkBotBuilder', 'PkBotFriend']) {
    await tabTo(page, page.getByLabel('Invite your first friend'))
    await page.keyboard.type(name)
    await page.keyboard.press('Enter')
    await expect(page.getByText(`Added ${name} to the allowlist`).first()).toBeVisible()
  }
  await shot(page, 'onboarding-6-invited-desktop')
  await tabTo(page, page.getByRole('button', { name: 'Go to my dashboard' }))
  await page.keyboard.press('Enter')
  await expect(page.getByRole('heading', { name: 'Survival', level: 1 })).toBeVisible()
  await expect(page.getByRole('navigation', { name: 'Server pages' })).toBeVisible()
  await page.waitForTimeout(3000)
  const video = page.video()
  await page.close()
  if (video) await video.saveAs(path.join(shotsDir, 'onboarding-walkthrough.webm'))
})
