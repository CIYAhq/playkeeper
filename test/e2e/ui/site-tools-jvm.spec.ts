import { expect, test, type Page } from '@playwright/test'
import fs from 'node:fs'

// The JVM arguments generator on playkeeper.io (/tools/jvm-flags), in a
// browser with the site's Content-Security-Policy: Aikar's flags, ZGC and
// Java's defaults in each format, the heap and Java Playkeeper would pick,
// the launcher's own arguments, and that every control changes the result.
// internal/site/jvm_test.go checks the script's numbers against Playkeeper's.

const AIKAR = '-XX:+UseG1GC -XX:+ParallelRefProcEnabled -XX:MaxGCPauseMillis=200 -XX:+UnlockExperimentalVMOptions -XX:+DisableExplicitGC -XX:+AlwaysPreTouch -XX:G1NewSizePercent=30 -XX:G1MaxNewSizePercent=40 -XX:G1HeapRegionSize=8M -XX:G1ReservePercent=20 -XX:G1HeapWastePercent=5 -XX:G1MixedGCCountTarget=4 -XX:InitiatingHeapOccupancyPercent=15 -XX:G1MixedGCLiveThresholdPercent=90 -XX:G1RSetUpdatingPauseTimePercent=5 -XX:SurvivorRatio=32 -XX:+PerfDisableSharedMem -XX:MaxTenuringThreshold=1 -Dusing.aikars.flags=https://mcflags.emc.gs -Daikars.new.flags=true'

function watchErrors(page: Page) {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()) })
  return () => errors.filter((e) => !/api\.github\.com|Failed to load resource/.test(e))
}

// A slider moved as a person drags it: its value, then an input event.
const slide = (page: Page, selector: string, value: number) => page.locator(selector).evaluate((el: HTMLInputElement, v) => {
  el.value = String(v)
  el.dispatchEvent(new Event('input', { bubbles: true }))
}, value)
const out = (page: Page) => page.locator('#jvm-out').textContent().then((s) => s ?? '')
const pick = (page: Page, id: string) => page.locator(`label[for="${id}"]`).click()
const version = (page: Page, v: string) => page.locator('#jvm-version').fill(v)

test('a server: Aikar\'s flags in a start script, with the heap and Java Playkeeper would pick', async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/tools/jvm-flags', { waitUntil: 'networkidle' })
  const tool = page.locator('[data-jvm-tool]')

  // It starts as Paper's own docs write the flags, with 6 of 8 GB as heap.
  const first = `#!/usr/bin/env sh\n# A 6 GB heap with Aikar's flags, on Java 25. playkeeper.io/tools/jvm-flags\ncd "$(dirname "$0")"\njava -Xms6144M -Xmx6144M ${AIKAR} -jar paper.jar --nogui`
  expect(await out(page)).toBe(first)
  await expect(tool.locator('[data-heap-size]')).toHaveText('6 GB')
  await expect(tool.locator('[data-outside-size]')).toHaveText('2 GB')

  // Each Minecraft version's Java, as minecraft.JavaFor picks it.
  for (const [v, java] of [['1.8.9', 8], ['1.16.5', 8], ['1.17', 16], ['1.17.1', 16], ['1.18', 17], ['1.20.4', 17], ['1.20.5', 21], ['1.21.11', 21], ['26.1', 25], ['26.4-snapshot-1', 25]] as const) {
    await version(page, v)
    await expect(tool.locator('[data-java]'), v).toHaveText(`Runs on Java ${java}`)
  }
  await version(page, '1.21.x')
  await expect(tool.locator('[data-java]')).toHaveText('Type a version like 1.21.1')
  await expect(page.locator('#jvm-version')).toHaveAttribute('aria-invalid', '')
  await page.locator('#jvm-version').blur()
  await expect(page.locator('#jvm-version'), 'leaving a bad version puts the last good one back').toHaveValue('26.4-snapshot-1')
  await version(page, '26.3')

  // From 12 GB of heap, Aikar's larger values.
  await slide(page, '#jvm-memory', 16)
  await expect(tool.locator('[data-heap-size]')).toHaveText('12 GB')
  let text = await out(page)
  expect(text).toContain('-Xms12288M -Xmx12288M')
  for (const f of ['G1NewSizePercent=40', 'G1MaxNewSizePercent=50', 'G1HeapRegionSize=16M', 'G1ReservePercent=15', 'InitiatingHeapOccupancyPercent=20']) expect(text).toContain(`-XX:${f} `)
  await expect(tool.locator('[data-flags-hint]')).toContainText('larger values')
  await slide(page, '#jvm-memory', 8)

  // Fabric keeps more outside the heap for its mods: 4 GB with 200 mods
  // leaves 768 + 6 × 200 MB.
  await pick(page, 'jvm-type-fabric')
  await expect(page.locator('#jvm-jar')).toHaveValue('fabric-server-launch.jar')
  await expect(tool.locator('[data-mods-field]')).toBeVisible()
  await slide(page, '#jvm-memory', 4)
  await slide(page, '#jvm-mods', 200)
  expect(await out(page)).toContain('java -Xms2128M -Xmx2128M ')
  expect(await out(page)).toMatch(/-jar fabric-server-launch\.jar nogui$/)

  // NeoForge and Forge from 1.17 take user_jvm_args.txt, one argument a line.
  await pick(page, 'jvm-type-neoforge')
  await version(page, '1.21.1')
  await slide(page, '#jvm-memory', 8)
  await expect(page.locator('label[for="jvm-format-sh"]')).toBeHidden()
  await expect(page.locator('label[for="jvm-format-args"]')).toBeVisible()
  await expect(tool.locator('[data-out-title]')).toHaveText('user_jvm_args.txt')
  await expect(tool.locator('[data-jar-field]')).toBeHidden()
  await expect(tool.locator('[data-restart-field]')).toBeHidden()
  text = await out(page)
  expect(text.split('\n').slice(0, 4)).toEqual(["# A 5.8 GB heap with Aikar's flags, on Java 21. playkeeper.io/tools/jvm-flags", '-Xms5968M', '-Xmx5968M', '-XX:+UseG1GC'])
  await expect(tool.locator('[data-next]')).toContainText('./run.sh nogui')
  // Before 1.17, Forge starts from its jar.
  await version(page, '1.16.5')
  await expect(tool.locator('[data-out-title]')).toHaveText('start.sh')
  expect(await out(page)).toMatch(/-jar forge\.jar nogui$/)

  // ZGC: generational on Java 21 needs asking for; Java 25 adds compact
  // object headers; before Java 21 it isn't offered.
  await pick(page, 'jvm-type-paper')
  await version(page, '1.21.1')
  await pick(page, 'jvm-flags-zgc')
  expect(await out(page)).toContain('java -Xms6144M -Xmx6144M -XX:+UseZGC -XX:+ZGenerational -XX:+AlwaysPreTouch -XX:+UseStringDeduplication -jar paper.jar --nogui')
  await version(page, '26.3')
  expect(await out(page)).toContain('java -Xms6144M -Xmx6144M -XX:+UseZGC -XX:+UseCompactObjectHeaders -XX:+AlwaysPreTouch -XX:+UseStringDeduplication -jar paper.jar --nogui')
  await version(page, '1.20.1')
  await expect(page.locator('#jvm-flags-zgc')).toBeDisabled()
  await expect(page.locator('#jvm-flags-aikar')).toBeChecked()
  await expect(tool.locator('[data-flags-hint]')).toContainText('ZGC needs Minecraft 1.20.5 or newer')
  await pick(page, 'jvm-flags-none')
  expect(await out(page)).toContain('\njava -Xms6144M -Xmx6144M -jar paper.jar --nogui')
  await pick(page, 'jvm-flags-aikar')

  // The GC log: Java 11 and newer need its folder first, Java 8 has its own.
  await version(page, '26.3')
  await page.locator('label[for="jvm-gclog"]').click()
  text = await out(page)
  expect(text).toContain('\nmkdir -p logs\n')
  expect(text).toContain(" '-Xlog:gc*:logs/gc.log:time,uptime:filecount=5,filesize=1M' -jar")
  await pick(page, 'jvm-format-flags')
  expect(await out(page)).toBe(`-Xms6144M -Xmx6144M ${AIKAR} -Xlog:gc*:logs/gc.log:time,uptime:filecount=5,filesize=1M`)
  await expect(tool.locator('[data-next]')).toContainText('Make a logs folder')
  await expect(tool.locator('[data-download]')).toBeHidden()
  await pick(page, 'jvm-format-sh')
  await version(page, '1.12.2')
  text = await out(page)
  expect(text).not.toContain('mkdir')
  expect(text).toContain(' -Xloggc:gc.log -verbose:gc -XX:+PrintGCDetails -XX:+PrintGCDateStamps -XX:+PrintGCTimeStamps -XX:+UseGCLogFileRotation -XX:NumberOfGCLogFiles=5 -XX:GCLogFileSize=1M -jar')
  await page.locator('label[for="jvm-gclog"]').click()
  await version(page, '26.3')

  // Restarting, and a jar name that needs quoting, in both scripts.
  await page.locator('label[for="jvm-restart"]').click()
  await page.locator('#jvm-jar').fill('my server.jar')
  text = await out(page)
  expect(text).toContain("\nwhile true; do\n  java -Xms6144M")
  expect(text).toContain(" -jar 'my server.jar' --nogui\n  echo \"Restarting in 5 seconds. Press Ctrl+C to stop.\"\n  sleep 5\ndone")
  await pick(page, 'jvm-format-bat')
  text = await out(page)
  expect(text.split('\n').slice(0, 4)).toEqual(['@echo off', "rem A 6 GB heap with Aikar's flags, on Java 25. playkeeper.io/tools/jvm-flags", 'cd /d "%~dp0"', ':start'])
  expect(text).toMatch(/ -jar "my server\.jar" --nogui\necho Restarting in 5 seconds\. Press Ctrl\+C to stop\.\ntimeout \/t 5 \/nobreak >nul\ngoto start$/)
  // A .bat is saved with Windows line ends.
  const [file] = await Promise.all([page.waitForEvent('download'), tool.locator('[data-download]').click()])
  expect(file.suggestedFilename()).toBe('start.bat')
  const saved = fs.readFileSync((await file.path()) as string, 'utf8')
  expect(saved).toBe(text.replace(/\n/g, '\r\n') + '\r\n')
  await page.locator('label[for="jvm-restart"]').click()
  await page.locator('#jvm-jar').fill('')
  await expect(tool.locator('[data-next]'), 'an empty jar name means the usual one').toContainText('paper.jar')
  expect(await out(page)).toMatch(/-jar paper\.jar --nogui\npause$/)
  expect(errors()).toEqual([])
})

test('every control changes the arguments, and Copy copies them', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL, viewport: { width: 1440, height: 900 }, permissions: ['clipboard-read', 'clipboard-write'] })
  const page = await ctx.newPage()
  const errors = watchErrors(page)
  await page.goto('/tools/jvm-flags', { waitUntil: 'networkidle' })
  const changes = async (what: string, act: () => Promise<unknown>) => {
    const before = await out(page)
    await act()
    expect.soft(await out(page), `${what} changes the arguments`).not.toBe(before)
  }
  await version(page, '1.21.1')
  for (const id of ['jvm-type-vanilla', 'jvm-type-fabric', 'jvm-type-neoforge', 'jvm-type-paper', 'jvm-flags-zgc', 'jvm-flags-none', 'jvm-flags-aikar', 'jvm-format-bat', 'jvm-format-flags', 'jvm-format-sh']) {
    await changes(id, () => pick(page, id))
  }
  await changes('the version', () => version(page, '1.12.2'))
  await changes('the memory', () => slide(page, '#jvm-memory', 12))
  await pick(page, 'jvm-type-fabric')
  await changes('the mods', () => slide(page, '#jvm-mods', 400))
  await changes('restarting', () => page.locator('label[for="jvm-restart"]').click())
  await changes('the GC log', () => page.locator('label[for="jvm-gclog"]').click())
  await changes('the jar', () => page.locator('#jvm-jar').fill('fabric.jar'))
  await changes('the game', () => pick(page, 'jvm-for-game'))
  await changes('its memory', () => slide(page, '#jvm-game-memory', 8))
  await changes('another launcher', () => pick(page, 'jvm-launcher-other'))

  await page.locator('[data-preview] [data-tool-copy]').click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(await out(page))
  await expect(page.locator('[data-preview] [data-tool-copy]')).toHaveClass(/is-copied/)
  expect(errors()).toEqual([])
  await ctx.close()
})

test("the game: the launcher's own arguments, with more memory", async ({ page }) => {
  const errors = watchErrors(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/tools/jvm-flags', { waitUntil: 'networkidle' })
  const tool = page.locator('[data-jvm-tool]')
  await pick(page, 'jvm-for-game')
  await expect(tool.locator('[data-panel="server"]')).toBeHidden()
  await expect(tool.locator('[data-heap]')).toBeHidden()
  await expect(tool.locator('[data-formats]')).toBeHidden()
  await expect(tool.locator('[data-download]')).toBeHidden()
  await expect(tool.locator('[data-out-title]')).toHaveText('JVM arguments')
  // Since 26.1, 4 GB with ZGC; before, 2 GB with G1.
  expect(await out(page)).toBe('-Xms4G -Xmx4G -XX:+UseCompactObjectHeaders -XX:+AlwaysPreTouch -XX:+UseStringDeduplication -XX:+UseZGC')
  await slide(page, '#jvm-game-memory', 6)
  expect(await out(page)).toBe('-Xms6G -Xmx6G -XX:+UseCompactObjectHeaders -XX:+AlwaysPreTouch -XX:+UseStringDeduplication -XX:+UseZGC')
  await version(page, '1.21.1')
  expect(await out(page)).toBe('-Xmx6G -XX:+UnlockExperimentalVMOptions -XX:+UseG1GC -XX:G1NewSizePercent=20 -XX:G1ReservePercent=20 -XX:MaxGCPauseMillis=50 -XX:G1HeapRegionSize=32M')
  // Other launchers have a memory setting of their own.
  await pick(page, 'jvm-launcher-other')
  expect(await out(page)).toBe('-XX:+UnlockExperimentalVMOptions -XX:+UseG1GC -XX:G1NewSizePercent=20 -XX:G1ReservePercent=20 -XX:MaxGCPauseMillis=50 -XX:G1HeapRegionSize=32M')
  await expect(tool.locator('[data-next]')).toContainText('set its memory to 6 GB')
  await pick(page, 'jvm-for-server')
  await expect(tool.locator('[data-out-title]')).toHaveText('start.sh')
  expect(errors()).toEqual([])
})

test('on a phone, Copy stays at hand in a bar while the settings scroll', async ({ browser, baseURL }) => {
  const ctx = await browser.newContext({ baseURL, viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, permissions: ['clipboard-read', 'clipboard-write'] })
  const page = await ctx.newPage()
  const errors = watchErrors(page)
  await page.goto('/tools/jvm-flags', { waitUntil: 'networkidle' })
  const dock = page.locator('[data-dock]')
  await page.locator('#jvm-version').scrollIntoViewIfNeeded()
  await expect(dock).toHaveClass(/is-shown/)
  await expect(dock).not.toHaveAttribute('inert')
  await expect(dock).toContainText('6 GB heap · Aikar\'s flags')
  await expect(dock).toHaveCSS('translate', 'none')
  const gap = await dock.evaluate((el) => window.innerHeight - el.getBoundingClientRect().bottom)
  expect(gap, 'the bar sits at the bottom of the screen, not at the end of the tool').toBe(12)
  const box = await dock.locator('[data-tool-copy]').boundingBox()
  if (!box) throw new Error('the bar has no Copy button')
  await page.touchscreen.tap(box.x + box.width / 2, box.y + box.height / 2)
  await expect(dock.locator('[data-tool-copy]')).toHaveClass(/is-copied/)
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(await out(page))
  await page.locator('#jvm-out').scrollIntoViewIfNeeded()
  await expect(dock).not.toHaveClass(/is-shown/)
  await expect(dock).toHaveAttribute('inert')
  const wide = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(wide, 'nothing sticks out sideways').toBe(0)
  expect(errors()).toEqual([])
  await ctx.close()
})
