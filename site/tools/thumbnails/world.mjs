// Turns a world a template's server saved into a snapshot render.mjs draws,
// for a modded server the capture bot can't join: the chunks around its
// spawn, read from its region files (level.dat says where spawn is). The
// world's own Minecraft blocks are kept, in the version the renderer draws;
// a mod's blocks, which Minecraft's textures don't have, are left out, and
// the snapshot says how many were.
//
//   node world.mjs <world-dir> --template ID --out FILE.json.gz [--radius 10]
import { existsSync, mkdirSync, openSync, readSync, closeSync, readFileSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join, resolve } from 'node:path'
import { gunzipSync, gzipSync, inflateSync } from 'node:zlib'

const require = createRequire(import.meta.url)
const nbt = require('prismarine-nbt')
const { Vec3 } = require('vec3')
const { VERSION } = require('./version')
const registry = require('prismarine-registry')(VERSION)
const Block = require('prismarine-block')(registry)
const Chunk = require('prismarine-chunk')(VERSION)

const argv = process.argv.slice(2)
const opt = (name, def) => { const i = argv.indexOf(`--${name}`); return i >= 0 ? argv[i + 1] : def }
const dir = argv[0] && !argv[0].startsWith('--') ? resolve(argv[0]) : null
const id = opt('template')
const outFile = opt('out')
const radius = Number(opt('radius', 10))
if (!dir || !id || !outFile) {
  console.error('usage: node world.mjs <world-dir> --template ID --out FILE.json.gz [--radius 10]')
  process.exit(2)
}

// Blocks Minecraft renamed since the versions modpacks run.
const renamed = { grass: 'short_grass' }
// Chunks that have their trees and plants: earlier stages are bare rock.
const done = new Set(['features', 'initialize_light', 'light', 'spawn', 'full'])

const level = nbt.simplify((await nbt.parse(readFileSync(join(dir, 'level.dat')))).parsed).Data
const spawnPos = level.spawn?.pos ?? [level.SpawnX, level.SpawnY, level.SpawnZ]
const spawn = { x: spawnPos[0] + 0.5, y: spawnPos[1], z: spawnPos[2] + 0.5 }

// chunkAt reads one chunk's NBT from its region file, or null.
const regions = new Map()
function chunkAt (cx, cz) {
  const file = join(dir, 'region', `r.${cx >> 5}.${cz >> 5}.mca`)
  if (!regions.has(file)) regions.set(file, existsSync(file) ? openSync(file, 'r') : null)
  const fd = regions.get(file)
  if (fd === null) return null
  const head = Buffer.alloc(4)
  readSync(fd, head, 0, 4, 4 * ((cx & 31) + (cz & 31) * 32))
  const sector = head.readUIntBE(0, 3)
  if (!sector) return null
  const len = Buffer.alloc(5)
  readSync(fd, len, 0, 5, sector * 4096)
  const data = Buffer.alloc(len.readUInt32BE(0) - 1)
  readSync(fd, data, 0, data.length, sector * 4096 + 5)
  const raw = len[4] === 1 ? gunzipSync(data) : len[4] === 2 ? inflateSync(data) : len[4] === 3 ? data : null
  if (!raw) throw new Error(`r.${cx >> 5}.${cz >> 5}.mca compresses a chunk in a way this doesn't read (${len[4]})`)
  return nbt.simplify(nbt.parseUncompressed(raw, 'big'))
}

// stateOf is the renderer's state id for a palette entry, or 0 for air and
// for a mod's block.
const states = new Map()
let modded = 0
function stateOf (entry) {
  const key = JSON.stringify(entry)
  if (states.has(key)) return states.get(key)
  let s = 0
  const [ns, name] = entry.Name.includes(':') ? entry.Name.split(':') : ['minecraft', entry.Name]
  const known = ns === 'minecraft' ? (registry.blocksByName[name] ? name : renamed[name]) : null
  if (known) {
    try { s = Block.fromProperties(known, entry.Properties || {}, 0).stateId } catch { s = registry.blocksByName[known].defaultState }
  } else if (ns !== 'minecraft' || !/air$/.test(name)) {
    s = -1
  }
  states.set(key, s)
  return s
}

// biomeOf is the renderer's id for a biome, which tints grass, leaves and
// water: plains for a mod's.
const biomes = new Map()
function biomeOf (full) {
  if (!biomes.has(full)) {
    const [ns, name] = full.includes(':') ? full.split(':') : ['minecraft', full]
    biomes.set(full, (ns === 'minecraft' && registry.biomesByName[name]) ? registry.biomesByName[name].id : registry.biomesByName.plains.id)
  }
  return biomes.get(full)
}

// indexes unpacks a paletted container: n palette indexes, each stored in
// as few bits as the palette needs (at least min), never across two longs.
function indexes (states, n = 4096, min = 4) {
  const out = new Uint16Array(n)
  const palette = states.palette
  if (palette.length === 1 || !states.data) return out
  const bits = Math.max(min, Math.ceil(Math.log2(palette.length)))
  const per = Math.floor(64 / bits)
  const mask = (1 << bits) - 1
  for (let i = 0; i < n; i++) {
    const [hi, lo] = states.data[Math.floor(i / per)]
    const at = (i % per) * bits
    let v
    if (at + bits <= 32) v = (lo >>> at) & mask
    else if (at >= 32) v = (hi >>> (at - 32)) & mask
    else v = ((lo >>> at) | (hi << (32 - at))) & mask
    out[i] = v
  }
  return out
}

const scx = Math.floor(spawn.x / 16)
const scz = Math.floor(spawn.z / 16)
const columns = []
let bare = 0
for (let i = -radius; i <= radius; i++) {
  for (let j = -radius; j <= radius; j++) {
    const c = chunkAt(scx + i, scz + j)
    if (!c) continue
    if (!done.has(String(c.Status).replace(/^minecraft:/, ''))) { bare++; continue }
    const col = new Chunk({ minY: -64, worldHeight: 384 })
    const p = new Vec3(0, 0, 0)
    for (const sec of c.sections || []) {
      const y0 = sec.Y * 16
      if (y0 < -64 || y0 >= 320) continue
      // Biomes are kept for every 4 × 4 × 4 cell.
      if (sec.biomes && sec.biomes.palette) {
        const bids = sec.biomes.palette.map(biomeOf)
        const bidx = indexes(sec.biomes, 64, 1)
        for (let n = 0; n < 64; n++) {
          p.x = (n & 3) * 4; p.z = ((n >> 2) & 3) * 4; p.y = y0 + (n >> 4) * 4
          col.setBiome(p, bids[bidx[n]])
        }
      }
      const bs = sec.block_states
      if (!bs || !bs.palette) continue
      const ids = bs.palette.map(stateOf)
      if (ids.every((s) => s === 0)) continue
      const idx = indexes(bs)
      for (let n = 0; n < 4096; n++) {
        const s = ids[idx[n]]
        if (s > 0) {
          p.x = n & 15; p.z = (n >> 4) & 15; p.y = y0 + (n >> 8)
          col.setBlockStateId(p, s)
        } else if (s < 0) {
          modded++
        }
      }
    }
    columns.push({ x: (scx + i) * 16, z: (scz + j) * 16, chunk: col.toJson() })
  }
}
for (const fd of regions.values()) if (fd !== null) closeSync(fd)
if (!columns.length) throw new Error(`no finished chunk around spawn (${spawn.x}, ${spawn.z}) in ${dir}`)
const snapshot = { version: VERSION, template: id, subject: false, focus: spawn, player: null, radius, columns, modded }
mkdirSync(dirname(resolve(outFile)), { recursive: true })
writeFileSync(outFile, gzipSync(JSON.stringify(snapshot)))
console.log(`[${id}] ${columns.length} chunks around spawn (${Math.floor(spawn.x)}, ${spawn.y}, ${Math.floor(spawn.z)}), ${bare} unfinished left out, ${modded} modded blocks left out`)
