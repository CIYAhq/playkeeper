import { useEffect, useState } from 'react'
import { get } from '@/api/client'
import type { Address } from '@/api/types'
import { errorText, machineApi, useWorkspace } from '@/api/workspace'
import { Card, Notice } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { PageBody, PageHeader, PhoneBackHeader } from '@/components/app/shell'
import { LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n'
import { runningOp } from '@/lib/address'
import { linkProps } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { Choose, FreeAddress, useClaim } from './free'
import { OwnDomain } from './own'
import type { AddressProps } from './parts'

// Fast while a claim, publish or certificate runs; the agent does the slow work.
const busyPollMs = 2000
const idlePollMs = 10_000

export function MachineSettingsPage({ id }: { id: string }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const [busy, setBusy] = useState(false)
  const poll = usePoll(() => get<Address>(machineApi(id, '/address')), busy ? busyPollMs : idlePollMs, id)
  const a = poll.data
  const running = !!a && !!runningOp(a)
  useEffect(() => setBusy(running), [running])

  const m = ws.machines.find((x) => x.id === id) ?? (ws.machine?.id === id ? ws.machine : undefined)
  if (!m && ws.machines.length) {
    return (
      <PageBody>
        <p className="text-sm text-muted-foreground">{t('machine.notFound')}</p>
      </PageBody>
    )
  }
  if (!m) {
    return (
      <PageBody>
        <Loading phone={phone} />
      </PageBody>
    )
  }
  const name = m.name || m.live?.hostname || ''

  let body
  if (a) {
    body = <AddressSettings id={id} a={a} machine={name} refresh={poll.refresh} />
  } else if (poll.error) {
    const retry = (
      <Button variant="outline" size="sm" onClick={() => void poll.refresh()}>
        {t('common.tryAgain')}
      </Button>
    )
    body = (
      <Card className={phone ? 'mt-2 p-4' : undefined}>
        <Notice tone="error" stacked title={t('address.loadFailed')} action={retry}>
          {errorText(poll.error)}
        </Notice>
      </Card>
    )
  } else {
    body = <Loading phone={phone} />
  }

  if (phone) {
    return (
      <>
        <PhoneBackHeader to={{ name: 'machine', id }} label={name} title={t('address.title')} />
        <div className="flex flex-1 flex-col">{body}</div>
      </>
    )
  }
  return (
    <>
      <PageHeader
        breadcrumb={
          <span className="flex items-center gap-1.5">
            <a {...linkProps({ name: 'machine', id })} className="hover:text-foreground">
              {name}
            </a>
            <span className="text-muted-foreground/60" aria-hidden="true">
              /
            </span>
            <span className="font-semibold text-foreground">{t('nav.settings')}</span>
          </span>
        }
        title={t('machine.settings')}
        subtitle={a?.ip ? `${name}${t('common.dot')}${a.ip}` : name}
      />
      <PageBody>{body}</PageBody>
    </>
  )
}

function Loading({ phone }: { phone: boolean }) {
  if (phone) {
    return (
      <div className="flex flex-col gap-5 pt-2" aria-busy="true">
        <LoadingLabel />
        <Skeleton className="h-[120px] rounded-3xl" />
        <Skeleton className="h-11 rounded-xl" />
        <Skeleton className="h-[168px] rounded-3xl" />
      </div>
    )
  }
  return (
    <Card aria-busy="true">
      <LoadingLabel />
      <Skeleton className="h-5 w-24" />
      <Skeleton className="mt-2 h-4 w-80 max-w-full" />
      <div className="mt-4 grid gap-3 md:grid-cols-2">
        <Skeleton className="h-[62px] rounded-2xl" />
        <Skeleton className="h-[62px] rounded-2xl" />
      </div>
      <Skeleton className="mt-5 h-24 rounded-2xl" />
    </Card>
  )
}

/** The address as it is: none yet, a free playkeeper.io name, or an own domain. */
function AddressSettings(props: AddressProps) {
  // Held here so a claim that fails while the address changes under it still shows why.
  const claim = useClaim(props.id, props.refresh)
  const kind = props.a.kind
  switch (kind) {
    case '':
      return <Choose {...props} claim={claim} />
    case 'playkeeper':
      return <FreeAddress key={props.a.free?.name} {...props} claim={claim} />
    case 'own':
      return <OwnDomain {...props} />
    default: {
      const never: never = kind
      return never
    }
  }
}
