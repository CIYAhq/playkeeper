// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, AuditEntry, MachineView, Me } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import { GlobalSettingsPage } from './settings'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
}))

const everything: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view', 'backups.copies.manage', 'backups.recovery_key', 'backups.recover']
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
  it.each([
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
  ])('$name', async ({ machines, rows, shown }) => {
    const errors = vi.spyOn(console, 'error')
    vi.mocked(client.get).mockImplementation(((path: string) => (path === '/api/audit' ? Promise.resolve(rows) : new Promise(() => {}))) as typeof client.get)
    const r = createRoot(document.body.appendChild(document.createElement('div')))
    root = r
    await act(async () => r.render(<WorkspaceContext.Provider value={workspace(machines)}>{<GlobalSettingsPage page={{ name: 'settings' }} />}</WorkspaceContext.Provider>))
    await act(async () => {})
    const cells = [...document.querySelectorAll('#audit tbody tr')].map((tr) => [...tr.querySelectorAll('td')].map((td) => td.textContent ?? ''))
    expect(cells.map((c) => [c[2], c[3]])).toEqual(shown)
    expect(errors.mock.calls.filter((args) => args.join(' ').includes('same key'))).toEqual([])
  })
})
