// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, DiskCandidate, DiskReport, DiskWay, MachineView, Me, OffsiteView, Operation, ServerStatus } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import * as controls from '@/components/app/controls'
import { AppShell } from '@/components/app/shell'
import { toastManager } from '@/components/ui/toast'
import { formatLocale } from '@/i18n'
import { formatClock, formatDate } from '@/lib/format'
import { DiskPage } from './disk'
import { MachinePage } from './machine'

const view = vi.hoisted(() => ({ phone: false }))

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => new Promise(() => {})),
}))

vi.mock('@/components/app/controls', async (importOriginal) => ({
  ...(await importOriginal<typeof controls>()),
  useIsPhone: () => view.phone,
}))

const GB = 2 ** 30
const MB = 2 ** 20
const backupSize = Math.round(0.6 * GB)
const survival = 'abcdefghjk'
const creative = 'bcdefghjkm'
const hex = (n: number) => n.toString(16).padStart(32, '0')

const everything: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view', 'backups.copies.manage', 'backups.recovery_key', 'backups.recover']
const me: Me = {
  user: { username: 'siya', role: 'owner' },
  csrfToken: 't',
  expiresAt: '2026-09-26T00:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.4.0',
  access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can: everything },
}

const machine: MachineView = {
  id: 'm2345abcde',
  projectId: 'p2345abcde',
  name: 'my-vps',
  kind: 'local',
  live: {
    hostname: 'my-vps',
    os: 'Ubuntu 24.04',
    arch: 'x86-64',
    cpus: 4,
    cpuPercent: 22,
    memoryTotalMB: 16384,
    systemReserveMB: 1536,
    serversMemoryMB: 4096,
    memoryFreeMB: 10752,
    diskFreeBytes: 41 * GB,
    diskTotalBytes: 80 * GB,
    docker: true,
    dockerVersion: '27.3.1',
    agentVersion: '0.4.0',
    defaultGamePort: 25565,
    offlineModeTest: false,
    servers: 2,
  },
}

function workspace(over: Partial<Workspace> = {}): Workspace {
  return {
    me,
    servers: [],
    serversError: undefined,
    machine,
    machines: [machine],
    prefs: {},
    setPrefs: async () => {},
    refresh: async () => {},
    updating: undefined,
    updatingSince: undefined,
    agentDown: false,
    stale: false,
    lastSeenAt: undefined,
    machineName: 'my-vps',
    lastSlug: undefined,
    setLastSlug: () => {},
    signOut: async () => {},
    reloadMe: async () => {},
    signInNotice: undefined,
    dismissSignInNotice: () => {},
    ...over,
  }
}

function backup(n: number, serverId: string, createdAt: string): DiskCandidate {
  return { id: hex(n), serverId, kind: 'backup', reason: 'pruned_backup', risk: 'low', path: `/backups/${n}.tar.gz`, bytes: backupSize, files: 1, modifiedAt: createdAt, params: { createdAt, backupId: `b${n}` }, text: 'Old backup' }
}

function setAside(n: number, serverId: string, why: string, date: string, bytes: number): DiskCandidate {
  return { id: hex(n), serverId, kind: 'server_folder', reason: 'leftover_copy', risk: 'medium', path: `/data/${n}`, bytes, files: 900, modifiedAt: `${date}T09:30:00Z`, params: { why, date }, text: 'Leftover copy' }
}

// Survival's backups come first (the table's order), each server's newest first.
const backups = [
  backup(1, survival, '2026-09-17T03:00:00Z'),
  backup(2, survival, '2026-09-16T03:00:00Z'),
  backup(3, survival, '2026-09-15T03:00:00Z'),
  backup(4, survival, '2026-09-14T03:00:00Z'),
  backup(5, survival, '2026-09-13T03:00:00Z'),
  backup(6, creative, '2026-09-16T15:00:00Z'),
  backup(7, creative, '2026-09-15T15:00:00Z'),
  backup(8, creative, '2026-09-14T15:00:00Z'),
  backup(9, creative, '2026-09-13T15:00:00Z'),
]
const folders = [setAside(20, survival, 'replaced', '2026-09-21', Math.round(2.1 * GB)), setAside(21, creative, 'failed_update', '2026-09-18', Math.round(1.4 * GB))]

function way(id: DiskWay['id'], action: DiskWay['action'], bytes: number, over: Partial<DiskWay> = {}): DiskWay {
  return { id, action, bytes, candidateIds: [], title: 'From the agent', text: 'From the agent', ...over }
}

const ways: DiskWay[] = [
  way('old_backups', 'review', 9 * backupSize, { candidateIds: backups.map((c) => c.id), serverIds: [survival, creative] }),
  way('old_logs', 'delete', Math.round(1.2 * GB), { everyServer: true, params: { days: '30' } }),
  way('old_crash_reports', 'delete', 120 * MB, { serverIds: [survival], params: { days: '30' } }),
  way('unused_software', 'delete', 96 * MB, { versions: [{ software: 'Paper', version: '26.1.1', build: '61' }] }),
  way('set_aside', 'review', folders[0]!.bytes + folders[1]!.bytes, { candidateIds: folders.map((c) => c.id), serverIds: [survival, creative] }),
  way('unfinished', 'delete', 310 * MB),
]

function report(over: Partial<DiskReport> = {}): DiskReport {
  const bar = [
    { group: 'backups' as const, bytes: Math.round(18.2 * GB) },
    { group: 'worlds' as const, bytes: 2 * GB },
    { group: 'server_files' as const, bytes: Math.round(1.1 * GB) },
    { group: 'logs' as const, bytes: Math.round(1.5 * GB) },
    { group: 'other' as const, bytes: 39 * GB - Math.round(18.2 * GB) - 2 * GB - Math.round(1.1 * GB) - Math.round(1.5 * GB) },
    { group: 'free' as const, bytes: 41 * GB },
  ]
  return {
    scannedAt: '2026-09-25T20:00:00Z',
    disk: { dir: '/var/lib/playkeeper', total: 80 * GB, free: 41 * GB, used: 39 * GB, bar },
    servers: [
      {
        id: survival,
        name: 'Survival',
        total: { bytes: Math.round(14.1 * GB), files: 4000 },
        kinds: [],
        groups: [
          { group: 'worlds', bytes: Math.round(1.2 * GB) },
          { group: 'server_files', bytes: Math.round(0.6 * GB) },
          { group: 'backups', bytes: Math.round(11.4 * GB) },
          { group: 'logs', bytes: Math.round(0.9 * GB) },
        ],
      },
      {
        id: creative,
        name: 'Creative',
        total: { bytes: Math.round(7.9 * GB), files: 2000 },
        kinds: [],
        groups: [
          { group: 'worlds', bytes: Math.round(0.8 * GB) },
          { group: 'server_files', bytes: Math.round(0.5 * GB) },
          { group: 'backups', bytes: Math.round(6.8 * GB) },
          { group: 'logs', bytes: 50 * MB },
        ],
      },
    ],
    machine: [],
    total: { bytes: 22 * GB, files: 6000 },
    candidates: [...backups, ...folders],
    ways,
    freeable: ways.reduce((n, w) => n + w.bytes, 0),
    truncated: false,
    ...over,
  }
}

const offsite = { enabled: true, configured: true, type: 's3', place: 'Backblaze B2', copies: 12, copiesBytes: 7 * GB } as OffsiteView

function finished(detail: Record<string, unknown>, over: Partial<Operation> = {}): Operation {
  return { id: 'op-disk', kind: 'disk-cleanup', status: 'succeeded', phase: '', actor: 'siya', startedAt: '2026-09-25T20:01:00Z', finishedAt: '2026-09-25T20:01:02Z', detail, ...over }
}

/** Answers GETs by path fragment; anything else never resolves. */
function answer(routes: Record<string, unknown>) {
  vi.mocked(client.get).mockImplementation(((path: string) => {
    const hit = Object.entries(routes).find(([part]) => path.includes(part))
    if (!hit) return new Promise(() => {})
    return hit[1] instanceof Error ? Promise.reject(hit[1]) : Promise.resolve(hit[1])
  }) as typeof client.get)
}

const gets = () => vi.mocked(client.get).mock.calls.map(([path]) => path)

let root: Root | undefined

async function render(node = <DiskPage id="m2345abcde" />, ws: Workspace = workspace()): Promise<string> {
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceContext.Provider value={ws}>{node}</WorkspaceContext.Provider>))
  await act(async () => {})
  return text()
}

const text = () => document.body.textContent ?? ''

function button(name: string | RegExp, within: ParentNode = document): HTMLElement {
  const all = [...within.querySelectorAll<HTMLElement>('button')]
  const hit = all.find((b) => (typeof name === 'string' ? b.textContent?.trim() === name : name.test(b.textContent ?? '')))
  if (!hit) throw new Error(`no button ${String(name)} in: ${all.map((b) => b.textContent).join(' | ')}`)
  return hit
}

function row(title: string): HTMLElement {
  const hit = [...document.querySelectorAll<HTMLElement>('li')].find((li) => li.textContent?.includes(title))
  if (!hit) throw new Error(`no row ${title}`)
  return hit
}

function dialog(): HTMLElement {
  const d = document.querySelector<HTMLElement>('[role="dialog"]')
  if (!d) throw new Error('no dialog is open')
  return d
}

async function click(el: HTMLElement) {
  await act(async () => el.click())
  await act(async () => {})
}

// happy-dom forwards a label's click to its input before React sees it, so a
// click on the box would tick it twice; the hidden input takes one click.
function boxes(within: ParentNode): HTMLInputElement[] {
  return [...within.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')]
}

const day = (iso: string) => new Date(iso).toLocaleDateString(formatLocale(), { weekday: 'short', day: 'numeric', month: 'short' })

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

beforeEach(() => {
  vi.spyOn(toastManager, 'add').mockReturnValue('toast')
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  view.phone = false
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.mocked(client.get).mockReset()
  vi.mocked(client.get).mockImplementation(() => new Promise(() => {}))
  vi.mocked(client.post).mockReset()
  vi.mocked(client.post).mockImplementation(() => new Promise(() => {}))
})

describe('Disk space page', () => {
  it('shows a skeleton while the first scan runs', async () => {
    const t = await render()
    expect(t).toContain('Loading…')
    expect(document.querySelectorAll('[data-slot="skeleton"]').length).toBeGreaterThan(0)
    expect(t).not.toContain('Ways to free space')
    expect(gets()[0]).toMatch(/\/api\/machines\/m2345abcde\/disk\?tz=/)
    expect(gets()[0]).not.toContain('fresh=1')
  })

  it('shows the bar, every way to free space and the table', async () => {
    answer({ '/disk?': report(), '/offsite': offsite })
    const t = await render()
    expect(t).toContain('my-vps · 41 GB free of 80 GB')
    expect(t).toContain('39 GB used')
    expect(t).toContain('of 80 GB · 41 GB free')
    expect(document.querySelector('[role="img"][aria-label="What takes up the disk"]')?.children).toHaveLength(6)
    for (const legend of ['Backups18.2 GB', 'Worlds2.0 GB', 'Server files and add-ons1.1 GB', 'Logs1.5 GB', 'Other files on my-vps', 'Free41 GB']) expect(t).toContain(legend)
    expect(t).toContain('Up to 10.6 GB in total.')
    expect(row('Backups beyond your keep rules').textContent).toContain('9 old backups. The newest stay.5.4 GBReview')
    expect(row('Logs older than 30 days').textContent).toContain('From every server1.2 GBDelete')
    expect(row('Crash reports older than 30 days').textContent).toContain('From Survival120 MBDelete')
    expect(row('Server versions nobody uses').textContent).toContain('Paper 26.1.1 build 61, replaced by updates96 MBDelete')
    expect(row('Server folders set aside').textContent).toContain('From Survival and Creative3.5 GBReview')
    expect(row('Unfinished backups and restores').textContent).toContain('Left by a backup, download or restore that stopped310 MBDelete')
    const rows = [...document.querySelectorAll('tbody tr')].map((tr) => [...tr.children].map((c) => c.textContent))
    expect(document.querySelector('thead')?.textContent).toBe('ServerWorldServer filesBackupsLogsTotal')
    expect(rows).toEqual([
      ['Survival', '1.2 GB', '0.6 GB', '11.4 GB', '0.9 GB', '14.1 GB'],
      ['Creative', '0.8 GB', '0.5 GB', '6.8 GB', '50 MB', '7.9 GB'],
    ])
  })

  it('says when there is nothing to free', async () => {
    answer({ '/disk?': report({ ways: [], freeable: 0 }) })
    const t = await render()
    expect(t).toContain('Nothing to free up right now.')
    expect(document.querySelectorAll('ul button')).toHaveLength(0)
  })

  it('shows the reason a disk couldn’t be read and still works from folder sizes', async () => {
    const reason = 'Playkeeper couldn’t read the size of the disk that holds /var/lib/playkeeper (permission denied).'
    answer({ '/disk?': report({ disk: null, problems: [{ code: 'disk_space', path: '/var/lib/playkeeper', text: reason }] }) })
    const t = await render()
    expect(t).toContain('Couldn’t read the disk')
    expect(t).toContain(reason)
    expect(t).toContain('Sizes below still come from the folders.')
    expect(t).not.toContain('39 GB used')
    expect(row('Backups beyond your keep rules').textContent).toContain('5.4 GB')
    expect(document.querySelectorAll('tbody tr')).toHaveLength(2)
    await click(button('Check again'))
    expect(gets().some((p) => p.includes('/disk?') && p.includes('fresh=1'))).toBe(true)
  })

  it('says sizes are at least what they say when a scan stopped at a cap', async () => {
    answer({ '/disk?': report({ truncated: true, problems: [{ code: 'too_many_files', path: '/data', text: 'Stopped counting' }] }) })
    const t = await render()
    expect(t).toContain('At least 39 GB used')
    expect(t).toContain('of 80 GB')
    expect(t).not.toContain('41 GB free ·')
    expect(t).toContain('Stopped counting at a million files in one folder, so sizes are at least what they say.')
    expect(t).not.toContain('Other files on my-vps')
    expect(button('Scan again')).toBeTruthy()
  })

  it('shows why the scan failed and tries again', async () => {
    answer({ '/disk?': new client.ApiError(502, { error: 'The agent isn’t answering.', code: 'agent_down' }) })
    expect(await render()).toContain('The agent isn’t answering.')
    answer({ '/disk?': report() })
    await click(button('Try again'))
    expect(gets().some((p) => p.includes('/disk?') && p.includes('fresh=1'))).toBe(true)
    expect(text()).toContain('39 GB used')
  })

  it('is opened from the machine page’s Disk meter', async () => {
    await render(<MachinePage id="m2345abcde" />)
    const link = document.querySelector<HTMLAnchorElement>('a[href="/machines/m2345abcde/disk"]')
    expect(link?.textContent).toContain('41 GB free')
  })
})

describe('Review before removal', () => {
  it('lists every old backup ticked and deletes only the ticked ones', async () => {
    answer({ '/disk?': report(), '/offsite': offsite })
    vi.mocked(client.post).mockResolvedValue(finished({ freed: 8 * backupSize, deleted: 8 }))
    await render()
    await click(button('Review', row('Backups beyond your keep rules')))
    const d = dialog()
    expect(d.textContent).toContain('Backups beyond your keep rules')
    expect(d.textContent).toContain('9 old backups · 5.4 GB. The newest stay.')
    const labels = [...d.querySelectorAll('li')].map((li) => li.textContent)
    expect(labels).toHaveLength(7)
    expect(labels[0]).toBe(`Survival · ${day(backups[0]!.modifiedAt)}, ${formatClock(backups[0]!.modifiedAt)}614 MB`)
    expect(labels[5]).toContain('Creative · ')
    expect(labels[6]).toBe(`3 more of Creative${day(backups[8]!.modifiedAt)} to ${day(backups[6]!.modifiedAt)}1.8 GB`)
    expect(d.textContent).toContain('Copies on Backblaze B2 stay.')
    expect(client.post).not.toHaveBeenCalled()

    expect([...d.querySelectorAll('[role="checkbox"]')].every((b) => b.getAttribute('aria-checked') === 'true')).toBe(true)
    expect(button(/^Delete 9 · 5\.4 GB$/, d)).toBeTruthy()
    await click(boxes(d)[0]!)
    expect(d.querySelector('[role="checkbox"]')?.getAttribute('aria-checked')).toBe('false')
    await click(button(/^Delete 8 · 4\.8 GB$/, d))

    expect(client.post).toHaveBeenCalledTimes(1)
    const [path, body] = vi.mocked(client.post).mock.calls[0]!
    expect(path).toBe('/api/machines/m2345abcde/disk/clean')
    expect(body).toEqual({ ids: backups.slice(1).map((c) => c.id), timeZone: expect.any(String) })
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(toastManager.add).toHaveBeenCalledWith(expect.objectContaining({ title: 'Freed 4.8 GB', type: 'success' }))
    expect(gets().some((p) => p.includes('/disk?') && p.includes('fresh=1'))).toBe(true)
  })

  it('can’t delete once everything is unticked', async () => {
    answer({ '/disk?': report() })
    await render()
    await click(button('Review', row('Server folders set aside')))
    for (const box of boxes(dialog())) await click(box)
    expect((button('Delete', dialog()) as HTMLButtonElement).disabled).toBe(true)
    await click(button('Cancel', dialog()))
    expect(document.querySelector('[role="dialog"]')).toBeNull()
    expect(client.post).not.toHaveBeenCalled()
  })

  it('says why each set-aside folder was kept', async () => {
    answer({ '/disk?': report() })
    await render()
    await click(button('Review', row('Server folders set aside')))
    const d = dialog()
    expect(d.textContent).toContain('Server folders set aside')
    expect(d.textContent).toContain('By restores and updates · 3.5 GB')
    const labels = [...d.querySelectorAll('li')].map((li) => li.textContent)
    expect(labels).toEqual([
      `Survival · from before a restore on ${formatDate('2026-09-21T12:00:00')}Usually deleted once the restore finishes2.1 GB`,
      `Creative · left by a failed update on ${formatDate('2026-09-18T12:00:00')}The backup from before it was put back1.4 GB`,
    ])
    expect(d.textContent).not.toContain('Copies on')
    expect(button('Delete 2 · 3.5 GB', d)).toBeTruthy()
  })

  it('lists a server’s set-aside folders newest first by the date in their names', async () => {
    const older = { ...setAside(22, survival, 'failed_update', '2026-09-03', 480 * MB), modifiedAt: '2026-09-20T08:00:00Z' }
    const newer = { ...setAside(23, survival, 'replaced', '2026-09-12', 820 * MB), modifiedAt: '2026-09-02T08:00:00Z' }
    answer({ '/disk?': report({ candidates: [older, newer], ways: [way('set_aside', 'review', 1300 * MB, { candidateIds: [older.id, newer.id], serverIds: [survival] })] }) })
    await render()
    await click(button('Review', row('Server folders set aside')))
    const labels = [...dialog().querySelectorAll('li')].map((li) => li.textContent)
    expect(labels).toEqual([
      `Survival · from before a restore on ${formatDate('2026-09-12T12:00:00')}Usually deleted once the restore finishes820 MB`,
      `Survival · left by a failed update on ${formatDate('2026-09-03T12:00:00')}The backup from before it was put back480 MB`,
    ])
  })

  it('confirms a Delete row first and sends the way, not a list of files', async () => {
    answer({ '/disk?': report() })
    vi.mocked(client.post).mockResolvedValue(finished({ freed: Math.round(1.2 * GB), deleted: 40 }))
    await render()
    await click(button('Delete', row('Logs older than 30 days')))
    expect(client.post).not.toHaveBeenCalled()
    expect(dialog().textContent).toContain('Logs older than 30 days')
    expect(dialog().textContent).toContain('From every server')
    await click(button('Cancel', dialog()))
    expect(client.post).not.toHaveBeenCalled()

    await click(button('Delete', row('Logs older than 30 days')))
    await click(button('Delete · 1.2 GB', dialog()))
    expect(vi.mocked(client.post).mock.calls[0]).toEqual(['/api/machines/m2345abcde/disk/clean', { ways: ['old_logs'], timeZone: expect.any(String) }])
    expect(toastManager.add).toHaveBeenCalledWith(expect.objectContaining({ title: 'Freed 1.2 GB', type: 'success' }))
  })

  it('waits for a running clean-up and says what couldn’t be deleted', async () => {
    const problems = [{ code: 'busy', path: '/data/x', text: 'Survival started while cleaning up, so its files stayed.' }]
    answer({ '/disk?': report(), '/operations/op-disk': finished({ freed: GB, deleted: 3, problems, moreProblems: 1 }) })
    vi.mocked(client.post).mockResolvedValue(finished({}, { status: 'running', finishedAt: undefined }))
    await render()
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    await click(button('Delete', row('Unfinished backups and restores')))
    await click(button('Delete · 310 MB', dialog()))
    expect(row('Unfinished backups and restores').querySelector('button[data-loading]')).not.toBeNull()
    expect((button('Review', row('Backups beyond your keep rules')) as HTMLButtonElement).disabled).toBe(true)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000)
    })
    expect(gets()).toContain('/api/machines/m2345abcde/operations/op-disk')
    expect(toastManager.add).toHaveBeenCalledWith(expect.objectContaining({ title: 'Freed 1.0 GB. 2 items couldn’t be deleted.', description: problems[0]!.text, type: 'warning' }))
  })

  it('shows a failed clean-up as an error', async () => {
    answer({ '/disk?': report() })
    vi.mocked(client.post).mockResolvedValue(finished({}, { status: 'failed', error: 'Stop Survival first.', hint: 'Servers must be stopped.' }))
    await render()
    await click(button('Delete', row('Server versions nobody uses')))
    await click(button('Delete · 96 MB', dialog()))
    expect(toastManager.add).toHaveBeenCalledWith(expect.objectContaining({ title: 'Stop Survival first.', description: 'Servers must be stopped.', type: 'error' }))
  })
})

describe('Disk space on a phone', () => {
  beforeEach(() => {
    view.phone = true
  })

  it('shows rows with a title and size, and no table', async () => {
    answer({ '/disk?': report() })
    const t = await render()
    expect(t).toContain('Ways to free space · up to 10.6 GB')
    expect(t).toContain('39 GB usedof 80 GB')
    expect(t).not.toContain('By server')
    expect(button(/^Backups beyond your keep rules5\.4 GB$/)).toBeTruthy()
    expect(button(/^Logs older than 30 days1\.2 GB$/)).toBeTruthy()
  })

  it('opens the review as a sheet with the rest of each server on one row', async () => {
    answer({ '/disk?': report(), '/offsite': offsite })
    await render()
    await click(button(/^Backups beyond your keep rules/))
    const d = dialog()
    const labels = [...d.querySelectorAll('li')].map((li) => li.textContent)
    expect(labels).toHaveLength(6)
    expect(labels[5]).toBe('4 more of Creative2.4 GB')
    expect(d.textContent).toContain('Copies on Backblaze B2 stay.')
    expect(button('Delete 9 · 5.4 GB', d)).toBeTruthy()
  })

  it('opens a confirm for rows that aren’t reviewed item by item', async () => {
    answer({ '/disk?': report() })
    await render()
    await click(button(/^Unfinished backups and restores/))
    expect(dialog().textContent).toContain('Left by a backup, download or restore that stopped')
    expect(button('Delete · 310 MB', dialog())).toBeTruthy()
    expect(client.post).not.toHaveBeenCalled()
  })

  it('keeps the server tabs with More chosen, since the machine opens from More', async () => {
    answer({ '/disk?': report() })
    const survivalServer = { id: survival, name: 'Survival', slug: 'survival', phase: 'online' } as ServerStatus
    await render(
      <AppShell route={{ name: 'machine', id: 'm2345abcde', sub: 'disk' }}>
        <DiskPage id="m2345abcde" />
      </AppShell>,
      workspace({ servers: [survivalServer] }),
    )
    const tabs = document.querySelector('nav[aria-label="Server pages"]')
    expect(tabs?.querySelector('[aria-current="page"]')?.textContent).toBe('More')
    expect(tabs?.querySelector('a[href="/servers/survival"]')).not.toBeNull()
  })

  it('keeps the unreadable disk short', async () => {
    answer({ '/disk?': report({ disk: null, problems: [{ code: 'disk_space', path: '/var/lib/playkeeper', text: 'Playkeeper couldn’t read the size of the disk.' }] }) })
    const t = await render()
    expect(t).toContain('Couldn’t read the disk')
    expect(t).not.toContain('Sizes below still come from the folders.')
    expect(button('Check again')).toBeTruthy()
  })
})
