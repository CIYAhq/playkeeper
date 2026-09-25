import type { Crash, CrashLine, DiagnosisAction, Params } from '@/api/types'
import { t } from '@/i18n'
import { formatClock, formatDate, formatList, formatMB, sameDay } from '@/lib/format'
import { num, str, strs } from '@/lib/params'

/**
 * What happened, in one line. Kinds without a line of their own, or without
 * the params it needs, fall back to the agent's English explanation.
 */
export function crashSummary(c: Crash, server: string, machine: string): string {
  const p = c.params
  switch (c.kind) {
    case 'container_memory_limit':
    case 'heap_out_of_memory': {
      const mb = num(p, 'budget_mb')
      return mb ? t('crash.memory', { memory: formatMB(mb) }) : t('crash.memoryPlain')
    }
    case 'metaspace_out_of_memory':
      return t('crash.metaspace')
    case 'thread_limit':
      return t('crash.threads')
    case 'watchdog': {
      const secs = num(p, 'seconds')
      return secs ? t('crash.watchdog', { seconds: Math.round(secs) }) : t('crash.watchdogPlain')
    }
    case 'port_in_use': {
      const reason = str(p, 'reason')
      const port = num(p, 'port')
      if (reason === 'in_use') return t('crash.portInside', { server })
      if (reason === 'address') return t('crash.portAddress', { machine })
      return !reason && port ? t('crash.portTaken', { machine, port }) : c.explanation
    }
    case 'newer_java': {
      const who = str(p, 'addon') ?? str(p, 'jar')
      const required = num(p, 'required')
      const available = num(p, 'available')
      return who && required && available ? t('crash.java', { who, required, available, server }) : c.explanation
    }
    case 'missing_dependency': {
      const addon = str(p, 'addon')
      const deps = strs(p, 'dependencies')
      return addon && deps.length ? t('crash.dependency', { addon, deps: formatList(deps), count: deps.length }) : c.explanation
    }
    case 'addon_failed': {
      const addon = str(p, 'addon')
      const jar = str(p, 'jar')
      if (addon) return t('crash.addonFailed', { addon })
      return jar ? t('crash.addonLoad', { jar }) : c.explanation
    }
    case 'mixin_failed': {
      const addon = str(p, 'addon')
      return addon ? t('crash.mixin', { addon }) : t('crash.mixinPlain')
    }
    case 'datapack_failed': {
      const pack = str(p, 'pack')
      return pack ? t('crash.datapack', { pack }) : t('crash.datapackPlain')
    }
    case 'corrupt_world': {
      if (str(p, 'file') === 'level.dat') return t('crash.levelDat')
      const x = num(p, 'chunk_x')
      const z = num(p, 'chunk_z')
      return x !== undefined && z !== undefined ? t('crash.chunk', { x: x * 16, z: z * 16 }) : t('crash.chunkPlain')
    }
    case 'world_locked':
      return t('crash.locked')
    case 'disk_full': {
      const free = num(p, 'free_mb')
      return c.certain || free === undefined ? t('crash.disk', { machine, server }) : t('crash.diskLow', { machine, free: formatMB(free) })
    }
    case 'eula':
      return t('crash.eula')
    case 'permission_denied':
      return t('crash.permission')
    case 'killed':
      return t('crash.killed')
    case 'incompatible_addon':
      return c.explanation
    case 'unknown':
      return c.start ? t('crash.unknownStart') : t('crash.unknown')
    default: {
      const unhandled: never = c.kind
      void unhandled
      return c.explanation
    }
  }
}

/** A quieter second line, for the kinds that have one. */
export function crashDetail(c: Crash): string | undefined {
  const backups = num(c.params, 'backups_mb')
  const disk = num(c.params, 'disk_mb')
  if (c.kind === 'disk_full' && backups && disk) return t('crash.diskDetail', { backups: formatMB(backups), disk: formatMB(disk) })
  return undefined
}

/** The last lines on a phone: two of them, without Java's package names. */
export function phoneLines<L extends Pick<CrashLine, 'text'>>(lines: L[]): L[] {
  return lines.slice(-2).map((l) => ({ ...l, text: l.text.replace(/^(?:[a-z][a-z0-9_]*\.)+(?=[A-Z]\w*(?:Exception|Error)\b)/, '') }))
}

/** What pressing the button does for a fix. */
export type FixPlan =
  | { kind: 'settings'; body: { memoryMB?: number; gameplay?: { viewDistance: number } } }
  | { kind: 'start' }
  | { kind: 'remove-addon'; jar: string }
  | { kind: 'restore'; backupId: string }
  | { kind: 'delete-backups'; ids: string[] }

export interface FixOption {
  id: string
  title: string
  hint?: string
  recommended: boolean
  /** Missing when Playkeeper can't do it; reason then says why. */
  plan?: FixPlan
  reason?: string
  button?: string
  /** A line under the button while this fix is picked. */
  footnote?: string
}

type FixText = Omit<FixOption, 'id' | 'recommended'>

/**
 * The fixes to pick from, in the agent's order. A fix Playkeeper can't do
 * yet stays in the list, disabled with a reason, and something always starts
 * the server.
 */
export function crashFixes(c: Crash, server: string, machine: string, phone: boolean, now: Date = new Date()): FixOption[] {
  const out = c.fixes.map((f, i): FixOption => ({ id: `${i}:${f.kind}`, recommended: !!f.recommended, ...fixText(c, f, server, machine, phone, now) }))
  const starts = out.some((o) => o.plan?.kind === 'start')
  if (c.kind === 'disk_full' && !starts && out.some((o) => o.plan?.kind === 'delete-backups')) {
    out.push({ id: 'myself', recommended: false, ...startText(t('crash.fix.myself'), server) })
  }
  if (!out.some((o) => o.plan)) out.push({ id: 'again', recommended: out.length === 0, ...startText(t('crash.fix.again', { server }), server) })
  return out
}

/** The fix to preselect: the recommended one when it can be done, else the first that can. */
export function preselect(options: FixOption[]): FixOption | undefined {
  return options.find((o) => o.plan && o.recommended) ?? options.find((o) => o.plan)
}

function startText(title: string, server: string, hint?: string): FixText {
  return { title, hint, plan: { kind: 'start' }, button: t('crash.do.start', { server }) }
}

/** The add-on's name when the diagnosis is about this jar, else the jar. */
function addonName(c: Crash, jar: string): string {
  return str(c.params, 'jar') === jar ? (str(c.params, 'addon') ?? jar) : jar
}

function fixText(c: Crash, f: DiagnosisAction, server: string, machine: string, phone: boolean, now: Date): FixText {
  const p = f.params
  const later = t('common.comingLater')
  switch (f.kind) {
    case 'raise_memory': {
      const to = num(p, 'to_mb')
      if (!to) return { title: f.title, reason: later }
      const free = formatMB(c.roomMB)
      const hint = c.roomMB > 0 ? (phone ? t('crash.fix.memoryFree', { machine, free }) : t('crash.fix.memoryFits', { free })) : undefined
      return { title: t('crash.fix.memory', { server, memory: formatMB(to) }), hint, plan: { kind: 'settings', body: { memoryMB: to } }, button: t('crash.do.save', { server }) }
    }
    case 'lower_view_distance': {
      const to = num(p, 'to')
      if (!to) return { title: f.title, reason: later }
      return { title: t('crash.fix.view', { to }), hint: t('crash.fix.viewHint'), plan: { kind: 'settings', body: { gameplay: { viewDistance: to } } }, button: t('crash.do.save', { server }) }
    }
    case 'restart':
      return restartText(c, server)
    case 'remove_addon': {
      const jar = str(p, 'jar')
      if (!jar) return { title: f.title, reason: later }
      return { title: t('crash.fix.remove', { addon: addonName(c, jar) }), hint: t('crash.fix.removeHint', { server }), plan: { kind: 'remove-addon', jar }, button: t('crash.do.remove', { server }) }
    }
    case 'update_addon':
      return { title: t('crash.fix.update', { addon: addonName(c, str(p, 'jar') ?? '') }), reason: later }
    case 'install_addon':
      return { title: t('crash.fix.install', { addon: str(p, 'name') ?? '' }), reason: later }
    case 'remove_datapack':
      return { title: t('crash.fix.removePack', { pack: str(p, 'pack') ?? '' }), reason: later }
    case 'restore_backup':
      return restoreText(p, server, now)
    case 'free_disk': {
      const ids = strs(p, 'backup_ids')
      if (!ids.length) return startText(t('crash.fix.myself'), server)
      return {
        title: t('crash.fix.deleteBackups', { count: ids.length }),
        hint: t('crash.fix.deleteHint', { size: formatMB(num(p, 'frees_mb') ?? 0), count: num(p, 'keep') ?? 0 }),
        plan: { kind: 'delete-backups', ids },
        button: t('crash.do.deleteBackups', { count: ids.length, server }),
      }
    }
    case 'change_port':
      return { title: t('crash.fix.port', { server }), reason: later }
    case 'accept_eula':
      return { title: t('crash.fix.eula'), reason: later }
    case 'fix_permissions':
      return { title: t('crash.fix.permissions', { server }), reason: later }
    case 'upgrade_host':
      return { title: str(p, 'resource') === 'memory' ? t('crash.fix.upgradeMemory') : f.title, reason: t('crash.fix.upgradeHint') }
    case 'run_profiler':
      return { title: t('crash.fix.profiler'), reason: later }
    case 'lower_memory':
    case 'pregenerate_world':
    case 'lower_simulation_distance':
    case 'move_to_dedicated_cpu':
    case 'raise_cpu_limit':
    case 'reduce_other_load':
      return { title: f.title, reason: later }
    default: {
      const unhandled: never = f.kind
      void unhandled
      return { title: f.title, reason: later }
    }
  }
}

/** Starting again, in words that fit what went wrong. */
function restartText(c: Crash, server: string): FixText {
  const p = c.params
  const budget = num(p, 'budget_mb')
  const port = num(p, 'port')
  const free = num(p, 'free_mb')
  if ((c.kind === 'container_memory_limit' || c.kind === 'heap_out_of_memory') && budget) {
    return startText(t('crash.fix.keep', { memory: formatMB(budget) }), server, t('crash.fix.keepHint'))
  }
  if (c.kind === 'port_in_use' && port && !str(p, 'reason')) return startText(t('crash.fix.samePort', { port }), server, t('crash.fix.samePortHint'))
  if (c.kind === 'corrupt_world' && str(p, 'file') !== 'level.dat') return startText(t('crash.fix.regrow'), server, t('crash.fix.regrowHint'))
  if (c.kind === 'world_locked') return startText(t('crash.fix.again', { server }), server, t('crash.fix.lockedHint'))
  if (c.kind === 'disk_full' && free !== undefined) return startText(t('crash.fix.again', { server }), server, t('crash.fix.freeNow', { free: formatMB(free) }))
  return startText(t('crash.fix.again', { server }), server)
}

function restoreText(p: Params | undefined, server: string, now: Date): FixText {
  const id = str(p, 'backup_id')
  const made = str(p, 'made_at')
  if (!id || !made) return { title: t('crash.fix.restorePlain'), reason: t('crash.fix.noBackup') }
  const at = new Date(made)
  const yesterday = new Date(now)
  yesterday.setDate(now.getDate() - 1)
  const time = formatClock(made)
  let title = t('crash.fix.restoreOn', { date: formatDate(made) })
  if (sameDay(at, now)) title = t('crash.fix.restoreToday', { time })
  else if (sameDay(at, yesterday)) title = t('crash.fix.restoreYesterday', { time })
  return {
    title,
    hint: sameDay(at, now) ? t('crash.fix.restoreHint', { time }) : t('crash.fix.restoreHintSince'),
    plan: { kind: 'restore', backupId: id },
    button: t('crash.do.restore', { server }),
    footnote: t('crash.restoreNote'),
  }
}
