// Pictures for a modpack template whose server the capture bot can't join,
// such as a NeoForge pack or one whose mods every player must have: made of
// the pack's official icon, from Modrinth, or from CurseForge through a
// running Playkeeper (--socket), then written for the site by
// site/tools/shots.py like the rendered ones.
//
//   node pack-art.mjs <captures-dir> <template-id>... [--socket /tmp/pk/agent.sock] [--extra DIR]
import { resolve } from 'node:path'
import { save, shapes } from './files.mjs'
import { art, packIcon, packOf } from './pack.mjs'

const argv = process.argv.slice(2)
const take = (name) => {
  const i = argv.indexOf(`--${name}`)
  return i >= 0 ? argv.splice(i, 2)[1] : null
}
const extra = take('extra')
const socket = take('socket')
const [dir, ...ids] = argv
if (!dir || !ids.length) {
  console.error('usage: node pack-art.mjs <captures-dir> <template-id>... [--socket /tmp/pk/agent.sock] [--extra DIR]')
  process.exit(2)
}
const out = { captures: resolve(dir), extra: extra ? resolve(extra) : null }
let failed = 0
for (const id of ids) {
  try {
    const pack = packOf(id)
    if (!pack) throw new Error('it installs no modpack; render.mjs draws its world')
    const icon = await packIcon(pack, { socket })
    for (const s of shapes(out)) save(out, id, s.name, await art(icon, s.w, s.h))
    console.log(`[${id}] ${pack.name}'s icon`)
  } catch (e) {
    failed++
    console.error(`[${id}] ${e.message}`)
  }
}
process.exit(failed ? 1 : 0)
