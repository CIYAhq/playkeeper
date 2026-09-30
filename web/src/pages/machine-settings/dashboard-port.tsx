import { useEffect, useId, useState, type ReactNode } from 'react'
import { ExternalLinkIcon } from 'lucide-react'
import { ApiError, get, post, put } from '@/api/client'
import type { DashboardPortView, OutsideChange, PagePort } from '@/api/types'
import { errorText, useWorkspace } from '@/api/workspace'
import { Card, CardTitle, CopyButton, SectionLabel } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { can } from '@/lib/access'
import { portProblem } from '@/lib/server-page'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { Group } from '../more'
import { ErrorLine } from '../two-factor'
import { ConfirmDialog } from './parts'

// 0.4.11: Serve the dashboard on the standard HTTPS port (443), on the
// dashboard's own machine. The agent opens port 443 for the panel, which
// stays unprivileged; the address loses its port once a browser from
// outside the machine reaches it there, and the panel's port keeps
// answering and sends browsers on.

const api443 = '/api/dashboard-port'
/** Fast while port 443 is on its way, so the switch shows it arriving. */
const busyPollMs = 3000
const idlePollMs = 30_000
/** How long a browser's check of port 443 may take. */
const reachTimeoutMs = 8000

type Reach = 'idle' | 'checking' | 'ok' | 'failed'

/**
 * Checks from this browser that port 443 answers at the machine's name,
 * while it listens there and nobody from outside has reached it yet: the
 * request is what the panel counts. It asks from the panel's own port, so
 * the answer is opaque; only arriving matters.
 */
function useReachCheck(v: DashboardPortView | undefined, refresh: () => Promise<void>): { reach: Reach; again: () => void } {
  const [reach, setReach] = useState<Reach>('idle')
  const [round, setRound] = useState(0)
  const url = v?.url
  const due = !!v && v.on && v.state === 'open' && !v.reached && !!v.serving && !!url && window.location.origin !== url
  useEffect(() => {
    if (!due || !url) return
    let cancelled = false
    const abort = new AbortController()
    const timer = window.setTimeout(() => abort.abort(), reachTimeoutMs)
    setReach('checking')
    fetch(`${url}/api/public/reach`, { mode: 'no-cors', cache: 'no-store', credentials: 'omit', signal: abort.signal }).then(
      () => {
        if (cancelled) return
        // The panel tells the agent of the visit a moment after it arrives:
        // until the dashboard says whether it counted, this is still a check.
        window.setTimeout(() => {
          void refresh().then(() => {
            if (!cancelled) setReach('ok')
          })
        }, 1500)
      },
      () => {
        if (!cancelled) setReach('failed')
      },
    )
    return () => {
      cancelled = true
      abort.abort()
      window.clearTimeout(timer)
    }
  }, [due, url, round, refresh])
  return { reach: due ? reach : 'idle', again: () => setRound((r) => r + 1) }
}

/** The switch and what it needs outside Playkeeper, on the dashboard's own machine. */
export function DashboardPortSettings() {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const switchId = useId()
  const [pending, setPending] = useState<boolean>()
  const [problem, setProblem] = useState<string>()
  const [confirmOff, setConfirmOff] = useState(false)
  const [retrying, setRetrying] = useState(false)
  const [fast, setFast] = useState(false)
  const poll = usePoll(() => get<DashboardPortView>(api443), fast ? busyPollMs : idlePollMs)
  const v = poll.data
  const busy = !!v && v.on && (v.state === 'waiting' || (v.state === 'open' && !v.reached))
  useEffect(() => setFast(busy), [busy])
  const { reach, again } = useReachCheck(v, poll.refresh)
  if (!v) return null

  const on = pending ?? v.on
  const locked = pending !== undefined ? t('reason.saving') : !can(ws.me, 'machine.manage') ? t('dashboardPort.notAllowed') : undefined

  async function save(next: boolean) {
    setPending(next)
    setProblem(undefined)
    try {
      await put(api443, { on: next })
      toastManager.add({ title: next ? t('dashboardPort.onToast') : t('dashboardPort.offToast', { port: v?.panelPort ?? 8443 }), type: 'success' })
      // The keeper takes the port a moment after the switch.
      window.setTimeout(() => void poll.refresh(), 1500)
      await poll.refresh()
    } catch (e) {
      setProblem(refusalText(e, v?.old))
    } finally {
      setPending(undefined)
      setConfirmOff(false)
    }
  }
  function toggle(next: boolean) {
    // Links without a port that went out stop working once it's off.
    if (!next && v?.reached) setConfirmOff(true)
    else void save(next)
  }
  async function retry() {
    setRetrying(true)
    try {
      await post(`${api443}/retry`, {})
      await new Promise((resolve) => window.setTimeout(resolve, 1500))
      await poll.refresh()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setRetrying(false)
    }
  }

  const row = (
    <div className="flex min-h-12 items-center gap-4 py-2">
      <label htmlFor={switchId} className="min-w-0 flex-1">
        <span className="block text-[13px] leading-5 font-semibold">{t('dashboardPort.switch')}</span>
        <span className="block text-xs text-muted-foreground">
          <StateLine v={{ ...v, on }} reach={reach} />
        </span>
      </label>
      <Switch id={switchId} checked={on} disabled={!!locked} title={locked} onCheckedChange={toggle} />
    </div>
  )
  const actions = on && <Actions v={v} reach={reach} retrying={retrying} onRetry={() => void retry()} onCheck={again} phone={phone} />
  const outside = on && v.url && <Outside v={v} phone={phone} />
  const confirm = (
    <ConfirmDialog
      open={confirmOff}
      onOpenChange={setConfirmOff}
      title={t('dashboardPort.offTitle')}
      body={t('dashboardPort.offBody', { url: v.url ?? '', old: v.old ?? '' })}
      confirm={t('dashboardPort.offConfirm')}
      busy={pending === false}
      onConfirm={() => void save(false)}
    />
  )

  if (phone) {
    return (
      <div className="mt-5 flex flex-col gap-5">
        <Group label={t('dashboardPort.title')}>
          <li className="flex flex-col px-4 pb-3">
            {row}
            {actions}
            <ErrorLine text={problem} className="mt-1" />
          </li>
        </Group>
        {outside}
        {confirm}
      </div>
    )
  }
  return (
    <Card className="mt-4">
      <CardTitle>{t('dashboardPort.title')}</CardTitle>
      {row}
      {actions}
      <ErrorLine text={problem} className="mt-1" />
      {outside}
      {confirm}
    </Card>
  )
}

/** A refusal to turn it on: what has port 443, from the refusal's params when it names them. */
function refusalText(e: unknown, old: string | undefined): string {
  if (!(e instanceof ApiError) || e.code !== 'port_in_use') return errorText(e)
  const state = String(e.params?.state ?? '')
  const p: PagePort = { port: 443, state: state === 'claimed' || state === 'denied' ? state : 'busy', holder: typeof e.params?.holder === 'string' && e.params.holder ? e.params.holder : undefined }
  return old ? `${portProblem(p)} ${t('dashboardPort.staysAt', { old })}` : portProblem(p)
}

/** The switch's line: what turning it on does, or where port 443 stands. */
function StateLine({ v, reach }: { v: DashboardPortView; reach: Reach }) {
  if (!v.on) return <>{v.url ? t('dashboardPort.offHint', { url: v.url, old: v.old ?? '' }) : t('dashboardPort.offHintNoName', { port: v.panelPort })}</>
  switch (v.state) {
    case 'off':
    case 'no_address':
      return <>{t('dashboardPort.noAddress')}</>
    case 'waiting':
      return <>{t('dashboardPort.waiting')}</>
    case 'busy':
    case 'claimed':
    case 'denied':
      return (
        <>
          {portProblem({ port: 443, state: v.state, holder: v.holder })} {v.old && t('dashboardPort.staysAt', { old: v.old })}
        </>
      )
    case 'open':
      if (v.reached) return <>{t('dashboardPort.live', { url: v.url ?? '', old: v.old ?? '' })}</>
      // Until the panel answers port 443, the check waits for it.
      if (!v.serving || reach === 'checking') return <>{t('dashboardPort.checking', { url: v.url ?? '' })}</>
      if (reach === 'ok') return <>{t('dashboardPort.inside')}</>
      return <>{t('dashboardPort.checkHint', { url: v.url ?? '' })}</>
    default: {
      const never: never = v.state
      return never
    }
  }
}

/** What the owner can do about where port 443 stands: try it again, or open the address once. */
function Actions({ v, reach, retrying, onRetry, onCheck, phone }: { v: DashboardPortView; reach: Reach; retrying: boolean; onRetry: () => void; onCheck: () => void; phone: boolean }) {
  const ws = useWorkspace()
  if ((v.state === 'busy' || v.state === 'claimed') && can(ws.me, 'machine.manage')) {
    return (
      <div className="pb-2">
        <Button variant="outline" size={phone ? 'touch' : 'sm'} className={cn(phone && 'w-full')} loading={retrying} onClick={onRetry}>
          {t('publicPage.retry')}
        </Button>
      </div>
    )
  }
  if (v.state !== 'open' || v.reached || !v.url || !v.serving) return null
  return (
    <div className={cn('flex flex-wrap items-center gap-2 pb-2', phone && 'flex-col items-stretch')}>
      <Button variant="outline" size={phone ? 'touch' : 'sm'} render={<a href={v.url} target="_blank" rel="noreferrer" />}>
        {t('dashboardPort.open', { url: phone ? v.url.replace(/^https:\/\//, '') : v.url })}
        <ExternalLinkIcon />
      </Button>
      {reach === 'failed' && (
        <Button variant="ghost" size={phone ? 'touch' : 'sm'} onClick={onCheck}>
          {t('dashboardPort.checkAgain')}
        </Button>
      )}
    </div>
  )
}

/** The places outside Playkeeper that keep the old address, with the change each needs. */
function Outside({ v, phone }: { v: DashboardPortView; phone: boolean }) {
  const items: { key: string; title: string; body: ReactNode; copy?: string; done?: boolean }[] = [
    { key: 'firewall', title: t('dashboardPort.firewall'), body: t('dashboardPort.firewallBody', { port: v.panelPort }) },
    ...v.outside.map((c) => outsideItem(c)),
    { key: 'links', title: t('dashboardPort.links'), body: t('dashboardPort.linksBody', { port: v.panelPort }) },
  ]
  const list = items.map((it) => (
    <li key={it.key} className={cn('flex flex-col gap-1.5 py-3', phone ? 'px-4' : 'border-b border-border last:border-b-0')}>
      <p className="flex items-center gap-2 text-[13px] font-semibold">
        {it.title}
        {it.done !== undefined && <span className={cn('text-xs font-medium', it.done ? 'text-success-foreground max-sm:text-success-strong' : 'text-warning-foreground')}>{it.done ? t('dashboardPort.done') : t('dashboardPort.toDo')}</span>}
      </p>
      <p className="text-xs leading-[18px] text-muted-foreground">{it.body}</p>
      {it.copy && (
        <div className="flex min-w-0 items-center gap-2">
          <code className="min-w-0 flex-1 truncate rounded bg-muted px-1.5 py-1 text-[11px]">{it.copy}</code>
          <CopyButton text={it.copy} aria-label={t('address.copyValue', { value: it.copy })} />
        </div>
      )}
    </li>
  ))
  if (phone) return <Group label={t('dashboardPort.outside')}>{list}</Group>
  return (
    <div className="mt-4 border-t border-border pt-4">
      <SectionLabel>{t('dashboardPort.outside')}</SectionLabel>
      <ul className="mt-1">{list}</ul>
    </div>
  )
}

function outsideItem(c: OutsideChange): { key: string; title: string; body: ReactNode; copy?: string; done?: boolean } {
  switch (c.kind) {
    case 'whop_signin':
      return {
        key: c.kind,
        title: t('dashboardPort.whopSignIn'),
        body: c.done ? t('dashboardPort.whopSignInDone') : t('dashboardPort.whopSignInTodo', { app: c.app ?? '', keep: c.keep ?? '' }),
        copy: c.done ? undefined : c.add,
        done: c.done,
      }
    case 'whop_app_webhook':
      return { key: c.kind, title: t('dashboardPort.whopAppWebhook'), body: t('dashboardPort.whopAppWebhookBody', { app: c.app ?? '', keep: c.keep ?? '' }), copy: c.add }
    case 'whop_webhook':
      return { key: c.kind, title: t('dashboardPort.whopWebhook'), body: c.done ? t('dashboardPort.whopWebhookDone', { url: c.add }) : t('dashboardPort.whopWebhookTodo', { url: c.add }) }
    case 'mcp':
      return { key: c.kind, title: t('dashboardPort.mcp'), body: t('dashboardPort.mcpBody', { keep: c.keep ?? '' }), copy: c.add }
    default: {
      const never: never = c.kind
      return never
    }
  }
}
