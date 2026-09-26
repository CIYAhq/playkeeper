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
  origin: string
  prefs: Record<string, string>
  backups: Map<string, Record<string, unknown>[]>
  update: Record<string, unknown>
  discord: Record<string, unknown>
  opSeq: number
  inviteSeq: number
}

const name = /^[A-Za-z0-9_]{3,16}$/
const prefKey = /^[a-z][a-z0-9.:_-]{0,63}$/
const inviteId = /^[a-kmnp-z2-9]{10}$/
const day = 86_400_000
const expiryDays = new Map([
  ['1d', 1],
  ['7d', 7],
  ['30d', 30],
  ['until_turned_off', 0],
])
const webhookHosts = new Set(['discord.com', 'canary.discord.com', 'ptb.discord.com', 'discordapp.com'])
const badName = 'Minecraft usernames are 3–16 letters, numbers or underscores.'

function invalid(error: string): Reply {
  return { status: 400, body: { error, code: 'invalid' } }
}

function notFound(error: string): Reply {
  return { status: 404, body: { error, code: 'not_found' } }
}

const noContent: Reply = { status: 204, raw: '' }

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

/** A new invite's id and link path, shaped like the panel's. */
function newInvite(state: FakeState): { id: string; path: string; createdAt: string } {
  const n = ++state.inviteSeq
  const a = 'abcdefghijkmnpqrstuvwxyz23456789'
  return { id: `fakeinv${a[(n >> 10) & 31]}${a[(n >> 5) & 31]}${a[n & 31]}`, path: `/join/FakeInviteCode${String(n).padStart(8, '0')}`, createdAt: new Date().toISOString() }
}

function badLabel(label: unknown): boolean {
  return label !== undefined && (typeof label !== 'string' || [...label].length > 64 || /\p{C}/u.test(label))
}

/** Why the panel would refuse a role and servers chosen on the Team page; only a new invite takes a label. */
function grantProblem(body: unknown, labelled: boolean): string | undefined {
  const b = body as { role?: unknown; servers?: { all?: unknown; servers?: unknown } | null; label?: unknown } | null
  if (!b || !['viewer', 'moderator', 'admin'].includes(String(b.role)) || (labelled ? badLabel(b.label) : b.label !== undefined)) return 'Invalid request.'
  const list = b.servers?.servers
  const some = Array.isArray(list) ? list : []
  if (b.servers?.all === true && some.length) return 'Choose all servers or some of them, not both.'
  if (b.servers?.all !== true && !some.length) return 'Choose all servers, or at least one server.'
  return undefined
}

function playerInvite(serverId: string, body: unknown, state: FakeState): Reply {
  const b = (body ?? {}) as { label?: unknown; expiry?: unknown; maxUses?: unknown; unlimited?: unknown; approval?: unknown }
  const days = expiryDays.get(String(b.expiry || '7d'))
  if (days === undefined) return invalid('An invite link works for 1 day, 7 days, 30 days, or until you turn it off.')
  const max = b.maxUses ?? 0
  if (typeof max !== 'number' || !Number.isInteger(max)) return invalid('Invalid request.')
  if (b.unlimited === true && max !== 0) return invalid('Choose a number of friends or no limit, not both.')
  const uses = b.unlimited === true ? 0 : max || 5
  if (b.unlimited !== true && (uses < 1 || uses > 100)) return invalid('An invite link can be for 1 to 100 friends, or have no limit.')
  const approval = b.approval || 'right_away'
  if (approval !== 'right_away' && approval !== 'after_yes') return invalid('An invite link lets people in right away or after you say yes.')
  if (badLabel(b.label)) return invalid('A label is at most 64 characters, on one line.')
  const inv = newInvite(state)
  const expiresAt = days ? new Date(Date.parse(inv.createdAt) + days * day).toISOString() : undefined
  return { status: 201, body: { ...inv, kind: 'player', projectId: 'fakeprojct', serverId, approval, label: b.label || undefined, createdBy: 1, expiresAt, maxUses: uses, uses: 0, status: 'active', usesLeft: uses || undefined } }
}

function teamInvite(body: unknown, state: FakeState): Reply {
  const why = grantProblem(body, true)
  if (why) return invalid(why)
  const b = body as { role: string; servers: unknown; label?: string }
  const { path, ...inv } = newInvite(state)
  const expiresAt = new Date(Date.parse(inv.createdAt) + 7 * day).toISOString()
  return {
    status: 201,
    body: { invite: { ...inv, kind: 'member', projectId: 'fakeprojct', role: b.role, servers: b.servers, label: b.label || undefined, createdBy: 1, expiresAt, maxUses: 1, uses: 0, status: 'active', usesLeft: 1 }, path, link: { base: state.origin, friendly: false } },
  }
}

/** Why the agent would refuse a pasted Discord webhook URL. */
function webhookProblem(raw: unknown): string | undefined {
  const text = typeof raw === 'string' ? raw.trim() : ''
  if (!text) return 'No webhook URL was given.'
  let u: URL
  try {
    u = new URL(text)
  } catch {
    return text.includes('://') ? 'That is not a web address.' : 'A Discord webhook URL starts with https://.'
  }
  if (u.protocol !== 'https:') return 'A Discord webhook URL starts with https://.'
  if (u.username || u.password || !webhookHosts.has(u.hostname.toLowerCase()) || u.port) return 'That address is not on discord.com, so it is not a Discord webhook URL.'
  const m = /^\/api(?:\/v[0-9]{1,2})?\/webhooks\/([^/]+)\/([^/]+)\/?$/.exec(u.pathname)
  if (!m) return 'That address is on Discord but is not a webhook URL.'
  if (!/^[0-9]{17,20}$/.test(m[1] ?? '')) return 'The webhook URL is damaged: the number after /webhooks/ is not a Discord id.'
  if (!/^[A-Za-z0-9_-]{60,100}$/.test(m[2] ?? '')) return 'The webhook URL looks cut off or changed: its secret last part is not valid.'
  return undefined
}

function discordAlerts(body: unknown, state: FakeState): Reply {
  const b = body as { alerts?: unknown; liveStatus?: unknown } | null
  const alerts = b?.alerts
  const kinds = Array.isArray(state.discord.kinds) ? (state.discord.kinds as unknown[]) : []
  if (!Array.isArray(alerts) || !alerts.every((k) => kinds.includes(k)) || typeof b?.liveStatus !== 'boolean') return invalid('Choose alerts from the list.')
  return { status: 200, body: { ...state.discord, alerts, liveStatus: b.liveStatus } }
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
  ['POST', /^\/api\/servers\/(\w+)\/(whitelist|operators|kick)$/, ({ body }) => (playerName(body) ? { status: 200, body: {} } : invalid(badName))],
  ['DELETE', /^\/api\/servers\/(\w+)\/(whitelist|operators)\/([^/]+)$/, (r) => (name.test(decodeURIComponent(r.params[2] ?? '')) ? { status: 200, body: {} } : invalid(badName))],
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
  ['POST', /^\/api\/servers\/(\w+)\/invites$/, (r, state) => playerInvite(r.params[0] ?? '', r.body, state)],
  ['DELETE', /^\/api\/servers\/(\w+)\/invites\/([^/]+)$/, (r) => (inviteId.test(r.params[1] ?? '') ? noContent : notFound('No such invite link.'))],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/join-requests\/([^/]+)\/(approve|decline)$/,
    (r) =>
      inviteId.test(r.params[1] ?? '')
        ? { status: 200, body: { request: { id: r.params[1], serverId: r.params[0], state: r.params[2] === 'approve' ? 'approved' : 'declined', decidedAt: new Date().toISOString(), decidedBy: 1 } } }
        : notFound('No such join request.'),
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/players\/message$/,
    ({ body }) => {
      if (!playerName(body)) return invalid(badName)
      const m = (body as { message?: unknown }).message
      const text = typeof m === 'string' ? m.trim() : ''
      if (!text || [...text].length > 200 || /\p{C}|[^\S ]/u.test(text)) return invalid('Write a message of up to 200 characters on one line.')
      return { status: 200, body: { message: 'Message sent.' } }
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/ban$/,
    ({ body }) => {
      const n = playerName(body)
      return n ? { status: 200, body: { message: `Banned ${n}: Banned from the Playkeeper dashboard` } } : invalid(badName)
    },
  ],
  ['POST', /^\/api\/team\/invites$/, (r, state) => teamInvite(r.body, state)],
  [
    'PUT',
    /^\/api\/team\/invites\/([^/]+)$/,
    (r) => {
      if (!inviteId.test(r.params[0] ?? '')) return notFound('No such invite link.')
      const why = grantProblem(r.body, false)
      return why ? invalid(why) : { status: 200, body: { id: r.params[0], kind: 'member', ...(r.body as object), maxUses: 1, uses: 0, status: 'active', usesLeft: 1, canEdit: true } }
    },
  ],
  ['DELETE', /^\/api\/team\/invites\/([^/]+)$/, (r) => (inviteId.test(r.params[0] ?? '') ? noContent : notFound('No such invite link.'))],
  [
    'PUT',
    /^\/api\/team\/members\/(\d+)$/,
    (r) => {
      const why = grantProblem(r.body, false)
      return why ? invalid(why) : { status: 200, body: { id: Number(r.params[0]), ...(r.body as object), owner: false, you: false, canEdit: true } }
    },
  ],
  ['DELETE', /^\/api\/team\/members\/(\d+)$/, () => noContent],
  ['POST', /^\/api\/team\/members\/(\d+)\/confirm-admin$/, (r) => ({ status: 200, body: { id: Number(r.params[0]), role: 'admin', owner: false, you: false, twoFactor: true, canEdit: true, waiting: false } })],
  [
    'POST',
    /^\/api\/discord\/connect$/,
    ({ body }, state) => {
      const why = webhookProblem((body as { webhookUrl?: unknown } | null)?.webhookUrl)
      return why ? invalid(why) : { status: 200, body: { ...state.discord, connected: true, webhookName: 'Server alerts', connectedAt: new Date().toISOString(), delivery: {} } }
    },
  ],
  ['PUT', /^\/api\/discord$/, (r, state) => discordAlerts(r.body, state)],
  ['DELETE', /^\/api\/discord$/, () => noContent],
  ['POST', /^\/api\/discord\/test$/, (_r, state) => ({ status: 200, body: { ...state.discord, delivery: { sent: new Date().toISOString() } } })],
]

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
  const origin = new URL(baseURL).origin
  const state: FakeState = { origin, prefs: {}, backups: new Map(), update: {}, discord: { connected: false, alerts: [], liveStatus: true, delivery: {}, kinds: [] }, opSeq: 0, inviteSeq: 0 }

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
      calls.push({ method, path, status: res.status(), faked: false, at })
      if (res.ok()) {
        if (path === '/api/me/prefs') Object.assign(state.prefs, await res.json().catch(() => ({})))
        const m = /^\/api\/servers\/(\w+)\/backups$/.exec(path)
        if (m?.[1]) state.backups.set(m[1], await res.json().catch(() => []))
        if (/^\/api\/machines\/\w+\/update$/.test(path)) state.update = await res.json().catch(() => ({}))
        if (path === '/api/discord') state.discord = await res.json().catch(() => state.discord)
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
