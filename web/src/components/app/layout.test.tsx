// @vitest-environment happy-dom
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { SectionNav } from '@/components/app/section-nav'
import { StickyHeader } from '@/components/app/sticky-header'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Menu, MenuCheckboxItem, MenuItem, MenuPopup, MenuRadioGroup, MenuRadioItem, MenuTrigger } from '@/components/ui/menu'
import { Radio, RadioGroup } from '@/components/ui/radio-group'
import { Select, SelectItem, SelectPopup, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Slider } from '@/components/ui/slider'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsList, TabsTab } from '@/components/ui/tabs'
import { Toggle } from '@/components/ui/toggle'
import { appearAtOnce } from '@/lib/presence'
import { navigate } from '@/lib/router'

const styles = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '../../styles.css'), 'utf8')

let root: Root | undefined

async function render(node: ReactNode) {
  if (!root) root = createRoot(document.body.appendChild(document.createElement('div')))
  const r = root
  await act(async () => r.render(node))
  await act(async () => {})
}

function media({ phone = false, reduce = false }) {
  vi.spyOn(window, 'matchMedia').mockImplementation((query: string) => ({
    matches: (phone && query === '(max-width: 639px)') || (reduce && query === '(prefers-reduced-motion: reduce)'),
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  }))
}

function scrollTo(y: number) {
  Object.defineProperty(window, 'scrollY', { value: y, configurable: true })
  window.dispatchEvent(new Event('scroll'))
}

const frame = () => new Promise((resolve) => window.requestAnimationFrame(() => resolve(undefined)))

beforeAll(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
})

afterEach(async () => {
  await act(async () => root?.unmount())
  root = undefined
  document.body.innerHTML = ''
  document.documentElement.removeAttribute('style')
  window.history.replaceState(null, '', '/')
  Object.defineProperty(window, 'scrollY', { value: 0, configurable: true })
  vi.restoreAllMocks()
})

describe('a sticky header', () => {
  it('stays at the top of the card on desktop, and makes its bottom edge the page’s scroll padding', async () => {
    media({})
    vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(163)
    const real = window.getComputedStyle.bind(window)
    vi.spyOn(window, 'getComputedStyle').mockImplementation((el: Element) => (el instanceof HTMLElement && el.dataset.stickyHeader !== undefined ? ({ top: '8px' } as CSSStyleDeclaration) : real(el)))
    await render(
      <StickyHeader className="border-b px-7">
        <h1>Survival</h1>
      </StickyHeader>,
    )
    const header = document.querySelector('header[data-sticky-header]')
    expect(header?.className.split(' ')).toEqual(expect.arrayContaining(['sticky', 'top-2', 'rounded-t-2xl', 'border-b', 'px-7']))
    expect(document.documentElement.style.getPropertyValue('--header-h')).toBe('171px')
    await act(async () => root?.unmount())
    root = undefined
    expect(document.documentElement.style.getPropertyValue('--header-h')).toBe('')
  })

  it('is a compact bar on phones that takes a background and a hairline once the page scrolls under it', async () => {
    media({ phone: true })
    await render(<StickyHeader className="flex items-center">Survival</StickyHeader>)
    const header = document.querySelector('header[data-sticky-header]') as HTMLElement
    expect(header.className.split(' ')).toEqual(expect.arrayContaining(['sticky', 'top-0', '-mx-4', 'data-scrolled:bg-sidebar/95', 'flex']))
    expect(header.hasAttribute('data-scrolled')).toBe(false)
    await act(async () => scrollTo(120))
    expect(header.hasAttribute('data-scrolled')).toBe(true)
    await act(async () => scrollTo(0))
    expect(header.hasAttribute('data-scrolled')).toBe(false)
  })
})

describe('a section list', () => {
  const sections = [
    { id: 'game', label: 'In the game' },
    { id: 'list', label: 'Server list' },
    { id: 'danger', label: 'Danger zone' },
  ]

  async function page() {
    await render(
      <>
        <header data-sticky-header="" />
        <SectionNav label="Sections" sections={sections} />
        {sections.map((s) => (
          <section key={s.id} id={s.id} tabIndex={-1} />
        ))}
      </>,
    )
    const scrolls: Record<string, ReturnType<typeof vi.fn>> = {}
    for (const s of sections) {
      const el = document.getElementById(s.id) as HTMLElement
      scrolls[s.id] = vi.fn()
      el.scrollIntoView = scrolls[s.id] as unknown as HTMLElement['scrollIntoView']
    }
    return scrolls
  }

  const entry = (label: string) => [...document.querySelectorAll('nav a')].find((a) => a.textContent === label) as HTMLAnchorElement
  const current = () => document.querySelector('nav [aria-current="location"]')?.textContent

  /** Where each section's top is on screen; the header ends at 100 px. */
  function layout(tops: Record<string, number>) {
    vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
      const top = this.hasAttribute('data-sticky-header') ? 0 : (tops[this.id] ?? 0)
      const bottom = this.hasAttribute('data-sticky-header') ? 100 : top + 300
      return { top, bottom, left: 0, right: 0, width: 0, height: bottom - top, x: 0, y: top, toJSON: () => ({}) } as DOMRect
    })
  }

  it('glides to the section pressed, below the header, marks it current at once and puts it in the address', async () => {
    media({})
    const scrolls = await page()
    const before = window.history.length
    const click = new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 })
    await act(async () => entry('Danger zone').dispatchEvent(click))
    expect(click.defaultPrevented).toBe(true)
    expect(scrolls.danger).toHaveBeenCalledWith({ block: 'start', behavior: 'smooth' })
    expect(current()).toBe('Danger zone')
    expect(window.location.hash).toBe('#danger')
    expect(window.history.length).toBe(before)
    expect(document.activeElement?.id).toBe('danger')
  })

  it('jumps instead when the system asks for less motion', async () => {
    media({ reduce: true })
    const scrolls = await page()
    await act(async () => entry('Server list').click())
    expect(scrolls.list).toHaveBeenCalledWith({ block: 'start', behavior: 'auto' })
    expect(current()).toBe('Server list')
  })

  it('leaves a press with a modifier key to the browser', async () => {
    media({})
    const scrolls = await page()
    const click = new MouseEvent('click', { bubbles: true, cancelable: true, button: 0, metaKey: true })
    await act(async () => entry('Danger zone').dispatchEvent(click))
    expect(click.defaultPrevented).toBe(false)
    expect(scrolls.danger).not.toHaveBeenCalled()
  })

  it('follows the scroll, and keeps the section pressed current at the end of the page', async () => {
    media({})
    layout({ game: 124, list: 440, danger: 760 })
    await page()
    await act(async () => frame())
    expect(current()).toBe('In the game')
    layout({ game: -700, list: 110, danger: 500 })
    await act(async () => {
      scrollTo(700)
      await frame()
    })
    expect(current()).toBe('Server list')

    // Pressed near the end, the page moves as far as it can but the section can't come up to the header.
    await act(async () => entry('Server list').click())
    vi.spyOn(document.documentElement, 'scrollHeight', 'get').mockReturnValue(1668)
    layout({ game: -500, list: 150, danger: 460 })
    await act(async () => {
      scrollTo(900)
      await new Promise((resolve) => setTimeout(resolve, 200))
      await frame()
    })
    expect(current()).toBe('Server list')

    // Scrolling yourself, the last section on screen is current at the end.
    await act(async () => {
      window.dispatchEvent(new Event('wheel'))
      scrollTo(900)
      await frame()
    })
    expect(current()).toBe('Danger zone')
  })

  it('follows a link elsewhere on the page to one of its sections, and Back, over the one pressed before', async () => {
    media({})
    layout({ game: 124, list: 440, danger: 760 })
    await page()
    await act(async () => entry('Server list').click())
    expect(current()).toBe('Server list')

    // Like the server menu's Delete, which goes to the Danger zone of the Settings already open.
    await act(async () => navigate('/#danger'))
    expect(current()).toBe('Danger zone')
    vi.spyOn(document.documentElement, 'scrollHeight', 'get').mockReturnValue(1668)
    layout({ game: -500, list: 150, danger: 460 })
    await act(async () => {
      scrollTo(900)
      await new Promise((resolve) => setTimeout(resolve, 200))
      await frame()
    })
    expect(current(), 'the section pressed before is still on screen at the end of the page').toBe('Danger zone')

    window.history.replaceState(null, '', '/#game')
    await act(async () => window.dispatchEvent(new PopStateEvent('popstate')))
    expect(current()).toBe('In the game')
  })
})

describe('switching pages and tabs', () => {
  it('puts what fades or slides in straight in place, and leaves spinners and loading shapes alone', () => {
    const running = (animationName: string, endTime = 200) => ({ animationName, finish: vi.fn(), effect: { getComputedTiming: () => ({ endTime }) } })
    const fade = running('fade')
    const enter = running('enter')
    const spin = running('spin', Infinity)
    const skeleton = running('skeleton', Infinity)
    const hover = { transitionProperty: 'color', finish: vi.fn(), effect: { getComputedTiming: () => ({ endTime: 120 }) } }
    ;(document as { getAnimations?: () => unknown[] }).getAnimations = () => [fade, enter, spin, skeleton, hover]
    appearAtOnce()
    expect(fade.finish).toHaveBeenCalled()
    expect(enter.finish).toHaveBeenCalled()
    expect(spin.finish).not.toHaveBeenCalled()
    expect(skeleton.finish).not.toHaveBeenCalled()
    expect(hover.finish).not.toHaveBeenCalled()
    delete (document as { getAnimations?: () => unknown[] }).getAnimations
  })

  it('has no page animation left in the styles', () => {
    expect(styles).not.toMatch(/animate-page|@keyframes page\b/)
  })
})

describe('the pointer', () => {
  const css = styles.replace(/\/\*[\s\S]*?\*\//g, '')
  const rule = (cursor: string) => {
    const m = new RegExp(`([^{}]+)\\{\\s*cursor:\\s*${cursor};\\s*\\}`).exec(css)
    if (!m?.[1]) throw new Error(`no cursor: ${cursor} rule`)
    return m[1].trim()
  }
  const pointer = rule('pointer')
  const notAllowed = rule('not-allowed')
  /** A part's own cursor class wins over the base rule; none may take the pointer away. */
  const noOverride = (el: Element) => expect(el.className, el.outerHTML.slice(0, 80)).not.toMatch(/\bcursor-(default|auto|text)\b/)

  it('shows on everything that can be pressed, and a disabled control shows it can’t be', async () => {
    await render(
      <>
        <Button>Restart</Button>
        <a href="/servers/survival">Survival</a>
        <label>
          <Switch />
          PvP
        </label>
        <Switch disabled aria-label="Off" />
        <Checkbox aria-label="Tick" />
        <RadioGroup defaultValue="a" aria-label="Pick">
          <Radio value="a" aria-label="A" />
        </RadioGroup>
        <Tabs defaultValue="one">
          <TabsList>
            <TabsTab value="one">One</TabsTab>
          </TabsList>
        </Tabs>
        <Toggle aria-label="Bold">B</Toggle>
        <Slider value={10} aria-label="View distance" />
        <details>
          <summary>Learn more</summary>
        </details>
      </>,
    )
    const clickable = ['button', 'a[href]', 'label', '[role="switch"]:not([data-disabled])', '[role="checkbox"]', '[role="radio"]', '[role="tab"]', 'summary']
    for (const sel of clickable) {
      const el = document.querySelector(sel)
      expect(el, sel).not.toBeNull()
      expect(el?.matches(pointer), sel).toBe(true)
      if (el) noOverride(el)
    }
    const off = document.querySelector('[role="switch"][data-disabled]')
    expect(off?.matches(notAllowed)).toBe(true)
    expect(document.querySelector('[data-slot="slider-control"]')?.className).toMatch(/\bcursor-pointer\b/)
  })

  it('shows on menu items and a select’s options', async () => {
    await render(
      <>
        <Menu open>
          <MenuTrigger>More</MenuTrigger>
          <MenuPopup>
            <MenuItem>Stop</MenuItem>
            <MenuCheckboxItem checked>Follow</MenuCheckboxItem>
            <MenuRadioGroup value="a">
              <MenuRadioItem value="a">Survival</MenuRadioItem>
            </MenuRadioGroup>
          </MenuPopup>
        </Menu>
        <Select open value="easy">
          <SelectTrigger aria-label="Difficulty">
            <SelectValue />
          </SelectTrigger>
          <SelectPopup>
            <SelectItem value="easy">Easy</SelectItem>
          </SelectPopup>
        </Select>
      </>,
    )
    for (const role of ['menuitem', 'menuitemcheckbox', 'menuitemradio', 'option', 'combobox']) {
      const el = document.querySelector(`[role="${role}"]`)
      expect(el, role).not.toBeNull()
      expect(el?.matches(pointer) || /\bcursor-pointer\b/.test(el?.className ?? ''), role).toBe(true)
      if (el) noOverride(el)
    }
  })
})
