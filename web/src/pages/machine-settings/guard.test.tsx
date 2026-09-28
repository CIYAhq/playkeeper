// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { Me, NetworkGuard } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import { toastManager } from '@/components/ui/toast'
import { NetworkGuardSettings } from './guard'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  post: vi.fn(),
}))

function me(can: string[]): Me {
  return {
    user: { username: 'siya', role: 'owner' },
    access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can },
    csrfToken: 't',
    expiresAt: '2026-09-26T00:00:00Z',
    idleTimeoutSeconds: 43200,
    version: '0.4.5',
  } as Me
}

let root: Root | undefined

async function show(guard: NetworkGuard, can = ['view', 'machine.manage']) {
  const ws = { me: me(can) } as unknown as Workspace
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () =>
    r.render(
      <WorkspaceContext.Provider value={ws}>
        <NetworkGuardSettings id="m1" guard={guard} />
      </WorkspaceContext.Provider>,
    ),
  )
}

const text = () => document.body.textContent ?? ''
function toggle(): HTMLElement {
  const el = document.querySelector<HTMLElement>('[role="switch"]')
  if (!el) throw new Error('no switch')
  return el
}

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  vi.mocked(client.post).mockReset()
  vi.restoreAllMocks()
})

describe('servers and this machine', () => {
  it('keeps servers away from the machine when the owner turns it on', async () => {
    vi.mocked(client.post).mockResolvedValue({ on: true, host: true })
    await show({ on: true, host: false })
    expect(text()).toContain('Keep servers away from this machine')
    expect(text()).toContain('Leave it off if a plugin uses a database on this VPS.')
    expect(text()).toContain('Servers never reach the cloud’s metadata service')
    expect(toggle().getAttribute('aria-checked')).toBe('false')
    await act(async () => toggle().click())
    expect(client.post).toHaveBeenCalledWith('/api/machines/m1/network-guard', { host: true })
    expect(toggle().getAttribute('aria-checked')).toBe('true')
  })

  it('says why it stays on while there are creators', async () => {
    vi.mocked(client.post).mockRejectedValue(new client.ApiError(409, { error: 'Servers stay away from this machine while it has creators.', code: 'conflict' }))
    const add = vi.spyOn(toastManager, 'add')
    await show({ on: true, host: true })
    await act(async () => toggle().click())
    expect(client.post).toHaveBeenCalledWith('/api/machines/m1/network-guard', { host: false })
    expect(add).toHaveBeenCalledWith(expect.objectContaining({ title: expect.stringContaining('while it has creators') }))
    expect(toggle().getAttribute('aria-checked')).toBe('true')
  })

  it('says when its rules are not in place', async () => {
    await show({ on: false, host: true, problem: 'the iptables command isn’t installed' })
    expect(text()).toContain('Its firewall rules aren’t in place: the iptables command isn’t installed')
  })

  it('is the owner’s to change', async () => {
    await show({ on: true, host: false }, ['view'])
    expect(toggle().hasAttribute('data-disabled')).toBe(true)
  })
})
