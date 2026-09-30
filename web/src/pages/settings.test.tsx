// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, AuditEntry, MachineView, Me, UpdateInfo, UsageStatsView } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import { GlobalSettingsPage } from './settings'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
  put: vi.fn(() => Promise.resolve({})),
}))

const everything: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view', 'backups.copies.manage', 'backups.recovery_key', 'backups.recover', 'machines.view']
const me: Me = {
  user: { username: 'siya', role: 'owner' },
  csrfToken: 't',
  expiresAt: '2026-09-26T00:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.4.0',
  access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can: everything },
}
const local = { id: 'm2345abcde', projectId: 'p2345abcde', name: 'my-vps', kind: 'local' } as MachineView
const remote = { id: 'r2345abcde', projectId: 'p2345abcde', name: 'home-server', kind: 'remote' } as MachineView

function workspace(machines: MachineView[]): Workspace {
  return {
    me,
    servers: [],
    serversError: undefined,
    machine: local,
    machines,
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
  }
}

/** An audit row numbered 57, as every agent numbers one of its rows. */
const row = (over: Partial<AuditEntry>): AuditEntry => ({ id: 57, ts: '2026-09-24T11:00:00Z', actor: 'siya', action: 'server.start', result: 'succeeded', source: 'agent', ...over })

let root: Root | undefined

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  vi.restoreAllMocks()
})

describe('Audit log', () => {
  for (const tc of [
    {
      name: 'rows numbered alike on the dashboard and two machines each show, with their machine',
      machines: [local, remote],
      rows: [row({ source: 'panel', action: 'login' }), row({ machineId: local.id, action: 'backup.created' }), row({ machineId: remote.id, action: 'server.stop' })],
      shown: [['login', ''], ['backup.created', 'on my-vps'], ['server.stop', 'on home-server']],
    },
    {
      name: 'with one machine, its rows don’t name it',
      machines: [local],
      rows: [row({ source: 'panel', action: 'login' }), row({ machineId: local.id, action: 'backup.created' })],
      shown: [['login', ''], ['backup.created', '']],
    },
  ]) {
    it(tc.name, async () => {
      const errors = vi.spyOn(console, 'error')
      vi.mocked(client.get).mockImplementation(((path: string) => (path === '/api/audit' ? Promise.resolve(tc.rows) : new Promise(() => {}))) as typeof client.get)
      const r = createRoot(document.body.appendChild(document.createElement('div')))
      root = r
      await act(async () => r.render(<WorkspaceContext.Provider value={workspace(tc.machines)}>{<GlobalSettingsPage page={{ name: 'settings' }} />}</WorkspaceContext.Provider>))
      await act(async () => {})
      const cells = [...document.querySelectorAll('#audit tbody tr')].map((tr) => [...tr.querySelectorAll('td')].map((td) => td.textContent ?? ''))
      expect(cells.map((c) => [c[2], c[3]])).toEqual(tc.shown)
      expect(errors.mock.calls.filter((args) => args.join(' ').includes('same key'))).toEqual([])
    })
  }
})

const report = { id: '00112233445566778899aabbccddeeff', version: '0.4.4', os: 'ubuntu', osVersion: '24.04', arch: 'amd64', source: 'playkeeper.io', kind: 'dashboard', address: 'ip', servers: 2, running: 1 } as const
const usage = (over: Partial<UsageStatsView>): UsageStatsView => ({ on: true, reason: 'default', canChange: true, service: 'https://stats.playkeeper.io', report, machines: [], ...over })

async function renderSettings(view: UsageStatsView, who: Me = me) {
  vi.mocked(client.get).mockImplementation(((path: string) => (path === '/api/usage-stats' ? Promise.resolve(view) : new Promise(() => {}))) as typeof client.get)
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceContext.Provider value={{ ...workspace([local, remote]), me: who }}>{<GlobalSettingsPage page={{ name: 'settings' }} />}</WorkspaceContext.Provider>))
  await act(async () => {})
}

const card = () => document.querySelector('#usage-stats') as HTMLElement
const toggle = () => card().querySelector('[role="switch"]') as HTMLElement | null

describe('Usage stats', () => {
  it('turns them off on every machine with one switch, and shows exactly what is sent', async () => {
    vi.mocked(client.put).mockResolvedValue(usage({ on: false, reason: 'settings', machines: [{ id: remote.id, name: 'home-server', stats: usage({ on: false, reason: 'settings' }) }] }))
    await renderSettings(usage({ lastSent: new Date(Date.now() - 3 * 3600_000).toISOString(), machines: [{ id: remote.id, name: 'home-server', stats: usage({}) }] }))
    expect(card().textContent).toContain('On · sent twice a day, last sent 3 h ago')
    expect(card().querySelector('li')?.textContent).toBe('home-serverOn')
    await act(async () => toggle()?.click())
    expect(client.put).toHaveBeenCalledWith('/api/usage-stats', { on: false })
    expect(card().textContent).toContain('Off · nothing is sent')
    expect(card().querySelector('li')?.textContent).toBe('home-serverOff')
    await act(async () => (card().querySelector('button:not([role="switch"])') as HTMLElement).click())
    expect(JSON.parse(card().querySelector('pre')?.textContent ?? '{}')).toEqual(report)
  })

  for (const tc of [
    { name: 'DO_NOT_TRACK for the agent', view: usage({ on: false, reason: 'env', variable: 'DO_NOT_TRACK', canChange: false }), line: 'Off · DO_NOT_TRACK is set for Playkeeper on this machine' },
    { name: 'an off chosen when it was installed', view: usage({ on: false, reason: 'install', canChange: false }), line: 'Off · turned off when Playkeeper was installed' },
    { name: 'playkeeper dev', view: usage({ on: false, reason: 'dev', canChange: false }), line: 'Off · playkeeper dev never sends them' },
  ]) {
    it(`keeps the switch still and says why for ${tc.name}`, async () => {
      await renderSettings(tc.view)
      expect(card().textContent).toContain(tc.line)
      expect(toggle()?.getAttribute('aria-disabled') === 'true' || toggle()?.hasAttribute('data-disabled')).toBe(true)
      expect(toggle()?.getAttribute('title')).toBe(tc.line)
      await act(async () => toggle()?.click())
      expect(client.put).not.toHaveBeenCalled()
    })
  }

  it('shows the state but no switch to those who can’t manage the machine', async () => {
    const viewer: Me = { ...me, user: { username: 'pia', role: 'member' }, access: { ...me.access, role: 'viewer', can: ['view', 'account.manage', 'machines.view'] } }
    await renderSettings(usage({}), viewer)
    expect(card().textContent).toContain('On · sent a minute after Playkeeper starts, then twice a day')
    expect(toggle()).toBeNull()
  })
})

const update = (over: Partial<UpdateInfo>): UpdateInfo => ({ current: '0.4.8', supported: true, latest: '0.4.8', available: false, checkedAt: new Date(Date.now() - 12 * 60_000).toISOString(), autoCheck: true, ...over })

async function renderUpdates(view: UpdateInfo, who: Me = me) {
  vi.mocked(client.get).mockImplementation(((path: string) => (path === `/api/machines/${local.id}/update` ? Promise.resolve(view) : new Promise(() => {}))) as typeof client.get)
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceContext.Provider value={{ ...workspace([local]), me: who }}>{<GlobalSettingsPage page={{ name: 'settings' }} />}</WorkspaceContext.Provider>))
  await act(async () => {})
}

const updates = () => document.querySelector('#updates') as HTMLElement
const autoSwitch = () => updates().querySelector('[role="switch"]') as HTMLElement | null

describe('Check for updates automatically', () => {
  it('is on unless turned off, says in one line what it does, and turns off with its switch', async () => {
    vi.mocked(client.put).mockResolvedValue(update({ autoCheck: false }))
    await renderUpdates(update({}))
    expect(updates().textContent).toContain('Check for updates automaticallyAsks playkeeper.io about every 30 minutes whether a new version is out.')
    expect(autoSwitch()?.getAttribute('aria-checked')).toBe('true')
    await act(async () => autoSwitch()?.click())
    expect(client.put).toHaveBeenCalledWith(`/api/machines/${local.id}/update/auto`, { on: false })
    expect(autoSwitch()?.getAttribute('aria-checked')).toBe('false')
  })

  it('is off when it was turned off', async () => {
    await renderUpdates(update({ autoCheck: false }))
    expect(autoSwitch()?.getAttribute('aria-checked')).toBe('false')
  })

  it('isn’t there for those who can’t manage the machine, or where this build can’t update', async () => {
    const viewer: Me = { ...me, user: { username: 'pia', role: 'member' }, access: { ...me.access, role: 'viewer', can: ['view', 'account.manage', 'machines.view'] } }
    await renderUpdates(update({}), viewer)
    expect(updates().textContent).toContain('You have the latest version')
    expect(updates().textContent).not.toContain('Check for updates automatically')
    await act(async () => root?.unmount())
    document.body.innerHTML = ''
    await renderUpdates(update({ supported: false, reason: 'This is a development build (dev); it does not install releases.' }))
    expect(updates().textContent).toContain('This is a development build')
    expect(autoSwitch()).toBeNull()
  })
})
