import { useEffect, useState, type ReactNode } from 'react'
import { ChevronRightIcon, CircleHelpIcon, HouseIcon, ListChecksIcon, LogOutIcon, MessageSquareIcon, PlusIcon, PuzzleIcon, ServerIcon, SettingsIcon, SlidersHorizontalIcon, UsersIcon } from 'lucide-react'
import { usePhoneServer, useWorkspace } from '@/api/workspace'
import { SectionLabel, Spinner } from '@/components/app/bits'
import { stepRoute, stepTitle } from '@/components/app/checklist'
import { useIsPhone } from '@/components/app/controls'
import { Avatar, PageHeader, roleLabel } from '@/components/app/shell'
import { UpdateDialog } from '@/components/app/update'
import { t } from '@/i18n'
import { can } from '@/lib/access'
import { addonTab } from '@/lib/addons'
import { checklist, complete, progress } from '@/lib/checklist'
import { formatMB } from '@/lib/format'
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
  const cls = 'flex min-h-14 w-full items-center gap-3.5 px-4 py-2 text-left'
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

/** The phone's More tab: updates, this server's settings, every server, and you. */
export function MorePage() {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const server = usePhoneServer()
  const [updateOpen, setUpdateOpen] = useState(false)

  useEffect(() => {
    if (!phone) navigate({ name: 'home' }, true)
  }, [phone])

  const live = ws.machine?.live
  const available = live?.updateAvailable
  const steps = server ? checklist(server) : checklist(undefined)
  const p = progress(steps)
  const addons = addonTab(server?.type)
  const healthy = !ws.agentDown && !!live?.docker
  return (
    <div className="flex flex-col gap-5 pb-6">
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
          {addons && (
            <li>
              <Row icon={<PuzzleIcon />} title={addons === 'mods' ? t('tab.mods') : t('tab.plugins')} to={{ name: 'server', slug: server.slug, tab: addons }} />
            </li>
          )}
          <li>
            <Row icon={<SlidersHorizontalIcon />} title={t('tab.settings')} hint={t('more.settingsHint')} to={{ name: 'server', slug: server.slug, tab: 'settings' }} />
          </li>
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
        {ws.machine && (
          <li>
            <Row icon={<ServerIcon />} title={ws.machineName} hint={t('more.machineHint', { status: healthy ? t('nav.healthy') : t('nav.notAnswering'), memory: live ? formatMB(live.memoryTotalMB) : '' })} to={{ name: 'machine', id: ws.machine.id }} />
          </li>
        )}
        {can(ws.me, 'servers.create') && (
          <li>
            <Row icon={<PlusIcon />} title={t('nav.newServer')} to={{ name: 'new-server' }} />
          </li>
        )}
      </Group>
      {can(ws.me, 'team.manage') || can(ws.me, 'machine.manage') ? (
        <Group label={t('global.title')}>
          {can(ws.me, 'team.manage') && (
            <li>
              <Row icon={<UsersIcon />} title={t('global.nav.team')} hint={t('more.teamHint')} to={{ name: 'team' }} />
            </li>
          )}
          {can(ws.me, 'machine.manage') && (
            <li>
              <Row icon={<MessageSquareIcon />} title={t('global.nav.discord')} hint={t('more.discordHint')} to={{ name: 'discord' }} />
            </li>
          )}
        </Group>
      ) : null}
      <Group label={t('more.you')}>
        <li>
          <Row icon={<Avatar name={ws.me.user.username} className="size-7" />} title={ws.me.user.username} hint={t('more.accountHint', { role: roleLabel(ws.me) })} to={{ name: 'account' }} />
        </li>
        <li>
          <Row icon={<SettingsIcon />} title={t('nav.settings')} hint={t('more.globalHint')} to={{ name: 'settings' }} />
        </li>
        <li>
          <Row icon={<CircleHelpIcon />} title={t('nav.help')} href={t('nav.helpUrl')} />
        </li>
        <li>
          <Row icon={<LogOutIcon />} title={t('nav.signOut')} onClick={() => void ws.signOut()} danger />
        </li>
      </Group>
      <p className="px-4 text-xs text-muted-foreground">
        {t('footer.notOfficial')}
        {t('common.dot')}
        {t('footer.version', { version: ws.me.version })}
      </p>
      <UpdateDialog open={updateOpen} onOpenChange={setUpdateOpen} />
    </div>
  )
}
