import { expect, test, type Page } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

// The server.properties editor on playkeeper.io (/tools/server-properties),
// in a browser with the site's Content-Security-Policy: it starts as the
// file the 26.3 server writes, each kind of control changes its line, it
// warns about what stops a server starting, and it reads a file of your own
// as java.util.Properties does. internal/site/properties_test.go checks the
// list against the server's file too.

const here = path.dirname(fileURLToPath(import.meta.url))
const serverFile = fs.readFileSync(path.join(here, '../../../internal/site/testdata/server-26.3.properties'), 'utf8').trim().split('\n').filter((l) => !l.startsWith('#'))
const HEADER = ['#Minecraft server properties', '#Made at playkeeper.io/tools/server-properties']

function watchErrors(page: Page) {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()) })
  return () => errors.filter((e) => !/api\.github\.com|Failed to load resource/.test(e))
}

const out = (page: Page) => page.locator('#sp-out').textContent().then((s) => s ?? '')
const line = async (page: Page, key: string) => (await out(page)).split('\n').find((l) => l.startsWith(key + '='))
const row = (page: Page, id: string) => page.locator(`[data-prop][id="${id}"]`)

test('it starts as the 26.3 server writes the file, and every kind of setting changes its line', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL, viewport: { width: 1440, height: 900 }, permissions: ['clipboard-read', 'clipboard-write'] })
  const page = await ctx.newPage()
  const errors = watchErrors(page)
  await page.goto('/tools/server-properties', { waitUntil: 'networkidle' })
  expect(await out(page)).toBe(HEADER.concat(serverFile).join('\n'))
  await expect(page.locator('[data-count]')).toHaveText('No changes')

  // A switch.
  await row(page, 'white-list').locator('.tool-switch').click()
  expect(await line(page, 'white-list')).toBe('white-list=false')
  await expect(page.locator('.sp-changed-line')).toHaveText(['white-list=false'])
  await expect(page.locator('[data-count]')).toHaveText('1 changed')
  await expect(row(page, 'white-list').locator('[data-default-text]')).toHaveText('Changed from true')
  await row(page, 'white-list').locator('[data-reset]').click()
  expect(await line(page, 'white-list')).toBe('white-list=true')
  await expect(page.locator('[data-count]')).toHaveText('No changes')

  // A choice, a level type with its namespace escaped, a number and text.
  await row(page, 'difficulty').getByText('Hard', { exact: true }).click()
  expect(await line(page, 'difficulty')).toBe('difficulty=hard')
  await row(page, 'level-type').getByText('Flat', { exact: true }).click()
  expect(await line(page, 'level-type')).toBe('level-type=minecraft\\:flat')
  await page.locator('#sp-view-distance').fill('16')
  expect(await line(page, 'view-distance')).toBe('view-distance=16')
  await page.locator('#sp-view-distance').fill('40')
  await expect(row(page, 'view-distance').locator('[data-note]')).toHaveText('It can be 3 to 32.')
  await page.locator('#sp-view-distance').fill('far')
  await expect(row(page, 'view-distance').locator('[data-note]')).toHaveText('Not a whole number, so the server would use 10.')
  await expect(page.locator('#sp-view-distance')).toHaveAttribute('aria-invalid', '')
  await page.locator('#sp-view-distance').fill(' 14 ')
  expect(await line(page, 'view-distance'), 'a number is written without the spaces Integer.parseInt fails on').toBe('view-distance=14')
  await expect(row(page, 'view-distance').locator('[data-note]')).toBeHidden()
  await page.locator('#sp-view-distance').fill('12')
  await page.locator('#sp-motd').fill(' §aHi: there')
  expect(await line(page, 'motd')).toBe('motd=\\ \\u00A7aHi\\: there')
  await page.locator('#sp-motd').fill('\\u00A7bTwo\\nlines')
  expect(await line(page, 'motd'), 'a MOTD typed with codes keeps them').toBe('motd=\\u00A7bTwo\\nlines')
  await expect(page.locator('[data-count]')).toHaveText('4 changed')

  // What would stop a server starting, or leave it open.
  await row(page, 'enable-rcon').locator('.tool-switch').click()
  await expect(row(page, 'rcon.password'.replace('.', '-')).locator('[data-note]')).toHaveText('RCON won’t start without a password.')
  await page.locator('#sp-rcon-password').fill('hunter2')
  await expect(row(page, 'rcon-password').locator('[data-note]')).toBeHidden()
  await row(page, 'management-server-enabled').locator('.tool-switch').click()
  await expect(row(page, 'management-server-tls-keystore').locator('[data-note]')).toContainText('The server won’t start like this')
  await row(page, 'online-mode').locator('.tool-switch').click()
  await expect(row(page, 'online-mode').locator('[data-note]')).toContainText('Anyone can join under any name')

  // Finding a setting, and only the changed ones.
  await page.locator('#sp-search').fill('port')
  const shown = await page.locator('[data-prop]:visible').evaluateAll((els) => els.map((e) => e.getAttribute('data-prop')))
  expect(shown).toEqual(expect.arrayContaining(['server-port', 'query.port', 'rcon.port', 'management-server-port']))
  expect(shown).not.toContain('difficulty')
  await expect(page.locator('#sp-group-gameplay')).toBeHidden()
  await page.locator('#sp-search').fill('zzz')
  await expect(page.locator('[data-empty]')).toHaveText('No setting matches “zzz”.')
  await page.locator('#sp-search').press('Escape')
  await expect(page.locator('#sp-search')).toHaveValue('')
  await page.locator('label[for="sp-changed"]').click()
  const changed = await page.locator('[data-prop]:visible').evaluateAll((els) => els.map((e) => e.getAttribute('data-prop')))
  expect(changed.sort()).toEqual(['difficulty', 'enable-rcon', 'level-type', 'management-server-enabled', 'motd', 'online-mode', 'rcon.password', 'view-distance'])
  await page.locator('label[for="sp-changed"]').click()

  // Download and Copy give the whole file.
  const text = await out(page)
  const [file] = await Promise.all([page.waitForEvent('download'), page.locator('[data-preview] [data-download]').click()])
  expect(file.suggestedFilename()).toBe('server.properties')
  expect(fs.readFileSync((await file.path()) as string, 'utf8')).toBe(text + '\n')
  const copyButton = page.locator('[data-preview] [data-tool-copy]')
  await copyButton.click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(text)
  await expect(copyButton.locator('[data-copy-label]')).toHaveText('Copied')
  await copyButton.click()
  await expect(copyButton.locator('[data-copy-label]'), 'a second click still goes back to Copy').toHaveText('Copy', { timeout: 4000 })
  expect(errors()).toEqual([])
  await ctx.close()
})

test('a file of your own is read as the server reads it, older settings and all', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/tools/server-properties', { waitUntil: 'networkidle' })
  await page.locator('#sp-view-distance').fill('20')
  await page.getByRole('button', { name: 'Load your file' }).click()
  await expect(page.locator('#sp-paste')).toBeFocused()
  await page.locator('#sp-paste').fill([
    '# a comment',
    '! another',
    'motd = My \\u00A7aserver',
    'max-players:10',
    'difficulty=2',
    'level-type=FLAT',
    'gamemode=extreme',
    'simulation-distance 6',
    'pvp=false',
    'my-plugin-key=hello \\',
    '    world',
  ].join('\n'))
  await page.getByRole('button', { name: 'Use it' }).click()
  await expect(page.locator('[data-toast]')).toContainText('Read 8 settings from your file.')
  await expect(page.locator('[data-load-panel]')).toBeHidden()
  await expect(page.locator('#sp-motd')).toHaveValue('My §aserver')
  await expect(page.locator('#sp-max-players')).toHaveValue('10')
  await expect(row(page, 'difficulty').locator('input:checked')).toHaveValue('normal')
  await expect(row(page, 'level-type').locator('input:checked')).toHaveValue('minecraft:flat')
  await expect(row(page, 'gamemode').locator('[data-note]')).toHaveText('Your file had extreme, which 26.3 doesn’t know, so it uses survival.')
  await expect(page.locator('#sp-view-distance'), 'a setting the file doesn\'t have goes back to its default').toHaveValue('10')
  await expect(page.locator('[data-other]')).toBeVisible()
  await expect(page.locator('[data-other] [data-prop="pvp"] [data-note]')).toHaveText('A game rule since 1.21.9: /gamerule pvp false.')
  const text = (await out(page)).split('\n')
  expect(text).toContain('pvp=false')
  expect(text).toContain('my-plugin-key=hello world')
  expect(text).toContain('motd=My \\u00A7aserver')
  expect(text).toContain('simulation-distance=6')
  const keys = text.slice(2).map((l) => l.split('=')[0])
  expect(keys, 'in the order the server writes them').toEqual([...keys].sort())
  await page.locator('[data-other] [data-prop="my-plugin-key"] [data-remove]').click()
  expect(await out(page)).not.toContain('my-plugin-key')
  expect(errors()).toEqual([])
})

test('without JavaScript every setting still reads, with its default', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL, javaScriptEnabled: false, viewport: { width: 1280, height: 900 } })
  const page = await ctx.newPage()
  await page.goto('/tools/server-properties')
  await expect(page.locator('.tool-nojs')).toBeVisible()
  await expect(row(page, 'view-distance').locator('.sp-what')).toBeVisible()
  await expect(row(page, 'view-distance').locator('[data-default-text]')).toHaveText('Default: 10 · 3 to 32')
  await expect(row(page, 'view-distance').locator('.sp-control')).toBeHidden()
  await expect(page.locator('.sp-bar')).toBeHidden()
  await expect(page.locator('[data-preview]')).toBeHidden()
  await ctx.close()
})

test('on a phone, Download stays at hand in a bar while the settings scroll', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL, viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })
  const page = await ctx.newPage()
  const errors = watchErrors(page)
  await page.goto('/tools/server-properties', { waitUntil: 'networkidle' })
  const dock = page.locator('[data-dock]')
  await page.locator('#max-players').scrollIntoViewIfNeeded()
  await expect(dock).toHaveClass(/is-shown/)
  await expect(dock).toHaveCSS('translate', 'none')
  expect(await dock.evaluate((el) => window.innerHeight - el.getBoundingClientRect().bottom), 'the bar sits at the bottom of the screen').toBe(12)
  await page.locator('#sp-max-players').fill('8')
  await expect(dock).toContainText('1 changed')
  const box = await dock.locator('[data-download]').boundingBox()
  if (!box) throw new Error('the bar has no Download button')
  const [file] = await Promise.all([page.waitForEvent('download'), page.touchscreen.tap(box.x + box.width / 2, box.y + box.height / 2)])
  expect(fs.readFileSync((await file.path()) as string, 'utf8')).toContain('\nmax-players=8\n')
  const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(wide, 'nothing sticks out sideways').toBe(0)
  expect(errors()).toEqual([])
  await ctx.close()
})
