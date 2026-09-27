import type { Page, Request, Route } from '@playwright/test'
import { addonKeyProblem, answerRead, installJob, isAddonRead, recordedFolder, updateJob, type Answer, type JobStart, type World } from './addon-fixtures'
import { answerModpackRead, isModpackRead, type PackWorld } from './modpack-fixtures'

// Realistic stand-ins for every API call that changes something, so the
// click-through can press every button without restarting, deleting or
// downloading anything. Reads go to the real panel, except a server's
// add-ons and a machine's modpacks: those are answered from recorded fixtures
// (addon-fixtures.ts, modpack-fixtures.ts) and never reach it. Each fake
// checks the request the way the panel does (CSRF and origin headers, body
// shape, the preference key rule) and answers with the shape the real
// handler returns.

export interface ApiCall {
  method: string
  path: string
  status: number
  faked: boolean
  error?: string
  /** An error the dashboard expects: a fake's on purpose, like a wrong password, or an icon it shows a stand-in for. */
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

/** A fake's answer, or undefined when it has none, which is reported like a write without a fake. */
type Handler = (req: { method: string; path: string; url: URL; body: unknown; params: string[] }, state: FakeState) => Reply | undefined

interface FakeState {
  origin: string
  prefs: Record<string, string>
  backups: Map<string, Record<string, unknown>[]>
  update: Record<string, unknown>
  /** The last answer the page got to each read of a server's add-ons, packs and pre-generation, and of a machine's add-on sources, by path. */
  reads: Map<string, Record<string, unknown>>
  discord: Record<string, unknown>
  opSeq: number
  inviteSeq: number
  /** Each machine's address as the real panel last showed it; address changes answer with it. */
  addresses: Map<string, Record<string, unknown>>
  /** What the page's reads show (see View). */
  view: () => View
  /** Each server's phase in the last list of servers. */
  phases: Map<string, string>
  /** How each faked add-on job ended, by operation id. */
  jobs: Map<string, Record<string, unknown>>
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
const worldCopyName = /^data\.(replaced|failed-restore)-[0-9]{8}-[0-9]{6}$/

function invalid(error: string): Reply {
  return { status: 400, body: { error, code: 'invalid' } }
}

function refuse(status: number, code: string, error: string, hint?: string): Reply {
  return { status, body: { error, code, hint } }
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

const freeName = /^(?=.{3,32}$)[a-z0-9]+(-[a-z0-9]+)*$/
const recoveryCodes = ['k7qm-4tzd-9hxw-2rbn', 'p3vc-8jwa-6fke-5msy', 'x2nd-7gqr-4bzh-9tce', 'm9wf-3kpa-8vrn-6dqj', 'c4ht-9xme-2qwz-7bnk', 'r6ya-5dkq-3pjw-8fmx', 'v8bn-2tce-7hqk-4wzr', 'e5jx-6mra-9dvf-3kpt', 'h3wq-8zcn-5tbm-2yja', 'z7kp-4fve-6xrd-9qhm']

function twoFactorSetup(): Record<string, unknown> {
  const secret = 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'
  return {
    qrCodeSvg: '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 21 21" shape-rendering="crispEdges"><path d="M0 0h7v7H0zM14 0h7v7h-7zM0 14h7v7H0zM9 9h3v3H9z" fill="#111"/></svg>',
    manualKey: secret.replace(/(.{4})(?=.)/g, '$1 '),
    uri: `otpauth://totp/Playkeeper:admin?secret=${secret}&issuer=Playkeeper&algorithm=SHA1&digits=6&period=30`,
    issuer: 'Playkeeper',
    account: 'admin',
    expiresAt: new Date(Date.now() + 10 * 60_000).toISOString(),
  }
}

/** The names service's answer for a free name, without asking it: every well-formed name is free. */
function nameAvailability(name: string, address: Record<string, unknown> | undefined): Reply {
  if (!freeName.test(name)) return { ...invalid('Use a–z, 0–9 and single dashes, like alex-mc.'), expected: true }
  return { status: 200, body: { name, address: `${name}.${String(address?.base ?? 'playkeeper.io')}`, available: true } }
}

function addressAnswer(state: FakeState, machineId: string): Reply {
  return { status: 200, body: state.addresses.get(machineId) ?? {} }
}

const routes: [string, RegExp, Handler][] = [
  ['POST', /^\/api\/auth\/login$/, () => ({ status: 401, body: { error: 'Wrong username or password.', code: 'unauthorized' }, expected: true })],
  ['POST', /^\/api\/setup$/, () => ({ status: 403, body: { error: 'That setup code is not correct.', code: 'forbidden' }, expected: true })],
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
  ['DELETE', /^\/api\/servers\/(\w+)\/world-copies\/([^/]+)$/, (r) => (worldCopyName.test(decodeURIComponent(r.params[1] ?? '')) ? { status: 204, raw: '' } : invalid('Invalid world copy name.'))],
  // Wave 1: the World tab's pre-generation and packs, and the Plugins and Mods tabs.
  [
    'POST',
    /^\/api\/servers\/(\w+)\/pregen\/start$/,
    (r, state) => {
      const preset = (r.body as { preset?: unknown } | null)?.preset
      if (typeof preset !== 'string' || !['small', 'medium', 'large', 'huge'].includes(preset)) return invalid('Choose one of the sizes: small, medium, large or huge.')
      if (pregenUnfinished(lastRead(state, r.params[0], 'pregen'))) return refuse(409, 'already_running', 'The map is already being pre-generated.', 'Wait for it to finish, or cancel it first.')
      return op(state, 'pregen-start', r.params[0])
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/pregen\/(pause|continue|cancel)$/,
    (r, state) => {
      const pg = lastRead(state, r.params[0], 'pregen')
      if (!pregenUnfinished(pg)) return refuse(409, 'not_running', 'The map is not being pre-generated.', 'Start pre-generating it first.')
      const next = { pause: 'paused', continue: 'running', cancel: 'idle' }[r.params[1] as 'pause' | 'continue' | 'cancel']
      return { status: 200, body: { ...pg, state: next, pausedBy: next === 'paused' ? 'user' : undefined } }
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/resourcepack$/,
    (r, state) => {
      if (!isZip(r.body)) return invalid('The file isn’t a zip file.')
      const rp = lastRead(state, r.params[0], 'resourcepack')
      const prev = rp.offer as { required?: boolean; prompt?: string } | undefined
      const sha1 = '0'.repeat(40)
      const fileName = (r.url.searchParams.get('name') ?? '').replace(/^.*[\\/]/s, '').trim().slice(0, 100) || 'resource-pack.zip'
      const offer = { sha1, fileName, size: r.body.length, addedAt: new Date().toISOString(), url: `http://${r.url.host}/resource-packs/${sha1}.zip`, required: prev?.required ?? false, prompt: prev?.prompt }
      return { status: 200, body: { pending: false, ...rp, offer } }
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/resourcepack\/settings$/,
    (r, state) => {
      const { required = false, prompt = '' } = (r.body ?? {}) as { required?: unknown; prompt?: unknown }
      if (typeof required !== 'boolean' || typeof prompt !== 'string') return invalid('Invalid request body.')
      const rp = lastRead(state, r.params[0], 'resourcepack')
      const offer = rp.offer as Record<string, unknown> | undefined
      if (!offer) return refuse(409, 'no_resource_pack', 'The server doesn’t offer a resource pack.', 'Upload one first.')
      if (!/^[^%\\\p{Cc}\u2028\u2029]{0,200}$/u.test(prompt.trim())) return invalid('The message shown to players must be one line of at most 200 characters, without percent signs or backslashes.')
      return { status: 200, body: { ...rp, offer: { ...offer, required, prompt: prompt.trim() || undefined } } }
    },
  ],
  ['DELETE', /^\/api\/servers\/(\w+)\/resourcepack$/, (r, state) => ({ status: 200, body: { pending: false, ...lastRead(state, r.params[0], 'resourcepack'), offer: undefined } })],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/datapacks$/,
    (r, state) => {
      if (!isZip(r.body)) return invalid('The file isn’t a zip file.')
      const dp = lastRead(state, r.params[0], 'datapacks')
      const added = dataPackName(r.url.searchParams.get('name') ?? '')
      const packs = ((dp.packs ?? []) as Record<string, unknown>[]).filter((p) => p.name !== added)
      packs.push({ name: added, size: r.body.length, enabled: dp.live ? true : undefined, addedAt: new Date().toISOString() })
      return { status: 200, body: { live: false, ...dp, packs, added } }
    },
  ],
  ['POST', /^\/api\/servers\/(\w+)\/datapacks\/([^/]+)\/(enable|disable)$/, (r, state) => changeDataPack(state, r.params, (p) => [{ ...p, enabled: r.params[2] === 'enable' }])],
  ['DELETE', /^\/api\/servers\/(\w+)\/datapacks\/([^/]+)$/, (r, state) => changeDataPack(state, r.params, () => [])],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/addons\/remove$/,
    (r, state) => {
      const orphans: unknown = (r.body as { orphans?: unknown } | null)?.orphans ?? []
      if (!Array.isArray(orphans)) return invalid('Invalid request body.')
      const problem = addonKeyProblem(r.body) ?? (orphans.length > 200 ? 'Too many add-ons to remove at once.' : orphans.map(addonKeyProblem).find(Boolean))
      if (problem) return invalid(problem)
      const managed = managedAddons(state, r.params[0])
      const name = (k: unknown) => managed.find((a) => sameAddon(a, k))?.name
      const target = name(r.body)
      if (!target) return notManaged
      if (!orphans.every(name)) return invalid(`Only add-ons that nothing else needs can be removed along with ${target}.`)
      return { status: 200, body: { removed: [r.body, ...orphans].map(name), warnings: [] } }
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/addons\/adopt$/,
    (r, state) => {
      const file = (r.body as { fileName?: unknown } | null)?.fileName
      if (typeof file !== 'string' || !/^[^/\\]*\.jar$/.test(file) || new TextEncoder().encode(file).length > 255) return invalid('That is not a file in the add-on folder.')
      const identified = (lastRead(state, r.params[0], 'addons/checks').identified ?? []) as { fileName: string; addon?: AddonRecord }[]
      const rec = identified.find((f) => f.fileName === file)?.addon
      if (!rec) return refuse(409, 'conflict', `Modrinth does not recognize ${file}, so Playkeeper cannot manage it.`, 'It stays in the folder as it is.')
      if (managedAddons(state, r.params[0]).some((a) => sameAddon(a, rec))) return refuse(409, 'conflict', `${rec.name} is already managed by Playkeeper as another file.`, 'Remove one of the two copies first.')
      return { status: 200, body: rec }
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/addons\/forget$/,
    (r, state) => {
      const problem = addonKeyProblem(r.body)
      if (problem) return invalid(problem)
      const rec = managedAddons(state, r.params[0]).find((a) => sameAddon(a, r.body))
      if (!rec) return notManaged
      if (!rec.gone) return refuse(409, 'conflict', `${rec.fileName} is still in the folder.`, 'Remove the add-on instead.')
      return { status: 200, body: { removed: [rec.name], warnings: [] } }
    },
  ],
  ['POST', /^\/api\/servers\/(\w+)\/addons\/update$/, (r, state) => addonJob(state, 'addon-update', r.params[0], updateJob(r.body, addonWorld(state, r.params[0])))],
  ['POST', /^\/api\/servers\/(\w+)\/addons\/install$/, (r, state) => addonJob(state, 'addon-install', r.params[0], installJob(r.body, addonWorld(state, r.params[0])))],
  // Wave 2: the second sign-in step, two-factor sign-in and the machine's address.
  ['POST', /^\/api\/auth\/second-factor$/, () => ({ status: 401, body: { error: 'That code didn’t work. Try the one showing now.', code: 'code_wrong' }, expected: true })],
  ['POST', /^\/api\/auth\/second-factor\/cancel$/, () => ({ status: 200, body: {} })],
  ['POST', /^\/api\/auth\/2fa\/setup$/, ({ body }) => ((body as { password?: unknown } | null)?.password ? { status: 200, body: twoFactorSetup() } : invalid('Enter your password.'))],
  ['DELETE', /^\/api\/auth\/2fa\/setup$/, () => ({ status: 200, body: {} })],
  ['POST', /^\/api\/auth\/2fa\/confirm$/, ({ body }) => (/^\d{6}$/.test(String((body as { code?: unknown } | null)?.code)) ? { status: 200, body: { recoveryCodes } } : invalid('Type the 6-digit code from your app.'))],
  [
    'POST',
    /^\/api\/auth\/2fa\/(recovery-codes|disable)$/,
    (r) => {
      const b = r.body as { password?: unknown; code?: unknown } | null
      if (!b?.password || !b.code) return invalid('Enter your password and a code.')
      return { status: 200, body: r.params[0] === 'disable' ? {} : { recoveryCodes } }
    },
  ],
  ['POST', /^\/api\/machines\/(\w+)\/address\/claim$/, (r, state) => (freeName.test(String((r.body as { name?: unknown } | null)?.name)) ? addressAnswer(state, r.params[0] ?? '') : invalid('Use a–z, 0–9 and single dashes, like alex-mc.'))],
  ['POST', /^\/api\/machines\/(\w+)\/address\/(refresh|release|certificate)$/, (r, state) => addressAnswer(state, r.params[0] ?? '')],
  ['POST', /^\/api\/machines\/(\w+)\/address\/check$/, (r, state) => ((r.body as { domain?: unknown } | null)?.domain ? addressAnswer(state, r.params[0] ?? '') : invalid('Type your domain.'))],
  ['DELETE', /^\/api\/machines\/(\w+)\/address$/, (r, state) => addressAnswer(state, r.params[0] ?? '')],
  // Wave 3: the crash screen's fixes that act on a plugin or mod, then start
  // the server. Its update and install with `start` are Wave 1's entries.
  ['POST', /^\/api\/servers\/(\w+)\/addons\/remove-file$/, (r, state) => (addonJar.test(String((r.body as { jar?: unknown } | null)?.jar ?? '')) ? op(state, 'remove-addon', r.params[0]) : invalid('That is not the name of a plugin or mod file.'))],
  // Wave 4: reinstalling changed software, trying a template's skipped add-ons again, and the friends' pack switch.
  ['POST', /^\/api\/servers\/(\w+)\/software\/reinstall$/, (r, state) => op(state, 'reinstall', r.params[0])],
  // Wave 4: the CurseForge key. A key typed here isn't one CurseForge knows, so it's refused as the real check would.
  ['POST', /^\/api\/machines\/(\w+)\/addon-sources\/curseforge$/, () => ({ status: 400, body: { error: "That key didn't work. Copy it again from console.curseforge.com.", code: 'curseforge_key_refused' }, expected: true })],
  [
    'DELETE',
    /^\/api\/machines\/(\w+)\/addon-sources\/curseforge$/,
    (r, state) => {
      const sources = { curseforge: { key: 'none' } }
      state.reads.set(`/api/machines/${r.params[0]}/addon-sources`, sources)
      return { status: 200, body: sources }
    },
  ],
  ['POST', /^\/api\/servers\/(\w+)\/template\/retry$/, (r, state) => op(state, 'template-retry', r.params[0])],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/mods\/share$/,
    ({ body }) => {
      const on = (body as { public?: unknown } | null)?.public
      if (typeof on !== 'boolean') return invalid('Say whether to share the pack.')
      return { status: 200, body: { public: on, token: on ? 'Fake0Share0Token0Abcde' : undefined, file: 'server.mrpack', size: 2048, loaderName: 'Fabric', share: { server: 'Server', type: 'fabric', minecraftVersion: '26.2', loaderVersion: '0.19.3', notice: { key: 'share.notice.none', text: 'Friends can join without mods' }, mods: [] } } }
    },
  ],
]

interface AddonRecord {
  source: string
  projectId: string
  name: string
  fileName: string
  gone?: boolean
}

const notManaged = refuse(404, 'not_managed', 'Playkeeper did not install this add-on, so it cannot manage it.', 'Scan the folder to let Playkeeper identify files added by hand.')

/** What the page last got for a read of the server's add-ons, packs or pre-generation. */
function lastRead(state: FakeState, serverId: string | undefined, what: string): Record<string, unknown> {
  return state.reads.get(`/api/servers/${serverId}/${what}`) ?? {}
}

function pregenUnfinished(pg: Record<string, unknown>): boolean {
  return pg.state === 'starting' || pg.state === 'running' || pg.state === 'paused'
}

/** Zip files, even empty ones, start with "PK". */
function isZip(body: unknown): body is Uint8Array {
  return body instanceof Uint8Array && body[0] === 0x50 && body[1] === 0x4b
}

/** The name the agent gives an uploaded data pack (packs.SafeName). */
function dataPackName(upload: string): string {
  const base = upload.replace(/^.*[\\/]/s, '').replace(/\.zip$/i, '')
  const name = base.replace(/^[^A-Za-z0-9_+]+/, '').replace(/[^A-Za-z0-9_.+-]+$/, '').replace(/[^A-Za-z0-9_.+-]+/g, '_').slice(0, 96).replace(/\.+$/, '')
  return `${name || 'datapack'}.zip`
}

/** Switches a data pack on or off, or removes it, in the last list of the server's packs: change says what the pack becomes. */
function changeDataPack(state: FakeState, [serverId, encoded]: string[], change: (p: Record<string, unknown>) => Record<string, unknown>[]): Reply {
  const dp = lastRead(state, serverId, 'datapacks')
  const name = decodeURIComponent(encoded ?? '')
  const packs = (dp.packs ?? []) as Record<string, unknown>[]
  if (!packs.some((p) => p.name === name)) return refuse(404, 'not_found', `No data pack named "${name}" is installed.`)
  return { status: 200, body: { ...dp, packs: packs.flatMap((p) => (p.name === name ? change(p) : [p])) } }
}

/** The add-ons Playkeeper manages on the server, from the last list of its folder; gone ones have lost their file. */
function managedAddons(state: FakeState, serverId: string | undefined): AddonRecord[] {
  const { files = [], missing = [] } = lastRead(state, serverId, 'addons') as { files?: { status: string; addon?: AddonRecord }[]; missing?: AddonRecord[] }
  return [...files.flatMap((f) => (f.addon && (f.status === 'managed' || f.status === 'modified') ? [f.addon] : [])), ...missing.map((a) => ({ ...a, gone: true }))]
}

function sameAddon(a: AddonRecord, k: unknown): boolean {
  const { source, projectId } = (k ?? {}) as { source?: unknown; projectId?: unknown }
  return a.source === source && a.projectId === projectId
}

const addonJar = /^[^./\\][^/\\]{0,195}\.jar$/

/** The phases in which a server's container runs (web/src/lib/phase.ts), so a finished add-on job asks for a restart. */
const running = ['online', 'starting', 'starting_container', 'preparing_world', 'downloading_server', 'stopping']

/** The server as the add-on fixtures see it: its folder as the Plugins tab last showed it, and whether it runs. */
function addonWorld(state: FakeState, serverId: string | undefined): World {
  const path = `/api/servers/${serverId}/addons`
  const folder = recordedFolder()
  const addons = state.reads.get(path) ?? ((lay(state.view(), path, folder) as Record<string, unknown> | undefined) ?? folder)
  return { addons, running: running.includes(state.phases.get(serverId ?? '') ?? '') }
}

/**
 * The machine as the modpack fixtures see it: it offers CurseForge when
 * Settings › Add-on sources last showed a key. Dev and CI builds carry none
 * (curseforge.BuildKey), so until the page reads the sources it has none.
 */
function packWorld(state: FakeState, machineId: string | undefined): PackWorld {
  const key = (state.reads.get(`/api/machines/${machineId}/addon-sources`)?.curseforge as { key?: unknown } | undefined)?.key
  return { curseforge: key === 'build' || key === 'file' }
}

/** Which recorded fixtures answer a read, if any: a server's add-ons or a machine's modpacks. */
export function fixtureRead(method: string, path: string): 'add-on' | 'modpack' | undefined {
  if (isAddonRead(method, path)) return 'add-on'
  if (isModpackRead(method, path)) return 'modpack'
  return undefined
}

/** Starts a faked add-on job that ends the way the fixtures say, or refuses it; undefined when they have no answer. */
function addonJob(state: FakeState, kind: string, serverId: string | undefined, start: JobStart | undefined): Reply | undefined {
  if (!start) return undefined
  if ('refused' in start) return { status: start.refused.status, body: start.refused.body, headers: start.refused.headers }
  const started = op(state, kind, serverId)
  const begun = started.body as Record<string, unknown>
  state.jobs.set(String(begun.id), { ...begun, ...start.ends, finishedAt: new Date().toISOString() })
  return started
}

/** A world a restore left behind. A fresh install has none, so the World tab's notice and its Discard button would never show. */
const leftoverWorld = { name: 'data.replaced-20260924-090000', kind: 'previous', createdAt: '2026-09-24T09:00:00Z', sizeBytes: 1_100_000_000 }

/**
 * States a fresh install isn't in, laid over the panel's real answers so the
 * click-through reaches the controls only they show: its servers stopped,
 * crashed (out of memory, as the agent reports it) or busy with a backup;
 * no players, sessions or backups; no servers at all; a newer Playkeeper to
 * update to; or a panel that still needs its admin account.
 */
export type View = 'live' | 'stopped' | 'crashed' | 'busy' | 'empty lists' | 'no servers' | 'update available' | 'first run'

type Json = Record<string, unknown>

const ago = (seconds: number) => new Date(Date.now() - seconds * 1000).toISOString()

function stopped(s: Json): Json {
  return { ...s, desired: 'stopped', phase: 'stopped', phaseDetail: undefined, reachable: false, reachableAt: undefined, startedAt: undefined, stoppedAt: ago(20 * 60), players: undefined, resources: undefined, operation: undefined, pendingRestart: false }
}

/** Memory budgets the dashboard offers, for the next one up. */
const budgets = [1024, 1536, 2048, 3072, 4096, 6144, 8192, 12288, 16384]

/**
 * The agent's diagnosis of a server whose Java ran out of heap, in the shape
 * the agent sends it (diagnose's javaMemory): the next budget up is offered
 * first, so the crash card's fix is "Save and start".
 */
function heapCrash(s: Json): Json {
  const budget = Number((s.config as Json | undefined)?.memoryMB) || 1536
  const next = budgets.find((b) => b > budget) ?? budget + 2048
  const room = next - budget + 1024
  const line = 'java.lang.OutOfMemoryError: Java heap space'
  return {
    at: ago(90),
    start: false,
    kind: 'heap_out_of_memory',
    params: { budget_mb: budget, heap_mb: budget - Math.max(Math.floor(budget / 4), 512) },
    certain: true,
    title: 'Your Paper server ran out of memory',
    explanation: 'Java ran out of the memory it has for the game (the heap) and couldn’t continue.',
    evidence: [
      { kind: 'log_line', params: { line }, text: line },
      { kind: 'memory_room', params: { budget_mb: budget, room_mb: room }, text: 'This machine could give the server more memory.' },
    ],
    fixes: [
      { kind: 'raise_memory', params: { from_mb: budget, to_mb: next }, title: 'Give it more memory', recommended: true },
      { kind: 'restart', title: 'Start the server again' },
    ],
    lines: [{ time: '12:04:10', level: 'ERROR', text: line }],
    roomMB: room,
  }
}

function server(view: View, s: Json): Json {
  switch (view) {
    case 'stopped':
      return stopped(s)
    case 'crashed':
      return { ...stopped(s), desired: 'running', phase: 'crashed', stoppedAt: ago(90), exitCode: 137, crashCount: 3, lastError: 'Java ran out of memory.', lastErrorHint: 'Choose a larger memory budget in Settings.', crash: heapCrash(s) }
    case 'busy':
      return { ...s, operation: { id: 'fake-op-busy', serverId: s.id, kind: 'backup', status: 'running', phase: 'copying', actor: 'admin', startedAt: ago(20) } }
    case 'empty lists':
      return { ...s, players: s.players ? { ...(s.players as Json), online: 0, names: [] } : undefined }
    case 'live':
    case 'no servers':
    case 'update available':
    case 'first run':
      return s
    default: {
      const unreachable: never = view
      return unreachable
    }
  }
}

/** The version the 'update available' view offers. */
export const newerRelease = '0.3.2'

/** A read's answer in `view`, or undefined when the view leaves it as the panel sent it. */
function lay(view: View, path: string, body: unknown): unknown {
  if (view === 'live' || body === undefined) return undefined
  if (view === 'first run') return path === '/api/setup/status' ? { needsSetup: true } : undefined
  if (path === '/api/servers' && Array.isArray(body)) return view === 'no servers' ? [] : body.map((s) => server(view, s as Json))
  if (view === 'empty lists') {
    if (/^\/api\/servers\/\w+\/(backups|whitelist|operators|activity)$/.test(path)) return []
    if (/^\/api\/servers\/\w+\/players\/sessions$/.test(path)) return { ...(body as Json), sessions: [] }
    if (/^\/api\/servers\/\w+\/players\/summary$/.test(path)) {
      const b = body as Json & { days?: Json[] }
      return { ...b, players: [], observedSessions: 0, uncertainSessions: 0, days: (b.days ?? []).map((d) => ({ ...d, uniquePlayers: 0, sessions: 0, playtimeSeconds: 0 })) }
    }
  }
  if (view === 'update available') {
    if (path === '/api/machines' && Array.isArray(body)) return body.map((m: Json) => (m.live ? { ...m, live: { ...(m.live as Json), updateAvailable: newerRelease } } : m))
    if (/^\/api\/machines\/\w+\/update$/.test(path)) return { ...(body as Json), supported: true, available: true, latest: newerRelease, notes: '- Backups finish sooner\n- The phone’s Settings tab has a heading again', releaseDate: ago(2 * 86_400) }
  }
  return undefined
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

/** Writes that only work out what another write would do and change nothing, so they're answered like reads. */
const plans = [
  // Wave 1: what updating plugins or mods would do, from the recorded fixtures.
  /^\/api\/servers\/\w+\/addons\/update\/plan$/,
  // Wave 4: what a template would create.
  /^\/api\/machines\/\w+\/templates\/plan$/,
]

/** The panel's refusal of a write without its CSRF headers; signing in and setting up come before there's a token. */
function csrfRefusal(path: string, headers: Record<string, string>): Reply | undefined {
  if (headers['x-requested-with'] === 'playkeeper' && (path === '/api/auth/login' || path === '/api/setup' || headers['x-csrf-token'])) return undefined
  return { status: 403, body: { error: 'Security token missing or invalid. Reload the page and try again.', code: 'forbidden' } }
}

/** A POST's body as the panel reads it: none, JSON, or text it refuses. */
function posted(request: Request): unknown {
  const raw = request.postData() ?? ''
  if (raw === '') return undefined
  try {
    return JSON.parse(raw)
  } catch {
    return raw
  }
}

function notePhases(state: FakeState, servers: unknown) {
  if (!Array.isArray(servers)) return
  for (const s of servers as { id?: unknown; phase?: unknown }[]) if (typeof s.id === 'string') state.phases.set(s.id, String(s.phase ?? ''))
}

/**
 * Serves the fakes on a page. The returned list collects every API call with
 * its status; unknown writes answer 501 and are listed as unfaked so a new
 * endpoint can't slip through unchecked, and add-on and modpack reads the
 * fixtures have no answer for do the same and are listed as unrecorded.
 * Reads show `view()` (see View).
 */
export async function installFakes(page: Page, baseURL: string, view: () => View = () => 'live'): Promise<{ calls: ApiCall[]; unfaked: string[]; unrecorded: string[] }> {
  const calls: ApiCall[] = []
  const unfaked: string[] = []
  const unrecorded: string[] = []
  const origin = new URL(baseURL).origin
  const state: FakeState = {
    prefs: {},
    backups: new Map(),
    update: {},
    reads: new Map(),
    opSeq: 0,
    addresses: new Map(),
    view,
    phases: new Map(),
    jobs: new Map(),
    origin,
    discord: { connected: false, alerts: [], liveStatus: true, delivery: {}, kinds: [] },
    inviteSeq: 0,
  }

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
    // Planning changes nothing, so it's answered like a read once it passes the panel's CSRF check.
    const planning = method === 'POST' && plans.some((re) => re.test(path)) && !csrfRefusal(path, request.headers())
    if (method === 'GET' || method === 'HEAD' || planning) {
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
      // A faked job finishes at once, for the dialogs that follow it; an add-on job ends the way the fixtures say.
      const fakeOp = /^\/api\/machines\/\w+\/operations\/(fake-op-\d+)$/.exec(path)
      if (fakeOp?.[1]) {
        calls.push({ method, path, status: 200, faked: true, at })
        const done = new Date().toISOString()
        const ended = state.jobs.get(fakeOp[1]) ?? { id: fakeOp[1], kind: 'addon-install', status: 'succeeded', phase: '', actor: 'admin', startedAt: done, finishedAt: done, detail: { files: [] } }
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(ended) })
        return
      }
      if (/^\/api\/servers\/\w+\/world-copies$/.test(path)) {
        calls.push({ method, path, status: 200, faked: true, at })
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([leftoverWorld]) })
        return
      }
      // Asking whether a free name is taken goes out to the names service, which tests never call.
      const lookup = /^\/api\/machines\/(\w+)\/address\/available$/.exec(path)
      if (lookup?.[1]) {
        const reply = nameAvailability(url.searchParams.get('name') ?? '', state.addresses.get(lookup[1]))
        calls.push({ method, path, status: reply.status, faked: true, expected: reply.expected, at })
        await route.fulfill({ status: reply.status, contentType: 'application/json', body: JSON.stringify(reply.body) })
        return
      }
      const kind = fixtureRead(method, path)
      if (kind) {
        let answer: Answer | undefined
        if (kind === 'add-on') answer = answerRead(method, url, planning ? posted(request) : undefined, addonWorld(state, /^\/api\/servers\/(\w+)\//.exec(path)?.[1]))
        else answer = answerModpackRead(url, packWorld(state, /^\/api\/machines\/(\w+)\//.exec(path)?.[1]))
        if (!answer) {
          const error = `No recorded answer for ${method} ${path}.`
          unrecorded.push(`${method} ${path}`)
          calls.push({ method, path, status: 501, faked: true, error, at })
          await route.fulfill({ status: 501, contentType: 'application/json', body: JSON.stringify({ error, code: 'internal' }) }).catch(() => {})
          return
        }
        const image = Buffer.isBuffer(answer.body)
        const body = image || answer.status !== 200 ? answer.body : (lay(view(), path, answer.body) ?? answer.body)
        if (answer.status === 200 && /^\/api\/servers\/\w+\/addons(\/checks)?$/.test(path)) state.reads.set(path, body as Record<string, unknown>)
        const error = answer.status >= 400 ? String((body as { error?: string }).error ?? '') : undefined
        // An icon the proxy refuses shows a stand-in, so its error is expected.
        const icon = /^\/api\/(servers|machines)\/\w+\/(addons|modpacks)\/icon$/.test(path)
        calls.push({ method, path, status: answer.status, faked: true, error, expected: (icon && answer.status >= 400) || undefined, at })
        await route.fulfill({ status: answer.status, headers: answer.headers, body: image ? (body as Buffer) : JSON.stringify(body) }).catch(() => {})
        return
      }
      const res = await route.fetch().catch(() => null)
      if (!res) {
        await route.abort().catch(() => {})
        return
      }
      const laid = res.ok() ? lay(view(), path, await res.json().catch(() => undefined)) : undefined
      if (laid !== undefined) {
        calls.push({ method, path, status: res.status(), faked: true, at })
        const b = /^\/api\/servers\/(\w+)\/backups$/.exec(path)
        if (b?.[1]) state.backups.set(b[1], laid as Record<string, unknown>[])
        if (/^\/api\/servers\/\w+\/(addons(\/checks)?|datapacks|resourcepack|pregen)$/.test(path)) state.reads.set(path, laid as Record<string, unknown>)
        if (/^\/api\/machines\/\w+\/update$/.test(path)) state.update = laid as Record<string, unknown>
        if (path === '/api/servers') notePhases(state, laid)
        await route.fulfill({ response: res, json: laid }).catch(() => {})
        return
      }
      // The records for a domain that isn't one are refused, like a wrong password.
      const refusedDomain = res.status() === 400 && /^\/api\/machines\/\w+\/address\/plan$/.test(path)
      calls.push({ method, path, status: res.status(), faked: false, expected: refusedDomain || undefined, at })
      if (res.ok()) {
        if (path === '/api/me/prefs') Object.assign(state.prefs, await res.json().catch(() => ({})))
        const m = /^\/api\/servers\/(\w+)\/backups$/.exec(path)
        if (m?.[1]) state.backups.set(m[1], await res.json().catch(() => []))
        if (/^\/api\/servers\/\w+\/(datapacks|resourcepack|pregen)$|^\/api\/machines\/\w+\/addon-sources$/.test(path)) state.reads.set(path, await res.json().catch(() => ({})))
        if (/^\/api\/machines\/\w+\/update$/.test(path)) state.update = await res.json().catch(() => ({}))
        if (path === '/api/discord') state.discord = await res.json().catch(() => state.discord)
        const address = /^\/api\/machines\/(\w+)\/address$/.exec(path)
        if (address?.[1]) state.addresses.set(address[1], await res.json().catch(() => ({})))
        if (path === '/api/servers') notePhases(state, await res.json().catch(() => []))
      }
      // The page may have moved on and cancelled the request meanwhile.
      await route.fulfill({ response: res }).catch(() => {})
      return
    }
    const headers = request.headers()
    let reply = csrfRefusal(path, headers)
    if (!reply) {
      let body: unknown
      if ((headers['content-type'] ?? '').includes('json')) {
        try {
          body = request.postDataJSON()
        } catch {
          body = undefined
        }
      } else {
        // An upload's body is the file itself.
        body = request.postDataBuffer()
      }
      for (const [m, re, handle] of routes) {
        const hit = m === method ? re.exec(path) : null
        if (hit) {
          reply = handle({ method, path, url, body, params: hit.slice(1) }, state)
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
  return { calls, unfaked, unrecorded }
}
