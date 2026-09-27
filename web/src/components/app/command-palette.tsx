import { Fragment, useMemo, type KeyboardEvent, type ReactNode } from 'react'
import { ArchiveIcon, ArrowDownIcon, ArrowUpIcon, BookOpenIcon, CopyIcon, CornerDownLeftIcon, ExternalLinkIcon, GlobeIcon, HouseIcon, PlayIcon, PlusIcon, RotateCwIcon, ServerIcon, SettingsIcon, UserPlusIcon } from 'lucide-react'
import { post } from '@/api/client'
import type { Me, ServerStatus } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { copyText, Kbd } from '@/components/app/bits'
import { serverTabs, serverTabsFor } from '@/components/app/server-tabs'
import {
  Command,
  CommandCollection,
  CommandDialog,
  CommandDialogPopup,
  CommandEmpty,
  CommandFooter,
  CommandGroup,
  CommandGroupLabel,
  CommandInput,
  CommandItem,
  CommandList,
  CommandPanel,
} from '@/components/ui/command'
import { Dialog, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { toastManager } from '@/components/ui/toast'
import { t, type MessageKey } from '@/i18n'
import { can, settingsHome } from '@/lib/access'
import { joinOf, machineLabel, machineOf, machineRoute, reachOf, type Join } from '@/lib/machines'
import { controls } from '@/lib/phase'
import { navigate, type Route, type ServerTab } from '@/lib/router'

interface PaletteItem {
  value: string
  label: string
  hint?: string
  icon: ReactNode
  external?: boolean
  run: () => void
}

interface PaletteGroup {
  value: string
  label: string
  items: PaletteItem[]
}

async function act(server: ServerStatus, path: string, done: string) {
  try {
    await post(serverApi(server.id, path))
    toastManager.add({ title: done, type: 'success' })
  } catch (e) {
    toastManager.add({ title: errorText(e), type: 'error' })
  }
}

function actionsFor(s: ServerStatus, join: Join, me: Me): PaletteItem[] {
  const c = controls(s)
  const items: PaletteItem[] = []
  if (s.exists && !c.busy && s.phase !== 'docker_unavailable' && can(me, 'backups.make')) {
    const stopped = s.phase !== 'online'
    items.push({
      value: `backup:${s.id}`,
      label: t('cmd.backup', { server: s.name }),
      hint: stopped ? t('cmd.backupHintStopped', { server: s.name }) : t('cmd.backupHint'),
      icon: <ArchiveIcon />,
      run: () => void act(s, '/backups', t('op.backup', { server: s.name })),
    })
  }
  const run = can(me, 'servers.run')
  if (c.canRestart && run) items.push({ value: `restart:${s.id}`, label: t('cmd.restart', { server: s.name }), hint: t('cmd.restartHint'), icon: <RotateCwIcon />, run: () => void act(s, '/restart', t('op.restart', { server: s.name })) })
  if (c.canStart && run) items.push({ value: `start:${s.id}`, label: t('cmd.start', { server: s.name }), icon: <PlayIcon />, run: () => void act(s, '/start', t('op.start', { server: s.name })) })
  const address = join.address
  if (address)
    items.push({
      value: `copy:${s.id}`,
      label: t('cmd.copyAddress', { server: s.name }),
      hint: address,
      icon: <CopyIcon />,
      run: () =>
        void copyText(address).then((ok) => toastManager.add(ok ? { title: t('toast.copied'), type: 'success' } : { title: t('toast.copyFailed'), type: 'error' })),
    })
  if (can(me, 'players.manage')) items.push({ value: `add:${s.id}`, label: t('cmd.addPlayer', { server: s.name }), icon: <UserPlusIcon />, run: () => navigate(`/servers/${s.slug}/players#add`) })
  return items
}

/**
 * Wraps Tab and Shift+Tab around the palette's own stops. Base UI's focus
 * guards let focus out of a dialog that holds an inline list.
 */
function keepTabInside(e: KeyboardEvent<HTMLElement>) {
  if (e.key !== 'Tab') return
  const stops = [...e.currentTarget.querySelectorAll<HTMLElement>('input, button, a[href]')].filter((el) => el.tabIndex >= 0 && !el.matches(':disabled') && el.checkVisibility())
  const first = stops[0]
  const last = stops.at(-1)
  if (!first || !last) return
  if (document.activeElement === (e.shiftKey ? first : last)) {
    e.preventDefault()
    ;(e.shiftKey ? last : first).focus()
  }
}

export function CommandPalette({ open, onOpenChange, route, serversOnly, onShortcuts }: { open: boolean; onOpenChange: (open: boolean) => void; route: Route; serversOnly?: boolean; onShortcuts: () => void }) {
  const ws = useWorkspace()
  const servers = useMemo(() => ws.servers ?? [], [ws.servers])
  const current = route.name === 'server' ? servers.find((s) => s.slug === route.slug) : undefined
  const tab: ServerTab = route.name === 'server' ? route.tab : 'overview'
  const { machines, agentDown, machineName } = ws

  const groups = useMemo<PaletteGroup[]>(() => {
    const ordered = current ? [current, ...servers.filter((s) => s.id !== current.id)] : servers
    const go: PaletteItem[] = []
    if (serversOnly) {
      for (const s of ordered) go.push({ value: `go:${s.id}`, label: s.name, hint: t('cmd.page', { server: s.name, page: t(serverTabs.find((p) => p.tab === tab)?.key ?? 'tab.overview') }), icon: <ServerIcon />, run: () => navigate({ name: 'server', slug: s.slug, tab }) })
      return [{ value: 'go', label: t('cmd.goTo'), items: go }]
    }
    go.push({ value: 'go:home', label: t('cmd.pageHome'), icon: <HouseIcon />, run: () => navigate({ name: 'home' }) })
    for (const s of ordered) {
      for (const p of serverTabsFor(ws.me, s)) go.push({ value: `go:${s.id}:${p.tab}`, label: t('cmd.page', { server: s.name, page: t(p.key) }), icon: p.icon, run: () => navigate({ name: 'server', slug: s.slug, tab: p.tab }) })
    }
    if (can(ws.me, 'servers.create')) go.push({ value: 'go:new', label: t('cmd.pageNew'), icon: <PlusIcon />, run: () => navigate({ name: 'new-server' }) })
    for (const m of machines) {
      const to = machineRoute(m)
      go.push({ value: `go:machine:${m.id}`, label: t('cmd.pageMachine', { machine: m.kind === 'local' ? machineName : machineLabel(m) }), icon: <ServerIcon />, run: () => navigate(to) })
      if (m.kind === 'local') go.push({ value: 'go:machine-settings', label: t('machine.settings'), hint: t('address.title'), icon: <GlobeIcon />, run: () => navigate({ name: 'machine-settings', id: m.id }) })
    }
    go.push({ value: 'go:settings', label: t('cmd.pageSettings'), icon: <SettingsIcon />, run: () => navigate(settingsHome(ws.me)) })
    const help: PaletteItem[] = [
      { value: 'help:backups', label: t('cmd.docBackups'), icon: <BookOpenIcon />, external: true, run: () => window.open(t('cmd.docBackupsUrl'), '_blank', 'noreferrer') },
      { value: 'help:readme', label: t('cmd.docReadme'), hint: t('cmd.docReadmeHint'), icon: <BookOpenIcon />, external: true, run: () => window.open(t('nav.helpUrl'), '_blank', 'noreferrer') },
    ]
    const actions = ordered.filter((s) => reachOf(s, { machines, agentDown }).state === 'live').flatMap((s) => actionsFor(s, joinOf(s, machineOf(s, machines)), ws.me))
    return [
      { value: 'actions', label: t('cmd.actions'), items: actions },
      { value: 'go', label: t('cmd.goTo'), items: go },
      { value: 'help', label: t('cmd.help'), items: help },
    ].filter((g) => g.items.length > 0)
  }, [current, servers, serversOnly, tab, agentDown, machines, machineName, ws.me])

  return (
    <CommandDialog open={open} onOpenChange={onOpenChange}>
      <CommandDialogPopup aria-label={t('cmd.title')} onKeyDownCapture={keepTabInside}>
        <Command items={groups} itemToStringValue={(item: unknown) => `${(item as PaletteItem).label} ${(item as PaletteItem).hint ?? ''}`}>
          <div className="relative">
            <CommandInput placeholder={t('cmd.placeholder')} aria-label={t('cmd.title')} />
            <div className="pointer-events-none absolute inset-y-0 right-4 flex items-center gap-2 text-xs text-muted-foreground max-sm:hidden">
              {current && <span>{t('cmd.scope', { server: current.name })}</span>}
              <Kbd>{t('cmd.esc')}</Kbd>
            </div>
          </div>
          <CommandPanel>
            <CommandEmpty>{t('cmd.emptyPlain')}</CommandEmpty>
            <CommandList>
              {(group: PaletteGroup) => (
                <Fragment key={group.value}>
                  <CommandGroup items={group.items}>
                    <CommandGroupLabel>{group.label}</CommandGroupLabel>
                    <CommandCollection>
                      {(item: PaletteItem) => (
                        <CommandItem
                          key={item.value}
                          value={item}
                          onClick={() => {
                            onOpenChange(false)
                            item.run()
                          }}
                          className="gap-3 rounded-lg [&_svg]:size-4 [&_svg]:text-muted-foreground"
                        >
                          {item.icon}
                          <span className="min-w-0 flex-1">
                            <span className="block truncate text-sm">{item.label}</span>
                            {item.hint && <span className="block truncate text-xs text-muted-foreground">{item.hint}</span>}
                          </span>
                          {item.external && <ExternalLinkIcon />}
                        </CommandItem>
                      )}
                    </CommandCollection>
                  </CommandGroup>
                </Fragment>
              )}
            </CommandList>
          </CommandPanel>
          <CommandFooter className="max-sm:hidden">
            <div className="flex items-center gap-4">
              <span className="flex items-center gap-1.5">
                <Kbd>
                  <ArrowUpIcon className="size-3" aria-hidden="true" />
                </Kbd>
                <Kbd>
                  <ArrowDownIcon className="size-3" aria-hidden="true" />
                </Kbd>
                {t('cmd.move')}
              </span>
              <span className="flex items-center gap-1.5">
                <Kbd>
                  <CornerDownLeftIcon className="size-3" aria-hidden="true" />
                </Kbd>
                {t('cmd.run')}
              </span>
              <span className="flex items-center gap-1.5">
                <Kbd>{t('cmd.keyG')}</Kbd>
                <Kbd>{t('cmd.keyS')}</Kbd>
                {t('cmd.switch')}
              </span>
            </div>
            <button type="button" className="flex items-center gap-1.5 hover:text-foreground" onClick={onShortcuts}>
              <Kbd>{t('cmd.keyHelp')}</Kbd>
              {t('cmd.shortcuts')}
            </button>
          </CommandFooter>
        </Command>
      </CommandDialogPopup>
    </CommandDialog>
  )
}

export function ShortcutsDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const rows: { keys: string[]; label: MessageKey }[] = [
    { keys: [t('nav.searchShortcut')], label: 'cmd.sc.search' },
    { keys: [t('cmd.keyG'), t('cmd.keyS')], label: 'cmd.sc.switch' },
    { keys: [t('cmd.keyG'), t('cmd.keyH')], label: 'cmd.sc.home' },
    { keys: [t('cmd.keyHelp')], label: 'cmd.sc.help' },
  ]
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="max-w-[420px]">
        <DialogHeader>
          <DialogTitle className="text-lg">{t('cmd.shortcutsTitle')}</DialogTitle>
        </DialogHeader>
        <DialogPanel>
          <dl className="flex flex-col divide-y divide-border">
            {rows.map((r) => (
              <div key={r.label} className="flex items-center justify-between py-2.5 text-sm">
                <dt>{t(r.label)}</dt>
                <dd className="flex gap-1">
                  {r.keys.map((k) => (
                    <Kbd key={k}>{k}</Kbd>
                  ))}
                </dd>
              </div>
            ))}
          </dl>
        </DialogPanel>
      </DialogPopup>
    </Dialog>
  )
}
