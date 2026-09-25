import { ChevronRightIcon, PlugIcon, PlusIcon, SettingsIcon } from 'lucide-react'
import { useCatalog } from '@/api/catalog'
import { useWorkspace } from '@/api/workspace'
import { Card, CardHint, CardTitle, Dot, MeterRow } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { PageBody, PageHeader, PhoneBackHeader } from '@/components/app/shell'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n'
import { formatBytes, formatMB, formatPercent } from '@/lib/format'
import { phaseLabel, phaseTone } from '@/lib/phase'
import { linkProps } from '@/lib/router'
import { newerStable, softwareLabel } from '@/lib/servers'

export function MachinePage({ id }: { id: string }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const m = ws.machines.find((x) => x.id === id) ?? (ws.machine?.id === id ? ws.machine : undefined)
  const { catalog } = useCatalog(m?.id)
  if (!m) {
    return (
      <PageBody>
        <p className="text-sm text-muted-foreground">{ws.machines.length ? t('machine.notFound') : t('common.loading')}</p>
      </PageBody>
    )
  }
  const live = m.live
  const name = m.name || live?.hostname || ''
  const reserved = live ? live.systemReserveMB + live.serversMemoryMB : 0
  const diskUsed = live?.diskTotalBytes && live.diskFreeBytes !== undefined ? ((live.diskTotalBytes - live.diskFreeBytes) / live.diskTotalBytes) * 100 : undefined
  const subtitle = live ? t('machine.lead', { os: live.os, cpus: live.cpus, memory: formatMB(live.memoryTotalMB), disk: formatBytes(live.diskTotalBytes) }) : ws.agentDown ? t('nav.notAnswering') : undefined
  return (
    <>
      {phone && <PhoneBackHeader to={{ name: 'more' }} label={t('nav.more')} />}
      <PageHeader
        breadcrumb={name}
        title={name}
        subtitle={subtitle}
        actions={
          <Button variant="outline" render={<a {...linkProps({ name: 'settings' })} />}>
            <SettingsIcon />
            {t('machine.settings')}
          </Button>
        }
      />
      <PageBody className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardTitle>{t('machine.resources')}</CardTitle>
          <CardHint>{t('machine.resourcesHint')}</CardHint>
          {live ? (
            <div className="mt-4 flex flex-col gap-4">
              <MeterRow label={t('machine.cpu')} value={formatPercent(live.cpuPercent)} percent={live.cpuPercent} />
              <MeterRow label={t('machine.memory')} value={t('home.ofTotal', { used: formatMB(reserved), total: formatMB(live.memoryTotalMB) })} percent={live.memoryTotalMB ? (reserved / live.memoryTotalMB) * 100 : 0} />
              <MeterRow label={t('machine.disk')} value={t('home.diskFree', { free: formatBytes(live.diskFreeBytes) })} percent={diskUsed} />
            </div>
          ) : (
            <p className="mt-3 text-[13px] text-muted-foreground">{ws.agentDown ? t('nav.notAnswering') : t('common.loading')}</p>
          )}
          <div className="mt-auto flex flex-wrap justify-between gap-2 border-t border-border pt-3 text-xs text-muted-foreground">
            <span>{t('machine.sampled')}</span>
            {live && (
              <span>
                {t('machine.version', { version: live.agentVersion })}
                {t('common.dot')}
                {live.docker ? t('machine.docker', { version: live.dockerVersion ?? '' }) : t('machine.dockerDown')}
              </span>
            )}
          </div>
        </Card>
        <Card>
          <div className="flex items-center justify-between gap-3">
            <CardTitle>{t('machine.servers')}</CardTitle>
            <Button variant="outline" size="sm" render={<a {...linkProps({ name: 'new-server' })} />}>
              <PlusIcon />
              {t('nav.newServer')}
            </Button>
          </div>
          <ul className="mt-3 flex flex-col">
            {(ws.servers ?? []).map((s) => {
              const tone = ws.stale ? 'unknown' : phaseTone(s.phase)
              const state = ws.stale ? t('status.unknown') : tone === 'online' ? `${t('status.online')}${t('common.dot')}${t('status.playing', { count: s.players?.online ?? 0 })}` : phaseLabel(s.phase)
              return (
                <li key={s.id} className="border-t border-border first:border-t-0">
                  <a {...linkProps({ name: 'server', slug: s.slug, tab: 'overview' })} className="flex min-h-14 items-center gap-3 py-2 outline-none hover:bg-accent/30 focus-visible:ring-2 focus-visible:ring-ring">
                    <Dot tone={tone} />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-sm font-semibold">{s.name}</span>
                      <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                        {softwareLabel(s)}
                        {newerStable(s.config, catalog?.versions) && <span className="size-1.5 rounded-full bg-success" aria-label={t('card.updateDot')} role="img" />}
                      </span>
                    </span>
                    <span className="text-xs text-muted-foreground">{state}</span>
                    <ChevronRightIcon className="size-4 text-muted-foreground" aria-hidden="true" />
                  </a>
                </li>
              )
            })}
          </ul>
          <div className="mt-auto flex items-center gap-3 rounded-2xl border border-dashed border-input px-3 py-2.5 text-[13px] text-muted-foreground">
            <PlugIcon className="size-4" aria-hidden="true" />
            <span className="flex-1">{t('machine.connect')}</span>
            <span className="text-xs">{t('common.later')}</span>
          </div>
        </Card>
      </PageBody>
    </>
  )
}
