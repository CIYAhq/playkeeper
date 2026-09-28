import { useEffect, useState, type ReactNode } from 'react'
import { ChevronRightIcon, CircleArrowUpIcon, ExternalLinkIcon, RefreshCwIcon } from 'lucide-react'
import { get, post, put } from '@/api/client'
import type { AuditEntry, UpdateInfo, UsageMachine, UsageStats, UsageStatsView } from '@/api/types'
import { errorText, machineApi, useWorkspace } from '@/api/workspace'
import { AddonSourcesCard } from '@/components/app/addon-sources'
import { Card, CardHint, CardTitle } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { PageBody, PageHeader, PhoneBackHeader } from '@/components/app/shell'
import { InlineSkeleton, ListSkeleton, TableSkeleton } from '@/components/app/skeletons'
import { UpdateDialog, useUpdateInfo } from '@/components/app/update'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Switch } from '@/components/ui/switch'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { can, settingsHome, settingsSections, type SettingsSectionName } from '@/lib/access'
import { formatDateTime, relativeTime } from '@/lib/format'
import { machineLabel } from '@/lib/machines'
import { linkProps, navigate, type Route } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { AiAgentsSection } from './ai-agents'
import { DiscordSettingsSection } from './discord'
import { MachineDetailsSection, MachinesSection } from './machines'
import { TeamSection } from './team'

export type SettingsPage = Extract<Route, { name: 'settings' | SettingsSectionName | 'machine-details' }>

/** Settings: Playkeeper itself, the audit log and about, or a section. */
export function GlobalSettingsPage({ page }: { page: SettingsPage }) {
  switch (page.name) {
    case 'settings':
      return <GeneralSettings />
    case 'team':
      return (
        <SettingsSection current="team">
          <TeamSection />
        </SettingsSection>
      )
    case 'addon-sources':
      return (
        <SettingsSection current="addon-sources">
          <AddonSourcesCard key={page.machine ?? ''} machine={page.machine} />
        </SettingsSection>
      )
    case 'discord':
      return (
        <SettingsSection current="discord">
          <DiscordSettingsSection />
        </SettingsSection>
      )
    case 'ai-agents':
      return (
        <SettingsSection current="ai-agents">
          <AiAgentsSection />
        </SettingsSection>
      )
    case 'machines':
      return (
        <SettingsSection current="machines">
          <MachinesSection />
        </SettingsSection>
      )
    case 'machine-details':
      return (
        <SettingsSection current="machines" phoneBack={{ to: { name: 'machines' }, label: t('global.nav.machines') }}>
          <MachineDetailsSection key={page.id} id={page.id} />
        </SettingsSection>
      )
    default: {
      const unreachable: never = page
      return unreachable
    }
  }
}

/** A Settings section: the sections list beside it on desktop, a back link on phones (to More, where the sections are listed, unless phoneBack says where). */
function SettingsSection({ current, phoneBack, children }: { current: SettingsSectionName | 'settings'; phoneBack?: { to: Route; label: string }; children: ReactNode }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const sections = settingsSections.filter((s) => can(ws.me, s.act))
  const general = current === 'settings'
  const here = sections.find((s) => s.route.name === current)
  useEffect(() => {
    if (!general && !here) navigate(settingsHome(ws.me), true)
  }, [general, here, ws.me])
  if (!general && !here) return null
  const item = (active: boolean) => cn('flex h-8 items-center rounded-lg px-2.5 text-[13px] font-medium text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring', active && 'bg-muted text-foreground')
  if (phone) {
    return (
      <>
        <PhoneBackHeader to={phoneBack?.to ?? { name: 'more' }} label={phoneBack?.label ?? t('nav.more')} title={phoneBack ? undefined : here ? t(here.label) : t('global.playkeeper')} />
        <div className="flex flex-col gap-4 pt-2 pb-6">{children}</div>
      </>
    )
  }
  return (
    <>
      <PageHeader title={t('global.title')} sticky />
      <PageBody className="grid max-w-[1240px] grid-cols-[200px_minmax(0,1fr)] items-start gap-7">
        <nav aria-label={t('global.nav.label')} className="sticky top-[calc(var(--header-h,0px)+24px)] flex flex-col gap-0.5">
          {sections.map((s) => (
            <a key={s.route.name} {...linkProps(s.route)} aria-current={s === here ? 'page' : undefined} className={item(s === here)}>
              {t(s.label)}
            </a>
          ))}
          <a {...linkProps({ name: 'settings' })} aria-current={general ? 'page' : undefined} className={item(general)}>
            {t('global.playkeeper')}
          </a>
        </nav>
        <div key={current} className="flex min-w-0 flex-col gap-4">
          {children}
        </div>
      </PageBody>
    </>
  )
}

/** Playkeeper itself, the audit log and about; the account has its own page. */
function GeneralSettings() {
  const ws = useWorkspace()
  const phone = useIsPhone()
  // Opening the page at a section jumps straight to it; a link to a section of
  // the page already open scrolls there by itself (navigate in router.ts).
  useEffect(() => {
    const hash = window.location.hash
    if (hash) document.getElementById(hash.slice(1))?.scrollIntoView({ block: 'start' })
  }, [])
  return (
    <SettingsSection current="settings">
      <PlaykeeperCard />
      <UsageStatsCard />
      {can(ws.me, 'audit.view') && <AuditCard phone={phone} />}
      <Card as="section" aria-labelledby="about-title">
        <CardTitle id="about-title">{t('global.about')}</CardTitle>
        <p className="mt-1 text-[13px] text-muted-foreground">{t('global.aboutBody')}</p>
        {/* Desktop pages carry this line in the footer; phones have none. */}
        {phone && <p className="mt-3 text-xs text-muted-foreground">{t('footer.notOfficial')}</p>}
        <a href={t('global.noticesUrl')} target="_blank" rel="noreferrer" className="mt-3 inline-flex items-center gap-1 self-start text-xs font-medium text-primary hover:underline">
          {t('global.notices')}
          <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
        </a>
      </Card>
    </SettingsSection>
  )
}

function PlaykeeperCard() {
  const ws = useWorkspace()
  const { info, setInfo, error } = useUpdateInfo(true)
  const [checking, setChecking] = useState(false)
  const [open, setOpen] = useState(false)

  async function check() {
    if (!ws.machine) return
    setChecking(true)
    try {
      setInfo(await post<UpdateInfo>(machineApi(ws.machine.id, '/update/check')))
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setChecking(false)
    }
  }

  const last = info?.lastResult
  return (
    <Card as="section" aria-labelledby="pk-title" id="updates" className="scroll-mt-4">
      <CardTitle id="pk-title">{t('global.playkeeper')}</CardTitle>
      <CardHint>{t('machine.version', { version: info?.current ?? ws.me.version })}</CardHint>
      <div className="mt-4 flex flex-wrap items-center gap-3 border-t border-border pt-4">
        <p className="min-w-0 flex-1 text-[13px]">
          {error ? (
            <span className="text-destructive-foreground">{error}</span>
          ) : !info ? (
            <InlineSkeleton className="w-48" />
          ) : !info.supported ? (
            <span className="text-muted-foreground">{info.reason ?? t('update.unsupported')}</span>
          ) : ws.updating ? (
            <span className="font-medium text-info-foreground">{t('update.updatingTitle', { version: ws.updating })}</span>
          ) : info.available && info.latest ? (
            <span>
              <span className="font-semibold">{t('update.title', { version: info.latest })}</span>
              {info.checkError && <span className="block text-xs text-destructive-foreground">{info.checkError}</span>}
            </span>
          ) : (
            <span className="text-muted-foreground">{info.checkedAt ? t('update.latestChecked', { time: relativeTime(info.checkedAt) }) : t('update.latest')}</span>
          )}
          {last && (
            <span className={cn('mt-1 block text-xs', last.outcome === 'updated' ? 'text-muted-foreground' : 'text-destructive-foreground')}>
              {last.outcome === 'updated' ? t('update.lastUpdated', { from: last.from, to: last.to, time: relativeTime(last.finishedAt) }) : t('update.lastFailed', { to: last.to, from: last.from, error: last.error ?? '' })}
            </span>
          )}
        </p>
        {info?.supported && can(ws.me, 'machine.manage') && (
          <div className="flex gap-2">
            <Button variant="ghost" size="sm" onClick={check} loading={checking} disabledReason={ws.updating ? t('reason.busy', { what: t('op.update') }) : undefined}>
              <RefreshCwIcon />
              {t('update.check')}
            </Button>
            {(info.available || ws.updating) && (
              <Button size="sm" onClick={() => setOpen(true)}>
                <CircleArrowUpIcon />
                {ws.updating ? t('nav.updating') : t('update.update')}
              </Button>
            )}
          </div>
        )}
      </div>
      <UpdateDialog open={open} onOpenChange={setOpen} />
    </Card>
  )
}

/** Why usage stats are as they are, in one line. */
function usageLine(st: UsageStats): string {
  if (st.on) {
    if (st.reason === 'env') return t('usage.onEnv', { variable: st.variable ?? '' })
    return st.lastSent ? t('usage.onSent', { time: relativeTime(st.lastSent) }) : t('usage.onNotYet')
  }
  switch (st.reason) {
    case 'env':
      return t('usage.offEnv', { variable: st.variable ?? '' })
    case 'install':
      return t('usage.offInstall')
    case 'dev':
      return t('usage.offDev')
    case 'default':
    case 'settings':
      return t('usage.off')
    default: {
      const unreachable: never = st.reason
      return unreachable
    }
  }
}

/** A joined machine's usage stats, in a word or two. */
function usageMachineText(m: UsageMachine): string {
  if (!m.stats) return t('usage.machineAway')
  if (m.stats.on) return t('usage.machineOn')
  return m.stats.canChange ? t('usage.machineOff') : t('usage.machineOffThere')
}

/** Anonymous usage stats: one switch for every machine, and exactly what is sent. */
function UsageStatsCard() {
  const ws = useWorkspace()
  const usage = usePoll(() => get<UsageStatsView>('/api/usage-stats'), 60_000)
  const [changed, setChanged] = useState<UsageStatsView>()
  const [saving, setSaving] = useState(false)
  const [open, setOpen] = useState(false)
  const st = changed ?? usage.data
  const manage = can(ws.me, 'machine.manage')

  async function change(on: boolean) {
    setSaving(true)
    try {
      setChanged(await put<UsageStatsView>('/api/usage-stats', { on }))
      toastManager.add({ title: on ? t('usage.turnedOn') : t('usage.turnedOff'), type: 'success' })
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setSaving(false)
    }
  }

  const locked = !st ? undefined : st.reason === 'dev' ? t('usage.offDev') : !st.canChange ? usageLine(st) : saving ? t('reason.saving') : undefined
  const name = (m: UsageMachine) => machineLabel(ws.machines.find((x) => x.id === m.id)) || m.name
  return (
    <Card as="section" aria-labelledby="usage-title" id="usage-stats" className="scroll-mt-4">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <CardTitle id="usage-title">{t('usage.title')}</CardTitle>
          <CardHint>{t('usage.hint')}</CardHint>
        </div>
        {manage && st && <Switch checked={st.on} onCheckedChange={(c) => void change(c)} aria-label={t('usage.switch')} disabled={!!locked} title={locked} />}
      </div>
      <div className="mt-4 flex flex-col gap-2 border-t border-border pt-4 text-[13px]">
        {usage.error && !st ? (
          <p className="text-destructive-foreground">{errorText(usage.error)}</p>
        ) : !st ? (
          <InlineSkeleton className="w-56" />
        ) : (
          <p className="text-muted-foreground">{usageLine(st)}</p>
        )}
        {st && st.machines.length > 0 && (
          <ul className="flex flex-col gap-1 text-xs text-muted-foreground" aria-label={t('usage.machines')}>
            {st.machines.map((m) => (
              <li key={m.id} className="flex justify-between gap-3">
                <span className="min-w-0 truncate">{name(m)}</span>
                <span className="shrink-0">{usageMachineText(m)}</span>
              </li>
            ))}
          </ul>
        )}
        {st && (
          <Collapsible open={open} onOpenChange={setOpen}>
            <CollapsibleTrigger className="inline-flex items-center gap-1 rounded text-xs font-medium text-primary outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
              <ChevronRightIcon className={cn('size-3.5 transition-transform', open && 'rotate-90')} aria-hidden="true" />
              {t('usage.whatsSent')}
            </CollapsibleTrigger>
            <CollapsiblePanel>
              <p className="mt-2 text-xs text-muted-foreground">{t('usage.whatsSentHint', { service: st.service.replace(/^https?:\/\//, '') })}</p>
              <pre className="mt-2 overflow-x-auto rounded-xl bg-muted p-3 font-mono text-xs leading-5">{JSON.stringify(st.report, null, 2)}</pre>
              <a href={t('usage.learnMoreUrl')} target="_blank" rel="noreferrer" className="mt-2 inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline">
                {t('usage.learnMore')}
                <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
              </a>
            </CollapsiblePanel>
          </Collapsible>
        )}
      </div>
    </Card>
  )
}

function AuditCard({ phone }: { phone: boolean }) {
  const ws = useWorkspace()
  const audit = usePoll(() => get<AuditEntry[]>('/api/audit'), 30_000)
  const serverName = (id?: string) => (id ? (ws.servers?.find((s) => s.id === id)?.name ?? '') : '')
  // With more than one machine, an agent's row says whose it is.
  const machineName = (e: AuditEntry) => (e.source === 'agent' && ws.machines.length > 1 ? machineLabel(ws.machines.find((m) => m.id === e.machineId)) : '')
  const rows = audit.data ?? []
  const tone = (e: AuditEntry) => (e.result === 'succeeded' ? 'text-success-foreground' : e.result === 'failed' || e.result === 'refused' ? 'text-destructive-foreground' : 'text-muted-foreground')
  if (phone) {
    return (
      <Card as="section" id="audit" className="scroll-mt-4">
        <CardTitle id="audit-title">{t('global.audit')}</CardTitle>
        <CardHint>{t('global.auditHint')}</CardHint>
        <div className="mt-3 max-h-[480px] overflow-auto" tabIndex={0} role="region" aria-labelledby="audit-title">
          {!audit.data ? (
            <ListSkeleton rows={5} rowClassName="flex min-h-14 items-center gap-3 border-t border-border py-2" />
          ) : rows.length === 0 ? (
            <p className="border-t border-border py-3 text-[13px] text-muted-foreground">{t('global.auditEmpty')}</p>
          ) : (
            <ul>
              {rows.map((e) => (
                <li key={`${e.source}-${e.id}`} className="border-t border-border py-2 text-[13px]">
                  <p className="flex flex-wrap items-baseline gap-x-2">
                    <span className="font-mono text-xs">{e.action}</span>
                    {serverName(e.serverId) && <span>{serverName(e.serverId)}</span>}
                    {machineName(e) && <span className="text-muted-foreground">{t('machines.onMachine', { name: machineName(e) })}</span>}
                    <span className={cn('ml-auto', tone(e))}>{e.result}</span>
                  </p>
                  <p className="text-muted-foreground">{[e.actor, formatDateTime(e.ts)].filter(Boolean).join(t('common.dot'))}</p>
                  {e.detail && <p className="break-words text-muted-foreground">{e.detail}</p>}
                </li>
              ))}
            </ul>
          )}
        </div>
      </Card>
    )
  }
  return (
    <Card as="section" id="audit" className="scroll-mt-4">
      <CardTitle id="audit-title">{t('global.audit')}</CardTitle>
      <CardHint>{t('global.auditHint')}</CardHint>
      <div className="mt-4 max-h-[480px] overflow-auto rounded-2xl border border-border" tabIndex={0} role="region" aria-labelledby="audit-title">
        <table className="w-full min-w-[640px] text-[13px]">
          <thead className="sticky top-0 bg-muted text-left text-xs text-muted-foreground">
            <tr className="h-9">
              <th className="px-3 font-medium">{t('global.col.when')}</th>
              <th className="px-3 font-medium">{t('global.col.who')}</th>
              <th className="px-3 font-medium">{t('global.col.what')}</th>
              <th className="px-3 font-medium">{t('global.col.server')}</th>
              <th className="px-3 font-medium">{t('global.col.result')}</th>
              <th className="px-3 font-medium">{t('global.col.details')}</th>
            </tr>
          </thead>
          <tbody>
            {!audit.data && <TableSkeleton rows={5} cols={['start', 'start', 'start', 'start', 'start', 'start']} rowClassName="h-11 border-t border-border" />}
            {audit.data && rows.length === 0 && (
              <tr>
                <td colSpan={6} className="px-3 py-4 text-muted-foreground">
                  {t('global.auditEmpty')}
                </td>
              </tr>
            )}
            {rows.map((e) => (
              <tr key={`${e.source}-${e.machineId ?? ''}-${e.id}`} className="h-11 border-t border-border align-top">
                <td className="px-3 py-2 whitespace-nowrap text-muted-foreground">{formatDateTime(e.ts)}</td>
                <td className="px-3 py-2">
                  {e.actorKind && e.actorName ? (
                    <>
                      {e.actorName}
                      <span className="block text-xs text-muted-foreground">{e.actorKind === 'token' ? t('global.actorToken') : t('global.actorCli')}</span>
                    </>
                  ) : (
                    e.actor
                  )}
                </td>
                <td className="px-3 py-2 font-mono text-xs">{e.action}</td>
                <td className="px-3 py-2">
                  {serverName(e.serverId)}
                  {machineName(e) && <span className="block text-xs text-muted-foreground">{t('machines.onMachine', { name: machineName(e) })}</span>}
                </td>
                <td className={cn('px-3 py-2', tone(e))}>{e.result}</td>
                <td className="max-w-[280px] px-3 py-2 break-words text-muted-foreground">{e.detail}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  )
}
