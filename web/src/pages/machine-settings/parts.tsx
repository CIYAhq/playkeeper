import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { CheckIcon, ChevronRightIcon, CopyIcon, ExternalLinkIcon, RefreshCwIcon } from 'lucide-react'
import { ApiError } from '@/api/client'
import type { Address } from '@/api/types'
import { Card, copyText, CopyButton, Notice, Spinner } from '@/components/app/bits'
import { Pip, type PipPose } from '@/components/app/art'
import { useIsPhone } from '@/components/app/controls'
import { Button } from '@/components/ui/button'
import { Dialog, DialogPopup } from '@/components/ui/dialog'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { rich } from '@/i18n/rich'
import { certValid, dashboardURL } from '@/lib/address'
import { formatDate } from '@/lib/format'
import { cn } from '@/lib/utils'
import { Group } from '../more'
import { DialogButtons, DialogHeading } from '../two-factor'

/** What each view of the address gets from the page. */
export interface AddressProps {
  id: string
  a: Address
  machine: string
  refresh: () => Promise<void>
}

export interface AddressRowData {
  id: string
  label: string
  value: string
  status?: string
}

/** The dashboard's row among the addresses. */
export function dashboardRow(host: string, port: number, status?: string): AddressRowData {
  return { id: 'dashboard', label: t('address.dashboard'), value: dashboardURL(host, port), status }
}

/** Open's label: the whole URL on desktop, without https:// on phones. */
export function openLabel(url: string, phone: boolean): string {
  return t('address.open', { url: phone ? url.replace(/^https:\/\//, '') : url })
}

/** A refused request, and when the refusal says to try again. */
export interface Failure {
  error: ApiError
  until?: number
}

export function failure(e: unknown): Failure {
  const error = e instanceof ApiError ? e : new ApiError(0, { error: String(e), code: 'internal' })
  const seconds = Number(error.params?.retryAfterSeconds ?? error.retryAfter ?? 0)
  return { error, until: seconds > 0 ? Date.now() + seconds * 1000 : undefined }
}

export function isFailure(v: unknown): v is Failure {
  return typeof v === 'object' && v !== null && (v as Failure).error instanceof ApiError
}

/**
 * Looks a key up once typing pauses and remembers each answer, refusals
 * included, until forgotten. An empty key looks nothing up.
 */
export function useLookup<T>(key: string, load: (key: string) => Promise<T>, delayMs: number): { answer: T | Failure | undefined; forget: () => void } {
  const [answers, setAnswers] = useState<Record<string, T | Failure>>({})
  const loadRef = useRef(load)
  useEffect(() => {
    loadRef.current = load
  })
  const answer = key ? answers[key] : undefined
  const known = answer !== undefined
  useEffect(() => {
    if (!key || known) return
    const timer = window.setTimeout(() => {
      loadRef.current(key).then(
        (v) => setAnswers((m) => ({ ...m, [key]: v })),
        (e: unknown) => setAnswers((m) => ({ ...m, [key]: failure(e) })),
      )
    }, delayMs)
    return () => window.clearTimeout(timer)
  }, [key, known, delayMs])
  const forget = useCallback(
    () =>
      setAnswers((m) => {
        const rest = { ...m }
        delete rest[key]
        return rest
      }),
    [key],
  )
  return { answer, forget }
}

/** A copy button that shows only its icon, named after what it copies. */
export function CopyIconButton({ value, touch, className }: { value: string; touch?: boolean; className?: string }) {
  const [done, setDone] = useState(false)
  async function copy() {
    if (!(await copyText(value))) {
      toastManager.add({ title: t('toast.copyFailed'), type: 'error' })
      return
    }
    setDone(true)
    window.setTimeout(() => setDone(false), 1800)
  }
  return (
    <Button
      variant={touch ? 'outline' : 'ghost'}
      size={touch ? 'icon-xl' : 'icon-xs'}
      className={cn('shrink-0 text-muted-foreground', touch && 'size-11 rounded-xl sm:size-11', className)}
      aria-label={t('address.copyValue', { value })}
      onClick={() => void copy()}
    >
      {done ? <CheckIcon className="text-success-foreground" /> : <CopyIcon />}
    </Button>
  )
}

/** A link out of Playkeeper, in the small green style. */
export function OutLink({ href, children, className }: { href: string; children: ReactNode; className?: string }) {
  return (
    <a href={href} target="_blank" rel="noreferrer" className={cn('inline-flex items-center gap-1 text-xs font-medium text-success-foreground hover:underline', className)}>
      {children}
      <ExternalLinkIcon className="size-3" aria-hidden="true" />
    </a>
  )
}

/** Let's Encrypt's terms, accepted by the button next to it until an admin has. */
export function TermsLine({ a, className }: { a: Address; className?: string }) {
  if (a.termsAccepted) return null
  return (
    <p className={cn('text-xs text-muted-foreground', className)}>
      {rich('address.terms', {
        link: (chunk) => (
          <a href={t('address.termsUrl')} target="_blank" rel="noreferrer" className="font-medium text-success-foreground hover:underline">
            {chunk}
          </a>
        ),
      })}
    </p>
  )
}

export type Tone = 'default' | 'green' | 'amber' | 'red'

const toneClass: Record<Tone, string> = {
  default: 'text-foreground',
  green: 'text-success-foreground',
  amber: 'text-warning-foreground',
  red: 'text-destructive-foreground',
}

/**
 * One result: a coloured title, a line on what to do, and maybe buttons or a
 * footnote. The line is dark when there's something to fix.
 */
export function ResultBlock({ tone = 'default', spinner, title, children, footer, actions, className }: { tone?: Tone; spinner?: boolean; title: ReactNode; children?: ReactNode; footer?: ReactNode; actions?: ReactNode; className?: string }) {
  return (
    <div className={cn('px-4 py-3.5', className)} role={tone === 'red' ? 'alert' : 'status'}>
      <p className={cn('flex items-center gap-2 text-sm font-semibold', toneClass[tone])}>
        {spinner && <Spinner className="size-3.5" />}
        {title}
      </p>
      {children && <div className={cn('mt-1 text-[13px] leading-[18px]', tone === 'red' || tone === 'amber' ? 'text-foreground' : 'text-muted-foreground')}>{children}</div>}
      {actions && <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-2">{actions}</div>}
      {footer && <div className="mt-2.5 text-xs text-muted-foreground">{footer}</div>}
    </div>
  )
}

export function RetryButton({ label = t('common.tryAgain'), busy, disabled, touch, onClick }: { label?: string; busy?: boolean; disabled?: boolean; touch?: boolean; onClick: () => void }) {
  return (
    <Button variant="outline" size={touch ? 'touch' : 'sm'} className={cn(touch && 'w-full')} loading={busy} disabled={disabled} onClick={onClick}>
      {!disabled && <RefreshCwIcon />}
      {label}
    </Button>
  )
}

/** A value to copy in a phone list: the value, what it is, and a Copy button. */
export function PhoneCopyRow({ value, label }: { value: string; label: ReactNode }) {
  return (
    <li className="flex min-h-[60px] items-center gap-3 py-2 pr-2.5 pl-4">
      <span className="min-w-0 flex-1">
        <span className="block text-base font-semibold break-all">{value}</span>
        <span className="block text-[13px] text-muted-foreground">{label}</span>
      </span>
      <CopyIconButton value={value} touch />
    </li>
  )
}

/** What went wrong with the certificate and the one fix, for a first attempt or a renewal. */
export function certProblemText(a: Address, now: number): { title: string; body: string; port80: boolean } | undefined {
  const p = a.certificate?.problem
  if (!p) return undefined
  const port80 = p.code === 'port80_unreachable'
  const other = [p.message, p.hint].filter(Boolean).join(' ')
  const notAfter = a.certificate?.notAfter
  if (notAfter && certValid(a.certificate, now)) {
    const date = formatDate(notAfter)
    return { title: t('address.certRenewProblem'), body: port80 ? t('address.certRenewPort80', { date }) : `${t('address.certRunsOut', { date })} ${other}`, port80 }
  }
  return { title: t('address.certProblem'), body: port80 ? t('address.certPort80') : other, port80 }
}

/** A certificate problem under a working address, with Try again. */
export function CertificateNotice({ a, now, busy, onRetry }: { a: Address; now: number; busy: boolean; onRetry: () => void }) {
  const text = certProblemText(a, now)
  if (!text) return null
  return (
    <Notice
      tone={certValid(a.certificate, now) ? 'warning' : 'error'}
      stacked
      title={text.title}
      className="mt-4"
      action={
        <div className="flex flex-col items-start gap-2">
          <div className="flex items-center gap-3">
            <RetryButton busy={busy} onClick={onRetry} />
            {text.port80 && <OutLink href={t('onboarding.check.firewallUrl')}>{t('address.openPort')}</OutLink>}
          </div>
          <TermsLine a={a} />
        </div>
      }
    >
      {text.body}
    </Notice>
  )
}

export function UnreachableNotice({ a }: { a: Address }) {
  if (!a.names.unreachable) return null
  return (
    <Notice tone="warning" stacked title={t('address.unreachable')} className="mt-4">
      {t('address.unreachableKeep')}
    </Notice>
  )
}

/** The top of a working address: Pip, what it is, and Open. */
export function DoneHeader({ pip, spinner, title, sub, open, openLabel, openEnabled }: { pip: PipPose; spinner?: boolean; title: string; sub?: string; open: string; openLabel: string; openEnabled: boolean }) {
  const phone = useIsPhone()
  const button = (
    <Button size={phone ? 'touch' : 'default'} className={cn(phone && 'mt-4 w-full')} disabled={!openEnabled} render={openEnabled ? <a href={open} target="_blank" rel="noreferrer" /> : undefined}>
      {openLabel}
      <ExternalLinkIcon />
    </Button>
  )
  const heading = (
    <div className="min-w-0 flex-1">
      <h2 className={cn('flex items-center gap-2 font-bold', phone ? 'text-[17px] leading-[22px] font-semibold' : 'text-lg leading-6')}>
        {spinner && <Spinner className="size-4" />}
        <span className="min-w-0 break-words">{title}</span>
      </h2>
      {sub && <p className="mt-0.5 text-[13px] text-muted-foreground">{sub}</p>}
    </div>
  )
  if (phone) {
    return (
      <>
        <div className="flex items-center gap-3">
          <Pip pose={pip} size={56} />
          {heading}
        </div>
        {button}
        {openEnabled && <p className="mt-2.5 text-center text-[13px] text-muted-foreground">{t('address.signInAgain')}</p>}
      </>
    )
  }
  return (
    <div className="flex flex-wrap items-center gap-4">
      <Pip pose={pip} size={48} />
      {heading}
      <div className="flex flex-col items-end gap-1.5">
        {button}
        {openEnabled && <span className="text-xs text-muted-foreground">{t('address.signInAgain')}</span>}
      </div>
    </div>
  )
}

/** The warning that replaces the header of a lapsed free address. */
export function LapsedHeader({ a, date, machine, busy, onRefresh }: { a: Address; date: string; machine: string; busy: boolean; onRefresh: () => void }) {
  const phone = useIsPhone()
  return (
    <div className={cn('flex gap-x-4 gap-y-3', phone ? 'flex-col' : 'flex-wrap items-center')} role="status">
      <div className="min-w-0 flex-1">
        <p className="text-[15px] font-semibold text-warning-foreground">{t('address.lapsed', { date })}</p>
        <p className="mt-0.5 text-[13px] text-muted-foreground">{t('address.lapsedBody', { machine })}</p>
        <TermsLine a={a} className="mt-1" />
      </div>
      <Button size={phone ? 'touch' : 'default'} loading={busy} onClick={onRefresh}>
        <RefreshCwIcon />
        {t('address.refresh')}
      </Button>
    </div>
  )
}

/**
 * A working address: its header, notices, each address with Copy, the IP
 * that keeps working, and the footer's change and remove actions.
 */
export function DoneView({ header, notices, rows, ip, footer, phoneFooter }: { header: ReactNode; notices?: ReactNode; rows: AddressRowData[]; ip?: string; footer: ReactNode; phoneFooter: ReactNode }) {
  const phone = useIsPhone()
  if (phone) {
    return (
      <div className="flex flex-col gap-5 pt-2 pb-6">
        <Card className="p-4">
          {header}
          {notices}
        </Card>
        <Group label={t('address.addresses')}>
          {rows.map((r) => (
            <PhoneCopyRow key={r.id} value={r.value} label={r.status ? `${r.label}${t('common.dot')}${r.status}` : r.label} />
          ))}
        </Group>
        <Group>{phoneFooter}</Group>
      </div>
    )
  }
  return (
    <Card>
      {header}
      {notices}
      <ul className="mt-4 overflow-hidden rounded-2xl border border-border">
        {rows.map((r) => (
          <li key={r.id} className="flex items-center gap-3 border-b border-border px-3 py-2.5 last:border-b-0">
            <span className="min-w-0 flex-1">
              <span className="block text-[11px] text-muted-foreground">{r.label}</span>
              <span className="block truncate text-[15px] font-semibold">{r.value}</span>
            </span>
            {r.status && <span className="text-xs text-muted-foreground">{r.status}</span>}
            <CopyButton text={r.value} aria-label={t('address.copyValue', { value: r.value })} />
          </li>
        ))}
      </ul>
      {ip && <p className="mt-4 text-xs text-muted-foreground">{t('address.ipWorks', { ip })}</p>}
      <div className="mt-4 flex items-center gap-2 border-t border-border pt-4">{footer}</div>
    </Card>
  )
}

/** A row in a phone list that does something: a plain one with a chevron, or a red one. */
export function PhoneAction({ label, onClick, danger }: { label: string; onClick: () => void; danger?: boolean }) {
  return (
    <li>
      <button type="button" onClick={onClick} className="flex min-h-14 w-full items-center gap-3 px-4 py-2 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset">
        <span className={cn('min-w-0 flex-1 text-base', danger && 'text-destructive-foreground')}>{label}</span>
        {!danger && <ChevronRightIcon className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />}
      </button>
    </li>
  )
}

/** Asks before an address stops working; a bottom sheet on phones. */
export function ConfirmDialog({ open, onOpenChange, title, body, confirm, busy, onConfirm }: { open: boolean; onOpenChange: (open: boolean) => void; title: string; body: string; confirm: string; busy: boolean; onConfirm: () => void }) {
  const phone = useIsPhone()
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[360px] max-sm:min-h-[400px]" showCloseButton={phone}>
        <div className="flex flex-1 flex-col px-5 pt-5 max-sm:pt-4">
          <DialogHeading title={title}>{body}</DialogHeading>
          <div className="mt-auto">
            <DialogButtons>
              {!phone && (
                <Button variant="ghost" onClick={() => onOpenChange(false)}>
                  {t('common.cancel')}
                </Button>
              )}
              <Button variant="destructive" size={phone ? 'touch' : 'default'} loading={busy} onClick={onConfirm}>
                {confirm}
              </Button>
            </DialogButtons>
          </div>
        </div>
      </DialogPopup>
    </Dialog>
  )
}
