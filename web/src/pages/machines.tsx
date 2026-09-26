import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { ChevronLeftIcon, ChevronRightIcon, CircleArrowUpIcon, ExternalLinkIcon, PlusIcon, RefreshCwIcon, ServerIcon, Trash2Icon } from 'lucide-react'
import { ApiError, del, get, post } from '@/api/client'
import type { DialAddress, JoinCommand, MachineEvent, MachineLinkInfo, MachineView } from '@/api/types'
import { errorText, machineApi, useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { Card, CardHint, CardTitle, CopyButton, Spinner, useNow } from '@/components/app/bits'
import { ChoiceSelect, Segmented, useIsPhone } from '@/components/app/controls'
import { lineWidth, ListSkeleton, LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { can } from '@/lib/access'
import { formatBytes, formatClock, formatDate, formatList, formatWhen, relativeTime } from '@/lib/format'
import { agentSilent, byMachine, countdown, groupFingerprint, machineEventText, machineLabel, machineState, olderMachine, problemText, systemLine, type MachineTone } from '@/lib/machines'
import { presenceProps, useListPresence } from '@/lib/presence'
import { linkProps, navigate } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

type Dial = DialAddress['kind']
type Form = 'install' | 'join'

/**
 * The join code this tab made, so leaving Settings and coming back shows the
 * same command while it works. It lives only in memory: the dashboard keeps
 * just a hash of it and shows it this once.
 */
let made: { cmd: JoinCommand; name: string; dial: Dial } | undefined

/** Only for tests: forget the code this tab made. */
export function forgetJoinCode() {
  made = undefined
}

function StateDot({ tone }: { tone: MachineTone }) {
  return <span key={tone} className={cn('size-1.5 shrink-0 animate-fade rounded-full', tone === 'good' ? 'bg-success' : tone === 'warn' ? 'bg-warning' : 'bg-muted-foreground')} aria-hidden="true" />
}

function StateLabel({ tone, label }: { tone: MachineTone; label: string }) {
  return (
    <span key={label} className={cn('flex animate-fade items-center gap-1.5 text-xs font-medium', tone === 'good' ? 'text-success-strong' : tone === 'warn' ? 'text-warning-strong' : 'text-muted-foreground')}>
      <StateDot tone={tone} />
      {label}
    </span>
  )
}

/** Settings › Machines: every machine, and connecting another. */
export function MachinesSection() {
  const ws = useWorkspace()
  const manage = can(ws.me, 'machine.manage')
  const [fast, setFast] = useState(false)
  const link = usePoll(() => get<MachineLinkInfo>('/api/machines/link'), fast ? 3000 : 15_000)
  return (
    <>
      <MachineList fingerprint={link.data?.fingerprint} />
      {manage && link.data?.available && <ConnectCard link={link.data} refresh={link.refresh} onWaiting={setFast} />}
      {manage && !link.data && !link.error && <ConnectSkeleton />}
      {manage && link.error && <p className="text-[13px] text-destructive-foreground">{errorText(link.error)}</p>}
    </>
  )
}

const machineKey = (m: MachineView) => m.id

function MachineList({ fingerprint }: { fingerprint?: string }) {
  const ws = useWorkspace()
  const rows = useListPresence(ws.machines.length ? ws.machines : undefined, machineKey)
  const serversOn = new Map(byMachine(ws.servers ?? [], ws.machines).map((g) => [g.machine.id, g.servers]))
  return (
    <Card aria-labelledby="machines-title">
      <CardTitle id="machines-title">{t('machines.title')}</CardTitle>
      {!ws.machines.length && <ListSkeleton rows={1} rowClassName="flex items-center gap-3 py-2.5" face="size-[18px] rounded" trailing={<Skeleton className="h-3 w-14" />} className="mt-3 flex flex-col" />}
      <ul className="mt-3 flex flex-col empty:hidden">
        {rows.map(({ key, item: m, state: presence }) => {
          const servers = serversOn.get(m.id) ?? []
          const state = machineState(m, ws)
          const local = m.kind === 'local'
          const body = (
            <>
              <ServerIcon className="mt-0.5 size-[18px] shrink-0 text-muted-foreground" aria-hidden="true" />
              <span className="min-w-0 flex-1">
                <span className="flex flex-wrap items-baseline gap-x-2">
                  <span className="text-sm font-semibold">{machineLabel(m)}</span>
                  {local && <span className="text-xs text-muted-foreground">{t('machines.here')}</span>}
                </span>
                <span className="block text-xs text-muted-foreground">{systemLine(m.live, t('machines.serverCount', { count: servers.length }))}</span>
                {local && fingerprint && <span className="block text-xs text-muted-foreground">{t('machines.fingerprint', { fingerprint: groupFingerprint(fingerprint) })}</span>}
              </span>
              <StateLabel tone={state.tone} label={state.label} />
              {!local && <ChevronRightIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />}
            </>
          )
          return (
            <li key={key} {...presenceProps(presence)} className="border-t border-border first:border-t-0">
              {local ? (
                <div className="flex items-center gap-3 py-2.5">{body}</div>
              ) : (
                <a {...linkProps({ name: 'machine-details', id: m.id })} className="-mx-2 flex items-center gap-3 rounded-lg px-2 py-2.5 outline-none hover:bg-accent/40 focus-visible:ring-2 focus-visible:ring-ring">
                  {body}
                </a>
              )}
            </li>
          )
        })}
      </ul>
    </Card>
  )
}

function defaultDial(addresses: DialAddress[]): Dial {
  const name = addresses.find((a) => a.kind === 'name')
  if (name && !name.proxied) return 'name'
  return addresses.find((a) => a.kind === 'ip')?.kind ?? addresses[0]?.kind ?? 'ip'
}

/** The connect-a-machine card: the four steps, then what became of the code. */
function ConnectCard({ link, refresh, onWaiting }: { link: MachineLinkInfo; refresh: () => Promise<void>; onWaiting: (waiting: boolean) => void }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const now = useNow(1000)
  const [cmd, setCmd] = useState<JoinCommand | undefined>(made?.cmd)
  const [name, setName] = useState(made?.name ?? '')
  const [dial, setDial] = useState<Dial>(made?.dial ?? defaultDial(link.addresses))
  const [form, setForm] = useState<Form>('install')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const [cancelled, setCancelled] = useState(false)
  const [pausedUntil, setPausedUntil] = useState<number>()
  const tried = useRef(false)

  useEffect(() => {
    setPausedUntil(link.joinPausedSeconds ? Date.now() + link.joinPausedSeconds * 1000 : undefined)
  }, [link])

  const code = cmd ? link.codes?.find((c) => c.id === cmd.id) : undefined
  const joined = code?.state === 'used' ? code.machineId : undefined
  const ranOut = !!cmd && !joined && (code?.state === 'expired' || new Date(cmd.expiresAt).getTime() <= now)
  const pausedFor = pausedUntil ? Math.max(0, (pausedUntil - now) / 1000) : 0
  const paused = pausedFor > 0
  const waiting = !!cmd && !joined && !ranOut && !paused

  const make = useCallback(
    async (n: string, d: Dial) => {
      setBusy(true)
      setError(undefined)
      const old = made?.cmd
      try {
        const next = await post<JoinCommand>('/api/join-codes', { name: n.trim(), dial: d })
        made = { cmd: next, name: n, dial: d }
        setCmd(next)
        setCancelled(false)
        if (old && old.id !== next.id) void del(`/api/join-codes/${old.id}`).catch(() => undefined)
      } catch (e) {
        if (!(e instanceof ApiError && e.status === 429)) setError(errorText(e))
      } finally {
        setBusy(false)
        void refresh()
      }
    },
    [refresh],
  )

  const dialable = link.addresses.length > 0
  useEffect(() => {
    if (tried.current || cmd || paused || !dialable) return
    tried.current = true
    void make(name, dial)
  }, [cmd, paused, dialable, make, name, dial])

  useEffect(() => {
    if (!cmd || name === made?.name || joined) return
    const id = window.setTimeout(() => void make(name, dial), 700)
    return () => window.clearTimeout(id)
  }, [name, cmd, dial, joined, make])

  useEffect(() => {
    onWaiting(waiting)
  }, [waiting, onWaiting])

  const { refresh: refreshWorkspace } = ws
  useEffect(() => {
    if (joined) void refreshWorkspace()
  }, [joined, refreshWorkspace])

  const joinedRef = useRef(joined)
  joinedRef.current = joined
  useEffect(
    () => () => {
      onWaiting(false)
      if (joinedRef.current) made = undefined
    },
    [onWaiting],
  )

  async function cancel() {
    if (!cmd) return
    try {
      await del(`/api/join-codes/${cmd.id}`)
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
      return
    }
    made = undefined
    setCmd(undefined)
    setCancelled(true)
    toastManager.add({ title: t('machines.connect.cancelled'), type: 'success' })
    void refresh()
  }

  if (joined) return <ConnectedCard id={joined} fallbackName={code?.name ?? ''} />
  if (paused) {
    return (
      <Card aria-labelledby="connect-paused" className="animate-fade">
        <h2 id="connect-paused" className="text-[15px] font-semibold text-warning-foreground">
          {t('machines.paused.title', { minutes: t('machines.minutes', { count: Math.max(1, Math.ceil(pausedFor / 60)) }) })}
        </h2>
        <p className="mt-1 text-[13px] text-muted-foreground">{t('machines.paused.body')}</p>
        <div className="mt-4 flex items-center gap-2">
          <Button variant="outline" size="sm" disabledReason={t('machines.paused.reason', { time: countdown(pausedFor) })}>
            <RefreshCwIcon />
            {t('machines.connect.makeCode')}
          </Button>
          <span className="text-xs text-muted-foreground tabular-nums">{t('machines.paused.in', { time: countdown(pausedFor) })}</span>
        </div>
      </Card>
    )
  }
  if (ranOut) {
    return (
      <Card aria-labelledby="connect-ran-out" className="animate-fade">
        <h2 id="connect-ran-out" className="text-[15px] font-semibold">
          {t('machines.ranOut.title')}
        </h2>
        <p className="mt-1 text-[13px] text-muted-foreground">{t('machines.ranOut.body')}</p>
        <Button variant="outline" size="sm" className="mt-4 self-start" loading={busy} onClick={() => void make(name, dial)}>
          <RefreshCwIcon />
          {t('machines.connect.makeCode')}
        </Button>
      </Card>
    )
  }

  const named = link.addresses.find((a) => a.kind === 'name')
  const left = cmd ? (new Date(cmd.expiresAt).getTime() - now) / 1000 : 0
  const lines = cmd ? (form === 'install' ? cmd.installLines : cmd.joinLines) : []
  const shownName = name.trim() || (made?.name ?? '')
  return (
    <Card aria-labelledby="connect-title" className="animate-fade">
      <CardTitle id="connect-title">{t('machines.connect.title')}</CardTitle>
      <CardHint>{t('machines.connect.lead')}</CardHint>
      <ol className="mt-4 flex flex-col gap-5">
        <Step n={1} title={t('machines.connect.step1')}>
          <p className="text-[13px] text-muted-foreground">{t('machines.connect.minimum', { cores: link.minimum.cores, memory: link.minimum.memoryGB, disk: link.minimum.freeDiskGB })}</p>
          <a href={link.sizingUrl} target="_blank" rel="noreferrer" className="mt-1 inline-flex items-center gap-1 text-[13px] font-medium text-primary hover:underline">
            {t('machines.connect.sizing')}
            <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
          </a>
        </Step>
        <Step n={2} title={t('machines.connect.step2')}>
          <div className="flex items-center gap-3">
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('machines.connect.namePlaceholder')} aria-label={t('machines.connect.name')} maxLength={40} autoComplete="off" spellCheck={false} className="w-[170px]" />
            <span className="text-xs text-muted-foreground">{t('machines.connect.optional')}</span>
          </div>
        </Step>
        <Step n={3} title={t('machines.connect.step3')}>
          <div className="flex flex-wrap items-center gap-3">
            <Segmented
              value={form}
              onChange={setForm}
              label={t('machines.connect.form')}
              options={[
                { value: 'install', label: t('machines.connect.formNew') },
                { value: 'join', label: t('machines.connect.formExisting') },
              ]}
            />
            {dialable && (
              <label className="ml-auto flex items-center gap-2 text-xs text-muted-foreground">
                {t('machines.connect.dials')}
                <ChoiceSelect
                  value={dial}
                  onChange={(d) => {
                    setDial(d)
                    if (cmd || cancelled) void make(name, d)
                  }}
                  label={t('machines.connect.dials')}
                  options={link.addresses.map((a) => ({ value: a.kind, label: a.address }))}
                  className="min-w-40 text-[13px] text-foreground"
                />
              </label>
            )}
          </div>
          {named?.proxied && (
            <div className="mt-3">
              <p className="text-[13px] font-semibold">{t('machines.proxy.title', { host: named.address.replace(/:\d+$/, '') })}</p>
              <p className="text-xs text-muted-foreground">{t('machines.proxy.body')}</p>
            </div>
          )}
          {!dialable ? (
            <p className="mt-3 text-[13px] text-muted-foreground">{t('machines.connect.noAddress')}</p>
          ) : error ? (
            <p className="mt-3 text-[13px] text-destructive-foreground" role="alert">
              {error}
            </p>
          ) : cmd ? (
            <>
              <div className="relative mt-3 rounded-xl bg-console px-4 py-3.5" role="group" aria-label={t('machines.connect.command')}>
                <pre tabIndex={0} className={cn('overflow-x-auto rounded-sm font-mono text-xs leading-[1.7] text-white/90 outline-none focus-visible:ring-2 focus-visible:ring-ring', !phone && 'pr-20')}>
                  {lines.map((l, i) => (
                    <span key={i} className="block">
                      {l}
                    </span>
                  ))}
                </pre>
                {!phone && <CopyButton text={form === 'install' ? cmd.install : cmd.join} toast={t('machines.connect.copied')} className="absolute top-3 right-3 bg-white" />}
              </div>
              {phone && <CopyButton text={form === 'install' ? cmd.install : cmd.join} toast={t('machines.connect.copied')} size="touch" className="mt-2 w-full" />}
              <div className="mt-2 flex flex-wrap items-center justify-between gap-2 text-xs">
                <p>
                  <strong className="font-semibold">{t('machines.connect.code', { code: cmd.code })}</strong> <span className="text-muted-foreground tabular-nums">{t('machines.connect.codeLeft', { time: countdown(left) })}</span>
                </p>
                <button type="button" onClick={() => void cancel()} className="rounded font-medium text-primary outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
                  {t('machines.connect.cancel')}
                </button>
              </div>
              <p className="mt-2 text-xs text-muted-foreground">{t('machines.connect.fingerprintNote')}</p>
            </>
          ) : busy ? (
            <>
              <LoadingLabel />
              <Skeleton className="mt-3 h-28 w-full rounded-xl" />
            </>
          ) : (
            <Button variant="outline" size="sm" className="mt-3" onClick={() => void make(name, dial)}>
              <RefreshCwIcon />
              {t('machines.connect.makeCode')}
            </Button>
          )}
        </Step>
        <Step n={4} title={t('machines.connect.step4')}>
          {cmd && !error && (
            <p className={cn('flex items-center gap-2 text-[13px] text-muted-foreground transition-opacity duration-(--motion-standard) ease-standard', !waiting && 'opacity-60')}>
              {waiting && <Spinner />}
              {shownName ? t('machines.connect.waitingFor', { name: shownName }) : t('machines.connect.waitingAny')}
            </p>
          )}
        </Step>
      </ol>
    </Card>
  )
}

function Step({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <li className="grid grid-cols-[20px_minmax(0,1fr)] gap-x-3">
      <span className="flex size-5 items-center justify-center rounded-full border border-border bg-muted text-[11px] font-semibold text-muted-foreground" aria-hidden="true">
        {n}
      </span>
      <h3 className="text-[13px] leading-5 font-semibold">{title}</h3>
      <div className="col-start-2 mt-1.5">{children}</div>
    </li>
  )
}

function ConnectedCard({ id, fallbackName }: { id: string; fallbackName: string }) {
  const ws = useWorkspace()
  const m = ws.machines.find((x) => x.id === id)
  const name = machineLabel(m) || fallbackName
  const version = m?.link?.version ?? m?.live?.agentVersion
  return (
    <Card aria-labelledby="connect-done" className="animate-fade">
      <div className="flex items-center gap-3">
        <Pip pose="cheer" size={52} />
        <div className="min-w-0">
          <h2 id="connect-done" className="text-[15px] font-semibold">
            {t('machines.connected.title', { name })}
          </h2>
          <p className="text-xs text-muted-foreground">{systemLine(m?.live, version ? t('machines.version', { version }) : '')}</p>
        </div>
      </div>
      <p className="mt-3 text-xs text-muted-foreground">{t('machines.connected.body')}</p>
      <div className="mt-4 flex flex-wrap gap-2">
        <Button size="sm" render={<a {...linkProps({ name: 'new-server', machine: id })} />}>
          <PlusIcon />
          {t('machines.connected.newServer', { name })}
        </Button>
        <Button variant="outline" size="sm" render={<a {...linkProps({ name: 'machine-details', id })} />}>
          {t('machines.details')}
        </Button>
      </div>
    </Card>
  )
}

/** "12 Sep at 16:40 by siya, from 203.0.113.24". */
function addedText(m: MachineView): string {
  if (!m.joinedAt) return t('common.none')
  const when = t('machines.fact.when', { date: formatDate(m.joinedAt), time: formatClock(m.joinedAt) })
  if (m.addedBy && m.joinedFrom) return t('machines.fact.addedFull', { when, actor: m.addedBy, address: m.joinedFrom })
  if (m.addedBy) return t('machines.fact.addedBy', { when, actor: m.addedBy })
  if (m.joinedFrom) return t('machines.fact.addedFrom', { when, address: m.joinedFrom })
  return when
}

/** Settings › Machines › a joined machine: how it is, its facts, recent events and Remove. */
export function MachineDetailsSection({ id }: { id: string }) {
  const ws = useWorkspace()
  const m = ws.machines.find((x) => x.id === id)
  const events = usePoll(() => get<MachineEvent[]>(machineApi(id, '/events')), 15_000, id)
  const eventRows = useListPresence(events.data?.slice(0, 8), eventKey)
  const now = useNow(30_000)
  const [removing, setRemoving] = useState(false)
  const [updating, setUpdating] = useState(false)
  const phone = useIsPhone()
  const local = m?.kind === 'local'
  useEffect(() => {
    if (local) navigate({ name: 'machine', id }, true)
  }, [local, id])

  const back = !phone && (
    <a {...linkProps({ name: 'machines' })} className="inline-flex items-center gap-0.5 self-start rounded text-[13px] font-medium text-primary outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
      <ChevronLeftIcon className="size-4" aria-hidden="true" />
      {t('machines.back')}
    </a>
  )
  if (!m || local) {
    return (
      <>
        {back}
        {ws.machines.length ? <p className="text-sm text-muted-foreground">{t('machine.notFound')}</p> : <DetailsSkeleton />}
      </>
    )
  }

  const name = machineLabel(m)
  const manage = can(ws.me, 'machine.manage')
  const link = m.link
  const connected = link?.state === 'connected'
  const state = machineState(m, ws)
  const servers = (ws.servers ?? []).filter((s) => s.machineId === m.id)
  const problem = link?.problems[0]
  const problemCopy = problem ? problemText(problem, now) : agentSilent(m) ? { title: t('machines.problem.agentDown', { name }), hint: t('machines.problem.agentDownHint', { name }) } : undefined
  const version = link?.version ?? m.live?.agentVersion
  let status: string
  if (connected && link?.connectedAt) {
    status = t('machines.connectedSince', { time: formatWhen(link.connectedAt, new Date(now)) })
    if (link.rttMs !== undefined) status += `${t('common.dot')}${t('machines.roundTrip', { ms: link.rttMs })}`
  } else if (link?.lastSeen) status = t('machines.lastSeen', { time: relativeTime(link.lastSeen, now) })
  else status = t('machines.neverConnected')

  async function update() {
    setUpdating(true)
    try {
      await post(machineApi(id, '/update/apply'), { version: ws.me.version })
      toastManager.add({ title: t('machines.updateStarted', { name }), type: 'success' })
      void events.refresh()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setUpdating(false)
    }
  }

  const list = events.data ?? []
  // On a phone the machine's name is the page's title; on desktop it sits under Settings.
  const Title = phone ? 'h1' : 'h2'
  return (
    <>
      {back}
      <div className="-mt-2 flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-3">
          <ServerIcon className="mt-1.5 size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
          <div className="min-w-0">
            <Title className="truncate text-xl leading-7 font-bold tracking-[-0.01em]">{name}</Title>
            <p className="flex items-center gap-1.5 text-[13px] text-muted-foreground">
              <StateDot tone={state.tone} />
              {status}
            </p>
          </div>
        </div>
        {manage &&
          (connected ? (
            <Button variant="outline" render={<a {...linkProps({ name: 'new-server', machine: m.id })} />}>
              <PlusIcon />
              {t('machines.newServerHere')}
            </Button>
          ) : (
            <Button variant="outline" disabledReason={t('machines.away.pill', { name })}>
              <PlusIcon />
              {t('machines.newServerHere')}
            </Button>
          ))}
      </div>
      {problemCopy && (
        <div className="-mt-1 flex flex-wrap items-center gap-x-4 gap-y-2 max-sm:flex-col max-sm:items-start" role="status">
          <div className="min-w-0 flex-1">
            <p className="text-[13px] font-semibold text-warning-foreground">{problemCopy.title}</p>
            {problemCopy.hint && <p className="text-xs text-muted-foreground">{problemCopy.hint}</p>}
          </div>
          {manage && problem && olderMachine(problem) && connected && (
            <Button variant="outline" size="sm" loading={updating || !!m.live?.updateInstalling} onClick={() => void update()}>
              <CircleArrowUpIcon />
              {t('machines.update', { name })}
            </Button>
          )}
        </div>
      )}
      <Card aria-label={name} className="py-1">
        <dl className="flex flex-col">
          <Fact label={t('machines.fact.fingerprint')}>
            <span className="font-semibold tracking-wide">{link?.fingerprint ? groupFingerprint(link.fingerprint) : t('common.none')}</span>
            <span className="block text-xs text-muted-foreground">{t('machines.fact.compare', { name })}</span>
          </Fact>
          <Fact label={t('machines.fact.added')}>{addedText(m)}</Fact>
          <Fact label={t('machines.fact.version')}>{version ?? t('common.none')}</Fact>
          <Fact label={t('machines.fact.dials')}>{m.dials || t('common.none')}</Fact>
          <Fact label={t('machines.fact.system')}>{systemLine(m.live, servers.length ? t('machines.fact.runs', { servers: formatList(servers.map((s) => s.name)) }) : '') || t('common.none')}</Fact>
          <Fact label={t('machine.disk')}>
            {m.live ? (
              <a {...linkProps({ name: 'machine', id: m.id, sub: 'disk' })} className="inline-flex items-center gap-0.5 rounded font-medium text-primary outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
                {t('home.diskFree', { free: formatBytes(m.live.diskFreeBytes) })}
                <ChevronRightIcon className="size-3.5" aria-hidden="true" />
              </a>
            ) : (
              t('common.none')
            )}
          </Fact>
        </dl>
      </Card>
      <div className="grid gap-4 lg:grid-cols-[1.35fr_1fr]">
        <Card aria-labelledby="machine-events">
          <CardTitle id="machine-events">{t('machines.events')}</CardTitle>
          <CardHint>{t('machines.eventsHint')}</CardHint>
          {events.data === undefined && !events.error ? (
            <ListSkeleton rows={4} lines={1} rowClassName={cn(eventRow, 'items-center')} className="mt-3 flex flex-col" trailing={<Skeleton className="h-3 w-10" />} />
          ) : events.error ? (
            <p className="mt-3 text-[13px] text-destructive-foreground">{errorText(events.error)}</p>
          ) : eventRows.length === 0 ? (
            <p className="mt-3 text-[13px] text-muted-foreground">{t('machines.eventsEmpty')}</p>
          ) : (
            <ul className="mt-3 flex flex-col">
              {eventRows.map(({ key, item: e, state }) => {
                const i = list.indexOf(e)
                return (
                  <li key={key} {...presenceProps(state)} className={eventRow}>
                    <span className="min-w-0 flex-1">{i >= 0 ? machineEventText(list, i) : machineEventText([e], 0)}</span>
                    <span className="shrink-0 text-xs text-muted-foreground">{formatWhen(e.at, new Date(now))}</span>
                  </li>
                )
              })}
            </ul>
          )}
        </Card>
        {manage && (
          <Card aria-labelledby="machine-remove">
            <CardTitle id="machine-remove">{t('machines.remove.title', { name })}</CardTitle>
            <CardHint>{t('machines.remove.hint')}</CardHint>
            <Button variant="destructive-outline" size="sm" className="mt-auto self-start" onClick={() => setRemoving(true)}>
              <Trash2Icon />
              {t('machines.remove.button', { name })}
            </Button>
          </Card>
        )}
      </div>
      <RemoveDialog machine={m} servers={servers.map((s) => s.name)} open={removing} onOpenChange={setRemoving} />
    </>
  )
}

const eventKey = (e: MachineEvent) => `${e.at}:${e.kind}:${e.code ?? ''}`
const eventRow = 'flex items-baseline gap-3 border-t border-border py-2 text-[13px] first:border-t-0'

/** Settings › Machines › a machine while the machines load: its heading and facts. */
function DetailsSkeleton() {
  return (
    <>
      <div className="flex items-start gap-3">
        <LoadingLabel />
        <Skeleton className="mt-1.5 size-5 rounded" />
        <div className="flex flex-col gap-2 pt-1">
          <Skeleton className="h-5 w-40" />
          <Skeleton className="h-3 w-56" />
        </div>
      </div>
      <Card className="py-1">
        {[0, 1, 2, 3, 4].map((i) => (
          <div key={i} className="grid grid-cols-[94px_minmax(0,1fr)] gap-4 border-t border-border py-3 first:border-t-0">
            <Skeleton className="h-3 w-16" />
            <Skeleton className={cn('h-3', lineWidth(i))} />
          </div>
        ))}
      </Card>
    </>
  )
}

/** The connect card while the dashboard's joining details load: its heading and the four steps. */
function ConnectSkeleton() {
  return (
    <Card>
      <LoadingLabel />
      <Skeleton className="h-4 w-48" />
      <Skeleton className="mt-2 h-3 w-60" />
      <div className="mt-5 flex flex-col gap-5">
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="grid grid-cols-[20px_minmax(0,1fr)] items-center gap-x-3">
            <Skeleton className="size-5 rounded-full" />
            <Skeleton className="h-3.5 w-32" />
            <Skeleton className={cn('col-start-2 mt-2 h-3', lineWidth(i + 1))} />
          </div>
        ))}
      </div>
    </Card>
  )
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[94px_minmax(0,1fr)] gap-4 border-t border-border py-3 text-[13px] first:border-t-0">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}

function RemoveDialog({ machine: m, servers, open, onOpenChange }: { machine: MachineView; servers: string[]; open: boolean; onOpenChange: (open: boolean) => void }) {
  const ws = useWorkspace()
  const [busy, setBusy] = useState(false)
  const name = machineLabel(m)
  async function remove() {
    setBusy(true)
    try {
      await del(machineApi(m.id))
      toastManager.add({ title: t('machines.removed', { name }), type: 'success' })
      onOpenChange(false)
      navigate({ name: 'machines' })
      void ws.refresh()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[400px]">
        <DialogHeader>
          <DialogTitle className="text-lg font-bold">{t('machines.remove.confirmTitle', { name })}</DialogTitle>
          <DialogDescription>{t('machines.remove.confirmBody')}</DialogDescription>
        </DialogHeader>
        <div className="px-6 pb-4 text-[13px]">
          <p className="font-semibold">{servers.length ? t('machines.remove.keeps', { count: servers.length, servers: formatList(servers), name }) : t('machines.remove.keepsAny', { name })}</p>
          <p className="text-xs text-muted-foreground">{t('machines.remove.keepsHint')}</p>
        </div>
        <DialogFooter variant="bare" className="mx-6 border-t border-border px-0 pt-4">
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button variant="destructive" onClick={() => void remove()} loading={busy}>
            <Trash2Icon />
            {t('machines.remove.confirm', { name })}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}
