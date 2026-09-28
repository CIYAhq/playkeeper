// Draws the share card's pictures (internal/sharecard/assets) from the
// dashboard's own SVGs: the Playkeeper mark, Pip waving and the pixel
// ground. Run from this directory after changing one of them:
//   node sharecard-assets.mjs
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { chromium } from '@playwright/test'

const web = fileURLToPath(new URL('../../../web/src/assets/', import.meta.url))
const out = fileURLToPath(new URL('../../../internal/sharecard/assets/', import.meta.url))
const pictures = [
  { svg: 'brand/playkeeper-mark.svg', png: 'mark.png', width: 56, height: 56 },
  { svg: 'pip/pip-wave.svg', png: 'pip.png', width: 216, height: 216 },
  { svg: 'pixel-art/ground.svg', png: 'ground.png', width: 480, height: 72, pixelated: true },
]

const browser = await chromium.launch()
const page = await browser.newPage({ deviceScaleFactor: 1 })
for (const p of pictures) {
  const svg = readFileSync(web + p.svg, 'utf8')
  const src = `data:image/svg+xml;base64,${Buffer.from(svg).toString('base64')}`
  await page.setViewportSize({ width: p.width, height: p.height })
  await page.setContent(`<style>html,body{margin:0;background:transparent}img{display:block;width:${p.width}px;height:${p.height}px${p.pixelated ? ';image-rendering:pixelated' : ''}}</style><img src="${src}">`)
  await page.locator('img').screenshot({ path: out + p.png, omitBackground: true })
}
await browser.close()
