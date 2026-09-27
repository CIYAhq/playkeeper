// Social previews for playkeeper.io (site/static/og): 1200 × 630 PNGs with
// the page's title and Pip on pixel ground, drawn from the site's own art.
// The 0.4.0 post's preview is its cover. Run from test/e2e/ui:
//   node site-og.mjs            (writes ../../../site/static/og/*.png)
import { chromium } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const repo = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..')
const out = path.join(repo, 'site/static/og')
const art = (p) => 'data:image/svg+xml;base64,' + fs.readFileSync(path.join(repo, p)).toString('base64')

const previews = {
  default: { title: 'Your VPS. Your Minecraft servers. Your worlds.', pip: 'pip-wave' },
  landing: { title: 'Your VPS. Your Minecraft servers. Your worlds.', pip: 'pip-wave' },
  'mods-and-modpacks': { eyebrow: 'Feature', title: 'Plugins, mods and modpacks. One click each.', pip: 'pip-cheer' },
  aternos: { eyebrow: 'Compare', title: 'The Aternos alternative with no queue', pip: 'pip-wave' },
  pterodactyl: { eyebrow: 'Compare', title: 'A Pterodactyl alternative for one VPS and a few friends', pip: 'pip-box' },
  'modded-minecraft-server': { eyebrow: 'Guide', title: 'How to make a modded Minecraft server', pip: 'pip-hardhat' },
  sizing: { eyebrow: 'Guide', title: 'How much RAM does a Minecraft server need?', pip: 'pip-search' },
  docs: { eyebrow: 'Docs', title: 'Playkeeper docs', pip: 'pip-letter' },
  pricing: { eyebrow: 'Pricing', title: 'Free and open source. You only pay for your VPS.', pip: 'pip-box' },
  blog: { eyebrow: 'Blog', title: 'Releases, guides and building Playkeeper in public', pip: 'pip-letter' },
  t: { eyebrow: 'Server template', title: 'A Minecraft server setup, shared from Playkeeper', pip: 'pip-search' },
}

const page = (p) => `<!doctype html><html><head><style>
  body { margin: 0; width: 1200px; height: 630px; background: #f2f2ec; font-family: system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; color: #1d211c; overflow: hidden; position: relative; }
  .brand { position: absolute; left: 72px; top: 64px; display: flex; align-items: center; gap: 14px; font-size: 30px; font-weight: 700; letter-spacing: -0.02em; }
  .brand img { width: 48px; height: 48px; border-radius: 12px; }
  .eyebrow { position: absolute; left: 72px; top: 196px; font-size: 22px; font-weight: 700; letter-spacing: 0.08em; text-transform: uppercase; color: #166534; }
  h1 { position: absolute; left: 72px; top: 236px; width: 760px; margin: 0; font-size: 68px; line-height: 1.02; font-weight: 800; letter-spacing: -0.045em; }
  .pip { position: absolute; right: 120px; bottom: 96px; width: 230px; }
  .ground { position: absolute; left: 0; right: 0; bottom: 0; height: 108px; background: url("${art('site/static/img/ground-hills.svg')}") repeat-x left bottom / 720px 108px; image-rendering: pixelated; }
  .url { position: absolute; right: 72px; top: 76px; font-size: 24px; color: #5c6157; font-weight: 500; }
</style></head><body>
  <div class="brand"><img src="${art('web/src/assets/brand/playkeeper-mark.svg')}"><span>Playkeeper</span></div>
  <div class="url">playkeeper.io</div>
  ${p.eyebrow ? `<div class="eyebrow">${p.eyebrow}</div>` : ''}
  <h1${p.eyebrow ? '' : ' style="top:200px"'}>${p.title}</h1>
  <img class="pip" src="${art(`web/src/assets/pip/${p.pip}.svg`)}">
  <div class="ground"></div>
</body></html>`

const cover = `<!doctype html><html><head><style>
  body { margin: 0; width: 1200px; height: 630px; background: #34416d; overflow: hidden; }
  img { width: 1200px; height: 630px; object-fit: contain; object-position: center bottom; image-rendering: pixelated; }
</style></head><body><img src="${art('site/static/img/blog/playkeeper-0-4-0.svg')}"></body></html>`

const browser = await chromium.launch()
const ctx = await browser.newContext({ viewport: { width: 1200, height: 630 }, deviceScaleFactor: 1 })
const tab = await ctx.newPage()
for (const [name, p] of Object.entries(previews)) {
  await tab.setContent(page(p), { waitUntil: 'load' })
  await tab.screenshot({ path: path.join(out, `${name}.png`) })
  console.log(name)
}
await tab.setContent(cover, { waitUntil: 'load' })
await tab.screenshot({ path: path.join(out, 'playkeeper-0-4-0.png') })
console.log('playkeeper-0-4-0')
await browser.close()
