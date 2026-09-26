import { useState } from 'react'
import { ArrowLeftIcon, ArrowRightIcon, ExternalLinkIcon, PlayIcon, RefreshCwIcon, RotateCwIcon, Trash2Icon } from 'lucide-react'
import { useCatalog } from '@/api/catalog'
import { get, post } from '@/api/client'
import type { Activity, FileRefusal, LogsResponse, MachineView, ServerStatus, SessionsResponse } from '@/api/types'
import { errorText, serverApi, useServerMachine, useWorkspace } from '@/api/workspace'
import { ActivityList } from '@/components/app/activity'
import { Pip } from '@/components/app/art'
import { Card, CardTitle, CopyButton, MeterRow, Notice, PlayerFace, useNow } from '@/components/app/bits'
import { FirstStepsCard } from '@/components/app/checklist'
import { CardGroup, ChoiceCard, useIsPhone } from '@/components/app/controls'
import { PlayersChart } from '@/components/app/players-chart'
import { JobSteps, type StepState } from '@/components/app/update'
import { Button } from '@/components/ui/button'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { parseLine, ranOutOfMemory } from '@/lib/console'
import { formatBytes, formatClock, formatDate, formatDuration, formatList, formatMB, formatPercent, formatSpan, joinAddress, relativeTime, sameDay } from '@/lib/format'
import { awayLong, joinHost, machineLabel, machineRoute } from '@/lib/machines'
import { createStepOf, isSettingUp, opLabel, phaseLabel, whyNot } from '@/lib/phase'
import { linkPath, linkProps } from '@/lib/router'
import { typeName } from '@/lib/servers'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { serverAction } from '.'

export function Overview({ server }: { server: ServerStatus }) {
  const { reach, stale } = useServerMachine(server)
  if (reach.state === 'away') return <MachineAwayView server={server} machine={reach.machine} since={reach.since} />
  if (reach.state === 'agentDown') return <AgentDownView machine={reach.machine} since={reach.since} />
  if (!stale && isSettingUp(server)) return <SettingUpView server={server} />
  if (!stale && (server.phase === 'crashed' || (server.refusal && server.phase !== 'docker_unavailable')) && !server.operation) return <CrashedView server={server} />
  return <Running server={server} />
}

function Running({ server: s }: { server: ServerStatus }) {
  const phone = useIsPhone()
  const activity = usePoll(() => get<Activity[]>(serverApi(s.id, '/activity?limit=5')), 15_000, s.id)
  const { servers } = useWorkspace()
  return (
    <>
      <ServerNotices server={s} />
      <FirstStepsCard server={s} phone={phone} onBackup={() => void serverAction(s, 'backups')} />
      <div className="grid gap-4 lg:grid-cols-3">
        <JoinCard server={s} />
        <PlayingCard server={s} />
        <RunningCard server={s} />
      </div>
      <div className="grid gap-4 lg:grid-cols-[1.45fr_1fr]">
        <PlayersChart server={s} />
        <Card>
          <CardTitle>{t('overview.activity')}</CardTitle>
          <ActivityList items={activity.data} servers={servers ?? []} here empty={t('overview.activityEmpty')} className="mt-3 flex-1" />
          <div className="mt-4 border-t border-border pt-3">
            <a {...linkPath('/settings#audit')} className="inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline">
              {t('home.auditLink')}
              <ArrowRightIcon className="size-3.5" aria-hidden="true" />
            </a>
          </div>
        </Card>
      </div>
    </>
  )
}

const recentMs = 15 * 60_000

/** Whether what a failed job wanted has happened since, so its notice can go. */
function recovered(s: ServerStatus): boolean {
  const op = s.lastOperation
  if (!op) return true
  if (['create', 'start', 'restart', 'recover', 'auto-restart'].includes(op.kind)) return s.phase === 'online'
  const needed = op.detail?.neededBytes
  if (op.kind === 'backup' && typeof needed === 'number') return (s.resources?.diskFreeBytes ?? 0) >= needed
  return false
}

/** One quiet line at a time: test mode, Docker, a failed job, or settings waiting for a restart. */
function ServerNotices({ server: s }: { server: ServerStatus }) {
  const { stale, offline, machine } = useServerMachine(s)
  const [dismissed, setDismissed] = useState<string>()
  const [busy, setBusy] = useState(false)
  if (stale) return null
  const last = s.lastOperation
  if (s.offlineModeTest) return <Notice tone="error" title={t('error.notice')}>{t('error.noticeBody')}</Notice>
  if (s.phase === 'docker_unavailable') return <Notice tone="warning" title={s.lastError ?? t('status.docker')}>{s.lastErrorHint}</Notice>
  const disk = machine?.live?.diskWarning
  if (disk) return <Notice tone={disk.status === 'fail' ? 'error' : 'warning'} title={t('overview.lowDiskTitle', { detail: disk.detail })}>{disk.fix}</Notice>
  if (last && last.status === 'failed' && last.finishedAt && Date.now() - new Date(last.finishedAt).getTime() < recentMs && dismissed !== last.id && !recovered(s)) {
    return (
      <Notice
        tone="error"
        title={`${t('op.failed', { what: opLabel(last, s.name) })}: ${last.error ?? ''}`}
        action={
          <Button variant="ghost" size="sm" onClick={() => setDismissed(last.id)}>
            {t('common.dismiss')}
          </Button>
        }
      >
        {last.hint}
      </Notice>
    )
  }
  if (s.pendingRestart && s.phase === 'online') {
    return (
      <Notice
        title={t('overview.pendingRestart')}
        action={
          <Button
            variant="outline"
            size="sm"
            loading={busy}
            disabledReason={whyNot(s, 'restart', offline)}
            onClick={async () => {
              setBusy(true)
              await serverAction(s, 'restart')
              setBusy(false)
            }}
          >
            <RotateCwIcon />
            {t('server.restart')}
          </Button>
        }
      >
        {t('overview.pendingRestartBody', { server: s.name })}
      </Notice>
    )
  }
  return null
}

function JoinCard({ server: s }: { server: ServerStatus }) {
  const { stale, host } = useServerMachine(s)
  const phone = useIsPhone()
  const address = joinAddress(host, s.gamePort)
  const online = !stale && s.phase === 'online'
  return (
    <Card>
      <div className="flex items-start justify-between gap-3">
        <CardTitle className="max-sm:text-[17px]">{t('overview.join')}</CardTitle>
        <CopyButton text={address} size={phone ? 'lg' : 'sm'} toast={t('toast.copied')} />
      </div>
      <p className="mt-2 text-[26px] leading-8 font-extrabold tracking-[-0.01em] break-all tabular-nums max-sm:text-[28px]">{address}</p>
      {!phone && <p className="mt-1 text-[13px] text-muted-foreground">{t('overview.joinHelp')}</p>}
      <p className="mt-auto flex items-center gap-2 pt-4 text-xs text-muted-foreground max-sm:pt-3 max-sm:text-[13px]">
        {online && s.reachable ? (
          <>
            <span className="size-2 rounded-full bg-success" aria-hidden="true" />
            {phone ? t('overview.answeringPhone', { time: relativeTime(s.reachableAt) }) : t('overview.answering', { port: s.gamePort, time: relativeTime(s.reachableAt) })}
          </>
        ) : online ? (
          <>
            <span className="size-2 rounded-full border-[1.5px] border-muted-foreground/60" aria-hidden="true" />
            {t('overview.notAnswering')}
          </>
        ) : (
          t('overview.offline', { server: s.name })
        )}
      </p>
    </Card>
  )
}

function PlayingCard({ server: s }: { server: ServerStatus }) {
  const { stale } = useServerMachine(s)
  const phone = useIsPhone()
  const online = !stale && s.phase === 'online'
  const names = online ? (s.players?.names ?? []) : []
  const sessions = usePoll(() => (names.length ? get<SessionsResponse>(serverApi(s.id, '/players/sessions?range=24h')) : Promise.resolve(undefined)), 30_000, `${s.id}:${names.join(',')}`)
  const open = new Map((sessions.data?.sessions ?? []).filter((x) => !x.end).map((x) => [x.player.toLowerCase(), x.durationSeconds]))
  const count = online ? (s.players?.online ?? 0) : 0
  const max = s.players?.max ?? s.config?.maxPlayers ?? 0
  return (
    <Card>
      <div className="flex items-center justify-between gap-3">
        <CardTitle className="max-sm:text-[17px]">{t('overview.playing')}</CardTitle>
        {phone ? (
          online && <span className="text-[15px] text-muted-foreground tabular-nums">{t('overview.countOfMax', { count, max })}</span>
        ) : (
          <a {...linkProps({ name: 'server', slug: s.slug, tab: 'players' })} className="inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline">
            {t('overview.allPlayers')}
            <ArrowRightIcon className="size-3.5" aria-hidden="true" />
          </a>
        )}
      </div>
      {!online ? (
        <p className="mt-3 text-[13px] text-muted-foreground">{t('overview.notRunning')}</p>
      ) : phone ? (
        names.length ? (
          <div className="mt-3 flex items-center gap-2.5">
            <span className="flex gap-1">
              {names.slice(0, 3).map((n) => (
                <PlayerFace key={n} name={n} size={32} />
              ))}
            </span>
            <span className="min-w-0 truncate text-[15px] text-muted-foreground">{formatList(names.slice(0, 3))}</span>
          </div>
        ) : (
          <p className="mt-3 text-[15px] text-muted-foreground">{t('overview.nobody')}</p>
        )
      ) : (
        <>
          <p className="mt-2 flex items-baseline gap-1.5">
            <span className="text-[26px] leading-8 font-extrabold tabular-nums">{count}</span>
            <span className="text-[13px] text-muted-foreground">{t('overview.ofMax', { max })}</span>
          </p>
          {names.length ? (
            <ul className="mt-2 flex flex-col gap-1.5">
              {names.slice(0, 4).map((n) => (
                <li key={n} className="flex items-center gap-2.5 text-[13px]">
                  <PlayerFace name={n} size={22} />
                  <span className="min-w-0 flex-1 truncate font-medium">{n}</span>
                  <span className="text-xs text-muted-foreground tabular-nums">{open.has(n.toLowerCase()) ? formatDuration(open.get(n.toLowerCase()) ?? 0) : ''}</span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="mt-2 text-[13px] text-muted-foreground">{t('overview.nobody')}</p>
          )}
        </>
      )}
    </Card>
  )
}

function RunningCard({ server: s }: { server: ServerStatus }) {
  const place = useServerMachine(s)
  const phone = useIsPhone()
  const r = !place.stale && s.phase === 'online' ? s.resources : undefined
  const uptime = (
    <div>
      <dt className="text-muted-foreground">{t('overview.uptime')}</dt>
      <dd className="mt-0.5 text-[13px] font-semibold">{s.startedAt ? formatSpan((Date.now() - new Date(s.startedAt).getTime()) / 1000) : t('common.none')}</dd>
    </div>
  )
  const limitBytes = (s.config?.memoryMB ?? 0) * 1024 * 1024
  const tps = r?.tps
  return (
    <Card>
      <div className="flex items-center justify-between gap-3">
        <CardTitle className="max-sm:text-[17px]">{t('overview.running')}</CardTitle>
        {place.route && (
          <a {...linkProps(place.route)} className="inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline max-sm:text-[15px]">
            {place.name}
            {!phone && <ArrowRightIcon className="size-3.5" aria-hidden="true" />}
          </a>
        )}
      </div>
      {!r ? (
        <p className="mt-3 text-[13px] text-muted-foreground">{t('overview.notRunning')}</p>
      ) : (
        <>
          <div className="mt-3 flex flex-col gap-3.5">
            <MeterRow label={t('overview.memory')} value={t('home.ofTotal', { used: formatBytes(r.memBytes), total: formatMB(s.config?.memoryMB ?? 0) })} percent={limitBytes && r.memBytes ? (r.memBytes / limitBytes) * 100 : 0} />
            <MeterRow label={t('overview.cpu')} value={formatPercent(r.cpuPercent)} percent={r.cpuPercent} />
          </div>
          {!phone && place.shared && (
            <dl className="mt-auto grid grid-cols-2 gap-3 pt-4 text-xs">
              <div className="col-span-2 -mb-1 border-t border-border" aria-hidden="true" />
              {uptime}
              <div>
                <dt className="text-muted-foreground">{t('machines.runsOn')}</dt>
                <dd className="mt-0.5 truncate text-[13px] font-semibold">{place.name}</dd>
              </div>
            </dl>
          )}
          {!phone && !place.shared && (
            <dl className="mt-auto grid grid-cols-3 gap-3 pt-4 text-xs">
              {uptime}
              <div>
                <dt className="text-muted-foreground">{t('overview.tickRate')}</dt>
                <dd className="mt-0.5 text-[13px] font-semibold tabular-nums">
                  {tps !== undefined ? tps.toFixed(1) : '—'}
                  {tps !== undefined && <span className={cn('ml-1.5 text-xs font-normal', tps >= 18 ? 'text-muted-foreground' : 'text-warning-foreground')}>{tps >= 18 ? t('overview.smooth') : t('overview.laggy')}</span>}
                </dd>
              </div>
              <div>
                <dt className="text-muted-foreground">{t('overview.diskFree')}</dt>
                <dd className="mt-0.5 text-[13px] font-semibold">{formatBytes(r.diskFreeBytes)}</dd>
              </div>
            </dl>
          )}
        </>
      )}
    </Card>
  )
}

export function ConsoleTail({ lines, className }: { lines: string[]; className?: string }) {
  return (
    <div className={cn('overflow-hidden rounded-xl bg-console px-3 py-2.5 font-mono text-xs leading-5 text-[#e8e8e0]', className)}>
      {lines.map((l, i) => {
        const p = parseLine(l)
        return (
          <div key={i} className="flex gap-3 truncate">
            {p.time && <span className="shrink-0 text-[#a3a89c]">{p.time}</span>}
            {p.level && p.level !== 'INFO' && <span className={cn('shrink-0 font-semibold', p.level === 'WARN' ? 'text-[#f5b94a]' : 'text-[#f87171]')}>{p.level}</span>}
            <span className={cn('truncate', p.level === 'WARN' && 'text-[#f5b94a]', (p.level === 'ERROR' || p.level === 'FATAL') && 'text-[#f87171]')}>{p.text}</span>
          </div>
        )
      })}
    </div>
  )
}

function useTail(server: ServerStatus, count: number, every: number) {
  const logs = usePoll(() => get<LogsResponse>(serverApi(server.id, `/logs?limit=${count}`)), every, server.id)
  return logs.data?.lines.map((l) => l.text) ?? []
}

function SettingUpView({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const place = useServerMachine(s)
  const tail = useTail(s, 3, 2000)
  const op = s.operation ?? s.lastOperation
  const failed = !s.operation && op?.status === 'failed'
  const [busy, setBusy] = useState(false)
  const cfg = s.config
  const type = typeName(s.type)
  const version = cfg?.minecraftVersion ?? ''
  const at = createStepOf(failed ? (op?.phase ?? '') : s.phase)
  const state = (i: number): StepState => (i < at ? 'done' : i === at ? (failed ? 'failed' : 'current') : 'todo')
  const pct = /(\d{1,3})\s*%/.exec(s.phaseDetail ?? '')?.[1]
  const other = (ws.servers ?? []).find((o) => o.id !== s.id)
  const disk = place.machine?.live?.diskFreeBytes
  return (
    <Card className="mx-auto w-full max-w-[520px] p-6 max-sm:p-4">
      <div className="flex items-start gap-4">
        <Pip pose={failed ? 'hurt' : 'hardhat'} size={64} />
        <div className="min-w-0 pt-1">
          <h2 className="text-lg font-bold">{failed ? t('creating.failedTitle', { server: s.name }) : t('creating.title', { server: s.name })}</h2>
          <p className="mt-1 text-[13px] leading-[18px] text-muted-foreground">{failed ? (op?.error ?? '') : t('creating.lead')}</p>
          {failed && op?.hint && <p className="mt-1 text-[13px] text-muted-foreground">{op.hint}</p>}
        </div>
      </div>
      <div className="mt-5 border-t border-border pt-5">
        <JobSteps
          steps={[
            { title: t('creating.checked', { machine: place.name }), hint: t('creating.checkedDetail', { memory: formatMB(cfg?.memoryMB ?? 0), disk: formatBytes(disk) }), state: state(0) },
            { title: at > 1 ? t('creating.downloaded', { type, version }) : t('creating.downloading', { type, version }), hint: t('creating.downloadedDetail'), state: state(1) },
            { title: t('creating.starting'), hint: pct ? t('creating.startingPercent', { percent: pct }) : t('creating.startingDetail'), state: state(2), progress: pct ? Number(pct) : undefined },
            { title: t('creating.reachable', { port: s.gamePort }), state: state(3) },
          ]}
        />
      </div>
      {tail.length > 0 && (
        <div className="mt-5">
          <div className="flex items-center justify-between text-xs">
            <span className="font-semibold">{t('creating.output')}</span>
            <a {...linkProps({ name: 'server', slug: s.slug, tab: 'console' })} className="inline-flex items-center gap-1 font-medium text-primary hover:underline">
              {t('creating.openConsole')}
              <ArrowRightIcon className="size-3.5" aria-hidden="true" />
            </a>
          </div>
          <ConsoleTail lines={tail} className="mt-2" />
        </div>
      )}
      <div className="mt-5 flex flex-wrap items-center justify-between gap-3">
        {failed ? (
          <>
            <Button variant="outline" render={<a {...linkPath(`/servers/${s.slug}/settings#danger`)} />}>
              <Trash2Icon />
              {t('server.deleteMenu')}
            </Button>
            <Button
              loading={busy}
              onClick={async () => {
                setBusy(true)
                await serverAction(s, 'start')
                setBusy(false)
              }}
            >
              <RefreshCwIcon />
              {t('common.tryAgain')}
            </Button>
          </>
        ) : (
          <Button variant="outline" render={<a {...linkProps(other ? { name: 'server', slug: other.slug, tab: 'overview' } : { name: 'home' })} />}>
            <ArrowLeftIcon />
            {other ? t('creating.takeMe', { server: other.name }) : t('creating.takeHome')}
          </Button>
        )}
      </div>
    </Card>
  )
}

function CrashedView({ server: s }: { server: ServerStatus }) {
  const { machine, offline } = useServerMachine(s)
  const tail = useTail(s, 3, 10_000)
  const refusal = s.refusal
  const oom = !refusal && ranOutOfMemory(s.exitCode, tail)
  const { catalog } = useCatalog(machine?.id, { server: s.id, fresh: true })
  const current = s.config?.memoryMB ?? 0
  const bigger = (catalog?.memoryOptionsMB ?? []).filter((mb) => mb > current && mb <= (catalog?.maxMemoryMB ?? 0))[0]
  const [choice, setChoice] = useState<'more' | 'keep'>('more')
  const [busy, setBusy] = useState(false)
  const free = machine?.live?.memoryFreeMB
  const withMore = oom && bigger !== undefined

  async function fix() {
    setBusy(true)
    try {
      if (withMore && choice === 'more') await post(serverApi(s.id, '/settings'), { memoryMB: bigger })
      await post(serverApi(s.id, '/start'))
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="grid gap-4 lg:grid-cols-[1.55fr_1fr]">
      <Card className="p-6">
        <div className="flex items-start gap-5">
          <Pip pose="hurt" size={80} className="max-sm:hidden" />
          <div className="min-w-0">
            <h2 className="text-lg font-bold">{t('crash.what')}</h2>
            {refusal ? (
              <p className="mt-1 text-sm">{refusalLine(refusal, s.name)}</p>
            ) : (
              <>
                <p className="mt-1 text-sm">{oom ? t('crash.oom', { memory: formatMB(current) }) : (s.lastError ?? t('crash.generic', { server: s.name }))}</p>
                {!oom && s.lastErrorHint && <p className="mt-2 text-[13px] text-muted-foreground">{s.lastErrorHint}</p>}
              </>
            )}
          </div>
        </div>
        {!refusal && tail.length > 0 && (
          <div className="mt-auto pt-5">
            <div className="text-xs font-semibold">{t('crash.lastLines')}</div>
            <ConsoleTail lines={tail} className="mt-2" />
          </div>
        )}
      </Card>
      <Card>
        <CardTitle>{t('crash.fix')}</CardTitle>
        {withMore ? (
          <>
            <p className="mt-0.5 text-[13px] text-muted-foreground">{t('crash.pick')}</p>
            <CardGroup value={choice} onChange={setChoice} label={t('crash.fix')} className="mt-3 flex flex-col gap-2.5">
              <ChoiceCard value="more" radio="start" className="gap-3 p-3.5">
                <span className="text-sm font-semibold">{t('crash.more', { server: s.name, memory: formatMB(bigger) })}</span>
                <span className="ml-2 text-xs font-medium text-success-foreground">{t('common.recommended')}</span>
                <span className="mt-0.5 block text-xs text-muted-foreground">{free !== undefined ? t('crash.moreHint', { free: formatMB(free) }) : ''}</span>
              </ChoiceCard>
              <ChoiceCard value="keep" radio="start" className="gap-3 p-3.5">
                <span className="text-sm font-semibold">{t('crash.keep', { memory: formatMB(current) })}</span>
                <span className="mt-0.5 block text-xs text-muted-foreground">{t('crash.keepHint')}</span>
              </ChoiceCard>
            </CardGroup>
          </>
        ) : (
          <p className="mt-1 text-[13px] text-muted-foreground">{refusal ? t('crash.refusedFix', { file: refusal.params.path, server: s.name }) : t('crash.startHint', { server: s.name })}</p>
        )}
        <Button className="mt-4 w-full" size="lg" loading={busy} onClick={fix} disabledReason={whyNot(s, 'start', offline)}>
          <PlayIcon />
          {withMore && choice === 'more' ? t('crash.save', { server: s.name }) : t('crash.startOnly', { server: s.name })}
        </Button>
      </Card>
    </div>
  )
}

/** Names the file that stopped a start and what to do about it. */
function refusalLine(r: FileRefusal, server: string): string {
  const file = r.params.path
  const english = [r.message, r.hint].filter(Boolean).join(' ')
  switch (r.code) {
    case 'link':
      return t('crash.refusedLink', { server, file })
    case 'special_file':
      return t('crash.refusedSpecial', { server, file })
    case 'not_a_file':
    case 'not_a_folder':
    case 'too_large':
    case 'too_many_entries':
    case 'changed':
    case 'bad_name':
      return english
    default: {
      const unreachable: never = r.code
      return english || unreachable
    }
  }
}

/** A machine's agent doesn't answer: the dashboard's own, or a joined machine's while its link is up. since is when it was last heard. */
function AgentDownView({ machine, since }: { machine?: MachineView; since?: string }) {
  const ws = useWorkspace()
  const [busy, setBusy] = useState(false)
  const command = t('agentDown.command')
  const name = machineLabel(machine) || ws.machineName
  return (
    <div className="flex flex-1 flex-col items-center py-8 text-center max-sm:py-2">
      <Pip pose="search" size={96} />
      <h2 className="mt-4 text-xl font-bold max-sm:text-lg">{t('agentDown.title')}</h2>
      <p className="mt-2 max-w-[520px] text-sm text-muted-foreground">
        {since ? t('agentDown.bodySince', { machine: name, time: relativeTime(since) }) : t('agentDown.body', { machine: name })}
      </p>
      <div className="mt-5 flex flex-wrap items-center justify-center gap-3">
        <Button
          loading={busy}
          onClick={async () => {
            setBusy(true)
            await ws.refresh()
            setBusy(false)
          }}
        >
          <RefreshCwIcon />
          {t('agentDown.check')}
        </Button>
        <a href={t('agentDown.whatUrl')} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-sm font-medium hover:underline">
          {t('agentDown.what')}
          <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
        </a>
      </div>
      <Card className="mt-8 w-full max-w-[460px] text-left">
        <CardTitle>{machine?.kind === 'remote' ? t('machines.agentDown.fixOn', { name }) : t('agentDown.fix')}</CardTitle>
        <p className="mt-1 text-xs text-muted-foreground">{t('agentDown.fixBody')}</p>
        <div className="mt-3 flex items-center gap-2">
          <code className="min-w-0 flex-1 truncate rounded-lg bg-console px-3 py-2 text-xs text-[#e8e8e0]">{command}</code>
          <CopyButton text={command} />
        </div>
        <p className="mt-3 text-xs text-muted-foreground">{t('agentDown.note')}</p>
      </Card>
    </div>
  )
}

/** A joined machine hasn't called in: the server may still run there, but the dashboard can't see or control it. */
function MachineAwayView({ server: s, machine: m, since }: { server: ServerStatus; machine: MachineView; since?: string }) {
  const phone = useIsPhone()
  const now = useNow(30_000)
  const name = machineLabel(m)
  const address = joinAddress(joinHost(m, window.location.hostname), s.gamePort)
  const joined = m.joinedAt
  let title: string
  if (since) title = t('machines.problem.offline', { name, duration: awayLong(since, now) })
  else if (joined) title = t('machines.problem.neverConnected', { name, duration: awayLong(joined, now) })
  else title = t('machines.away.pill', { name })
  return (
    <>
      <Card className="animate-fade flex-row items-center gap-7 px-8 py-6 max-sm:flex-col max-sm:items-start max-sm:gap-3 max-sm:p-5" role="status">
        <Pip pose="sleep" size={phone ? 64 : 84} />
        <div className="min-w-0">
          <h2 className="text-[17px] leading-6 font-bold">{title}</h2>
          <p className="mt-1 text-sm">{t('machines.away.body', { server: s.name, name })}</p>
          <p className="mt-2.5 text-xs text-muted-foreground">{since ? t('machines.problem.offlineHint', { name }) : t('machines.problem.neverConnectedHint', { name })}</p>
          <Button variant="outline" size="sm" className="mt-2" render={<a {...linkProps(machineRoute(m))} />}>
            {t('machines.details')}
          </Button>
        </div>
      </Card>
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <div className="flex items-start justify-between gap-3">
            <CardTitle className="max-sm:text-[17px]">{t('overview.join')}</CardTitle>
            <CopyButton text={address} size={phone ? 'lg' : 'sm'} toast={t('toast.copied')} />
          </div>
          <p className="mt-2 text-xl leading-7 font-extrabold tracking-[-0.01em] break-all">{address}</p>
          <p className="mt-auto flex items-center gap-2 pt-4 text-xs text-muted-foreground">
            <span className="size-2 rounded-full border-[1.5px] border-muted-foreground/60" aria-hidden="true" />
            {t('machines.away.join', { name })}
          </p>
        </Card>
        <LastKnownCard server={s} at={s.lastKnownAt ?? since} />
      </div>
    </>
  )
}

/** What the dashboard last heard about a server it can't see now: players, or its state, and when. */
function LastKnownCard({ server: s, at }: { server: ServerStatus; at?: string }) {
  const online = s.phase === 'online'
  const names = online ? (s.players?.names ?? []) : []
  const count = online ? (s.players?.online ?? 0) : 0
  let line = t('common.none')
  if (at) {
    const time = sameDay(new Date(at), new Date()) ? formatClock(at) : `${formatDate(at)} ${formatClock(at)}`
    if (!online) line = t('machines.lastKnownState', { state: phaseLabel(s.phase), time })
    else if (count) line = t('machines.lastKnownPlaying', { count, time })
    else line = t('machines.lastKnownNobody', { time })
  }
  return (
    <Card>
      <CardTitle className="max-sm:text-[17px]">{t('machines.lastKnown')}</CardTitle>
      <p className="mt-2 text-[15px] font-semibold">{line}</p>
      {names.length > 0 && <p className="mt-0.5 text-xs text-muted-foreground">{formatList(names.slice(0, 4))}</p>}
    </Card>
  )
}
