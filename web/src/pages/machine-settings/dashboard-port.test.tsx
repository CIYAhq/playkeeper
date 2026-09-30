// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import type { DashboardPortView, Me } from '@/api/types'
import { WorkspaceContext, type Workspace } from '@/api/workspace'
import { DashboardPortSettings } from './dashboard-port'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(),
  put: vi.fn(),
  post: vi.fn(),
}))

const url = 'https://beta.playkeeper.me'
const old = 'https://beta.playkeeper.me:8443'

function me(can: string[]): Me {
  return {
    user: { username: 'siya', role: 'owner' },
    access: { projectId: 'p2345abcde', role: 'admin', servers: { all: true }, twoFactor: false, can },
    csrfToken: 't',
    expiresAt: '2026-09-26T00:00:00Z',
    idleTimeoutSeconds: 43200,
    version: '0.4.11',
  } as Me
}

function view(over: Partial<DashboardPortView>): DashboardPortView {
  return { on: true, state: 'open', port: 8443, url, old, panelPort: 8443, outside: [], ...over }
}

let root: Root | undefined

async function show(v: DashboardPortView | DashboardPortView[], can = ['view', 'machines.view', 'machine.manage']) {
  const answers = Array.isArray(v) ? [...v] : [v]
  vi.mocked(client.get).mockImplementation(async () => (answers.length > 1 ? answers.shift() : answers[0]) as never)
  const ws = { me: me(can) } as unknown as Workspace
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () =>
    r.render(
      <WorkspaceContext.Provider value={ws}>
        <DashboardPortSettings />
      </WorkspaceContext.Provider>,
    ),
  )
  await act(async () => {})
}

const text = () => document.body.textContent ?? ''

function toggle(): HTMLElement {
  const el = document.querySelector<HTMLElement>('[role="switch"]')
  if (!el) throw new Error('no switch')
  return el
}

function button(label: string): HTMLElement {
  const el = [...document.querySelectorAll<HTMLElement>('button, a')].find((b) => b.textContent?.includes(label))
  if (!el) throw new Error(`no ${label} in ${text()}`)
  return el
}

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  vi.mocked(client.get).mockReset()
  vi.mocked(client.put).mockReset()
  vi.mocked(client.post).mockReset()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('the dashboard on the standard HTTPS port', () => {
  it('says what turning it on does, and that port 443 must be open', async () => {
    await show(view({ on: false, state: 'off' }))
    expect(text()).toContain('Serve the dashboard on the standard HTTPS port (443)')
    expect(text()).toContain(`Opens at ${url} instead of ${old}. Port 443 must be open in your provider’s firewall.`)
    expect(toggle().getAttribute('aria-checked')).toBe('false')
    expect(text()).not.toContain('Outside Playkeeper')
  })

  it('turns on, or says what has port 443', async () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})))
    vi.mocked(client.put).mockRejectedValueOnce(
      new client.ApiError(409, { error: 'nginx uses port 443, so the dashboard can’t have it.', code: 'port_in_use', params: { port: 443, state: 'busy', holder: 'nginx' } }),
    )
    await show(view({ on: false, state: 'off' }))
    await act(async () => toggle().click())
    expect(client.put).toHaveBeenCalledWith('/api/dashboard-port', { on: true })
    expect(text()).toContain(`nginx uses port 443, so Playkeeper leaves it alone. The dashboard stays at ${old}.`)
    expect(toggle().getAttribute('aria-checked')).toBe('false')

    vi.mocked(client.put).mockResolvedValueOnce(view({}))
    vi.mocked(client.get).mockResolvedValue(view({}) as never)
    await act(async () => toggle().click())
    expect(toggle().getAttribute('aria-checked')).toBe('true')
    expect(text()).not.toContain('nginx')
  })

  it('checks from this browser that port 443 answers, and finishes when it does', async () => {
    const fetch = vi.fn(() => Promise.resolve(new Response(null, { status: 200 })))
    vi.stubGlobal('fetch', fetch)
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    await show([view({}), view({ reached: true, port: 443 })])
    expect(fetch).toHaveBeenCalledWith(`${url}/api/public/reach`, expect.objectContaining({ mode: 'no-cors', credentials: 'omit', cache: 'no-store' }))
    await act(async () => vi.advanceTimersByTime(2000))
    vi.useRealTimers()
    await act(async () => {})
    expect(text()).toContain(`Answers at ${url}. ${old} keeps working and sends browsers there.`)
  })

  it('asks for a visit when this browser can’t reach port 443', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))))
    await show(view({}))
    expect(text()).toContain(`Open ${url} once to finish. If it doesn’t open, allow port 443 in your provider’s firewall.`)
    expect(button(`Open ${url}`).getAttribute('href')).toBe(url)
    expect(button('Check again')).toBeTruthy()
  })

  it('lists what outside Playkeeper keeps the old address', async () => {
    await show(
      view({
        reached: true,
        port: 443,
        outside: [
          { kind: 'whop_signin', app: 'app_6oyNYgGluUMTx4', add: `${url}/api/public/whop/signin/callback`, keep: `${old}/api/public/whop/signin/callback`, done: false },
          { kind: 'whop_webhook', add: `${url}/api/public/whop/webhook`, automatic: true, done: true },
          { kind: 'mcp', add: `${url}/mcp`, keep: `${old}/mcp`, done: false },
        ],
      }),
    )
    expect(text()).toContain('Outside Playkeeper')
    expect(text()).toContain('Allow TCP port 443 from anywhere. Keep port 8443 open too')
    expect(text()).toContain(`open the app app_6oyNYgGluUMTx4 and add this redirect URL on its OAuth tab. Keep ${old}/api/public/whop/signin/callback`)
    expect(document.querySelector('code')?.textContent).toBe(`${url}/api/public/whop/signin/callback`)
    expect(text()).toContain('To do')
    expect(text()).toContain(`Playkeeper moved it to ${url}/api/public/whop/webhook.`)
    expect(text()).toContain(`Agents set up with ${old}/mcp keep working.`)
    expect(text()).toContain('Links and bookmarks with :8443 keep working')
  })

  it('asks before links without a port stop working', async () => {
    vi.mocked(client.put).mockResolvedValue(view({ on: false, state: 'off' }))
    await show(view({ reached: true, port: 443 }))
    await act(async () => toggle().click())
    expect(client.put).not.toHaveBeenCalled()
    expect(text()).toContain(`${url} stops opening the dashboard, so links to it that went out stop working. ${old} keeps working.`)
    await act(async () => button('Stop').click())
    expect(client.put).toHaveBeenCalledWith('/api/dashboard-port', { on: false })
  })

  it('names what took port 443, and tries again', async () => {
    vi.mocked(client.post).mockResolvedValue({ ok: true })
    await show(view({ state: 'claimed', holder: 'caddy', reached: true }))
    expect(text()).toContain(`caddy is set to use port 443, so Playkeeper leaves it alone. The dashboard stays at ${old}.`)
    await act(async () => button('Try again').click())
    expect(client.post).toHaveBeenCalledWith('/api/dashboard-port/retry', {})
  })

  it('waits for an address, and for the machine’s start', async () => {
    await show(view({ state: 'no_address', url: undefined, old: undefined }))
    expect(text()).toContain('It takes effect once this machine has an address with a certificate.')
    await act(async () => root?.unmount())
    await show(view({ state: 'waiting', reached: true, port: 443 }))
    expect(text()).toContain('Opens on port 443 a few minutes after the machine starts.')
  })

  it('is for people who manage the machine to change', async () => {
    await show(view({ on: false, state: 'off' }), ['view', 'machines.view'])
    expect(toggle().hasAttribute('data-disabled')).toBe(true)
  })
})
