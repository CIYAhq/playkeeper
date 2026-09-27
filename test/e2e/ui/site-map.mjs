// A sample world for the site's screenshots of the live map: squaremap's
// tiles as the map draws them (one pixel per block at the top zoom level,
// half as many at each level below), from a made-up landscape of flat block
// colours around a spawn village. Nothing here comes from Minecraft's art.
import zlib from 'node:zlib'

/** Squaremap's settings as Playkeeper writes them (internal/webmap). */
export const mapZoom = { max: 3, default: 3, extra: 2 }
export const tileSize = 512

const crcTable = Array.from({ length: 256 }, (_, n) => {
  let c = n
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1
  return c >>> 0
})

function crc32(buf) {
  let c = 0xffffffff
  for (const b of buf) c = crcTable[(c ^ b) & 0xff] ^ (c >>> 8)
  return (c ^ 0xffffffff) >>> 0
}

function chunk(type, data) {
  const len = Buffer.alloc(4)
  len.writeUInt32BE(data.length)
  const body = Buffer.concat([Buffer.from(type, 'ascii'), data])
  const crc = Buffer.alloc(4)
  crc.writeUInt32BE(crc32(body))
  return Buffer.concat([len, body, crc])
}

/** An RGB image as a PNG. */
function png(width, height, rgb) {
  const raw = Buffer.alloc((width * 3 + 1) * height)
  for (let y = 0; y < height; y++) rgb.copy(raw, y * (width * 3 + 1) + 1, y * width * 3, (y + 1) * width * 3)
  const head = Buffer.alloc(13)
  head.writeUInt32BE(width, 0)
  head.writeUInt32BE(height, 4)
  head[8] = 8
  head[9] = 2
  return Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), chunk('IHDR', head), chunk('IDAT', zlib.deflateSync(raw, { level: 9 })), chunk('IEND', Buffer.alloc(0))])
}

function hash(x, z, seed) {
  let h = (Math.imul(x | 0, 374761393) + Math.imul(z | 0, 668265263) + Math.imul(seed, 144269504)) | 0
  h = Math.imul(h ^ (h >>> 13), 1274126177)
  return ((h ^ (h >>> 16)) >>> 0) / 4294967296
}

function smooth(x, z, cell, seed) {
  const gx = Math.floor(x / cell)
  const gz = Math.floor(z / cell)
  const fx = x / cell - gx
  const fz = z / cell - gz
  const u = fx * fx * (3 - 2 * fx)
  const v = fz * fz * (3 - 2 * fz)
  const a = hash(gx, gz, seed)
  const b = hash(gx + 1, gz, seed)
  const c = hash(gx, gz + 1, seed)
  const d = hash(gx + 1, gz + 1, seed)
  return a + (b - a) * u + (c - a) * v + (a - b - c + d) * u * v
}

function noise(x, z, seed) {
  return smooth(x, z, 96, seed) * 0.55 + smooth(x, z, 40, seed + 1) * 0.3 + smooth(x, z, 14, seed + 2) * 0.15
}

const hex = (s) => [parseInt(s.slice(1, 3), 16), parseInt(s.slice(3, 5), 16), parseInt(s.slice(5, 7), 16)]
const colours = {
  deep: hex('#3a6fd8'),
  water: hex('#4a80e6'),
  shallow: hex('#5b8fec'),
  sand: hex('#dccb93'),
  sand2: hex('#e4d5a2'),
  grass: [hex('#7eaa47'), hex('#77a441'), hex('#85b04d'), hex('#7aa744')],
  meadow: hex('#8fb957'),
  leaves: hex('#3f7a2b'),
  leaves2: hex('#4b8a33'),
  trunk: hex('#5f4a2e'),
  path: hex('#a0825a'),
  path2: hex('#94764f'),
  farmland: hex('#6e4f2f'),
  crop: hex('#a7c040'),
  crop2: hex('#c9b84a'),
  plank: hex('#b08a55'),
  roof: hex('#7a5433'),
  roof2: hex('#6a482b'),
  stone: hex('#8d8d88'),
  stone2: hex('#7d7d78'),
  flower: [hex('#e7cf4a'), hex('#d8473c'), hex('#f0f0ea')],
}

/** Houses of the spawn village: x, z, width, depth, roof along x. */
const houses = [
  [-18, -24, 9, 7, true], [5, -26, 7, 9, false], [-26, 5, 7, 7, true], [12, 6, 9, 7, true], [-8, 17, 7, 7, false], [-13, -62, 9, 7, true], [5, -56, 7, 7, false],
]

function village(x, z) {
  for (const [hx, hz, w, d, alongX] of houses) {
    if (x >= hx && x < hx + w && z >= hz && z < hz + d) {
      const edge = x === hx || x === hx + w - 1 || z === hz || z === hz + d - 1
      if (edge) return colours.plank
      const ridge = alongX ? z === hz + (d >> 1) : x === hx + (w >> 1)
      return ridge ? colours.roof2 : colours.roof
    }
  }
  // The well at spawn.
  if (Math.abs(x) <= 1 && Math.abs(z) <= 1) return x === 0 && z === 0 ? colours.water : colours.stone
  // Paths: a cross through spawn, down to the beach, and one to a door.
  const onPath = (Math.abs(z) <= 1 && x > -70 && x < 60) || (Math.abs(x) <= 1 && z > -90 && z < 70) || (z === 10 && x > 1 && x < 13) || (z === -59 && x > -5 && x < 5)
  if (onPath) return hash(x, z, 9) > 0.75 ? colours.path2 : colours.path
  // A field south east of spawn, in rows.
  if (x >= 4 && x < 22 && z >= 20 && z < 32) return z % 3 === 0 ? colours.farmland : hash(x, z, 5) > 0.3 ? colours.crop : colours.crop2
  // A stone square around the well.
  if (Math.abs(x) <= 3 && Math.abs(z) <= 3) return hash(x, z, 4) > 0.5 ? colours.stone : colours.stone2
  return undefined
}

/** The colour of the block at x, z seen from above. */
export function blockColour(x, z) {
  // The coast runs north to south just east of spawn, bending with the noise.
  const coast = 34 + (noise(0, z, 11) - 0.5) * 18
  const lake = noise(x, z, 21)
  const depth = x - coast + (lake - 0.5) * 10
  if (depth > 14) return colours.deep
  if (depth > 5) return colours.water
  if (depth > 0) return colours.shallow
  if (depth > -4 || (lake < 0.2 && depth > -40)) return hash(x, z, 3) > 0.5 ? colours.sand : colours.sand2
  if (lake < 0.16) return colours.water
  const v = village(x, z)
  if (v) return v
  const crown = tree(x, z)
  if (crown) return crown
  if (hash(x, z, 7) > 0.997) return colours.flower[Math.floor(hash(x, z, 8) * 3)]
  if (noise(x, z, 41) > 0.68) return colours.meadow
  return colours.grass[Math.floor(hash(x, z, 6) * 4)]
}

/** How thick the woods are: most to the west, a few trees around the village. */
function woods(x, z) {
  return noise(x, z, 31) + Math.max(0, -x - 40) / 260 - Math.max(0, 70 - Math.hypot(x, z)) / 140
}

/** The leaves over x, z, if a tree stands there: one tree at most in each 6-block cell, lit from the north west. */
function tree(x, z) {
  const cell = 6
  const gx = Math.floor(x / cell)
  const gz = Math.floor(z / cell)
  for (let dz = -1; dz <= 1; dz++) {
    for (let dx = -1; dx <= 1; dx++) {
      const cx = gx + dx
      const cz = gz + dz
      const tx = cx * cell + 1 + Math.floor(hash(cx, cz, 51) * (cell - 2))
      const tz = cz * cell + 1 + Math.floor(hash(cx, cz, 52) * (cell - 2))
      const density = woods(tx, tz)
      if (hash(cx, cz, 53) > (density > 0.5 ? 0.85 : 0.04)) continue
      if (village(tx, tz) || Math.abs(tz) <= 3 || Math.abs(tx) <= 3) continue
      const r = 2 + hash(cx, cz, 54) * 1.4
      const d = Math.hypot(x - tx, z - tz)
      if (d <= r) return x - tx + (z - tz) < -1 ? colours.leaves2 : colours.leaves
    }
  }
  return undefined
}

/** A tile of `world` at tile zoom `tz` (0 to mapZoom.max), as squaremap serves it, or undefined past the drawn area. */
export function mapTile(world, tz, tx, tzz) {
  if (world !== 'world' || tz < 0 || tz > mapZoom.max) return undefined
  const perPixel = 2 ** (mapZoom.max - tz)
  const x0 = tx * tileSize * perPixel
  const z0 = tzz * tileSize * perPixel
  // The explored land: about 900 blocks around spawn.
  if (x0 > 900 || z0 > 900 || x0 + tileSize * perPixel < -900 || z0 + tileSize * perPixel < -900) return undefined
  const rgb = Buffer.alloc(tileSize * tileSize * 3)
  for (let j = 0; j < tileSize; j++) {
    for (let i = 0; i < tileSize; i++) {
      const c = blockColour(x0 + i * perPixel, z0 + j * perPixel)
      rgb.set(c, (j * tileSize + i) * 3)
    }
  }
  return png(tileSize, tileSize, rgb)
}
