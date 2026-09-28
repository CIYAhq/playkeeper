// A modpack template's thumbnails carry its pack's official icon, from the
// pack's Modrinth page, and say whose it is in site/data/thumbs.json, which
// the template's page shows. The icon goes on the picture of the pack's
// world as a badge, or, for a pack whose server the capture bot can't join,
// makes the whole picture (pack-art.mjs).
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const sharp = require('sharp')

const here = dirname(fileURLToPath(import.meta.url))
const site = join(here, '../..')
const credits = join(site, 'data/thumbs.json')
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

// packIcon fetches a pack's icon at its full size, and who it's by.
export async function packIcon (pack) {
  if (pack.source !== 'modrinth') throw new Error(`${pack.name} is on ${pack.source}; only Modrinth packs' icons are fetched`)
  const p = await get(`https://api.modrinth.com/v2/project/${encodeURIComponent(pack.project)}`)
  if (!p.icon_url) throw new Error(`${pack.name} has no icon on Modrinth`)
  let by = ''
  if (p.organization) {
    by = (await get(`https://api.modrinth.com/v3/organization/${encodeURIComponent(p.organization)}`)).name
  } else {
    const members = await get(`https://api.modrinth.com/v2/team/${encodeURIComponent(p.team)}/members`)
    const owner = members.find((m) => m.role === 'Owner') || members[0]
    by = owner ? owner.user.username : ''
  }
  if (!by) throw new Error(`${pack.name} names no owner on Modrinth to credit its icon to`)
  // Modrinth's icon_url is a 96-pixel copy; the upload sits beside it.
  let png = null
  for (const url of [p.icon_url.replace(/_96\.webp$/, '.png'), p.icon_url]) {
    try { png = await sharp(await get(url, false)).png().toBuffer(); break } catch {}
  }
  if (!png) throw new Error(`${pack.name}'s icon doesn't download`)
  return { png, credit: { pack: p.title, by, url: `https://modrinth.com/modpack/${p.slug}` } }
}

function rounded (size, radius, fill) {
  return Buffer.from(`<svg xmlns="http://www.w3.org/2000/svg" width="${size}" height="${size}"><rect width="${size}" height="${size}" rx="${radius}" ry="${radius}" fill="${fill}"/></svg>`)
}

async function roundIcon (png, size) {
  const r = Math.round(size * 0.18)
  return sharp(png).resize(size, size, { fit: 'cover' }).composite([{ input: rounded(size, r, '#fff'), blend: 'dest-in' }]).png().toBuffer()
}

// badge puts the icon on a picture's lower left corner, on a white card
// with a soft shadow.
export async function badge (picture, icon) {
  const { width: W, height: H } = await sharp(picture).metadata()
  const size = Math.round(Math.min(W, H) * 0.2)
  const pad = Math.round(size * 0.07)
  const card = size + pad * 2
  const x = Math.round(Math.min(W, H) * 0.05)
  const y = H - x - card
  const shadow = await sharp(rounded(card, Math.round(card * 0.2), 'rgba(0,0,0,0.45)')).blur(Math.max(1, card * 0.06)).png().toBuffer()
  return sharp(picture).composite([
    { input: shadow, left: x, top: y + Math.round(card * 0.04) },
    { input: rounded(card, Math.round(card * 0.2), '#ffffff'), left: x, top: y },
    { input: await roundIcon(icon, size), left: x + pad, top: y + pad }
  ]).png().toBuffer()
}

// art is a picture made of the icon alone: the icon in the middle, on the
// same icon blurred to fill the frame.
export async function art (icon, W, H) {
  const back = await sharp(icon).resize(W, H, { fit: 'cover' }).blur(Math.round(Math.min(W, H) * 0.06)).modulate({ brightness: 0.72, saturation: 1.15 }).png().toBuffer()
  const size = Math.round(Math.min(W, H) * 0.56)
  const shadow = await sharp(rounded(size, Math.round(size * 0.18), 'rgba(0,0,0,0.5)')).blur(Math.round(size * 0.05)).png().toBuffer()
  const left = Math.round((W - size) / 2)
  const top = Math.round((H - size) / 2)
  return sharp(back).composite([
    { input: shadow, left, top: top + Math.round(size * 0.03) },
    { input: await roundIcon(icon, size), left, top }
  ]).png().toBuffer()
}

// credit records whose icon a template's thumbnails carry, and whether the
// icon is all they show, or that they carry none.
export function credit (id, c, art = false) {
  const all = existsSync(credits) ? JSON.parse(readFileSync(credits, 'utf8')) : {}
  if (c) all[id] = { icon: `${c.pack}'s icon, by ${c.by}, from Modrinth`, url: c.url, ...(art ? { art: true } : {}) }
  else delete all[id]
  const sorted = Object.fromEntries(Object.keys(all).sort().map((k) => [k, all[k]]))
  writeFileSync(credits, JSON.stringify(sorted, null, 2) + '\n')
}
