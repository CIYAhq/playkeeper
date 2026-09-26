import { expect, test, type Locator, type Page } from '@playwright/test'
import { FakeConsole, fakePanel, paperLines } from './fake-panel'

// The Console with a long, busy log arriving over several polls, at desktop
// and phone sizes. The page keeps its height while the log scrolls inside it;
// the log follows new lines only while you're at the bottom, keeps your place
// when you scroll up, and "Jump to latest" takes you back.

const sizes = [
  { name: 'desktop', viewport: { width: 1440, height: 900 }, phone: false },
  { name: 'phone', viewport: { width: 390, height: 844 }, phone: true },
]

/** A line in the log: its time, level and message are spans. */
const rowsSelector = 'div:has(> span)'

/** Where the page and the log stand. */
function measure(log: Locator) {
  return log.evaluate((el, rowsSelector) => {
    const box = el.getBoundingClientRect()
    const rows = el.querySelectorAll(rowsSelector)
    const last = rows[rows.length - 1]?.getBoundingClientRect()
    return {
      page: document.documentElement.scrollHeight,
      window: window.innerHeight,
      top: box.top,
      bottom: box.bottom,
      height: box.height,
      scrollable: el.scrollHeight - el.clientHeight,
      fromBottom: el.scrollHeight - el.scrollTop - el.clientHeight,
      rows: rows.length,
      lastInView: !!last && last.top >= box.top && last.bottom <= box.bottom,
    }
  }, rowsSelector)
}

/** The message a row shows for a raw line: the server's own time and level are dropped. */
const message = (raw: string) => raw.replace(/^\[[^\]]*\]: /, '')

/** Waits until the page shows the newest line the server printed. */
async function caughtUp(log: Locator, fake: FakeConsole) {
  await expect
    .poll(async () => {
      const last = await log.evaluate((el, rowsSelector) => Array.from(el.querySelectorAll(rowsSelector)).at(-1)?.lastElementChild?.textContent ?? '', rowsSelector)
      return last === message(fake.last?.text ?? '')
    })
    .toBe(true)
}

/** Waits for the page's enter animations to end, so boxes are where they stay. */
async function settled(page: Page) {
  await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== 'running' || a.effect?.getComputedTiming().endTime === Infinity))
}

/** Waits for the page's next read of the log. */
async function nextRead(fake: FakeConsole) {
  const seen = fake.reads
  await expect.poll(() => fake.reads, { timeout: 10_000 }).toBeGreaterThan(seen)
}

/** Waits until the log stops moving, after a wheel, a swipe or a smooth jump. */
async function still(log: Locator) {
  let last = -1
  await expect
    .poll(async () => {
      const now = await log.evaluate((el) => el.scrollTop)
      const same = now === last
      last = now
      return same
    }, { intervals: [150] })
    .toBe(true)
}

/** Scrolls the log up the way a person does: the mouse wheel, or a swipe on a phone. */
async function scrollUp(page: Page, log: Locator, phone: boolean) {
  const box = await log.boundingBox()
  if (!box) throw new Error('The log is not on the page.')
  const x = box.x + box.width / 2
  const y = box.y + box.height / 2
  if (phone) {
    // A finger dragging down the log. Chromium's synthesizeScrollGesture ignores touch in headless mode, raw touch events don't.
    const cdp = await page.context().newCDPSession(page)
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y: y - 200 }] })
    for (let i = 1; i <= 20; i++) {
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x, y: y - 200 + i * 20 }] })
      await page.waitForTimeout(16)
    }
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
    await cdp.detach()
  } else {
    await page.mouse.move(x, y)
    await page.mouse.wheel(0, -1200)
  }
  await still(log)
}

/** Marks the first line in view, and says how far below the top of the log it is. */
function markPlace(log: Locator) {
  return log.evaluate((el, rowsSelector) => {
    const top = el.getBoundingClientRect().top
    const row = Array.from(el.querySelectorAll(rowsSelector)).find((r) => r.getBoundingClientRect().bottom > top)
    if (!row) throw new Error('No line in view.')
    row.setAttribute('data-test-place', '')
    return row.getBoundingClientRect().top - top
  }, rowsSelector)
}

/** Where the marked line is now, or null once it left the page. */
function place(log: Locator) {
  return log.evaluate((el) => {
    const row = el.querySelector('[data-test-place]')
    return row ? row.getBoundingClientRect().top - el.getBoundingClientRect().top : null
  })
}

/** Presses the button inside the page, and says how far from the bottom the log is straight after. */
function pressAndMeasure(button: Locator) {
  return button.evaluate((b) => {
    ;(b as HTMLButtonElement).click()
    const el = document.querySelector('[role="log"]')
    if (!el) throw new Error('The log is not on the page.')
    return el.scrollHeight - el.scrollTop - el.clientHeight
  })
}

for (const size of sizes) {
  test.describe(size.name, () => {
    test.use({ viewport: size.viewport, isMobile: size.phone, hasTouch: size.phone, deviceScaleFactor: size.phone ? 3 : 1 })

    test('a long log scrolls inside the page, follows at the bottom and keeps your place above it', async ({ page }) => {
      const fake = new FakeConsole()
      fake.append(paperLines(1500, 1))
      let batch = 1
      fake.beforeRead = () => fake.append(paperLines(450, ++batch))
      const { unexpected } = await fakePanel(page, fake)
      if (size.phone) {
        // An iPhone's notch and home indicator.
        const cdp = await page.context().newCDPSession(page)
        await cdp.send('Emulation.setSafeAreaInsetsOverride', { insets: { top: 47, bottom: 34 } })
      }
      await page.goto('/servers/survival/console')
      const log = page.getByRole('log', { name: 'Server output' })
      const jump = page.getByRole('button', { name: 'Jump to latest' })

      await caughtUp(log, fake)
      await settled(page)
      const start = await measure(log)
      expect(start.page, 'the page fits the window').toBeLessThanOrEqual(start.window)
      expect(start.fromBottom).toBeLessThanOrEqual(8)
      if (size.phone) {
        const tabBar = page.getByRole('navigation', { name: 'Server pages' })
        expect(await tabBar.evaluate((el) => getComputedStyle(el).paddingBottom), 'the phone has a home indicator to keep clear of').toBe('34px')
        const form = await page.getByRole('textbox', { name: 'Minecraft command' }).boundingBox()
        const tabs = await tabBar.boundingBox()
        expect(start.top, 'the log starts below the notch').toBeGreaterThan(47)
        expect((form?.y ?? 0) + (form?.height ?? 0), 'the command box sits above the tab bar').toBeLessThanOrEqual(tabs?.y ?? 0)
      }

      // Thousands of lines over several polls: the page stays put while the log follows them.
      for (let i = 0; i < 4; i++) {
        await nextRead(fake)
        await caughtUp(log, fake)
        const now = await measure(log)
        expect(now.page, 'the page keeps its height').toBe(start.page)
        expect(now.height, 'the log keeps its height').toBe(start.height)
        expect(now.fromBottom, 'the log follows new lines').toBeLessThanOrEqual(8)
        expect(now.lastInView).toBe(true)
        await expect(jump).toBeHidden()
      }
      const full = await measure(log)
      expect(full.rows, 'the page keeps the newest 2000 lines').toBe(2000)
      expect(full.scrollable, 'the log scrolls').toBeGreaterThan(full.height * 10)

      // Scrolling up stops following and offers the way back, without moving anything.
      await scrollUp(page, log, size.phone)
      await expect(jump).toBeVisible()
      const up = await measure(log)
      expect(up.fromBottom).toBeGreaterThan(200)
      expect([up.page, up.top, up.height], 'nothing moves when the button appears').toEqual([start.page, start.top, start.height])

      // New lines arrive and the oldest drop off, but the line you're reading stays put.
      const at = await markPlace(log)
      for (let i = 0; i < 2; i++) {
        await nextRead(fake)
        await caughtUp(log, fake)
        const now = await place(log)
        expect(now, 'the line you were reading is still there').not.toBeNull()
        expect(Math.abs((now ?? 0) - at), 'the line you were reading stays where it was').toBeLessThan(2)
        expect((await measure(log)).page).toBe(start.page)
        await expect(jump).toBeVisible()
      }

      // "Jump to latest" goes back to the newest line and follows again.
      if (size.phone) await jump.tap()
      else await jump.click()
      await expect(jump).toBeHidden()
      await expect.poll(async () => (await measure(log)).fromBottom).toBeLessThanOrEqual(8)
      await expect(log).toBeFocused()
      await nextRead(fake)
      await caughtUp(log, fake)
      const back = await measure(log)
      expect(back.fromBottom, 'following again').toBeLessThanOrEqual(8)
      expect(back.lastInView).toBe(true)

      // The same with the keyboard: Page Up in the log, Tab to the button, Enter.
      await page.keyboard.press('PageUp')
      await expect(jump).toBeVisible()
      await page.keyboard.press('Tab')
      await expect(jump).toBeFocused()
      await page.keyboard.press('Enter')
      await expect(jump).toBeHidden()
      await expect.poll(async () => (await measure(log)).fromBottom).toBeLessThanOrEqual(8)
      await expect(log).toBeFocused()

      // The jump glides down, unless the system asks for reduced motion.
      await scrollUp(page, log, size.phone)
      await expect(jump).toBeVisible()
      expect(await pressAndMeasure(jump), 'a smooth scroll starts on the next frame').toBeGreaterThan(8)
      await expect.poll(async () => (await measure(log)).fromBottom).toBeLessThanOrEqual(8)
      await page.emulateMedia({ reducedMotion: 'reduce' })
      await scrollUp(page, log, size.phone)
      await expect(jump).toBeVisible()
      expect(await pressAndMeasure(jump), 'with reduced motion the jump is instant').toBeLessThanOrEqual(8)
      await expect(jump).toBeHidden()

      const end = await measure(log)
      expect(end.page).toBe(start.page)
      expect(end.rows).toBe(2000)
      expect(unexpected, 'API calls the fake panel does not answer').toEqual([])
    })
  })
}
