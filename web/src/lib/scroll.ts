/** How the page moves to a place: smoothly, or in one jump when the system asks for less motion. */
export function scrollBehavior(): ScrollBehavior {
  return window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth'
}

/**
 * Scrolls a section to the top of the page, below any sticky header (the
 * page's scroll padding plus the section's scroll margin), and moves focus to
 * it when it takes focus, without a second jump.
 */
export function scrollToSection(el: HTMLElement) {
  el.scrollIntoView({ block: 'start', behavior: scrollBehavior() })
  if (el.matches('[tabindex]')) el.focus({ preventScroll: true })
}

export interface SectionBox {
  id: string
  top: number
  bottom: number
}

/** How far below the sticky header a section's top may be and still count as reached: its scroll margin, and some slack. */
const reach = 32

/**
 * The section a section list marks current, from where the sections are on
 * screen. `top` is where the sticky header ends, `height` the window's height.
 * A section is current once its top has come up to the header. At the end of
 * a page, where the last sections can't come up that far, the one asked for
 * (pressed in the list, or in the address) stays current while it's on
 * screen; otherwise the last one on screen is.
 */
export function currentSection(sections: SectionBox[], view: { top: number; height: number; atEnd: boolean }, asked?: string): string | undefined {
  let current = sections[0]?.id
  for (const s of sections) if (s.top <= view.top + reach) current = s.id
  if (!view.atEnd) return current
  const shown = sections.filter((s) => s.top < view.height && s.bottom > view.top)
  if (asked && shown.some((s) => s.id === asked)) return asked
  return shown.at(-1)?.id ?? current
}
