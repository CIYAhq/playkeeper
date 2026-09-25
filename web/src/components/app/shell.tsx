import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { ChevronLeftIcon, CircleHelpIcon, EllipsisIcon, GlobeIcon, HouseIcon, LayoutGridIcon, LogOutIcon, PlusIcon, SearchIcon, ServerIcon, SettingsIcon, SquareTerminalIcon, UsersIcon } from 'lucide-react'
import type { ServerStatus } from '@/api/types'
import { usePhoneServer, useWorkspace } from '@/api/workspace'
import { BrandMark } from '@/components/app/art'
import { Dot, Kbd, Spinner } from '@/components/app/bits'
import { GetStartedCard } from '@/components/app/checklist'
import { CommandPalette, ShortcutsDialog } from '@/components/app/command-palette'
import { useIsPhone } from '@/components/app/controls'
import { useJobToasts } from '@/components/app/jobs'
import { UpdateRow } from '@/components/app/update'
import { t } from '@/i18n'
import { isSettingUp, phaseLabel, phaseTone } from '@/lib/phase'
import { linkProps, navigate, type Route, type ServerTab } from '@/lib/router'
import { cn } from '@/lib/utils'

const ShellCtx = createContext<{ openPalette: () => void }>({ openPalette: () => undefined })

/** Lets a page's own header open the command palette. */
export function useShell() {
  return useContext(ShellCtx)
}

/** The dashboard around every signed-in page: a sidebar on desktop, bottom tabs on phones. */
export function AppShell({ route, children }: { route: Route; children: ReactNode }) {
  const phone = useIsPhone()
  const { servers, setLastSlug } = useWorkspace()
  const [palette, setPalette] = useState<{ open: boolean; servers?: boolean }>({ open: false })
  const [shortcuts, setShortcuts] = useState(false)

  const slug = route.name === 'server' ? route.slug : undefined
  useEffect(() => {
    if (slug && servers?.some((s) => s.slug === slug)) setLastSlug(slug)
  }, [slug, servers, setLastSlug])

  useJobToasts()
  useShortcuts({
    palette: () => setPalette({ open: true }),
    switchServer: () => setPalette({ open: true, servers: true }),
    home: () => navigate({ name: 'home' }),
    help: () => setShortcuts(true),
  })

  const overlays = (
    <>
      <CommandPalette open={palette.open} serversOnly={palette.servers} onOpenChange={(open) => setPalette({ open })} route={route} onShortcuts={() => setShortcuts(true)} />
      <ShortcutsDialog open={shortcuts} onOpenChange={setShortcuts} />
    </>
  )

  const shell = { openPalette: () => setPalette({ open: true }) }
  if (phone) {
    return (
      <ShellCtx.Provider value={shell}>
        <PhoneShell route={route} overlays={overlays}>
          {children}
        </PhoneShell>
      </ShellCtx.Provider>
    )
  }
  return (
    <ShellCtx.Provider value={shell}>
      <div className="flex min-h-dvh">
        <a href="#main" className="skip-link rounded-lg bg-white px-3 py-2 text-sm font-medium shadow-popup">
          {t('nav.skip')}
        </a>
        <Sidebar route={route} onSearch={shell.openPalette} />
        <main id="main" tabIndex={-1} className="my-2 mr-2 flex min-h-[calc(100dvh-16px)] min-w-0 flex-1 flex-col rounded-2xl border border-border bg-background shadow-card outline-none">
          {children}
          <p className="mt-auto px-7 pt-6 pb-4 text-xs text-muted-foreground">{t('footer.notOfficial')}</p>
        </main>
        {overlays}
      </div>
    </ShellCtx.Provider>
  )
}

function useShortcuts(actions: { palette: () => void; switchServer: () => void; home: () => void; help: () => void }) {
  const ref = useRef(actions)
  useEffect(() => {
    ref.current = actions
  })
  useEffect(() => {
    let pendingG = 0
    function onKey(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        ref.current.palette()
        return
      }
      if (e.metaKey || e.ctrlKey || e.altKey) return
      const el = e.target as HTMLElement | null
      if (el && (el.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName))) return
      if (document.querySelector('[role="dialog"]')) return
      if (e.key === '?') {
        ref.current.help()
        return
      }
      if (e.key === 'g') {
        pendingG = Date.now()
        return
      }
      if (Date.now() - pendingG < 1000) {
        pendingG = 0
        if (e.key === 'h') ref.current.home()
        if (e.key === 's') ref.current.switchServer()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])
}

function SideItem({ to, active, icon, children, trailing, muted }: { to: Route; active?: boolean; icon?: ReactNode; children: ReactNode; trailing?: ReactNode; muted?: boolean }) {
  return (
    <a
      {...linkProps(to)}
      aria-current={active ? 'page' : undefined}
      className={cn(
        'flex h-8 items-center gap-2.5 rounded-lg border border-transparent px-2 text-sm font-medium outline-none hover:bg-black/[.035] focus-visible:ring-2 focus-visible:ring-ring [&_svg]:size-4 [&_svg]:shrink-0',
        active && 'border-border bg-white shadow-outline hover:bg-white',
        muted && !active && 'text-muted-foreground',
      )}
    >
      {icon}
      <span className="min-w-0 flex-1 truncate">{children}</span>
      {trailing}
    </a>
  )
}

/** What the sidebar says next to a server: players, or its state when it isn't online. */
function serverMeta(s: ServerStatus, stale: boolean): ReactNode {
  if (stale) return <span className="text-xs text-muted-foreground">{t('status.unknown')}</span>
  if (isSettingUp(s)) return <span className="text-xs font-medium text-info-foreground">{t('status.creating')}</span>
  const tone = phaseTone(s.phase)
  switch (tone) {
    case 'online':
      return <span className="text-xs text-muted-foreground tabular-nums">{s.players ? `${s.players.online}/${s.players.max}` : ''}</span>
    case 'crashed':
      return <span className="text-xs font-medium text-destructive-foreground">{t('status.crashed')}</span>
    case 'busy':
      return <span className="text-xs text-info-foreground">{phaseLabel(s.phase)}</span>
    case 'stopped':
    case 'unknown':
      return <span className="text-xs text-muted-foreground">{phaseLabel(s.phase)}</span>
    default: {
      const unreachable: never = tone
      return unreachable
    }
  }
}

function Sidebar({ route, onSearch }: { route: Route; onSearch: () => void }) {
  const ws = useWorkspace()
  const live = ws.machine?.live
  const tab: ServerTab = route.name === 'server' ? route.tab : 'overview'
  const healthy = !!ws.updating || (!ws.agentDown && !!live && live.docker)
  return (
    <aside className="sticky top-0 flex h-dvh w-64 shrink-0 flex-col px-3 pt-3 pb-2">
      <a {...linkProps({ name: 'home' })} className="flex h-9 items-center gap-2 rounded-lg px-1.5 text-[15px] font-bold outline-none focus-visible:ring-2 focus-visible:ring-ring">
        <BrandMark size={24} />
        {t('brand.name')}
      </a>
      <button
        type="button"
        onClick={onSearch}
        className="mt-3 flex h-8 items-center gap-2 rounded-lg border border-border bg-white px-2.5 text-[13px] text-muted-foreground shadow-outline outline-none hover:border-input focus-visible:ring-2 focus-visible:ring-ring"
      >
        <SearchIcon className="size-4" aria-hidden="true" />
        <span className="flex-1 text-left">{t('nav.search')}</span>
        <Kbd>{t('nav.searchShortcut')}</Kbd>
      </button>
      <nav aria-label={t('nav.main')} className="mt-3 flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto">
        <SideItem to={{ name: 'home' }} active={route.name === 'home'} icon={<HouseIcon />}>
          {t('nav.home')}
        </SideItem>
        {ws.machine && (
          <a
            {...linkProps({ name: 'machine', id: ws.machine.id })}
            aria-current={route.name === 'machine' ? 'page' : undefined}
            className={cn('mt-3 flex h-7 items-center gap-2 rounded-lg px-2 text-xs font-semibold text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring', route.name === 'machine' && 'text-foreground')}
          >
            <ServerIcon className="size-3.5" aria-hidden="true" />
            <span className="min-w-0 flex-1 truncate">{ws.machineName}</span>
            <span className={cn('flex items-center gap-1.5 text-[11px] font-medium', healthy ? 'text-success-strong' : 'text-warning-strong')}>
              <span className={cn('size-1.5 rounded-full', healthy ? 'bg-success' : 'bg-warning')} aria-hidden="true" />
              {healthy ? t('nav.healthy') : ws.agentDown ? t('nav.notAnswering') : t('status.docker')}
            </span>
          </a>
        )}
        {(ws.servers ?? []).map((s) => (
          <SideItem
            key={s.id}
            to={{ name: 'server', slug: s.slug, tab }}
            active={route.name === 'server' && route.slug === s.slug}
            icon={!ws.stale && isSettingUp(s) ? <Spinner /> : <Dot tone={ws.stale ? 'unknown' : phaseTone(s.phase)} />}
            trailing={serverMeta(s, ws.stale)}
          >
            {s.name}
          </SideItem>
        ))}
        <SideItem to={{ name: 'new-server' }} active={route.name === 'new-server'} icon={<PlusIcon />} muted>
          {t('nav.newServer')}
        </SideItem>
      </nav>
      <div className="flex flex-col gap-0.5 pt-2">
        <GetStartedCard route={route} className="mb-2" />
        <UpdateRow />
        <SideItem to={{ name: 'settings' }} active={route.name === 'settings'} icon={<SettingsIcon />}>
          {t('nav.settings')}
        </SideItem>
        <UserRow />
      </div>
    </aside>
  )
}

export function roleLabel(role: string): string {
  return role === 'member' ? t('nav.role.member') : t('nav.role.owner')
}

function UserRow() {
  const { me, signOut } = useWorkspace()
  const name = me.user.username
  return (
    <div className="mt-1 flex items-center gap-2.5 px-2 py-1.5">
      <a {...linkProps({ name: 'settings' })} className="flex min-w-0 flex-1 items-center gap-2.5 rounded-lg outline-none focus-visible:ring-2 focus-visible:ring-ring">
        <Avatar name={name} />
        <span className="min-w-0 leading-tight">
          <span className="block truncate text-[13px] font-semibold">{name}</span>
          <span className="block text-xs text-muted-foreground">{roleLabel(me.user.role)}</span>
        </span>
      </a>
      <a href={t('nav.helpUrl')} target="_blank" rel="noreferrer" aria-label={t('common.external', { label: t('nav.help') })} className="inline-flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-black/5 hover:text-foreground">
        <CircleHelpIcon className="size-4" aria-hidden="true" />
      </a>
      <button type="button" onClick={() => void signOut()} aria-label={t('nav.signOut')} className="inline-flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-black/5 hover:text-foreground">
        <LogOutIcon className="size-4" aria-hidden="true" />
      </button>
    </div>
  )
}

export function Avatar({ name, className }: { name: string; className?: string }) {
  return (
    <span className={cn('inline-flex size-7 shrink-0 items-center justify-center rounded-full bg-selected text-[13px] font-semibold text-primary ring-1 ring-primary/15', className)} aria-hidden="true">
      {name.slice(0, 1).toUpperCase()}
    </span>
  )
}

const phoneTabs: { tab: ServerTab | 'more'; key: 'tab.overview' | 'tab.players' | 'tab.console' | 'tab.world' | 'nav.more'; icon: ReactNode }[] = [
  { tab: 'overview', key: 'tab.overview', icon: <LayoutGridIcon /> },
  { tab: 'players', key: 'tab.players', icon: <UsersIcon /> },
  { tab: 'console', key: 'tab.console', icon: <SquareTerminalIcon /> },
  { tab: 'world', key: 'tab.world', icon: <GlobeIcon /> },
  { tab: 'more', key: 'nav.more', icon: <EllipsisIcon /> },
]

function PhoneShell({ route, overlays, children }: { route: Route; overlays: ReactNode; children: ReactNode }) {
  const ws = useWorkspace()
  const phoneServer = usePhoneServer()
  const inServer = route.name === 'server' || (route.name === 'more' && !!phoneServer)
  const slug = route.name === 'server' ? route.slug : phoneServer?.slug
  const current: ServerTab | 'more' | undefined = route.name === 'server' ? (route.tab === 'settings' ? 'more' : route.tab) : route.name === 'more' ? 'more' : undefined
  const updateDot = !!ws.machine?.live?.updateAvailable || !!ws.updating
  return (
    <div className="flex min-h-dvh flex-col bg-sidebar">
      <a href="#main" className="skip-link rounded-lg bg-white px-3 py-2 text-sm font-medium shadow-popup">
        {t('nav.skip')}
      </a>
      <main id="main" tabIndex={-1} className={cn('flex flex-1 flex-col px-4 pt-[max(env(safe-area-inset-top),8px)] outline-none', inServer ? 'pb-[calc(68px+env(safe-area-inset-bottom))]' : 'pb-[max(env(safe-area-inset-bottom),24px)]')}>
        {children}
      </main>
      {inServer && slug && (
        <nav aria-label={t('nav.serverTabs')} className="fixed inset-x-0 bottom-0 z-40 border-t border-border bg-white/95 pb-[env(safe-area-inset-bottom)] backdrop-blur">
          <ul className="flex h-[52px]">
            {phoneTabs.map((p) => {
              const to: Route = p.tab === 'more' ? { name: 'more' } : { name: 'server', slug, tab: p.tab }
              const active = current === p.tab
              return (
                <li key={p.tab} className="flex-1">
                  <a
                    {...linkProps(to)}
                    aria-current={active ? 'page' : undefined}
                    className={cn('relative flex h-full flex-col items-center justify-center gap-0.5 text-[11px] font-medium [&_svg]:size-6', active ? 'text-primary' : 'text-muted-foreground')}
                  >
                    {p.icon}
                    {t(p.key)}
                    {p.tab === 'more' && updateDot && <span className="absolute top-1.5 left-[calc(50%+8px)] size-2 rounded-full bg-success ring-2 ring-white" aria-hidden="true" />}
                  </a>
                </li>
              )
            })}
          </ul>
        </nav>
      )}
      {overlays}
    </div>
  )
}

/** A page's title row: title, a line under it and actions on the right. */
export function PageHeader({ title, subtitle, actions, breadcrumb, phoneAction }: { title: ReactNode; subtitle?: ReactNode; actions?: ReactNode; breadcrumb?: ReactNode; phoneAction?: ReactNode }) {
  const phone = useIsPhone()
  if (phone) {
    return (
      <header className="flex items-center gap-3 pt-5 pb-4">
        <div className="min-w-0 flex-1">
          <h1 className="text-[30px] leading-9 font-extrabold tracking-[-0.02em]">{title}</h1>
          {subtitle && <p className="mt-0.5 text-[13px] text-muted-foreground">{subtitle}</p>}
        </div>
        {phoneAction}
      </header>
    )
  }
  return (
    <header className="border-b border-border px-7 pt-4 pb-5">
      {breadcrumb && <div className="mb-3 text-[13px] text-muted-foreground">{breadcrumb}</div>}
      <div className={cn('flex flex-wrap items-end gap-4', !breadcrumb && 'pt-5')}>
        <div className="min-w-0 flex-1">
          <h1 className="text-title font-bold tracking-[-0.015em]">{title}</h1>
          {subtitle && <p className="mt-1 text-sm text-muted-foreground">{subtitle}</p>}
        </div>
        {actions && <div className="flex items-center gap-2">{actions}</div>}
      </div>
    </header>
  )
}

/** The phone's "…" button that opens More from pages without tabs. */
export function PhoneMoreButton() {
  return (
    <a {...linkProps({ name: 'more' })} aria-label={t('nav.more')} className="inline-flex size-11 items-center justify-center rounded-full border border-border bg-white shadow-outline">
      <EllipsisIcon className="size-5" aria-hidden="true" />
    </a>
  )
}

/** The phone header of pages opened from another: a back link and a title. */
export function PhoneBackHeader({ to, label, title }: { to: Route; label: string; title?: ReactNode }) {
  return (
    <header className="flex items-center gap-1 pt-2 pb-2">
      <a {...linkProps(to)} className="-ml-2 inline-flex min-h-11 items-center gap-0.5 rounded-lg px-1 text-[15px] font-medium text-success-strong">
        <ChevronLeftIcon className="size-5" aria-hidden="true" />
        {label}
      </a>
      {title && <div className="ml-auto text-[15px] font-semibold">{title}</div>}
    </header>
  )
}

export function PageBody({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn('px-7 py-6 max-sm:px-0 max-sm:py-0', className)}>{children}</div>
}
