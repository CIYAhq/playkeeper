import { useState, type ReactNode } from 'react'
import { ArrowRightIcon, CircleAlertIcon, LinkIcon, PlayIcon, PlusIcon, ShieldCheckIcon, XIcon } from 'lucide-react'
import { useCatalog } from '@/api/catalog'
import { get, post } from '@/api/client'
import type { Activity, CatalogEntry, ProjectRole, ServerStatus, TeamResponse } from '@/api/types'
import { errorText, machineApi, serverApi, useWorkspace } from '@/api/workspace'
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
import { formatBytes, formatList, formatMB, formatPercent, formatSpan, serverJoinAddress } from '@/lib/format'
import { couldntStart, isSettingUp, phaseLabel, phaseTone, statusTone } from '@/lib/phase'
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
  const activity = usePoll<Activity[] | undefined>(() => (machine ? get<Activity[]>(machineApi(machine.id, '/activity?limit=5')) : Promise.resolve(undefined)), 10000, machine?.id ?? '')

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

  const count = servers && t('home.servers', { count: servers.length, machine: ws.machineName })
  const subtitle = !servers ? <InlineSkeleton className="w-56" /> : ws.stale ? count : `${count}${t('common.dot')}${t('home.playing', { count: playersOnline(servers) })}`
  return (
    <>
      <PageHeader title={t('home.title')} subtitle={subtitle} actions={newButton} phoneAction={<PhoneMoreButton />} />
      <PageBody className="flex flex-col gap-4">
        <HomeNotice />
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {servers ? servers.map((s) => <ServerCard key={s.id} server={s} update={newerStable(s.config, catalog?.versions)} />) : [0, 1].map((i) => <ServerCardSkeleton key={i} />)}
          <NewServerCard />
        </div>
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
  const { stale } = useWorkspace()
  if (stale) return <span className="text-[13px] text-muted-foreground">{t('status.noLive')}</span>
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
  const { stale } = useWorkspace()
  const address = serverJoinAddress(s)
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
      <div className="relative z-10 flex h-10 items-center gap-2 rounded-lg bg-muted pr-1 pl-3 text-sm font-semibold">
        <LinkIcon className="size-4 text-muted-foreground" aria-hidden="true" />
        <span className="min-w-0 flex-1 truncate">{address}</span>
        <CopyButton text={address} size="xs" className="bg-white" />
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

function NewServerCard() {
  const ws = useWorkspace()
  const live = ws.machine?.live
  const full = !!live && live.memoryFreeMB <= 0
  if (!can(ws.me, 'servers.create')) return null
  return (
    <a
      {...linkProps({ name: 'new-server' })}
      className={cn(
        'flex min-h-[176px] flex-col items-center justify-center rounded-3xl border border-dashed border-input bg-warm p-4 text-center outline-none hover:border-primary/50 focus-visible:ring-2 focus-visible:ring-ring',
      )}
    >
      <PlusIcon className="size-5 text-primary" aria-hidden="true" />
      <span className="mt-3 text-[15px] font-semibold">{t('nav.newServer')}</span>
      {live && <span className="mt-1 text-xs text-muted-foreground">{full ? t('home.newServerFull', { machine: ws.machineName }) : t('home.newServerFree', { memory: formatMB(live.memoryFreeMB), machine: ws.machineName })}</span>}
    </a>
  )
}

function MachineCard() {
  const ws = useWorkspace()
  const m = ws.machine
  const live = m?.live
  const reserved = live ? live.systemReserveMB + live.serversMemoryMB : 0
  const gaveBack = gaveBackText(ws.servers, live?.sleepingMemoryMB)
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
