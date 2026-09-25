import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { firstServer, login, shot, tabTo } from './helpers'

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
]
const prefix = process.env.PK_SHOT_PREFIX ?? 'view'

async function axe(page: Page, where: string) {
  const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
  const bad = result.violations.filter((x) => x.impact === 'serious' || x.impact === 'critical')
  expect(bad.map((x) => `${x.id}: ${x.nodes.map((n) => n.target.join(' ')).join(', ')}`), where).toEqual([])
}

test('every page, desktop and narrow, with no serious accessibility violations', async ({ page }) => {
  await login(page)
  const s = await firstServer(page)
  const machine = ((await (await page.request.get('/api/machines')).json()) as { id: string; name: string }[])[0]
  expect(machine).toBeTruthy()
  const pages = [
    { route: '/', name: 'home', heading: 'Home' },
    { route: `/servers/${s.slug}`, name: 'overview', heading: s.name },
    { route: `/servers/${s.slug}/running`, name: 'running', heading: s.name },
    { route: `/servers/${s.slug}/console`, name: 'console', heading: s.name },
    { route: `/servers/${s.slug}/players`, name: 'players', heading: s.name },
    { route: `/servers/${s.slug}/world`, name: 'world', heading: s.name },
    { route: `/servers/${s.slug}/settings`, name: 'server-settings', heading: s.name },
    { route: '/servers/new', name: 'new-server', heading: 'New server' },
    { route: `/machines/${machine?.id}`, name: 'machine', heading: machine?.name ?? '' },
    { route: '/settings', name: 'settings', heading: 'Settings' },
  ]

  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`/servers/${s.slug}`)
  await expect(page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: new RegExp(`^${s.name}`) })).toBeVisible()
  await expect(page.getByRole('navigation', { name: 'Server pages' }).getByRole('link')).toHaveText(['Overview', 'Console', 'Players', 'World', 'Settings'])

  // On phones a server's Settings open from More, and How it's running from
  // the Overview, each with a plain back header instead of the server's.
  const phoneHeader: Record<string, string> = { 'server-settings': 'In the game', running: 'How it’s running' }
  for (const vp of viewports) {
    await page.setViewportSize({ width: vp.width, height: vp.height })
    const list = vp.name === 'narrow' ? [...pages, { route: '/more', name: 'more', heading: 'More' }] : pages
    for (const v of list) {
      await page.goto(v.route)
      const plain = vp.name === 'narrow' ? phoneHeader[v.name] : undefined
      const heading = plain ? page.getByRole('heading', { name: plain }).or(page.getByText(plain)).first() : page.getByRole('heading', { name: v.heading, level: 1 })
      await expect(heading).toBeVisible()
      await page.waitForTimeout(2500)
      await shot(page, `${prefix}-${v.name}-${vp.name}`)
      await axe(page, `${v.route} at ${vp.name}`)
    }
  }

  // Old 0.2 links open the same page of the first server.
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/players')
  await expect(page).toHaveURL(new RegExp(`/servers/${s.slug}/players$`))
})

// Runs after the host A scenario, which made one backup with players online
// and one with the server stopped, and left the server running.
test('backups with players online, how it’s running and the memory advice', async ({ page }) => {
  await login(page)
  const s = await firstServer(page)
  await page.setViewportSize({ width: 1440, height: 900 })

  await page.goto(`/servers/${s.slug}/world`)
  await expect(page.getByText(/^Players stay online\./)).toBeVisible()
  await expect(page.getByText(/^Manual · no downtime · [\d,]+ files$/).first()).toBeVisible()
  await expect(page.getByText(/^Manual · [\d.,]+ s offline · [\d,]+ files$/).first()).toBeVisible()
  await expect(page.getByText('World saving is paused')).toBeHidden()

  await page.goto(`/servers/${s.slug}`)
  await page.getByRole('link', { name: 'How it’s running' }).click()
  await expect(page).toHaveURL(new RegExp(`/servers/${s.slug}/running$`))
  await expect(page.locator('#main').getByRole('heading', { level: 2 }).first()).toHaveText(/^(Running smoothly|A bit behind|Lagging|Not measured yet)/)
  for (const title of ['Tick rate', 'Tick time', 'Memory', 'CPU']) await expect(page.getByRole('heading', { name: title, level: 3 })).toBeVisible()

  await page.goto(`/servers/${s.slug}/settings#memory`)
  await expect(page.locator('#memory').getByText(/^(Suggests a size after|Starts measuring at its next restart)/)).toBeVisible()
})

test('keyboard: skip link, server pages, the command palette traps focus and Esc closes it', async ({ page }) => {
  await login(page)
  const s = await firstServer(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`/servers/${s.slug}`)
  await expect(page.getByRole('heading', { name: s.name, level: 1 })).toBeVisible()
  await page.keyboard.press('Tab')
  const skip = page.getByRole('link', { name: 'Skip to content' })
  await expect(skip).toBeFocused()
  await expect(skip).toBeVisible()
  await page.keyboard.press('Enter')
  await expect(page.locator('#main')).toBeFocused()

  await tabTo(page, page.getByRole('navigation', { name: 'Server pages' }).getByRole('link', { name: 'World' }))
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(new RegExp(`/servers/${s.slug}/world$`))
  await expect(page.getByRole('heading', { name: 'Make a backup' })).toBeVisible()

  await page.keyboard.press('Control+K')
  const palette = page.getByRole('dialog', { name: 'Search or jump to' })
  await expect(palette).toBeVisible()
  await page.keyboard.type('console')
  await expect(palette.getByRole('option', { name: new RegExp(`${s.name} › Console`) })).toBeVisible()
  for (let i = 0; i < 6; i++) {
    await page.keyboard.press('Tab')
    expect(await palette.evaluate((d) => d.contains(document.activeElement))).toBe(true)
  }
  await page.keyboard.press('Escape')
  await expect(palette).toBeHidden()
})

test('keyboard: restore preview and typed confirmation are reachable', async ({ page }) => {
  await login(page)
  const s = await firstServer(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`/servers/${s.slug}/world`)
  const actions = page.getByRole('button', { name: /^Actions for the backup from/ }).first()
  await expect(actions).toBeVisible()
  await tabTo(page, actions, 150)
  await page.keyboard.press('Enter')
  const restore = page.getByRole('menuitem', { name: /Restore this backup/ })
  await expect(restore).toBeVisible()
  for (let i = 0; i < 5 && !(await restore.evaluate((el) => el.hasAttribute('data-highlighted'))); i++) await page.keyboard.press('ArrowDown')
  await page.keyboard.press('Enter')
  const dialog = page.getByRole('dialog', { name: 'Restore this backup?' })
  await expect(dialog).toBeVisible({ timeout: 60_000 })
  await expect(dialog.getByText(/Nothing has changed yet/)).toBeVisible()
  const confirm = dialog.getByLabel('Type the confirmation')
  await tabTo(page, confirm, 120)
  const apply = dialog.getByRole('button', { name: 'Replace the world and restore' })
  await expect(apply).toBeDisabled()
  await page.keyboard.type('replace world')
  await expect(apply).toBeEnabled()
  await shot(page, `${prefix}-restore-preview-desktop`, { fullPage: false })
  await tabTo(page, dialog.getByRole('button', { name: 'Cancel' }))
  await page.keyboard.press('Enter')
  await expect(dialog).toBeHidden()
})

test('reduced motion disables animation', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await login(page)
  const anim = await page.evaluate(() => {
    const el = document.createElement('span')
    el.className = 'animate-spin'
    document.body.appendChild(el)
    const s = getComputedStyle(el)
    return { name: s.animationName }
  })
  expect(anim.name).toBe('none')
})
