// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, expect, it, vi } from 'vitest'
import * as client from '../api/client'
import type { DailyActivity, Me, PlayersSummary } from '../api/types'
import { PlayersPage } from './Players'

vi.mock('../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
}))

const me: Me = { user: { username: 'admin' }, csrfToken: 't', expiresAt: '2026-09-25T00:00:00Z', idleTimeoutSeconds: 43200, version: 'test' }

let root: Root | undefined

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  document.body.innerHTML = ''
})

// Regression for item 70: a day with a crash-ended session read "≥ 11m 17s",
// but such a total is at most that, not at least.
it.each<[string, Partial<DailyActivity>, string]>([
  ['a crash-ended session makes it at most', { playtimeUpperBound: true }, '≤ 11m 17s'],
  ['an unseen session start makes it at least', { playtimeLowerBound: true }, '≥ 11m 17s'],
  ['both make it an estimate', { playtimeLowerBound: true, playtimeUpperBound: true }, '≈ 11m 17s'],
])('daily playtime: %s', async (_, bounds, want) => {
  const day: DailyActivity = { date: '2026-09-24', uniquePlayers: 2, sessions: 2, playtimeSeconds: 677, playtimeLowerBound: false, playtimeUpperBound: false, coverage: 1, ...bounds }
  const summary: PlayersSummary = { tz: 'UTC', days: [day], players: [], observedSessions: 2, uncertainSessions: 1, retentionDays: 180 }
  vi.mocked(client.get).mockImplementation(((path: string) =>
    path.startsWith('/api/players/summary') ? Promise.resolve(summary) : new Promise(() => {})) as typeof client.get)
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<PlayersPage status={undefined} statusError={undefined} refresh={async () => {}} me={me} />))
  const cell = [...document.querySelectorAll('td.num')].map((td) => td.textContent).find((t) => t?.includes('11m 17s'))
  expect(cell).toBe(want)
})
