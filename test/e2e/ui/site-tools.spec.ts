import { expect, test, type Page } from '@playwright/test'
import fs from 'node:fs'
import zlib from 'node:zlib'

// The free tools on playkeeper.io/tools, in a browser with the site's
// Content-Security-Policy (cmd/site -serve sends it): the hub, and the
// server icon maker making real 64 × 64 PNGs, where every control changes
// the icon. playwright.site.config.ts builds and serves the site.

// A PNG of w × h, drawn by fill(x, y) → [r, g, b, a], for the picture tests.
function png(w: number, h: number, fill: (x: number, y: number) => number[]): Buffer {
  const crcTable = Array.from({ length: 256 }, (_, n) => {
    let c = n
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1
    return c >>> 0
  })
  const crc = (b: Buffer) => {
    let c = 0xffffffff
    for (const x of b) c = crcTable[(c ^ x) & 0xff] ^ (c >>> 8)
    return (c ^ 0xffffffff) >>> 0
  }
  const chunk = (type: string, data: Buffer) => {
    const len = Buffer.alloc(4)
    len.writeUInt32BE(data.length)
    const body = Buffer.concat([Buffer.from(type), data])
    const sum = Buffer.alloc(4)
    sum.writeUInt32BE(crc(body))
    return Buffer.concat([len, body, sum])
  }
  const ihdr = Buffer.alloc(13)
  ihdr.writeUInt32BE(w, 0)
  ihdr.writeUInt32BE(h, 4)
  ihdr.set([8, 6, 0, 0, 0], 8)
  const raw = Buffer.alloc((w * 4 + 1) * h)
  for (let y = 0; y < h; y++) {
    raw[y * (w * 4 + 1)] = 0
    for (let x = 0; x < w; x++) raw.set(fill(x, y), y * (w * 4 + 1) + 1 + x * 4)
  }
  return Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), chunk('IHDR', ihdr), chunk('IDAT', zlib.deflateSync(raw)), chunk('IEND', Buffer.alloc(0))])
}

// A landscape picture: red on the left half, blue on the right, a green
// stripe down the middle, and transparent corners.
const picture = png(200, 100, (x, y) => ((x < 12 || x > 187) && (y < 12 || y > 87) ? [0, 0, 0, 0] : x >= 95 && x < 105 ? [0, 200, 0, 255] : x < 100 ? [220, 30, 30, 255] : [30, 60, 220, 255]))

async function pngSize(file: string) {
  const b = fs.readFileSync(file)
  expect(b.subarray(0, 8).toString('hex'), 'a PNG signature').toBe('89504e470d0a1a0a')
  expect(b.subarray(12, 16).toString(), 'IHDR first').toBe('IHDR')
  return { width: b.readUInt32BE(16), height: b.readUInt32BE(20), bytes: b.length }
}

async function download(page: Page, button: string) {
  const [file] = await Promise.all([page.waitForEvent('download'), page.locator(button).first().click()])
  expect(file.suggestedFilename()).toBe('server-icon.png')
  return (await file.path()) as string
}

const icon = (page: Page) => page.locator('[data-big]').evaluate((c: HTMLCanvasElement) => c.toDataURL())

// A slider moved as a person drags it: its value, then an input event.
const slide = (page: Page, selector: string, value: number) => page.locator(selector).evaluate((el: HTMLInputElement, v) => {
  el.value = String(v)
  el.dispatchEvent(new Event('input', { bubbles: true }))
}, value)

function watchErrors(page: Page) {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()) })
  return () => errors.filter((e) => !/api\.github\.com|Failed to load resource/.test(e))
}

test('the hub lists each tool with its card, and the header has a Tools menu', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/tools', { waitUntil: 'networkidle' })
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Free Minecraft server tools')
  await expect(page.locator('a.tool-tile[href="/tools/server-icon"]')).toContainText('Minecraft server icon maker')
  await expect(page.locator('a.tool-tile[href="/sizing"]')).toContainText('RAM calculator')
  await page.getByRole('button', { name: 'Tools' }).click()
  const menu = page.locator('#menu-tools')
  await expect(menu).toBeVisible()
  await expect(menu.getByRole('link', { name: 'Server icon maker' })).toHaveAttribute('href', '/tools/server-icon')
  await expect(menu.getByRole('link', { name: 'All free tools' })).toHaveAttribute('href', '/tools')
  expect(errors()).toEqual([])
})

test('the icon maker saves a real 64 × 64 PNG of the letters, and every letters control changes the icon', async ({ page }) => {
  const errors = watchErrors(page)
  await page.goto('/tools/server-icon', { waitUntil: 'networkidle' })
  const tool = page.locator('[data-icon-tool]')
  await expect(page.locator('[data-file-info]')).toContainText(/64 × 64 PNG · \d/)
  const saved = await pngSize(await download(page, '[data-preview] [data-download]'))
  expect(saved).toMatchObject({ width: 64, height: 64 })
  expect(saved.bytes).toBeLessThan(64 * 1024)

  let before = await icon(page)
  const changes = async (what: string, act: () => Promise<unknown>) => {
    await act()
    const now = await icon(page)
    expect.soft(now, `${what} changes the icon`).not.toBe(before)
    before = now
  }
  await changes('typing letters', () => tool.locator('#icon-text').fill('AB!'))
  await expect(page.locator('[data-big]')).toHaveAttribute('aria-label', /AB! in White/)
  for (const name of ['icon-fg', 'icon-bg']) {
    const swatches = tool.locator(`input[name="${name}"]`)
    for (let i = 0; i < (await swatches.count()); i++) {
      const input = swatches.nth(i)
      if (await input.isChecked()) continue
      const id = await input.getAttribute('id')
      await changes(`the ${name} swatch ${await input.getAttribute('value')}`, () => tool.locator(`label[for="${id}"]`).click())
    }
  }
  await changes('a hex letter colour', () => tool.locator('#icon-fg-hex').fill('#ff00aa'))
  expect(await tool.locator('input[name="icon-fg"]:checked').count(), 'a typed colour unticks the swatches').toBe(0)
  const typed = await icon(page)
  await tool.locator('#icon-fg-hex').fill('#ff00a')
  await tool.locator('#icon-fg-hex').blur()
  await expect(tool.locator('#icon-fg-hex'), 'leaving a half-typed colour puts back the one in use').toHaveValue('#ff00aa')
  expect(await icon(page)).toBe(typed)
  for (const id of ['icon-effect-none', 'icon-effect-outline', 'icon-effect-shadow', 'icon-bg-solid', 'icon-bg-fade', 'icon-bg-blocks']) {
    await changes(id, () => tool.locator(`label[for="${id}"]`).click())
  }
  await changes('Shuffle the blocks', () => tool.getByRole('button', { name: 'Shuffle the blocks' }).click())
  await tool.locator('label[for="icon-bg-solid"]').click()
  await expect(tool.getByRole('button', { name: 'Shuffle the blocks' }), 'Shuffle shows only with blocks').toBeHidden()
  await tool.locator('#icon-name').fill('Pip Land')
  await expect(tool.locator('[data-list-name]')).toHaveText('Pip Land')

  // Text pasted into a field stays text, even with a picture on the
  // clipboard too, as after copying part of a web page.
  const prevented = await tool.locator('#icon-text').evaluate((el, b64) => {
    el.focus()
    const dt = new DataTransfer()
    dt.setData('text/plain', 'ABC')
    dt.items.add(new File([Uint8Array.from(atob(b64), (c) => c.charCodeAt(0))], 'copied.png', { type: 'image/png' }))
    const paste = new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true })
    el.dispatchEvent(paste)
    return paste.defaultPrevented
  }, picture.toString('base64'))
  expect(prevented, 'the field gets the text').toBe(false)
  await expect(tool.locator('#icon-source-letters')).toBeChecked()
  // A radio or slider takes no text, so with one focused, as after a click
  // on an option, the same paste opens the picture.
  await tool.locator('label[for="icon-effect-outline"]').click()
  const opened = await tool.locator('#icon-effect-outline').evaluate((el, b64) => {
    const dt = new DataTransfer()
    dt.setData('text/plain', 'ABC')
    dt.items.add(new File([Uint8Array.from(atob(b64), (c) => c.charCodeAt(0))], 'copied.png', { type: 'image/png' }))
    const paste = new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true })
    el.dispatchEvent(paste)
    return paste.defaultPrevented
  }, picture.toString('base64'))
  expect(opened, 'the picture is taken').toBe(true)
  await expect(tool.locator('#icon-source-picture')).toBeChecked()
  expect(errors()).toEqual([])
})

test('the icon maker frames a picture: opened or dropped, zoomed, moved, fitted, sharp or smooth, on a colour', async ({ page }) => {
  const errors = watchErrors(page)
  await page.goto('/tools/server-icon', { waitUntil: 'networkidle' })
  const tool = page.locator('[data-icon-tool]')
  await tool.locator('label[for="icon-source-picture"]').click()
  await expect(tool.locator('[data-empty]')).toBeVisible()
  await expect(tool.locator('[data-preview] [data-download]')).toBeDisabled()
  await expect(tool.locator('[data-picture-controls]'), 'no controls that would do nothing before there is a picture').toBeHidden()
  await tool.locator('#icon-file').setInputFiles({ name: 'landscape.png', mimeType: 'image/png', buffer: picture })
  await expect(tool.locator('[data-empty]')).toBeHidden()
  await expect(tool.locator('[data-picture-controls]')).toBeVisible()
  await expect(tool.locator('[data-drop-text]')).toHaveText('landscape.png')
  await expect(tool.locator('#icon-pixels-smooth')).toBeChecked()

  // Filled from the middle: the stripe runs down the centre, red left, blue right.
  const at = (x: number, y: number) => page.locator('[data-big]').evaluate((c: HTMLCanvasElement, [x, y]) => Array.from(c.getContext('2d')!.getImageData(x * 4 + 2, y * 4 + 2, 1, 1).data), [x, y])
  expect((await at(32, 32))[1], 'the stripe in the middle').toBeGreaterThan(120)
  expect((await at(4, 32))[0], 'red on the left').toBeGreaterThan(150)
  expect((await at(60, 32))[2], 'blue on the right').toBeGreaterThan(150)

  let before = await icon(page)
  const changes = async (what: string, act: () => Promise<unknown>) => {
    await act()
    const now = await icon(page)
    expect.soft(now, `${what} changes the icon`).not.toBe(before)
    before = now
  }
  await changes('zooming', () => slide(page, '#icon-zoom', 300))
  await expect(tool.locator('[data-zoom-value]')).toHaveText('300%')
  // The arrow keys move the picture the way they point, as dragging does:
  // the green stripe down its middle moves right with Right.
  const stripe = () => page.locator('[data-big]').evaluate((c: HTMLCanvasElement) => {
    const d = c.getContext('2d')!.getImageData(0, c.height / 2, c.width, 1).data
    let x = 0
    for (let i = 1; i < c.width; i++) if (d[i * 4 + 1] - d[i * 4] - d[i * 4 + 2] > d[x * 4 + 1] - d[x * 4] - d[x * 4 + 2]) x = i
    return x
  })
  const middle = await stripe()
  await page.locator('[data-big]').focus()
  await page.keyboard.press('Shift+ArrowRight')
  expect(await stripe(), 'Right moves the picture right').toBeGreaterThan(middle + 16)
  await page.keyboard.press('Shift+ArrowLeft')
  expect(await stripe()).toBe(middle)
  const box = (await page.locator('[data-big]').boundingBox())!
  await changes('dragging the picture', async () => {
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
    await page.mouse.down()
    await page.mouse.move(box.x + box.width / 2 + 80, box.y + box.height / 2, { steps: 5 })
    await page.mouse.up()
  })
  await changes('an arrow key on the preview', async () => {
    await page.locator('[data-big]').focus()
    await page.keyboard.press('ArrowLeft')
  })
  await changes('Whole picture', () => tool.locator('label[for="icon-fit-whole"]').click())
  await slide(page, '#icon-zoom', 100)
  before = await icon(page)
  await changes('a colour behind it', () => tool.locator('label[for="icon-pbg-night"]').click())
  expect((await at(32, 2)).slice(0, 3), 'the colour fills around a whole landscape picture').toEqual([0x1c, 0x23, 0x40])
  await changes('Sharp pixels', () => tool.locator('label[for="icon-pixels-sharp"]').click())
  await changes('no colour behind it', () => tool.locator('label[for="icon-pbg-none"]').click())
  expect((await at(32, 2))[3], 'transparent around it again').toBe(0)
  expect(await pngSize(await download(page, '[data-preview] [data-download]'))).toMatchObject({ width: 64, height: 64 })

  // Dropping a picture on the tool opens it, from the letters too.
  await tool.locator('label[for="icon-source-letters"]').click()
  const small = png(16, 16, (x, y) => ((x + y) % 2 ? [255, 210, 63, 255] : [28, 35, 64, 255]))
  await tool.locator('[data-drop]').evaluate((el, bytes) => {
    const dt = new DataTransfer()
    dt.items.add(new File([new Uint8Array(bytes)], 'pixel.png', { type: 'image/png' }))
    const root = el.closest('[data-icon-tool]')!
    root.dispatchEvent(new DragEvent('dragenter', { dataTransfer: dt, bubbles: true }))
    root.dispatchEvent(new DragEvent('drop', { dataTransfer: dt, bubbles: true }))
  }, [...small])
  await expect(tool.locator('#icon-source-picture')).toBeChecked()
  await expect(tool.locator('[data-drop-text]')).toHaveText('pixel.png')
  await expect(tool.locator('#icon-pixels-sharp'), 'a small picture is taken for pixel art').toBeChecked()
  expect(errors()).toEqual([])
})

test('on a phone the icon and Download stay at hand while the controls scroll', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL, viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true })
  const page = await ctx.newPage()
  const errors = watchErrors(page)
  await page.goto('/tools/server-icon', { waitUntil: 'networkidle' })
  const dock = page.locator('[data-dock]')
  await expect(dock).not.toHaveClass(/is-shown/)
  await expect(dock).toHaveAttribute('inert', '')
  await page.locator('#icon-bg-hex').scrollIntoViewIfNeeded()
  await expect(dock).toHaveClass(/is-shown/)
  await expect(dock).not.toHaveAttribute('inert', '')
  await expect(dock).toHaveCSS('translate', 'none')
  const gap = await dock.evaluate((el) => window.innerHeight - el.getBoundingClientRect().bottom)
  expect(gap, 'the bar sits at the bottom of the screen, not at the end of the tool').toBe(12)
  expect(await pngSize(await download(page, '[data-dock] [data-download]'))).toMatchObject({ width: 64, height: 64 })
  await page.locator('#icon-preview-title').scrollIntoViewIfNeeded()
  await expect(dock).not.toHaveClass(/is-shown/)
  expect(errors()).toEqual([])
  await ctx.close()
})
