import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
import { login, shot, tabTo } from './helpers'

const views = [
  { route: '/', name: 'overview', heading: 'Overview' },
  { route: '/console', name: 'console', heading: 'Console' },
  { route: '/players', name: 'players', heading: 'Players' },
  { route: '/world', name: 'world', heading: 'World' },
  { route: '/settings', name: 'settings', heading: 'Settings' },
]
const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
]
const prefix = process.env.PK_SHOT_PREFIX ?? 'view'

test('main views in order, desktop and narrow, with no serious accessibility violations', async ({ page }) => {
  await login(page)
  const nav = page.getByRole('navigation', { name: 'Main' }).getByRole('link')
  await expect(nav).toHaveText(['Overview', 'Console', 'Players', 'World', 'Settings'])
  for (const vp of viewports) {
    await page.setViewportSize({ width: vp.width, height: vp.height })
    for (const v of views) {
      await page.goto(v.route)
      await expect(page.getByRole('heading', { name: v.heading, level: 1 })).toBeVisible()
      await page.waitForTimeout(2500)
      await shot(page, `${prefix}-${v.name}-${vp.name}`)
      const axe = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
      const bad = axe.violations.filter((x) => x.impact === 'serious' || x.impact === 'critical')
      expect(bad.map((x) => `${x.id}: ${x.nodes.map((n) => n.target.join(' ')).join(', ')}`), `${v.route} at ${vp.name}`).toEqual([])
    }
  }
})

test('keyboard: skip link, navigation, dialog focus trap and Esc', async ({ page }) => {
  await login(page)
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible()
  await page.keyboard.press('Tab')
  const skip = page.getByRole('link', { name: 'Skip to content' })
  await expect(skip).toBeFocused()
  await expect(skip).toBeVisible()
  await page.keyboard.press('Enter')
  await expect(page.locator('#main')).toBeFocused()
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible()
  await tabTo(page, page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'World' }))
  await page.keyboard.press('Enter')
  await expect(page.getByRole('heading', { name: 'World', level: 1 })).toBeVisible()
  const backup = page.getByRole('button', { name: 'Back up now' })
  await tabTo(page, backup)
  await page.keyboard.press('Enter')
  const dialog = page.getByRole('dialog', { name: 'Back up now?' })
  await expect(dialog).toBeVisible()
  for (let i = 0; i < 6; i++) {
    await page.keyboard.press('Tab')
    expect(await dialog.evaluate((d) => d.contains(document.activeElement))).toBe(true)
  }
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
})

test('keyboard: restore preview and typed confirmation are reachable', async ({ page }) => {
  await login(page)
  await page.goto('/world')
  const restore = page.getByRole('button', { name: 'Restore…' }).first()
  await expect(restore).toBeVisible()
  await tabTo(page, restore, 120)
  await page.keyboard.press('Enter')
  await expect(page.getByText('The backup file is intact and can be restored')).toBeVisible({ timeout: 60_000 })
  const confirm = page.getByLabel(/Type replace/)
  await tabTo(page, confirm, 120)
  const apply = page.getByRole('button', { name: 'Replace world and restore' })
  await expect(apply).toBeDisabled()
  await page.keyboard.type('replace world')
  await expect(apply).toBeEnabled()
  await shot(page, `${prefix}-restore-preview-desktop`)
  await tabTo(page, page.getByRole('button', { name: 'Cancel' }))
  await page.keyboard.press('Enter')
  await expect(apply).toBeHidden()
})

test('reduced motion disables animation', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await login(page)
  const anim = await page.evaluate(() => {
    const el = document.createElement('span')
    el.className = 'spinner'
    document.body.appendChild(el)
    const s = getComputedStyle(el)
    return { name: s.animationName, duration: s.animationDuration }
  })
  expect(anim.name).toBe('none')
})
