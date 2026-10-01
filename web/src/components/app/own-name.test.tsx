// @vitest-environment happy-dom
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it } from 'vitest'
import type { MachineView } from '@/api/types'
import { OwnNameField, ownNameProblem, ownPlayers } from '@/components/app/create'

// The walkthrough of 1 Oct 2026: new servers have the allowlist on, and
// nobody asked for the owner's own Minecraft name, so their first join was
// refused.

const on = (agentVersion?: string) => ({ id: 'm1', projectId: 'p1', name: 'my-vps', kind: 'local', live: agentVersion ? { agentVersion } : undefined }) as MachineView

describe('your own Minecraft name on a new server', () => {
  it('goes to an agent that takes it, and only a name Minecraft allows', () => {
    expect(ownPlayers(' Steve_Builds ', on('0.4.16'), '0.4.16')).toEqual({ players: ['Steve_Builds'] })
    expect(ownPlayers('Steve_Builds', on('0.4.17'), '0.4.16')).toEqual({ players: ['Steve_Builds'] })
    expect(ownPlayers('Steve_Builds', on('0.6.0-dev'), '0.6.0-dev')).toEqual({ players: ['Steve_Builds'] })
    // An older agent refuses fields it doesn't know, and with them the server.
    expect(ownPlayers('Steve_Builds', on('0.4.15'), '0.4.16')).toEqual({})
    expect(ownPlayers('Steve_Builds', on(), '0.4.16')).toEqual({})
    expect(ownPlayers('Steve_Builds', undefined, '0.4.16')).toEqual({})
    for (const bad of ['', 'ab', 'bad name', 'x'.repeat(17)]) expect(ownPlayers(bad, on('0.4.16'), '0.4.16'), bad).toEqual({})
  })

  it('says why a name can’t be used, and nothing about none', () => {
    expect(ownNameProblem('')).toBeUndefined()
    expect(ownNameProblem('Steve_Builds')).toBeUndefined()
    expect(ownNameProblem('bad name')).toBe('Minecraft usernames are 3–16 letters, numbers or underscores.')
  })
})

let root: Root | undefined
beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})
afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
})

describe('the field that asks for it', () => {
  it('is labelled, optional, and says in place of its hint when a name won’t do', async () => {
    function Field() {
      const [v, setV] = useState('')
      return <OwnNameField value={v} onChange={setV} />
    }
    const r = createRoot(document.body.appendChild(document.createElement('div')))
    root = r
    await act(async () => r.render(<Field />))
    const input = document.querySelector('input')
    const label = document.querySelector('label')
    expect(label?.textContent).toBe('Your Minecraft name')
    expect(input?.id).toBe(label?.htmlFor)
    expect(input?.placeholder).toBe('Optional')
    const hint = () => document.getElementById(input?.getAttribute('aria-describedby') ?? '')
    expect(hint()?.textContent).toBe('So you can join. It goes on the allowlist.')
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, 'bad name')
      input?.dispatchEvent(new Event('input', { bubbles: true }))
    })
    expect(hint()?.textContent).toBe('Minecraft usernames are 3–16 letters, numbers or underscores.')
    expect(input?.getAttribute('aria-invalid')).toBe('true')
  })
})
