// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { currentSection, scrollBehavior, scrollToSection, type SectionBox } from './scroll'

function reducedMotion(on: boolean) {
  vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({ matches: on && query === '(prefers-reduced-motion: reduce)', media: query, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false }))
}

afterEach(() => {
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})

describe('scrolling to a place', () => {
  it('glides, unless the system asks for less motion, then jumps', () => {
    reducedMotion(false)
    expect(scrollBehavior()).toBe('smooth')
    reducedMotion(true)
    expect(scrollBehavior()).toBe('auto')
  })

  it('brings a section to the top and moves focus there without a second jump', () => {
    document.body.innerHTML = '<section id="memory" tabindex="-1"></section><section id="plain"></section>'
    const memory = document.getElementById('memory') as HTMLElement
    const plain = document.getElementById('plain') as HTMLElement
    memory.scrollIntoView = vi.fn()
    plain.scrollIntoView = vi.fn()
    const focus = vi.spyOn(memory, 'focus')
    reducedMotion(false)
    scrollToSection(memory)
    expect(memory.scrollIntoView).toHaveBeenCalledWith({ block: 'start', behavior: 'smooth' })
    expect(focus).toHaveBeenCalledWith({ preventScroll: true })
    expect(document.activeElement).toBe(memory)

    reducedMotion(true)
    scrollToSection(plain)
    expect(plain.scrollIntoView).toHaveBeenCalledWith({ block: 'start', behavior: 'auto' })
    expect(document.activeElement).toBe(memory)
  })
})

describe('the current section', () => {
  // Six sections 400 px tall, under a header that ends at 171 px, in a 900 px window.
  const at = (scrolled: number): SectionBox[] => ['game', 'list', 'memory', 'schedules', 'version', 'danger'].map((id, i) => ({ id, top: 195 + i * 416 - scrolled, bottom: 595 + i * 416 - scrolled }))
  const view = (atEnd = false) => ({ top: 171, height: 900, atEnd })

  it('is the first section at the top of the page', () => {
    expect(currentSection(at(0), view())).toBe('game')
  })

  it('is the one whose top came up to the header, where a jump to it leaves it', () => {
    expect(currentSection(at(416 * 2), view())).toBe('memory')
    expect(currentSection(at(416 * 2 - 30), view())).toBe('list')
    expect(currentSection(at(416 * 2 + 200), view())).toBe('memory')
  })

  it('ignores the section asked for while the page can still scroll', () => {
    expect(currentSection(at(416 * 2), view(), 'schedules')).toBe('memory')
  })

  it('keeps the section asked for at the end of the page while it’s on screen, else the last one on screen', () => {
    const end = at(416 * 4 - 250)
    expect(currentSection(end, view(true), 'danger')).toBe('danger')
    expect(currentSection(end, view(true), 'version')).toBe('version')
    expect(currentSection(end, view(true), 'game')).toBe('danger')
    expect(currentSection(end, view(true))).toBe('danger')
  })

  it('has none without sections', () => {
    expect(currentSection([], view())).toBeUndefined()
  })
})
