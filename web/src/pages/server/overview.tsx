import { useEffect, useState } from 'react'
import { ArrowLeftIcon, ArrowRightIcon, ExternalLinkIcon, PlayIcon, RefreshCwIcon, RotateCwIcon, Trash2Icon } from 'lucide-react'
import { del, get, post } from '@/api/client'
import type { Activity, AddonNotice, Crash, LagStatus, LogsResponse, RestorePreview, ServerStatus, SessionsResponse } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { ActivityList } from '@/components/app/activity'
import { Pip } from '@/components/app/art'
import { Card, CardTitle, CopyButton, MeterRow, Notice, PlayerFace, SectionLabel } from '@/components/app/bits'
import { FirstStepsCard } from '@/components/app/checklist'
import { CardGroup, ChoiceCard, useIsPhone } from '@/components/app/controls'
import { loaderLabel } from '@/components/app/modpacks'
import { FailedJobNotice, SavingPausedNotice } from '@/components/app/notices'
import { PlayersChart } from '@/components/app/players-chart'
import { RestoreDialog } from '@/components/app/restore'
import { SignInNotice } from '@/components/app/sign-in-notice'
import { SoftwareChangedView } from '@/components/app/software'
import { JobSteps, type StepState } from '@/components/app/update'
import { Button } from '@/components/ui/button'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { parseLine } from '@/lib/console'
import { crashDetail, crashFixes, crashSummary, lookupKey, lookUpAddonFixes, phoneLines, preselect, refusalFixes, refusalLine, type AddonLookups } from '@/lib/crash'
import { formatBytes, formatDuration, formatList, formatMB, formatPercent, formatSpan, relativeTime, serverJoinAddress } from '@/lib/format'
import { busyReason, createStepOf, failedJob, isSettingUp, packStepOf, statusTone, templateStepOf, whyNot } from '@/lib/phase'
import { linkPath, linkProps } from '@/lib/router'
import { formatTPS } from '@/lib/running'
import { typeName } from '@/lib/servers'
import { addonKind } from '@/lib/software'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { serverAction } from '.'

export function Overview({ server }: { server: ServerStatus }) {
  const ws = useWorkspace()
  if (ws.agentDown) return <AgentDownView />
  if (!ws.stale && isSettingUp(server)) return <SettingUpView server={server} />
  if (!ws.stale && server.softwareChanged && !server.operation) return <SoftwareChangedView server={server} change={server.softwareChanged} />
  if (!ws.stale && statusTone(server) === 'crashed' && !server.operation) return <CrashedView server={server} />
  return <Running server={server} />
}

function Running({ server: s }: { server: ServerStatus }) {
  const phone = useIsPhone()
  const activity = usePoll(() => get<Activity[]>(serverApi(s.id, '/activity?limit=5')), 15_000, s.id)
  const { servers } = useWorkspace()
  return (
    <>
      {phone && <SignInNotice />}
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

/** One quiet line at a time: test mode, Docker, world saving paused, a failed job, or settings waiting for a restart. */
function ServerNotices({ server: s }: { server: ServerStatus }) {
  const { stale, machine, refresh } = useWorkspace()
  const [dismissed, setDismissed] = useState<string>()
  const [busy, setBusy] = useState(false)
  if (stale) return null
  if (s.offlineModeTest) return <Notice tone="error" title={t('error.notice')}>{t('error.noticeBody')}</Notice>
  if (s.phase === 'docker_unavailable') return <Notice tone="warning" title={s.lastError ?? t('status.docker')}>{s.lastErrorHint}</Notice>
  if (s.savingPausedSince) return <SavingPausedNotice server={s} />
  const disk = machine?.live?.diskWarning
  if (disk) return <Notice tone={disk.status === 'fail' ? 'error' : 'warning'} title={t('overview.lowDiskTitle', { detail: disk.detail })}>{disk.fix}</Notice>
  const failed = failedJob(s)
  if (failed && dismissed !== failed.id) return <FailedJobNotice server={s} op={failed} onDismiss={() => setDismissed(failed.id)} />

  if (s.pendingRestart && s.phase === 'online') {
    return (
      <Notice
        title={t('overview.pendingRestart')}
        action={
          <Button
            variant="outline"
            size="sm"
            loading={busy}
            disabledReason={whyNot(s, 'restart', stale)}
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
  const skipped = s.config?.template?.skipped ?? []
  if (skipped.length > 0 && !s.config?.template?.pending) {
    const names = skipped.map((n) => n.params?.name ?? n.message)
    return (
      <Notice
        title={t('templateSkipped.title', { count: skipped.length, names: formatList(names) })}
        action={
          <Button
            variant="outline"
            size="sm"
            loading={busy}
            disabledReason={busyReason(s)}
            onClick={async () => {
              setBusy(true)
              try {
                await post(serverApi(s.id, '/template/retry'), {})
                await refresh()
              } catch (e) {
                toastManager.add({ title: errorText(e), type: 'error' })
              } finally {
                setBusy(false)
              }
            }}
          >
            <RefreshCwIcon />
            {t('common.tryAgain')}
          </Button>
        }
      >
        {skipped.length === 1 ? skipped[0]?.message : t('templateSkipped.body')}
      </Notice>
    )
  }
  return null
}

function JoinCard({ server: s }: { server: ServerStatus }) {
  const { stale } = useWorkspace()
  const phone = useIsPhone()
  const address = serverJoinAddress(s)
  const online = !stale && s.phase === 'online'
  const long = address.length > 20
  return (
    <Card>
      <div className="flex items-start justify-between gap-3">
        <CardTitle className="max-sm:text-[17px]">{t('overview.join')}</CardTitle>
        <CopyButton text={address} size={phone ? 'lg' : 'sm'} toast={t('toast.copied')} />
      </div>
      <p className={cn('mt-2 font-extrabold tracking-[-0.01em] break-all tabular-nums', long ? 'text-xl leading-[26px]' : 'text-[26px] leading-8 max-sm:text-[28px]')}>{address}</p>
      {!phone && <p className="mt-1 text-[13px] text-muted-foreground">{t('overview.joinHelp')}</p>}
      <p className="mt-auto flex items-center gap-2 pt-4 text-xs text-muted-foreground max-sm:pt-3 max-sm:text-[13px]">
        {online && s.reachable ? (
          <>
            <span className="size-2 rounded-full bg-success" aria-hidden="true" />
            {phone || s.joinAddress ? t('overview.answeringPhone', { time: relativeTime(s.reachableAt) }) : t('overview.answering', { port: s.gamePort, time: relativeTime(s.reachableAt) })}
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
  const { stale } = useWorkspace()
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

/** The tick rate's word on the Overview card: "smooth", or "a bit behind" in amber. */
function lagWord(lag: LagStatus | undefined): { text: string; behind: boolean } | undefined {
  switch (lag) {
    case 'smooth':
      return { text: t('overview.smooth'), behind: false }
    case 'a_bit_behind':
      return { text: t('overview.behind'), behind: true }
    case 'lagging':
      return { text: t('overview.lagging'), behind: true }
    case 'frozen':
      return { text: t('overview.paused'), behind: false }
    case 'unknown':
    case undefined:
      return undefined
    default: {
      const unknown: never = lag
      void unknown
      return undefined
    }
  }
}

function RunningCard({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const r = !ws.stale && s.phase === 'online' ? s.resources : undefined
  const limitBytes = (s.config?.memoryMB ?? 0) * 1024 * 1024
  const tps = r?.tps
  const word = lagWord(r?.lag)
  return (
    <Card className="relative transition-[box-shadow,border-color] duration-(--motion-fast) ease-standard focus-within:border-primary/40 hover:border-primary/40 hover:shadow-lift">
      <div className="flex items-center justify-between gap-3">
        <CardTitle className="max-sm:text-[17px]">
          <a {...linkProps({ name: 'server', slug: s.slug, tab: 'overview', page: 'running' })} className="outline-none after:absolute after:inset-0 after:rounded-3xl focus-visible:after:ring-2 focus-visible:after:ring-ring">
            {t('overview.running')}
          </a>
        </CardTitle>
        {ws.machine && (
          <a {...linkProps({ name: 'machine', id: ws.machine.id })} className="relative z-10 inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline max-sm:text-[15px]">
            {ws.machineName}
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
          {!phone && (
            <dl className="mt-auto grid grid-cols-3 gap-3 pt-4 text-xs">
              <div>
                <dt className="text-muted-foreground">{t('overview.uptime')}</dt>
                <dd className="mt-0.5 text-[13px] font-semibold">{s.startedAt ? formatSpan((Date.now() - new Date(s.startedAt).getTime()) / 1000) : '—'}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">{t('overview.tickRate')}</dt>
                <dd className={cn('mt-0.5 text-[13px] font-semibold tabular-nums', word?.behind && 'text-warning-foreground')}>
                  {tps !== undefined ? formatTPS(tps) : '—'}
                  {tps !== undefined && word && (word.behind ? `${t('common.dot')}${word.text}` : <span className="ml-1.5 text-xs font-normal text-muted-foreground">{word.text}</span>)}
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

type ConsoleLine = { time?: string; level?: string; text: string }

/**
 * Console lines on the dark panel: the time, WARN and ERROR in colour, then
 * the text. Aligned lines keep the text in one column and band warnings.
 */
function ConsoleLines({ lines, levels = true, aligned = false, className }: { lines: ConsoleLine[]; levels?: boolean; aligned?: boolean; className?: string }) {
  return (
    <div className={cn('overflow-hidden rounded-xl bg-console px-3 py-2.5 font-mono text-xs leading-5 text-[#e8e8e0]', className)}>
      {lines.map((l, i) => {
        const warn = l.level === 'WARN'
        const error = l.level === 'ERROR' || l.level === 'FATAL'
        return (
          <div key={i} className={cn('flex gap-3 truncate', aligned && warn && '-mx-1.5 rounded-md bg-white/[0.06] px-1.5')}>
            {l.time && <span className="shrink-0 text-[#a3a89c]">{l.time}</span>}
            {levels && (warn || error) && <span className={cn('shrink-0 font-semibold', aligned && 'w-10', warn ? 'text-[#f5b94a]' : 'text-[#f87171]')}>{l.level}</span>}
            {levels && aligned && !warn && !error && <span className="w-10 shrink-0" aria-hidden="true" />}
            <span className={cn('truncate', warn && 'text-[#f5b94a]', error && 'text-[#f87171]')}>{l.text}</span>
          </div>
        )
      })}
    </div>
  )
}

export function ConsoleTail({ lines, className }: { lines: string[]; className?: string }) {
  return <ConsoleLines lines={lines.map(parseLine)} className={className} />
}

function useTail(server: ServerStatus, count: number, every: number) {
  const logs = usePoll(() => get<LogsResponse>(serverApi(server.id, `/logs?limit=${count}`)), every, server.id)
  return logs.data?.lines.map((l) => l.text) ?? []
}

function SettingUpView({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const tail = useTail(s, 3, 2000)
  const op = s.operation ?? s.lastOperation
  const failed = !s.operation && op?.status === 'failed'
  const [busy, setBusy] = useState(false)
  const cfg = s.config
  const type = typeName(s.type)
  const version = cfg?.minecraftVersion ?? ''
  const pack = cfg?.modpack
  const addonsDone = Number(op?.detail?.addons ?? 0)
  const addonsTotal = Number(op?.detail?.addonsTotal ?? 0)
  const packsDone = Number(op?.detail?.packs ?? 0)
  const packsTotal = Number(op?.detail?.packsTotal ?? 0)
  const onlyPacks = packsTotal > 0 && addonsTotal === 0
  // A template's add-ons and data packs install on the first start; the detail stays once they're in.
  const tpl = !pack && !!cfg?.template && (!!cfg.template.pending || addonsTotal > 0 || packsTotal > 0)
  // The pack's own steps only show in the operation's phase.
  const at = pack ? packStepOf(op?.phase ?? s.phase) : tpl ? templateStepOf(op?.phase ?? s.phase) : createStepOf(failed ? (op?.phase ?? '') : s.phase)
  const state = (i: number): StepState => (i < at ? 'done' : i === at ? (failed ? 'failed' : 'current') : 'todo')
  const pct = /(\d{1,3})\s*%/.exec(s.phaseDetail ?? '')?.[1]
  const other = (ws.servers ?? []).find((o) => o.id !== s.id)
  const disk = ws.machine?.live?.diskFreeBytes
  // Until the pack itself is read, the config holds the recommended loader, not the pack's.
  const packRead = !pack?.pending || !['', 'pulling_image', 'preparing_modpack'].includes(op?.phase ?? '')
  const loader = packRead ? (cfg?.software?.fabricLoader ?? cfg?.software?.quiltLoader) : undefined
  const done = Number(op?.detail?.packFiles ?? 0)
  const total = Number(op?.detail?.packFilesTotal ?? 0)
  // Only a finished download has matched its checksum.
  const checked = state(1) === 'done'
  const software = {
    title: loader ? t(at > 1 ? 'creating.downloadedPack' : 'creating.downloadingPack', { type, version, loader: loaderLabel(s.type, loader) }) : at > 1 ? t('creating.downloaded', { type, version }) : t('creating.downloading', { type, version }),
    hint: checked ? t(loader ? 'creating.downloadedPackDetail' : 'creating.downloadedDetail') : undefined,
    state: state(1),
  }
  const starting = (i: number) => ({ title: t('creating.starting'), hint: pct ? t('creating.startingPercent', { percent: pct }) : t('creating.startingDetail'), state: state(i), progress: pct ? Number(pct) : undefined })
  const mods = addonKind(s.type) === 'mods'
  const skippedDetail = op?.detail?.skipped
  const skipped = Array.isArray(skippedDetail) ? (skippedDetail as AddonNotice[]).map((n) => n.params?.name ?? n.message) : []
  const [fetched, fetching] = onlyPacks ? [packsDone, packsTotal] : [addonsDone, addonsTotal]
  const addonsHint = [fetching ? t('creating.packFiles', { done: fetched, total: fetching }) : '', skipped.length ? t('creating.templateSkipped', { count: skipped.length, names: skipped.join(', ') }) : ''].filter(Boolean).join(t('common.dot'))
  const steps = tpl
    ? [
        { title: t('creating.checked', { machine: ws.machineName }), hint: t('creating.checkedDetail', { memory: formatMB(cfg?.memoryMB ?? 0), disk: formatBytes(disk) }), state: state(0) },
        { ...software, title: at > 1 ? t('creating.downloaded', { type, version }) : t('creating.downloading', { type, version }), hint: checked ? t('creating.downloadedDetail') : undefined },
        {
          title: t(at > 2 ? (onlyPacks ? 'creating.templatePacksDone' : mods ? 'creating.templateModsDone' : 'creating.templatePluginsDone') : onlyPacks ? 'creating.templatePacks' : mods ? 'creating.templateMods' : 'creating.templatePlugins'),
          hint: addonsHint || undefined,
          state: state(2),
          progress: at === 2 && fetching ? (fetched / fetching) * 100 : undefined,
        },
        starting(3),
        { title: t('creating.reachable', { port: s.gamePort }), state: state(4) },
      ]
    : pack
    ? [
        { title: t('creating.checked', { machine: ws.machineName }), hint: t('creating.checkedDetail', { memory: formatMB(cfg?.memoryMB ?? 0), disk: formatBytes(disk) }), state: state(0) },
        software,
        { title: t(at > 2 ? 'creating.packModsDone' : 'creating.packMods'), hint: total ? t('creating.packFiles', { done, total }) : undefined, state: state(2), progress: at === 2 && total ? (done / total) * 100 : undefined },
        starting(3),
        { title: t('creating.reachable', { port: s.gamePort }), state: state(4) },
      ]
    : [
        { title: t('creating.checked', { machine: ws.machineName }), hint: t('creating.checkedDetail', { memory: formatMB(cfg?.memoryMB ?? 0), disk: formatBytes(disk) }), state: state(0) },
        { ...software, title: at > 1 ? t('creating.downloaded', { type, version }) : t('creating.downloading', { type, version }), hint: checked ? t('creating.downloadedDetail') : undefined },
        starting(2),
        { title: t('creating.reachable', { port: s.gamePort }), state: state(3) },
      ]
  return (
    <Card className="mx-auto w-full max-w-[520px] p-6 max-sm:p-4">
      <div className="flex items-start gap-4">
        <Pip pose={failed ? 'hurt' : 'hardhat'} size={64} />
        <div className="min-w-0 pt-1">
          <h2 className="text-lg font-bold">{failed ? t('creating.failedTitle', { server: s.name }) : t('creating.title', { server: s.name })}</h2>
          <p className="mt-1 text-[13px] leading-[18px] text-muted-foreground">{failed ? (op?.error ?? '') : t(pack ? 'creating.leadPack' : 'creating.lead')}</p>
          {failed && op?.hint && <p className="mt-1 text-[13px] text-muted-foreground">{op.hint}</p>}
        </div>
      </div>
      <div className="mt-5 border-t border-border pt-5">
        <JobSteps steps={steps} />
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

/** Without a diagnosis (a start that failed before the server ran), the agent's error and a fresh start. */
function fallbackCrash(s: ServerStatus): Crash {
  return { at: s.stoppedAt ?? '', start: false, kind: 'unknown', certain: false, title: '', explanation: '', evidence: [], fixes: [{ kind: 'restart', title: '' }], lines: [], roomMB: 0 }
}

/**
 * What the library says about the crash's update and install fixes, asked
 * once per crash; empty while it's asked, and without such fixes.
 */
function useAddonLookups(s: ServerStatus): AddonLookups {
  const keys = (s.crash?.fixes ?? []).flatMap((f) => lookupKey(f) ?? []).join('\n')
  const at = keys ? `${s.id}:${s.crash?.at}:${keys}` : ''
  const minecraft = s.config?.minecraftVersion ?? ''
  const [found, setFound] = useState<{ at: string; lookups: AddonLookups }>()
  useEffect(() => {
    if (!at) return
    let stale = false
    void lookUpAddonFixes(s.id, keys.split('\n'), minecraft).then((lookups) => !stale && setFound({ at, lookups }))
    return () => {
      stale = true
    }
  }, [at, s.id, keys, minecraft])
  return found?.at === at ? found.lookups : {}
}

/** Why the server stopped or didn't start, and fixes that act. */
function CrashedView({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const refusal = s.refusal
  const logs = usePoll(() => (s.crash || refusal ? Promise.resolve(undefined) : get<LogsResponse>(serverApi(s.id, '/logs?limit=3'))), 10_000, `${s.id}:${s.crash || refusal ? 'crash' : 'tail'}`)
  const [picked, setPicked] = useState<string>()
  const [preview, setPreview] = useState<RestorePreview>()
  const [busy, setBusy] = useState(false)
  const summary = refusal ? refusalLine(refusal, s.name) : s.crash ? crashSummary(s.crash, s.name, ws.machineName) : (s.lastError ?? t('crash.generic', { server: s.name }))
  const detail = refusal ? undefined : s.crash ? crashDetail(s.crash) : s.lastErrorHint
  const lines: ConsoleLine[] = refusal ? [] : s.crash ? s.crash.lines : (logs.data?.lines ?? []).map((l) => parseLine(l.text))
  const lookups = useAddonLookups(s)
  const options = refusal ? refusalFixes(refusal, s.name) : crashFixes(s.crash ?? fallbackCrash(s), s.name, ws.machineName, phone, new Date(), lookups)
  const choice = options.find((o) => o.id === picked && o.plan) ?? preselect(options)

  async function act() {
    const plan = choice?.plan
    if (!plan) return
    setBusy(true)
    try {
      switch (plan.kind) {
        case 'settings':
          await post(serverApi(s.id, '/settings'), plan.body)
          await post(serverApi(s.id, '/start'))
          break
        case 'start':
          await post(serverApi(s.id, '/start'))
          break
        case 'remove-addon':
          await post(serverApi(s.id, '/addons/remove-file'), { jar: plan.jar, start: true })
          break
        case 'update-addon':
          await post(serverApi(s.id, '/addons/update'), { addons: [plan.key], fingerprint: plan.fingerprint, start: true })
          break
        case 'install-addon':
          await post(serverApi(s.id, '/addons/install'), { ...plan.key, fingerprint: plan.fingerprint, start: true })
          break
        case 'restore':
          setPreview(await post<RestorePreview>(serverApi(s.id, `/backups/${encodeURIComponent(plan.backupId)}/restore`)))
          return
        case 'delete-backups':
          for (const id of plan.ids) await del(serverApi(s.id, `/backups/${encodeURIComponent(id)}`))
          await post(serverApi(s.id, '/start'))
          break
        default: {
          const unhandled: never = plan
          void unhandled
        }
      }
      await ws.refresh()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  const button = (
    <Button className={cn('w-full', phone && 'text-base')} size={phone ? 'touch' : 'lg'} loading={busy} onClick={act} disabledReason={whyNot(s, 'start', ws.stale)}>
      <PlayIcon />
      {choice?.button}
    </Button>
  )
  const dialog = <RestoreDialog preview={preview} server={s} onClose={() => setPreview(undefined)} />

  if (phone) {
    return (
      <div className="flex flex-col gap-5 pb-20">
        <Card className="p-4">
          <div className="flex items-start gap-3.5">
            <Pip pose="hurt" size={56} />
            <div className="min-w-0 pt-1">
              <h2 className="text-[17px] leading-6 font-bold">{t('crash.what')}</h2>
              <p className="mt-0.5 text-[15px] leading-5">{summary}</p>
              {detail && <p className="mt-1 text-[13px] text-muted-foreground">{detail}</p>}
            </div>
          </div>
          {lines.length > 0 && <ConsoleLines lines={phoneLines(lines)} levels={false} className="mt-3.5 leading-6" />}
        </Card>
        <section aria-labelledby="crash-fix">
          <SectionLabel className="px-4">
            <span id="crash-fix">{t('crash.fix')}</span>
          </SectionLabel>
          <CardGroup value={choice?.id ?? ''} onChange={setPicked} label={t('crash.fix')} className="mt-2 overflow-hidden rounded-3xl border border-border bg-white">
            {options.map((o) => (
              <ChoiceCard
                key={o.id}
                value={o.id}
                disabled={!o.plan}
                reason={o.reason}
                className="min-h-16 items-center gap-3 rounded-none border-0 border-b border-border bg-transparent px-4 py-3 shadow-none last:border-b-0 hover:border-border has-[[data-checked]]:border-border has-[[data-checked]]:bg-transparent has-[[data-checked]]:shadow-none has-[[data-disabled]]:bg-transparent"
              >
                <span className={cn('block text-base', !o.plan && 'text-muted-foreground')}>{o.title}</span>
                <span className="block text-[13px] text-muted-foreground">{o.reason ?? [o.recommended && o.plan ? t('common.recommended') : '', o.hint ?? ''].filter(Boolean).join(t('common.dot'))}</span>
              </ChoiceCard>
            ))}
          </CardGroup>
          {choice?.footnote && <p className="mt-3 px-4 text-[13px] text-muted-foreground">{choice.footnote}</p>}
        </section>
        <div className="fixed inset-x-4 bottom-[calc(80px+env(safe-area-inset-bottom))] z-20">{button}</div>
        {dialog}
      </div>
    )
  }

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1.45fr)_minmax(0,1fr)]">
      <Card className="min-w-0 p-6">
        <div className="flex items-start gap-5">
          <Pip pose="hurt" size={72} />
          <div className="min-w-0 pt-2">
            <h2 className="text-lg font-bold">{t('crash.what')}</h2>
            <p className="mt-1 text-sm">{summary}</p>
            {detail && <p className="mt-2 text-[13px] text-muted-foreground">{detail}</p>}
          </div>
        </div>
        {lines.length > 0 && (
          <div className="mt-auto pt-6">
            <div className="text-xs font-semibold">{t('crash.lastLines')}</div>
            <ConsoleLines lines={lines} aligned className="mt-2 leading-6" />
          </div>
        )}
      </Card>
      <Card>
        <CardTitle>{t('crash.fix')}</CardTitle>
        <CardGroup value={choice?.id ?? ''} onChange={setPicked} label={t('crash.fix')} className="mt-3 flex flex-col gap-2.5">
          {options.map((o) => (
            <ChoiceCard key={o.id} value={o.id} radio="start" disabled={!o.plan} reason={o.reason} className="gap-3 p-3.5">
              <span className={cn('text-sm font-semibold', !o.plan && 'text-muted-foreground')}>{o.title}</span>
              {o.recommended && o.plan && <span className="ml-2 text-xs text-muted-foreground">{t('common.recommended')}</span>}
              {(o.reason ?? o.hint) && <span className="mt-0.5 block text-xs text-muted-foreground">{o.reason ?? o.hint}</span>}
            </ChoiceCard>
          ))}
        </CardGroup>
        <div className="mt-auto pt-4">{button}</div>
        {choice?.footnote && <p className="mt-3 text-center text-xs text-muted-foreground">{choice.footnote}</p>}
      </Card>
      {dialog}
    </div>
  )
}

function AgentDownView() {
  const ws = useWorkspace()
  const [busy, setBusy] = useState(false)
  const command = t('agentDown.command')
  return (
    <div className="flex flex-1 flex-col items-center py-8 text-center max-sm:py-2">
      <Pip pose="search" size={96} />
      <h2 className="mt-4 text-xl font-bold max-sm:text-lg">{t('agentDown.title')}</h2>
      <p className="mt-2 max-w-[520px] text-sm text-muted-foreground">
        {ws.lastSeenAt ? t('agentDown.bodySince', { machine: ws.machineName, time: relativeTime(new Date(ws.lastSeenAt).toISOString()) }) : t('agentDown.body', { machine: ws.machineName })}
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
        <CardTitle>{t('agentDown.fix')}</CardTitle>
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

