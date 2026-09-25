import type { ActionKind, DiagnosisAction, DiagnosisEvidence, LagCause, MetricsBucket, Running } from '@/api/types'
import { formatLocale, t } from '@/i18n'
import { niceMax } from '@/lib/chart'
import { formatClock, formatDate, formatMB, formatMs, formatPercent, sameDay } from '@/lib/format'
import { num, str } from '@/lib/params'

export type RunRange = '1h' | '24h' | '7d'

/** The tick rate the agent measured against: 20 on every normal server. */
export function targetTPS(r: Running | undefined): number {
  return num(r?.params, 'target_tps') ?? 20
}

function evidence(list: DiagnosisEvidence[], kind: string) {
  return list.find((e) => e.kind === kind)?.params
}

function number(v: number, digits: number): string {
  return new Intl.NumberFormat(formatLocale(), { maximumFractionDigits: digits, minimumFractionDigits: 0 }).format(v)
}

/** "17.1": a tick rate with one decimal, like the game's own /tps. */
export function formatTPS(v: number): string {
  return new Intl.NumberFormat(formatLocale(), { minimumFractionDigits: 1, maximumFractionDigits: 1 }).format(v)
}

function cores(v: number): string {
  const rounded = Math.round(v * 10) / 10
  return t('unit.cores', { count: rounded, value: rounded })
}

/** Chunks in the square a player keeps loaded at a distance. */
function square(distance: number): number {
  return (2 * distance + 1) ** 2
}

/** Whole ticks in the headline, but never "20 of 20" while it is behind. */
export function headlineTPS(tps: number, target: number, behind: boolean): number {
  const whole = Math.round(tps)
  return behind && whole >= target && tps < target ? Math.floor(tps * 10) / 10 : whole
}

function when(iso: string, now: Date): string {
  return sameDay(new Date(iso), now) ? formatClock(iso) : `${formatDate(iso)} ${formatClock(iso)}`
}

export interface Headline {
  title: string
  subtitle?: string
}

/** "A bit behind: 17 of 20 ticks a second", and the line under it. */
export function runningHeadline(r: Running, server: string, now: Date = new Date()): Headline {
  const p = r.params
  const tps = num(p, 'tps')
  const target = targetTPS(r)
  switch (r.status) {
    case 'smooth':
      return { title: tps !== undefined ? t('running.smooth', { tps: headlineTPS(tps, target, false), target }) : t('running.smoothPlain'), subtitle: smoothLine(r, server, target) }
    case 'a_bit_behind':
    case 'lagging': {
      const lagging = r.status === 'lagging'
      const title =
        tps !== undefined ? t(lagging ? 'running.lagging' : 'running.behind', { tps: headlineTPS(tps, target, true), target }) : t(lagging ? 'running.laggingPlain' : 'running.behindPlain')
      return { title, subtitle: behindLine(r, now) }
    }
    case 'frozen':
      return { title: t('running.frozen'), subtitle: t('running.frozenBody') }
    case 'unknown':
      if (p?.running === false) return { title: t('running.off'), subtitle: t('running.offBody', { server }) }
      if (p?.sprinting === true) return { title: t('running.sprinting'), subtitle: t('running.sprintingBody') }
      return { title: t('running.unmeasured'), subtitle: t('running.unmeasuredBody', { server }) }
    default: {
      const unknown: never = r.status
      void unknown
      return { title: r.title, subtitle: r.explanation }
    }
  }
}

function smoothLine(r: Running, server: string, target: number): string | undefined {
  const mspt = num(r.params, 'mspt')
  const busy = mspt !== undefined && mspt >= 0.8 * (1000 / target)
  if (r.players === undefined) return mspt === undefined ? undefined : t(busy ? 'running.busy' : 'running.room', { server })
  if (mspt === undefined) return t('running.players', { count: r.players })
  return t(busy ? 'running.playersBusy' : 'running.playersRoom', { count: r.players, server })
}

function behindLine(r: Running, now: Date): string | undefined {
  const players = r.players ?? 0
  if (r.behindSince) {
    const time = when(r.behindSince, now)
    if (players > 0 && r.causes.some((c) => c.kind === 'chunk_generation')) return t('running.sinceExploring', { time, count: players })
    if (players > 0) return t('running.sincePlayers', { time, count: players })
    return t('running.since', { time })
  }
  const overloads = num(r.params, 'overloads') ?? 0
  if (overloads > 0) return t('running.overloads', { count: overloads, minutes: r.windowMinutes })
  const slowest = num(evidence(r.evidence, 'tick_time'), 'max_ms')
  return slowest !== undefined && slowest >= 500 ? t('running.slowTick', { time: formatMs(slowest) }) : undefined
}

export interface CauseContext {
  server: string
  machine: string
  slug: string
  players?: number
  /** The minutes the measurements cover. */
  minutes: number
}

export interface CauseText {
  title: string
  body: string
  evidence?: string
}

/**
 * A cause in plain words: a title, one sentence and one line of numbers.
 * Kinds without the params their words need keep the agent's English.
 */
export function causeText(c: LagCause, ctx: CauseContext): CauseText {
  const p = c.params
  const agent: CauseText = { title: c.title, body: c.explanation, evidence: c.evidence[0]?.text }
  switch (c.kind) {
    case 'cpu_steal': {
      const percent = num(p, 'percent')
      if (percent === undefined) return agent
      return { title: t('running.steal'), body: t('running.stealBody', { server: ctx.server }), evidence: t('running.stealEvidence', { percent: formatPercent(percent) }) }
    }
    case 'cpu_limit': {
      const limit = num(p, 'cores')
      const used = num(p, 'used_cores')
      if (limit === undefined || used === undefined) return agent
      return { title: t('running.cpuLimit'), body: t('running.cpuLimitBody', { cores: cores(limit) }), evidence: t('running.cpuUsed', { cores: cores(used) }) }
    }
    case 'host_cpu_busy': {
      const busy = num(p, 'busy_percent')
      const others = num(p, 'others_percent')
      if (busy === undefined) return agent
      const line = t('running.hostEvidence', { busy: formatPercent(busy) })
      if (others === undefined) return { title: t('running.host'), body: t('running.hostBody', { server: ctx.server }), evidence: line }
      if (others >= 30) {
        return { title: t('running.others'), body: t('running.othersBody', { machine: ctx.machine }), evidence: t('running.othersEvidence', { busy: formatPercent(busy), others: formatPercent(others) }) }
      }
      return { title: t('running.hostSmall', { machine: ctx.machine }), body: t('running.hostSmallBody', { server: ctx.server, machine: ctx.machine }), evidence: line }
    }
    case 'memory_pressure': {
      const after = num(evidence(c.evidence, 'heap_after_gc'), 'percent')
      const pauses = num(evidence(c.evidence, 'gc_pauses'), 'percent')
      const full = num(evidence(c.evidence, 'full_gc'), 'count')
      const ranOut = num(evidence(c.evidence, 'evacuation_failure'), 'count')
      let line: string | undefined
      if (pauses !== undefined) line = t('running.memoryPauses', { percent: formatPercent(pauses), minutes: ctx.minutes })
      else if (full) line = t('running.memoryFull', { count: full, minutes: ctx.minutes })
      else if (ranOut) line = t('running.memoryRanOut', { count: ranOut })
      return { title: t('running.memory'), body: after !== undefined ? t('running.memoryBody', { percent: formatPercent(after) }) : t('running.memoryBodyPlain'), evidence: line }
    }
    case 'chunk_generation': {
      const count = num(p, 'count')
      return {
        title: (ctx.players ?? 0) > 0 ? t('running.chunks') : t('running.chunksPlain'),
        body: t('running.chunksBody'),
        evidence: count !== undefined ? t('running.chunksEvidence', { count, minutes: ctx.minutes }) : undefined,
      }
    }
    case 'slow_disk': {
      const percent = num(p, 'percent')
      return { title: t('running.disk'), body: t('running.diskBody'), evidence: percent !== undefined ? t('running.diskEvidence', { percent: formatPercent(percent) }) : undefined }
    }
    case 'high_distance': {
      const view = num(p, 'view_distance')
      const sim = num(p, 'simulation_distance')
      if (view !== undefined && sim !== undefined) return { title: t('running.view'), body: t('running.viewSimBody', { view, sim }), evidence: t('running.viewEvidence', { chunks: square(view) }) }
      if (view !== undefined) return { title: t('running.view'), body: t('running.viewBody', { view }), evidence: t('running.viewEvidence', { chunks: square(view) }) }
      if (sim !== undefined) return { title: t('running.sim'), body: t('running.simBody', { sim }), evidence: t('running.simEvidence', { chunks: square(sim) }) }
      return agent
    }
    case 'world_workload': {
      const cpu = num(p, 'server_cpu')
      return { title: t('running.workload'), body: t('running.workloadBody'), evidence: cpu !== undefined ? t('running.cpuUsed', { cores: cores(cpu / 100) }) : undefined }
    }
    default: {
      const unknown: never = c.kind
      void unknown
      return agent
    }
  }
}

/**
 * What the one button of a cause does: open the setting that fixes it, wait
 * for a feature that is coming later, or say what to do elsewhere.
 */
export type CauseAction =
  | { mode: 'link'; kind: ActionKind; label: string; href: string }
  | { mode: 'later'; kind: ActionKind; label: string }
  | { mode: 'advice'; kind: ActionKind; label: string; note?: string }

function actionView(a: DiagnosisAction, ctx: CauseContext): CauseAction {
  const p = a.params
  const advice: CauseAction = { mode: 'advice', kind: a.kind, label: a.title }
  switch (a.kind) {
    case 'raise_memory': {
      const to = num(p, 'to_mb')
      return to ? { mode: 'link', kind: a.kind, label: t('running.giveMemory', { memory: formatMB(to) }), href: `/servers/${ctx.slug}/settings?memory=${to}#memory` } : advice
    }
    case 'lower_view_distance': {
      const to = num(p, 'to')
      return to ? { mode: 'link', kind: a.kind, label: t('running.lowerView', { to }), href: `/servers/${ctx.slug}/settings?view=${to}#game` } : advice
    }
    case 'lower_simulation_distance': {
      const to = num(p, 'to')
      return { mode: 'later', kind: a.kind, label: to ? t('running.lowerSim', { to }) : a.title }
    }
    case 'pregenerate_world':
      return { mode: 'later', kind: a.kind, label: t('running.pregen') }
    case 'run_profiler':
      return { mode: 'later', kind: a.kind, label: t('running.profiler') }
    case 'raise_cpu_limit':
      return { mode: 'later', kind: a.kind, label: t('running.moreCPU') }
    case 'move_to_dedicated_cpu':
      return { mode: 'advice', kind: a.kind, label: t('running.dedicated'), note: t('running.atProvider') }
    case 'reduce_other_load':
      return { mode: 'advice', kind: a.kind, label: t('running.reduceLoad', { machine: ctx.machine }) }
    case 'upgrade_host': {
      const resource = str(p, 'resource')
      const label = resource === 'memory' ? t('running.upgradeMemory') : resource === 'disk' ? t('running.upgradeDisk') : t('running.upgradeCPU')
      return { mode: 'advice', kind: a.kind, label, note: t('running.atProvider') }
    }
    case 'lower_memory':
    case 'restart':
    case 'remove_addon':
    case 'update_addon':
    case 'install_addon':
    case 'remove_datapack':
    case 'restore_backup':
    case 'free_disk':
    case 'change_port':
    case 'accept_eula':
    case 'fix_permissions':
      return advice
    default: {
      const unknown: never = a.kind
      void unknown
      return advice
    }
  }
}

/** A cause's one action: the first that Playkeeper can do now, else the one the agent recommends. */
export function causeAction(c: LagCause, ctx: CauseContext): CauseAction | undefined {
  const views = c.actions.map((a) => actionView(a, ctx))
  const recommended = c.actions.findIndex((a) => a.recommended)
  return views.find((v) => v.mode === 'link') ?? views[Math.max(recommended, 0)]
}

/** A chart's scale: the top value and the labels at the top, the middle and zero. */
export interface Axis {
  max: number
  labels: [string, string, string]
}

function axis(max: number, top: string): Axis {
  return { max, labels: [top, number(max / 2, 1), number(0, 0)] }
}

function highest(values: (number | null)[]): number {
  return Math.max(0, ...values.filter((v): v is number => v !== null))
}

export function tickRateAxis(target: number): Axis {
  return axis(target, number(target, 1))
}

/** Tick time leaves room above the budget, so a slow stretch shows as a climb over the dashed line. */
export function tickTimeAxis(budgetMS: number, values: (number | null)[]): Axis {
  const top = highest(values)
  const max = top > budgetMS * 1.6 ? niceMax(top) : budgetMS * 1.6
  return axis(max, number(max, 1))
}

/** Memory in GB up to the server's limit (MB below 1 GB). */
export function memoryAxis(limitMB: number): Axis {
  const gb = limitMB >= 1024
  const max = gb ? limitMB / 1024 : limitMB
  return axis(max, gb ? formatMB(limitMB) : t('unit.mb', { value: max }))
}

/** CPU in percent of one core, at least 100. */
export function cpuAxis(values: (number | null)[]): Axis {
  const max = Math.max(100, niceMax(highest(values)))
  return axis(max, t('unit.percent', { value: max }))
}

export function bucketValues(buckets: MetricsBucket[], pick: (b: MetricsBucket) => number | null): (number | null)[] {
  return buckets.map((b) => (b.state === 'online' ? pick(b) : null))
}

export interface TimeLabel {
  /** From 0 (the first bucket) to 1 (now). */
  at: number
  text: string
}

/** Clock times along the bottom (days for a week), ending with "now". */
export function timeLabels(buckets: MetricsBucket[], range: RunRange, phone = false): TimeLabel[] {
  const n = buckets.length
  const first = buckets[0]
  const last = buckets[n - 1]
  if (!first || !last || n < 2) return []
  const out: TimeLabel[] = []
  if (range === '7d') {
    const weekday = new Intl.DateTimeFormat(formatLocale(), { weekday: 'short' })
    buckets.forEach((b, i) => {
      const d = new Date(b.start)
      if (i > 0 && d.getHours() === 0 && i / (n - 1) < 0.92) out.push({ at: i / (n - 1), text: weekday.format(d) })
    })
  } else {
    const from = Date.parse(first.start)
    const span = Date.parse(last.start) - from
    for (const at of phone ? [0, 0.5] : [0, 0.25, 0.5, 0.75]) out.push({ at, text: formatClock(new Date(from + at * span).toISOString()) })
  }
  out.push({ at: 1, text: t('overview.chartNow') })
  return out
}
