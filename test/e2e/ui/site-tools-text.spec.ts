import { expect, test, type Page } from '@playwright/test'

// The colour codes tool and the MOTD generator on playkeeper.io, in a browser
// with the site's Content-Security-Policy: the text engine's output in every
// format (js/tools/mc-text.js), the shared editor (js/tools/mc-editor.js),
// and each page's controls. playwright.site.config.ts serves the site.

function watchErrors(page: Page) {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()) })
  return () => errors.filter((e) => !/api\.github\.com|Failed to load resource/.test(e))
}

// Selects the text of the editor from a to b, as a person dragging does.
const select = (page: Page, id: string, a: number, b: number) => page.locator(`#${id}`).evaluate((el: HTMLTextAreaElement, [a, b]) => {
  el.focus()
  el.setSelectionRange(a, b)
  el.dispatchEvent(new Event('select'))
}, [a, b] as const)
const text = (page: Page, id: string) => page.locator(id).textContent()

test('the text engine writes each format as the game and plugins read it', async ({ page }) => {
  await page.goto('/tools/color-codes', { waitUntil: 'networkidle' })
  const r = await page.evaluate(() => {
    const mc = (window as unknown as { mcText: Record<string, (...a: unknown[]) => unknown> }).mcText
    const G = mc.Gradients as unknown as new () => { add(s: string[]): number; apply(c: unknown[]): void }
    const javaBold = mc.parseLegacy('&6&lGold &fwhite', false)
    const bedrockBold = mc.parseLegacy('&6&lGold &fwhite', true)
    const grads = new G()
    const g = mc.parseMiniMessage('<gray>Hi <gradient:#ff0000:#0000ff><bold>abc</bold></gradient>!', grads) as { ch: string; st: { c: string; b: boolean } }[]
    grads.apply(g)
    const long = mc.parseLegacy('word '.repeat(20), false) as unknown[]
    return {
      javaBold: (javaBold as { st: { b: boolean } }[]).map((c) => c.st.b),
      bedrockBold: (bedrockBold as { st: { b: boolean } }[]).map((c) => c.st.b),
      amp: mc.legacy(javaBold, '&', 'hash', false),
      bedrock: mc.legacy(mc.parseLegacy('&6&lGold&r&7 gray', true), '§', 'nearest', true),
      bedrockPlain: mc.legacy(mc.parseLegacy('&lBold&r plain', true), '§', 'nearest', true),
      nearestRun: mc.legacy(g, '§', 'nearest', false),
      sameNearest: (() => {
        const gg = new G()
        const cs = mc.parseMiniMessage('<gradient:#ff5555:#ff7070>abcd</gradient>!', gg) as unknown[]
        gg.apply(cs)
        return mc.legacy(cs, '§', 'nearest', false)
      })(),
      hashHex: mc.legacy(mc.parseLegacy('&#ff8800Orange', false), '&', 'hash', false),
      xHex: mc.legacy(mc.parseLegacy('&#ff8800Orange', false), '§', 'x', false),
      nearHex: mc.legacy(mc.parseLegacy('&#ff8800Orange', false), '§', 'nearest', false),
      xRead: mc.legacy(mc.parseLegacy('§x§1§2§3§4§5§6Hex', false), '&', 'hash', false),
      tomJerry: mc.legacy(mc.parseLegacy('Tom & Jerry', false), '&', 'hash', false),
      props: mc.properties(mc.parseLegacy('  &aHi é\nline 2', false)),
      mm: mc.minimessage(g, grads),
      gradientEnds: [g[3].st.c, g[5].st.c],
      json: mc.json(mc.parseLegacy('&6&lGold&r plain', false)),
      plainJson: mc.json(mc.parseLegacy('Just text', false)),
      wrapped: (mc.wrap(long, 271) as unknown[][]).map((l) => (l as { ch: string }[]).map((c) => c.ch).join('')),
      width: mc.widthOf(mc.parseLegacy('Hi il', false)),
      boldWidth: mc.widthOf(mc.parseLegacy('&lHi', false)),
    }
  })
  expect(r.javaBold, 'on Java a colour turns bold off').toEqual([true, true, true, true, true, false, false, false, false, false])
  expect(r.bedrockBold, 'on Bedrock bold carries on past a colour').toEqual([true, true, true, true, true, true, true, true, true, true])
  expect(r.amp).toBe('&6&lGold &fwhite')
  expect(r.bedrock).toBe('§6§lGold§r§7 gray')
  expect(r.bedrockPlain, 'one reset, not two').toBe('§lBold§r plain')
  expect(r.nearestRun, 'each letter of a gradient as its nearest colour, with bold after each').toBe('§7Hi §4§la§5§lb§1§lc§7!')
  expect(r.sameNearest, 'letters that come out as the same colour share one code').toBe('§cabcd§r!')
  expect(r.hashHex).toBe('&#ff8800Orange')
  expect(r.xHex).toBe('§x§f§f§8§8§0§0Orange')
  expect(r.nearHex).toBe('§6Orange')
  expect(r.xRead).toBe('&#123456Hex')
  expect(r.tomJerry, 'a & without a code stays text').toBe('Tom & Jerry')
  expect(r.props, 'server.properties: the first space kept, § and é escaped, the break as \\n').toBe('\\  \\u00A7aHi \\u00E9\\nline 2')
  expect(r.mm).toBe('<gray>Hi </gray><gradient:#ff0000:#0000ff><bold>abc</bold></gradient><gray>!')
  expect(r.gradientEnds).toEqual(['#ff0000', '#0000ff'])
  expect(JSON.parse(r.json as string)).toEqual(['', { text: 'Gold', color: 'gold', bold: true }, { text: ' plain' }])
  expect(r.plainJson).toBe('"Just text"')
  expect(r.wrapped.length, 'twenty words wrap onto several lines').toBeGreaterThan(1)
  for (const l of r.wrapped) expect(l.length, `"${l}" fits the list`).toBeLessThanOrEqual(46)
  expect(r.width, "H and i and l, with the font's widths and a space").toBe(6 + 2 + 4 + 2 + 2)
  expect(r.boldWidth, 'bold adds a pixel a letter').toBe(7 + 3)
})

test('the colour codes page: codes copy with a click, the text maker colours a selection, reads pasted codes, undoes, and does Bedrock', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL, viewport: { width: 1440, height: 900 }, permissions: ['clipboard-read', 'clipboard-write'] })
  const page = await ctx.newPage()
  const errors = watchErrors(page)
  await page.goto('/tools/color-codes', { waitUntil: 'networkidle' })
  await page.locator('.cc-table button[data-copy-text="§4"]').click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('§4')
  await expect(page.locator('.cc-table button[data-copy-text="§4"]')).toHaveClass(/is-copied/)
  await page.locator('.cc-table button[data-copy-text="#FFAA00"]').click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('#FFAA00')

  const tool = page.locator('[data-cc-tool]')
  await expect(page.locator('#cc-out-amp')).toHaveText(/^&7Welcome to &#ffaa00&lP/)
  await expect(page.locator('#cc-out-mm')).toContainText('<gradient:#ffaa00:#ff5555><bold>Pip Land</bold></gradient>')
  await expect(page.locator('[data-chat] span').first()).toHaveText('Welcome to ')

  // "have fun!" is the last 9 characters.
  const value = await page.locator('#cc-text').inputValue()
  const end = value.length
  await select(page, 'cc-text', end - 9, end)
  await tool.locator('[data-color="4"]').click()
  await expect(page.locator('#cc-out-amp')).toHaveText(/&4have fun!$/)
  await select(page, 'cc-text', end - 9, end)
  await tool.locator('[data-format="b"]').click()
  await expect(tool.locator('[data-format="b"]')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.locator('#cc-out-amp')).toHaveText(/&4&lhave fun!$/)
  await page.keyboard.press('Control+z')
  await expect(page.locator('#cc-out-amp'), 'undo takes the bold off again').toHaveText(/&4have fun!$/)
  await select(page, 'cc-text', end - 9, end)
  await tool.locator('[data-hex]').fill('#123abc')
  await tool.getByRole('button', { name: 'Colour it' }).click()
  await expect(page.locator('#cc-out-amp')).toHaveText(/&#123abchave fun!$/)
  await tool.locator('label[for="cc-hex-x"]').click()
  await expect(page.locator('#cc-out-amp')).toHaveText(/&x&1&2&3&a&b&chave fun!$/)
  await tool.locator('label[for="cc-hex-hash"]').click()
  await expect(page.locator('#cc-out-amp')).toHaveText(/&#123abchave fun!$/)
  await select(page, 'cc-text', 0, 7)
  await tool.getByRole('button', { name: 'Add the gradient' }).click()
  await expect(page.locator('#cc-out-mm')).toHaveText(/^<gradient:#ffaa00:#ff5555>Welcome<\/gradient>/)
  await select(page, 'cc-text', 0, end)
  await tool.getByRole('button', { name: 'Clear' }).click()
  await expect(page.locator('#cc-out-amp'), 'Clear leaves plain text').toHaveText(value)

  // Every colour and format on a selection changes the text.
  for (const b of await tool.locator('.mc-colors:not(.mc-colors-bedrock) [data-color], .mc-toolbar-row [data-format]').all()) {
    const before = await text(page, '#cc-out-amp')
    await select(page, 'cc-text', 0, 7)
    await b.click()
    expect.soft(await text(page, '#cc-out-amp'), `${await b.getAttribute('title')} changes the text`).not.toBe(before)
  }

  // Pasting codes reads them.
  await page.locator('#cc-text').evaluate((el: HTMLTextAreaElement) => {
    el.focus()
    el.setSelectionRange(0, el.value.length)
    const dt = new DataTransfer()
    dt.setData('text/plain', '&cRed &l&nbold')
    el.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }))
  })
  await expect(page.locator('#cc-text')).toHaveValue('Red bold')
  await expect(page.locator('#cc-out-amp')).toHaveText('&cRed &l&nbold')
  await page.locator('#cc-text').evaluate((el: HTMLTextAreaElement) => {
    el.setSelectionRange(0, el.value.length)
    const dt = new DataTransfer()
    dt.setData('text/plain', '<green>Go <#ff8800>now')
    el.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }))
  })
  await expect(page.locator('#cc-out-amp')).toHaveText('&aGo &#ff8800now')

  // Bedrock: its colours, no & codes or MiniMessage, the nearest colour for hex, rawtext.
  await tool.locator('label[for="cc-edition-bedrock"]').click()
  await expect(tool.locator('.mc-colors-bedrock')).toBeVisible()
  await expect(tool.locator('[data-format="u"]')).toBeHidden()
  await expect(page.locator('#cc-tab-amp')).toBeHidden()
  await expect(page.locator('#cc-tab-sect')).toHaveAttribute('aria-selected', 'true')
  await expect(page.locator('#cc-out-sect')).toHaveText('§aGo §6now')
  await expect(page.locator('[data-note-hex]')).toBeVisible()
  await page.locator('#cc-tab-json').click()
  await expect(page.locator('#cc-out-json')).toHaveText('/tellraw @a {"rawtext":[{"text":"§aGo §6now"}]}')
  await select(page, 'cc-text', 0, 3)
  await tool.locator('[data-color="q"]').click()
  await page.locator('#cc-tab-sect').click()
  await expect(page.locator('#cc-out-sect')).toHaveText('§qGo §6now')
  await tool.locator('label[for="cc-edition-java"]').click()
  await expect(page.locator('#cc-tab-amp')).toBeVisible()
  await page.locator('#cc-tab-amp').click()
  await expect(page.locator('#cc-out-amp'), "Bedrock's own colour is hex on Java").toHaveText(/^&#13a045Go /)
  expect(errors()).toEqual([])
  await ctx.close()
})

test('the MOTD generator: the server list wraps and warns as the game does, centres lines, and writes server.properties and MiniMOTD', async ({ page }) => {
  const errors = watchErrors(page)
  await page.goto('/tools/motd', { waitUntil: 'networkidle' })
  const tool = page.locator('[data-motd-tool]')
  await expect(page.locator('#motd-out-props')).toHaveText(/^motd=\\u00A7b\\u00A7lP/)
  await expect(page.locator('#motd-out-props')).toContainText('\\n')
  await page.locator('#motd-tab-mini').click()
  await expect(page.locator('#motd-out-mini')).toContainText('line1="<gradient:#55ffff:#5555ff><bold>PIP LAND</bold></gradient>')
  await expect(page.locator('#motd-out-mini')).toContainText('line2="<yellow>New: </yellow><gray>a live map and weekly backups"')
  await expect(page.locator('[data-note-hex]')).toBeAttached()
  await expect(tool.locator('[data-width="0"]')).toHaveText(/^\d+ of 271$/)
  await expect(tool.locator('[data-warning]')).toBeHidden()

  // A preset, then a first line too wide for the list.
  await tool.getByRole('button', { name: 'Maintenance' }).click()
  await expect(page.locator('#motd-text')).toHaveValue('Down for maintenance\nBack in a few minutes. Thanks for waiting!')
  await page.locator('#motd-text').fill('This first line is far too long to fit in the server list at all\nSecond line')
  await expect(tool.locator('[data-warning]')).toHaveText(/Line 1 is wider than the list: Minecraft wraps it, and line 2 drops out of sight/)
  await expect(tool.locator('.motd-meter').first()).toHaveClass(/is-over/)
  await expect(tool.locator('[data-list-motd]'), 'the list shows the first line wrapped onto two').not.toContainText('Second line')

  // Two lines at most: Enter on the second does nothing.
  await page.locator('#motd-text').fill('One\nTwo')
  await page.locator('#motd-text').press('End')
  await page.locator('#motd-text').press('Enter')
  await page.locator('#motd-text').type('x')
  await expect(page.locator('#motd-text')).toHaveValue('One\nTwox')

  // Centred: spaces in front, the first written as "\ " so server.properties keeps them.
  await tool.locator('label[for="motd-align-centre"]').click()
  await page.locator('#motd-tab-props').click()
  await expect(page.locator('#motd-out-props')).toHaveText(/^motd=\\ {20,}(\\u00A7.)*One\\n(\\u00A7.)* {20,}(\\u00A7.)*Twox$/)
  await expect(tool.locator('[data-list-motd]')).toContainText('One')

  await tool.locator('#motd-name').fill('Creeper Cove')
  await expect(tool.locator('[data-list-name]')).toHaveText('Creeper Cove')
  const iconBefore = await tool.locator('[data-list-icon]').evaluate((c: HTMLCanvasElement) => c.toDataURL())
  await tool.locator('#motd-icon').setInputFiles({ name: 'red.png', mimeType: 'image/png', buffer: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==', 'base64') })
  await expect.poll(() => tool.locator('[data-list-icon]').evaluate((c: HTMLCanvasElement) => c.toDataURL()), 'Use your icon draws it in the list').not.toBe(iconBefore)
  expect(errors()).toEqual([])
})
