import { useState, type ReactNode } from 'react'
import { ChevronLeftIcon, ChevronRightIcon, CpuIcon, EyeIcon, GaugeIcon, MapIcon, MemoryStickIcon } from 'lucide-react'
import { get } from '@/api/client'
import type { ActionKind, LagCause, MetricsResponse, Running, ServerStatus } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { Card, SectionLabel } from '@/components/app/bits'
import { Segmented, useIsPhone } from '@/components/app/controls'
import { LineChart } from '@/components/app/line-chart'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { formatLocale, t } from '@/i18n'
import { formatMB } from '@/lib/format'
import { num } from '@/lib/params'
import { linkPath, linkProps } from '@/lib/router'
import {
  bucketValues,
  causeAction,
  causeText,
  cpuAxis,
  formatTPS,
  memoryAxis,
  runningHeadline,
  targetTPS,
  tickRateAxis,
  tickTimeAxis,
  timeLabels,
  type Axis,
  type CauseAction,
  type CauseContext,
  type RunRange,
} from '@/lib/running'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

const actionIcons: Partial<Record<ActionKind, ReactNode>> = {
  raise_memory: <MemoryStickIcon />,
  lower_view_distance: <EyeIcon />,
  lower_simulation_distance: <EyeIcon />,
  pregenerate_world: <MapIcon />,
  run_profiler: <GaugeIcon />,
  raise_cpu_limit: <CpuIcon />,
}

function decimal(v: number, digits: number): string {
  return new Intl.NumberFormat(formatLocale(), { maximumFractionDigits: digits }).format(v)
}

interface ChartSpec {
  id: string
  title: string
  value?: string
  unit: string
  bad: boolean
  values: (number | null)[]
  axis: Axis
  isBad?: (v: number) => boolean
  threshold?: number
  thresholdLabel?: string
}

/** "How it's running": the headline, the four charts and what slows the server down. */
export function RunningPage({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const [range, setRange] = useState<RunRange>('1h')
  const shown: RunRange = phone ? '1h' : range
  const running = usePoll(() => get<Running>(serverApi(s.id, '/running')), 15_000, s.id)
  const metrics = usePoll(() => get<MetricsResponse>(serverApi(s.id, `/metrics?range=${shown}`)), shown === '1h' ? 30_000 : 60_000, `${s.id}:${shown}`)
  const r = running.data
  const head = r ? runningHeadline(r, s.name) : undefined
  const ctx: CauseContext = { server: s.name, machine: ws.machineName, slug: s.slug, players: r?.players, minutes: r?.windowMinutes ?? 10 }
  const causes = r && (r.status === 'a_bit_behind' || r.status === 'lagging') ? r.causes : []

  const buckets = metrics.data?.buckets ?? []
  const live = !ws.stale && s.phase === 'online' ? s.resources : undefined
  const target = targetTPS(r)
  const budgetMS = 1000 / target
  const limitMB = s.config?.memoryMB ?? 0
  // Memory is charted in GB, or in MB for budgets under 1 GB.
  const perUnit = limitMB >= 1024 ? 2 ** 30 : 2 ** 20
  const limit = limitMB >= 1024 ? limitMB / 1024 : limitMB
  const tps = live?.tps ?? num(r?.params, 'tps')
  const mspt = live?.mspt ?? num(r?.params, 'mspt')
  const tpsBad = (v: number) => v < 0.95 * target
  const msptBad = (v: number) => v > budgetMS
  // Java normally fills most of its container, so memory only turns amber when it's what slows the server down.
  const memPressure = causes.some((c) => c.kind === 'memory_pressure')
  const memBad = (v: number) => memPressure && limit > 0 && v >= 0.9 * limit
  const mspts = bucketValues(buckets, (b) => b.msptAvg)
  const cpus = bucketValues(buckets, (b) => b.cpuAvg)
  const memNow = live?.memBytes !== undefined ? live.memBytes / perUnit : undefined
  const charts: ChartSpec[] = [
    {
      id: 'tps',
      title: t('overview.tickRate'),
      value: tps !== undefined ? formatTPS(tps) : undefined,
      unit: phone ? t('running.tpsUnitPhone') : t('running.tpsUnit'),
      bad: tps !== undefined && tpsBad(tps),
      values: bucketValues(buckets, (b) => b.tpsAvg),
      axis: tickRateAxis(target),
      isBad: tpsBad,
      threshold: target,
      thresholdLabel: t('running.smoothLine', { target }),
    },
    {
      id: 'mspt',
      title: t('running.tickTime'),
      value: mspt !== undefined ? decimal(mspt, 0) : undefined,
      unit: t('running.msUnit'),
      bad: mspt !== undefined && msptBad(mspt),
      values: mspts,
      axis: tickTimeAxis(budgetMS, mspts),
      isBad: msptBad,
      threshold: budgetMS,
      thresholdLabel: t('running.budgetLine', { ms: decimal(budgetMS, 0) }),
    },
    {
      id: 'memory',
      title: t('overview.memory'),
      value: memNow !== undefined ? decimal(memNow, 1) : undefined,
      unit: t('running.memUnit', { memory: formatMB(limitMB) }),
      bad: memNow !== undefined && memBad(memNow),
      values: bucketValues(buckets, (b) => (b.memAvg === null ? null : b.memAvg / perUnit)),
      axis: memoryAxis(limitMB),
      isBad: memBad,
      threshold: limit > 0 ? limit : undefined,
      thresholdLabel: t('running.limitLine', { memory: formatMB(limitMB) }),
    },
    {
      id: 'cpu',
      title: t('overview.cpu'),
      value: live?.cpuPercent !== undefined ? decimal(live.cpuPercent, 0) : undefined,
      unit: t('running.cpuUnit'),
      bad: false,
      values: cpus,
      axis: cpuAxis(cpus),
    },
  ]
  const rangeLabel = { '1h': t('running.range.1h'), '24h': t('running.range.24h'), '7d': t('running.range.7d') }[shown]
  const labels = timeLabels(buckets, shown, phone)
  const loadingCharts = !metrics.data && !metrics.error

  const chart = (c: ChartSpec) => (
    <Card key={c.id} className={cn('p-4', phone && 'rounded-3xl')}>
      <div className="flex items-baseline justify-between gap-3">
        <h3 className={cn('font-semibold', phone ? 'text-[15px]' : 'text-[13px]')}>{c.title}</h3>
        <p className="flex items-baseline gap-1.5">
          <span className={cn('text-lg leading-6 font-bold tabular-nums', c.bad && 'text-warning-foreground')}>{c.value ?? '—'}</span>
          <span className="text-xs text-muted-foreground">{c.unit}</span>
        </p>
      </div>
      {loadingCharts ? (
        <Skeleton className={cn('mt-3 w-full', phone ? 'h-[82px]' : 'h-[110px]')} />
      ) : (
        <LineChart
          className="mt-3"
          plotClassName={phone ? 'h-[58px]' : 'h-[88px]'}
          values={c.values}
          max={c.axis.max}
          bad={c.isBad}
          threshold={c.threshold}
          thresholdLabel={phone ? undefined : c.thresholdLabel}
          yLabels={phone ? undefined : c.axis.labels}
          xLabels={labels}
          label={t('running.chartAria', { title: c.title, range: rangeLabel, value: c.value ? `${c.value} ${c.unit}` : '—' })}
        />
      )}
    </Card>
  )

  const headline = head ? (
    <div className="min-w-0">
      <h2 className={cn('font-bold tracking-[-0.01em]', phone ? 'text-[17px] leading-6' : 'text-xl leading-7')}>{head.title}</h2>
      {head.subtitle && <p className={cn('mt-0.5 text-muted-foreground', phone ? 'text-[15px] leading-5' : 'text-[13px]')}>{head.subtitle}</p>}
    </div>
  ) : running.error ? (
    <div className="min-w-0">
      <h2 className={cn('font-bold', phone ? 'text-[17px]' : 'text-xl')}>{t('running.loadFailed')}</h2>
      <p className="mt-0.5 text-[13px] text-muted-foreground">{errorText(running.error)}</p>
    </div>
  ) : (
    <div className="flex min-w-0 flex-col gap-2">
      <Skeleton className="h-7 w-80 max-w-full" />
      <Skeleton className="h-4 w-64 max-w-full" />
    </div>
  )

  const smooth = r?.status === 'smooth'
  const behind = r?.status === 'a_bit_behind' || r?.status === 'lagging'

  if (phone) {
    const [first, ...rest] = causes
    return (
      <div className="flex flex-col gap-4 pb-6">
        <div className="px-1">{headline}</div>
        {charts[0] && chart(charts[0])}
        {first && (
          <section aria-labelledby="slowing" className="mt-1">
            <SectionLabel className="px-4">
              <span id="slowing">{t('running.causesPhone')}</span>
            </SectionLabel>
            <FirstCause cause={first} ctx={ctx} />
            {rest.length > 0 && (
              <ul className="mt-3 overflow-hidden rounded-3xl border border-border bg-white">
                {rest.map((c) => (
                  <CauseLine key={c.kind} cause={c} ctx={ctx} />
                ))}
              </ul>
            )}
          </section>
        )}
        {smooth && <NothingSlow phone />}
        {behind && !first && <NoClearCause phone />}
      </div>
    )
  }

  return (
    <>
      <a {...linkProps({ name: 'server', slug: s.slug, tab: 'overview' })} className="-mt-1 inline-flex w-fit items-center gap-0.5 rounded-md text-[13px] font-medium text-success-strong outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
        <ChevronLeftIcon className="size-4" aria-hidden="true" />
        {t('tab.overview')}
      </a>
      <div className="mt-1 flex flex-wrap items-end justify-between gap-3">
        {headline}
        <Segmented
          value={range}
          onChange={setRange}
          label={t('overview.range')}
          options={[
            { value: '1h', label: t('running.range.1h') },
            { value: '24h', label: t('running.range.24h') },
            { value: '7d', label: t('running.range.7d') },
          ]}
        />
      </div>
      <div className="grid gap-4 md:grid-cols-2">{charts.map(chart)}</div>
      {causes.length > 0 && (
        <section aria-labelledby="slowing" className="mt-2">
          <h2 id="slowing" className="text-[15px] font-semibold">
            {t('running.causes')}
          </h2>
          <Card as="div" className="mt-3 p-0">
            <ol>
              {causes.map((c, i) => (
                <CauseRow key={c.kind} cause={c} rank={i + 1} ctx={ctx} primary={i === 0} />
              ))}
            </ol>
          </Card>
        </section>
      )}
      {smooth && <NothingSlow />}
      {behind && causes.length === 0 && <NoClearCause />}
    </>
  )
}

function ActionButton({ action, primary, size }: { action: CauseAction; primary: boolean; size?: 'touch' }) {
  const variant = primary ? 'default' : 'outline'
  const cls = size === 'touch' ? 'w-full text-base' : undefined
  if (action.mode === 'link') {
    return (
      <Button variant={variant} size={size} className={cls} render={<a {...linkPath(action.href)} />}>
        {actionIcons[action.kind]}
        {action.label}
      </Button>
    )
  }
  return (
    <Button variant={variant} size={size} className={cls} disabled>
      {actionIcons[action.kind]}
      {action.label}
    </Button>
  )
}

function CauseRow({ cause, rank, ctx, primary }: { cause: LagCause; rank: number; ctx: CauseContext; primary: boolean }) {
  const text = causeText(cause, ctx)
  const action = causeAction(cause, ctx)
  return (
    <li className="flex items-center gap-4 border-b border-border px-4 py-3.5 last:border-b-0">
      <span className="flex size-6 shrink-0 items-center justify-center self-start rounded-full border border-border text-xs text-muted-foreground tabular-nums" aria-hidden="true">
        {rank}
      </span>
      <div className="min-w-0 flex-1">
        <h3 className="text-sm font-semibold">{text.title}</h3>
        <p className="mt-0.5 text-[13px] leading-[18px]">{text.body}</p>
        {text.evidence && <p className="mt-1 text-xs text-muted-foreground">{text.evidence}</p>}
      </div>
      {action?.mode === 'advice' ? (
        <p className="max-w-[240px] shrink-0 text-right text-[13px] leading-[18px]">
          <span className="block font-medium">{action.label}</span>
          {action.note && <span className="block text-xs text-muted-foreground">{action.note}</span>}
        </p>
      ) : action ? (
        <div className="flex shrink-0 items-center gap-3">
          {action.mode === 'later' && <span className="text-xs text-muted-foreground">{t('common.comingLater')}</span>}
          <ActionButton action={action} primary={primary} />
        </div>
      ) : null}
    </li>
  )
}

function FirstCause({ cause, ctx }: { cause: LagCause; ctx: CauseContext }) {
  const text = causeText(cause, ctx)
  const action = causeAction(cause, ctx)
  return (
    <Card className="mt-2 p-4">
      <h3 className="text-[17px] leading-6 font-semibold">{text.title}</h3>
      <p className="mt-1 text-[15px] leading-5">{text.body}</p>
      {text.evidence && <p className="mt-2 text-[13px] text-muted-foreground">{text.evidence}</p>}
      {action?.mode === 'advice' ? (
        <p className="mt-3 text-[15px] font-medium">
          {action.label}
          {action.note && <span className="block text-[13px] font-normal text-muted-foreground">{action.note}</span>}
        </p>
      ) : action ? (
        <div className="mt-4">
          <ActionButton action={action} primary size="touch" />
          {action.mode === 'later' && <p className="mt-2 text-center text-[13px] text-muted-foreground">{t('common.comingLater')}</p>}
        </div>
      ) : null}
    </Card>
  )
}

function CauseLine({ cause, ctx }: { cause: LagCause; ctx: CauseContext }) {
  const text = causeText(cause, ctx)
  const action = causeAction(cause, ctx)
  const sub = action?.mode === 'later' ? `${action.label}${t('common.dot')}${t('common.comingLater')}` : action?.label
  const body = (
    <>
      <span className="min-w-0 flex-1">
        <span className="block text-base">{text.title}</span>
        {sub && <span className="block text-[13px] text-muted-foreground">{sub}</span>}
      </span>
      {action?.mode === 'link' && <ChevronRightIcon className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />}
    </>
  )
  return (
    <li className="border-b border-border last:border-b-0">
      {action?.mode === 'link' ? (
        <a {...linkPath(action.href)} className="flex min-h-16 items-center gap-3 px-4 py-2.5 transition-colors active:bg-accent">
          {body}
        </a>
      ) : (
        <div className="flex min-h-16 items-center gap-3 px-4 py-2.5">{body}</div>
      )}
    </li>
  )
}

function NothingSlow({ phone }: { phone?: boolean }) {
  return (
    <Card as="div" className={cn('flex-row items-center', phone ? 'gap-4 p-4' : 'gap-5 px-5 py-6')}>
      <Pip pose="cheer" size={phone ? 48 : 56} />
      <p className={cn('font-semibold', phone ? 'text-base' : 'text-[15px]')}>{t('running.nothing')}</p>
    </Card>
  )
}

function NoClearCause({ phone }: { phone?: boolean }) {
  return (
    <Card as="div" className={cn('flex-row items-center', phone ? 'gap-4 p-4' : 'gap-5 px-5 py-6')}>
      <Pip pose="search" size={phone ? 48 : 56} />
      <div className="min-w-0">
        <p className={cn('font-semibold', phone ? 'text-base' : 'text-[15px]')}>{t('running.noCause')}</p>
        <p className="mt-0.5 text-[13px] text-muted-foreground">{t('running.noCauseBody')}</p>
      </div>
    </Card>
  )
}
