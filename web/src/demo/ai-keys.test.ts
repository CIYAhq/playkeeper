import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { AIKeys, AuditEntry, ServerStatus } from '@/api/types'
import type { DemoState, InstalledAddon } from './data'
import { answer, reloadDemo, resetDemo } from './engine'
import { demoMarker } from './marker'

vi.mock('./toast', () => ({ demoToast: vi.fn() }))

const key = 'sk-or-v1-' + '0123456789abcdef'.repeat(4)
const kept = new Map<string, string>()

async function ask<T>(method: string, path: string, body?: unknown): Promise<T> {
  const reply = answer(method, path, body).then(
    (value) => ({ value }),
    (error: unknown) => ({ error }),
  )
  await vi.advanceTimersByTimeAsync(200)
  const settled = await reply
  if ('error' in settled) throw settled.error
  return settled.value as T
}

async function survival(): Promise<ServerStatus> {
  const found = (await ask<ServerStatus[]>('GET', '/api/servers')).find((s) => s.slug === 'survival')
  if (!found) throw new Error('no Survival in the sample data')
  return found
}

const keys = (id: string) => ask<AIKeys>('GET', `/api/servers/${id}/ai-keys`)
const audits = () => ask<AuditEntry[]>('GET', '/api/audit')
const stored = () => kept.get(demoMarker) ?? ''

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-09-25T12:10:00Z'))
  kept.clear()
  vi.stubGlobal('sessionStorage', {
    getItem: (k: string) => kept.get(k) ?? null,
    setItem: (k: string, v: string) => void kept.set(k, v),
    removeItem: (k: string) => void kept.delete(k),
  })
  resetDemo()
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

it('checks a key as the agent does, then keeps only that one is set: nothing a visitor pastes is saved', async () => {
  const { id } = await survival()
  expect(await keys(id)).toEqual({ keys: { openrouter: { set: false } }, pending: false, available: false })

  const other = 'sk-proj-' + 'x'.repeat(40)
  await expect(ask('PUT', `/api/servers/${id}/ai-keys/openrouter`, { key: other })).rejects.toMatchObject({
    status: 400,
    code: 'invalid_request',
    field: 'key',
    reason: 'ai_key_prefix',
    message: 'That isn’t an OpenRouter key: those start with sk-or-.',
  })
  await expect(ask('PUT', `/api/servers/${id}/ai-keys/openrouter`, { key: 'sk-or-v1-short' })).rejects.toMatchObject({ status: 400, reason: 'ai_key_short' })
  await expect(ask('PUT', `/api/servers/${id}/ai-keys/openrouter`, {})).rejects.toMatchObject({ status: 400, reason: 'ai_key_missing' })

  expect(await ask('PUT', `/api/servers/${id}/ai-keys/openrouter`, { key: `  ${key}\n` })).toEqual({ keys: { openrouter: { set: true } }, pending: false, available: false })
  expect((await keys(id)).keys.openrouter.set).toBe(true)
  const saved = await audits()
  expect(saved[0]).toMatchObject({ action: 'ai_key.saved', target: 'openrouter', serverId: id, result: 'succeeded' })
  for (const where of [stored(), JSON.stringify(saved)]) {
    expect(where).not.toContain(key)
    expect(where).not.toContain(key.slice(-12))
    expect(where).not.toContain(other)
  }

  expect(await ask('DELETE', `/api/servers/${id}/ai-keys/openrouter`)).toEqual({ keys: { openrouter: { set: false } }, pending: false, available: false })
  expect((await audits())[0]).toMatchObject({ action: 'ai_key.removed', target: 'openrouter' })
  const before = (await audits()).length
  await ask('DELETE', `/api/servers/${id}/ai-keys/openrouter`)
  expect((await audits()).length, 'removing no key isn’t audited').toBe(before)

  await expect(ask('PUT', `/api/servers/${id}/ai-keys/openai`, { key })).rejects.toMatchObject({ status: 404 })
  await expect(ask('DELETE', `/api/servers/${id}/ai-keys/toString`)).rejects.toMatchObject({ status: 404 })
})

it('offers the key on a server with AI Build Battle, keeps it through a reload, and forgets it with the server', async () => {
  const { id } = await survival()
  const s = JSON.parse(stored()) as DemoState
  s.addons[id]?.installed.push({ source: 'playkeeper' as string as InstalledAddon['source'], projectId: 'ai-build-battle', version: '0.4.9', published: Date.now(), installedAt: Date.now() })
  kept.set(demoMarker, JSON.stringify(s))
  reloadDemo()
  expect((await keys(id)).available).toBe(true)

  await ask('PUT', `/api/servers/${id}/ai-keys/openrouter`, { key })
  reloadDemo()
  expect(await keys(id)).toEqual({ keys: { openrouter: { set: true } }, pending: false, available: true })

  await ask('POST', `/api/servers/${id}/delete`)
  expect((JSON.parse(stored()) as DemoState).aiKeys?.[id]).toBeUndefined()
})

it('refuses a change while the server is in the middle of something', async () => {
  const { id } = await survival()
  await ask('POST', `/api/servers/${id}/restart`)
  await expect(ask('PUT', `/api/servers/${id}/ai-keys/openrouter`, { key })).rejects.toMatchObject({ status: 409, code: 'busy' })
  expect((await keys(id)).keys.openrouter.set).toBe(false)
})
