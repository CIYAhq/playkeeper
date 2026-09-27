import { useState } from 'react'
import { get } from '@/api/client'
import type { MetricsResponse, ServerStatus } from '@/api/types'
import { serverApi } from '@/api/workspace'
import { Card, CardTitle } from '@/components/app/bits'
import { Segmented } from '@/components/app/controls'
import { LoadingLabel } from '@/components/app/skeletons'
import { Skeleton } from '@/components/ui/skeleton'
import { formatLocale, t, type MessageKey } from '@/i18n'
import { niceMax, regroup, ticks, type Bar } from '@/lib/chart'
import { formatClock, formatDate, formatDateTime } from '@/lib/format'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

export type ChartRange = '24h' | '7d' | '30d'

// The agent's buckets are 10 minutes, 1 hour and 6 hours; bars are wider.
const factors: Record<ChartRange, number> = { '24h': 6, '7d': 3, '30d': 2 }

const skeletonBars = ['h-[20%]', 'h-[35%]', 'h-[25%]', 'h-[50%]', 'h-[40%]', 'h-[65%]', 'h-[55%]', 'h-[30%]', 'h-[45%]', 'h-[70%]', 'h-[60%]', 'h-[35%]', 'h-[25%]', 'h-[40%]', 'h-[55%]', 'h-[75%]', 'h-[50%]', 'h-[30%]', 'h-[45%]', 'h-[35%]', 'h-[20%]', 'h-[30%]', 'h-[50%]', 'h-[40%]']

// "now" is pinned to the axis's right edge: a label whose middle falls in
// this last part of the axis would run into it on a phone.
const nowReserve = 0.18

/** The label under a bar, if it has one: "now" under the last bar, and times, weekdays or dates under a few clear of it. */
export function axisLabel(bar: Bar, range: ChartRange, index: number, count: number): string | undefined {
  const d = new Date(bar.start)
  if (index === count - 1) return t('overview.chartNow')
  if (count - index - 0.5 < count * nowReserve) return undefined
  switch (range) {
    case '24h':
      return d.getHours() % 6 === 0 ? formatClock(bar.start) : undefined
    case '7d':
      return d.getHours() < 3 ? new Intl.DateTimeFormat(formatLocale(), { weekday: 'short' }).format(d) : undefined
    case '30d':
      return d.getHours() < 12 && d.getDate() % 7 === 1 ? formatDate(bar.start) : undefined
    default: {
      const unreachable: never = range
      return unreachable
    }
  }
}

function barTitle(bar: Bar): string {
  const when = formatDateTime(bar.start)
  switch (bar.state) {
    case 'online':
      return `${when}: ${t('unit.players', { count: bar.players ?? 0 })}`
    case 'offline':
      return `${when}: ${t('overview.chartStopped')}`
    case 'no_data':
      return `${when}: ${t('overview.chartMissing')}`
    case 'not_collected':
      return `${when}: ${t('overview.chartBefore')}`
    default: {
      const unreachable: never = bar.state
      return unreachable
    }
  }
}

export function PlayersChart({ server, className }: { server: ServerStatus; className?: string }) {
  const [range, setRange] = useState<ChartRange>('24h')
  const [table, setTable] = useState(false)
  const m = usePoll(() => get<MetricsResponse>(serverApi(server.id, `/metrics?range=${range}`)), 60_000, `${server.id}:${range}`)
  const bars = m.data ? regroup(m.data.buckets, factors[range], m.data.bucketSeconds) : []
  const top = ticks(niceMax(Math.max(1, ...bars.map((b) => b.players ?? 0))))
  const max = top[top.length - 1] ?? 1
  const titleKey: MessageKey = `overview.chartTitle.${range}`
  return (
    <Card className={className}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <CardTitle>{t(titleKey)}</CardTitle>
        <Segmented
          value={range}
          onChange={setRange}
          label={t('overview.range')}
          options={[
            { value: '24h', label: t('overview.range.24h') },
            { value: '7d', label: t('overview.range.7d') },
            { value: '30d', label: t('overview.range.30d') },
          ]}
        />
      </div>
      {table ? (
        <div className="mt-4 max-h-[220px] overflow-y-auto rounded-2xl border border-border" tabIndex={0} role="region" aria-label={t(titleKey)}>
          <table className="w-full text-[13px]">
            <thead className="sticky top-0 bg-muted text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">{t('overview.chartPeriod')}</th>
                <th className="px-3 py-2 font-medium">{t('overview.chartValue')}</th>
              </tr>
            </thead>
            <tbody>
              {bars.map((b) => (
                <tr key={b.start} className="border-t border-border">
                  <td className="px-3 py-1.5">{formatDateTime(b.start)}</td>
                  <td className="px-3 py-1.5 tabular-nums">{b.state === 'online' ? (b.players ?? 0) : barTitle(b).split(': ')[1]}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : !m.data ? (
        <div className="mt-4 flex gap-2">
          <LoadingLabel />
          <div className="w-4 shrink-0" />
          <div className="min-w-0 flex-1">
            <div className="flex h-[130px] items-end gap-[3px] border-b border-border">
              {skeletonBars.map((h, i) => (
                <Skeleton key={i} className={cn('min-w-0 flex-1 rounded-b-none', h)} />
              ))}
            </div>
            <div className="mt-1.5 h-4" />
          </div>
        </div>
      ) : (
        <div className="mt-4 flex gap-2" role="img" aria-label={t('overview.chartAriaPlain', { title: t(titleKey) })}>
          <div className="flex h-[150px] w-4 flex-col-reverse justify-between pb-5 text-right text-[11px] leading-none text-muted-foreground tabular-nums" aria-hidden="true">
            {top.map((v) => (
              <span key={v}>{v}</span>
            ))}
          </div>
          <div className="min-w-0 flex-1">
            <div className={cn('relative flex h-[130px] items-end border-b border-border', bars.length > 40 ? 'gap-px' : 'gap-[3px]')}>
              {bars.map((b) => (
                <div key={b.start} title={barTitle(b)} className="flex h-full min-w-0 flex-1 items-end">
                  {b.state === 'online' && (b.players ?? 0) > 0 && <div className="w-full rounded-t-[3px] bg-primary" style={{ height: `${((b.players ?? 0) / max) * 100}%` }} />}
                  {b.state === 'online' && !b.players && <div className="h-[3px] w-full rounded-full bg-border" />}
                  {b.state === 'offline' && <div className="h-full w-full rounded-t-[3px] bg-[#EEEEE7]" />}
                </div>
              ))}
            </div>
            <div className="relative mt-1.5 h-4 text-[11px] text-muted-foreground" aria-hidden="true">
              {bars.map((b, i) => {
                const text = axisLabel(b, range, i, bars.length)
                if (!text) return null
                const left = ((i + 0.5) / bars.length) * 100
                return (
                  <span key={b.start} className={cn('absolute whitespace-nowrap', i === bars.length - 1 ? 'right-0' : '-translate-x-1/2')} style={i === bars.length - 1 ? undefined : { left: `${left}%` }}>
                    {text}
                  </span>
                )
              })}
            </div>
          </div>
        </div>
      )}
      <div className="mt-auto flex flex-wrap items-center gap-x-4 gap-y-2 pt-4 text-xs text-muted-foreground">
        <span className="flex items-center gap-1.5">
          <span className="size-2.5 rounded-[3px] bg-primary" aria-hidden="true" />
          {t('overview.legendOnline')}
        </span>
        <span className="flex items-center gap-1.5">
          <span className="size-2.5 rounded-[3px] bg-[#EEEEE7]" aria-hidden="true" />
          {t('overview.legendStopped')}
        </span>
        <span className="flex items-center gap-1.5">
          <span className="size-2.5 rounded-[3px] bg-border" aria-hidden="true" />
          {t('overview.legendNoData')}
        </span>
        <button type="button" className={cn('font-medium text-primary hover:underline', !table && 'sr-only focus-visible:not-sr-only')} onClick={() => setTable((v) => !v)}>
          {table ? t('overview.chartTableHide') : t('overview.chartTable')}
        </button>
      </div>
    </Card>
  )
}
