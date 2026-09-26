// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, expect, it, vi } from 'vitest'
import * as client from '@/api/client'
import { useModpackPreview } from '@/api/modpacks'
import type { ModpackPreview } from '@/api/types'
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
