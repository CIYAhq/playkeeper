// The live demo's world imports: starting a new server from a world. The demo
// takes no bytes: its upload (upload.ts) only reports how much arrived, and
// whatever the archive holds, the check finds the sample world in it, named
// after the file, as a singleplayer world saved by Minecraft 1.21.11.

import { ApiError } from '@/api/client'
import type { ImportPreview, ImportWorld, WorldImport, WorldImportPreview, WorldImportVersion } from '@/api/types'
import { fakeSha, iso, versionsOf, type DemoState, type Request, type Routes } from './data'

const gb = 1024 ** 3
const worldVersion = '1.21.11'

function importOf(s: DemoState, r: Request): WorldImport {
  const imp = s.imports[r.params.imp ?? '']
  if (!imp) throw new ApiError(404, { error: 'That upload is gone.', code: 'not_found', hint: 'Choose the world’s file again.' })
  return imp
}

const received = (imp: WorldImport) => imp.files.reduce((n, f) => n + f.received, 0)

/** The world the check finds: the file's name, and a size that fits what arrived. */
function worldIn(imp: WorldImport): ImportWorld {
  const archive = imp.files[0]?.name ?? 'world.zip'
  const name = archive.replace(/\.(zip|tar\.gz|tgz|tar)$/i, '') || 'world'
  const bytes = Math.max(received(imp), 1) * 2.3
  return {
    id: 'w1',
    archive,
    path: name,
    level: { name, version: worldVersion, dataVersion: 4671, gameMode: 'survival', hardcore: false, difficulty: 'normal', dataPacks: ['vanilla'], seed: '-4172144997902289642', spawn: { x: 112, z: -48 } },
    origin: 'singleplayer',
    default: true,
    dimensions: ['minecraft:overworld', 'minecraft:the_nether', 'minecraft:the_end'],
    players: 4,
    sizeBytes: Math.round(bytes),
    files: Math.round(bytes / 220_000) + 40,
  }
}

function create(s: DemoState, r: Request): WorldImport {
  const id = `im${s.seq++}${fakeSha(r.now % 1_000_000).slice(0, 6)}`
  const imp: WorldImport = { id, createdAt: iso(r.now), files: [], limitBytes: 16 * gb }
  s.imports[id] = imp
  return imp
}

function announce(s: DemoState, r: Request): WorldImport {
  const imp = importOf(s, r)
  const b = (r.body ?? {}) as { name?: string; size?: number }
  imp.files.push({ index: imp.files.length, name: String(b.name ?? 'world.zip'), size: Math.max(0, Number(b.size) || 0), received: 0 })
  return imp
}

/** What upload.ts says arrived of a file: no bytes, only how many. */
function arrived(s: DemoState, r: Request): WorldImport {
  const imp = importOf(s, r)
  const f = imp.files[Number(r.params.n)]
  if (!f) throw new ApiError(404, { error: 'That file isn’t part of the upload.', code: 'not_found' })
  f.received = Math.min(f.size, Math.max(f.received, Number((r.body as { received?: number } | undefined)?.received) || 0))
  if (f.received === f.size) f.sha256 = fakeSha(f.size % 1_000_000)
  return imp
}

function inspect(s: DemoState, r: Request): WorldImport {
  const imp = importOf(s, r)
  if (imp.files.some((f) => f.received < f.size)) throw new ApiError(409, { error: 'The upload isn’t finished yet.', code: 'busy' })
  const w = worldIn(imp)
  imp.inspection = { archives: imp.files.map((f) => ({ name: f.name, format: 'zip', bytes: f.size, entries: w.files + 12 })), worlds: [w], warnings: [] }
  return imp
}

/** A new server's choices: the recommended version, then the world's own, which doesn't upgrade it. */
function versionChoices(): WorldImportVersion[] {
  const offered = versionsOf('paper')
  const recommended = offered.find((v) => v.recommended)
  const own = offered.find((v) => v.minecraftVersion === worldVersion)
  return [...(recommended ? [{ ...recommended, keep: false }] : []), ...(own ? [{ ...own, keep: true }] : [])]
}

function preview(s: DemoState, r: Request): WorldImportPreview {
  const imp = importOf(s, r)
  const world = imp.inspection?.worlds[0] ?? worldIn(imp)
  const choices = versionChoices()
  const chosen = choices.find((v) => v.id === (r.body as { versionId?: string } | undefined)?.versionId) ?? choices[0]
  const target = chosen?.minecraftVersion ?? worldVersion
  const upgrade = target !== worldVersion
  const share = [0.82, 0.13, 0.05]
  const dims: [string, string][] = [
    ['minecraft:overworld', 'world'],
    ['minecraft:the_nether', 'world_nether'],
    ['minecraft:the_end', 'world_the_end'],
  ]
  const p: ImportPreview = {
    world,
    target: { type: 'paper', minecraftVersion: target, levelName: 'world' },
    version: {
      compat: upgrade ? 'upgrade' : 'same',
      world: worldVersion,
      target,
      warnings: upgrade
        ? [{ kind: 'world_upgrade', params: { world: worldVersion, target }, text: `This world was saved by Minecraft ${worldVersion}. The server upgrades it to ${target} when it first starts, and an upgraded world can't be opened in ${worldVersion} again.`, hint: 'Keep your upload as a copy in case you want to go back.' }]
        : [],
    },
    folders: dims.map(([, folder]) => folder),
    fileCount: world.files,
    sizeBytes: world.sizeBytes,
    dimensions: dims.map(([id, folder], i) => ({ id, folder, files: Math.round(world.files * (share[i] ?? 0)), bytes: Math.round(world.sizeBytes * (share[i] ?? 0)) })),
    dataPacks: [],
    players: world.players,
    settings: [
      { key: 'difficulty', value: 'normal', source: 'level.dat' },
      { key: 'gamemode', value: 'survival', source: 'level.dat' },
      { key: 'level-seed', value: world.level?.seed ?? '', source: 'level.dat' },
    ],
    leftOut: [{ kind: 'session_lock', files: 1, bytes: 3, text: 'session.lock is left out; it only marks a world as open.' }],
    warnings: [],
    problems: [],
  }
  return { id: imp.id, preview: p, versions: choices, versionId: chosen?.id ?? '', keepsOriginal: true, memoryMB: 4096 }
}

/** What a server made from an upload is made with, for the engine's create; the upload is used up. */
export function fromUpload(s: DemoState, r: Request): { body: Record<string, unknown>; worldBytes: number } {
  const imp = importOf(s, r)
  const b = (r.body ?? {}) as { versionId?: string; name?: string; memoryMB?: number }
  const world = imp.inspection?.worlds[0] ?? worldIn(imp)
  delete s.imports[imp.id]
  return { body: { name: b.name, type: 'paper', versionId: b.versionId, memoryMB: b.memoryMB, motd: b.name, gameplay: { difficulty: 'normal', gameMode: 'survival' } }, worldBytes: world.sizeBytes }
}

export const worldRoutes: Routes = {
  'POST /api/machines/:machine/world-imports': create,
  'GET /api/machines/:machine/world-imports/:imp': importOf,
  'DELETE /api/machines/:machine/world-imports/:imp': (s, r) => {
    delete s.imports[r.params.imp ?? '']
    return {}
  },
  'POST /api/machines/:machine/world-imports/:imp/files': announce,
  'PUT /api/machines/:machine/world-imports/:imp/files/:n': arrived,
  'POST /api/machines/:machine/world-imports/:imp/inspect': inspect,
  'POST /api/machines/:machine/world-imports/:imp/preview': preview,
  // A restore leaves no world folder beside the live one in the demo.
  'GET /api/servers/:id/world-copies': () => [],
}
