// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it } from 'vitest'
import type { Catalog } from '@/api/types'
import { VersionsFrom } from '@/components/app/create'
import { formatLongDate, relativeTime } from '@/lib/format'

// While a type's upstream can't be reached, the versions the dashboard
// offers come from the last list it gave or the one built into the release,
// and one quiet line says which and how old, as on 1 Oct 2026, when PaperMC
// answered 503 for hours.

const catalog = (over: Partial<Catalog>): Catalog => ({
  type: 'paper',
  types: [],
  versions: [],
  versionsCheckedAt: '2026-10-01T10:43:11Z',
  memoryOptionsMB: [2048],
  recommendedMemoryMB: 2048,
  hostMemoryMB: 4096,
  maxMemoryMB: 3328,
  systemReserveMB: 768,
  memoryFreeMB: 3328,
  servers: [],
  image: 'itzg/minecraft-server:2026.9.1-java25',
  ...over,
})

let root: Root | undefined
let host: HTMLDivElement | undefined
afterEach(() => {
  act(() => root?.unmount())
  host?.remove()
  root = host = undefined
})

function render(c: Catalog): string {
  host = document.createElement('div')
  document.body.append(host)
  root = createRoot(host)
  act(() => root?.render(<VersionsFrom catalog={c} />))
  return host.textContent ?? ''
}

describe('The line that says the versions aren’t live', () => {
  it('names the list built into the release and its date', () => {
    expect(render(catalog({ versionsFrom: 'builtin', versionsUpstream: 'PaperMC' }))).toBe(`PaperMC isn’t answering right now, so these are the versions this Playkeeper came with, from ${formatLongDate('2026-10-01T10:43:11Z')}.`)
  })

  it('names the kept list by how long ago its upstream gave it', () => {
    const at = new Date(Date.now() - 3 * 3600_000).toISOString()
    expect(render(catalog({ type: 'fabric', versionsFrom: 'kept', versionsUpstream: 'Mojang', versionsCheckedAt: at }))).toBe(`Mojang isn’t answering right now, so these are the versions it listed ${relativeTime(at)}.`)
  })

  it('names the type when the upstream isn’t named', () => {
    expect(render(catalog({ type: 'neoforge', versionsFrom: 'builtin' }))).toContain('NeoForge isn’t answering right now')
  })

  it('says nothing about a list the upstream just gave', () => {
    expect(render(catalog({}))).toBe('')
  })
})
