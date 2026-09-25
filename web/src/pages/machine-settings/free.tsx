import { Fragment, useId, useState, type ReactNode } from 'react'
import { CircleCheckIcon, CircleIcon, PencilIcon } from 'lucide-react'
import { get, post } from '@/api/client'
import type { Address, NameAvailability } from '@/api/types'
import { machineApi, useWorkspace } from '@/api/workspace'
import { Card, CardHint, CardTitle, SectionLabel, Spinner, useNow } from '@/components/app/bits'
import { CardGroup, ChoiceCard, useIsPhone } from '@/components/app/controls'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupAddon, InputGroupInput, InputGroupText } from '@/components/ui/input-group'
import { Radio } from '@/components/ui/radio-group'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { certState, claimStep, dashboardURL, freeServers, freeStage, nameProblem, normalizeName, runningOp, type FreeStage, type NameProblem } from '@/lib/address'
import { formatCountdown, formatDate } from '@/lib/format'
import { cn } from '@/lib/utils'
import { Group } from '../more'
import { refusal } from '../two-factor'
import { OwnSteps } from './own'
import {
  CertificateNotice,
  ConfirmDialog,
  CopyIconButton,
  dashboardRow,
  DoneHeader,
  DoneView,
  failure,
  isFailure,
  LapsedHeader,
  openLabel,
  PhoneAction,
  ResultBlock,
  RetryButton,
  TermsLine,
  UnreachableNotice,
  useLookup,
  type AddressProps,
  type AddressRowData,
  type Failure,
} from './parts'

const checkDelayMs = 400
const ntpCommand = 'sudo timedatectl set-ntp true'

type ClaimState = { status: 'idle' } | { status: 'claiming'; name: string } | { status: 'failed'; name: string; failure: Failure }

export interface Claim {
  state: ClaimState
  claim: (name: string) => Promise<void>
  reset: () => void
}

const idle: ClaimState = { status: 'idle' }

/** Claiming a name: the request itself, then the page reads the address again. */
export function useClaim(id: string, refresh: () => Promise<void>): Claim {
  const [state, setState] = useState<ClaimState>(idle)
  async function claim(name: string) {
    setState({ status: 'claiming', name })
    try {
      await post<Address>(machineApi(id, '/address/claim'), { name, acceptTerms: true })
      await refresh()
      setState(idle)
    } catch (e) {
      setState({ status: 'failed', name, failure: failure(e) })
    }
  }
  return { state, claim, reset: () => setState((s) => (s.status === 'idle' ? s : idle)) }
}

/** The name the page starts with: the user's own, when it can be one. */
function startingName(username: string, base: string): string {
  const n = normalizeName(username, base)
  return nameProblem(n) ? '' : n
}

type Choice = 'free' | 'own'

/** No address yet: free name or own domain. */
export function Choose({ id, a, machine, refresh, claim }: AddressProps & { claim: Claim }) {
  const phone = useIsPhone()
  const [choice, setChoice] = useState<Choice>('free')
  if (claim.state.status === 'claiming') return <Claiming address={`${claim.state.name}.${a.base}`} machine={machine} ip={a.ip} step={0} />
  const label = t('address.choose', { machine })
  const choose = (c: Choice) => {
    claim.reset()
    setChoice(c)
  }
  const picker = choice === 'free' ? <FreePicker id={id} a={a} machine={machine} claim={claim} onUseOwn={() => choose('own')} /> : <OwnSteps id={id} a={a} machine={machine} refresh={refresh} />
  if (phone) {
    return (
      <div className="flex flex-1 flex-col gap-5 pt-2 pb-6">
        <section>
          <SectionLabel className="px-4 pb-2">{label}</SectionLabel>
          <CardGroup value={choice} onChange={choose} label={label} className="overflow-hidden rounded-3xl border border-border bg-white">
            <PhoneChoice value="free" title={t('address.free')} hint={choice === 'free' ? t('address.freeHintShort') : undefined} />
            <PhoneChoice value="own" title={t('address.own')} hint={choice === 'free' ? t('address.ownHintShort') : undefined} />
          </CardGroup>
        </section>
        {picker}
      </div>
    )
  }
  return (
    <Card>
      <CardTitle>{t('address.title')}</CardTitle>
      <CardHint>{a.ip ? t('address.lead', { ip: a.ip }) : t('address.leadNoIp')}</CardHint>
      <CardGroup value={choice} onChange={choose} label={label} className="mt-4 grid gap-3 md:grid-cols-2">
        <ChoiceCard value="free" radio="start" className="gap-3 px-4 py-3.5">
          <span className="block text-sm font-semibold">{t('address.free')}</span>
          <span className="mt-0.5 block text-[13px] text-muted-foreground">{t('address.freeHint')}</span>
        </ChoiceCard>
        <ChoiceCard value="own" radio="start" className="gap-3 px-4 py-3.5">
          <span className="block text-sm font-semibold">{t('address.own')}</span>
          <span className="mt-0.5 block text-[13px] text-muted-foreground">{t('address.ownHint')}</span>
        </ChoiceCard>
      </CardGroup>
      {picker}
    </Card>
  )
}

function PhoneChoice({ value, title, hint }: { value: Choice; title: string; hint?: string }) {
  return (
    <label className="flex min-h-[52px] cursor-pointer items-center gap-3 border-b border-border px-4 py-2 last:border-b-0 has-[:focus-visible]:bg-muted/50">
      <span className="min-w-0 flex-1">
        <span className="block text-base">{title}</span>
        {hint && <span className="block text-[13px] text-muted-foreground">{hint}</span>}
      </span>
      <Radio value={value} className="shrink-0" />
    </label>
  )
}

/**
 * Picking a name, with its availability as you type and the addresses it
 * gives. `current` is the name being changed, if any.
 */
function FreePicker({ id, a, machine, claim, current, onUseOwn, onCancel }: { id: string; a: Address; machine: string; claim: Claim; current?: string; onUseOwn?: () => void; onCancel?: () => void }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const inputId = useId()
  const statusId = useId()
  const [raw, setRaw] = useState(() => (current ? '' : startingName(ws.me.user.username, a.base)))
  const name = normalizeName(raw, a.base)
  const problem = nameProblem(name)
  const mine = !!current && name === current
  const { answer, forget } = useLookup(problem || mine ? '' : name, (n) => get<NameAvailability>(machineApi(id, `/address/available?name=${encodeURIComponent(n)}`)), checkDelayMs)
  const available = !problem && !mine && !!answer && !isFailure(answer) && answer.available
  const address = `${name}.${a.base}`

  const claimFailed = claim.state.status === 'failed' ? claim.state : undefined
  let failed: { failure: Failure; retry: () => void } | undefined
  if (claimFailed) failed = { failure: claimFailed.failure, retry: () => void claim.claim(claimFailed.name) }
  else if (isFailure(answer)) failed = { failure: answer, retry: forget }

  function change(v: string) {
    setRaw(v)
    claim.reset()
  }
  function pick(n: string) {
    change(n)
    document.getElementById(inputId)?.focus()
  }
  const submit = () => {
    if (available) void claim.claim(name)
  }

  const shown = name || t('address.namePlaceholder')
  const rows: AddressRowData[] = [
    ...freeServers(previewServers(a, ws.servers)).map((s) => ({ id: s.id, label: s.name, value: `${s.slug}.${shown}.${a.base}` })),
    dashboardRow(`${shown}.${a.base}`, a.panelPort),
  ]
  const claimLabel = name && !problem ? t('address.claim', { address }) : t('address.claimPlain')
  const input = (
    <InputGroup className="max-sm:h-11">
      <InputGroupInput
        id={inputId}
        value={raw}
        onChange={(e) => change(e.target.value)}
        onBlur={() => name !== raw && setRaw(name)}
        onKeyDown={(e) => e.key === 'Enter' && submit()}
        placeholder={t('address.namePlaceholder')}
        autoComplete="off"
        autoCapitalize="none"
        spellCheck={false}
        enterKeyHint="go"
        aria-invalid={problem === 'characters' || problem === 'long' || (!!answer && !isFailure(answer) && answer.code === 'invalid_name') || undefined}
        aria-describedby={statusId}
        className="max-sm:text-[17px]"
      />
      <InputGroupAddon align="inline-end" className="self-stretch rounded-r-[inherit] border-l border-input bg-muted/60 px-3 max-sm:text-[17px]">
        <InputGroupText>{`.${a.base}`}</InputGroupText>
      </InputGroupAddon>
    </InputGroup>
  )
  const status = nameStatus({ name, address, problem, mine, answer, onPick: pick })
  const failedBlock = failed && <ClaimFailure failure={failed.failure} machine={machine} onRetry={failed.retry} onUseOwn={onUseOwn} touch={phone} />
  const changeHint = current && <p className="text-xs text-muted-foreground">{t('address.changeHint', { address: `${current}.${a.base}` })}</p>

  if (phone) {
    return (
      <>
        <section>
          <SectionLabel className="px-4 pb-2">
            <label htmlFor={inputId}>{t('address.pickName')}</label>
          </SectionLabel>
          {input}
          <div id={statusId} className="px-1 pt-2" aria-live="polite">
            {status ?? <p className={cn('text-xs', problem === 'long' ? 'text-destructive-foreground' : 'text-muted-foreground')}>{t('address.nameRule')}</p>}
          </div>
        </section>
        <Group label={t('address.youGet')}>
          {rows.map((r) => (
            <li key={r.id} className="min-h-14 px-4 py-2">
              <span className={cn('block text-base break-all', !name && 'text-muted-foreground')}>{r.value}</span>
              <span className="block text-[13px] text-muted-foreground">{r.label}</span>
            </li>
          ))}
        </Group>
        <div className="mt-auto flex flex-col gap-3 pt-2">
          {failedBlock ? (
            <div className="overflow-hidden rounded-3xl border border-border bg-white">{failedBlock}</div>
          ) : (
            <>
              <TermsLine a={a} className="text-center" />
              {changeHint}
              <Button size="touch" className="w-full" disabled={!available} onClick={submit}>
                {claimLabel}
              </Button>
            </>
          )}
          {onCancel && (
            <Button variant="ghost" size="touch" className="w-full" onClick={onCancel}>
              {t('common.cancel')}
            </Button>
          )}
        </div>
      </>
    )
  }
  return (
    <>
      <div className="mt-5 grid gap-5 md:grid-cols-2">
        <div className="min-w-0">
          <label htmlFor={inputId} className="block text-[13px] font-semibold">
            {t('address.pickName')}
          </label>
          <div className="mt-2">{input}</div>
          <div id={statusId} className="mt-2 min-h-[42px]" aria-live="polite">
            {status}
          </div>
          <p className={cn('text-xs', problem === 'long' ? 'text-destructive-foreground' : 'text-muted-foreground')}>{t('address.nameRule')}</p>
        </div>
        <div className="min-w-0 self-start rounded-2xl border border-border bg-muted/40 p-4">
          <p className="text-[13px] font-semibold">{t('address.youGet')}</p>
          <dl className="mt-3 grid grid-cols-[auto_minmax(0,1fr)] gap-x-6 gap-y-2.5 text-[13px]">
            {rows.map((r) => (
              <Fragment key={r.id}>
                <dt className="text-muted-foreground">{r.label}</dt>
                <dd className={cn('truncate font-semibold', !name && 'font-normal text-muted-foreground')}>{r.value}</dd>
              </Fragment>
            ))}
          </dl>
        </div>
      </div>
      <div className="mt-5 flex flex-wrap items-center justify-end gap-x-4 gap-y-3 border-t border-border pt-4">
        {failedBlock ? (
          <div className="w-full overflow-hidden rounded-2xl border border-border">{failedBlock}</div>
        ) : (
          <>
            <div className="mr-auto flex min-w-0 flex-col gap-1">
              <TermsLine a={a} />
              {changeHint}
            </div>
            {onCancel && (
              <Button variant="ghost" onClick={onCancel}>
                {t('common.cancel')}
              </Button>
            )}
            <Button disabled={!available} onClick={submit}>
              {claimLabel}
            </Button>
          </>
        )}
        {failedBlock && onCancel && (
          <Button variant="ghost" onClick={onCancel}>
            {t('common.cancel')}
          </Button>
        )}
      </div>
    </>
  )
}

/** The servers in the agent's order with the slugs they'd get, which the address only lists once there is a name. */
function previewServers(a: Address, servers: { id: string; slug: string }[] | undefined): { id: string; name: string; slug: string }[] {
  return (a.servers ?? []).map((s) => ({ id: s.serverId, name: s.name, slug: servers?.find((x) => x.id === s.serverId)?.slug ?? s.label }))
}

/** The line under the name: checking, free, taken, not allowed, held or reserved; null when there's nothing to say. */
function nameStatus({ name, address, problem, mine, answer, onPick }: { name: string; address: string; problem: NameProblem | undefined; mine: boolean; answer: NameAvailability | Failure | undefined; onPick: (name: string) => void }): ReactNode {
  if (problem === 'characters') return <p className="text-xs font-semibold text-destructive-foreground">{t('address.notAllowed')}</p>
  if (problem) return null
  if (mine) return <p className="text-xs font-semibold text-muted-foreground">{t('address.yours', { address })}</p>
  if (!answer) {
    return (
      <p className="flex items-center gap-2 text-xs text-muted-foreground">
        <Spinner className="size-3.5" />
        {t('address.checking', { address })}
      </p>
    )
  }
  if (isFailure(answer)) return null
  if (answer.available) return <p className="text-xs font-semibold text-success-foreground">{t('address.isFree', { address })}</p>
  const until = Number(answer.params?.until)
  switch (answer.code) {
    case 'name_taken':
      return (
        <>
          <p className="text-xs font-semibold">{t('address.taken', { address })}</p>
          {!!answer.suggestions?.length && (
            <p className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
              {t('address.suggestions')}
              {answer.suggestions.map((s) => (
                <button key={s} type="button" onClick={() => onPick(s)} className="rounded-sm font-semibold text-success-foreground outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
                  {s}
                </button>
              ))}
            </p>
          )}
        </>
      )
    case 'name_held':
      return (
        <>
          <p className="text-xs font-semibold">{until ? t('address.held', { address, date: formatDate(new Date(until * 1000).toISOString()) }) : answer.message}</p>
          <a href={t('address.helpUrl')} target="_blank" rel="noreferrer" className="mt-2 inline-block text-xs font-semibold text-success-foreground hover:underline">
            {t('address.learnMore')}
          </a>
        </>
      )
    case 'name_reserved':
      return <p className="text-xs font-semibold">{t('address.reserved', { address })}</p>
    case 'invalid_name':
      return <p className="text-xs font-semibold text-destructive-foreground">{t('address.notAllowed')}</p>
    default:
      return <p className="text-xs font-semibold">{answer.message ?? name}</p>
  }
}

/** Why a claim, or a look at a name, was refused, with the one fix. */
function ClaimFailure({ failure, machine, onRetry, onUseOwn, touch }: { failure: Failure; machine: string; onRetry: () => void; onUseOwn?: () => void; touch: boolean }) {
  const e = failure.error
  const retry = <RetryButton touch={touch} onClick={onRetry} />
  const useOwn = onUseOwn && (
    <Button variant="outline" size={touch ? 'touch' : 'sm'} className={cn(touch && 'w-full')} onClick={onUseOwn}>
      {t('address.useOwn')}
    </Button>
  )
  switch (e.code) {
    case 'rate_limited':
      return <RateLimited until={failure.until} touch={touch} onRetry={onRetry} />
    case 'zone_full':
      return (
        <ResultBlock title={t('address.full')} actions={useOwn}>
          {t('address.fullBody')}
        </ResultBlock>
      )
    case 'clock_skew': {
      const minutes = Math.max(1, Math.round(Math.abs(Number(e.params?.skewSeconds ?? 0)) / 60))
      return (
        <ResultBlock tone="red" title={t('address.clock', { machine, count: minutes })} actions={retry}>
          {t('address.clockBody')}
          <span className="mt-2.5 flex items-center gap-2 rounded-xl bg-console py-1.5 pr-1.5 pl-3 font-mono text-[13px] text-[#e8e8e0]">
            <code className="min-w-0 flex-1 break-all">{ntpCommand}</code>
            <CopyIconButton value={ntpCommand} className="text-[#e8e8e0]/70 hover:bg-white/10 hover:text-[#e8e8e0]" />
          </span>
        </ResultBlock>
      )
    }
    case 'names_unreachable':
      return (
        <ResultBlock
          tone="red"
          title={t('address.unreachable')}
          actions={
            <>
              {retry}
              {useOwn}
            </>
          }
        >
          {t('address.unreachableBody')}
        </ResultBlock>
      )
    case 'not_public_address':
      return (
        <ResultBlock tone="red" title={t('address.notPublic', { machine })} actions={retry}>
          {t('address.notPublicBody', { ip: String(e.params?.ip ?? '') })}
        </ResultBlock>
      )
    case 'name_taken':
    case 'name_held':
    case 'name_reserved':
    case 'invalid_name':
      return (
        <ResultBlock tone="red" title={e.message}>
          {e.hint}
        </ResultBlock>
      )
    default:
      return (
        <ResultBlock tone="red" title={e.message} actions={retry}>
          {e.hint}
        </ResultBlock>
      )
  }
}

function RateLimited({ until, touch, onRetry }: { until?: number; touch: boolean; onRetry: () => void }) {
  const now = useNow()
  const left = until ? Math.max(0, (until - now) / 1000) : 0
  return <ResultBlock title={t('address.tooMany')} actions={<RetryButton touch={touch} disabled={left > 0} label={left > 0 ? t('address.tryAgainIn', { time: formatCountdown(left) }) : undefined} onClick={onRetry} />} />
}

/** The claim's three steps; `step` is the one in progress. */
function Claiming({ address, machine, ip, step }: { address: string; machine: string; ip?: string; step: 0 | 1 | 2 }) {
  const phone = useIsPhone()
  const title = t('address.claimingTitle', { address })
  const steps = [{ title: t('address.stepReserved') }, { title: t('address.stepPointing', { machine }), hint: ip }, { title: t('address.stepCertificate'), hint: t('address.stepCertificateHint') }]
  const items = steps.map((s, i) => {
    const state = i < step ? 'done' : i === step ? 'current' : 'todo'
    return (
      <li key={s.title} aria-current={state === 'current' ? 'step' : undefined} className={cn('flex items-start gap-2.5', phone && 'min-h-14 px-4 py-3')}>
        <span className="flex size-[18px] shrink-0 items-center justify-center pt-px">
          {state === 'done' && <CircleCheckIcon className="size-[18px] text-success-foreground" aria-hidden="true" />}
          {state === 'current' && <Spinner className="size-4" />}
          {state === 'todo' && <CircleIcon className="size-[18px] text-muted-foreground/50" aria-hidden="true" />}
        </span>
        <span className="min-w-0">
          <span className={cn('block text-sm font-semibold', state === 'todo' && 'text-muted-foreground', phone && 'text-base font-normal')}>{s.title}</span>
          {s.hint && <span className="block text-xs text-muted-foreground max-sm:text-[13px]">{s.hint}</span>}
        </span>
      </li>
    )
  })
  if (phone) {
    return (
      <div className="flex flex-col gap-5 pt-2 pb-6" role="status">
        <Group label={title}>{items}</Group>
      </div>
    )
  }
  return (
    <Card role="status">
      <CardTitle>{title}</CardTitle>
      <ol className="mt-4 grid gap-4 md:grid-cols-3">{items}</ol>
    </Card>
  )
}

function rowStatus(stage: FreeStage, published: boolean): string | undefined {
  if (stage === 'lapsed') return t('address.notUpdating')
  if (stage === 'publishing') return published ? t('address.published') : t('address.publishingRow')
  return undefined
}

/** A free name: being claimed, publishing, working, or lapsed, with change and release. */
export function FreeAddress({ id, a, machine, refresh, claim }: AddressProps & { claim: Claim }) {
  const phone = useIsPhone()
  const now = useNow(10_000)
  const [changing, setChanging] = useState(false)
  const [releasing, setReleasing] = useState(false)
  const [busy, setBusy] = useState<'release' | 'refresh' | 'certificate'>()
  const host = a.host ?? ''

  async function act(kind: 'release' | 'refresh' | 'certificate', done?: () => void) {
    setBusy(kind)
    try {
      await post<Address>(machineApi(id, `/address/${kind}`), kind === 'release' ? {} : { acceptTerms: true })
      done?.()
      await refresh()
    } catch (e) {
      toastManager.add({ title: refusal(e), type: 'error' })
    } finally {
      setBusy(undefined)
    }
  }

  if (claim.state.status === 'claiming') return <Claiming address={`${claim.state.name}.${a.base}`} machine={machine} ip={a.ip} step={0} />
  if (changing) {
    const cancel = () => {
      claim.reset()
      setChanging(false)
    }
    const picker = <FreePicker id={id} a={a} machine={machine} claim={claim} current={a.free?.name} onCancel={cancel} />
    if (phone) return <div className="flex flex-1 flex-col gap-5 pt-2 pb-6">{picker}</div>
    return (
      <Card>
        <CardTitle>{t('address.changeName')}</CardTitle>
        {picker}
      </Card>
    )
  }
  const stage = freeStage(a)
  if (stage === 'claiming') return <Claiming address={host} machine={machine} ip={a.ip} step={claimStep(runningOp(a, 'address.publish'))} />

  const open = dashboardURL(host, a.panelPort)
  const cert = certState(a, now)
  const header =
    stage === 'lapsed' ? (
      <LapsedHeader a={a} date={formatDate(a.free?.stoppedAt ?? a.free?.refreshedAt ?? new Date(now).toISOString())} machine={machine} busy={busy === 'refresh'} onRefresh={() => void act('refresh')} />
    ) : (
      <DoneHeader
        pip={stage === 'done' ? 'cheer' : 'hardhat'}
        spinner={stage === 'publishing'}
        title={stage === 'done' ? t('address.yours', { address: host }) : t('address.publishing', { address: host })}
        sub={stage === 'publishing' ? t('address.publishingHint') : cert === 'active' ? t('address.certActiveShort') : cert === 'getting' ? t('address.certGettingShort') : undefined}
        open={open}
        openLabel={openLabel(open, phone)}
        openEnabled={stage === 'done'}
      />
    )
  const rows: AddressRowData[] = [
    ...(a.servers ?? []).flatMap((s) => (s.address ? [{ id: s.serverId, label: s.name, value: s.address, status: rowStatus(stage, s.published) }] : [])),
    dashboardRow(host, a.panelPort, rowStatus(stage, a.free?.state === 'active' && a.free.dns === 'ok')),
  ]
  const release = () =>
    act('release', () => {
      setReleasing(false)
      toastManager.add({ title: t('address.released', { address: host }), type: 'success' })
    })
  return (
    <>
      <DoneView
        header={header}
        notices={
          <>
            <UnreachableNotice a={a} />
            <CertificateNotice a={a} now={now} busy={busy === 'certificate'} onRetry={() => void act('certificate')} />
          </>
        }
        rows={rows}
        ip={a.ip}
        footer={
          <>
            <Button variant="outline" onClick={() => setChanging(true)}>
              <PencilIcon />
              {t('address.changeName')}
            </Button>
            <Button variant="ghost" onClick={() => setReleasing(true)}>
              {t('address.release')}
            </Button>
          </>
        }
        phoneFooter={
          <>
            <PhoneAction label={t('address.changeName')} onClick={() => setChanging(true)} />
            <PhoneAction label={t('address.release')} onClick={() => setReleasing(true)} danger />
          </>
        }
      />
      <ConfirmDialog
        open={releasing}
        onOpenChange={setReleasing}
        title={t('address.releaseTitle', { address: host })}
        body={t('address.releaseBody', { count: a.free?.holdDays ?? 30 })}
        confirm={t('address.release')}
        busy={busy === 'release'}
        onConfirm={() => void release()}
      />
    </>
  )
}
