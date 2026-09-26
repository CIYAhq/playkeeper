import type { APIResponse, Page, Request, Route } from '@playwright/test'

// Realistic stand-ins for every API call that changes something, so the
// click-through can press every button without restarting, deleting or
// downloading anything. Reads go to the real panel, and so do the POSTs that
// only work something out (a schedule's next runs, what backup rules would
// keep, a template's plan). Each fake checks the request the way the panel does (CSRF and origin
// headers, body shape, the preference key rule) and answers with the shape
// the real handler returns.

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

interface FakeState {
  prefs: Record<string, string>
  backups: Map<string, Record<string, unknown>[]>
  update: Record<string, unknown>
  opSeq: number
  /** Each machine's address as the real panel last showed it; address changes answer with it. */
  addresses: Map<string, Record<string, unknown>>
  ops: Map<string, Record<string, unknown>>
  schedules: Map<string, Record<string, unknown>[]>
  backupRules: Map<string, Record<string, unknown>>
  offsite: Map<string, Record<string, unknown>>
  /** Servers the fakes made an SSH key for. */
  sshKeys: Set<string>
}

const name = /^[A-Za-z0-9_]{3,16}$/
const prefKey = /^[a-z][a-z0-9.:_-]{0,63}$/
const worldCopyName = /^data\.(replaced|failed-restore)-[0-9]{8}-[0-9]{6}$/

function invalid(error: string): Reply {
  return { status: 400, body: { error, code: 'invalid' } }
}

function op(state: FakeState, kind: string, serverId?: string, detail?: Record<string, unknown>): Reply {
  state.opSeq++
  const o = { id: `fake-op-${state.opSeq}`, serverId, kind, status: 'running', phase: '', actor: 'admin', startedAt: new Date().toISOString(), detail }
  state.ops.set(o.id, o)
  return { status: 202, body: o }
}

/** A fake operation once it's done, as the operations endpoint reports it to a page that waits for it. */
function finished(o: Record<string, unknown>): Record<string, unknown> {
  const detail = { ...(o.detail as Record<string, unknown> | undefined) }
  if (o.kind === 'disk-cleanup') detail.freed = 734_003_200
  if (o.kind === 'offsite-recover') detail.restoreId = 'fakerestore'
  return { ...o, status: 'succeeded', finishedAt: new Date().toISOString(), detail }
}

function knownZone(z: unknown): boolean {
  if (typeof z !== 'string' || z.length > 64) return false
  try {
    new Intl.DateTimeFormat('en', { timeZone: z })
    return true
  } catch {
    return false
  }
}

/**
 * POSTs that only work something out and change nothing, such as planning a
 * template, which reads it; they go to the real panel. It counts them as
 * actions (30 a minute for each session), and the crawl clicks much faster
 * than a person, so a refusal is tried again after the wait the panel asks
 * for.
 */
const computes = [/^\/api\/servers\/\w+\/schedules\/preview$/, /^\/api\/servers\/\w+\/backup-rules\/estimate$/, /^\/api\/machines\/\w+\/templates\/plan$/]

async function fetchFromPanel(route: Route, compute: boolean): Promise<APIResponse | null> {
  for (let tries = 1; ; tries++) {
    const res = await route.fetch().catch(() => null)
    if (!compute || res?.status() !== 429 || tries === 5) return res
    const wait = Math.min(Math.max(Number(res.headers()['retry-after']) || 2, 1), 5)
    await new Promise((resolve) => setTimeout(resolve, wait * 1000))
  }
}

const automaticEvery = [1, 2, 3, 4, 6, 8, 12, 24]
const diskWays = ['old_backups', 'old_logs', 'old_crash_reports', 'unused_software', 'downloads', 'set_aside', 'unfinished']
const diskID = /^[0-9a-f]{32}$/

interface PlaceBody {
  config?: { type?: string; s3?: Record<string, unknown>; sftp?: Record<string, unknown> }
  secretKey?: string
  password?: string
  sftpAuth?: string
  hostKey?: string
  enabled?: unknown
  recoveryKey?: string
  name?: unknown
}

function missing(field: string, error: string, hint?: string): Reply {
  return { status: 400, body: { error, hint, field, code: 'invalid' }, expected: true }
}

/**
 * The agent's first complaint about the place on the page, as offsite's
 * Config.Validate words it. The click-through doesn't type, so pressing Test
 * connection on an empty form shows this, which is what a person sees too.
 */
function placeProblem(b: PlaceBody, view: Record<string, unknown> | undefined, hasSSHKey: boolean): Reply | undefined {
  const c = b.config
  if (!c) return undefined
  if (c.type === 's3') {
    const s3 = c.s3 ?? {}
    if (!s3.endpoint) return missing('endpoint', 'Enter the endpoint address of the storage service.', 'It starts with https://, for example https://s3.eu-central-003.backblazeb2.com.')
    if (!s3.bucket) return missing('bucket', 'The bucket name must be 3 to 63 characters long.', 'Bucket names are 3 to 63 lowercase letters, digits, dots and hyphens.')
    if (!s3.accessKeyId) return missing('accessKeyId', 'Enter the access key ID exactly as the storage service shows it.')
    const secretSet = (view?.s3 as { secretKeySet?: boolean } | undefined)?.secretKeySet
    if (!b.secretKey && !secretSet) return missing('secretKey', 'Enter the secret access key exactly as the storage service showed it.', 'The secret is shown only once when the key is created; create a new key if you no longer have it.')
    return undefined
  }
  if (c.type === 'sftp') {
    const sftp = c.sftp ?? {}
    if (b.sftpAuth === 'key' && !hasSSHKey) return missing('privateKey', 'Make the key Playkeeper signs in with first.', 'Then add its line to authorized_keys on the other machine.')
    if (!sftp.host) return missing('host', "Enter the other machine's address: its host name or IP address.")
    if (!sftp.user) return missing('user', 'Enter the user name to sign in with on the other machine.')
    if (!sftp.folder) return missing('folder', 'Enter the folder on the other machine where the copies go.', "For example /srv/backups/playkeeper, or backups/playkeeper for a folder in the user's home folder.")
    return undefined
  }
  return missing('type', 'Choose where the copies go: S3-compatible storage or another machine over SFTP.')
}

const standInPublicKey = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFN0YW5kLWluIGtleSBmb3IgdGhlIGNsaWNrLXRocm91Z2g playkeeper-stand-in'
const standInHostKey = { key: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEhvc3Qga2V5IHN0YW5kLWluIGZvciB0aGUgY2xpY2stdGhyb3U', type: 'ssh-ed25519', fingerprint: 'SHA256:c3RhbmQtaW4gaG9zdCBrZXkgZm9yIHRoZSBjbGljaw' }

/** What a connection test that got through says, step by step as the agent's probe does. */
function testPassed(b: PlaceBody, view: Record<string, unknown> | undefined): Record<string, unknown> {
  const pinned = (view?.sftp as { hostKeyFingerprint?: string } | undefined)?.hostKeyFingerprint
  if (b.config?.type === 'sftp' && !b.hostKey && !pinned) {
    return {
      ok: false,
      skew: 0,
      hostKey: standInHostKey,
      checks: [
        {
          step: 'connect',
          ok: false,
          kind: 'host_key_unknown',
          msg: `Check that the other machine's host key fingerprint is ${standInHostKey.fingerprint}, then confirm it.`,
          hint: 'On the other machine, ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub shows it. Nothing is sent before you confirm it.',
        },
      ],
    }
  }
  const steps: [string, string][] =
    b.config?.type === 'sftp'
      ? [
          ['connect', 'Signed in to the other machine.'],
          ['folder', 'Found the folder.'],
          ['write', 'Wrote a small encrypted test file.'],
          ['rename', 'Renamed the test file.'],
          ['read', 'Read the test file back unchanged.'],
          ['list', "Found the test file in the folder's list."],
          ['delete', 'Deleted the test file.'],
        ]
      : [
          ['write', 'Wrote a small encrypted test file.'],
          ['read', 'Read the test file back unchanged.'],
          ['list', "Found the test file in the folder's list."],
          ['multipart', 'Started and cancelled a multipart upload, as large backups need.'],
          ['delete', 'Deleted the test file.'],
        ]
  return { ok: true, skew: 0, checks: steps.map(([step, msg]) => ({ step, ok: true, msg })) }
}

/** The copies view after a save, like the agent's offsiteView of the merged settings. */
function savedPlace(serverId: string, b: PlaceBody, view: Record<string, unknown> | undefined): Record<string, unknown> {
  const next: Record<string, unknown> = { enabled: false, configured: false, type: '', place: '', copies: 0, copiesBytes: 0, queued: 0, providers: [], ...view }
  const c = b.config
  if (c?.type === 's3') Object.assign(next, { configured: true, type: 's3', place: String(c.s3?.endpoint ?? ''), s3: { ...(view?.s3 as object | undefined), ...c.s3, secretKeySet: true }, sftp: undefined })
  if (c?.type === 'sftp') {
    const sftp = { ...(view?.sftp as object | undefined), ...c.sftp, auth: b.sftpAuth, passwordSet: b.sftpAuth === 'password' || undefined }
    if (b.hostKey) Object.assign(sftp, { hostKey: b.hostKey, hostKeyType: standInHostKey.type, hostKeyFingerprint: standInHostKey.fingerprint })
    Object.assign(next, { configured: true, type: 'sftp', place: String(c.sftp?.host ?? ''), sftp, s3: undefined })
  }
  if (typeof b.enabled === 'boolean') next.enabled = b.enabled
  if (next.enabled && !next.key) next.key = { recipient: 'age1standin', createdAt: new Date().toISOString(), oldKeys: 0, fileName: `playkeeper-recovery-key-${serverId}.txt` }
  return next
}

function playerName(body: unknown): string | undefined {
  const n = (body as { name?: unknown } | null)?.name
  return typeof n === 'string' && name.test(n) ? n : undefined
}

function backupFor(state: FakeState, serverId: string, backupId: string): Record<string, unknown> | undefined {
  return state.backups.get(serverId)?.find((b) => b.id === backupId)
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
  ['DELETE', /^\/api\/servers\/(\w+)\/world-copies\/([^/]+)$/, (r) => (worldCopyName.test(decodeURIComponent(r.params[1] ?? '')) ? { status: 204, raw: '' } : invalid('Invalid world copy name.'))],
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
  ['POST', /^\/api\/servers\/(\w+)\/addons\/update$/, (r, state) => (confirmed(r.body) && Array.isArray((r.body as { addons?: unknown }).addons) ? op(state, 'addon-update', r.params[0]) : invalid('This request doesn’t include the plan you confirmed.'))],
  ['POST', /^\/api\/servers\/(\w+)\/addons\/install$/, (r, state) => (confirmed(r.body) && typeof (r.body as { projectId?: unknown }).projectId === 'string' ? op(state, 'addon-install', r.params[0]) : invalid('This request doesn’t include the plan you confirmed.'))],
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
  [
    'POST',
    /^\/api\/servers\/(\w+)\/schedules$/,
    (r, state) => {
      const b = (r.body ?? {}) as Record<string, unknown>
      if (!b.kind || !b.timing) return invalid('Say what the schedule does and when.')
      const now = new Date().toISOString()
      return { status: 201, body: { id: `fake${++state.opSeq}`, serverId: r.params[0], name: '', enabled: true, payload: {}, createdAt: now, updatedAt: now, createdBy: 'admin', updatedBy: 'admin', ...b } }
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/schedules\/([a-z0-9]{1,32})$/,
    (r, state) => {
      const s = state.schedules.get(r.params[0] ?? '')?.find((x) => x.id === r.params[1])
      if (!s) return { status: 404, body: { error: 'Schedule not found.', code: 'not_found' } }
      return { status: 200, body: { ...s, ...(r.body as Record<string, unknown> | null), updatedAt: new Date().toISOString(), updatedBy: 'admin' } }
    },
  ],
  [
    'DELETE',
    /^\/api\/servers\/(\w+)\/schedules\/([a-z0-9]{1,32})$/,
    (r, state) =>
      state.schedules.get(r.params[0] ?? '')?.some((x) => x.id === r.params[1]) ? { status: 200, body: { deleted: r.params[1] } } : { status: 404, body: { error: 'Schedule not found.', code: 'not_found' } },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/sleep$/,
    ({ body }) => {
      const b = body as { enabled?: unknown; idleMinutes?: unknown } | null
      const idle = b?.idleMinutes ?? 0
      if (typeof b?.enabled !== 'boolean' || typeof idle !== 'number' || !Number.isInteger(idle)) return invalid('Say whether the server sleeps and after how many minutes.')
      if (idle !== 0 && (idle < 5 || idle > 24 * 60)) return invalid('Choose between 5 minutes and 24 hours of nobody playing before the server sleeps.')
      return { status: 200, body: { sleep: { enabled: b.enabled, idleMinutes: idle || 15, listening: false } } }
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/backup-rules$/,
    (r, state) => {
      const b = (r.body ?? {}) as { automatic?: { everyHours?: unknown }; rules?: unknown; timeZone?: unknown }
      if (b.timeZone !== undefined && b.timeZone !== '' && !knownZone(b.timeZone)) return { status: 400, body: { error: 'Unknown time zone.', code: 'invalid', field: 'timeZone' } }
      if (b.rules !== undefined && (typeof b.rules !== 'object' || b.rules === null)) return invalid('Send the rules as an object.')
      if (b.automatic && !automaticEvery.includes(Number(b.automatic.everyHours))) {
        return invalid(`Automatic backups can run every 1, 2, 3, 4, 6, 8 or 12 hours, or once a day, not every ${String(b.automatic.everyHours)} hours.`)
      }
      const view = { ...state.backupRules.get(r.params[0] ?? '') }
      if (b.automatic) view.automatic = { ...(view.automatic as object | undefined), ...b.automatic }
      if (b.rules) Object.assign(view, { rules: b.rules, custom: true })
      return { status: 200, body: view }
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/offsite$/,
    (r, state) => {
      const id = r.params[0] ?? ''
      const b = (r.body ?? {}) as PlaceBody
      const view = state.offsite.get(id)
      const bad = placeProblem(b, view, state.sshKeys.has(id) || !!view?.sshKey)
      if (bad) return bad
      if (b.enabled === true && !b.config && !view?.configured) return invalid('Choose where the copies go first.')
      const next = savedPlace(id, b, view)
      state.offsite.set(id, next)
      return { status: 200, body: next }
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/offsite\/test$/,
    (r, state) => {
      const id = r.params[0] ?? ''
      const b = (r.body ?? {}) as PlaceBody
      const view = state.offsite.get(id)
      return placeProblem(b, view, state.sshKeys.has(id) || !!view?.sshKey) ?? { status: 200, body: testPassed(b, view) }
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/offsite\/ssh-key$/,
    (r, state) => {
      state.sshKeys.add(r.params[0] ?? '')
      return { status: 200, body: { publicKey: standInPublicKey, authorizedKey: `restrict ${standInPublicKey}`, fingerprint: 'SHA256:c3RhbmQtaW4ga2V5IGZvciB0aGUgY2xpY2stdGhy' } }
    },
  ],
  ['POST', /^\/api\/servers\/(\w+)\/offsite\/retry$/, (r, state) => ({ status: 200, body: state.offsite.get(r.params[0] ?? '') ?? {} })],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/offsite\/new-key$/,
    (r, state) => {
      const id = r.params[0] ?? ''
      const view = state.offsite.get(id)
      const key = view?.key as { recipient: string; oldKeys: number } | undefined
      if (!view || !key) return { status: 409, body: { error: 'There is no key to replace yet.', hint: 'Turn on copies somewhere else first.', code: 'conflict' } }
      const next = { ...view, key: { ...key, recipient: 'age1standinnew', oldKeys: key.oldKeys + 1, savedAt: undefined, createdAt: new Date().toISOString() } }
      state.offsite.set(id, next)
      const rotation = { recipient: 'age1standinnew', oldRecipient: key.recipient, oldKeys: key.oldKeys + 1, code: 'rotated', msg: 'New copies use the new key.', hint: 'Save the new recovery key file.' }
      return { status: 200, body: { rotation, offsite: next } }
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/offsite\/restore$/,
    (r, state) => {
      const n = (r.body as PlaceBody | null)?.name
      return typeof n === 'string' && n ? op(state, 'offsite-restore', r.params[0], { name: n }) : { status: 400, body: { error: "That is not the name of a backup's copy.", code: 'invalid', field: 'name' } }
    },
  ],
  [
    'POST',
    /^\/api\/servers\/(\w+)\/offsite\/restore\/cancel$/,
    (r, state) => {
      const o = state.ops.get(String((r.body as { operationId?: unknown } | null)?.operationId ?? ''))
      return o ? { status: 202, body: o } : { status: 409, body: { error: "That isn't running any more.", code: 'conflict' } }
    },
  ],
  ['POST', /^\/api\/servers\/(\w+)\/offsite\/copies\/([^/]+)\/check$/, (r, state) => op(state, 'offsite-check', r.params[0], { name: decodeURIComponent(r.params[1] ?? '') })],
  ['DELETE', /^\/api\/servers\/(\w+)\/offsite\/copies\/([^/]+)$/, (r) => ({ status: 200, body: { deleted: decodeURIComponent(r.params[1] ?? '') } })],
  [
    'POST',
    /^\/api\/machines\/(\w+)\/offsite\/recover$/,
    ({ body }) => {
      const b = (body ?? {}) as PlaceBody
      if (!b.recoveryKey) return missing('recoveryKey', 'Choose the recovery key file.')
      const bad = placeProblem(b, undefined, false)
      if (bad) return bad
      const at = (days: number) => new Date(Date.now() - days * 86_400_000).toISOString()
      const copies = [
        { name: 'survival-2026-09-25-0300.tar.zst.age', sizeBytes: 412_000_000, createdAt: at(1) },
        { name: 'survival-2026-09-24-0300.tar.zst.age', sizeBytes: 409_000_000, createdAt: at(2) },
      ]
      return { status: 200, body: { server: 'Survival', keys: 2, place: String(b.config?.type === 'sftp' ? b.config.sftp?.host : b.config?.s3?.endpoint), copies } }
    },
  ],
  [
    'POST',
    /^\/api\/machines\/(\w+)\/offsite\/recover\/restore$/,
    (r, state) => {
      const b = (r.body ?? {}) as PlaceBody
      if (!b.recoveryKey) return missing('recoveryKey', 'Choose the recovery key file.')
      return typeof b.name === 'string' && b.name ? op(state, 'offsite-recover', undefined, { name: b.name }) : { status: 400, body: { error: 'Pick a copy to restore.', code: 'invalid', field: 'name' } }
    },
  ],
  [
    'POST',
    /^\/api\/machines\/(\w+)\/disk\/clean$/,
    (r, state) => {
      const b = (r.body ?? {}) as { ids?: unknown; ways?: unknown; timeZone?: unknown }
      const ids = Array.isArray(b.ids) ? b.ids : []
      const ways = Array.isArray(b.ways) ? b.ways : []
      if (b.timeZone !== undefined && !knownZone(b.timeZone)) return { status: 400, body: { error: 'Unknown time zone.', code: 'invalid', field: 'timeZone' } }
      if (!ids.length && !ways.length) return { status: 400, body: { error: 'Choose what to delete.', code: 'invalid', field: 'ids' } }
      if (ids.some((id) => typeof id !== 'string' || !diskID.test(id))) return { status: 400, body: { error: 'One of the chosen items is not valid.', hint: 'Scan again and choose from the new list.', code: 'invalid', field: 'ids' } }
      if (ways.some((w) => typeof w !== 'string' || !diskWays.includes(w))) return { status: 400, body: { error: 'Unknown way to free space.', code: 'invalid', field: 'ways' } }
      return op(state, 'disk-cleanup')
    },
  ],
]

const addonJar = /^[^./\\][^/\\]{0,195}\.jar$/

/** An add-on install or update carries the fingerprint of the plan the user confirmed. */
function confirmed(body: unknown): boolean {
  return /^[0-9a-f]{32}$/.test(String((body as { fingerprint?: unknown } | null)?.fingerprint ?? ''))
}

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
  const state: FakeState = { prefs: {}, backups: new Map(), update: {}, opSeq: 0, addresses: new Map(), ops: new Map(), schedules: new Map(), backupRules: new Map(), offsite: new Map(), sshKeys: new Set() }
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
    const compute = method === 'POST' && computes.some((re) => re.test(path))
    if (method === 'GET' || method === 'HEAD' || compute) {
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
      const key = /^\/api\/servers\/(\w+)\/offsite\/recovery-key$/.exec(path)
      if (key?.[1]) {
        calls.push({ method, path, status: 200, faked: true, at })
        const file = `playkeeper-recovery-key-${key[1]}.txt`
        await route.fulfill({ status: 200, headers: { 'Content-Type': 'text/plain; charset=utf-8', 'Content-Disposition': `attachment; filename="${file}"`, 'Cache-Control': 'no-store' }, body: '# A stand-in recovery key from the click-through. It opens nothing.\n' })
        return
      }
      // A faked job finishes at once, for the dialogs that follow it.
      const fakeOp = /^\/api\/(?:servers|machines)\/\w+\/operations\/(fake-op-\d+)$/.exec(path)
      if (fakeOp?.[1]) {
        const o = state.ops.get(fakeOp[1])
        calls.push({ method, path, status: o ? 200 : 404, faked: true, at })
        await route.fulfill({ status: o ? 200 : 404, contentType: 'application/json', body: JSON.stringify(o ? finished(o) : { error: 'Operation not found.', code: 'not_found' }) })
        return
      }
      if (/^\/api\/machines\/\w+\/restore\/fakerestore$/.test(path)) {
        calls.push({ method, path, status: 200, faked: true, at })
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(restorePreview(undefined)) })
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
      const res = await fetchFromPanel(route, compute)
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
        const address = /^\/api\/machines\/(\w+)\/address$/.exec(path)
        if (address?.[1]) state.addresses.set(address[1], await res.json().catch(() => ({})))
        const sched = /^\/api\/servers\/(\w+)\/schedules$/.exec(path)
        if (sched?.[1] && method === 'GET') state.schedules.set(sched[1], ((await res.json().catch(() => ({}))) as { schedules?: Record<string, unknown>[] }).schedules ?? [])
        const rules = /^\/api\/servers\/(\w+)\/backup-rules$/.exec(path)
        if (rules?.[1]) state.backupRules.set(rules[1], await res.json().catch(() => ({})))
        const place = /^\/api\/servers\/(\w+)\/offsite$/.exec(path)
        if (place?.[1]) state.offsite.set(place[1], await res.json().catch(() => ({})))
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
