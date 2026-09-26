import type { Page, Request, Route } from '@playwright/test'

// Realistic stand-ins for every API call that changes something, so the
// click-through can press every button without restarting, deleting or
// downloading anything. Reads go to the real panel. Each fake checks the
// request the way the panel does (CSRF and origin headers, body shape, the
// preference key rule) and answers with the shape the real handler returns.

export interface ApiCall {
  method: string
  path: string
  status: number
  faked: boolean
  error?: string
  /** A fake that answers with an error on purpose, like a wrong password. */
  expected?: boolean
  at: number
}

interface Reply {
  status: number
  body?: unknown
  headers?: Record<string, string>
  raw?: string
  expected?: boolean
}

type Handler = (req: { method: string; path: string; body: unknown; params: string[] }, state: FakeState) => Reply

interface FakeState {
  prefs: Record<string, string>
  backups: Map<string, Record<string, unknown>[]>
  update: Record<string, unknown>
  opSeq: number
  /** Each server's map as the panel last described it. */
  maps: Map<string, Record<string, unknown>>
  /** World uploads opened on the fakes. */
  imports: Map<string, WorldUpload>
}

interface UploadedFile {
  index: number
  name: string
  size: number
  received: number
  sha256?: string
}

interface WorldUpload {
  id: string
  createdAt: string
  files: UploadedFile[]
  limitBytes: number
  inspection?: unknown
}

const name = /^[A-Za-z0-9_]{3,16}$/
const prefKey = /^[a-z][a-z0-9.:_-]{0,63}$/

function invalid(error: string): Reply {
  return { status: 400, body: { error, code: 'invalid' } }
}

function op(state: FakeState, kind: string, serverId?: string): Reply {
  state.opSeq++
  return { status: 202, body: { id: `fake-op-${state.opSeq}`, serverId, kind, status: 'running', phase: '', actor: 'admin', startedAt: new Date().toISOString() } }
}

function playerName(body: unknown): string | undefined {
  const n = (body as { name?: unknown } | null)?.name
  return typeof n === 'string' && name.test(n) ? n : undefined
}

function backupFor(state: FakeState, serverId: string, backupId: string): Record<string, unknown> | undefined {
  return state.backups.get(serverId)?.find((b) => b.id === backupId)
}

const routes: [string, RegExp, Handler][] = [
  ['POST', /^\/api\/auth\/login$/, () => ({ status: 401, body: { error: 'Wrong username or password.', code: 'unauthorized' }, expected: true })],
  ['POST', /^\/api\/auth\/logout$/, () => ({ status: 200, body: {} })],
  ['POST', /^\/api\/auth\/logout-all$/, () => ({ status: 200, body: {} })],
  [
    'POST',
    /^\/api\/auth\/password$/,
    ({ body }) => {
      const b = body as { currentPassword?: string; newPassword?: string } | null
      if (!b?.currentPassword) return invalid('Enter your current password.')
      if (!b.newPassword || b.newPassword.length < 10) return invalid('The new password needs at least 10 characters.')
      return { status: 200, body: {} }
    },
  ],
  [
    'POST',
    /^\/api\/me\/prefs$/,
    ({ body }, state) => {
      const p = (body ?? {}) as Record<string, unknown>
      const keys = Object.keys(p)
      if (keys.length < 1 || keys.length > 20) return invalid('Send between 1 and 20 preferences.')
      for (const k of keys) {
        const v = p[k]
        if (!prefKey.test(k) || typeof v !== 'string' || v.length > 512) return invalid(`Invalid preference "${k}".`)
      }
      for (const k of keys) {
        if (p[k] === '') delete state.prefs[k]
        else state.prefs[k] = p[k] as string
      }
      return { status: 200, body: { ...state.prefs } }
    },
  ],
  ['POST', /^\/api\/servers\/(\w+)\/(start|stop|restart)$/, (r, state) => op(state, r.params[1] ?? '', r.params[0])],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/backups$/,
    (r, state) => {
      const note = (r.body as { note?: unknown } | null)?.note
      if (note !== undefined && (typeof note !== 'string' || note.length > 200)) return invalid('Keep the note under 200 characters.')
      return op(state, 'backup', r.params[0])
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/settings$/,
    ({ body }) => (body && typeof body === 'object' && Object.keys(body).length > 0 ? { status: 200, body: {} } : invalid('Nothing to change.')),
  ],
  ['POST', /^\/api\/servers\/(\w+)\/version$/, (r, state) => ((r.body as { versionId?: string } | null)?.versionId ? op(state, 'update-version', r.params[0]) : invalid('Pick a version.'))],
  ['POST', /^\/api\/servers\/(\w+)\/delete$/, (r, state) => ((r.body as { confirm?: string } | null)?.confirm ? op(state, 'delete', r.params[0]) : invalid('Type the server’s name to confirm.'))],
  ['POST', /^\/api\/servers\/(\w+)\/icon$/, () => ({ status: 200, body: {} })],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/command$/,
    ({ body }) => {
      const c = (body as { command?: unknown } | null)?.command
      if (typeof c !== 'string' || !c.trim()) return invalid('Type a command.')
      return { status: 200, body: { output: 'There are 0 of a max of 10 players online: ' } }
    },
  ],
  ['POST', /^\/api\/servers\/(\w+)\/(whitelist|operators|kick)$/, ({ body }) => (playerName(body) ? { status: 200, body: {} } : invalid('Minecraft usernames are 3–16 letters, numbers or underscores.'))],
  ['DELETE', /^\/api\/servers\/(\w+)\/(whitelist|operators)\/([^/]+)$/, (r) => (name.test(decodeURIComponent(r.params[2] ?? '')) ? { status: 200, body: {} } : invalid('Minecraft usernames are 3–16 letters, numbers or underscores.'))],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/backups\/([\w-]+)\/verify$/,
    (r, state) => {
      const b = backupFor(state, r.params[0] ?? '', r.params[1] ?? '')
      return b ? { status: 200, body: { ...b, verified: true, verifiedAt: new Date().toISOString() } } : { status: 404, body: { error: 'No such backup.', code: 'not_found' } }
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/backups\/([\w-]+)\/restore$/,
    (r, state) => {
      const b = backupFor(state, r.params[0] ?? '', r.params[1] ?? '')
      if (!b) return { status: 404, body: { error: 'No such backup.', code: 'not_found' } }
      return { status: 200, body: restorePreview(b, r.params[0]) }
    },
  ],
  ['DELETE', /^\/api\/servers\/(\w+)\/backups\/([\w-]+)$/, () => ({ status: 200, body: {} })],
  ['POST', /^\/api\/(servers|machines)\/(\w+)\/restore\/upload$/, () => ({ status: 200, body: restorePreview(undefined) })],
  ['POST', /^\/api\/machines\/(\w+)\/servers$/, (r, state) => ((r.body as { acceptEula?: boolean } | null)?.acceptEula ? op(state, 'create', 'fakeserver') : invalid('Accept the Minecraft EULA first.'))],
  ['POST', /^\/api\/machines\/(\w+)\/update\/check$/, (_r, state) => ({ status: 200, body: { current: 'dev', supported: true, available: false, ...state.update, checkedAt: new Date().toISOString() } })],
  ['POST', /^\/api\/machines\/(\w+)\/update\/apply$/, (_r, state) => op(state, 'update')],
  ['POST', /^\/api\/machines\/(\w+)\/restore\/([\w-]+)\/apply$/, (r, state) => ((r.body as { confirm?: string } | null)?.confirm ? op(state, 'restore') : invalid('Type the confirmation.'))],
  ['DELETE', /^\/api\/machines\/(\w+)\/restore\/([\w-]+)$/, () => ({ status: 200, body: {} })],
  // Wave 6: the map's switches, and worlds uploaded for a new server.
  ['POST', /^\/api\/servers\/(\w+)\/map\/enable$/, (r, state) => op(state, 'map_enable', r.params[0])],
  ['POST', /^\/api\/servers\/(\w+)\/map\/disable$/, (r, state) => (typeof (r.body as { deleteMap?: unknown } | null)?.deleteMap === 'boolean' ? op(state, 'map_disable', r.params[0]) : invalid('Say whether to keep the drawn map.'))],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/map\/share$/,
    (r, state) => {
      const b = (r.body ?? {}) as { public?: unknown; players?: unknown }
      if (b.public === undefined && b.players === undefined) return invalid('Say which switch to change.')
      if ((b.public !== undefined && typeof b.public !== 'boolean') || (b.players !== undefined && typeof b.players !== 'boolean')) return invalid('Invalid request.')
      const map = state.maps.get(r.params[0] ?? '') ?? {}
      const shared = typeof b.public === 'boolean' ? b.public : map.public === true
      const token = 'Fk3dEf6hIj9lMn2pQr5tUv'
      return { status: 200, body: { ...map, public: shared, publicPlayers: typeof b.players === 'boolean' ? b.players : map.publicPlayers === true, path: shared ? `/map/${token}` : '', link: undefined } }
    },
  ],
  ['POST', /^\/api\/servers\/(\w+)\/map\/restart-later$/, (r, state) => ({ status: 200, body: { ...state.maps.get(r.params[0] ?? ''), restartWhenEmpty: true } })],
  [
    'POST',
    /^\/api\/machines\/(\w+)\/world-imports$/,
    (_r, state) => {
      const id = `f${String(state.imports.size + 1).padStart(15, '0')}`
      const imp: WorldUpload = { id, createdAt: new Date().toISOString(), files: [], limitBytes: 68_719_476_736 }
      state.imports.set(id, imp)
      return { status: 201, body: imp }
    },
  ],
  [
    'POST',
    /^\/api\/machines\/(\w+)\/world-imports\/(\w+)\/files$/,
    (r, state) => {
      const imp = state.imports.get(r.params[1] ?? '')
      if (!imp) return importGone()
      const b = (r.body ?? {}) as { name?: unknown; size?: unknown }
      if (typeof b.name !== 'string' || !/\.(zip|tar\.gz|tgz|tar)$/i.test(b.name) || typeof b.size !== 'number' || b.size <= 0) return invalid('Choose a .zip, .tar.gz or .tar file.')
      imp.files.push({ index: imp.files.length, name: b.name, size: b.size, received: 0 })
      return { status: 201, body: imp }
    },
  ],
  [
    'PUT',
    /^\/api\/machines\/(\w+)\/world-imports\/(\w+)\/files\/(\d+)$/,
    (r, state) => {
      const imp = state.imports.get(r.params[1] ?? '')
      const f = imp?.files[Number(r.params[2])]
      if (!imp || !f) return importGone()
      f.received = f.size
      f.sha256 = '0a1daf7328832d9ce31542d7458ffcc022d8f4b1092ce6195e97730795124709'
      return { status: 200, body: imp }
    },
  ],
  [
    'POST',
    /^\/api\/machines\/(\w+)\/world-imports\/(\w+)\/inspect$/,
    (r, state) => {
      const imp = state.imports.get(r.params[1] ?? '')
      const f = imp?.files[0]
      if (!imp || !f) return importGone()
      if (imp.files.some((x) => x.received < x.size)) return { status: 409, body: { error: 'The upload isn’t finished yet.', code: 'conflict' } }
      imp.inspection = { archives: [{ name: f.name, format: 'zip', bytes: f.size, entries: 8 }], worlds: [sampleWorld(f.name)] }
      return { status: 200, body: imp }
    },
  ],
  [
    'POST',
    /^\/api\/machines\/(\w+)\/world-imports\/(\w+)\/preview$/,
    (r, state) => {
      const imp = state.imports.get(r.params[1] ?? '')
      const f = imp?.files[0]
      if (!imp?.inspection || !f) return { status: 409, body: { error: 'Check the upload first.', code: 'conflict' } }
      const versionId = (r.body as { versionId?: unknown } | null)?.versionId
      if (versionId !== undefined && versionId !== 'paper-26.2' && versionId !== 'paper-1.21.4') return invalid('Pick one of the versions the preview offers.')
      return { status: 200, body: samplePreview(imp.id, f.name, versionId === 'paper-1.21.4') }
    },
  ],
  [
    'POST',
    /^\/api\/machines\/(\w+)\/world-imports\/(\w+)\/create$/,
    (r, state) => {
      if (!state.imports.get(r.params[1] ?? '')?.inspection) return importGone()
      const b = (r.body ?? {}) as { acceptEula?: unknown; name?: unknown; memoryMB?: unknown }
      if (b.acceptEula !== true) return invalid('You must accept the Minecraft EULA before Playkeeper downloads or starts a server.')
      if (typeof b.memoryMB !== 'number' || b.memoryMB <= 0) return invalid('Pick how much memory the server gets.')
      return op(state, 'create', 'fakeserver')
    },
  ],
  [
    'DELETE',
    /^\/api\/machines\/(\w+)\/world-imports\/(\w+)$/,
    (r, state) => {
      state.imports.delete(r.params[1] ?? '')
      return { status: 204, raw: '' }
    },
  ],
]

function importGone(): Reply {
  return { status: 404, body: { error: 'This upload isn’t here anymore.', hint: 'Upload the world again.', code: 'not_found' } }
}

/** A Minecraft 1.21.4 singleplayer world, as the agent describes it after checking an upload. */
function sampleWorld(archive: string) {
  return {
    id: `${archive.replace(/\.(zip|tar\.gz|tgz|tar)$/i, '')}/Survival 2024`,
    archive,
    path: 'Survival 2024',
    level: {
      name: 'Survival 2024',
      version: '1.21.4',
      dataVersion: 4189,
      series: 'main',
      gameMode: 'survival',
      hardcore: false,
      difficulty: 'normal',
      dataPacks: ['vanilla', 'file/Graves.zip'],
      brands: ['vanilla'],
      seed: '-4172144997902289642',
      spawn: { x: 40, z: 12 },
    },
    origin: 'singleplayer',
    default: true,
    dimensions: ['minecraft:overworld', 'minecraft:the_nether', 'minecraft:the_end'],
    players: 1,
    sizeBytes: 5_767_513,
    files: 8,
  }
}

const paperVersions = [
  {
    id: 'paper-26.2',
    label: 'Paper 26.2',
    minecraftVersion: '26.2',
    paperBuild: 129,
    jarSha256: 'b1d8f6bfa1b6101fa8e947b53041cb3bdf5540e7b83b6547ca19ba7edefeb083',
    java: 25,
    recommended: true,
    notes: 'Recommended: the newest stable Paper release. Java Edition 26.2 clients can join.',
    channel: 'STABLE',
    experimental: false,
    supported: true,
    keep: false,
  },
  {
    id: 'paper-1.21.4',
    label: 'Paper 1.21.4',
    minecraftVersion: '1.21.4',
    paperBuild: 232,
    jarSha256: '5ee4f542f628a14c644410b08c94ea42e772ef4d29fe92973636b6813d4eaffc',
    java: 21,
    recommended: false,
    notes: 'Older version that PaperMC no longer updates. Choose it for friends or plugins that still need 1.21.4.',
    channel: 'STABLE',
    experimental: false,
    supported: false,
    keep: true,
  },
]

/** The agent's preview of the sample world on Paper 26.2, or kept on 1.21.4. */
function samplePreview(id: string, archive: string, keep: boolean) {
  const target = keep ? '1.21.4' : '26.2'
  const upgrade = {
    kind: 'world_upgrade',
    params: { target, world: '1.21.4' },
    text: `This world was saved by Minecraft 1.21.4. The server upgrades it to ${target} when it first starts, and an upgraded world can't be opened in 1.21.4 again.`,
    hint: 'Keep your upload as a copy in case you want to go back.',
  }
  return {
    id,
    preview: {
      world: sampleWorld(archive),
      target: { type: 'paper', minecraftVersion: target, levelName: 'world' },
      version: keep ? { compat: 'same', world: '1.21.4', dataVersion: 4189, target, targetDataVersion: 4189 } : { compat: 'upgrade', world: '1.21.4', dataVersion: 4189, target, targetDataVersion: 4903, warnings: [upgrade] },
      folders: keep ? ['world', 'world_nether', 'world_the_end'] : ['world'],
      fileCount: 7,
      sizeBytes: 5_767_510,
      dimensions: [
        { id: 'minecraft:overworld', folder: 'world', files: 2, bytes: 4_194_304 },
        { id: 'minecraft:the_nether', folder: keep ? 'world_nether/DIM-1' : 'world/DIM-1', files: 1, bytes: 1_048_576 },
        { id: 'minecraft:the_end', folder: keep ? 'world_the_end/DIM1' : 'world/DIM1', files: 1, bytes: 524_288 },
      ],
      dataPacks: ['file/Graves.zip'],
      players: 1,
      settings: [
        { key: 'level-seed', value: '-4172144997902289642', source: 'level.dat' },
        { key: 'gamemode', value: 'survival', source: 'level.dat' },
        { key: 'difficulty', value: 'normal', source: 'level.dat' },
        { key: 'hardcore', value: 'false', source: 'level.dat' },
      ],
      leftOut: [{ kind: 'session_lock', files: 1, bytes: 3, examples: ['Survival 2024/session.lock'], text: 'session.lock is left out; it only marks a world as open.' }],
      warnings: keep
        ? [{ kind: 'bukkit_split', params: { levelName: 'world' }, text: 'Paper keeps the Nether and the End in their own folders, world_nether and world_the_end. Playkeeper moves them there.' }]
        : [
            upgrade,
            {
              kind: 'layout_upgrade',
              params: { target },
              text: `Minecraft ${target} keeps worlds in a newer folder layout. The server converts this world when it first starts.`,
              hint: 'Big worlds can take several minutes; let the first start finish.',
            },
          ],
    },
    versions: paperVersions,
    versionId: keep ? 'paper-1.21.4' : 'paper-26.2',
    keepsOriginal: !keep,
    willCreateRollback: false,
    memoryMB: 8192,
  }
}

/** A generated 8×8 face, so tests never fetch or show a real player's skin. */
export function standInFace(player: string): string {
  let h = 2166136261
  for (const ch of player) h = Math.imul(h ^ ch.charCodeAt(0), 16777619) >>> 0
  const skin = ['#F2C9A0', '#E0AC7E', '#C68A5B', '#8D5A3B', '#F5D6B8'][h % 5]
  const hair = ['#3B2A1E', '#6B4A2B', '#C9A15A', '#222222', '#8E3B2E', '#5A5F66'][(h >> 4) % 6]
  const eye = ['#2F5DA8', '#3C7A3A', '#4A3526', '#2B2B2B'][(h >> 8) % 4]
  const px: string[] = []
  for (let y = 0; y < 8; y++) {
    for (let x = 0; x < 8; x++) {
      let c = skin
      if (y < 2 || (y === 2 && (x === 0 || x === 7))) c = hair
      if (y === 4 && (x === 1 || x === 6)) c = '#FFFFFF'
      if (y === 4 && (x === 2 || x === 5)) c = eye
      if (y === 6 && (x === 3 || x === 4)) c = '#7A4A3A'
      px.push(`<rect x="${x}" y="${y}" width="1" height="1" fill="${c}"/>`)
    }
  }
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8" shape-rendering="crispEdges">${px.join('')}</svg>`
}

function restorePreview(b: Record<string, unknown> | undefined, serverId?: string) {
  return {
    id: 'fakerestore',
    serverId,
    source: 'backup',
    receivedAt: new Date().toISOString(),
    sizeBytes: Number(b?.sizeBytes ?? 12_000_000),
    sha256: String(b?.sha256 ?? '0'.repeat(64)),
    manifest: undefined,
    compatible: true,
    problems: [],
    warnings: [],
    currentWorld: { exists: true, levelName: String(b?.levelName ?? 'world'), sizeBytes: 11_000_000 },
    willCreateRollback: true,
    needsEula: !serverId,
    memoryMB: 2048,
    confirmPhrase: 'replace world',
    steps: ['Stop the server', 'Save the current world as a backup', 'Put the backup’s world in place', 'Start the server'],
    notRestored: [],
  }
}

/**
 * Serves the fakes on a page. The returned list collects every API call with
 * its status; unknown writes answer 501 and are listed as unfaked so a new
 * endpoint can't slip through unchecked.
 */
export async function installFakes(page: Page, baseURL: string): Promise<{ calls: ApiCall[]; unfaked: string[] }> {
  const calls: ApiCall[] = []
  const unfaked: string[] = []
  const state: FakeState = { prefs: {}, backups: new Map(), update: {}, opSeq: 0, maps: new Map(), imports: new Map() }
  const origin = new URL(baseURL).origin

  // Links out of the dashboard open a stand-in page instead of the internet.
  await page.context().route(
    (url) => url.origin !== origin,
    (route) => route.fulfill({ status: 200, contentType: 'text/html', body: '<!doctype html><title>Outside Playkeeper</title>' }),
  )

  await page.route(`${origin}/api/**`, async (route: Route, request: Request) => {
    const url = new URL(request.url())
    const method = request.method()
    const path = url.pathname
    const at = Date.now()
    if (method === 'GET' || method === 'HEAD') {
      const head = /^\/api\/players\/([^/]+)\/head$/.exec(path)
      if (head?.[1]) {
        calls.push({ method, path, status: 200, faked: true, at })
        await route.fulfill({ status: 200, contentType: 'image/svg+xml', body: standInFace(decodeURIComponent(head[1])) })
        return
      }
      if (/^\/api\/servers\/\w+\/backups\/[\w-]+\/download$/.test(path)) {
        calls.push({ method, path, status: 200, faked: true, at })
        await route.fulfill({ status: 200, headers: { 'Content-Type': 'application/gzip', 'Content-Disposition': 'attachment; filename="backup.tar.gz"' }, body: 'fake backup' })
        return
      }
      const res = await route.fetch().catch(() => null)
      if (!res) {
        await route.abort().catch(() => {})
        return
      }
      // A map tile nobody has explored yet is a 404 the map leaves blank.
      const undrawn = res.status() === 404 && /^\/api\/(servers\/\w+\/map|public\/map\/\w+)\/tiles\//.test(path)
      calls.push({ method, path, status: res.status(), faked: false, at, expected: undrawn })
      if (res.ok()) {
        if (path === '/api/me/prefs') Object.assign(state.prefs, await res.json().catch(() => ({})))
        const m = /^\/api\/servers\/(\w+)\/backups$/.exec(path)
        if (m?.[1]) state.backups.set(m[1], await res.json().catch(() => []))
        const map = /^\/api\/servers\/(\w+)\/map$/.exec(path)
        if (map?.[1]) state.maps.set(map[1], await res.json().catch(() => ({})))
        if (/^\/api\/machines\/\w+\/update$/.test(path)) state.update = await res.json().catch(() => ({}))
      }
      // The page may have moved on and cancelled the request meanwhile.
      await route.fulfill({ response: res }).catch(() => {})
      return
    }
    const headers = request.headers()
    let reply: Reply | undefined
    if (headers['x-requested-with'] !== 'playkeeper' || (path !== '/api/auth/login' && path !== '/api/setup' && !headers['x-csrf-token'])) {
      reply = { status: 403, body: { error: 'Security token missing or invalid. Reload the page and try again.', code: 'forbidden' } }
    } else {
      let body: unknown = null
      if ((headers['content-type'] ?? '').includes('json')) {
        try {
          body = request.postDataJSON()
        } catch {
          body = undefined
        }
      }
      for (const [m, re, handle] of routes) {
        const hit = m === method ? re.exec(path) : null
        if (hit) {
          reply = handle({ method, path, body, params: hit.slice(1) }, state)
          break
        }
      }
    }
    if (!reply) {
      unfaked.push(`${method} ${path}`)
      reply = { status: 501, body: { error: `No fake for ${method} ${path}.`, code: 'internal' } }
    }
    const error = reply.status >= 400 ? String((reply.body as { error?: string } | undefined)?.error ?? '') : undefined
    calls.push({ method, path, status: reply.status, faked: true, error, expected: reply.expected, at })
    await route.fulfill({ status: reply.status, headers: { 'Content-Type': 'application/json', ...reply.headers }, body: reply.raw ?? JSON.stringify(reply.body ?? {}) })
  })
  return { calls, unfaked }
}
