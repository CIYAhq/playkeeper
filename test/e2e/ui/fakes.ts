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

type Handler = (req: { method: string; path: string; body: unknown; params: string[] }, state: FakeState) => Reply

interface DialAddress {
  kind: string
  address: string
}

interface AddonKey {
  source?: unknown
  projectId?: unknown
}

interface AddonFileView {
  fileName: string
  addon?: { source: string; projectId: string; name: string }
}

interface FakeState {
  prefs: Record<string, string>
  backups: Map<string, Record<string, unknown>[]>
  update: Record<string, unknown>
  opSeq: number
  /** The operations the fakes started, for the dialogs that follow one by its id. */
  ops: Map<string, Record<string, unknown>>
  /** Each server's add-on files as the panel last listed them, the ones its checks identified too. */
  addons: Map<string, AddonFileView[]>
  /** What GET /api/machines/link last said: the addresses a joining machine can dial and the dashboard's fingerprint. */
  link: { addresses?: DialAddress[]; fingerprint?: string }
  machines: { id: string; kind: string }[]
  seq: number
  /** Each machine's address as the real panel last showed it; address changes answer with it. */
  addresses: Map<string, Record<string, unknown>>
}

const name = /^[A-Za-z0-9_]{3,16}$/
const prefKey = /^[a-z][a-z0-9.:_-]{0,63}$/
const id = /^[a-z2-9]{10}$/
const worldCopyName = /^data\.(replaced|failed-restore)-[0-9]{8}-[0-9]{6}$/
const projectRef = /^[A-Za-z0-9._-]{1,64}$/
const planFingerprint = /^[0-9a-f]{32}$/
const maxAddonKeys = 200

function invalid(error: string): Reply {
  return { status: 400, body: { error, code: 'invalid' } }
}

function op(state: FakeState, kind: string, serverId?: string): Reply {
  state.opSeq++
  const body = { id: `fake-op-${state.opSeq}`, serverId, kind, status: 'running', phase: '', actor: 'admin', startedAt: new Date().toISOString() }
  state.ops.set(body.id, body)
  return { status: 202, body }
}

/** Why the agent would refuse an add-on's source and project id, or undefined. */
function badAddonKey(k: AddonKey | null | undefined): string | undefined {
  if (k?.source !== 'modrinth' && k?.source !== 'hangar') return 'Add-ons come from Modrinth or Hangar.'
  if (typeof k.projectId !== 'string' || !projectRef.test(k.projectId) || k.projectId === '.' || k.projectId === '..') return 'That is not a valid project id.'
  return undefined
}

/** Why the agent would refuse a list of add-ons, or undefined. */
function badAddonKeys(keys: unknown): string | undefined {
  if (keys === undefined) return undefined
  if (!Array.isArray(keys) || keys.length > maxAddonKeys) return `At most ${maxAddonKeys} add-ons can be changed at once.`
  for (const k of keys) {
    const bad = badAddonKey(k as AddonKey)
    if (bad) return bad
  }
  return undefined
}

function badFingerprint(body: unknown): string | undefined {
  const f = (body as { fingerprint?: unknown } | null)?.fingerprint
  return typeof f === 'string' && planFingerprint.test(f) ? undefined : "This request doesn't include the plan you confirmed."
}

/** An add-on's name as the panel last listed it on the server. */
function addonName(state: FakeState, serverId: string, k: AddonKey): string {
  const file = state.addons.get(serverId)?.find((f) => f.addon?.source === k.source && f.addon?.projectId === k.projectId)
  return file?.addon?.name ?? String(k.projectId)
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
  ['POST', /^\/api\/join-codes$/, ({ body }, state) => joinCodeReply(body, state)],
  ['DELETE', /^\/api\/join-codes\/([^/]+)$/, (r) => (id.test(r.params[0] ?? '') ? { status: 204 } : { status: 404, body: { error: 'Join code not found.', code: 'not_found' } })],
  [
    'DELETE',
    /^\/api\/machines\/([a-z2-9]{10})$/,
    (r, state) => (state.machines.find((m) => m.id === r.params[0])?.kind === 'local' ? invalid('This is the dashboard’s own machine, so it can’t be removed.') : { status: 204 }),
  ],
  ['DELETE', /^\/api\/servers\/(\w+)\/world-copies\/([^/]+)$/, (r) => (worldCopyName.test(decodeURIComponent(r.params[1] ?? '')) ? { status: 204, raw: '' } : invalid('Invalid world copy name.'))],
  // Wave 1: plugins and mods. Installs and updates carry the plan they confirm.
  [
    'POST',
    /^\/api\/servers\/(\w+)\/addons\/install$/,
    (r, state) => {
      const bad = badAddonKey(r.body as AddonKey) ?? badFingerprint(r.body)
      return bad ? invalid(bad) : op(state, 'addon-install', r.params[0])
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/addons\/update$/,
    (r, state) => {
      const bad = badAddonKeys((r.body as { addons?: unknown } | null)?.addons) ?? badFingerprint(r.body)
      return bad ? invalid(bad) : op(state, 'addon-update', r.params[0])
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/addons\/remove$/,
    (r, state) => {
      const b = r.body as (AddonKey & { orphans?: unknown }) | null
      const bad = badAddonKey(b) ?? badAddonKeys(b?.orphans)
      if (bad) return invalid(bad)
      const keys = [b as AddonKey, ...((b?.orphans as AddonKey[] | undefined) ?? [])]
      return { status: 200, body: { removed: keys.map((k) => addonName(state, r.params[0] ?? '', k)), warnings: [] } }
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/addons\/forget$/,
    (r, state) => {
      const bad = badAddonKey(r.body as AddonKey)
      return bad ? invalid(bad) : { status: 200, body: { removed: [addonName(state, r.params[0] ?? '', r.body as AddonKey)], warnings: [] } }
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/addons\/adopt$/,
    (r, state) => {
      const f = (r.body as { fileName?: unknown } | null)?.fileName
      if (typeof f !== 'string' || !f || f.length > 255 || /[/\\]/.test(f) || !f.endsWith('.jar')) return invalid('That is not a file in the add-on folder.')
      const addon = state.addons.get(r.params[0] ?? '')?.find((x) => x.fileName === f)?.addon
      return addon ? { status: 200, body: { ...addon, fileName: f, installedAt: new Date().toISOString() } } : invalid('Playkeeper doesn’t know which add-on this file is.')
    },
  ],
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
  // The crash screen's fixes that act on a plugin or mod, then start the server.
  ['POST', /^\/api\/servers\/(\w+)\/addons\/remove-file$/, (r, state) => (addonJar.test(String((r.body as { jar?: unknown } | null)?.jar ?? '')) ? op(state, 'remove-addon', r.params[0]) : invalid('That is not the name of a plugin or mod file.'))],
  // Wave 4: reinstalling changed software, trying a template's skipped add-ons again, and the friends' pack switch.
  ['POST', /^\/api\/servers\/(\w+)\/software\/reinstall$/, (r, state) => op(state, 'reinstall', r.params[0])],
  // Wave 4: the CurseForge key. A key typed here isn't one CurseForge knows, so it's refused as the real check would.
  ['POST', /^\/api\/machines\/(\w+)\/addon-sources\/curseforge$/, () => ({ status: 400, body: { error: "That key didn't work. Copy it again from console.curseforge.com.", code: 'curseforge_key_refused' }, expected: true })],
  ['DELETE', /^\/api\/machines\/(\w+)\/addon-sources\/curseforge$/, () => ({ status: 200, body: { curseforge: { key: 'none' } } })],
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

const addonJar = /^[^./\\][^/\\]{0,195}\.jar$/

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
  const state: FakeState = { prefs: {}, backups: new Map(), update: {}, opSeq: 0, ops: new Map(), addons: new Map(), link: {}, machines: [], seq: 0, addresses: new Map() }
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
    // Planning a template reads it and changes nothing, so the real panel answers.
    const planning = method === 'POST' && /^\/api\/machines\/\w+\/templates\/plan$/.test(path)
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
      // A faked job finishes at once, for the dialogs that follow it.
      const fakeOp = /^\/api\/machines\/\w+\/operations\/(fake-op-\d+)$/.exec(path)
      if (fakeOp?.[1]) {
        calls.push({ method, path, status: 200, faked: true, at })
        const done = new Date().toISOString()
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: fakeOp[1], kind: 'addon-install', status: 'succeeded', phase: '', actor: 'admin', startedAt: done, finishedAt: done, detail: { files: [] } }) })
        return
      }
      if (/^\/api\/servers\/\w+\/world-copies$/.test(path)) {
        calls.push({ method, path, status: 200, faked: true, at })
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([leftoverWorld]) })
        return
      }
      const fakeOp = /^\/api\/machines\/\w+\/operations\/(fake-op-\d+)$/.exec(path)
      if (fakeOp?.[1]) {
        const started = state.ops.get(fakeOp[1])
        const status = started ? 200 : 404
        calls.push({ method, path, status, faked: true, at })
        const body = started ? { ...started, status: 'succeeded', finishedAt: new Date().toISOString() } : { error: 'Operation not found.', code: 'not_found' }
        await route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
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
      const res = await route.fetch().catch(() => null)
      if (!res) {
        await route.abort().catch(() => {})
        return
      }
      // Add-on icons come from their sites through the panel, and one that
      // can't be had shows a stand-in, so its error is expected.
      const icon = /^\/api\/(servers|machines)\/\w+\/(addons|modpacks)\/icon$/.test(path)
      // The records for a domain that isn't one are refused, like a wrong password.
      const refusedDomain = res.status() === 400 && /^\/api\/machines\/\w+\/address\/plan$/.test(path)
      calls.push({ method, path, status: res.status(), faked: false, expected: (icon && !res.ok()) || refusedDomain || undefined, at })
      if (res.ok()) {
        if (path === '/api/me/prefs') Object.assign(state.prefs, await res.json().catch(() => ({})))
        const m = /^\/api\/servers\/(\w+)\/backups$/.exec(path)
        if (m?.[1]) state.backups.set(m[1], await res.json().catch(() => []))
        if (/^\/api\/machines\/\w+\/update$/.test(path)) state.update = await res.json().catch(() => ({}))
        if (path === '/api/machines/link') state.link = await res.json().catch(() => ({}))
        if (path === '/api/machines') state.machines = await res.json().catch(() => [])
        const addons = /^\/api\/servers\/(\w+)\/addons(?:\/checks)?$/.exec(path)
        if (addons?.[1]) {
          const got = (await res.json().catch(() => ({}))) as { files?: AddonFileView[]; identified?: AddonFileView[] }
          const files = new Map((state.addons.get(addons[1]) ?? []).map((f) => [f.fileName, f]))
          for (const f of [...(got.files ?? []), ...(got.identified ?? [])]) files.set(f.fileName, { ...files.get(f.fileName), ...f })
          state.addons.set(addons[1], [...files.values()])
        }
        const address = /^\/api\/machines\/(\w+)\/address$/.exec(path)
        if (address?.[1]) state.addresses.set(address[1], await res.json().catch(() => ({})))
      }
      // The page may have moved on and cancelled the request meanwhile.
      await route.fulfill({ response: res }).catch(() => {})
      return
    }
    // Planning an update changes nothing, so the real panel makes the plan, with its real fingerprint.
    if (method === 'POST' && /^\/api\/servers\/\w+\/addons\/update\/plan$/.test(path)) {
      const res = await route.fetch().catch(() => null)
      if (!res) {
        await route.abort().catch(() => {})
        return
      }
      calls.push({ method, path, status: res.status(), faked: false, at })
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
