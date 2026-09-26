// @vitest-environment happy-dom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { CardsSkeleton, lineWidth, ListSkeleton, TableSkeleton } from '@/components/app/skeletons'
import { ToastProvider, toastManager } from '@/components/ui/toast'
import { mergePrefs, undoPrefs, usePending, withChanges } from './optimistic'
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

  it('accepts a list rebuilt from the same items on every render', async () => {
    const items = [{ id: 'a' }, { id: 'b' }]
    function List() {
      const rows = useListPresence([...items], byId)
      return <ul>{rows.map((r) => r.key + r.state)}</ul>
    }
    await render(<List />)
    await render(<List />)
    expect(document.querySelector('ul')?.textContent).toBe('astayingbstaying')
  })
})

function deferred() {
  let resolve: () => void = () => {}
  const promise = new Promise<void>((r) => {
    resolve = r
  })
  return { promise, resolve }
}

describe('optimistic changes', () => {
  const byName = (p: { name: string }) => p.name.toLowerCase()

  it('merges preference changes and can put them back', () => {
    const before = { 'checklist.hidden.a': '1', 'sidebar.open': '1' }
    const change = { 'checklist.hidden.b': '1', 'sidebar.open': '' }
    const after = mergePrefs(before, change)
    expect(after).toEqual({ 'checklist.hidden.a': '1', 'checklist.hidden.b': '1' })
    expect(mergePrefs(after, undoPrefs(before, change))).toEqual(before)
  })

  it('shows rows being added or removed on top of the list', () => {
    const list = [{ name: 'mara_k' }, { name: 'Lenn0x' }]
    expect(withChanges(list, [], byName)).toBe(list)
    expect(withChanges(undefined, [{ add: { name: 'tobi2009' } }], byName)).toBeUndefined()
    expect(withChanges(list, [{ add: { name: 'tobi2009' } }, { add: { name: 'MARA_K' } }, { remove: 'lenn0x' }], byName)).toEqual([{ name: 'mara_k' }, { name: 'tobi2009' }])
  })

  function Probe({ save, reload, onError }: { save: () => Promise<unknown>; reload: () => Promise<void>; onError: (e: unknown) => void }) {
    const { changes, run } = usePending<string>()
    return (
      <button type="button" onClick={() => void run('tobi2009', save, reload).catch(onError)}>
        {changes.join(',')}
      </button>
    )
  }

  it('keeps a saved change on screen until fresh data has loaded', async () => {
    const saved = deferred()
    const reloaded = deferred()
    const onError = vi.fn()
    await render(<Probe save={() => saved.promise} reload={() => reloaded.promise} onError={onError} />)
    const button = document.querySelector('button') as HTMLButtonElement
    await act(async () => button.click())
    expect(button.textContent).toBe('tobi2009')
    await act(async () => saved.resolve())
    expect(button.textContent).toBe('tobi2009')
    await act(async () => reloaded.resolve())
    expect(button.textContent).toBe('')
    expect(onError).not.toHaveBeenCalled()
  })

  it('takes a change back and passes on the error when saving fails', async () => {
    const refused = new Error('That player does not exist')
    const onError = vi.fn()
    const reload = vi.fn(async () => {})
    await render(<Probe save={() => Promise.reject(refused)} reload={reload} onError={onError} />)
    const button = document.querySelector('button') as HTMLButtonElement
    await act(async () => button.click())
    expect(button.textContent).toBe('')
    expect(onError).toHaveBeenCalledWith(refused)
    expect(reload).not.toHaveBeenCalled()
  })
})

describe('skeletons', () => {
  const visibleShapes = () => document.querySelectorAll('[data-slot="skeleton"]:not([aria-hidden="true"])')

  it('fill the real rows’ boxes and say “Loading…” once', async () => {
    await render(<ListSkeleton rows={4} face="size-7 rounded-md" rowClassName="flex min-h-12 border-t" className="mt-3 flex flex-col" label="Checking…" />)
    const rows = [...document.querySelectorAll('ul > li')]
    expect(rows).toHaveLength(4)
    expect(rows.every((row) => row.className === 'flex min-h-12 border-t')).toBe(true)
    expect(document.body.textContent).toBe('Checking…')
    expect(visibleShapes()).toHaveLength(0)
  })

  it('line up with a table’s columns, numbers on the right', async () => {
    await render(
      <table>
        <tbody>
          <TableSkeleton rows={2} cols={['start', 'end']} rowClassName="h-12" />
        </tbody>
      </table>,
    )
    const rows = [...document.querySelectorAll('tr')].map((tr) => [...tr.querySelectorAll('[data-slot="skeleton"]')].map((s) => s.className))
    expect(rows).toHaveLength(2)
    expect(rows.every((cells) => cells.length === 2 && cells[1]?.includes('ml-auto') && !cells[0]?.includes('ml-auto'))).toBe(true)
    expect(document.body.textContent).toBe('Loading…')
  })

  it('stand in for each choice card and vary their line widths', async () => {
    await render(<CardsSkeleton count={3} className="grid grid-cols-3" card="h-56" />)
    expect(document.querySelectorAll('.grid > [data-slot="skeleton"].h-56')).toHaveLength(3)
    expect(visibleShapes()).toHaveLength(0)
    expect(new Set([0, 1, 2, 3, 4, 5].map(lineWidth)).size).toBe(6)
    expect(lineWidth(6)).toBe(lineWidth(0))
  })
})

describe('toasts', () => {
  it('wrap a long path instead of cutting it off', async () => {
    await render(
      <ToastProvider>
        <p>page</p>
      </ToastProvider>,
    )
    let id = ''
    await act(async () => {
      id = toastManager.add({ title: 'The world folder is missing because a restore did not finish; the previous world is at /var/lib/playkeeper/servers/abcdefghjk/data.replaced-20260926-103028.', description: 'Move that folder back to /var/lib/playkeeper/servers/abcdefghjk/data, then upload the icon again.', type: 'error' })
    })
    const text = document.querySelector('[data-slot="toast-title"]')?.parentElement
    // happy-dom has no layout: a path breaks only where overflow-wrap lets it, and only in a column that can shrink.
    expect(text?.className.split(' ')).toEqual(expect.arrayContaining(['min-w-0', 'wrap-anywhere']))
    expect(text?.parentElement?.className.split(' ')).toContain('min-w-0')
    await act(async () => toastManager.close(id))
  })
})
