import { readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, join, relative, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { LogsResponse, ServerStatus } from '@/api/types'
import { faceOf, samplePlayers } from './data'
import { answer, resetDemo } from './engine'
import { faceCount } from './faces'
import { demoMarker } from './marker'
import { demoToast } from './toast'
import { noDemo } from './vite'

vi.mock('./toast', () => ({ demoToast: vi.fn() }))

const src = fileURLToPath(new URL('..', import.meta.url))
const demoDir = join(src, 'demo')

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name)
    if (path === demoDir) return []
    if (statSync(path).isDirectory()) return files(path)
    return /\.tsx?$/.test(path) ? [path] : []
  })
}

it('keeps the live demo out of the dashboard: nothing outside src/demo imports it', () => {
  const specifiers = /\bfrom\s+'([^']+)'|\bimport\s*\(\s*'([^']+)'\s*\)|\bimport\s+'([^']+)'/g
  const found = files(src).flatMap((path) =>
    [...readFileSync(path, 'utf8').matchAll(specifiers)].flatMap((m) => {
      const spec = m[1] ?? m[2] ?? m[3] ?? ''
      const target = spec.startsWith('@/') ? join(src, spec.slice(2)) : spec.startsWith('.') ? resolve(dirname(path), spec) : ''
      return target === demoDir || target.startsWith(demoDir + sep) ? [`${relative(src, path)} imports ${spec}`] : []
    }),
  )
  expect(found).toEqual([])
})

it('fails any other build that picks up the demo, by module or by its marker', () => {
  type Generate = (this: { error: (message: string) => never }, options: unknown, bundle: Record<string, unknown>) => void
  const generate = noDemo().generateBundle as unknown as Generate
  const context = {
    error: (message: string): never => {
      throw new Error(message)
    },
  }
  const bundle = (moduleIds: string[], code = '') => ({ 'assets/index.js': { type: 'chunk', fileName: 'assets/index.js', code, moduleIds } })
  expect(() => generate.call(context, {}, bundle([join(src, 'main.tsx')], 'console.log(1)'))).not.toThrow()
  expect(() => generate.call(context, {}, bundle([join(src, 'main.tsx'), join(demoDir, 'data.ts')]))).toThrow(/contains the live demo/)
  expect(() => generate.call(context, {}, bundle([join(src, 'main.tsx')], `const k = "${demoMarker}"`))).toThrow(/contains the live demo/)
})

it('gives every sample player a face of their own, and anyone else one of the same faces', () => {
  expect(new Set(samplePlayers.map(faceOf)).size).toBe(samplePlayers.length)
  for (const name of ['Steve', 'alex_2', 'x', 'JunoFox2']) {
    expect(faceOf(name)).toBeGreaterThanOrEqual(0)
    expect(faceOf(name)).toBeLessThan(faceCount)
  }
})

async function ask<T>(method: string, path: string, body?: unknown, raw?: Blob): Promise<T> {
  const reply = answer(method, path, body, raw).then(
    (value) => ({ value }),
    (error: unknown) => ({ error }),
  )
  await vi.advanceTimersByTimeAsync(200)
  const settled = await reply
  if ('error' in settled) throw settled.error
  return settled.value as T
}

async function survival(): Promise<ServerStatus> {
  const servers = await ask<ServerStatus[]>('GET', '/api/servers')
  const found = servers.find((s) => s.slug === 'survival')
  if (!found) throw new Error('no Survival in the sample data')
  return found
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-09-25T12:10:00Z'))
  resetDemo()
  vi.mocked(demoToast).mockClear()
})

afterEach(() => {
  vi.useRealTimers()
})

it('fails soft where it has nothing to show or change', async () => {
  await expect(ask('GET', '/api/nothing-here')).rejects.toMatchObject({ status: 404, code: 'not_found' })
  await expect(ask('POST', '/api/nothing-here', {})).rejects.toMatchObject({ status: 400, code: 'demo' })
  const { id } = await survival()
  await expect(ask('POST', `/api/servers/${id}/world`, undefined, new Blob(['x']))).rejects.toMatchObject({ status: 400, code: 'demo' })
})

it('has one machine, with no events and no joining, for the machines pages', async () => {
  const machines = await ask<{ id: string; kind: string }[]>('GET', '/api/machines')
  expect(machines.map((m) => m.kind)).toEqual(['local'])
  await expect(ask('GET', `/api/machines/${machines[0]?.id}/events`)).resolves.toEqual([])
  await expect(ask('POST', '/api/machines/join-codes', { name: '', dial: 'name' })).rejects.toMatchObject({ status: 400, code: 'demo' })
})

it('plays a restart out, back online, and ends it with the demo toast', async () => {
  const before = await survival()
  expect(before.phase).toBe('online')
  await ask('POST', `/api/servers/${before.id}/restart`)
  expect((await survival()).phase).not.toBe('online')
  await expect(ask('POST', `/api/servers/${before.id}/stop`)).rejects.toMatchObject({ status: 409, code: 'busy' })
  expect(demoToast).not.toHaveBeenCalled()

  await vi.advanceTimersByTimeAsync(20_000)
  const after = await survival()
  expect(after.phase).toBe('online')
  expect(after.operation).toBeUndefined()
  expect(demoToast).toHaveBeenCalledWith('restart')
})

it('keeps console lines arriving', async () => {
  const { id } = await survival()
  const first = await ask<LogsResponse>('GET', `/api/servers/${id}/logs`)
  expect(first.lines.length).toBeGreaterThan(0)
  await vi.advanceTimersByTimeAsync(30_000)
  const more = await ask<LogsResponse>('GET', `/api/servers/${id}/logs?after=${first.next}`)
  expect(more.lines.length).toBeGreaterThan(0)
  expect(more.lines.every((l) => l.seq > first.next)).toBe(true)
})

it('starts over on the hour', async () => {
  const { id } = await survival()
  await ask('POST', `/api/servers/${id}/stop`)
  await vi.advanceTimersByTimeAsync(20_000)
  expect((await survival()).phase).toBe('stopped')

  vi.setSystemTime(new Date('2026-09-25T13:00:05Z'))
  expect((await survival()).phase).toBe('online')
})
