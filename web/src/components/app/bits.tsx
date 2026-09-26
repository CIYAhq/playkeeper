import { useEffect, useState, type ReactNode } from 'react'
import { CheckIcon, CopyIcon } from 'lucide-react'
import { playerHeadUrl } from '@/api/client'
import type { Operation, ServerStatus } from '@/api/types'
import { Button, type ButtonProps } from '@/components/ui/button'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { formatClock, relativeTime } from '@/lib/format'
import { awayShort } from '@/lib/machines'
import { isSettingUp, opLabel, phaseLabel, statusLabel, statusTone, type Tone } from '@/lib/phase'
import { cn } from '@/lib/utils'

export function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), intervalMs)
    return () => window.clearInterval(id)
  }, [intervalMs])
  return now
}

/** m:ss since a moment, for running jobs. */
export function Elapsed({ since }: { since: string }) {
  const now = useNow()
  const s = Math.max(0, Math.floor((now - new Date(since).getTime()) / 1000))
  const text = s >= 3600 ? `${Math.floor(s / 3600)}:${String(Math.floor((s % 3600) / 60)).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}` : `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
  return <span className="tabular-nums">{text}</span>
}

export function Spinner({ className }: { className?: string }) {
  return (
    <span className={cn('inline-block size-3 shrink-0 animate-spin rounded-full border-[1.5px] border-info/25 border-t-info', className)} aria-hidden="true" />
  )
}

export function Dot({ tone, className }: { tone: Tone; className?: string }) {
  return <ToneDot key={tone} tone={tone} className={className} />
}

function ToneDot({ tone, className }: { tone: Tone; className?: string }) {
  switch (tone) {
    case 'online':
      return <span className={cn('inline-block size-2 shrink-0 animate-fade rounded-full bg-success ring-3 ring-success/20', className)} aria-hidden="true" />
    case 'crashed':
      return <span className={cn('inline-block size-2 shrink-0 animate-fade rounded-full bg-destructive', className)} aria-hidden="true" />
    case 'busy':
      return <Spinner className={className} />
    case 'stopped':
    case 'unknown':
      return <span className={cn('inline-block size-2 shrink-0 animate-fade rounded-full border-[1.5px] border-muted-foreground/60', className)} aria-hidden="true" />
    default: {
      const unreachable: never = tone
      return unreachable
    }
  }
}

/** A server's state as a pill: dot, label and a short detail. */
export function serverState(st: ServerStatus | undefined, agentDown: boolean): { tone: Tone; label: string; detail?: string; labelClass: string } {
  if (agentDown || !st) return { tone: 'unknown', label: t('status.noLive'), labelClass: 'text-foreground' }
  if (isSettingUp(st) && st.operation) return { tone: 'busy', label: t('status.settingUp'), labelClass: 'text-info-foreground' }
  const tone = statusTone(st)
  switch (tone) {
    case 'online':
      return {
        tone,
        label: t('status.online'),
        detail: st.players ? (st.players.online > 0 ? t('status.playing', { count: st.players.online }) : t('status.nobodyYet')) : undefined,
        labelClass: 'text-success-foreground',
      }
    case 'crashed': {
      const at = st.crash?.at ?? st.softwareChanged?.detectedAt ?? st.stoppedAt
      return { tone, label: statusLabel(st), detail: at ? relativeTime(at) : undefined, labelClass: 'text-destructive-foreground' }
    }
    case 'busy':
      return { tone, label: phaseLabel(st.phase), labelClass: 'text-info-foreground' }
    case 'stopped':
    case 'unknown':
      return {
        tone,
        label: phaseLabel(st.phase),
        detail: st.phase === 'asleep' && st.sleep?.asleepSince ? t('status.asleepSince', { time: formatClock(st.sleep.asleepSince) }) : undefined,
        labelClass: 'text-foreground',
      }
    default: {
      const unreachable: never = tone
      return unreachable
    }
  }
}

export function StatusPill({
  server,
  agentDown = false,
  away,
  elapsed,
  showDetail = true,
  onChalk = false,
  className,
}: {
  server: ServerStatus | undefined
  agentDown?: boolean
  /** The machine the server runs on can't be reached: its name, and when it was last heard. */
  away?: { name: string; since?: string }
  elapsed?: string
  showDetail?: boolean
  onChalk?: boolean
  className?: string
}) {
  const s = away
    ? { tone: 'unknown' as const, label: t('machines.away.pill', { name: away.name }), detail: away.since ? awayShort(away.since, Date.now()) : undefined, labelClass: 'text-foreground' }
    : serverState(server, agentDown)
  const labelClass = onChalk ? s.labelClass.replace('text-success-foreground', 'text-success-strong') : s.labelClass
  return (
    <span className={cn('inline-flex h-[26px] shrink-0 items-center gap-1.5 rounded-full border border-border bg-white px-2.5 text-[13px] font-semibold', className)}>
      <Dot tone={s.tone} />
      <span key={s.label} className={cn('animate-fade transition-colors duration-(--motion-standard)', labelClass)}>
        {s.label}
      </span>
      {showDetail && s.detail && (
        <span className="font-normal text-muted-foreground">
          {t('common.dot')}
          {s.detail}
        </span>
      )}
      {elapsed && (
        <span className="font-normal text-muted-foreground">
          {t('common.dot')}
          <Elapsed since={elapsed} />
        </span>
      )}
    </span>
  )
}

/** A long job, shrunk into a pill with a timer; the whole pill opens its page. */
export function JobPill({ op, server, onClick }: { op: Operation; server: string; onClick?: () => void }) {
  return (
    <button type="button" onClick={onClick} className="inline-flex h-[26px] items-center gap-1.5 rounded-full border border-border bg-white px-2.5 text-[13px] font-semibold text-foreground shadow-outline hover:bg-accent/50">
      <Spinner />
      <span>{opLabel(op, server)}</span>
      <span className="font-normal text-muted-foreground">
        <Elapsed since={op.startedAt} />
      </span>
    </button>
  )
}

/** Plain 12 px text that replaces every badge. */
export function Marker({ tone = 'muted', children, className }: { tone?: 'muted' | 'green' | 'amber' | 'red'; children: ReactNode; className?: string }) {
  const color = { muted: 'text-muted-foreground', green: 'text-success-foreground', amber: 'text-warning-foreground', red: 'text-destructive-foreground' }[tone]
  return <span className={cn('text-xs font-medium', color, className)}>{children}</span>
}

export function Card({ className, children, as: As = 'section', ...rest }: { className?: string; children: ReactNode; as?: 'section' | 'div' | 'article' } & React.HTMLAttributes<HTMLElement>) {
  return (
    <As className={cn('flex flex-col rounded-3xl border border-border bg-card p-5 shadow-card', className)} {...rest}>
      {children}
    </As>
  )
}

export function CardTitle({ children, className, id }: { children: ReactNode; className?: string; id?: string }) {
  return (
    <h2 id={id} className={cn('text-[15px] leading-5 font-semibold', className)}>
      {children}
    </h2>
  )
}

export function CardHint({ children, className }: { children: ReactNode; className?: string }) {
  return <p className={cn('mt-0.5 text-[13px] leading-[18px] text-muted-foreground', className)}>{children}</p>
}

export function Kbd({ children }: { children: ReactNode }) {
  return <kbd className="inline-flex h-5 min-w-5 items-center justify-center rounded-sm border border-border bg-white px-1 font-sans text-[11px] font-medium text-muted-foreground">{children}</kbd>
}

function initials(name: string): string {
  const parts = name.replace(/[_\d]+/g, ' ').trim().split(/\s+/)
  const first = parts[0]?.[0] ?? name[0] ?? '?'
  return first.toUpperCase()
}

const faceColors = ['#E3F1E6', '#FFF3D1', '#E7EEFB', '#F7E4E4', '#EDE7F6', '#E6F4F1']

/**
 * A player's face from their own skin, served by the panel. Players on a
 * default skin, or whose skin can't be found, get their initial instead.
 */
export function PlayerFace({ name, uuid, size = 28, className }: { name: string; uuid?: string; size?: number; className?: string }) {
  const [failed, setFailed] = useState(false)
  const radius = Math.round(size / 5)
  const color = faceColors[[...name].reduce((a, c) => a + c.charCodeAt(0), 0) % faceColors.length]
  const src = playerHeadUrl(name, uuid)
  return (
    <span
      className={cn('relative inline-flex shrink-0 items-center justify-center overflow-hidden font-semibold text-foreground/80 after:absolute after:inset-0 after:rounded-[inherit] after:ring-1 after:ring-black/12 after:ring-inset', className)}
      style={{ width: size, height: size, borderRadius: radius, background: failed ? color : undefined, fontSize: Math.round(size * 0.45) }}
      role="img"
      aria-label={t('players.face', { name })}
    >
      {failed ? (
        initials(name)
      ) : (
        <img src={src} width={size} height={size} alt="" loading="lazy" className="pixelated h-full w-full" onError={() => setFailed(true)} />
      )}
    </span>
  )
}

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    const ta = document.createElement('textarea')
    ta.value = text
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    ta.remove()
    return ok
  }
}

export function CopyButton({ text, label, copiedLabel, toast, ...props }: { text: string; label?: string; copiedLabel?: string; toast?: string } & Omit<ButtonProps, 'onClick' | 'children'>) {
  const [done, setDone] = useState(false)
  async function copy() {
    const ok = await copyText(text)
    if (!ok) {
      toastManager.add({ title: t('toast.copyFailed'), type: 'error' })
      return
    }
    if (toast) toastManager.add({ title: toast, type: 'success' })
    setDone(true)
    window.setTimeout(() => setDone(false), 1800)
  }
  return (
    <Button variant="outline" size="sm" onClick={copy} aria-live="polite" {...props}>
      {done ? <CheckIcon /> : <CopyIcon />}
      {done ? (copiedLabel ?? t('common.copied')) : (label ?? t('common.copy'))}
    </Button>
  )
}

/** A section label: 11 px uppercase. */
export function SectionLabel({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn('section-label', className)}>{children}</div>
}

/** A notice is inline text: a bold title, a muted line (after it, or under it when stacked) and maybe an action. Never a box. */
export function Notice({ title, children, action, tone = 'default', stacked, className }: { title: ReactNode; children?: ReactNode; action?: ReactNode; tone?: 'default' | 'warning' | 'error'; stacked?: boolean; className?: string }) {
  const titleColor = { default: 'text-foreground', warning: 'text-warning-foreground', error: 'text-destructive-foreground' }[tone]
  return (
    <div className={cn('flex flex-wrap items-center gap-x-4 gap-y-2', className)} role={tone === 'error' ? 'alert' : 'status'}>
      <p className="min-w-0 flex-1 text-[13px] leading-5">
        <strong className={cn('font-semibold', titleColor)}>{title}</strong>
        {children && (stacked ? <span className="mt-0.5 block text-xs text-muted-foreground">{children}</span> : <span className="text-muted-foreground"> {children}</span>)}
      </p>
      {action}
    </div>
  )
}

/** A labelled bar: the name on the left, the reading on the right. */
export function MeterRow({ label, value, percent, className }: { label: string; value: string; percent: number | undefined; className?: string }) {
  return (
    <div className={className}>
      <div className="flex items-baseline justify-between gap-3 text-[13px]">
        <span className="font-medium">{label}</span>
        <span className="text-muted-foreground tabular-nums">{value}</span>
      </div>
      <Progress value={percent ?? 0} className="mt-1.5" label={label} />
    </div>
  )
}

export function Progress({ value, tone = 'primary', className, label }: { value: number; tone?: 'primary' | 'info' | 'muted'; className?: string; label?: string }) {
  const pct = Math.max(0, Math.min(100, value))
  return (
    <div className={cn('h-1.5 w-full overflow-hidden rounded-full bg-foreground/8', className)} role="progressbar" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(pct)} aria-label={label}>
      <div className={cn('h-full rounded-full transition-[width,background-color] duration-(--motion-slow) ease-standard', { primary: 'bg-primary', info: 'bg-info', muted: 'bg-muted-foreground/60' }[tone])} style={{ width: `${pct}%` }} />

    </div>
  )
}
