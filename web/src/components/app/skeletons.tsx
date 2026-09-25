import type { ReactNode } from 'react'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n'
import { cn } from '@/lib/utils'

// Skeletons stand in for content that's loading, in the same boxes, so the
// page doesn't jump when it arrives. The shapes are hidden from screen
// readers; each group says "Loading…" once instead.

const widths = ['w-3/5', 'w-2/5', 'w-1/2', 'w-2/3', 'w-1/3', 'w-3/4']

/** A varied width for the `i`th line, so rows don't look stamped out. */
export function lineWidth(i: number): string {
  return widths[i % widths.length] ?? 'w-1/2'
}

export function LoadingLabel({ label }: { label?: string }) {
  return <span className="sr-only">{label ?? t('common.loading')}</span>
}

/** A skeleton that sits inside a line of text, such as a subtitle. */
export function InlineSkeleton({ className }: { className?: string }) {
  return (
    <>
      <LoadingLabel />
      <span aria-hidden="true" className={cn('inline-block h-3 animate-skeleton rounded-sm bg-black/[.06] align-middle', className)} />
    </>
  )
}

/**
 * List rows that are still loading: an optional picture, one or two lines of
 * text and an optional trailing shape. `rowClassName` should be the real
 * row's box (height, padding, borders) so nothing moves when data arrives.
 */
export function ListSkeleton({ rows = 3, rowClassName, className, face, lines = 2, trailing, label }: { rows?: number; rowClassName: string; className?: string; face?: string; lines?: 1 | 2; trailing?: ReactNode; label?: string }) {
  return (
    <ul className={className}>
      {Array.from({ length: rows }, (_, i) => (
        <li key={i} className={rowClassName}>
          {i === 0 && <LoadingLabel label={label} />}
          {face && <Skeleton className={cn('shrink-0', face)} />}
          <div className="flex min-w-0 flex-1 flex-col gap-2">
            <Skeleton className={cn('h-3', lineWidth(i))} />
            {lines === 2 && <Skeleton className={cn('h-2.5', lineWidth(i + 3))} />}
          </div>
          {trailing}
        </li>
      ))}
    </ul>
  )
}

/** Table rows that are still loading, one line per column; `end` columns line up on the right. */
export function TableSkeleton({ rows = 3, cols, rowClassName }: { rows?: number; cols: ('start' | 'end')[]; rowClassName: string }) {
  return (
    <>
      {Array.from({ length: rows }, (_, i) => (
        <tr key={i} className={rowClassName}>
          {cols.map((align, j) => (
            <td key={j} className="px-3">
              {i === 0 && j === 0 && <LoadingLabel />}
              <Skeleton className={cn('h-3', align === 'end' ? 'ml-auto w-12' : lineWidth(i + j))} />
            </td>
          ))}
        </tr>
      ))}
    </>
  )
}

/** Choice cards that are still loading, in the real grid; `card` sizes each one. */
export function CardsSkeleton({ count, className, card }: { count: number; className?: string; card: string }) {
  return (
    <div className={className}>
      <LoadingLabel />
      {Array.from({ length: count }, (_, i) => (
        <Skeleton key={i} className={cn('rounded-2xl', card)} />
      ))}
    </div>
  )
}

/** Meter rows (label, value and bar) that are still loading. */
export function MeterSkeleton({ rows = 3, className }: { rows?: number; className?: string }) {
  return (
    <div className={cn('flex flex-col gap-4', className)}>
      <LoadingLabel />
      {Array.from({ length: rows }, (_, i) => (
        <div key={i}>
          <div className="flex h-5 items-center justify-between gap-3">
            <Skeleton className={cn('h-3', i % 2 ? 'w-16' : 'w-24')} />
            <Skeleton className="h-3 w-20" />
          </div>
          <Skeleton className="mt-1.5 h-1.5 w-full rounded-full" />
        </div>
      ))}
    </div>
  )
}
