import { useEffect, useState, type ReactNode } from 'react'
import { CircleArrowUpIcon, ExternalLinkIcon, RefreshCwIcon } from 'lucide-react'
import { get, post } from '@/api/client'
import type { AuditEntry, UpdateInfo } from '@/api/types'
import { errorText, machineApi, useWorkspace } from '@/api/workspace'
import { AddonSourcesCard } from '@/components/app/addon-sources'
import { Card, CardHint, CardTitle } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { PageBody, PageHeader, PhoneBackHeader } from '@/components/app/shell'
import { InlineSkeleton, TableSkeleton } from '@/components/app/skeletons'
import { UpdateDialog, useUpdateInfo } from '@/components/app/update'
import { Button } from '@/components/ui/button'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { can, settingsHome, settingsSections, type SettingsSectionName } from '@/lib/access'
import { formatDateTime, relativeTime } from '@/lib/format'
import { linkProps, navigate, type Route } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { AiAgentsSection } from './ai-agents'
import { MachineDetailsSection, MachinesSection } from './machines'

export type SettingsPage = Extract<Route, { name: 'settings' | SettingsSectionName | 'machine-details' }>

/** Settings: Playkeeper itself, add-on sources and the audit log, or a section. */
export function GlobalSettingsPage({ page }: { page: SettingsPage }) {
  switch (page.name) {
    case 'settings':
      return <GeneralSettings />
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

/** The sections of Settings the account can use, beside each Settings page on desktop. */
function SectionsNav({ current }: { current?: SettingsSectionName }) {
  const ws = useWorkspace()
  return (
    <nav aria-label={t('global.nav.label')} className="flex flex-col gap-0.5">
      {settingsSections
        .filter((s) => can(ws.me, s.act))
        .map((s) => (
          <a
            key={s.route.name}
            {...linkProps(s.route)}
            aria-current={s.route.name === current ? 'page' : undefined}
            className={cn(
              'flex h-8 items-center rounded-lg px-2.5 text-[13px] font-medium text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring',
              s.route.name === current && 'bg-muted text-foreground',
            )}
          >
            {t(s.label)}
          </a>
        ))}
    </nav>
  )
}

/** A Settings section: the sections list beside it on desktop, a back link on phones (to More, where the sections are listed). */
function SettingsSection({ current, phoneBack, children }: { current: SettingsSectionName; phoneBack?: { to: Route; label: string }; children: ReactNode }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const here = settingsSections.find((s) => s.route.name === current && can(ws.me, s.act))
  useEffect(() => {
    if (!here) navigate(settingsHome(ws.me), true)
  }, [here, ws.me])
  if (!here) return null
  if (phone) {
    return (
      <>
        <PhoneBackHeader to={phoneBack?.to ?? { name: 'more' }} label={phoneBack?.label ?? t('global.title')} title={phoneBack ? undefined : t(here.label)} />
        <div className="flex flex-col gap-4 pt-2 pb-6">{children}</div>
      </>
    )
  }
  return (
    <>
      <PageHeader title={t('global.title')} />
      <PageBody className="grid max-w-[1240px] grid-cols-[200px_minmax(0,1fr)] items-start gap-7">
        <SectionsNav current={current} />
        <div className="flex min-w-0 flex-col gap-5">{children}</div>
      </PageBody>
    </>
  )
}

/** Playkeeper itself, add-on sources and the audit log, with the sections list beside them on desktop. */
function GeneralSettings() {
  const phone = useIsPhone()
  const hash = window.location.hash
  useEffect(() => {
    if (!hash) return
    document.getElementById(hash.slice(1))?.scrollIntoView({ block: 'start' })
  }, [hash])
  const cards = (
    <>
      <PlaykeeperCard />
      <AddonSourcesCard />
      <AuditCard />
      <Card as="section" aria-labelledby="about-title">
        <CardTitle id="about-title">{t('global.about')}</CardTitle>
        <p className="mt-1 text-[13px] text-muted-foreground">{t('global.aboutBody')}</p>
        <p className="mt-3 text-xs text-muted-foreground">{t('footer.notOfficial')}</p>
        <a href={t('global.noticesUrl')} target="_blank" rel="noreferrer" className="mt-3 inline-flex items-center gap-1 self-start text-xs font-medium text-primary hover:underline">
          {t('global.notices')}
          <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
        </a>
      </Card>
    </>
  )
  if (phone) {
    return (
      <>
        <PhoneBackHeader to={{ name: 'more' }} label={t('nav.more')} />
        <PageHeader title={t('global.title')} subtitle={t('global.lead')} />
        <PageBody className="flex max-w-[860px] flex-col gap-4">{cards}</PageBody>
      </>
    )
  }
  return (
    <>
      <PageHeader title={t('global.title')} subtitle={t('global.lead')} />
      <PageBody className="grid max-w-[1240px] grid-cols-[200px_minmax(0,1fr)] items-start gap-7">
        <SectionsNav />
        <div className="flex max-w-[860px] min-w-0 flex-col gap-4">{cards}</div>
      </PageBody>
    </>
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
        {info?.supported && (
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

function AuditCard() {
  const ws = useWorkspace()
  const audit = usePoll(() => get<AuditEntry[]>('/api/audit'), 30_000)
  const serverName = (id?: string) => (id ? (ws.servers?.find((s) => s.id === id)?.name ?? '') : '')
  const rows = (audit.data ?? []).slice(0, 100)
  return (
    <Card as="section" aria-labelledby="audit-title" id="audit" className="scroll-mt-4">
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
              <tr key={`${e.source}-${e.id}`} className="h-11 border-t border-border align-top">
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
                <td className="px-3 py-2">{serverName(e.serverId)}</td>
                <td className={cn('px-3 py-2', e.result === 'succeeded' ? 'text-success-foreground' : e.result === 'failed' || e.result === 'refused' ? 'text-destructive-foreground' : 'text-muted-foreground')}>{e.result}</td>
                <td className="max-w-[280px] px-3 py-2 break-words text-muted-foreground">{e.detail}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  )
}
