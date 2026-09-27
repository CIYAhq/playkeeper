import { useCallback, useEffect, useMemo, useRef, useState, type MouseEvent } from 'react'
import { currentSection, scrollToSection } from '@/lib/scroll'
import { cn } from '@/lib/utils'

/** How long the page must be still before a jump counts as done. */
const stillMs = 150

const scrollKeys = new Set(['ArrowUp', 'ArrowDown', 'PageUp', 'PageDown', 'Home', 'End', ' '])

function askedIn(ids: readonly string[]): string | undefined {
  const hash = decodeURIComponent(window.location.hash.slice(1))
  return ids.includes(hash) ? hash : undefined
}

/**
 * Which of the page's sections a section list marks current, following the
 * scroll. A section pressed in the list is current from the press until the
 * page has stopped moving; the section in the address counts as asked for
 * until you scroll yourself.
 */
export function useSectionSpy(ids: readonly string[]) {
  const [active, setActive] = useState(() => askedIn(ids) ?? ids[0])
  const asked = useRef(askedIn(ids))
  const hold = useRef(0)
  const update = useRef<() => void>(() => undefined)

  useEffect(() => {
    let frame = 0
    const measure = () => {
      frame = 0
      if (hold.current) return
      const top = Math.max(0, document.querySelector('[data-sticky-header]')?.getBoundingClientRect().bottom ?? 0)
      const boxes = ids.flatMap((id) => {
        const r = document.getElementById(id)?.getBoundingClientRect()
        return r && r.bottom > r.top ? [{ id, top: r.top, bottom: r.bottom }] : []
      })
      const end = document.documentElement.scrollHeight - window.innerHeight
      const next = currentSection(boxes, { top, height: window.innerHeight, atEnd: end > 0 && window.scrollY >= end - 2 }, asked.current)
      if (next) setActive(next)
    }
    const schedule = () => {
      if (!frame) frame = window.requestAnimationFrame(measure)
    }
    const release = () => {
      window.clearTimeout(hold.current)
      hold.current = 0
      schedule()
    }
    const onScroll = () => {
      if (hold.current) {
        window.clearTimeout(hold.current)
        hold.current = window.setTimeout(release, stillMs)
      }
      schedule()
    }
    const takeOver = () => {
      asked.current = undefined
      if (hold.current) release()
    }
    const onKey = (e: KeyboardEvent) => {
      if (scrollKeys.has(e.key)) takeOver()
    }
    update.current = release
    window.addEventListener('scroll', onScroll, { passive: true })
    window.addEventListener('resize', schedule)
    window.addEventListener('wheel', takeOver, { passive: true })
    window.addEventListener('touchstart', takeOver, { passive: true })
    window.addEventListener('keydown', onKey)
    schedule()
    return () => {
      window.cancelAnimationFrame(frame)
      window.clearTimeout(hold.current)
      hold.current = 0
      window.removeEventListener('scroll', onScroll)
      window.removeEventListener('resize', schedule)
      window.removeEventListener('wheel', takeOver)
      window.removeEventListener('touchstart', takeOver)
      window.removeEventListener('keydown', onKey)
    }
  }, [ids])

  /** Marks `id` current now and keeps it current while the page moves there. */
  const follow = useCallback((id: string) => {
    asked.current = id
    setActive(id)
    window.clearTimeout(hold.current)
    hold.current = window.setTimeout(() => update.current(), stillMs)
  }, [])

  return { active, follow }
}

/**
 * A page's list of its sections. Pressing one scrolls smoothly to it (in one
 * jump with reduced motion), below the sticky header, puts it in the address
 * and moves focus to it; the current section follows the scroll.
 */
export function SectionNav({ sections, label, className }: { sections: { id: string; label: string }[]; label: string; className?: string }) {
  const key = sections.map((s) => s.id).join(' ')
  const ids = useMemo(() => key.split(' '), [key])
  const { active, follow } = useSectionSpy(ids)
  const go = (e: MouseEvent<HTMLAnchorElement>, id: string) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return
    const el = document.getElementById(id)
    if (!el) return
    e.preventDefault()
    follow(id)
    window.history.replaceState(window.history.state, '', `${window.location.pathname}${window.location.search}#${id}`)
    scrollToSection(el)
  }
  return (
    <nav aria-label={label} className={cn('flex flex-col gap-0.5', className)}>
      {sections.map((x) => (
        <a
          key={x.id}
          href={`#${x.id}`}
          onClick={(e) => go(e, x.id)}
          aria-current={active === x.id ? 'location' : undefined}
          className="rounded-lg px-2.5 py-1.5 text-[13px] font-medium text-muted-foreground hover:bg-accent hover:text-foreground aria-[current=location]:bg-accent aria-[current=location]:font-semibold aria-[current=location]:text-foreground"
        >
          {x.label}
        </a>
      ))}
    </nav>
  )
}
