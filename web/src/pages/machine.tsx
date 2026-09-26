import { useEffect, useState } from 'react'
import { ChevronRightIcon, GlobeIcon, PlugIcon, PlusIcon, SettingsIcon } from 'lucide-react'
import { useCatalog } from '@/api/catalog'
import { get } from '@/api/client'
import type { Address } from '@/api/types'
import { machineApi, useWorkspace } from '@/api/workspace'
import { Card, CardHint, CardTitle, Dot, MeterRow, Progress } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { PageBody, PageHeader, PhoneBackHeader } from '@/components/app/shell'
import { LoadingLabel, MeterSkeleton } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n'
import { certState, type CertState } from '@/lib/address'
import { formatBytes, formatLongDate, formatMB, formatPercent } from '@/lib/format'
import { statusLabel, statusTone } from '@/lib/phase'
import { linkProps } from '@/lib/router'
import { newerStable, softwareLabel } from '@/lib/servers'
import { cn } from '@/lib/utils'
import { certProblemText } from './machine-settings/parts'
import { Group } from './more'

export function MachinePage({ id }: { id: string }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const m = ws.machines.find((x) => x.id === id) ?? (ws.machine?.id === id ? ws.machine : undefined)
  const { catalog } = useCatalog(m?.id)
  const address = useAddress(m?.id)
  if (!m && ws.machines.length) {
    return (
      <PageBody>
        <p className="text-sm text-muted-foreground">{t('machine.notFound')}</p>
      </PageBody>
    )
  }
  if (!m) {
    return (
      <PageBody className="grid gap-4 lg:grid-cols-2">
        <LoadingLabel />
        <Skeleton className="h-64 rounded-3xl" />
        <Skeleton className="h-64 rounded-3xl" />
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
          <Button variant="outline" render={<a {...linkProps({ name: 'machine-settings', id: m.id })} />}>
            <SettingsIcon />
            {t('machine.settings')}
          </Button>
        }
      />
      <PageBody className="grid gap-4 lg:grid-cols-2">
        {phone && <AddressRow id={m.id} address={address} />}
        <Card>
          <CardTitle>{t('machine.resources')}</CardTitle>
          <CardHint>{t('machine.resourcesHint')}</CardHint>
          {live ? (
            <div className="mt-4 flex flex-col gap-4">
              <MeterRow label={t('machine.cpu')} value={formatPercent(live.cpuPercent)} percent={live.cpuPercent} />
              <MeterRow label={t('machine.memory')} value={t('home.ofTotal', { used: formatMB(reserved), total: formatMB(live.memoryTotalMB) })} percent={live.memoryTotalMB ? (reserved / live.memoryTotalMB) * 100 : 0} />
              <a {...linkProps({ name: 'machine', id: m.id, sub: 'disk' })} className="group -mx-2 -my-1.5 rounded-xl px-2 py-1.5 outline-none transition-colors duration-(--motion-fast) ease-standard hover:bg-accent/60 focus-visible:ring-2 focus-visible:ring-ring active:bg-accent">
                <span className="flex items-baseline justify-between gap-3 text-[13px]">
                  <span className="font-medium">{t('machine.disk')}</span>
                  <span className="flex items-center gap-0.5 text-muted-foreground tabular-nums">
                    {t('home.diskFree', { free: formatBytes(live.diskFreeBytes) })}
                    <ChevronRightIcon className="size-3.5 self-center transition-transform duration-(--motion-fast) ease-standard group-hover:translate-x-0.5" aria-hidden="true" />
                  </span>
                </span>
                <Progress value={diskUsed ?? 0} className="mt-1.5" label={t('machine.disk')} />
              </a>
            </div>
          ) : ws.agentDown ? (
            <p className="mt-3 text-[13px] text-muted-foreground">{t('nav.notAnswering')}</p>
          ) : (
            <MeterSkeleton className="mt-4" />
          )}
          {address && <CertificateLine id={m.id} address={address} />}
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
              const tone = ws.stale ? 'unknown' : statusTone(s)
              const state = ws.stale ? t('status.unknown') : tone === 'online' ? `${t('status.online')}${t('common.dot')}${t('status.playing', { count: s.players?.online ?? 0 })}` : statusLabel(s)
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

/** The machine's address; undefined while loading or when it can't be read. */
function useAddress(id: string | undefined): Address | undefined {
  const [address, setAddress] = useState<Address>()
  useEffect(() => {
    if (!id) return
    let cancelled = false
    get<Address>(machineApi(id, '/address')).then(
      (a) => !cancelled && setAddress(a),
      () => undefined,
    )
    return () => {
      cancelled = true
    }
  }, [id])
  return address
}

/** The Health line: which certificate the dashboard shows, linking to where that's set up. */
function CertificateLine({ id, address }: { id: string; address: Address }) {
  const now = Date.now()
  const state = certState(address, now)
  const value = certificateValue(address, state, now)
  return (
    <a {...linkProps({ name: 'machine-settings', id })} className="mt-4 mb-3 flex items-center justify-between gap-3 rounded-md text-[13px] outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
      <span className="font-medium">{t('machine.certificate')}</span>
      <span className={cn('flex min-w-0 items-center gap-1', state === 'problem' ? 'text-warning-foreground' : 'text-muted-foreground')}>
        <span className="truncate">{value}</span>
        <ChevronRightIcon className="size-4 shrink-0" aria-hidden="true" />
      </span>
    </a>
  )
}

function certificateValue(a: Address, state: CertState, now: number): string {
  switch (state) {
    case 'none':
      return t('machine.certSelfSigned')
    case 'getting':
      return t('address.certGettingShort')
    case 'active':
      return t('machine.certActive', { date: formatLongDate(a.certificate?.notAfter ?? '') })
    case 'problem':
      return certProblemText(a, now)?.title ?? t('address.certProblem')
    default: {
      const never: never = state
      return never
    }
  }
}

/** The phone's way to the machine's address, with the address it has. */
function AddressRow({ id, address }: { id: string; address: Address | undefined }) {
  const host = address ? (address.kind ? (address.host ?? null) : null) : undefined
  return (
    <Group>
      <li>
        <a {...linkProps({ name: 'machine-settings', id })} className="flex min-h-14 w-full items-center gap-3.5 px-4 py-2 text-left">
          <GlobeIcon className="size-[22px] shrink-0 text-muted-foreground" aria-hidden="true" />
          <span className="min-w-0 flex-1">
            <span className="block text-base">{t('address.title')}</span>
            {host !== undefined && <span className="block truncate text-[13px] text-muted-foreground">{host ?? t('address.rowNone')}</span>}
          </span>
          <ChevronRightIcon className="size-5 text-muted-foreground" aria-hidden="true" />
        </a>
      </li>
    </Group>
  )
}
