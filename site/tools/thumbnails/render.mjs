// Renders the world snapshots capture.js saved into the site's template
// thumbnails: for each <id>.json.gz in the shots folder, the 16:10 capture
// site/tools/shots.py takes, <out>/templates/<id>.png (files.mjs), and with
// --extra a 16:9 and a square picture of the same view.
//
// The camera is chosen from the world itself. For spawn it stands a little
// behind the world's spawn, above the ground, and looks the way whose
// picture shows the most: trees, water, sand, stone, relief, anything built,
// a band of sky, and no hill up close or land lost in the fog. For a
// showpiece (a skyblock island, the OneBlock block) it frames the blocks
// around where the bot stood, with the bot on them.
//
//   node render.mjs <shots-dir> [--out CAPTURES-DIR] [--extra DIR] [--only a,b] [--view heading,height]
import { createServer } from 'node:http'
import { mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync, existsSync } from 'node:fs'
import { createRequire } from 'node:module'
import { gunzipSync } from 'node:zlib'
import { dirname, extname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { SITE, save, shapes } from './files.mjs'

const require = createRequire(import.meta.url)
const { chromium } = require('playwright-core')
const { Vec3 } = require('vec3')
const { VERSION } = require('./version')

const here = dirname(fileURLToPath(import.meta.url))
const argv = process.argv.slice(2)
const opt = (name, def) => { const i = argv.indexOf(`--${name}`); return i >= 0 ? argv[i + 1] : def }
const shots = argv[0] && !argv[0].startsWith('--') ? resolve(argv[0]) : null
if (!shots) {
  console.error('usage: node render.mjs <shots-dir> [--out CAPTURES-DIR] [--extra DIR] [--only a,b]')
  process.exit(2)
}
const out = { captures: resolve(opt('out', join(shots, 'captures'))), extra: opt('extra') ? resolve(opt('extra')) : null }
const only = (opt('only') || '').split(',').filter(Boolean)
// --view <heading>,<height> takes that view of spawn instead of the best
// scoring one: a heading in degrees (0, 15 … 345) and a height (0, 1 or 2).
const [pinHeading, pinTier] = (opt('view') || '').split(',').map(Number)
const dist = join(here, 'dist')
if (!existsSync(join(dist, 'thumb.js'))) {
  console.error('dist/ is missing: run npm ci && npm run build in site/tools/thumbnails first')
  process.exit(2)
}

const Chunk = require('prismarine-chunk')(VERSION)
const registry = require('prismarine-registry')(VERSION)

// kind sorts a top block into what a view is made of.
function kind (name) {
  if (/water|kelp|seagrass|bubble_column/.test(name)) return 'water'
  if (/lava|magma/.test(name)) return 'lava'
  if (/leaves|_log$|_wood$|mangrove_roots|bamboo|vine/.test(name)) return 'tree'
  if (/sand|terracotta|cactus|dead_bush/.test(name)) return 'sand'
  if (/snow|ice/.test(name)) return 'snow'
  if (/tulip|poppy|dandelion|orchid|allium|bluet|daisy|cornflower|lily|rose|peony|lilac|sunflower|petals|torchflower|pitcher|wildflowers|eyeblossom/.test(name)) return 'flower'
  if (/^(grass_block|dirt|coarse_dirt|podzol|mycelium|moss_block|moss_carpet|short_grass|tall_grass|fern|large_fern|rooted_dirt|mud|farmland|dirt_path|sweet_berry_bush|pumpkin|melon|sugar_cane|leaf_litter|bush|firefly_bush|short_dry_grass|tall_dry_grass)$/.test(name)) return 'grass'
  if (/stone|andesite|diorite|granite|deepslate|tuff|calcite|gravel|cobble|clay|dripstone|obsidian|basalt|ore$/.test(name) && !/brick|polished|smooth|chiseled|cut_|slab|stairs|wall/.test(name)) return 'rock'
  return 'built'
}
const passable = new Set(['air', 'cave_air', 'void_air', 'light', 'barrier', 'structure_void'])
const soft = /leaves|short_grass|tall_grass|fern|flower|tulip|poppy|dandelion|orchid|allium|bluet|daisy|cornflower|lily|rose|peony|lilac|sunflower|petals|bush|vine|snow$|carpet|sapling|mushroom|torch|kelp|seagrass|litter/

// world indexes a snapshot's columns: the top block of each x, z, and the
// blocks near the focus for a showpiece.
function world (snap) {
  const cols = new Map()
  for (const c of snap.columns) cols.set(`${c.x},${c.z}`, Chunk.fromJson(c.chunk))
  const tops = new Map()
  const minY = -64
  const maxY = 319
  const top = (x, z) => {
    x = Math.floor(x); z = Math.floor(z)
    const key = `${x},${z}`
    if (tops.has(key)) return tops.get(key)
    const ch = cols.get(`${Math.floor(x / 16) * 16},${Math.floor(z / 16) * 16}`)
    let t = null
    if (ch) {
      const p = new Vec3(((x % 16) + 16) % 16, 0, ((z % 16) + 16) % 16)
      let ground = null
      for (let y = maxY; y >= minY; y--) {
        p.y = y
        const id = ch.getBlockStateId(p)
        if (!id) continue
        const b = registry.blocksByStateId[id]
        if (!b || passable.has(b.name)) continue
        if (!t) t = { y, name: b.name }
        if (!soft.test(b.name) && !/_log$|_wood$/.test(b.name)) { ground = y; break }
      }
      if (t) t.ground = ground ?? t.y
    }
    tops.set(key, t)
    return t
  }
  const blocks = (center, reach) => {
    const out = []
    for (const [key, ch] of cols) {
      const [cx, cz] = key.split(',').map(Number)
      if (cx + 16 < center.x - reach || cx > center.x + reach || cz + 16 < center.z - reach || cz > center.z + reach) continue
      const p = new Vec3(0, 0, 0)
      for (let x = 0; x < 16; x++) {
        for (let z = 0; z < 16; z++) {
          for (let y = Math.max(minY, Math.floor(center.y - reach)); y <= Math.min(maxY, center.y + reach); y++) {
            p.x = x; p.y = y; p.z = z
            const id = ch.getBlockStateId(p)
            if (!id) continue
            const b = registry.blocksByStateId[id]
            if (!b || passable.has(b.name)) continue
            out.push({ x: cx + x, y, z: cz + z, name: b.name })
          }
        }
      }
    }
    return out
  }
  return { top, blocks }
}

const rad = (d) => d * Math.PI / 180

// view casts rays through a picture from a camera over the snapshot's land,
// 16 across and 9 down, and says what they meet: the kind of each block hit,
// how far, and the rays that meet nothing (sky) or only the fog.
function view (w, cam, yaw, pitch, vfov, aspect, far) {
  const tv = Math.tan(rad(vfov) / 2)
  const th = tv * aspect
  const f = [Math.cos(yaw) * Math.cos(pitch), Math.sin(pitch), Math.sin(yaw) * Math.cos(pitch)]
  const r = [-Math.sin(yaw), 0, Math.cos(yaw)]
  const u = [r[1] * f[2] - r[2] * f[1], r[2] * f[0] - r[0] * f[2], r[0] * f[1] - r[1] * f[0]]
  const hits = []
  let sky = 0; let fogged = 0; let close = 0
  for (let i = 0; i < 16; i++) {
    for (let j = 0; j < 9; j++) {
      const a = (-1 + (2 * (i + 0.5)) / 16) * th
      const b = (1 - (2 * (j + 0.5)) / 9) * tv
      const d = [f[0] + r[0] * a + u[0] * b, f[1] + r[1] * a + u[1] * b, f[2] + r[2] * a + u[2] * b]
      const n = Math.hypot(...d)
      let hit = null
      for (let s = 1; s <= far; s += 1) {
        const x = cam.x + (d[0] / n) * s; const y = cam.y + (d[1] / n) * s; const z = cam.z + (d[2] / n) * s
        const t = w.top(x, z)
        if (t && y <= t.y + 1) { hit = { s, t }; break }
        if (!t && y < 40) break
      }
      if (!hit) { sky++; continue }
      if (hit.s > far * 0.7) fogged++
      if (hit.s < 14) close++
      hits.push(hit)
    }
  }
  return { hits, sky: sky / 144, fogged: fogged / 144, close: close / 144 }
}

// landscape picks the camera for spawn: 24 headings at three heights, each
// scored by what the site's 16:10 picture shows. The extra pictures are the
// same view, cropped to their shape.
const landVfov = { '16x10': 52, '16x9': 50, '1x1': 60 }
function landscape (snap, w, pin = null) {
  const f = snap.focus
  const reach = snap.radius * 16
  const far = reach * 0.92
  const fAt = w.top(f.x, f.z)
  const floor = fAt ? fAt.y : f.y
  const vfov = landVfov[SITE.name]
  let best = null
  for (let i = 0; i < 72; i++) {
    const yaw = ((i % 24) / 24) * Math.PI * 2
    const dir = { x: Math.cos(yaw), z: Math.sin(yaw) }
    const back = 10
    const cx = f.x - dir.x * back
    const cz = f.z - dir.z * back
    let high = floor
    for (let dx = -3; dx <= 3; dx++) for (let dz = -3; dz <= 3; dz++) { const t = w.top(cx + dx, cz + dz); if (t) high = Math.max(high, t.y) }
    const tier = Math.floor(i / 24)
    const cy = [Math.max(high + 10, floor + 14), Math.max(high + 18, floor + 24), Math.max(high + 30, floor + 40)][tier]
    // Aim at the ground 20 to 60 blocks out.
    let sum = 0; let n = 0
    for (let d = 20; d <= 60; d += 4) { const t = w.top(cx + dir.x * d, cz + dir.z * d); if (t) { sum += t.y; n++ } }
    const aimY = n ? sum / n : floor
    const pitch = Math.max(rad(-28), Math.min(rad(-7), Math.atan2(aimY - cy, 40)))
    const v = view(w, { x: cx, y: cy, z: cz }, yaw, pitch, vfov, SITE.w / SITE.h, far)
    if (!v.hits.length) continue
    // What the picture shows, nearer counting more.
    const kinds = {}
    let seen = 0
    const heights = []
    for (const h of v.hits) {
      const wt = 1 / (1 + h.s / 45)
      kinds[kind(h.t.name)] = (kinds[kind(h.t.name)] || 0) + wt
      seen += wt
      heights.push(h.t.ground)
    }
    let entropy = 0
    for (const x of Object.values(kinds)) { const p = x / seen; entropy -= p * Math.log2(p) }
    const mean = heights.reduce((s, h) => s + h, 0) / heights.length
    const relief = Math.min(18, Math.sqrt(heights.reduce((s, h) => s + (h - mean) ** 2, 0) / heights.length))
    const frac = (k) => (kinds[k] || 0) / seen
    let score = entropy + relief * 0.05
    if (frac('tree') > 0.06 && frac('tree') < 0.5) score += 0.35
    if (frac('water') > 0.04 && frac('water') < 0.35) score += 0.3
    if (frac('water') > 0.45) score -= 0.8 + (frac('water') - 0.45) * 4
    if (frac('flower') > 0.01) score += 0.1
    if (frac('built') > 0.004) score += 0.5
    if (frac('lava') > 0.002) score += 0.15
    // A band of sky above the land; not a wall of land up close, nor land
    // lost in the fog.
    if (v.sky < 0.1) score -= (0.1 - v.sky) * 8
    if (v.sky > 0.42) score -= (v.sky - 0.42) * 8
    score -= v.close * 5 + v.fogged * 3
    // The lowest camera, unless a higher one sees clearly more.
    score -= tier * 0.2
    if (pin && ((i % 24) * 15 !== pin.heading || tier !== pin.tier)) continue
    if (!best || score > best.score) {
      const td = 40
      best = { score, yaw: (i % 24) * 15, cam: { x: cx, y: cy, z: cz, tx: cx + dir.x * td, ty: cy + Math.tan(pitch) * td, tz: cz + dir.z * td, fov: vfov } }
    }
  }
  if (!best) throw new Error('no heading sees any of the snapshot')
  const look = { top: '#4a8fe3', horizon: '#d6e7f2', bottom: '#d6e7f2', sun: [0.35, 0.75, 0.5], fog: [far * 0.5, far], ambient: 0.8, directional: 0.5 }
  return { ...best, look }
}

// fit is the distance from which a camera looking along -dir at c, with this
// vertical field of view and aspect, has every point inside the middle fill
// of its picture.
function fit (pts, c, dir, vfov, aspect, fill) {
  const f = [-dir[0], -dir[1], -dir[2]]
  const rl = Math.hypot(f[2], f[0])
  const r = [-f[2] / rl, 0, f[0] / rl]
  const u = [r[1] * f[2] - r[2] * f[1], r[2] * f[0] - r[0] * f[2], r[0] * f[1] - r[1] * f[0]]
  const tv = Math.tan(rad(vfov) / 2)
  const th = tv * aspect
  const inside = (d) => pts.every((p) => {
    const v = [p[0] - (c[0] + dir[0] * d), p[1] - (c[1] + dir[1] * d), p[2] - (c[2] + dir[2] * d)]
    const z = v[0] * f[0] + v[1] * f[1] + v[2] * f[2]
    if (z <= 0.3) return false
    return Math.abs(v[0] * r[0] + v[2] * r[2]) / (z * th) <= fill && Math.abs(v[0] * u[0] + v[1] * u[1] + v[2] * u[2]) / (z * tv) <= fill
  })
  let lo = 1
  let hi = 600
  for (let i = 0; i < 40; i++) { const mid = (lo + hi) / 2; if (inside(mid)) hi = mid; else lo = mid }
  return hi
}

// subject frames the blocks around where the bot stood: an island in the
// void, or the one block.
function subject (snap, w, aspect) {
  const f = snap.player || snap.focus
  const blocks = w.blocks(f, 40)
  if (!blocks.length) throw new Error('no blocks around the showpiece')
  // Framed on the ground and what's on it: a tree may run off the picture,
  // so the island stays big.
  const pts = []
  const head = f.y + 3
  // Islands further off stay in the picture, but only the bot's is framed.
  const island = blocks.filter((b) => Math.hypot(b.x + 0.5 - f.x, b.z + 0.5 - f.z) <= 14)
  const framed = island.filter((b) => b.y < head && !/leaves|_log$|_wood$/.test(b.name))
  for (const b of framed.length ? framed : blocks) for (const [dx, dy, dz] of [[0, 0, 0], [1, 0, 0], [0, 1, 0], [0, 0, 1], [1, 1, 0], [1, 0, 1], [0, 1, 1], [1, 1, 1]]) pts.push([b.x + dx, b.y + dy, b.z + dz])
  if (snap.player) for (const [dx, dy, dz] of [[-0.3, 0, -0.3], [0.3, 1.85, 0.3], [-0.3, 1.85, 0.3], [0.3, 0, -0.3]]) pts.push([snap.player.x + dx, snap.player.y + dy, snap.player.z + dz])
  const lo = [0, 1, 2].map((i) => Math.min(...pts.map((p) => p[i])))
  const hi = [0, 1, 2].map((i) => Math.max(...pts.map((p) => p[i])))
  const c = lo.map((v, i) => (v + hi[i]) / 2)
  const vfov = aspect > 1.2 ? 40 : 46
  const pitch = rad(24)
  // The diagonal on the player's side, then the one that faces the most
  // that isn't plain ground.
  let best = null
  for (const deg of [45, 135, 225, 315]) {
    const a = rad(deg)
    const dx = Math.cos(a); const dz = Math.sin(a)
    let n = 0
    for (const b of blocks) {
      if (/^(dirt|grass_block|stone|sand|sandstone|cobblestone|bedrock)$/.test(b.name)) continue
      if ((b.x + 0.5 - c[0]) * dx + (b.z + 0.5 - c[2]) * dz > 0) n++
    }
    if (snap.player) {
      const px = snap.player.x - c[0]; const pz = snap.player.z - c[2]
      n += (1000 * (px * dx + pz * dz)) / Math.max(0.5, Math.hypot(px, pz))
    }
    if (!best || n > best.n) best = { n, deg, dx, dz }
  }
  const dir = [best.dx * Math.cos(pitch), Math.sin(pitch), best.dz * Math.cos(pitch)]
  // Something tall above the ground, like a tree, gets the upper part of
  // the picture: the ground is framed smaller and lower.
  const tall = island.some((b) => b.y >= head + 2)
  const dist = fit(pts, c, dir, vfov, aspect, blocks.length < 20 ? 0.6 : tall ? 0.52 : 0.8)
  const lift = tall ? dist * Math.tan(rad(vfov) / 2) * 0.32 : 0
  const cam = { x: c[0] + dir[0] * dist, y: c[1] + dir[1] * dist + lift, z: c[2] + dir[2] * dist, tx: c[0], ty: c[1] + lift, tz: c[2], fov: vfov }
  // The player model faces -z; turned a little from the camera, three
  // quarters on.
  if (snap.player) cam.playerYaw = Math.atan2(cam.x - snap.player.x, cam.z - snap.player.z) + Math.PI + 0.5
  const look = { top: '#3b85e6', horizon: '#c4e0f6', bottom: '#b3d4f1', sun: [0.4, 0.8, 0.45], fog: [600, 1100], ambient: 0.84, directional: 0.52 }
  return { yaw: best.deg, cam, look, blocks: blocks.length }
}

function serve (files) {
  const types = { '.html': 'text/html', '.js': 'text/javascript', '.json': 'application/json', '.png': 'image/png' }
  const server = createServer((req, res) => {
    const url = decodeURIComponent(new URL(req.url, 'http://x').pathname)
    const path = url === '/' ? '/index.html' : url
    const file = files[path] || (/^\/[\w./-]+$/.test(path) && !path.includes('..') ? join(dist, path) : null)
    if (!file || !existsSync(file)) { res.writeHead(404); res.end(); return }
    res.writeHead(200, { 'Content-Type': types[extname(file)] || 'application/octet-stream' })
    res.end(readFileSync(file))
  })
  return new Promise((resolve) => server.listen(0, '127.0.0.1', () => resolve(server)))
}

const files = readdirSync(shots).filter((f) => f.endsWith('.json.gz')).map((f) => f.slice(0, -8)).filter((id) => !only.length || only.includes(id)).sort()
if (!files.length) {
  console.error(`no snapshots in ${shots}`)
  process.exit(1)
}
const tmp = join(shots, '.render')
mkdirSync(tmp, { recursive: true })
const browser = await chromium.launch({
  channel: process.env.CHROME ? undefined : 'chrome',
  executablePath: process.env.CHROME || undefined,
  args: ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader', '--ignore-gpu-blocklist']
})
let failed = 0
for (const id of files) {
  const started = Date.now()
  try {
    const raw = gunzipSync(readFileSync(join(shots, `${id}.json.gz`)))
    const snap = JSON.parse(raw)
    if (snap.version !== VERSION) throw new Error(`the snapshot is Minecraft ${snap.version}, and the renderer draws ${VERSION}`)
    writeFileSync(join(tmp, `${id}.json`), raw)
    const w = world(snap)
    const server = await serve({ '/world.json': join(tmp, `${id}.json`) })
    const page = await browser.newPage({ viewport: { width: 1280, height: 720 } })
    page.on('pageerror', (e) => console.log(`[${id}] page:`, e.message.slice(0, 200)))
    await page.goto(`http://127.0.0.1:${server.address().port}/`)
    await page.waitForFunction(() => window.thumb && window.thumb.ready)
    const loaded = await page.evaluate(() => window.thumb.load('world.json'))
    const land = snap.subject ? null : landscape(snap, w, Number.isFinite(pinHeading) ? { heading: pinHeading, tier: pinTier || 0 } : null)
    for (const s of shapes(out)) {
      const view = land ? { ...land, cam: { ...land.cam, fov: landVfov[s.name] } } : subject(snap, w, s.w / s.h)
      const url = await page.evaluate((p) => window.thumb.shot(p), { w: s.w, h: s.h, cam: view.cam, look: view.look })
      save(out, id, s.name, Buffer.from(url.slice(url.indexOf(',') + 1), 'base64'))
      console.log(`[${id}] ${s.name} heading ${view.yaw}°${view.score !== undefined ? `, score ${view.score.toFixed(2)}` : `, ${view.blocks} blocks`}`)
    }
    await page.close()
    server.close()
    console.log(`[${id}] ${loaded.columns} columns, meshed in ${(loaded.ms / 1000).toFixed(1)} s, done in ${((Date.now() - started) / 1000).toFixed(0)} s`)
  } catch (e) {
    failed++
    console.error(`[${id}] ${e.message}`)
  }
}
await browser.close()
rmSync(tmp, { recursive: true, force: true })
process.exit(failed ? 1 : 0)
