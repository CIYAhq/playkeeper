import { useState } from 'react'
import { ArrowRightIcon, CircleAlertIcon, LinkIcon, PlayIcon, PlusIcon, ServerIcon } from 'lucide-react'
import { useCatalog } from '@/api/catalog'
import { get, post } from '@/api/client'
import type { Activity, CatalogEntry, MachineView, ServerStatus } from '@/api/types'
import { errorText, machineApi, serverApi, useWorkspace } from '@/api/workspace'
import { ActivityList } from '@/components/app/activity'
import { Emblem, Pip } from '@/components/app/art'
import { Card, CardHint, CardTitle, CopyButton, Elapsed, MeterRow, Notice, PlayerFace, Spinner, StatusPill } from '@/components/app/bits'
import { EmptySteps } from '@/components/app/checklist'
import { useIsPhone } from '@/components/app/controls'
import { PageBody, PageHeader, PhoneMoreButton } from '@/components/app/shell'
import { Button } from '@/components/ui/button'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { demo } from '@/lib/demo'
import { formatBytes, formatDate, formatMB, formatPercent, formatSpan, joinAddress, sameDay } from '@/lib/format'
import { awayLong, awayOf, byMachine, isAway, isStale, joinHost, machineLabel, machineOf, machineState, reachOf } from '@/lib/machines'
import { isSettingUp, phaseLabel, phaseTone } from '@/lib/phase'
import { linkPath, linkProps } from '@/lib/router'
import { iconURL, newerStable, playersOnline, softwareLabel } from '@/lib/servers'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

export function HomePage() {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const servers = ws.servers
  const machine = ws.machine
  const { catalog } = useCatalog(machine?.id)
  const reachable = ws.machines.filter((m) => !isAway(m)).map((m) => m.id)
  const activity = usePoll(() => recentActivity(reachable), 10000, reachable.join(' '))

  const newButton = (
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
          <p className="mt-2 max-w-[420px] text-sm text-muted-foreground max-sm:text-[15px]">{t('home.emptyBody')}</p>
          {phone ? (
            <Button size="touch" className="mt-6 w-full" render={<a {...linkProps({ name: 'new-server' })} />}>
              <PlusIcon />
              {t('checklist.create')}
            </Button>
          ) : (
            <div className="mt-5">{newButton}</div>
          )}
          <EmptySteps phone={phone} />
        </PageBody>
      </>
    )
  }

  const grouped = ws.machines.length > 1
  const players = playersOnline(servers?.filter((s) => !s.lastKnownAt))
  let subtitle: string | undefined
  if (servers && grouped) subtitle = `${t('machines.home.subtitle', { count: servers.length, machines: ws.machines.length })}${t('common.dot')}${t('machines.home.playing', { count: players })}`
  else if (servers) subtitle = `${t('home.servers', { count: servers.length, machine: ws.machineName })}${t('common.dot')}${t('home.playing', { count: players })}`
  return (
    <>
      <PageHeader title={t('home.title')} subtitle={demo ? demo.homeSubtitle() : subtitle} actions={demo ? <demo.HomeAction /> : newButton} phoneAction={<PhoneMoreButton />} />
      <PageBody className="flex flex-col gap-4">
        <MachineNotice />
        {grouped ? (
          byMachine(servers ?? [], ws.machines).map((g) => (
            <section key={g.machine.id} aria-labelledby={`on-${g.machine.id}`} className="flex flex-col gap-3">
              <MachineHeading machine={g.machine} />
              <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
                {g.servers.map((s) => (
                  <ServerCard key={s.id} server={s} update={newerStable(s.config, catalog?.versions)} />
                ))}
                <NewServerCard machine={g.machine} />
              </div>
            </section>
          ))
        ) : (
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            {(servers ?? []).map((s) => (
              <ServerCard key={s.id} server={s} update={newerStable(s.config, catalog?.versions)} />
            ))}
            {demo ? <demo.HomeCard /> : <NewServerCard />}
          </div>
        )}
        <div className="grid gap-4 lg:grid-cols-[1.6fr_1fr]">
          <Card>
            <CardTitle>{t('home.activityTitle')}</CardTitle>
            <ActivityList items={activity.data} servers={servers ?? []} empty={t('home.activityEmpty')} className="mt-3 flex-1" />
            <a {...linkPath('/settings#audit')} className="mt-4 inline-flex items-center gap-1 self-start text-xs font-medium text-primary hover:underline">
              {t('home.auditLink')}
              <ArrowRightIcon className="size-3.5" aria-hidden="true" />
            </a>
          </Card>
          <MachineCard />
        </div>
      </PageBody>
    </>
  )
}

/** The latest activity on the machines that answer, newest first. */
async function recentActivity(machines: string[]): Promise<Activity[]> {
  const got = await Promise.allSettled(machines.map((id) => get<Activity[]>(machineApi(id, '/activity?limit=5'))))
  const lists = got.flatMap((r) => (r.status === 'fulfilled' ? [r.value] : []))
  if (!lists.length && got[0]?.status === 'rejected') throw got[0].reason
  return lists
    .flat()
    .sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts))
    .slice(0, 5)
}

/** One line about the machine when something needs attention: the agent, or disk space. */
function MachineNotice() {
  const ws = useWorkspace()
  const disk = ws.machine?.live?.diskWarning
  if (ws.agentDown) return <Notice tone="error" title={t('agentDown.title')}>{t('agentDown.note')}</Notice>
  if (disk) return <Notice tone={disk.status === 'fail' ? 'error' : 'warning'} title={t('overview.lowDiskTitle', { detail: disk.detail })}>{disk.fix}</Notice>
  return null
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
  const tone = phaseTone(s.phase)
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
            {t('card.crashed')}
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
  const address = joinAddress(joinHost(machineOf(s, ws.machines), window.location.hostname), s.gamePort)
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

/** The dashed card that starts a new server: on the dashboard's machine, or on the machine given. */
function NewServerCard({ machine }: { machine?: MachineView }) {
  const ws = useWorkspace()
  const m = machine ?? ws.machine
  const live = m?.live
  const name = m && m.kind === 'remote' ? machineLabel(m) : ws.machineName
  const full = !!live && live.memoryFreeMB <= 0
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
      <a {...linkProps(m.kind === 'local' ? { name: 'machine', id: m.id } : { name: 'machine-settings', id: m.id })} className="rounded outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
        {t('machines.home.on', { name })}
      </a>
      <span className="text-[13px] font-normal text-muted-foreground">{meta.join(t('common.dot'))}</span>
    </h2>
  )
}

function MachineCard() {
  const ws = useWorkspace()
  const m = ws.machine
  const live = m?.live
  const reserved = live ? live.systemReserveMB + live.serversMemoryMB : 0
  const diskUsed = live?.diskTotalBytes && live.diskFreeBytes !== undefined ? ((live.diskTotalBytes - live.diskFreeBytes) / live.diskTotalBytes) * 100 : undefined
  return (
    <Card>
      <CardTitle>{ws.machineName}</CardTitle>
      {live && <CardHint>{t('home.machineMeta', { os: live.os, memory: formatMB(live.memoryTotalMB) })}</CardHint>}
      {live ? (
        <div className="mt-4 flex flex-col gap-4">
          <MeterRow label={t('home.memoryReserved')} value={t('home.ofTotal', { used: formatMB(reserved), total: formatMB(live.memoryTotalMB) })} percent={live.memoryTotalMB ? (reserved / live.memoryTotalMB) * 100 : 0} />
          <MeterRow label={t('home.cpu')} value={formatPercent(live.cpuPercent)} percent={live.cpuPercent} />
          <MeterRow label={t('home.disk')} value={t('home.diskFree', { free: formatBytes(live.diskFreeBytes) })} percent={diskUsed} />
        </div>
      ) : (
        <p className="mt-3 flex-1 text-[13px] text-muted-foreground">{ws.agentDown ? t('nav.notAnswering') : t('common.loading')}</p>
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
