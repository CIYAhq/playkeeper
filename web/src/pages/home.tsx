import { useState, type ReactNode } from 'react'
import { ArrowRightIcon, CircleAlertIcon, LinkIcon, PlayIcon, PlusIcon, ServerIcon, ShieldCheckIcon, XIcon } from 'lucide-react'
import { useCatalog } from '@/api/catalog'
import { get, post } from '@/api/client'
import type { Activity, CatalogEntry, MachineView, ProjectRole, ServerStatus, TeamResponse } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { ActivityList } from '@/components/app/activity'
import { Emblem, Pip } from '@/components/app/art'
import { Card, CardHint, CardTitle, CopyButton, Elapsed, MeterRow, Notice, PlayerFace, Spinner, StatusPill } from '@/components/app/bits'
import { EmptySteps } from '@/components/app/checklist'
import { useIsPhone } from '@/components/app/controls'
import { PageBody, PageHeader, PhoneMoreButton } from '@/components/app/shell'
import { SignInNotice } from '@/components/app/sign-in-notice'
import { InlineSkeleton, LoadingLabel, MeterSkeleton } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { rich } from '@/i18n/rich'
import { can, welcomeKey } from '@/lib/access'
import { demo } from '@/lib/demo'
import { formatBytes, formatDate, formatList, formatMB, formatPercent, formatSpan, sameDay } from '@/lib/format'
import { awayLong, awayOf, byMachine, isAway, isStale, joinOf, machineLabel, machineOf, machineRoute, machineState, reachOf } from '@/lib/machines'
import { couldntStart, isSettingUp, phaseLabel, phaseTone, statusTone } from '@/lib/phase'
import { presenceProps, useListPresence } from '@/lib/presence'
import { linkPath, linkProps } from '@/lib/router'
import { iconURL, newerStable, playersOnline, softwareLabel } from '@/lib/servers'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { AsleepDetail, gaveBackText } from '@/pages/server/sleep'
import { ConfirmAdminNotice } from './team'

export function HomePage() {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const servers = ws.servers
  const machine = ws.machine
  const { catalog } = useCatalog(machine?.id)
  const activity = usePoll(recentActivity, 10000)
  const grouped = ws.machines.length > 1
  const sections = useListPresence(grouped ? ws.machines : undefined, machineKey)

  const create = can(ws.me, 'servers.create')
  const newButton = create && (
    <Button render={<a {...linkProps({ name: 'new-server' })} />}>
      <PlusIcon />
      {t('nav.newServer')}
    </Button>
  )

  if (servers && servers.length === 0) {
    return (
      <>
        <PageHeader title={t('home.title')} subtitle={phone ? undefined : t('home.emptySubtitle', { machine: ws.machineName })} phoneAction={<PhoneMoreButton />} />
        <PageBody className="flex flex-1 flex-col items-center pt-10 text-center max-sm:pt-0">
          <Pip pose="wave" size={phone ? 104 : 96} />
          <h2 className="mt-4 text-title font-extrabold tracking-[-0.015em]">{t('home.emptyTitle')}</h2>
          <p className="mt-2 max-w-[420px] text-sm text-muted-foreground max-sm:text-[15px]">{create ? t('home.emptyBody') : t('home.emptyMember')}</p>
          {create &&
            (phone ? (
              <Button size="touch" className="mt-6 w-full" render={<a {...linkProps({ name: 'new-server' })} />}>
                <PlusIcon />
                {t('checklist.create')}
              </Button>
            ) : (
              <div className="mt-5">{newButton}</div>
            ))}
          {can(ws.me, 'backups.recover') && (
            <p className="mt-3 text-xs text-muted-foreground max-sm:text-[13px]">
              {rich('recover.homeLink', {
                recover: (chunk) => (
                  <a {...linkProps({ name: 'recover' })} className="font-medium text-success-strong hover:underline">
                    {chunk}
                  </a>
                ),
              })}
            </p>
          )}
          {create && <EmptySteps phone={phone} />}
        </PageBody>
      </>
    )
  }

  const count = servers && (grouped ? t('machines.home.subtitle', { count: servers.length, machines: ws.machines.length }) : t('home.servers', { count: servers.length, machine: ws.machineName }))
  const subtitle = !servers ? <InlineSkeleton className="w-56" /> : ws.stale ? count : `${count}${t('common.dot')}${t('home.playing', { count: playersOnline(servers.filter((s) => !s.lastKnownAt)) })}`
  return (
    <>
      <PageHeader title={t('home.title')} subtitle={demo ? demo.homeSubtitle() : subtitle} actions={demo ? <demo.HomeAction /> : newButton} phoneAction={<PhoneMoreButton />} />
      <PageBody className="flex flex-col gap-4">
        <HomeNotice />
        {grouped && servers ? (
          sections.map(({ key, item: m, state }) => (
            <section key={key} {...presenceProps(state)} aria-labelledby={`on-${m.id}`} className="flex flex-col gap-3">
              <MachineHeading machine={m} />
              <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
                {byMachine(servers, ws.machines)
                  .find((g) => g.machine.id === m.id)
                  ?.servers.map((s) => <ServerCard key={s.id} server={s} update={newerStable(s.config, catalog?.versions)} />)}
                <NewServerCard machine={m} />
              </div>
            </section>
          ))
        ) : (
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            {servers ? servers.map((s) => <ServerCard key={s.id} server={s} update={newerStable(s.config, catalog?.versions)} />) : [0, 1].map((i) => <ServerCardSkeleton key={i} />)}
            {demo ? <demo.HomeCard /> : <NewServerCard />}
          </div>
        )}
        <div className="grid gap-4 lg:grid-cols-[1.6fr_1fr]">
          <Card>
            <CardTitle>{t('home.activityTitle')}</CardTitle>
            <ActivityList items={activity.data} servers={servers ?? []} empty={t('home.activityEmpty')} className="mt-3 flex-1" />
            {can(ws.me, 'audit.view') && (
              <a {...linkPath('/settings#audit')} className="mt-4 inline-flex items-center gap-1 self-start text-xs font-medium text-primary hover:underline">
                {t('home.auditLink')}
                <ArrowRightIcon className="size-3.5" aria-hidden="true" />
              </a>
            )}
          </Card>
          <MachineCard />
        </div>
      </PageBody>
    </>
  )
}

const machineKey = (m: MachineView) => m.id

/** The latest activity on the machines that answer, newest first, with each team join once. */
function recentActivity(): Promise<Activity[]> {
  return get<Activity[]>('/api/activity?limit=5')
}

/**
 * Home's one notice: the agent not answering, what to know after signing
 * in, or disk space; else what a team member should know.
 */
function HomeNotice() {
  const ws = useWorkspace()
  const disk = ws.machine?.live?.diskWarning
  if (ws.agentDown) return <Notice tone="error" title={t('agentDown.title')}>{t('agentDown.body', { machine: ws.machineName })}</Notice>
  if (ws.signInNotice) return <SignInNotice />
  if (disk) return <Notice tone={disk.status === 'fail' ? 'error' : 'warning'} title={t('overview.lowDiskTitle', { detail: disk.detail })}>{disk.fix}</Notice>
  return can(ws.me, 'team.manage') ? <TeamNotice /> : <MemberNotice />
}

/** For whoever can confirm them, a member waiting for their Admin rights comes first. */
function TeamNotice() {
  const team = usePoll(() => get<TeamResponse>('/api/team'), 15_000)
  const waiting = team.data?.members.find((m) => m.canConfirm)
  return waiting ? <ConfirmAdminNotice member={waiting} onConfirmed={team.refresh} /> : <MemberNotice />
}

/** Admin rights waiting for two-factor sign-in or a confirmation, or the welcome after joining with an invite link. */
function MemberNotice() {
  const ws = useWorkspace()
  const [dismissed, setDismissed] = useState(false)
  const access = ws.me.access
  if (access.needsTwoFactor) {
    const turnOn = (
      <Button variant="outline" size="sm" render={<a {...linkPath('/account/two-factor')} />}>
        <ShieldCheckIcon />
        {t('home.turnItOn')}
      </Button>
    )
    return <TwoLineNotice tone="warning" title={t('home.adminLaterTitle')} body={t('home.adminLaterBody')} action={turnOn} />
  }
  if (access.awaitingConfirmation) return <TwoLineNotice tone="warning" title={t('home.adminWaitingTitle')} body={t('home.adminLaterBody')} />
  if (dismissed || ws.prefs[welcomeKey] !== '1' || !ws.servers?.length) return null
  const name = ws.me.user.username
  const servers = access.servers.all ? t('home.welcomeAllServers') : formatList(ws.servers.map((s) => s.name))
  const close = (
    <Button
      variant="ghost"
      size="icon-sm"
      aria-label={t('common.dismiss')}
      onClick={() => {
        setDismissed(true)
        void ws.setPrefs({ [welcomeKey]: '' }).catch(() => undefined)
      }}
    >
      <XIcon />
    </Button>
  )
  return <TwoLineNotice pip title={access.team ? t('home.welcome', { team: access.team, name }) : t('home.welcomeAny', { name })} body={welcomeLine(access.role, servers)} action={close} />
}

function welcomeLine(role: ProjectRole, servers: string): string {
  switch (role) {
    case 'admin':
      return t('home.welcomeAdmin', { servers })
    case 'moderator':
      return t('home.welcomeModerator', { servers })
    case 'viewer':
      return t('home.welcomeViewer', { servers })
    default: {
      const unreachable: never = role
      return unreachable
    }
  }
}

function TwoLineNotice({ title, body, action, tone = 'default', pip }: { title: string; body: string; action?: ReactNode; tone?: 'default' | 'warning'; pip?: boolean }) {
  return (
    <div className="flex animate-enter items-center gap-3" role="status">
      {pip && <Pip pose="wave" size={40} />}
      <div className="min-w-0 flex-1">
        <p className={cn('text-[13px] leading-5 font-semibold', tone === 'warning' && 'text-warning-foreground')}>{title}</p>
        <p className="text-xs leading-4 text-muted-foreground max-sm:text-[13px] max-sm:leading-[18px]">{body}</p>
      </div>
      {action}
    </div>
  )
}

async function startServer(s: ServerStatus) {
  try {
    await post(serverApi(s.id, '/start'))
  } catch (e) {
    toastManager.add({ title: errorText(e), type: 'error' })
  }
}

function StartButton({ server, label }: { server: ServerStatus; label: string }) {
  const [busy, setBusy] = useState(false)
  const { me } = useWorkspace()
  if (!can(me, 'servers.run')) return null
  return (
    <Button
      size="sm"
      className="relative z-10 ml-auto"
      loading={busy}
      onClick={async () => {
        setBusy(true)
        await startServer(server)
        setBusy(false)
      }}
    >
      <PlayIcon />
      {label}
    </Button>
  )
}

function CardDetail({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const away = awayOf(reachOf(s, ws))
  if (away) return <span className="text-[13px] text-muted-foreground">{t('machines.away.pill', { name: away.name })}</span>
  if (isStale(s, ws.stale) || reachOf(s, ws).state !== 'live') return <span className="text-[13px] text-muted-foreground">{t('status.noLive')}</span>
  if (isSettingUp(s) && s.operation) {
    return (
      <span className="flex items-center gap-2 text-[13px] text-info-foreground">
        <Spinner />
        {t('status.settingUp')}
        {t('common.dot')}
        <Elapsed since={s.operation.startedAt} />
      </span>
    )
  }
  const tone = statusTone(s)
  switch (tone) {
    case 'online': {
      const names = s.players?.names ?? []
      if (!s.players?.online) return <span className="text-[13px] text-muted-foreground">{t('card.nobodyOn')}</span>
      return (
        <span className="flex items-center gap-2.5 text-[13px] text-muted-foreground">
          <span className="flex gap-1">
            {names.slice(0, 3).map((n) => (
              <PlayerFace key={n} name={n} size={28} />
            ))}
          </span>
          {t('status.playing', { count: s.players.online })}
        </span>
      )
    }
    case 'crashed':
      return (
        <>
          <span className="flex items-center gap-2 text-[13px] text-destructive-foreground">
            <CircleAlertIcon className="size-4" aria-hidden="true" />
            {couldntStart(s) ? t('status.couldntStart') : t('card.crashed')}
          </span>
          {!s.operation && <StartButton server={s} label={t('server.startAgain')} />}
        </>
      )
    case 'busy':
      return (
        <span className="flex items-center gap-2 text-[13px] text-info-foreground">
          <Spinner />
          {phaseLabel(s.phase)}
        </span>
      )
    case 'stopped':
    case 'unknown':
      if (s.phase === 'asleep') return <AsleepDetail server={s} />
      return (
        <>
          <Pip pose="sleep" size={40} />
          <span className="text-[13px] text-muted-foreground">{s.stoppedAt ? t('card.napping', { duration: formatSpan((Date.now() - new Date(s.stoppedAt).getTime()) / 1000) }) : t('card.stoppedNever')}</span>
          {s.exists && !s.operation && s.phase === 'stopped' && <StartButton server={s} label={t('card.start')} />}
        </>
      )
    default: {
      const unreachable: never = tone
      return unreachable
    }
  }
}

function ServerCard({ server: s, update }: { server: ServerStatus; update?: CatalogEntry }) {
  const ws = useWorkspace()
  const reach = reachOf(s, ws)
  const stale = isStale(s, ws.stale) || reach.state !== 'live'
  const join = joinOf(s, machineOf(s, ws.machines))
  const stopped = phaseTone(s.phase) !== 'online'
  return (
    <article className="relative flex flex-col gap-3.5 rounded-3xl border border-border bg-card p-4 shadow-card transition-[box-shadow,border-color] focus-within:border-primary/40 hover:border-primary/40 hover:shadow-lift">
      <div className="flex items-start gap-3">
        <Emblem size={40} stopped={stopped} icon={iconURL(s)} name={s.name} />
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-[17px] leading-[22px] font-bold">
            <a {...linkProps({ name: 'server', slug: s.slug, tab: 'overview' })} className="outline-none after:absolute after:inset-0 after:rounded-3xl focus-visible:after:ring-2 focus-visible:after:ring-ring">
              {s.name}
            </a>
          </h2>
          <p className="mt-0.5 flex items-center gap-1.5 text-[13px] text-muted-foreground">
            {softwareLabel(s)}
            {update && (
              <span className="size-1.5 rounded-full bg-success" role="img" aria-label={t('card.updateDot')}>
                <span className="sr-only">{t('card.updateDot')}</span>
              </span>
            )}
          </p>
        </div>
        <StatusPill server={s} agentDown={stale} showDetail={false} />
      </div>
      <div className="flex h-11 items-center gap-3">
        <CardDetail server={s} />
      </div>
      <div className={cn('relative z-10 flex h-10 items-center gap-2 rounded-lg bg-muted pl-3 text-sm font-semibold', join.address ? 'pr-1' : 'pr-3')}>
        <LinkIcon className="size-4 text-muted-foreground" aria-hidden="true" />
        {join.address ? (
          <>
            <span className="min-w-0 flex-1 truncate">{join.address}</span>
            <CopyButton text={join.address} size="xs" className="bg-white" />
          </>
        ) : (
          <span className="min-w-0 flex-1 truncate font-normal text-muted-foreground" title={join.reason}>
            {join.reason}
          </span>
        )}
      </div>
    </article>
  )
}

/** A server card while the list loads, in the same box as the real one. */
function ServerCardSkeleton() {
  return (
    <div className="flex flex-col gap-3.5 rounded-3xl border border-border bg-card p-4 shadow-card">
      <LoadingLabel />
      <div className="flex items-start gap-3">
        <Skeleton className="size-10 rounded-xl" />
        <div className="flex min-w-0 flex-1 flex-col gap-2 pt-0.5">
          <Skeleton className="h-4 w-28" />
          <Skeleton className="h-3 w-20" />
        </div>
        <Skeleton className="h-[26px] w-20 rounded-full" />
      </div>
      <div className="flex h-11 items-center">
        <Skeleton className="h-3 w-32" />
      </div>
      <Skeleton className="h-10 rounded-lg" />
    </div>
  )
}

/** The dashed card that starts a new server: on the dashboard's machine, or on the machine given. */
function NewServerCard({ machine }: { machine?: MachineView }) {
  const ws = useWorkspace()
  const m = machine ?? ws.machine
  const live = m?.live
  const name = m && m.kind === 'remote' ? machineLabel(m) : ws.machineName
  const full = !!live && live.memoryFreeMB <= 0
  if (!can(ws.me, 'servers.create')) return null
  const cls = 'flex min-h-[176px] flex-col items-center justify-center rounded-3xl border border-dashed border-input bg-warm p-4 text-center outline-none'
  if (machine && isAway(machine)) {
    return (
      <div className={cn(cls, 'text-muted-foreground')} aria-disabled="true">
        <PlusIcon className="size-5" aria-hidden="true" />
        <span className="mt-3 text-[15px] font-semibold">{t('machines.newServerHere')}</span>
        <span className="mt-1 text-xs">{t('machines.away.pill', { name })}</span>
      </div>
    )
  }
  return (
    <a {...linkProps(machine ? { name: 'new-server', machine: machine.id } : { name: 'new-server' })} className={cn(cls, 'hover:border-primary/50 focus-visible:ring-2 focus-visible:ring-ring')}>
      <PlusIcon className="size-5 text-primary" aria-hidden="true" />
      <span className="mt-3 text-[15px] font-semibold">{machine ? t('machines.newServerHere') : t('nav.newServer')}</span>
      {live && <span className="mt-1 text-xs text-muted-foreground">{full ? t('home.newServerFull', { machine: name }) : t('home.newServerFree', { memory: formatMB(live.memoryFreeMB), machine: name })}</span>}
    </a>
  )
}

/** "On home-server  Ubuntu 24.04 · 32 GB · connected today" above a machine's servers. */
function MachineHeading({ machine: m }: { machine: MachineView }) {
  const ws = useWorkspace()
  const now = Date.now()
  const name = m.kind === 'local' ? ws.machineName : machineLabel(m)
  const link = m.link
  let word: string
  if (m.kind === 'local') word = machineState(m, ws).tone === 'good' ? t('machines.home.healthy') : ws.agentDown ? t('machines.home.notAnswering') : t('status.docker')
  else if (link?.state === 'connected') {
    if (m.error || !m.live) word = t('machines.home.notAnswering')
    else if (!link.connectedAt) word = t('machines.home.healthy')
    else word = sameDay(new Date(link.connectedAt), new Date(now)) ? t('machines.home.connectedToday') : t('machines.home.connectedOn', { date: formatDate(link.connectedAt) })
  } else if (link?.state === 'waiting') word = t('machines.home.waiting')
  else word = link?.lastSeen ? t('machines.home.away', { duration: awayLong(link.lastSeen, now) }) : t('machines.offline')
  const meta = m.live ? [m.live.os, formatMB(m.live.memoryTotalMB), word] : [word]
  return (
    <h2 id={`on-${m.id}`} className="flex flex-wrap items-baseline gap-x-2 text-[15px] font-semibold">
      <ServerIcon className="size-4 shrink-0 self-center text-muted-foreground" aria-hidden="true" />
      <a {...linkProps(machineRoute(m))} className="rounded outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
        {t('machines.home.on', { name })}
      </a>
      <span key={word} className="animate-fade text-[13px] font-normal text-muted-foreground">
        {meta.join(t('common.dot'))}
      </span>
    </h2>
  )
}

function MachineCard() {
  const ws = useWorkspace()
  const m = ws.machine
  const live = m?.live
  const reserved = live ? live.systemReserveMB + live.serversMemoryMB : 0
  const gaveBack = gaveBackText(ws.servers?.filter((s) => machineOf(s, ws.machines)?.id === m?.id), live?.sleepingMemoryMB)
  const diskUsed = live?.diskTotalBytes && live.diskFreeBytes !== undefined ? ((live.diskTotalBytes - live.diskFreeBytes) / live.diskTotalBytes) * 100 : undefined
  return (
    <Card>
      <CardTitle>{ws.machineName}</CardTitle>
      {live && <CardHint>{t('home.machineMeta', { os: live.os, memory: formatMB(live.memoryTotalMB) })}</CardHint>}
      {live ? (
        <div className="mt-4 flex flex-col gap-4">
          <MeterRow label={t('home.memoryReserved')} value={[t('home.ofTotal', { used: formatMB(reserved), total: formatMB(live.memoryTotalMB) }), gaveBack].filter(Boolean).join(t('common.dot'))} percent={live.memoryTotalMB ? (reserved / live.memoryTotalMB) * 100 : 0} />
          <MeterRow label={t('home.cpu')} value={formatPercent(live.cpuPercent)} percent={live.cpuPercent} />
          <MeterRow label={t('home.disk')} value={t('home.diskFree', { free: formatBytes(live.diskFreeBytes) })} percent={diskUsed} />
        </div>
      ) : ws.agentDown ? (
        <p className="mt-3 flex-1 text-[13px] text-muted-foreground">{t('nav.notAnswering')}</p>
      ) : (
        <MeterSkeleton className="mt-4" />
      )}
      {m && (
        <a {...linkProps({ name: 'machine', id: m.id })} className="mt-auto inline-flex items-center gap-1 self-start pt-4 text-xs font-medium text-primary hover:underline">
          {t('home.machineDetails')}
          <ArrowRightIcon className="size-3.5" aria-hidden="true" />
        </a>
      )}
    </Card>
  )
}
