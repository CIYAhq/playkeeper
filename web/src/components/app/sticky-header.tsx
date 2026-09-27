import { useEffect, useLayoutEffect, useRef, useState, type ComponentProps } from 'react'
import { useIsPhone } from '@/components/app/controls'
import { cn } from '@/lib/utils'

/** Whether the page has scrolled away from its top. */
function useScrolled(): boolean {
  const [scrolled, setScrolled] = useState(false)
  useEffect(() => {
    const check = () => setScrolled(window.scrollY > 0)
    check()
    window.addEventListener('scroll', check, { passive: true })
    return () => window.removeEventListener('scroll', check)
  }, [])
  return scrolled
}

// Desktop: the top of the page's card, drawn over the card's own top edge. Its
// chalk shadow covers the gap above the card and the corners beside its
// rounding, so the card looks the same while the page scrolls under it.
const desktop = 'sticky top-2 z-20 -mx-px -mt-px rounded-t-2xl border border-border bg-background shadow-[0_-9px_0_9px_var(--sidebar)]'
// Phones: a bar from edge to edge that reaches up under the status bar. It is
// clear at the top of the page and takes the chalk, blurred, with a hairline
// once the page scrolls under it, like a native navigation bar. The page
// itself still scrolls: one scroll area, and the bottom tab bar as it is.
const phone =
  'sticky top-0 z-20 -mx-4 -mt-[env(safe-area-inset-top)] border-t-[length:env(safe-area-inset-top)] border-transparent px-4 transition-[background-color,box-shadow] duration-(--motion-fast) ease-standard data-scrolled:bg-sidebar/95 data-scrolled:shadow-[0_1px_0_var(--border)] data-scrolled:backdrop-blur'

/**
 * A page header that stays at the top while the content below it scrolls.
 * Where its bottom edge stays is --header-h, the page's scroll padding
 * (styles.css), so a jump to a section or a focused control lands below it.
 */
export function StickyHeader({ className, children, ...props }: ComponentProps<'header'>) {
  const onPhone = useIsPhone()
  const ref = useRef<HTMLElement>(null)
  const scrolled = useScrolled()
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    const root = document.documentElement
    const measure = () => root.style.setProperty('--header-h', `${(Number.parseFloat(getComputedStyle(el).top) || 0) + el.offsetHeight}px`)
    measure()
    const resize = typeof ResizeObserver === 'undefined' ? undefined : new ResizeObserver(measure)
    resize?.observe(el)
    return () => {
      resize?.disconnect()
      root.style.removeProperty('--header-h')
    }
  }, [onPhone])
  return (
    <header ref={ref} data-sticky-header="" data-scrolled={scrolled ? '' : undefined} className={cn(onPhone ? phone : desktop, className)} {...props}>
      {children}
    </header>
  )
}
