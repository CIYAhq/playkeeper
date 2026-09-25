// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { navigate } from './router'

afterEach(() => {
  document.body.innerHTML = ''
  vi.restoreAllMocks()
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
