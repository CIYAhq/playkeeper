// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeAll, expect, it, vi } from 'vitest'
import * as client from '../api/client'
import type { Me, Operation, ServerStatus } from '../api/types'
import { Onboarding } from './Onboarding'

vi.mock('../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
}))

const me: Me = { user: { username: 'admin' }, csrfToken: 't', expiresAt: '2026-09-25T00:00:00Z', idleTimeoutSeconds: 43200, version: 'test' }

function operation(kind: string, status: Operation['status'], phase: string): Operation {
  const op: Operation = { id: 'op1', kind, status, phase, actor: 'admin', startedAt: '2026-09-24T18:00:00Z' }
  if (status === 'failed') Object.assign(op, { finishedAt: '2026-09-24T18:01:00Z', error: 'The operation failed here.', hint: 'Try again.' })
  return op
}

function serverStatus(phase: ServerStatus['phase'], op?: Operation): ServerStatus {
  return {
    exists: true,
    desired: 'stopped',
    phase,
    phaseDetail: 'stale detail',
    reachable: false,
    gamePort: 25565,
    offlineModeTest: false,
    crashCount: 0,
    pendingRestart: false,
    agentVersion: 'test',
    operation: op,
  }
}

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(() => {
  vi.mocked(client.get).mockReset()
  vi.mocked(client.get).mockImplementation(() => new Promise(() => {}))
  document.body.innerHTML = ''
})

// Regression for items 62 and 86: after a failed start the step list read the
// server's phase ("stopped"), which matches no step, so step 1 was marked
// failed whatever phase the operation had failed in.
it.each([
  ['a create that failed downloading Paper', 'create', 'downloading_server', ['done', 'failed', '', '', '']],
  ['a restore that failed before any start step', 'restore', 'replacing_world', ['', '', '', '', '']],
])('step list after %s', async (_, kind, failedPhase, want) => {
  vi.mocked(client.get).mockImplementation(((path: string) => {
    if (path === '/api/operations/op1') return Promise.resolve(operation(kind, 'failed', failedPhase))
    if (path.startsWith('/api/server/logs')) return Promise.resolve({ epoch: 'e', lines: [], next: 0, truncated: false })
    return new Promise(() => {})
  }) as typeof client.get)
  const root = createRoot(document.body.appendChild(document.createElement('div')))
  const props = { statusError: undefined, refresh: async () => {}, me }
  await act(async () => root.render(<Onboarding {...props} status={serverStatus('downloading_server', operation(kind, 'running', failedPhase))} />))
  await act(async () => root.render(<Onboarding {...props} status={serverStatus('stopped')} />))
  await act(async () => {})

  expect(document.querySelector('h1')?.textContent).toBe('Starting failed')
  expect(document.body.textContent).toContain('The operation failed here.')
  expect([...document.querySelectorAll('.checklist li')].map((li) => li.className)).toEqual(want)
  expect(document.body.textContent).not.toContain('stale detail')
  await act(async () => root.unmount())
})
