import { useId, useState, type ReactNode } from 'react'
import { ArchiveIcon, CheckIcon, ChevronDownIcon, ChevronRightIcon, CopyIcon, EllipsisIcon, GlobeIcon, HouseIcon, LayoutGridIcon, PlayIcon, PlusIcon, PuzzleIcon, RotateCwIcon, SearchIcon, SlidersHorizontalIcon, SquareIcon, SquareTerminalIcon, Trash2Icon, UsersIcon } from 'lucide-react'
import { post } from '@/api/client'
import type { ServerStatus } from '@/api/types'
import { errorText, serverApi, useServer, useWorkspace } from '@/api/workspace'
import { Emblem, Pip } from '@/components/app/art'
import { copyText, Dot, JobPill, StatusPill } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { PageBody, PhoneBackHeader, useShell } from '@/components/app/shell'
import { LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Menu, MenuItem, MenuPopup, MenuRadioGroup, MenuRadioItem, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { Sheet, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t, type MessageKey } from '@/i18n'
import { formatMB, relativeTime, serverJoinAddress } from '@/lib/format'
import { controls, isSettingUp, phaseTone, statusLabel, statusTone, whyNot } from '@/lib/phase'
import { addonTab } from '@/lib/addons'
import { linkPath, linkProps, navigate, type ServerSub, type ServerTab } from '@/lib/router'
import { iconURL, softwareLabel, styleTitle, typeName } from '@/lib/servers'
import { cn } from '@/lib/utils'
import { ConsolePage } from './console'
import { Overview } from './overview'
import { PlayersPage } from './players'
import { PluginsPage, PluginsPhoneHeader } from './plugins'
import { RunningPage } from './running'
import { ServerSettingsPage } from './settings'
import { WorldPage } from './world'
import { PacksPage } from './world-packs'
import { PregenPage } from './world-pregen'

const tabs: { tab: ServerTab; key: MessageKey; icon: ReactNode }[] = [
  { tab: 'overview', key: 'tab.overview', icon: <LayoutGridIcon /> },
  { tab: 'console', key: 'tab.console', icon: <SquareTerminalIcon /> },
  { tab: 'players', key: 'tab.players', icon: <UsersIcon /> },
  { tab: 'world', key: 'tab.world', icon: <GlobeIcon /> },
  { tab: 'plugins', key: 'tab.plugins', icon: <PuzzleIcon /> },
  { tab: 'mods', key: 'tab.mods', icon: <PuzzleIcon /> },
  { tab: 'settings', key: 'tab.settings', icon: <SlidersHorizontalIcon /> },
]

export async function serverAction(server: ServerStatus, action: 'start' | 'stop' | 'restart' | 'backups', body: unknown = {}): Promise<boolean> {
  try {
    await post(serverApi(server.id, `/${action}`), body)
    return true
  } catch (e) {
    toastManager.add({ title: errorText(e), type: 'error' })
    return false
  }
}

export function ServerPage({ slug, tab, sub, page }: { slug: string; tab: ServerTab; sub?: ServerSub; page?: 'running' }) {
  const ws = useWorkspace()
  const server = useServer(slug)
  const phone = useIsPhone()
  if (!ws.servers) {
    return (
      <PageBody>
        <LoadingLabel />
        <Skeleton className="h-10 w-64" />
        <Skeleton className="mt-6 h-48 w-full rounded-3xl" />
      </PageBody>
    )
  }
  if (!server) return <NotFound />
  const settingUp = !ws.stale && isSettingUp(server)
  let body: ReactNode
  switch (tab) {
    case 'overview':
      body = page === 'running' ? <RunningPage server={server} /> : <Overview server={server} />
      break
    case 'console':
      body = <ConsolePage server={server} />
      break
    case 'players':
      body = <PlayersPage server={server} />
      break
    case 'world':
      body = sub === 'pregen' ? <PregenPage server={server} /> : sub === 'packs' ? <PacksPage server={server} /> : <WorldPage server={server} />
      break
    case 'plugins':
    case 'mods':
      body = <PluginsPage server={server} tab={tab} sub={sub} />
      break
    case 'settings':
      body = <ServerSettingsPage server={server} />
      break
    default: {
      const unreachable: never = tab
      body = unreachable
    }
  }
  if (settingUp && (page || (tab !== 'overview' && tab !== 'console'))) body = <Overview server={server} />
  // The Plugins tab keeps its running job and highlighted file across its
  // views, and animates switching between them itself.
  const pageKey = tab === 'plugins' || tab === 'mods' ? tab : `${tab}:${sub ?? page ?? ''}`
  return (
    <>
      {phone ? (
        tab === 'settings' ? (
          <PhoneBackHeader to={{ name: 'more' }} label={t('nav.more')} title={t('tab.settings')} />
        ) : page === 'running' && !settingUp ? (
          <PhoneBackHeader to={{ name: 'server', slug: server.slug, tab: 'overview' }} label={t('tab.overview')} title={t('overview.running')} />
        ) : (tab === 'plugins' || tab === 'mods') && !settingUp ? (
          <PluginsPhoneHeader server={server} tab={tab} sub={sub} />
        ) : tab === 'world' && sub && !settingUp ? null : (
          <PhoneServerHeader server={server} tab={tab} />
        )
      ) : (
        <ServerHeader server={server} tab={tab} settingUp={settingUp} />
      )}
      <PageBody key={pageKey} className="flex flex-1 animate-page flex-col gap-4">
        {body}
      </PageBody>
    </>
  )
}

function NotFound() {
  return (
    <PageBody className="flex flex-1 flex-col items-center justify-center py-16 text-center">
      <Pip pose="search" size={96} />
      <h1 className="mt-4 text-xl font-bold">{t('server.notFound')}</h1>
      <p className="mt-1 max-w-[380px] text-sm text-muted-foreground">{t('server.notFoundBody')}</p>
      <Button className="mt-5" render={<a {...linkProps({ name: 'home' })} />}>
        <HouseIcon />
        {t('server.goHome')}
      </Button>
    </PageBody>
  )
}

/** "Minecraft 26.1.2 · Paper · Survival with friends". */
function metaLine(s: ServerStatus, settingUp: boolean, stale: boolean, lastSeenAt: number | undefined): string {
  const cfg = s.config
  const parts: string[] = []
  if (cfg?.minecraftVersion) parts.push(t('server.minecraft', { version: cfg.minecraftVersion }))
  parts.push(typeName(s.type))
  if (stale) {
    if (lastSeenAt && s.phase === 'online') parts.push(t('server.lastSeen', { time: relativeTime(new Date(lastSeenAt).toISOString()) }))
  } else {
    const style = styleTitle(cfg)
    if (style) parts.push(style)
    if (settingUp && cfg?.memoryMB) parts.push(formatMB(cfg.memoryMB))
  }
  return parts.join(t('common.dot'))
}

function useCopyAddress(server: ServerStatus) {
  return async () => {
    const ok = await copyText(serverJoinAddress(server))
    toastManager.add(ok ? { title: t('toast.copied'), type: 'success' } : { title: t('toast.copyFailed'), type: 'error' })
  }
}

function PrimaryAction({ server }: { server: ServerStatus }) {
  const { stale } = useWorkspace()
  const [busy, setBusy] = useState(false)
  const run = async (action: 'start' | 'restart') => {
    setBusy(true)
    await serverAction(server, action)
    setBusy(false)
  }
  const tone = statusTone(server)
  if (!stale && (tone === 'crashed' || (tone === 'stopped' && server.exists))) {
    return (
      <Button onClick={() => run('start')} loading={busy} disabledReason={whyNot(server, 'start', stale)}>
        <PlayIcon />
        {tone === 'crashed' ? t('server.startAgain') : t('server.start')}
      </Button>
    )
  }
  return (
    <Button variant="outline" onClick={() => run('restart')} loading={busy} disabledReason={whyNot(server, 'restart', stale)}>
      <RotateCwIcon />
      {t('server.restart')}
    </Button>
  )
}

function MoreMenu({ server }: { server: ServerStatus }) {
  const { stale } = useWorkspace()
  const c = controls(server)
  const backUpBlocked = whyNot(server, 'change', stale)
  return (
    <Menu>
      <MenuTrigger render={<Button variant="outline" size="icon" aria-label={t('common.moreActions')} />}>
        <EllipsisIcon />
      </MenuTrigger>
      <MenuPopup align="end" className="min-w-52">
        {c.canRestart && (
          <MenuItem onClick={() => void serverAction(server, 'restart')}>
            <RotateCwIcon />
            {t('server.restart')}
          </MenuItem>
        )}
        {c.canStop && (
          <MenuItem onClick={() => void serverAction(server, 'stop')}>
            <SquareIcon />
            {t('server.stop')}
          </MenuItem>
        )}
        <MenuItem disabled={!!backUpBlocked} title={backUpBlocked} onClick={() => void serverAction(server, 'backups')}>
          <ArchiveIcon />
          {t('server.backUp')}
        </MenuItem>
        <MenuSeparator />
        <MenuItem variant="destructive" onClick={() => navigate(`/servers/${server.slug}/settings#danger`)}>
          <Trash2Icon />
          {t('server.deleteMenu')}
        </MenuItem>
      </MenuPopup>
    </Menu>
  )
}

function ServerHeader({ server: s, tab, settingUp }: { server: ServerStatus; tab: ServerTab; settingUp: boolean }) {
  const ws = useWorkspace()
  const copy = useCopyAddress(s)
  const op = !ws.stale ? s.operation : undefined
  const locked = (t2: ServerTab) => settingUp && t2 !== 'overview' && t2 !== 'console'
  return (
    <header className="border-b border-border px-7 pt-3.5">
      <div className="flex h-8 items-center gap-2 text-[13px]">
        <nav aria-label={t('nav.breadcrumb')} className="flex min-w-0 items-center gap-1.5">
          {ws.machine && (
            <a {...linkProps({ name: 'machine', id: ws.machine.id })} className="text-muted-foreground hover:text-foreground">
              {ws.machineName}
            </a>
          )}
          <span className="text-muted-foreground/60" aria-hidden="true">
            /
          </span>
          <Menu>
            <MenuTrigger className="inline-flex items-center gap-1 rounded-md px-1 py-0.5 font-semibold outline-none hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring" aria-label={t('nav.switchServerFrom', { server: s.name })}>
              {s.name}
              <ChevronDownIcon className="size-3.5 text-muted-foreground" aria-hidden="true" />
            </MenuTrigger>
            <MenuPopup align="start" className="min-w-56">
              <MenuRadioGroup
                value={s.id}
                onValueChange={(id: string) => {
                  const o = ws.servers?.find((x) => x.id === id)
                  if (o) navigate({ name: 'server', slug: o.slug, tab })
                }}
              >
                {(ws.servers ?? []).map((o) => (
                  <MenuRadioItem key={o.id} value={o.id} closeOnClick>
                    <span className="flex items-center gap-2">
                      <Dot tone={ws.stale ? 'unknown' : statusTone(o)} />
                      {o.name}
                    </span>
                  </MenuRadioItem>
                ))}
              </MenuRadioGroup>
              <MenuSeparator />
              <MenuItem onClick={() => navigate({ name: 'new-server' })}>
                <PlusIcon />
                {t('nav.newServer')}
              </MenuItem>
            </MenuPopup>
          </Menu>
        </nav>
        {op && (
          <div className="ml-auto">
            <JobPill op={op} server={s.name} onClick={() => navigate({ name: 'server', slug: s.slug, tab: op.kind === 'backup' || op.kind === 'restore' ? 'world' : 'overview' })} />
          </div>
        )}
      </div>
      <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-3">
        <Emblem size={44} stopped={ws.stale || phaseTone(s.phase) !== 'online'} icon={iconURL(s)} name={s.name} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
            <h1 className="truncate text-title font-bold tracking-[-0.015em]">{s.name}</h1>
            <StatusPill server={s} agentDown={ws.stale} elapsed={settingUp ? s.operation?.startedAt : undefined} />
          </div>
          <p className="mt-0.5 truncate text-[13px] text-muted-foreground">{metaLine(s, settingUp, ws.stale, ws.lastSeenAt)}</p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" onClick={copy}>
            <CopyIcon />
            {t('server.copyAddress')}
          </Button>
          {!settingUp && <PrimaryAction server={s} />}
          {!settingUp && !ws.stale && <MoreMenu server={s} />}
        </div>
      </div>
      <nav aria-label={t('nav.serverTabs')} className="mt-4 -mb-px flex gap-[22px] overflow-x-auto">
        {tabs.filter((x) => (x.tab !== 'plugins' && x.tab !== 'mods') || x.tab === addonTab(s.type)).map((x) => {
          const active = x.tab === tab
          const cls = cn(
            'inline-flex h-10 shrink-0 items-center gap-2 border-b-2 text-sm font-medium outline-none [&_svg]:size-4',
            active ? 'border-primary text-foreground' : 'border-transparent text-muted-foreground hover:text-foreground',
          )
          if (locked(x.tab)) {
            return (
              <span key={x.tab} role="link" aria-disabled="true" title={t('server.tabAfterSetup', { server: s.name })} className={cn(cls, 'cursor-not-allowed text-muted-foreground/50 hover:text-muted-foreground/50')}>
                {x.icon}
                {t(x.key)}
              </span>
            )
          }
          return (
            <a key={x.tab} {...linkProps({ name: 'server', slug: s.slug, tab: x.tab })} aria-current={active ? 'page' : undefined} className={cn(cls, 'focus-visible:text-foreground focus-visible:underline')}>
              {x.icon}
              {t(x.key)}
            </a>
          )
        })}
      </nav>
    </header>
  )
}

function PhoneServerHeader({ server: s, tab }: { server: ServerStatus; tab: ServerTab }) {
  const shell = useShell()
  const [open, setOpen] = useState(false)
  const hint = useId()
  return (
    <header className="flex items-start gap-2 pt-4 pb-3">
      <div className="min-w-0 flex-1">
        <h1 className="text-[22px] leading-7 font-bold tracking-[-0.015em]">
          <button type="button" onClick={() => setOpen(true)} aria-haspopup="dialog" aria-describedby={hint} className="-ml-1 inline-flex max-w-full items-center gap-1 rounded-lg px-1 text-left">
            <span className="truncate">{s.name}</span>
            <ChevronDownIcon className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
          </button>
        </h1>
        <span id={hint} className="sr-only">
          {t('nav.switchServer')}
        </span>
        <PhoneStatus server={s} />
      </div>
      <Button variant="ghost" size="icon-lg" aria-label={t('nav.search')} onClick={shell.openPalette}>
        <SearchIcon className="size-5" />
      </Button>
      <SwitcherSheet open={open} onOpenChange={setOpen} current={s} tab={tab} />
    </header>
  )
}

function PhoneStatus({ server }: { server: ServerStatus }) {
  const ws = useWorkspace()
  return (
    <div className="mt-0.5 flex items-center">
      <StatusPill server={server} agentDown={ws.stale} onChalk className="h-auto border-0 bg-transparent px-0 text-[13px] font-medium" />
    </div>
  )
}

/** The phone's server switcher: every server, then New server and All servers. */
export function SwitcherSheet({ open, onOpenChange, current, tab }: { open: boolean; onOpenChange: (open: boolean) => void; current?: ServerStatus; tab: ServerTab }) {
  const ws = useWorkspace()
  const live = ws.machine?.live
  const go = (to: Parameters<typeof navigate>[0]) => {
    onOpenChange(false)
    navigate(to)
  }
  const line = (s: ServerStatus) => {
    const tone = ws.stale ? 'unknown' : statusTone(s)
    const state =
      tone === 'online'
        ? s.players?.online
          ? `${t('status.online')}${t('common.dot')}${t('status.playing', { count: s.players.online })}`
          : t('status.online')
        : tone === 'crashed'
          ? statusLabel(s)
          : tone === 'stopped' && s.stoppedAt
            ? t('switcher.stoppedAgo', { time: relativeTime(s.stoppedAt) })
            : ws.stale
              ? t('status.unknown')
              : undefined
    return [state, softwareLabel(s)].filter(Boolean).join(t('common.dot'))
  }
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetPopup side="bottom" className="px-4">
        <div className="flex items-center justify-between pt-3 pb-3">
          <SheetTitle className="text-lg font-bold">{t('nav.switchServer')}</SheetTitle>
        </div>
        <ul className="overflow-hidden rounded-3xl border border-border bg-white">
          {(ws.servers ?? []).map((s) => (
            <li key={s.id} className="border-b border-border last:border-b-0">
              <button type="button" onClick={() => go({ name: 'server', slug: s.slug, tab: tab === 'settings' ? 'overview' : tab })} className="flex min-h-16 w-full items-center gap-3 px-3 py-2 text-left" aria-current={s.id === current?.id ? 'true' : undefined}>
                <Emblem size={44} stopped={ws.stale || phaseTone(s.phase) !== 'online'} icon={iconURL(s)} name={s.name} />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-base font-semibold">{s.name}</span>
                  <span className="block truncate text-[13px] text-muted-foreground">{line(s)}</span>
                </span>
                {s.id === current?.id ? <CheckIcon className="size-5 text-primary" aria-hidden="true" /> : <ChevronRightIcon className="size-5 text-muted-foreground" aria-hidden="true" />}
              </button>
            </li>
          ))}
        </ul>
        <ul className="mt-3 mb-2 overflow-hidden rounded-3xl border border-border bg-white">
          <li className="border-b border-border">
            <button type="button" onClick={() => go({ name: 'new-server' })} className="flex min-h-14 w-full items-center gap-3 px-4 py-2 text-left">
              <PlusIcon className="size-5 text-primary" aria-hidden="true" />
              <span className="min-w-0 flex-1">
                <span className="block text-base">{t('nav.newServer')}</span>
                {live && <span className="block text-[13px] text-muted-foreground">{t('home.newServerFree', { memory: formatMB(live.memoryFreeMB), machine: ws.machineName })}</span>}
              </span>
              <ChevronRightIcon className="size-5 text-muted-foreground" aria-hidden="true" />
            </button>
          </li>
          <li>
            <a {...linkPath('/')} onClick={(e) => { e.preventDefault(); go({ name: 'home' }) }} className="flex min-h-14 w-full items-center gap-3 px-4 py-2">
              <HouseIcon className="size-5 text-muted-foreground" aria-hidden="true" />
              <span className="flex-1 text-base">{t('nav.allServers')}</span>
              <ChevronRightIcon className="size-5 text-muted-foreground" aria-hidden="true" />
            </a>
          </li>
        </ul>
      </SheetPopup>
    </Sheet>
  )
}
