// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeAll, expect, it, vi } from 'vitest'
import * as client from '../api/client'
import type { Catalog, CatalogEntry, Operation, ServerConfig } from '../api/types'
import { compareMinecraft, upgradeTargets } from '../lib/versions'
import { ServerStep } from './Onboarding'

vi.mock('../api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(() => new Promise(() => {})),
  post: vi.fn(),
}))

function entry(mc: string, build: number, over: Partial<CatalogEntry> = {}): CatalogEntry {
  return { id: `paper-${mc}`, label: `Paper ${mc}`, minecraftVersion: mc, paperBuild: build, jarSha256: '0'.repeat(64), java: 25, recommended: false, notes: '', channel: 'STABLE', experimental: false, supported: true, ...over }
}

const catalog: Catalog = {
  versions: [entry('26.3', 41, { experimental: true, channel: 'ALPHA', notes: 'Experimental: only alpha builds.' }), entry('26.2', 129, { recommended: true }), entry('26.1.2', 74, { supported: false })],
  memoryOptionsMB: [1536, 2048],
  recommendedMemoryMB: 1536,
  hostMemoryMB: 4096,
  maxMemoryMB: 3328,
  image: 'itzg/minecraft-server',
}

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(() => {
  document.body.innerHTML = ''
})

it('preselects the latest stable version and creates an experimental one only with consent', async () => {
  vi.mocked(client.get).mockImplementation(((path: string) => (path === '/api/catalog' ? Promise.resolve(catalog) : new Promise(() => {}))) as typeof client.get)
  vi.mocked(client.post).mockResolvedValue({ id: 'op1' } as Operation)
  const root = createRoot(document.body.appendChild(document.createElement('div')))
  await act(async () => root.render(<ServerStep onStarted={() => {}} onBack={() => {}} />))
  const radios = [...document.querySelectorAll<HTMLInputElement>('input[name=version]')]
  expect(radios.map((r) => r.checked)).toEqual([false, true, false])
  const submit = document.querySelector<HTMLButtonElement>('button[type=submit]')!
  expect(submit.disabled).toBe(false)

  await act(async () => radios[0]!.click())
  expect(document.body.textContent).toContain('Paper 26.3 is experimental')
  expect(submit.disabled).toBe(true)
  const consent = [...document.querySelectorAll<HTMLInputElement>('input[type=checkbox]')].find((c) => c.closest('.banner'))!
  await act(async () => consent.click())
  expect(submit.disabled).toBe(false)
  await act(async () => submit.click())
  expect(vi.mocked(client.post)).toHaveBeenCalledWith('/api/server', expect.objectContaining({ versionId: 'paper-26.3', acceptExperimental: true }))
  await act(async () => root.unmount())
})

it('only offers versions a server can move to', () => {
  expect(compareMinecraft('26.2', '26.1.2')).toBe(1)
  expect(compareMinecraft('1.21.11', '26.2')).toBe(-1)
  expect(compareMinecraft('26.2', '26.2.0')).toBe(0)
  const cfg = { minecraftVersion: '26.2', paperBuild: 120 } as ServerConfig
  const targets = upgradeTargets(cfg, [...catalog.versions, entry('26.2', 110)]).map((v) => `${v.minecraftVersion}#${v.paperBuild}`)
  expect(targets).toEqual(['26.3#41', '26.2#129'])
})
