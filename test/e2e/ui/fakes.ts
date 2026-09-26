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

interface DialAddress {
  kind: string
  address: string
}

interface FakeState {
  prefs: Record<string, string>
  backups: Map<string, Record<string, unknown>[]>
  update: Record<string, unknown>
  opSeq: number
  /** What GET /api/machines/link last said: the addresses a joining machine can dial and the dashboard's fingerprint. */
  link: { addresses?: DialAddress[]; fingerprint?: string }
  machines: { id: string; kind: string }[]
  seq: number
}

const name = /^[A-Za-z0-9_]{3,16}$/
const prefKey = /^[a-z][a-z0-9.:_-]{0,63}$/
const id = /^[a-z2-9]{10}$/
const worldCopyName = /^data\.(replaced|failed-restore)-[0-9]{8}-[0-9]{6}$/

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

/** A new id in the panel's alphabet, different on every call. */
function nextId(state: FakeState, alphabet = 'abcdefghijkmnpqrstuvwxyz23456789', length = 10): string {
  state.seq++
  let n = state.seq * 2654435761
  let out = ''
  for (let i = 0; i < length; i++) {
    out += alphabet.charAt(n % alphabet.length)
    n = Math.floor(n / alphabet.length) + 7919 * (i + 1)
  }
  return out
}

function tokenReply(body: unknown, state: FakeState): Reply {
  const b = (body ?? {}) as { name?: unknown; role?: unknown; allServers?: unknown; servers?: unknown; days?: unknown }
  const tokenName = typeof b.name === 'string' ? b.name.trim() : ''
  if (!tokenName || [...tokenName].length > 40) return invalid('Give the token a name of up to 40 characters, such as "Claude on my laptop".')
  const days = b.days === undefined || b.days === 0 ? 60 : b.days
  if (typeof days !== 'number' || ![30, 60, 90, 365].includes(days)) return invalid('A token can last 30, 60, 90 or 365 days.')
  if (typeof b.role !== 'string' || !['viewer', 'moderator', 'admin'].includes(b.role)) return invalid('Choose what the token can do: viewer, moderator or admin.')
  const servers = Array.isArray(b.servers) ? b.servers : []
  if (b.allServers === true && servers.length > 0) return invalid('Send either all servers or a list of servers, not both.')
  if (b.allServers !== true && (servers.length === 0 || !servers.every((x) => typeof x === 'string' && id.test(x)))) return invalid('Choose at least one server, or all servers.')
  const now = Date.now()
  const token = { id: nextId(state), name: tokenName, role: b.role, allServers: b.allServers === true, servers, createdAt: new Date(now).toISOString(), expiresAt: new Date(now + days * 86_400_000).toISOString(), account: 'admin', mine: true }
  return { status: 201, body: { token, secret: `pk_mcp_${nextId(state, 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789', 32)}` } }
}

/** A machine name as machinelink.CleanName makes it: letters, digits, single spaces and - _ ., at most 40. */
function cleanName(raw: string): string {
  return raw
    .replace(/[^\p{L}\p{N}\s._-]/gu, '')
    .trim()
    .split(/\s+/)
    .join(' ')
    .slice(0, 40)
    .trim()
}

/** The join command in both forms, as machinelink.Command writes them. */
function joinCommand(address: string, code: string, fingerprint: string, machineName: string) {
  const quote = (a: string) => (/^[A-Za-z0-9._:/@%+=,-]+$/.test(a) ? a : `'${a.replaceAll("'", '')}'`)
  const shell = (args: string[]) => args.map(quote).join(' ')
  const flags = ['--code', code, '--fingerprint', fingerprint, ...(machineName ? ['--name', machineName] : [])]
  const continued = (head: string, pairs: string[]) => {
    const lines = [head]
    for (let i = 0; i + 1 < pairs.length; i += 2) lines.push(`  ${shell(pairs.slice(i, i + 2))}`)
    return lines.map((l, i) => (i < lines.length - 1 ? `${l} \\` : l))
  }
  const install = 'curl -fsSL https://playkeeper.io/install | sudo sh -s --'
  return {
    install: `${install} ${shell(['--join', address, ...flags])}`,
    join: `sudo playkeeper join ${shell([address, ...flags])}`,
    installLines: continued(install, ['--join', address, ...flags]),
    joinLines: continued(`sudo playkeeper join ${quote(address)}`, flags),
  }
}

function joinCodeReply(body: unknown, state: FakeState): Reply {
  const b = (body ?? {}) as { name?: unknown; dial?: unknown }
  const raw = typeof b.name === 'string' ? b.name : ''
  const machineName = cleanName(raw)
  if (raw.trim() && !machineName) return invalid('Use letters, numbers, spaces, dashes, dots or underscores in the name.')
  const addresses = state.link.addresses ?? []
  const dial = addresses.find((a) => a.kind === b.dial) ?? (b.dial ? undefined : addresses[0])
  if (!dial) return invalid('Choose the address the machine dials.')
  const raw8 = nextId(state, '0123456789ABCDEFGHJKMNPQRSTVWXYZ', 8)
  const code = `${raw8.slice(0, 4)}-${raw8.slice(4)}`
  const now = Date.now()
  const view = { id: nextId(state), name: machineName || undefined, dials: dial.address, createdAt: new Date(now).toISOString(), expiresAt: new Date(now + 30 * 60_000).toISOString(), createdBy: 'admin', state: 'waiting' }
  return { status: 201, body: { ...view, code, ...joinCommand(dial.address, code, state.link.fingerprint ?? '4N2DGMFMPH723389KAWSKMR2EM', machineName) } }
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
  // Wave 8: AI agent tokens, join codes and joined machines.
  ['POST', /^\/api\/tokens$/, ({ body }, state) => tokenReply(body, state)],
  ['DELETE', /^\/api\/tokens\/([^/]+)$/, (r) => (id.test(r.params[0] ?? '') ? { status: 204 } : invalid('Invalid token id.'))],
  ['POST', /^\/api\/machines\/join-codes$/, ({ body }, state) => joinCodeReply(body, state)],
  ['DELETE', /^\/api\/machines\/join-codes\/([^/]+)$/, (r) => (id.test(r.params[0] ?? '') ? { status: 204 } : { status: 404, body: { error: 'Join code not found.', code: 'not_found' } })],
  [
    'DELETE',
    /^\/api\/machines\/([a-z2-9]{10})$/,
    (r, state) => (state.machines.find((m) => m.id === r.params[0])?.kind === 'local' ? invalid('This is the dashboard’s own machine, so it can’t be removed.') : { status: 204 }),
  ],
  ['DELETE', /^\/api\/servers\/(\w+)\/world-copies\/([^/]+)$/, (r) => (worldCopyName.test(decodeURIComponent(r.params[1] ?? '')) ? { status: 204, raw: '' } : invalid('Invalid world copy name.'))],
]

/** A world a restore left behind. A fresh install has none, so the World tab's notice and its Discard button would never show. */
const leftoverWorld = { name: 'data.replaced-20260924-090000', kind: 'previous', createdAt: '2026-09-24T09:00:00Z', sizeBytes: 1_100_000_000 }

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
  const state: FakeState = { prefs: {}, backups: new Map(), update: {}, opSeq: 0, link: {}, machines: [], seq: 0 }
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
      if (/^\/api\/servers\/\w+\/world-copies$/.test(path)) {
        calls.push({ method, path, status: 200, faked: true, at })
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([leftoverWorld]) })
        return
      }
      const res = await route.fetch().catch(() => null)
      if (!res) {
        await route.abort().catch(() => {})
        return
      }
      calls.push({ method, path, status: res.status(), faked: false, at })
      if (res.ok()) {
        if (path === '/api/me/prefs') Object.assign(state.prefs, await res.json().catch(() => ({})))
        const m = /^\/api\/servers\/(\w+)\/backups$/.exec(path)
        if (m?.[1]) state.backups.set(m[1], await res.json().catch(() => []))
        if (/^\/api\/machines\/\w+\/update$/.test(path)) state.update = await res.json().catch(() => ({}))
        if (path === '/api/machines/link') state.link = await res.json().catch(() => ({}))
        if (path === '/api/machines') state.machines = await res.json().catch(() => [])
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
    await route.fulfill({ status: reply.status, headers: { 'Content-Type': 'application/json', ...reply.headers }, body: reply.status === 204 ? '' : (reply.raw ?? JSON.stringify(reply.body ?? {})) })
  })
  return { calls, unfaked }
}
