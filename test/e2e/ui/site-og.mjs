// Social previews for playkeeper.io (site/static/og): 1200 × 630 PNGs with
// the page's title and Pip on pixel ground, drawn from the site's own art.
// The 0.4.0 post's preview is its cover; the live demo's goes with the demo,
// which serves it itself (web/src/demo/vite.ts). The titles are Inter
// ExtraBold, so draw them where Inter has that weight. Run from test/e2e/ui:
//   node site-og.mjs            (writes ../../../site/static/og/*.png)
//   node site-og.mjs demo docs  (draws only those)
import { chromium } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const repo = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..')
const out = path.join(repo, 'site/static/og')
const art = (p) => 'data:image/svg+xml;base64,' + fs.readFileSync(path.join(repo, p)).toString('base64')

const previews = {
  default: { title: 'Host your own Minecraft server. Online 24/7.', pip: 'pip-wave' },
  landing: { title: 'Host your own Minecraft server. Online 24/7.', pip: 'pip-wave' },
  'mods-and-modpacks': { eyebrow: 'Feature', title: 'Plugins, mods and modpacks. One click each.', pip: 'pip-cheer' },
  aternos: { eyebrow: 'Compare', title: 'The Aternos alternative with no queue', pip: 'pip-wave' },
  pterodactyl: { eyebrow: 'Compare', title: 'A Pterodactyl alternative for one VPS and a few friends', pip: 'pip-box' },
  'modded-minecraft-server': { eyebrow: 'Guide', title: 'How to make a modded Minecraft server', pip: 'pip-hardhat' },
  'add-mods-to-minecraft-server': { eyebrow: 'Guide', title: 'How to add mods to a Minecraft server', pip: 'pip-box' },
  'fabric-server': { eyebrow: 'Guide', title: 'How to make a Fabric server', pip: 'pip-hardhat' },
  'play-minecraft-with-friends': { eyebrow: 'Guide', title: 'How to play Minecraft Java with friends', pip: 'pip-cheer' },
  'can-java-and-bedrock-play-together': { eyebrow: 'Guide', title: 'Can Java and Bedrock play together?', pip: 'pip-search' },
  'minecraft-crossplay-server': { eyebrow: 'Guide', title: 'How to make a Minecraft crossplay server', pip: 'pip-hardhat' },
  'minecraft-server-cost': { eyebrow: 'Guide', title: 'What a Minecraft server really costs', pip: 'pip-search' },
  'modded-minecraft-server-ram': { eyebrow: 'Guide', title: 'How much RAM modpack servers really use', pip: 'pip-search' },
  'popular-minecraft-plugins': { eyebrow: 'Guide', title: 'The plugins 188,000 Minecraft servers run', pip: 'pip-box' },
  sizing: { eyebrow: 'Guide', title: 'How much RAM does a Minecraft server need?', pip: 'pip-search' },
  'hostinger-minecraft-server': { eyebrow: 'Guide', title: 'How to host a Minecraft server on Hostinger', pip: 'pip-hardhat' },
  'digitalocean-minecraft-server': { eyebrow: 'Guide', title: 'How to host a Minecraft server on DigitalOcean', pip: 'pip-hardhat' },
  'vultr-minecraft-server': { eyebrow: 'Guide', title: 'How to host a Minecraft server on Vultr', pip: 'pip-hardhat' },
  'port-forward-minecraft-server': { eyebrow: 'Guide', title: 'How to port forward a Minecraft server', pip: 'pip-hardhat' },
  'make-a-minecraft-server': { eyebrow: 'Guide', title: 'How to make a Minecraft server', pip: 'pip-hardhat' },
  errors: { eyebrow: 'Guide', title: 'Minecraft server errors, and how to fix them', pip: 'pip-hurt' },
  'error-connection-refused': { eyebrow: 'Error fix', title: 'Connection refused: what it means, and the fix', pip: 'pip-hurt' },
  'error-unable-to-access-jarfile': { eyebrow: 'Error fix', title: 'Error: Unable to access jarfile, and the fix', pip: 'pip-hurt' },
  'error-failed-to-verify-username': { eyebrow: 'Error fix', title: 'Failed to verify username! What it means, and the fix', pip: 'pip-hurt' },
  'error-cant-keep-up': { eyebrow: 'Error fix', title: "Can't keep up! Is the server overloaded? What it means", pip: 'pip-hurt' },
  'error-failed-to-bind-to-port': { eyebrow: 'Error fix', title: 'FAILED TO BIND TO PORT! What it means, and the fix', pip: 'pip-hurt' },
  'error-agree-to-the-eula': { eyebrow: 'Error fix', title: 'You need to agree to the EULA: the fix', pip: 'pip-hurt' },
  'error-outdated-server': { eyebrow: 'Error fix', title: 'Outdated server or Incompatible client: the fix', pip: 'pip-hurt' },
  'error-timed-out': { eyebrow: 'Error fix', title: 'Timed out on a Minecraft server: what it means', pip: 'pip-hurt' },
  'error-ticking-entity': { eyebrow: 'Error fix', title: 'Ticking entity: what the crash means, and the fix', pip: 'pip-hurt' },
  'error-world-corrupted': { eyebrow: 'Error fix', title: 'Minecraft world corrupted: what the server says, and the fix', pip: 'pip-hurt' },
  docs: { eyebrow: 'Docs', title: 'Playkeeper docs', pip: 'pip-letter' },
  pricing: { eyebrow: 'Pricing', title: 'Free and open source. You only pay for your VPS.', pip: 'pip-box' },
  blog: { eyebrow: 'Blog', title: 'Releases, guides and building Playkeeper in public', pip: 'pip-letter' },
  t: { eyebrow: 'Server template', title: 'A Minecraft server setup, shared from Playkeeper', pip: 'pip-search' },
  demo: { eyebrow: 'Live demo', title: 'Try the dashboard in your browser', pip: 'pip-wave', file: 'web/src/demo/social.png' },
  tools: { eyebrow: 'Free tools', title: 'Free Minecraft server tools', pip: 'pip-box' },
  'server-icon': { eyebrow: 'Free tool', title: 'Minecraft server icon maker', pip: 'pip-cheer' },
  'color-codes': { eyebrow: 'Free tool', title: 'Minecraft color codes, with a live preview', pip: 'pip-wave' },
  motd: { eyebrow: 'Free tool', title: 'Minecraft MOTD generator', pip: 'pip-letter' },
  'jvm-flags': { eyebrow: 'Free tool', title: "Minecraft JVM arguments, with Aikar's flags", pip: 'pip-hardhat' },
  'server-properties': { eyebrow: 'Free tool', title: 'Every server.properties setting, explained', pip: 'pip-search' },
  // The frame the site's build draws each category's and template's preview
  // in (internal/site/previews.go): everything but the words and Pip.
  frame: { frame: true, file: 'site/og/frame.png' },
}
const only = process.argv.slice(2)
const wanted = (name) => only.length === 0 || only.includes(name)

const page = (p) => `<!doctype html><html><head><style>
  body { margin: 0; width: 1200px; height: 630px; background: #f2f2ec; font-family: Inter, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; color: #1d211c; overflow: hidden; position: relative; }
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
  ${p.frame ? '' : `${p.eyebrow ? `<div class="eyebrow">${p.eyebrow}</div>` : ''}
  <h1${p.eyebrow ? '' : ' style="top:200px"'}>${p.title}</h1>
  <img class="pip" src="${art(`web/src/assets/pip/${p.pip}.svg`)}">`}
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
  if (!wanted(name)) continue
  await tab.setContent(page(p), { waitUntil: 'load' })
  await tab.screenshot({ path: p.file ? path.join(repo, p.file) : path.join(out, `${name}.png`) })
  console.log(name)
}
if (wanted('playkeeper-0-4-0')) {
  await tab.setContent(cover, { waitUntil: 'load' })
  await tab.screenshot({ path: path.join(out, 'playkeeper-0-4-0.png') })
  console.log('playkeeper-0-4-0')
}
// The modpack pages' previews come from their packs (site/data/modpacks), so
// a new pack's page gets one without an entry above.
const packs = path.join(repo, 'site/data/modpacks')
const packPreviews = { modpacks: { eyebrow: 'Guide', title: 'Modpack server requirements', pip: 'pip-box' } }
for (const file of fs.readdirSync(packs).filter((f) => f.endsWith('.json')).sort()) {
  const pack = JSON.parse(fs.readFileSync(path.join(packs, file), 'utf8'))
  const label = pack.short || pack.name
  packPreviews[`modpack-${path.basename(file, '.json')}`] = { eyebrow: 'Modpack server', title: `How to make ${/^[aeiou]/i.test(label) ? 'an' : 'a'} ${label} server`, pip: 'pip-hardhat' }
}
for (const [name, p] of Object.entries(packPreviews)) {
  if (!wanted(name)) continue
  await tab.setContent(page(p), { waitUntil: 'load' })
  await tab.screenshot({ path: path.join(out, `${name}.png`) })
  console.log(name)
}
// The template library's previews carry their pages' headings, one per page
// in site/data/library.
const library = path.join(repo, 'site/data/library')
const libraryPreviews = { templates: { eyebrow: 'Server templates', title: 'Minecraft server templates, one click each', pip: 'pip-cheer' } }
for (const file of fs.readdirSync(library).filter((f) => f.endsWith('.json')).sort()) {
  const id = path.basename(file, '.json')
  const src = fs.readFileSync(path.join(repo, 'site/pages/templates', `${id}.html`), 'utf8')
  libraryPreviews[`template-${id}`] = { eyebrow: 'Server template', title: src.match(/^h1:\s*(.+)$/m)[1], pip: 'pip-box' }
}
for (const [name, p] of Object.entries(libraryPreviews)) {
  if (!wanted(name)) continue
  await tab.setContent(page(p), { waitUntil: 'load' })
  await tab.screenshot({ path: path.join(out, `${name}.png`) })
  console.log(name)
}
await browser.close()
