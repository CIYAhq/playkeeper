// @vitest-environment happy-dom
import { act, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeAll, expect, it, vi } from 'vitest'
import * as client from '../api/client'
import type { Me, Operation, ServerStatus } from '../api/types'
import { GlobalBanners } from '../App'
import { Overview } from './Overview'

vi.mock('../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
}))

const me: Me = { user: { username: 'admin' }, csrfToken: 't', expiresAt: '2026-09-25T00:00:00Z', idleTimeoutSeconds: 43200, version: 'test' }

function serverStatus(over: Partial<ServerStatus>): ServerStatus {
  return { exists: true, desired: 'running', phase: 'online', reachable: true, gamePort: 25565, offlineModeTest: false, crashCount: 0, pendingRestart: false, agentVersion: 'test', ...over }
}

function failed(kind: string, error: string, detail?: Record<string, unknown>): Operation {
  const at = new Date(Date.now() - 60_000).toISOString()
  return { id: `${kind}-1`, kind, status: 'failed', phase: '', actor: 'admin', startedAt: at, finishedAt: at, error, detail }
}

async function text(node: ReactNode): Promise<string> {
  const root = createRoot(document.body.appendChild(document.createElement('div')))
  await act(async () => root.render(node))
  const out = document.body.textContent ?? ''
  await act(async () => root.unmount())
  document.body.innerHTML = ''
  return out
}

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(() => {
  vi.mocked(client.get).mockClear()
})

// Regression for item 66: a failure banner stayed up to 15 minutes after its
// cause cleared, such as "Start failed" next to Online and Joinable.
it('drops a failed start once the server is online', async () => {
  const last = failed('start', 'The server did not finish starting within 10m0s.')
  expect(await text(<GlobalBanners agentDown={false} status={serverStatus({ phase: 'stopped', lastOperation: last })} />)).toContain('Start failed')
  expect(await text(<GlobalBanners agentDown={false} status={serverStatus({ phase: 'online', lastOperation: last })} />)).not.toContain('Start failed')
})

it('drops a backup refused for space once enough disk is free', async () => {
  const last = failed('backup', 'Not enough disk space for a backup: 400.0 MiB free, about 1.0 GiB needed.', { neededBytes: 2 ** 30 })
  const at = new Date().toISOString()
  expect(await text(<GlobalBanners agentDown={false} status={serverStatus({ lastOperation: last, resources: { diskFreeBytes: 400 * 2 ** 20, at } })} />)).toContain('Backup failed')
  expect(await text(<GlobalBanners agentDown={false} status={serverStatus({ lastOperation: last, resources: { diskFreeBytes: 17 * 2 ** 30, at } })} />)).not.toContain(
    'Backup failed',
  )
})

it('warns about low disk space on the Overview with the preflight advice', async () => {
  const diskWarning = {
    id: 'disk',
    label: 'Disk space',
    status: 'fail' as const,
    detail: 'Only 0.4 GB free.',
    fix: 'Free at least 5 GB of disk space (old logs, unused Docker images: sudo docker image prune), then check again.',
  }
  const out = await text(<Overview status={serverStatus({ diskWarning })} statusError={undefined} refresh={async () => {}} me={me} />)
  expect(out).toContain('Low disk space: Only 0.4 GB free.')
  expect(out).toContain('Free at least 5 GB of disk space')
})
