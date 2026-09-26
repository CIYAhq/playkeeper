import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { firstServer, login, shot, tabTo } from './helpers'

const viewports = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'narrow', width: 390, height: 844 },
]
const prefix = process.env.PK_SHOT_PREFIX ?? 'view'
// The server types with a Map tab, as in web/src/lib/map.ts.
const mapTypes = ['paper', 'purpur', 'fabric', 'quilt', 'neoforge']

async function axe(page: Page, where: string) {
  const result = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()
  const bad = result.violations.filter((x) => x.impact === 'serious' || x.impact === 'critical')
  expect(bad.map((x) => `${x.id}: ${x.nodes.map((n) => n.target.join(' ')).join(', ')}`), where).toEqual([])
}

test('every page, desktop and narrow, with no serious accessibility violations and nothing wider than the screen', async ({ page }) => {
  await login(page)
  const s = await firstServer(page)
  const machine = ((await (await page.request.get('/api/machines')).json()) as { id: string; name: string }[])[0]
  expect(machine).toBeTruthy()
  // phone: the heading a phone shows instead, under its own back header;
  // dialog: the page opens with a dialog, whose heading stands in.
  const pages: { route: string; name: string; heading: string; phone?: string; dialog?: boolean }[] = [
    { route: '/', name: 'home', heading: 'Home' },
    { route: `/servers/${s.slug}`, name: 'overview', heading: s.name },
    { route: `/servers/${s.slug}/running`, name: 'running', heading: s.name },
    { route: `/servers/${s.slug}/console`, name: 'console', heading: s.name },
    { route: `/servers/${s.slug}/players`, name: 'players', heading: s.name },
    { route: `/servers/${s.slug}/world`, name: 'world', heading: s.name },
    { route: `/servers/${s.slug}/plugins`, name: 'plugins', heading: s.name },
    { route: `/servers/${s.slug}/settings`, name: 'server-settings', heading: s.name },
    { route: '/servers/new', name: 'new-server', heading: 'New server' },
    { route: `/machines/${machine?.id}`, name: 'machine', heading: machine?.name ?? '' },
    { route: '/settings', name: 'settings', heading: 'Settings' },
    // Waves 1 to 4.
    { route: `/servers/${s.slug}/plugins/browse`, name: 'plugins-browse', heading: s.name, phone: 'Browse' },
    { route: `/servers/${s.slug}/world/pregen`, name: 'world-pregen', heading: s.name, phone: 'Pre-generate' },
    { route: `/servers/${s.slug}/world/packs`, name: 'world-packs', heading: s.name, phone: 'Packs' },
    { route: `/machines/${machine?.id}/settings`, name: 'machine-settings', heading: 'Machine settings', phone: 'Address' },
    { route: '/account', name: 'account', heading: 'Your account', phone: 'Account' },
    { route: '/account/two-factor', name: 'two-factor', heading: 'Turn on two-factor sign-in', phone: 'Two-factor', dialog: true },
  ]

  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`/servers/${s.slug}`)
  await expect(page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: new RegExp(`^${s.name}`) })).toBeVisible()
  const tabs = ['Overview', 'Console', 'Players', 'World', ...(mapTypes.includes(s.type || 'paper') ? ['Map'] : []), 'Plugins', 'Settings']
  await expect(page.getByRole('navigation', { name: 'Server pages' }).getByRole('link')).toHaveText(tabs)

  // On phones a server's Settings open from More, and How it's running from
  // the Overview, each with a plain back header instead of the server's.
  // Plugins opens from More under its own heading.
  const phoneHeader: Record<string, string> = { 'server-settings': 'In the game', running: 'How it’s running' }
  for (const vp of viewports) {
    await page.setViewportSize({ width: vp.width, height: vp.height })
    const list = vp.name === 'narrow' ? [...pages, { route: '/more', name: 'more', heading: 'More' }] : pages
    for (const v of list) {
      await page.goto(v.route)
      const plain = vp.name === 'narrow' ? phoneHeader[v.name] : undefined
      const title = vp.name === 'narrow' ? (v.phone ?? (v.name === 'plugins' ? 'Plugins' : v.heading)) : v.heading
      const heading = plain ? page.getByRole('heading', { name: plain }).or(page.getByText(plain)).first() : page.getByRole('heading', { name: title, ...(v.dialog && vp.name !== 'narrow' ? {} : { level: 1 }) })
      await expect(heading).toBeVisible()
      await page.waitForTimeout(2500)
      await shot(page, `${prefix}-${v.name}-${vp.name}`)
      await axe(page, `${v.route} at ${vp.name}`)
      // A page that scrolls sideways on a phone puts controls under others.
      expect(await page.evaluate(() => document.documentElement.scrollWidth), `${v.route} at ${vp.name} scrolls sideways`).toBeLessThanOrEqual(vp.width)
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

test('keyboard: a dialog from a menu holds focus, and Esc gives it back to the menu button', async ({ page }) => {
  await login(page)
  const s = await firstServer(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(`/servers/${s.slug}`)
  const more = page.getByRole('button', { name: 'More actions' })
  await expect(more).toBeVisible()
  await tabTo(page, more, 80)
  await page.keyboard.press('Enter')
  const share = page.getByRole('menuitem', { name: 'Share as a template' })
  await expect(share).toBeVisible()
  for (let i = 0; i < 8 && !(await share.evaluate((el) => el.hasAttribute('data-highlighted'))); i++) await page.keyboard.press('ArrowDown')
  await page.keyboard.press('Enter')
  const dialog = page.getByRole('dialog', { name: `Share ${s.name} as a template` })
  await expect(dialog).toBeVisible()
  await expect.poll(() => dialog.evaluate((d) => d.contains(document.activeElement))).toBe(true)
  // Base UI's focus guard at either end hands focus back a moment later.
  for (let i = 0; i < 8; i++) {
    await page.keyboard.press('Tab')
    await expect.poll(() => dialog.evaluate((d) => d.contains(document.activeElement))).toBe(true)
  }
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
  await expect(more).toBeFocused()
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

test('the new-schedule dialog is as wide as its design and shows the whole time', async ({ page }) => {
  await login(page)
  const s = await firstServer(page)
  for (const vp of viewports) {
    await page.setViewportSize({ width: vp.width, height: vp.height })
    await page.goto(`/servers/${s.slug}/settings${vp.name === 'narrow' ? '/schedules' : ''}`)
    await page.getByRole('button', { name: 'New schedule' }).first().click()
    const dialog = page.getByRole('dialog', { name: /^New schedule for/ })
    await expect(dialog).toBeVisible()
    if (vp.name === 'desktop') await expect.poll(async () => Math.round((await dialog.boundingBox())?.width ?? 0), { message: 'dialog width' }).toBe(560)

    const lines = await dialog.getByRole('radiogroup', { name: 'What should happen?' }).evaluate((group) =>
      [...group.querySelectorAll('label')].map((card) => {
        const text = [...card.querySelectorAll('span')].find((x) => x.children.length === 0 && x.textContent?.trim())
        const range = document.createRange()
        range.selectNodeContents(text ?? card)
        const tops = new Set([...range.getClientRects()].filter((r) => r.width > 0).map((r) => Math.round(r.top)))
        return `${text?.textContent}: ${tops.size} line${tops.size === 1 ? '' : 's'}`
      }),
    )
    expect(lines, `kinds at ${vp.name}`).toEqual(['Restart: 1 line', 'Back up: 1 line', 'Run a command: 1 line'])

    // The browser sizes a time field to its locale's format, 12- or 24-hour;
    // a copy of the field left to its own width is the width the time needs.
    const time = await dialog.getByLabel('At', { exact: true }).evaluate((input) => {
      const field = input.closest('[data-slot=input-control]') as HTMLElement
      const own = field.cloneNode(true) as HTMLElement
      own.style.cssText = 'position: absolute; visibility: hidden; width: max-content; min-width: 0'
      document.body.append(own)
      const need = own.getBoundingClientRect().width
      own.remove()
      return { have: field.getBoundingClientRect().width, need }
    })
    expect(time.have + 0.5, `time field at ${vp.name}: ${time.have.toFixed(1)}px for a time that needs ${time.need.toFixed(1)}px`).toBeGreaterThanOrEqual(time.need)
    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(dialog).toBeHidden()
  }
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
