import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type * as client from '@/api/client'
import type { BackupRulesView, DiscordSettings, DiskReport, InvitesResponse, JoinRequestView, MachineView, MapInfo, OffsiteCopy, OffsiteView, Operation, PlayerProfile, RetentionSettings, SchedulePreview, SchedulesResponse, ScheduleRun, ServerStatus, SleepView, TeamResponse, WorldImport, WorldImportPreview } from '@/api/types'
import { estimate } from './automation'
import { answer, resetDemo } from './engine'
import { demoToast } from './toast'
import { uploadWorld } from './upload'

vi.mock('./toast', () => ({ demoToast: vi.fn() }))
// The demo build gives lib/upload the demo's client; here the real client
// asks the engine instead. The engine imports the client itself, so it is
// only imported once a request is made.
vi.mock('@/api/client', async (real) => {
  const actual = await real<typeof client>()
  const engine = () => import('./engine')
  return { ...actual, get: async (path: string) => (await engine()).answer('GET', path), post: async (path: string, body: unknown = {}) => (await engine()).answer('POST', path, body) }
})

async function ask<T>(method: string, path: string, body?: unknown): Promise<T> {
  const reply = answer(method, path, body).then(
    (value) => ({ value }),
    (error: unknown) => ({ error }),
  )
  await vi.advanceTimersByTimeAsync(200)
  const settled = await reply
  if ('error' in settled) throw settled.error
  return settled.value as T
}

async function server(slug: string): Promise<ServerStatus> {
  const found = (await ask<ServerStatus[]>('GET', '/api/servers')).find((s) => s.slug === slug)
  if (!found) throw new Error(`no ${slug} in the sample data`)
  return found
}

const machine = async () => (await ask<MachineView[]>('GET', '/api/machines'))[0]

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-09-25T12:10:00Z'))
  resetDemo()
  vi.mocked(demoToast).mockClear()
})

afterEach(() => {
  vi.useRealTimers()
})

it('answers every screen Waves 5 to 7 added with sample data', async () => {
  const survival = await server('survival')
  const s = (path: string) => `/api/servers/${survival.id}${path}`
  const team = await ask<TeamResponse>('GET', '/api/team')
  expect(team.members.map((m) => m.username)).toEqual(['siya', 'juno', 'pia', 'brick'])
  expect(team.invites[0]?.status).toBe('active')
  expect((await ask<InvitesResponse>('GET', s('/invites'))).invites.map((i) => i.label)).toEqual(['School friends', 'Discord crew', 'Weekend guests'])
  const [request] = await ask<JoinRequestView[]>('GET', s('/join-requests'))
  expect(request?.notice.detail.text).toBe('Asked with the Discord crew link')
  expect((await ask<DiscordSettings>('GET', '/api/discord')).connected).toBe(true)
  expect((await ask<BackupRulesView>('GET', s('/backup-rules'))).automatic).toMatchObject({ enabled: true, everyHours: 6, onlyIfPlayed: true })
  expect((await ask<OffsiteView>('GET', s('/offsite'))).place).toBe('Backblaze B2')
  expect((await ask<{ copies: OffsiteCopy[] }>('GET', s('/offsite/copies'))).copies.length).toBeGreaterThan(10)
  expect((await ask<SchedulesResponse>('GET', s('/schedules'))).schedules.map((x) => x.kind)).toEqual(['backup', 'announcement'])
  expect((await ask<{ runs: ScheduleRun[] }>('GET', s('/schedules/runs?limit=6'))).runs).toHaveLength(6)
  expect((await ask<SleepView>('GET', s('/sleep'))).enabled).toBe(false)
  expect((await ask<PlayerProfile>('GET', s('/players/profile?name=Kestrel_7'))).joined?.text).toBe('Joined with the School friends link')
  await expect(ask('GET', s('/players/profile?name=Nobody'))).rejects.toMatchObject({ status: 404 })
  await expect(ask('GET', s('/world-copies'))).resolves.toEqual([])
  expect((await ask<MapInfo>('GET', s('/map'))).state).toBe('not_installed')
  const disk = await ask<DiskReport>('GET', `/api/machines/${(await machine())?.id}/disk`)
  expect(disk.ways.map((w) => w.id)).toEqual(['old_logs', 'unused_software', 'downloads'])
  expect(disk.freeable).toBe(disk.ways.reduce((n, w) => n + w.bytes, 0))
})

it('keeps Survival’s backups, copies and runs to one timeline', async () => {
  const survival = await server('survival')
  const backups = await ask<{ id: string; kind: string; createdAt: string }[]>('GET', `/api/servers/${survival.id}/backups`)
  const copies = (await ask<{ copies: OffsiteCopy[] }>('GET', `/api/servers/${survival.id}/offsite/copies`)).copies
  const here = new Set(backups.map((b) => b.id))
  // A copy says whether its backup is still on the machine, and the machine keeps what the rules keep.
  expect(copies.every((c) => c.onHost === here.has(c.backupId))).toBe(true)
  expect(backups.filter((b) => b.kind === 'scheduled').length).toBeLessThanOrEqual(13)
  // The rules keep every backup of the last 24 hours; older runs' backups may be gone, as the rules allow.
  const newest = Date.parse(backups.find((b) => b.kind === 'scheduled')?.createdAt ?? '')
  const runs = (await ask<{ runs: ScheduleRun[] }>('GET', `/api/servers/${survival.id}/schedules/runs?limit=20`)).runs
  const recent = runs.filter((r) => r.backup && Date.parse(r.due) > newest - 24 * 3600_000)
  expect(recent.length).toBeGreaterThan(1)
  expect(recent.every((r) => here.has(r.backup?.id ?? ''))).toBe(true)
  expect(runs.filter((r) => r.result === 'skipped').every((r) => r.reason === 'nobody_played')).toBe(true)
})

it('works out the rules editor’s totals as internal/backup/retention does', () => {
  const defaults: RetentionSettings = { onHost: { hours: 24, daily: 7, weekly: 4 }, offSite: { daily: 14, weekly: 8, monthly: 12 } }
  // Counts from retention.Settings.Estimate in UTC, for the same settings and pace.
  const cases: { settings: RetentionSettings; every: number; onHost: [number, number[]]; offSite: [number, number[]] }[] = [
    { settings: defaults, every: 6, onHost: [13, [4, 7, 4, 0]], offSite: [29, [14, 8, 12, 0]] },
    { settings: defaults, every: 24, onHost: [10, [1, 7, 4, 0]], offSite: [29, [14, 8, 12, 0]] },
    { settings: defaults, every: 1, onHost: [33, [24, 7, 4, 0]], offSite: [29, [14, 8, 12, 0]] },
    { settings: { onHost: { last: 10, daily: 14, weekly: 8 }, offSite: defaults.offSite }, every: 24, onHost: [20, [10, 14, 8, 0]], offSite: [29, [14, 8, 12, 0]] },
    { settings: { onHost: { hours: 48, monthly: 6 }, offSite: { last: 3, weekly: 52 } }, every: 12, onHost: [9, [4, 6, 0]], offSite: [54, [3, 52, 0]] },
  ]
  for (const c of cases) {
    for (const where of ['onHost', 'offSite'] as const) {
      const e = estimate(c.settings, where === 'onHost' ? 'on-host' : 'off-site', c.every, 1e9)
      expect([e.count, e.rows.map((r) => r.count)], `${where}, every ${c.every} hours`).toEqual(c[where])
      expect(e.bytes).toBe(e.count * 1e9)
    }
  }
  expect(estimate(defaults, 'on-host', 0, 1e9).summary.code).toBe('estimate_off')
  expect(estimate({ onHost: { keepAll: true }, offSite: {} }, 'on-host', 6, 1e9).count).toBe(-1)
})

it('puts Creative to sleep with its memory given back, until it wakes', async () => {
  const creative = await server('creative')
  expect(creative).toMatchObject({ phase: 'asleep', desired: 'running', sleep: { enabled: true, idleMinutes: 30, listening: true } })
  expect((await machine())?.live?.sleepingMemoryMB).toBe(3072)
  expect((await ask<SleepView>('GET', `/api/servers/${creative.id}/sleep`)).today.count).toBe(1)

  await ask('POST', `/api/servers/${creative.id}/start`)
  await vi.advanceTimersByTimeAsync(20_000)
  const awake = await server('creative')
  expect(awake.phase).toBe('online')
  expect(awake.sleep?.asleepSince).toBeUndefined()
  expect((await machine())?.live?.sleepingMemoryMB).toBe(0)
})

it('copies a new backup of Survival to Backblaze B2 a minute and a half after it’s made', async () => {
  const { id } = await server('survival')
  const before = (await ask<OffsiteView>('GET', `/api/servers/${id}/offsite`)).copies
  await ask('POST', `/api/servers/${id}/backups`, { note: '' })
  await vi.advanceTimersByTimeAsync(10_000)
  const uploading = await ask<OffsiteView>('GET', `/api/servers/${id}/offsite`)
  expect(uploading.pending?.uploading).toBe(true)
  expect(uploading.copies).toBe(before)
  await vi.advanceTimersByTimeAsync(90_000)
  const done = await ask<OffsiteView>('GET', `/api/servers/${id}/offsite`)
  expect(done.pending).toBeUndefined()
  expect(done.copies).toBe(before + 1)
})

it('makes a server from a world whose upload sends no bytes', async () => {
  const mid = (await machine())?.id ?? ''
  const file = new File([], 'Our old survival world.zip')
  Object.defineProperty(file, 'size', { value: 412 * 1024 * 1024 })
  const seen: number[] = []
  const upload = uploadWorld({ base: `/api/machines/${mid}/world-imports`, files: [file], signal: new AbortController().signal, onProgress: (p) => seen.push(p.sent) })
  await vi.advanceTimersByTimeAsync(10_000)
  const imp = await upload
  expect(imp.files[0]).toMatchObject({ name: 'Our old survival world.zip', size: file.size, received: file.size })
  expect(seen.at(-1)).toBe(file.size)
  expect(new Set(seen).size).toBeGreaterThan(5)

  const inspected = await ask<WorldImport>('POST', `/api/machines/${mid}/world-imports/${imp.id}/inspect`)
  expect(inspected.inspection?.worlds.map((w) => w.path)).toEqual(['Our old survival world'])
  const preview = await ask<WorldImportPreview>('POST', `/api/machines/${mid}/world-imports/${imp.id}/preview`, { options: { world: 'w1' } })
  expect(preview.versions?.map((v) => [v.minecraftVersion, v.keep])).toEqual([
    ['26.1.2', false],
    ['1.21.11', true],
  ])
  expect(preview.preview.version?.compat).toBe('upgrade')
  const op = await ask<Operation>('POST', `/api/machines/${mid}/world-imports/${imp.id}/create`, { versionId: preview.versionId, name: 'Old World', memoryMB: 2048, acceptEula: true })
  await vi.advanceTimersByTimeAsync(20_000)
  const made = await server('old-world')
  expect(made.id).toBe(op.serverId)
  expect(made.phase).toBe('online')
  expect(made.worldBytes).toBe(inspected.inspection?.worlds[0]?.sizeBytes)
  await expect(ask('GET', `/api/machines/${mid}/world-imports/${imp.id}`)).rejects.toMatchObject({ status: 404 })
})

it('previews a schedule’s next runs, and says what’s wrong with one that has none', async () => {
  const { id } = await server('survival')
  const weekly = await ask<SchedulePreview>('POST', `/api/servers/${id}/schedules/preview`, { kind: 'announcement', timing: { kind: 'weekly', timeZone: 'UTC', at: '19:30', days: ['fri'] }, payload: {} })
  expect(weekly.valid).toBe(true)
  expect(weekly.nextRuns).toEqual(['2026-09-25T19:30:00.000Z', '2026-10-02T19:30:00.000Z', '2026-10-09T19:30:00.000Z'])
  const every = await ask<SchedulePreview>('POST', `/api/servers/${id}/schedules/preview`, { kind: 'backup', timing: { kind: 'interval', timeZone: 'UTC', at: '00:00', everyHours: 6 }, payload: {} })
  expect(every.nextRuns).toEqual(['2026-09-25T18:00:00.000Z', '2026-09-26T00:00:00.000Z', '2026-09-26T06:00:00.000Z'])
  const none = await ask<SchedulePreview>('POST', `/api/servers/${id}/schedules/preview`, { kind: 'announcement', timing: { kind: 'weekly', timeZone: 'UTC', at: '19:30', days: [] }, payload: {} })
  expect(none).toMatchObject({ valid: false, nextRuns: [] })
})

it('answers "not in the demo" to changing any of it', async () => {
  const { id } = await server('survival')
  for (const [method, path] of [
    ['PUT', '/api/discord'],
    ['POST', '/api/team/invites'],
    ['POST', `/api/servers/${id}/backup-rules`],
    ['POST', `/api/servers/${id}/offsite`],
    ['POST', `/api/servers/${id}/sleep`],
    ['POST', `/api/servers/${id}/schedules`],
    ['POST', `/api/servers/${id}/join-requests/jrbramble/approve`],
    ['POST', `/api/servers/${id}/map/enable`],
  ]) {
    await expect(ask(method ?? '', path ?? '', {}), `${method} ${path}`).rejects.toMatchObject({ status: 400, code: 'demo' })
  }
})
