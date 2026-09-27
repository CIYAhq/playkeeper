// Screenshots for playkeeper.io, taken from the live demo: the dashboard with
// its sample data, as the site shows it (site/static/shots). Each shot is a
// page of the demo at a desktop or phone size, cropped to one readable part,
// at twice the pixels; site/tools/shots.py turns them into WebP at 1x and 2x.
// The demo's own line ("Live demo · resets every hour") is hidden and its
// made-up address reads as the site's example, alex.playkeeper.io.
// Usage: node site-captures.mjs <demo-url> <out-dir> [name...]
//   for example: node site-captures.mjs http://127.0.0.1:8080/demo /tmp/captures
import { chromium } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

const [demo, out, ...only] = process.argv.slice(2)
if (!demo || !out) {
  console.error('usage: node site-captures.mjs <demo-url> <out-dir> [name...]')
  process.exit(2)
}

const desktop = { width: 1280, height: 800 }
const phone = { width: 390, height: 844 }

// Each shot: the page, its size, what to do first, and the part to keep:
// the cards whose headings are given (and what's between them), or a
// rectangle in CSS pixels, or the whole view.
const shots = [
  { name: 'loop-overview', route: '/servers/survival', size: desktop },
  { name: 'loop-new-server', route: '/servers/new', size: desktop },
  { name: 'loop-setting-up', route: '/servers/new', size: desktop, create: 900 },
  { name: 'loop-setting-up-2', route: '/servers/new', size: desktop, create: 7000 },
  { name: 'demo-home', route: '/', size: desktop, keepDemo: true },
  { name: 'ptero-hero', route: '/servers/survival', size: desktop, panel: true },
  { name: 'ptero-world', route: '/servers/survival/world', size: desktop, panel: true },
  { name: 'post-backups', route: '/servers/survival/world', size: desktop, cards: ['Make a backup', 'World'] },
  { name: 'post-address', route: '/servers/survival', size: desktop, cards: ['Join address', 'Playing now'] },
  { name: 'feature-mods', route: '/servers/cobblemon/mods', size: desktop, panel: true, start: true },
  { name: 'post-mods', route: '/servers/cobblemon/mods', size: desktop, cards: ['Fabric API', 'Lithium'], from: 'Friends need', start: true },
  { name: 'guide-mods-tab', route: '/servers/cobblemon/mods', size: desktop, cards: ['Fabric API', 'Lithium'], from: 'Friends need', start: true },
  { name: 'detail-browse', route: '/servers/survival/plugins/browse', size: desktop, cards: ['CoreProtect', 'Chunky', 'ViaVersion'], from: 'Picked by Playkeeper' },
  { name: 'step-type', route: '/servers/new', size: desktop, cards: ['Purpur', 'Quilt', 'Vanilla'], from: 'A server type' },
  { name: 'guide-crash', route: '/servers/cobblemon', size: desktop, cards: ['What happened', 'How to fix it'] },
  { name: 'detail-updates', route: '/servers/survival/plugins', size: desktop, cards: ['BlueMap', 'ViaVersion'], from: 'Plugins on Survival' },
  { name: 'feat-backups', route: '/servers/survival/world', size: phone, clip: { x: 0, y: 0, width: 390, height: 380 } },
  { name: 'feat-crash', route: '/servers/cobblemon', size: phone, clip: { x: 0, y: 0, width: 390, height: 380 } },
  { name: 'feat-friends', route: '/servers/survival/players', size: phone, clip: { x: 0, y: 0, width: 390, height: 380 } },
  { name: 'feat-address', route: '/machines/q7m2vk9xpd/settings', size: phone, clip: { x: 0, y: 0, width: 390, height: 380 } },
  { name: 'feat-types', route: '/servers/new', size: phone, clip: { x: 0, y: 0, width: 390, height: 380 } },
]

// cardClip is the rectangle around the cards with the given headings, and
// from the element with the text "from" when there's one, with a margin.
async function cardClip(page, headings, from) {
  return page.evaluate(({ headings, from }) => {
    const all = [...document.querySelectorAll('body *')]
    const byText = (text) => all.find((el) => el.children.length === 0 && el.textContent.trim() === text && el.getBoundingClientRect().width > 0)
    const card = (el) => {
      for (let n = el; n && n !== document.body; n = n.parentElement) {
        const cs = getComputedStyle(n)
        if (parseFloat(cs.borderTopLeftRadius) >= 12 && cs.borderTopWidth !== '0px' && n.getBoundingClientRect().width > 160) return n
      }
      return el
    }
    const boxes = headings.map((h) => { const el = byText(h); if (!el) throw new Error('no ' + h); return card(el).getBoundingClientRect() })
    if (from) { const el = [...document.querySelectorAll('body *')].find((e) => e.children.length === 0 && e.textContent.trim().startsWith(from)); if (el) boxes.push(el.getBoundingClientRect()) }
    const x = Math.min(...boxes.map((b) => b.left)) - 10
    const y = Math.min(...boxes.map((b) => b.top)) - 10
    const r = Math.max(...boxes.map((b) => b.right)) + 10
    const bottom = Math.max(...boxes.map((b) => b.bottom)) + 10
    return { x: Math.max(0, x), y: Math.max(0, y), width: r - Math.max(0, x), height: bottom - Math.max(0, y) }
  }, { headings, from })
}

// panelClip is the dashboard's main panel, without the sidebar.
async function panelClip(page, height) {
  return page.evaluate((height) => {
    const main = document.querySelector('main') || document.body
    const b = main.getBoundingClientRect()
    return { x: b.left, y: 0, width: Math.min(window.innerWidth, b.right + 8) - b.left, height: Math.min(height, window.innerHeight) }
  }, height)
}

// Hides the demo's own line and gives its made-up address the site's name.
async function dress(page, keepDemo) {
  await page.evaluate((keep) => {
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
    const texts = []
    while (walker.nextNode()) texts.push(walker.currentNode)
    for (const t of texts) {
      if (!keep && /Live demo · resets every hour/.test(t.textContent)) {
        const line = t.parentElement.closest('p, div')
        if (line) line.style.display = 'none'
      }
      if (t.textContent.includes('demo.playkeeper.io')) t.textContent = t.textContent.replaceAll('demo.playkeeper.io', 'alex.playkeeper.io')
    }
    // The demo's own toasts ("That was a demo start") stay out of the shots.
    for (const el of document.querySelectorAll('[data-demo-toast]')) {
      let n = el
      while (n && n !== document.body && getComputedStyle(n).position !== 'fixed') n = n.parentElement
      if (n && n !== document.body) n.style.display = 'none'
    }
  }, keepDemo)
}

fs.mkdirSync(out, { recursive: true })
const browser = await chromium.launch()
for (const shot of shots.filter((s) => !only.length || only.includes(s.name))) {
  const isPhone = shot.size === phone
  const ctx = await browser.newContext({ viewport: shot.size, deviceScaleFactor: 2, isMobile: isPhone, hasTouch: isPhone, locale: 'en-GB', timezoneId: 'UTC', reducedMotion: 'reduce' })
  const page = await ctx.newPage()
  await page.goto(demo + shot.route, { waitUntil: 'networkidle' })
  if (shot.start) {
    const start = page.getByRole('button', { name: /^Start again$/ })
    if (await start.count()) {
      await start.first().click()
      await page.getByText('Online', { exact: true }).first().waitFor({ timeout: 30_000 })
    }
  }
  if (shot.create) {
    // Through New server's steps with what it picks, then the new
    // server's setup, shot.create milliseconds in.
    for (let step = 0; step < 8; step++) {
      const next = page.getByRole('button', { name: /^(Continue to|Create and start)/ }).filter({ visible: true })
      if (!(await next.count())) break
      const eula = page.getByRole('checkbox', { name: /I accept/ })
      if ((await eula.count()) && !(await eula.first().isChecked())) await eula.first().click()
      await next.first().click({ timeout: 10_000 }).catch(async (err) => {
        await page.screenshot({ path: path.join(out, `_failed-${shot.name}-${step}.png`) })
        console.error('stuck at', await next.first().innerText())
        throw err
      })
      await page.waitForTimeout(700)
    }
    await page.getByText(/^Setting up /).first().waitFor({ timeout: 15_000 })
    await page.waitForTimeout(shot.create)
  }
  await page.waitForTimeout(800)
  await dress(page, shot.keepDemo)
  await page.waitForTimeout(200)
  let clip = shot.clip
  if (shot.cards) clip = await cardClip(page, shot.cards, shot.from)
  if (shot.panel) clip = await panelClip(page, 560)
  const file = path.join(out, `${shot.name}.png`)
  await page.screenshot({ path: file, clip, animations: 'disabled' })
  console.log(file)
  await ctx.close()
}
await browser.close()
