// @vitest-environment happy-dom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { useAIKeys, type AIKeysState } from '@/api/ai-keys'
import * as client from '@/api/client'
import type { Action, AIKeys, MachineView, Me, ServerConfig, ServerStatus } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import { toastManager } from '@/components/ui/toast'
import { AIKeyEditor, AIKeyNotice } from './ai-key'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  put: vi.fn(() => new Promise(() => {})),
  del: vi.fn(() => new Promise(() => {})),
}))

const admin: Action[] = ['view', 'servers.run', 'servers.manage', 'files.view', 'files.edit']
const machine = { id: 'm2345abcde', projectId: 'p2345abcde', name: 'my-vps', kind: 'local' } as MachineView
const config = { versionId: 'paper-26.2', minecraftVersion: '26.2', paperBuild: 12, memoryMB: 3072, heapMB: 2048, levelName: 'world', motd: 'Hi', maxPlayers: 10, whitelist: true } as ServerConfig

function me(can: Action[] = admin): Me {
  return {
    user: { username: 'siya', role: 'owner' },
    csrfToken: 't',
    expiresAt: '2026-10-01T00:00:00Z',
    idleTimeoutSeconds: 43200,
    version: '0.4.9',
    access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can },
  }
}

function server(over: Partial<ServerStatus> = {}): ServerStatus {
  return {
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
    firstSteps: { backedUp: true, downloaded: true },
    ...over,
  }
}

function workspace(can?: Action[]): Workspace {
  return {
    me: me(can),
    servers: [server()],
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
}

const key = 'sk-or-v1-' + '0123456789abcdef'.repeat(4)
const keysPath = '/api/servers/abcdefghjk/ai-keys'
const keys = (set: boolean, over: Partial<AIKeys> = {}): AIKeys => ({ keys: { openrouter: { set } }, pending: false, available: true, ...over })
const busy = { id: 'b1', kind: 'backup', status: 'running', phase: '', actor: 'siya', startedAt: '' } as const

let root: Root | undefined

async function render(node: ReactNode, ws = workspace()): Promise<string> {
  await act(async () => root?.unmount())
  document.body.innerHTML = ''
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await rerender(node, ws)
  return document.body.textContent ?? ''
}

async function rerender(node: ReactNode, ws = workspace()) {
  await act(async () => root?.render(<WorkspaceContext.Provider value={ws}>{node}</WorkspaceContext.Provider>))
  await settle()
}

async function settle() {
  await act(async () => {})
  await act(async () => {})
}

/** Answers the keys' read with each of reads in turn, the last one from then on. */
function answer(...reads: (AIKeys | Error | Promise<AIKeys>)[]) {
  vi.mocked(client.get).mockImplementation(((path: string) => {
    if (path !== keysPath) return new Promise(() => {})
    const next = reads.length > 1 ? reads.shift() : reads[0]
    return next instanceof Error ? Promise.reject(next) : Promise.resolve(next)
  }) as typeof client.get)
}

function button(text: string): HTMLButtonElement {
  const found = [...document.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === text)
  if (!found) throw new Error(`no button ${text}`)
  return found
}

const hasButton = (text: string) => [...document.querySelectorAll('button')].some((b) => b.textContent?.trim() === text)
const field = () => document.querySelector<HTMLInputElement>('input[aria-label="OpenRouter key"]')!
const line = () => document.getElementById(field().getAttribute('aria-describedby') ?? '')

async function press(el: HTMLElement) {
  await act(async () => el.click())
  await settle()
}

async function type(value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(field(), value)
    field().dispatchEvent(new Event('input', { bubbles: true }))
  })
}

function phone() {
  return vi.spyOn(window, 'matchMedia').mockImplementation(
    (query: string) => ({ matches: query.includes('max-width: 639px'), media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {} }) as unknown as MediaQueryList,
  )
}

/** The editor with the keys' real state, as the Plugins tab has it. */
function Editor({ s = server() }: { s?: ServerStatus }) {
  const state = useAIKeys(s.id, true, s.phase)
  return <AIKeyEditor server={s} keys={state} provider="openrouter" phone={false} />
}

function fixed(k: AIKeys | undefined, over: Partial<AIKeysState> = {}): AIKeysState {
  return { keys: k, error: undefined, save: vi.fn(() => Promise.resolve(keys(true))), remove: vi.fn(() => Promise.resolve(keys(false))), ...over }
}

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
  vi.restoreAllMocks()
  for (const fn of [client.get, client.put, client.del]) {
    vi.mocked(fn).mockReset()
    vi.mocked(fn).mockImplementation(() => new Promise(() => {}))
  }
})

describe('AIKeyEditor', () => {
  it('saves a key once and never shows it again: the field empties, says it’s saved, and Remove key deletes it', async () => {
    answer(keys(false))
    vi.mocked(client.put).mockResolvedValue(keys(true))
    vi.mocked(client.del).mockResolvedValue(keys(false))
    await render(<Editor />)
    expect(field().placeholder).toBe('Paste your OpenRouter key')
    expect(line()?.textContent).toBe('/aibuild spends your OpenRouter credits with it.')
    expect(hasButton('Remove key')).toBe(false)
    const getKey = [...document.querySelectorAll('a')].find((a) => a.textContent === 'Get a key')
    expect(getKey?.getAttribute('href')).toBe('https://openrouter.ai/keys')
    expect(getKey?.getAttribute('target')).toBe('_blank')
    expect(getKey?.getAttribute('rel')).toContain('noreferrer')

    await type(`  ${key}\n`)
    await press(button('Save'))
    expect(client.put).toHaveBeenCalledWith(`${keysPath}/openrouter`, { key })
    expect(toastManager.add).toHaveBeenCalledWith(expect.objectContaining({ title: 'OpenRouter key saved', type: 'success', description: undefined }))
    expect(field().value).toBe('')
    expect(field().placeholder).toBe('Saved · type to replace')
    expect(document.body.innerHTML).not.toContain(key)
    expect(document.body.innerHTML).not.toContain(key.slice(-8))

    await press(button('Remove key'))
    expect(client.del).toHaveBeenCalledWith(`${keysPath}/openrouter`)
    expect(toastManager.add).toHaveBeenCalledWith(expect.objectContaining({ title: 'OpenRouter key removed', type: 'success' }))
    expect(field().placeholder).toBe('Paste your OpenRouter key')
    expect(hasButton('Remove key')).toBe(false)
  })

  it('shows the save under way at once, and takes no second save or a removal meanwhile', async () => {
    answer(keys(true))
    let finish: (k: AIKeys) => void = () => {}
    vi.mocked(client.put).mockReturnValue(new Promise<AIKeys>((resolve) => (finish = resolve)))
    await render(<Editor />)
    await type(key)
    await press(button('Save'))
    expect(button('Save').hasAttribute('data-loading')).toBe(true)
    expect(button('Save').disabled).toBe(true)
    expect(button('Remove key').disabled).toBe(true)
    await act(async () => finish(keys(true)))
    await settle()
    expect(button('Save').hasAttribute('data-loading')).toBe(false)
    expect(client.put).toHaveBeenCalledTimes(1)
  })

  it('checks a key as the agent does before sending it, by the field, without quoting it', async () => {
    await render(<AIKeyEditor server={server()} keys={fixed(keys(false))} provider="openrouter" phone={false} />)
    expect(button('Save').disabled).toBe(true)
    expect(button('Save').title).toBe('Paste a key first.')

    const other = 'sk-proj-' + 'x'.repeat(40)
    await type(other)
    await press(button('Save'))
    expect(line()?.textContent).toBe('That isn’t an OpenRouter key: those start with sk-or-.')
    expect(line()?.getAttribute('role')).toBe('alert')
    expect(field().getAttribute('aria-invalid')).toBe('true')
    expect(document.body.textContent).not.toContain(other)

    await type('sk-or-v1-short')
    expect(line()?.textContent, 'typing clears it').toBe('/aibuild spends your OpenRouter credits with it.')
    await press(button('Save'))
    expect(line()?.textContent).toBe('That key is too short. Copy all of it from openrouter.ai/keys.')
    expect(toastManager.add).not.toHaveBeenCalled()
  })

  it('puts the agent’s refusal by the field in the owner’s words, anything else in a toast, and keeps what was typed', async () => {
    const save = vi.fn<AIKeysState['save']>()
    await render(<AIKeyEditor server={server()} keys={fixed(keys(false), { save })} provider="openrouter" phone={false} />)
    await type(key)
    save.mockRejectedValueOnce(new client.ApiError(400, { error: 'That key is too long. Copy only the key from openrouter.ai/keys.', code: 'invalid_request', field: 'key', reason: 'ai_key_long' }))
    await press(button('Save'))
    expect(line()?.textContent).toBe('That key is too long. Copy only the key from openrouter.ai/keys.')
    expect(field().value).toBe(key)

    save.mockRejectedValueOnce(new client.ApiError(409, { error: 'Backing up Survival. Try again when it’s done.', code: 'busy' }))
    await press(button('Save'))
    expect(toastManager.add).toHaveBeenCalledWith(expect.objectContaining({ title: 'Backing up Survival. Try again when it’s done.', type: 'error' }))
    expect(line()?.textContent).toBe('/aibuild spends your OpenRouter credits with it.')
    expect(field().value).toBe(key)
    expect(save).toHaveBeenCalledWith('openrouter', key)
  })

  it('says a saved key applies after a restart while the running server can’t see it yet', async () => {
    const save = vi.fn(() => Promise.resolve(keys(true, { pending: true })))
    await render(<AIKeyEditor server={server()} keys={fixed(keys(true, { pending: true }), { save })} provider="openrouter" phone={false} />)
    expect(line()?.textContent).toBe('Applies after a restart')
    expect(field().placeholder).toBe('Saved · type to replace')
    await type(key)
    await press(button('Save'))
    expect(toastManager.add).toHaveBeenCalledWith(expect.objectContaining({ title: 'OpenRouter key saved', description: 'Applies after a restart' }))
  })

  it('waits while the server is busy, and while its keys load', async () => {
    await render(<AIKeyEditor server={server({ operation: busy })} keys={fixed(keys(true))} provider="openrouter" phone={false} />)
    await type(key)
    for (const name of ['Save', 'Remove key']) {
      expect(button(name).disabled).toBe(true)
      expect(button(name).title).toBe('Backing up Survival. Try again when it’s done.')
    }

    await render(<AIKeyEditor server={server()} keys={fixed(undefined)} provider="openrouter" phone={false} />)
    expect(field().disabled).toBe(true)
    expect(field().placeholder).toBe('Loading…')

    const error = new client.ApiError(502, { error: 'Survival’s machine didn’t answer.', code: 'agent_unreachable' })
    await render(<AIKeyEditor server={server()} keys={{ ...fixed(undefined), error }} provider="openrouter" phone={false} />)
    expect(field().disabled).toBe(false)
    expect(line()?.textContent).toBe('Survival’s machine didn’t answer.')
  })
})

describe('AIKeyNotice', () => {
  it('asks for the key on one line while the server has AI Build Battle and no key, and saves one from its dialog', async () => {
    answer(keys(false))
    vi.mocked(client.put).mockResolvedValue(keys(true))
    const text = await render(<AIKeyNotice server={server()} />)
    expect(text).toBe('AI Build Battle needs your OpenRouter keyAdd key')
    expect(document.querySelector('[role=alert]')).toBeNull()

    await press(button('Add key'))
    const dialog = document.querySelector('[role=dialog]')
    expect(dialog?.getAttribute('data-slot')).toBe('dialog-popup')
    expect(dialog?.textContent).toContain('Add your OpenRouter key')
    await type(key)
    await press(button('Save'))
    expect(client.put).toHaveBeenCalledWith(`${keysPath}/openrouter`, { key })
    expect(document.querySelector('[role=dialog]')).toBeNull()
    expect(document.body.textContent).toBe('')
    expect(document.body.innerHTML).not.toContain(key)
  })

  it('opens as a sheet from the bottom on a phone', async () => {
    const media = phone()
    answer(keys(false))
    await render(<AIKeyNotice server={server()} />)
    await press(button('Add key'))
    expect(document.querySelector('[role=dialog]')?.getAttribute('data-slot')).toBe('sheet-popup')
    expect(document.querySelector('[data-slot=dialog-popup]')).toBeNull()
    media.mockRestore()
  })

  it('stays quiet with a key, without the plugin, when the keys can’t be read, and asks nothing for an account that can’t change files', async () => {
    for (const read of [keys(true), keys(false, { available: false }), new client.ApiError(502, { error: 'No answer.', code: 'agent_unreachable' })]) {
      answer(read)
      expect(await render(<AIKeyNotice server={server()} />)).toBe('')
    }
    vi.mocked(client.get).mockClear()
    answer(keys(false))
    expect(await render(<AIKeyNotice server={server()} />, workspace(['view', 'servers.run', 'files.view']))).toBe('')
    expect(client.get).not.toHaveBeenCalledWith(keysPath)
  })
})

describe('useAIKeys', () => {
  it('reads the keys again when the server’s phase changes, and keeps what a save answered over a read that set off before it', async () => {
    let late: (k: AIKeys) => void = () => {}
    answer(
      keys(false),
      new Promise<AIKeys>((resolve) => (late = resolve)),
    )
    vi.mocked(client.put).mockResolvedValue(keys(true))
    await render(<Editor />)
    await rerender(<Editor s={server({ phase: 'starting' })} />)
    expect(client.get).toHaveBeenCalledTimes(2)
    await type(key)
    await press(button('Save'))
    expect(field().placeholder).toBe('Saved · type to replace')
    await act(async () => late(keys(false)))
    await settle()
    expect(field().placeholder).toBe('Saved · type to replace')
  })
})
