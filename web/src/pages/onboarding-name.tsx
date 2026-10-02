import { useEffect, useState, type ReactNode } from 'react'
import { CircleAlertIcon, CircleCheckIcon, GlobeIcon } from 'lucide-react'
import { get, post } from '@/api/client'
import type { Address } from '@/api/types'
import { errorText, machineApi, useWorkspace } from '@/api/workspace'
import { Spinner, useNow } from '@/components/app/bits'
import { TermsLine } from '@/components/app/terms-line'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n'
import { namedDashboard, runningOp } from '@/lib/address'
import { startingName, useFreeSuggestion } from '@/lib/free-names'
import { linkProps } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'

// Fast while a claim runs; the agent does the slow work.
const busyPollMs = 2000
const idlePollMs = 60_000

type Claim = { status: 'idle' } | { status: 'claiming'; name: string } | { status: 'claimed'; name: string } | { status: 'failed'; name: string; error: string }

/**
 * The machine check's offer of a free name, which nobody has to take: a name
 * the names service says is free, one button that claims it, and the claim
 * carrying on while setup does. It shows nothing while the machine already
 * has an address, or the names service can't be reached.
 */
export function FreeNameOffer({ id }: { id: string }) {
  const ws = useWorkspace()
  const now = useNow(5000)
  const [claim, setClaim] = useState<Claim>({ status: 'idle' })
  const [round, setRound] = useState(0)
  const [busy, setBusy] = useState(false)
  const poll = usePoll(() => get<Address>(machineApi(id, '/address')), busy || claim.status === 'claiming' ? busyPollMs : idlePollMs, id)
  const a = poll.data
  const running = !!a && !!runningOp(a)
  // A claim the machine doesn't show yet, as a poll that left before it can say, is still being got.
  const unseen = claim.status === 'claimed' && a?.kind !== 'playkeeper'
  useEffect(() => setBusy(running || unseen), [running, unseen])
  const offering = !!a && a.kind === '' && !a.names.unreachable && claim.status === 'idle'
  const suggestion = useFreeSuggestion(id, a ? startingName(ws.me.user.username, a.base) : '', { round, off: !offering })

  async function take(name: string) {
    setClaim({ status: 'claiming', name })
    try {
      await post<Address>(machineApi(id, '/address/claim'), { name, acceptTerms: true })
      await poll.refresh()
      setClaim({ status: 'claimed', name })
    } catch (e) {
      setClaim({ status: 'failed', name, error: errorText(e) })
    }
  }
  function another() {
    setClaim({ status: 'idle' })
    setRound((r) => r + 1)
  }

  if (!a) return null
  if (claim.status === 'claiming' || claim.status === 'claimed') {
    const host = claim.status === 'claimed' && !unseen && a.host ? a.host : `${claim.name}.${a.base}`
    const settled = claim.status === 'claimed' && !unseen && !running
    const url = settled ? namedDashboard(a, now) : undefined
    // A publish that failed, or a certificate that didn't come, is Machine settings' to finish.
    const op = a.operation
    const problem = settled && !url ? (op?.kind === 'address.publish' && op.status === 'failed' ? (op.error ?? '') : a.certificate?.problem?.message) : undefined
    if (problem !== undefined) {
      return (
        <Row icon={<CircleAlertIcon className="size-[18px] text-warning" aria-hidden="true" />} title={t('onboarding.name.unfinished', { address: host })} live>
          {problem && <p>{problem}</p>}
          <a {...linkProps({ name: 'machine-settings', id })} className="mt-1 inline-flex min-h-6 items-center font-medium text-primary hover:underline">
            {t('onboarding.name.toSettings')}
          </a>
        </Row>
      )
    }
    if (url) {
      return (
        <Row icon={<CircleCheckIcon className="size-[18px] text-success-foreground" aria-hidden="true" />} title={t('address.yours', { address: host })} live>
          {t('onboarding.name.doneHint')}{' '}
            <a href={url} target="_blank" rel="noreferrer" className="font-medium break-words text-primary hover:underline">
            {url}
          </a>
        </Row>
      )
    }
    return (
      <Row icon={<Spinner className="size-4" />} title={t('onboarding.name.getting', { address: host })} live>
        {t('onboarding.name.gettingHint')}
      </Row>
    )
  }
  if (claim.status === 'failed') {
    return (
      <Row icon={<CircleAlertIcon className="size-[18px] text-warning" aria-hidden="true" />} title={t('onboarding.name.failed', { address: `${claim.name}.${a.base}` })} live>
        <p>{claim.error}</p>
        <span className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-2">
          <Button size="sm" variant="outline" onClick={() => void take(claim.name)}>
            {t('common.tryAgain')}
          </Button>
          <AnotherName onClick={another} />
        </span>
      </Row>
    )
  }
  if (!offering || !suggestion) return null
  return (
    <Row icon={<GlobeIcon className="size-[18px] text-muted-foreground" aria-hidden="true" />} title={t('onboarding.name.title')}>
      <p>{t('onboarding.name.lead')}</p>
      <span className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-2">
        <Button size="sm" variant="outline" className="max-w-full" onClick={() => void take(suggestion)}>
          <span className="truncate">{t('onboarding.name.get', { address: `${suggestion}.${a.base}` })}</span>
        </Button>
        <AnotherName onClick={another} />
      </span>
      <TermsLine a={a} className="mt-2" />
    </Row>
  )
}

function AnotherName({ onClick }: { onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className="min-h-6 rounded-sm text-xs font-medium text-primary outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring max-sm:text-[13px]">
      {t('onboarding.name.another')}
    </button>
  )
}

/** One row under the machine check's list. */
function Row({ icon, title, live, children }: { icon: ReactNode; title: string; live?: boolean; children: ReactNode }) {
  return (
    <div className="mt-3 flex gap-3 rounded-2xl border border-border bg-muted/40 px-3 py-3 max-sm:rounded-3xl max-sm:bg-white max-sm:px-4" role={live ? 'status' : undefined}>
      <span className="mt-px flex size-[18px] shrink-0 items-center justify-center">{icon}</span>
      <div className="min-w-0 flex-1">
        <p className="text-[13px] font-semibold break-words max-sm:text-[15px]">{title}</p>
        <div className="text-xs text-muted-foreground max-sm:text-[13px]">{children}</div>
      </div>
    </div>
  )
}
