// @vitest-environment happy-dom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { presence, presenceProps, settle, useListPresence } from './presence'
import { navigate } from './router'

let root: Root | undefined

async function render(node: ReactNode) {
  if (!root) root = createRoot(document.body.appendChild(document.createElement('div')))
  const r = root
  await act(async () => r.render(node))
}

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('a link to the page you are on', () => {
  it('goes back to the top and moves focus to the page', () => {
    document.body.innerHTML = '<main id="main" tabindex="-1"><h1>Settings</h1></main>'
    window.history.replaceState(null, '', '/settings')
    const scroll = vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
    const push = vi.spyOn(window.history, 'pushState')
    navigate({ name: 'settings' })
    expect(push).not.toHaveBeenCalled()
    expect(scroll).toHaveBeenCalledWith(expect.objectContaining({ top: 0 }))
    expect(document.activeElement?.id).toBe('main')
  })

  it('goes to its section when it has one', () => {
    document.body.innerHTML = '<main id="main" tabindex="-1"><section id="danger" tabindex="-1">Danger zone</section></main>'
    window.history.replaceState(null, '', '/servers/survival/settings#danger')
    const section = document.getElementById('danger') as HTMLElement
    section.scrollIntoView = vi.fn()
    navigate('/servers/survival/settings#danger')
    expect(section.scrollIntoView).toHaveBeenCalled()
    expect(document.activeElement).toBe(section)
  })
})

describe('list presence', () => {
  const byId = (x: { id: string }) => x.id
  const states = (rows: { key: string; state: string }[]) => rows.map((r) => `${r.key}:${r.state}`)

  it('shows a list’s first data without animating it', () => {
    expect(states(presence([], [{ id: 'a' }, { id: 'b' }], byId, true))).toEqual(['a:staying', 'b:staying'])
  })

  it('lets new rows enter and keeps removed ones in place while they leave', () => {
    const rows = presence([], [{ id: 'a' }, { id: 'b' }, { id: 'c' }], byId, true)
    const next = presence(rows, [{ id: 'a' }, { id: 'c' }, { id: 'd' }], byId)
    expect(states(next)).toEqual(['a:staying', 'b:leaving', 'c:staying', 'd:entering'])
    expect(states(settle(next))).toEqual(['a:staying', 'c:staying', 'd:staying'])
    expect(states(presence(next, [{ id: 'a' }, { id: 'b' }, { id: 'c' }], byId))).toEqual(['a:staying', 'b:entering', 'c:staying', 'd:leaving'])
  })

  it('keeps a row that leaves first in the list at the top', () => {
    const rows = presence([], [{ id: 'a' }, { id: 'b' }], byId, true)
    expect(states(presence(rows, [{ id: 'b' }], byId))).toEqual(['a:leaving', 'b:staying'])
  })

  it('removes leaving rows once the standard motion time has passed', async () => {
    vi.useFakeTimers()
    function List({ items }: { items?: { id: string }[] }) {
      const rows = useListPresence(items, byId)
      return (
        <ul>
          {rows.map((r) => (
            <li key={r.key} {...presenceProps(r.state)}>
              {r.key}
            </li>
          ))}
        </ul>
      )
    }
    await render(<List />)
    await render(<List items={[{ id: 'a' }, { id: 'b' }]} />)
    expect(document.querySelectorAll('[data-entering]')).toHaveLength(0)
    await render(<List items={[{ id: 'a' }, { id: 'c' }]} />)
    const leaving = document.querySelector('[data-leaving]')
    expect(leaving?.textContent).toBe('b')
    expect(leaving?.hasAttribute('inert')).toBe(true)
    expect(document.querySelector('[data-entering]')?.textContent).toBe('c')
    await act(async () => vi.advanceTimersByTime(250))
    expect(document.querySelector('ul')?.textContent).toBe('ac')
    expect(document.querySelectorAll('[data-entering], [data-leaving]')).toHaveLength(0)
  })
})
