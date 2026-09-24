// @vitest-environment happy-dom
import { act, type ComponentType } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeAll, expect, it, vi } from 'vitest'
import * as client from '../api/client'
import type { Me, ServerStatus } from '../api/types'
import type { PageProps } from '../App'
import { Overview } from './Overview'
import { PlayersPage } from './Players'

vi.mock('../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
}))

const me: Me = { user: { username: 'admin' }, csrfToken: 't', expiresAt: '2026-09-25T00:00:00Z', idleTimeoutSeconds: 43200, version: 'test' }

function newInstall(): ServerStatus {
  return {
    exists: true,
    desired: 'running',
    phase: 'online',
    reachable: true,
    gamePort: 25565,
    offlineModeTest: false,
    crashCount: 0,
    pendingRestart: false,
    agentVersion: 'test',
    collectingSince: new Date(Date.now() - 10 * 60_000).toISOString(),
  }
}

function lastMetricsRange(): string | undefined {
  const urls = vi.mocked(client.get).mock.calls.map((c) => String(c[0])).filter((u) => u.startsWith('/api/metrics'))
  return urls.at(-1)?.match(/range=(\w+)/)?.[1]
}

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(() => {
  vi.mocked(client.get).mockClear()
  document.body.innerHTML = ''
})

// Regression for 757d0ff: the default range was fixed at mount, before server
// status had loaded, so a new install stayed on the 24-hour chart.
it.each<[string, ComponentType<PageProps>]>([
  ['Overview', Overview],
  ['Players', PlayersPage],
])('%s switches to the last hour once status shows a new install', async (_, Page) => {
  const root = createRoot(document.body.appendChild(document.createElement('div')))
  const props = { statusError: undefined, refresh: async () => {}, me }
  await act(async () => root.render(<Page {...props} status={undefined} />))
  expect(lastMetricsRange()).toBe('24h')
  await act(async () => root.render(<Page {...props} status={newInstall()} />))
  expect(lastMetricsRange()).toBe('1h')
  await act(async () => root.unmount())
})
