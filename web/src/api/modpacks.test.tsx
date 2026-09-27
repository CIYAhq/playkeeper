// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import { mergePacks, useModpackPreview } from '@/api/modpacks'
import type { ModpackCard, ModpackPreview } from '@/api/types'
import { packVoicePort } from '@/components/app/modpacks'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof client>()),
  get: vi.fn(),
}))

/** The voice chat port New server's last step and the pack sheet name for one pack. */
function VoicePort() {
  const plan = useModpackPreview('m2345abcde', 'modrinth', 'VOIC0001', 'VOIC0001V')
  return <p>{plan.data ? String(packVoicePort(plan.data) ?? 'none') : 'loading'}</p>
}

let root: Root | undefined

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  document.body.innerHTML = ''
  vi.mocked(client.get).mockReset()
})

async function show(): Promise<string> {
  await act(async () => root?.unmount())
  const r = createRoot(document.body.appendChild(document.createElement('div')))
  root = r
  await act(async () => r.render(<VoicePort />))
  await vi.waitFor(() => expect(document.body.textContent).not.toContain('loading'))
  return document.body.lastElementChild?.textContent ?? ''
}

// The agent works out the port a pack's voice chat would get as servers take
// ports, so a pack chosen again minutes later names the port it gets then:
// the one after the port the first server made from it took, or none.
it.each([
  ['the next free port', 24455, '24455'],
  ['no port at all', undefined, 'none'],
])('a pack’s plan asked for again names %s', async (_, second, want) => {
  const plan = (port?: number): ModpackPreview => ({
    type: 'fabric',
    minecraftVersion: '26.2',
    loaderVersion: '0.19.3',
    files: 17,
    downloadSize: 16_000_000,
    ready: true,
    blockers: [],
    warnings: [],
    manual: [],
    ports: port ? [{ protocol: 'udp', port }] : undefined,
  })
  vi.mocked(client.get).mockResolvedValueOnce(plan(24454)).mockResolvedValueOnce(plan(second))
  expect(await show()).toBe('24454')
  expect(await show()).toBe(want)
  expect(client.get).toHaveBeenCalledTimes(2)
})

it('merges Modrinth’s and CurseForge’s packs in the order asked for', () => {
  const pack = (source: ModpackCard['source'], projectId: string, downloads: number, updated: string): ModpackCard => ({
    source, projectId, slug: projectId, name: projectId, summary: '', downloads, updated, pageUrl: '', types: ['fabric'], minecraftVersions: ['26.2'],
  })
  const modrinth = [pack('modrinth', 'm1', 10, '2026-09-01T00:00:00Z'), pack('modrinth', 'm2', 5, '2026-09-03T00:00:00Z'), pack('modrinth', 'm3', 1, '2026-09-05T00:00:00Z')]
  const curseforge = [pack('curseforge', 'c1', 100, '2026-09-02T00:00:00Z'), pack('curseforge', 'c2', 2, '2026-09-04T00:00:00Z')]
  const ids = (sort: Parameters<typeof mergePacks>[1]) => mergePacks([modrinth, curseforge], sort).map((c) => c.projectId)
  expect(ids('relevance')).toEqual(['m1', 'c1', 'm2', 'c2', 'm3'])
  expect(ids('newest')).toEqual(['m1', 'c1', 'm2', 'c2', 'm3'])
  expect(ids('downloads')).toEqual(['c1', 'm1', 'm2', 'c2', 'm3'])
  expect(ids('updated')).toEqual(['m3', 'c2', 'm2', 'c1', 'm1'])
  expect(mergePacks([modrinth], 'downloads')).toEqual(modrinth)
})
