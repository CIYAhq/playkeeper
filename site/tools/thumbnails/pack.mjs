// A modpack template whose server the capture bot can't join gets a picture
// made of its pack's official icon, from the pack's Modrinth page (the
// template's page shows the pack with the same icon and links it).
import { existsSync, readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const sharp = require('sharp')

const here = dirname(fileURLToPath(import.meta.url))
const site = join(here, '../..')
const agent = { 'User-Agent': 'playkeeper-template-thumbnails (github.com/CIYAhq/playkeeper)' }

// packOf is the modpack a site template installs, or null.
export function packOf (id) {
  const file = join(site, 'data/templates', `${id}.json`)
  if (!existsSync(file)) return null
  return JSON.parse(readFileSync(file, 'utf8')).modpack || null
}

async function get (url, json = true) {
  const res = await fetch(url, { headers: agent })
  if (!res.ok) throw new Error(`${url}: ${res.status}`)
  return json ? res.json() : Buffer.from(await res.arrayBuffer())
}

// packIcon fetches a pack's icon at its full size.
export async function packIcon (pack) {
  if (pack.source !== 'modrinth') throw new Error(`${pack.name} is on ${pack.source}; only Modrinth packs' icons are fetched`)
  const p = await get(`https://api.modrinth.com/v2/project/${encodeURIComponent(pack.project)}`)
  if (!p.icon_url) throw new Error(`${pack.name} has no icon on Modrinth`)
  // Modrinth's icon_url is a 96-pixel copy; the upload sits beside it.
  for (const url of [p.icon_url.replace(/_96\.webp$/, '.png'), p.icon_url]) {
    try { return await sharp(await get(url, false)).png().toBuffer() } catch {}
  }
  throw new Error(`${pack.name}'s icon doesn't download`)
}

function rounded (size, radius, fill) {
  return Buffer.from(`<svg xmlns="http://www.w3.org/2000/svg" width="${size}" height="${size}"><rect width="${size}" height="${size}" rx="${radius}" ry="${radius}" fill="${fill}"/></svg>`)
}

// art is a picture made of the icon alone: the icon in the middle, on the
// same icon blurred to fill the frame.
export async function art (icon, W, H) {
  const back = await sharp(icon).resize(W, H, { fit: 'cover' }).blur(Math.round(Math.min(W, H) * 0.06)).modulate({ brightness: 0.72, saturation: 1.15 }).png().toBuffer()
  const size = Math.round(Math.min(W, H) * 0.56)
  const shadow = await sharp(rounded(size, Math.round(size * 0.18), 'rgba(0,0,0,0.5)')).blur(Math.round(size * 0.05)).png().toBuffer()
  const face = await sharp(icon).resize(size, size, { fit: 'cover' }).composite([{ input: rounded(size, Math.round(size * 0.18), '#fff'), blend: 'dest-in' }]).png().toBuffer()
  const left = Math.round((W - size) / 2)
  const top = Math.round((H - size) / 2)
  return sharp(back).composite([
    { input: shadow, left, top: top + Math.round(size * 0.03) },
    { input: face, left, top }
  ]).png().toBuffer()
}
