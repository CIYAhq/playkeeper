// @vitest-environment happy-dom
// @vitest-environment-options {"url": "https://play.example.net/servers/survival/plugins"}
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it } from 'vitest'
import { SourceLink } from './detail'

let root: Root | undefined

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(() => {
  act(() => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
})

describe('SourceLink', () => {
  it.each([
    { name: 'a path on the dashboard', href: '/api/servers/rstuvwxyzq/offsite/recovery-key', shown: false },
    { name: 'a page on the dashboard, written out', href: 'https://play.example.net/api/servers/rstuvwxyzq/offsite/recovery-key', shown: false },
    { name: 'a script', href: 'javascript:alert(document.cookie)', shown: false },
    { name: 'the add-on page on Modrinth', href: 'https://modrinth.com/plugin/chunky', shown: true },
  ])('links to $name only when it is another site', ({ href, shown }) => {
    const el = document.body.appendChild(document.createElement('div'))
    act(() => {
      root = createRoot(el)
      root.render(<SourceLink href={href}>Open source page</SourceLink>)
    })
    const a = el.querySelector('a')
    expect(a !== null).toBe(shown)
    if (a) expect(a.getAttribute('href')).toBe(href)
  })
})
