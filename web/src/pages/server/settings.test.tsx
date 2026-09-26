// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Action, MachineView, Me, OffsiteView, ServerConfig, ServerStatus } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import { ServerSettingsPage } from './settings'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  api: vi.fn(() => new Promise(() => {})),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(() => Promise.resolve({})),
  del: vi.fn(() => Promise.resolve({})),
}))

const everything: Action[] = ['view', 'account.manage', 'servers.run', 'servers.console', 'players.manage', 'backups.make', 'backups.restore', 'servers.manage', 'servers.create', 'team.manage', 'machine.manage', 'audit.view', 'backups.copies.manage', 'backups.recovery_key', 'backups.recover']
const me: Me = {
  user: { username: 'siya', role: 'owner' },
  csrfToken: 't',
  expiresAt: '2026-09-26T00:00:00Z',
  idleTimeoutSeconds: 43200,
  version: '0.3.0',
  access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can: everything },
}
const machine = { id: 'm2345abcde', projectId: 'p2345abcde', name: 'my-vps', kind: 'local' } as MachineView
const config = { versionId: 'paper-26.1.2', minecraftVersion: '26.1.2', paperBuild: 74, memoryMB: 1536, heapMB: 1024, levelName: 'world', motd: 'Hi', maxPlayers: 10, whitelist: true, playStyle: 'friends' } as ServerConfig
const server: ServerStatus = {
  id: 'abcdefghjk',
  name: 'Survival',
  slug: 'survival',
  game: 'minecraft-java',
  type: 'paper',
  createdAt: '2026-09-20T10:00:00Z',
  exists: true,
  desired: 'running',
  phase: 'online',
  reachable: true,
  gamePort: 25565,
  offlineModeTest: false,
  crashCount: 0,
  pendingRestart: false,
  gameplay: {},
  config,
  firstSteps: { backedUp: false, downloaded: false },
}
const ws: Workspace = {
  me,
  servers: [server],
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
}

function offsite(over: Partial<OffsiteView> = {}): OffsiteView {
  return {
    enabled: true,
    configured: true,
    type: 's3',
    place: 'Backblaze B2',
    key: { recipient: 'age1examplerecipient', createdAt: '2026-09-20T10:00:00Z', oldKeys: 0, fileName: 'playkeeper-recovery-key-survival.txt' },
    copies: 3,
    copiesBytes: 3 * 2 ** 30,
    queued: 0,
    providers: [],
    ...over,
  }
}

let root: Root | undefined

async function render(view: OffsiteView) {
  vi.mocked(client.get).mockImplementation(((path: string) => {
    if (path.endsWith('/offsite')) return Promise.resolve(view)
    if (path.endsWith('/backups')) return Promise.resolve([])
    return new Promise(() => {})
  }) as typeof client.get)
  if (root) await act(async () => root?.unmount())
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<WorkspaceContext.Provider value={ws}>{<ServerSettingsPage server={server} />}</WorkspaceContext.Provider>))
  await act(async () => {})
}

function button(label: string): HTMLButtonElement {
  const b = [...document.querySelectorAll('button')].find((el) => el.textContent?.trim() === label)
  if (!b) throw new Error(`no button ${label}`)
  return b
}

async function click(el: HTMLElement) {
  await act(async () => el.click())
  await act(async () => {})
}

/** Opens the delete dialog and types the server's name. */
async function openAndType() {
  await click(button('Delete server…'))
  const input = [...document.querySelectorAll('label')].find((l) => l.textContent?.includes('to confirm'))?.querySelector('input')
  if (!input) throw new Error('no name field')
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
  await act(async () => {
    setValue?.call(input, 'Survival')
    input.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

function deletes(): unknown[] {
  return vi
    .mocked(client.post)
    .mock.calls.filter(([path]) => String(path).endsWith('/delete'))
    .map(([, body]) => body)
}

const warning = 'Only the recovery key opens the 3 copies of Survival on Backblaze B2, and deleting Survival deletes the key.'

afterEach(async () => {
  if (root) await act(async () => root?.unmount())
  root = undefined
  vi.mocked(client.get).mockReset()
  vi.mocked(client.post).mockReset()
})

describe('deleting a server with copies somewhere else', () => {
  it('warns while the recovery key was never downloaded, and deletes without it only once told to', async () => {
    await render(offsite())
    await openAndType()
    const text = document.body.textContent ?? ''
    expect(text).toContain('Its recovery key was never downloaded.')
    expect(text).toContain(warning)
    expect(button('Download recovery key').disabled).toBe(false)
    const go = button('Delete Survival')
    expect(go.disabled).toBe(true)
    expect(go.title).toBe('Download the recovery key first, or tick the box to delete without it.')
    const box = [...document.querySelectorAll('label')].find((l) => l.textContent?.includes('Delete it without the key'))
    if (!box) throw new Error('no box to delete without the key')
    await click(box)
    expect(button('Delete Survival').disabled).toBe(false)
    vi.mocked(client.post).mockResolvedValue({})
    await click(button('Delete Survival'))
    expect(deletes()).toEqual([{ confirm: 'Survival', forgetKey: true }])
  })

  it('warns while copies are on but none was made yet', async () => {
    await render(offsite({ copies: 0 }))
    await openAndType()
    expect(document.body.textContent).toContain('Copies of Survival go to Backblaze B2, and only its recovery key opens them.')
    expect(button('Delete Survival').disabled).toBe(true)
  })

  it('doesn’t warn once the key was downloaded', async () => {
    await render(offsite({ key: { ...offsite().key!, savedAt: '2026-09-21T10:00:00Z' } }))
    await openAndType()
    expect(document.body.textContent).not.toContain('never downloaded')
    vi.mocked(client.post).mockResolvedValue({})
    await click(button('Delete Survival'))
    expect(deletes()).toEqual([{ confirm: 'Survival' }])
  })

  it('shows the agent’s refusal when the page didn’t know the key was at risk', async () => {
    await render(offsite({ enabled: false, copies: 0 }))
    await openAndType()
    expect(document.body.textContent).not.toContain('never downloaded')
    const refusal = new client.ApiError(409, {
      error: 'Deleting Survival deletes its recovery key, which was never downloaded.',
      code: 'conflict',
      reason: 'recovery_key_not_saved',
      params: { place: 'Backblaze B2', copies: 3 },
    })
    vi.mocked(client.post).mockRejectedValueOnce(refusal).mockResolvedValue({})
    await click(button('Delete Survival'))
    expect(document.body.textContent).toContain('Its recovery key was never downloaded.')
    expect(button('Delete Survival').disabled).toBe(true)
    const box = [...document.querySelectorAll('label')].find((l) => l.textContent?.includes('Delete it without the key'))
    if (!box) throw new Error('no box to delete without the key')
    await click(box)
    await click(button('Delete Survival'))
    expect(deletes()).toEqual([{ confirm: 'Survival' }, { confirm: 'Survival', forgetKey: true }])
  })
})
