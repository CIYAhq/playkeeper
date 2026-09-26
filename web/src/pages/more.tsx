import { useEffect, useState, type ReactNode } from 'react'
import { BotIcon, ChevronRightIcon, CircleHelpIcon, HouseIcon, LibraryIcon, ListChecksIcon, LogOutIcon, MapIcon, MessageSquareIcon, PlusIcon, PuzzleIcon, ServerCogIcon, ServerIcon, SettingsIcon, Share2Icon, SlidersHorizontalIcon, UsersIcon } from 'lucide-react'
import { usePhoneServer, useWorkspace } from '@/api/workspace'
import { SectionLabel, Spinner } from '@/components/app/bits'
import { stepRoute, stepTitle } from '@/components/app/checklist'
import { useIsPhone } from '@/components/app/controls'
import { Avatar, PageHeader, roleLabel } from '@/components/app/shell'
import { TemplateDialog } from '@/components/app/templates'
import { UpdateDialog } from '@/components/app/update'
import { t } from '@/i18n'
import { can, settingsSections } from '@/lib/access'
import { addonTab } from '@/lib/addons'
import { checklist, complete, progress } from '@/lib/checklist'
import { demo } from '@/lib/demo'
import { formatMB } from '@/lib/format'
import { machineLabel, machineRoute, machineState } from '@/lib/machines'
import { hasMap } from '@/lib/map'
import { linkProps, navigate, type Route } from '@/lib/router'
import { cn } from '@/lib/utils'

function Row({ icon, title, hint, to, href, onClick, danger }: { icon: ReactNode; title: string; hint?: string; to?: Route; href?: string; onClick?: () => void; danger?: boolean }) {
  const body = (
    <>
      <span className={cn('flex size-6 shrink-0 items-center justify-center [&_svg]:size-[22px]', danger ? 'text-destructive-foreground' : 'text-muted-foreground')}>{icon}</span>
      <span className="min-w-0 flex-1">
        <span className={cn('block text-base', danger && 'text-destructive-foreground')}>{title}</span>
        {hint && <span className="block truncate text-[13px] text-muted-foreground">{hint}</span>}
      </span>
      {!danger && <ChevronRightIcon className="size-5 text-muted-foreground" aria-hidden="true" />}
    </>
  )
  const cls = 'flex min-h-14 w-full items-center gap-3.5 px-4 py-1.5 text-left'
  if (to) {
    return (
      <a {...linkProps(to)} className={cls}>
        {body}
      </a>
    )
  }
  if (href) {
    return (
      <a href={href} target="_blank" rel="noreferrer" className={cls}>
        {body}
      </a>
    )
  }
  return (
    <button type="button" onClick={onClick} className={cls}>
      {body}
    </button>
  )
}

export function Group({ label, children }: { label?: string; children: ReactNode }) {
  return (
    <section>
      {label && <SectionLabel className="px-4 pb-2">{label}</SectionLabel>}
      <ul className="overflow-hidden rounded-3xl border border-border bg-white [&>li]:border-b [&>li]:border-border [&>li:last-child]:border-b-0">{children}</ul>
    </section>
  )
}

/** The phone's More tab: updates, this server's settings, every server, the dashboard's settings, and you. */
export function MorePage() {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const server = usePhoneServer()
  const [updateOpen, setUpdateOpen] = useState(false)
  const [sharing, setSharing] = useState(false)

  useEffect(() => {
    if (!phone) navigate({ name: 'home' }, true)
  }, [phone])

  const live = ws.machine?.live
  const available = live?.updateAvailable
  const steps = server ? checklist(server) : checklist(undefined)
  const p = progress(steps)
  const addons = addonTab(server?.type)
  return (
    <div className="flex flex-col gap-4 pb-6">
      <PageHeader title={t('more.title')} />
      {(available || ws.updating) && can(ws.me, 'machine.manage') && (
        <Group>
          <li>
            <Row
              icon={ws.updating ? <Spinner className="size-4" /> : <span className="size-2.5 rounded-full bg-success ring-3 ring-success/20" />}
              title={ws.updating ? t('nav.updating') : t('nav.updateAvailable')}
              hint={ws.updating ? undefined : t('more.updateRow', { version: available ?? '' })}
              onClick={() => setUpdateOpen(true)}
            />
          </li>
        </Group>
      )}
      {server && can(ws.me, 'servers.manage') && (
        <Group label={server.name}>
          {hasMap(server) && (
            <li>
              <Row icon={<MapIcon />} title={t('tab.map')} hint={t('more.mapHint')} to={{ name: 'server', slug: server.slug, tab: 'map' }} />
            </li>
          )}
          {addons && (
            <li>
              <Row icon={<PuzzleIcon />} title={addons === 'mods' ? t('tab.mods') : t('tab.plugins')} to={{ name: 'server', slug: server.slug, tab: addons }} />
            </li>
          )}
          <li>
            <Row icon={<SlidersHorizontalIcon />} title={t('tab.settings')} hint={t('more.settingsHint')} to={{ name: 'server', slug: server.slug, tab: 'settings' }} />
          </li>
          {demo?.templates !== false && (
            <li>
              <Row icon={<Share2Icon />} title={t('template.menu')} hint={t('more.templateHint')} onClick={() => setSharing(true)} />
            </li>
          )}
          {!complete(steps) && p.next && (
            <li>
              <Row icon={<ListChecksIcon />} title={t('checklist.title')} hint={t('checklist.nextLower', { done: p.done, total: p.total, step: stepTitle(p.next.id, true).toLowerCase() })} to={stepRoute(p.next.id, server)} />
            </li>
          )}
        </Group>
      )}
      <Group label={t('more.servers')}>
        <li>
          <Row icon={<HouseIcon />} title={t('nav.allServers')} hint={(ws.servers ?? []).map((s) => s.name).join(', ') || t('nav.noServers')} to={{ name: 'home' }} />
        </li>
        {ws.machines.map((m) => {
          const status = machineState(m, ws).label
          return (
            <li key={m.id}>
              <Row icon={<ServerIcon />} title={m.kind === 'local' ? ws.machineName : machineLabel(m)} hint={m.live ? t('more.machineHint', { status, memory: formatMB(m.live.memoryTotalMB) }) : status} to={machineRoute(m)} />
            </li>
          )
        })}
        {can(ws.me, 'servers.create') && (
          <li>
            <Row icon={<PlusIcon />} title={t('nav.newServer')} to={{ name: 'new-server' }} />
          </li>
        )}
      </Group>
      {settingsSections.some((x) => can(ws.me, x.act)) ? (
        <Group label={t('global.title')}>
          {can(ws.me, 'team.manage') && (
            <li>
              <Row icon={<UsersIcon />} title={t('global.nav.team')} hint={t('more.teamHint')} to={{ name: 'team' }} />
            </li>
          )}
          {can(ws.me, 'machine.manage') && (
            <li>
              <Row icon={<LibraryIcon />} title={t('global.nav.addonSources')} hint={t('more.addonSourcesHint')} to={{ name: 'addon-sources' }} />
            </li>
          )}
          {can(ws.me, 'machine.manage') && (
            <li>
              <Row icon={<MessageSquareIcon />} title={t('global.nav.discord')} hint={t('more.discordHint')} to={{ name: 'discord' }} />
            </li>
          )}
          {can(ws.me, 'account.manage') && (
            <li>
              <Row icon={<BotIcon />} title={t('global.nav.aiAgents')} hint={t('more.aiAgentsHint')} to={{ name: 'ai-agents' }} />
            </li>
          )}
          {can(ws.me, 'view') && (
            <li>
              <Row icon={<ServerCogIcon />} title={t('global.nav.machines')} hint={t('more.machinesHint')} to={{ name: 'machines' }} />
            </li>
          )}
        </Group>
      ) : null}
      <Group label={t('more.you')}>
        <li>
          <Row icon={<Avatar name={ws.me.user.username} className="size-7" />} title={ws.me.user.username} hint={t('more.accountHint', { role: roleLabel(ws.me) })} to={{ name: 'account' }} />
        </li>
        <li>
          <Row icon={<SettingsIcon />} title={t('global.playkeeper')} hint={t('more.globalHint')} to={{ name: 'settings' }} />
        </li>
        <li>
          <Row icon={<CircleHelpIcon />} title={t('nav.help')} href={t('nav.helpUrl')} />
        </li>
        <li>
          <Row icon={<LogOutIcon />} title={t('nav.signOut')} onClick={() => void ws.signOut()} danger />
        </li>
      </Group>
      <UpdateDialog open={updateOpen} onOpenChange={setUpdateOpen} />
      {server && <TemplateDialog server={server} open={sharing} onOpenChange={setSharing} />}
    </div>
  )
}
