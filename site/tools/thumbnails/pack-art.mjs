// Thumbnails for a modpack template whose server the capture bot can't
// join, such as a NeoForge pack or one whose mods every player must have:
// made of the pack's official icon from its Modrinth page, and credited in
// site/data/thumbs.json.
//
//   node pack-art.mjs better-mc [cobblemon ...] [--out DIR]
import { mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { FORMATS, THUMBS, encode, masterSize } from './files.mjs'
import { art, credit, packIcon, packOf } from './pack.mjs'

const argv = process.argv.slice(2)
const i = argv.indexOf('--out')
const out = i >= 0 ? resolve(argv.splice(i, 2)[1]) : THUMBS
if (!argv.length) {
  console.error('usage: node pack-art.mjs <template-id>... [--out DIR]')
  process.exit(2)
}
mkdirSync(out, { recursive: true })
let failed = 0
for (const id of argv) {
  try {
    const pack = packOf(id)
    if (!pack) throw new Error('it installs no modpack; site/tools/thumbnails/render.mjs draws its world')
    const icon = await packIcon(pack)
    for (const format of Object.keys(FORMATS)) {
      const [W, H] = masterSize(format)
      await encode(out, id, format, await art(icon.png, W, H))
    }
    if (out === THUMBS) credit(id, icon.credit, true)
    console.log(`[${id}] ${icon.credit.pack}'s icon, by ${icon.credit.by}`)
  } catch (e) {
    failed++
    console.error(`[${id}] ${e.message}`)
  }
}
process.exit(failed ? 1 : 0)
