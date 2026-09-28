// The thumbnail files the site uses: each picture at the widths its cards
// and pages show, as AVIF and WebP, in site/static/thumbs as
// <id>-<shape>-<width>w.<avif|webp> (internal/site/thumbs.go reads them).
import { readdirSync, rmSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const sharp = require('sharp')

export const THUMBS = join(dirname(fileURLToPath(import.meta.url)), '../../static/thumbs')

// FORMATS are the shapes and the widths of each file. Masters are made at
// twice the widest, so every file is a downscale.
export const FORMATS = { '16x9': { w: 16, h: 9, widths: [400, 800, 1200] }, '1x1': { w: 1, h: 1, widths: [400, 800] } }

export function masterSize (format) {
  const { w, h, widths } = FORMATS[format]
  const W = widths[widths.length - 1] * 2
  return [W, Math.round((W * h) / w)]
}

// encode writes a template's files of one shape from its master PNG,
// replacing the ones it had. Minecraft's textures are all detail, so the
// widest files, for large screens at twice their size, get a lower quality
// to stay under the 150 kB internal/site's tests allow.
export async function encode (out, id, format, png) {
  for (const old of readdirSync(out)) if (old.startsWith(`${id}-${format}-`)) rmSync(join(out, old))
  for (const w of FORMATS[format].widths) {
    const img = sharp(png).resize({ width: w, kernel: 'lanczos3' })
    const base = join(out, `${id}-${format}-${w}w`)
    const wide = w > 800
    await img.clone().avif({ quality: wide ? 44 : 52, effort: 6, chromaSubsampling: '4:2:0' }).toFile(base + '.avif')
    await img.clone().webp({ quality: wide ? 62 : 74, effort: 6, smartSubsample: true }).toFile(base + '.webp')
  }
}
