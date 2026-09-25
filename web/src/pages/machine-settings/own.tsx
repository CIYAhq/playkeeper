import { useId, useState, type ReactNode } from 'react'
import { ExternalLinkIcon, PencilIcon, RefreshCwIcon } from 'lucide-react'
import { del, get, post } from '@/api/client'
import type { Address, AddressPlan, DNSRecord, JoinAddress } from '@/api/types'
import { machineApi } from '@/api/workspace'
import { Card, CardHint, CardTitle, SectionLabel, useNow } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupInput } from '@/components/ui/input-group'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { certState, dashboardURL, ownDone, recordFor, runningOp, zoneOf } from '@/lib/address'
import { formatList, formatLongDate, relativeTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import { Group } from '../more'
import { ErrorLine, refusal } from '../two-factor'
import {
  certProblemText,
  CertificateNotice,
  ConfirmDialog,
  CopyIconButton,
  dashboardRow,
  DoneHeader,
  DoneView,
  isFailure,
  openLabel,
  OutLink,
  PhoneAction,
  PhoneCopyRow,
  ResultBlock,
  RetryButton,
  TermsLine,
  useLookup,
  type AddressProps,
  type AddressRowData,
} from './parts'

const planDelayMs = 500

/** A typed domain the way the agent reads it, also when pasted as a URL. */
function domainOf(raw: string): string {
  return raw
    .trim()
    .toLowerCase()
    .replace(/^https?:\/\//, '')
    .replace(/[/:].*$/, '')
    .replace(/\.$/, '')
}

/**
 * The own domain's three steps: the domain, its records and the check,
 * with what the check found. `footer` goes under them.
 */
export function OwnSteps({ id, a, refresh, onCancel, onChecked, footer }: AddressProps & { onCancel?: () => void; onChecked?: () => void; footer?: ReactNode }) {
  const phone = useIsPhone()
  const now = useNow(10_000)
  const inputId = useId()
  const [raw, setRaw] = useState(a.kind === 'own' ? (a.host ?? '') : '')
  const [checking, setChecking] = useState(false)
  const [checkError, setCheckError] = useState<string>()
  const [certBusy, setCertBusy] = useState(false)
  const domain = domainOf(raw)
  const saved = a.kind === 'own' && a.host === domain
  const { answer } = useLookup(saved ? '' : domain, (d) => get<AddressPlan>(machineApi(id, `/address/plan?domain=${encodeURIComponent(d)}`)), planDelayMs)
  let plan: { records: DNSRecord[]; servers: JoinAddress[] } | undefined
  if (saved) plan = { records: a.records ?? [], servers: a.servers ?? [] }
  else if (answer && !isFailure(answer)) plan = { records: answer.records ?? [], servers: answer.servers ?? [] }
  const planError = !saved && isFailure(answer) ? refusal(answer.error) : undefined
  const loading = !!domain && !saved && !answer
  const check = saved ? a.check : undefined

  async function runCheck() {
    if (!plan || checking) return
    setChecking(true)
    setCheckError(undefined)
    try {
      await post<Address>(machineApi(id, '/address/check'), { domain, acceptTerms: true })
      await refresh()
      onChecked?.()
    } catch (e) {
      setCheckError(refusal(e))
    } finally {
      setChecking(false)
    }
  }
  async function certificate() {
    setCertBusy(true)
    try {
      await post<Address>(machineApi(id, '/address/certificate'), { acceptTerms: true })
      await refresh()
    } catch (e) {
      toastManager.add({ title: refusal(e), type: 'error' })
    } finally {
      setCertBusy(false)
    }
  }

  const input = (
    <InputGroup className={cn('max-sm:h-11', !phone && 'mt-2 max-w-[300px]')}>
      <InputGroupInput
        id={inputId}
        value={raw}
        onChange={(e) => {
          setRaw(e.target.value)
          setCheckError(undefined)
        }}
        onBlur={() => domain !== raw && setRaw(domain)}
        onKeyDown={(e) => e.key === 'Enter' && void runCheck()}
        placeholder={t('address.domainPlaceholder')}
        autoComplete="off"
        autoCapitalize="none"
        spellCheck={false}
        inputMode="url"
        enterKeyHint="go"
        aria-invalid={!!planError || undefined}
        className="max-sm:text-[17px]"
      />
    </InputGroup>
  )
  const results = check && <Results a={a} now={now} phone={phone} checking={checking} onCheck={() => void runCheck()} certBusy={certBusy} onCertificate={() => void certificate()} />

  if (phone) {
    return (
      <>
        <section>
          <SectionLabel className="px-4 pb-2">
            <label htmlFor={inputId}>{t('address.yourDomain')}</label>
          </SectionLabel>
          {input}
          <ErrorLine text={planError} className="mt-2 px-1" />
        </section>
        {loading && <Skeleton className="h-[136px] rounded-3xl" />}
        {plan?.records.map((r, i) => (
          <Group key={`${r.type}-${r.name}-${i}`} label={t('address.recordLabel', { type: r.type, for: recordFor(r, plan.servers, true) })}>
            <PhoneCopyRow value={r.name} label={t('address.name')} />
            <PhoneCopyRow value={r.value} label={t('address.value')} />
          </Group>
        ))}
        {results}
        {footer}
        <div className="mt-auto flex flex-col gap-3 pt-2">
          <ErrorLine text={checkError} className="mt-0 text-center" />
          <TermsLine a={a} className="text-center" />
          <Button size="touch" className="w-full" disabled={!plan} loading={checking} onClick={() => void runCheck()}>
            <RefreshCwIcon />
            {check ? t('address.checkAgain') : t('address.checkRecords')}
          </Button>
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
      <ol className="mt-5 flex flex-col gap-5">
        <Step n={1} title={<label htmlFor={inputId}>{t('address.yourDomain')}</label>}>
          {input}
          <ErrorLine text={planError} className="mt-2" />
        </Step>
        <Step n={2} title={plan ? t('address.addRecords', { zone: zoneOf(domain) }) : t('address.addRecordsPlain')}>
          {loading && <Skeleton className="mt-2 h-[124px] rounded-2xl" />}
          {plan && <RecordsTable records={plan.records} servers={plan.servers} />}
          <OutLink href={t('address.helpUrl')} className="mt-2.5">
            {t('address.dnsHelp')}
          </OutLink>
        </Step>
        <Step n={3} title={t('address.checkThem')}>
          {results || (
            <div className="mt-2 flex flex-col items-start gap-2">
              <Button disabled={!plan} loading={checking} onClick={() => void runCheck()}>
                <RefreshCwIcon />
                {t('address.checkRecords')}
              </Button>
              <TermsLine a={a} />
            </div>
          )}
          <ErrorLine text={checkError} />
        </Step>
      </ol>
      {(footer || onCancel) && (
        <div className="mt-5 flex items-center gap-2 border-t border-border pt-4">
          {footer}
          {onCancel && (
            <Button variant="ghost" onClick={onCancel}>
              {t('common.cancel')}
            </Button>
          )}
        </div>
      )}
    </>
  )
}

function Step({ n, title, children }: { n: number; title: ReactNode; children: ReactNode }) {
  return (
    <li className="grid grid-cols-[20px_minmax(0,1fr)] gap-x-3">
      <span className="flex size-5 items-center justify-center rounded-full bg-muted text-[11px] font-semibold text-muted-foreground" aria-hidden="true">
        {n}
      </span>
      <div className="min-w-0">
        <p className="text-[13px] leading-5 font-semibold">{title}</p>
        {children}
      </div>
    </li>
  )
}

function RecordsTable({ records, servers }: { records: DNSRecord[]; servers: JoinAddress[] }) {
  return (
    <div className="mt-2 overflow-x-auto rounded-2xl border border-border">
      <table className="w-full min-w-[600px] text-[13px]">
        <thead className="bg-muted/50 text-left text-xs text-muted-foreground">
          <tr>
            <th scope="col" className="px-3 py-2 font-medium">
              {t('address.type')}
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              {t('address.name')}
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              {t('address.value')}
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              {t('address.for')}
            </th>
          </tr>
        </thead>
        <tbody>
          {records.map((r, i) => (
            <tr key={`${r.type}-${r.name}-${i}`} className="border-t border-border">
              <td className="px-3 py-2.5 font-semibold">{r.type}</td>
              <td className="px-3 py-2.5">
                <CopyCell value={r.name} />
              </td>
              <td className="px-3 py-2.5">
                <CopyCell value={r.value} />
              </td>
              <td className="px-3 py-2.5 text-xs text-muted-foreground">{recordFor(r, servers)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function CopyCell({ value }: { value: string }) {
  return (
    <span className="inline-flex max-w-full items-center gap-1">
      <span className="min-w-0 break-all">{value}</span>
      <CopyIconButton value={value} />
    </span>
  )
}

/**
 * What the last check found: where the domain points, any server record
 * that's wrong, and the certificate. On desktop each carries its own fix;
 * on phones the page's bottom button checks again.
 */
function Results({ a, now, phone, checking, onCheck, certBusy, onCertificate }: { a: Address; now: number; phone: boolean; checking: boolean; onCheck: () => void; certBusy: boolean; onCertificate: () => void }) {
  const c = a.check
  if (!c) return null
  const domain = a.host ?? ''
  const n = c.name
  const cert = certState(a, now)
  const again = (label: string) => !phone && <RetryButton label={label} busy={checking} onClick={onCheck} />
  const blocks: ReactNode[] = []
  switch (n.code) {
    case 'name_ok': {
      const here = n.records?.find((r) => r.here)?.addr ?? a.ip
      const checked = t('address.checked', { time: relativeTime(c.at, now) })
      blocks.push(
        <ResultBlock key="name" tone="green" title={t('address.pointsHere', { domain })} footer={here ? `${here}${t('common.dot')}${checked}` : checked}>
          {cert !== 'active' && t('address.pointsHereBody')}
        </ResultBlock>,
      )
      break
    }
    case 'name_elsewhere': {
      const found = formatList((n.params?.found ?? '').split(',').filter(Boolean))
      const ip = n.params?.ipv4 ?? n.params?.ipv6 ?? a.ip ?? ''
      blocks.push(
        <ResultBlock key="name" tone="amber" title={t('address.pointsElsewhere', { domain })} actions={again(t('address.checkAgain'))}>
          {t('address.pointsElsewhereBody', { found, ip })}
        </ResultBlock>,
      )
      break
    }
    case 'name_missing':
      blocks.push(
        <ResultBlock key="name" title={t('address.notFound', { domain })} actions={again(t('address.checkNow'))}>
          {t('address.notFoundBody')}
        </ResultBlock>,
      )
      break
    default:
      blocks.push(
        <ResultBlock key="name" tone="amber" title={n.message} actions={again(t('address.checkAgain'))}>
          {n.hint}
        </ResultBlock>,
      )
  }
  const wrong = n.ok ? (c.records ?? []).filter((r) => !r.ok) : []
  wrong.forEach((r, i) =>
    blocks.push(
      <ResultBlock key={`srv-${i}`} tone="amber" title={r.message} actions={i === wrong.length - 1 && again(t('address.checkAgain'))}>
        {r.hint}
      </ResultBlock>,
    ),
  )
  if (cert === 'getting') {
    const op = runningOp(a)
    blocks.push(
      <ResultBlock key="cert" spinner title={t('address.certGetting')} footer={op && t('address.certStarted', { time: relativeTime(op.startedAt, now) })}>
        {t('address.certGettingBody')}
      </ResultBlock>,
    )
  } else if (cert === 'active') {
    const url = dashboardURL(domain, a.panelPort)
    blocks.push(
      <ResultBlock
        key="cert"
        tone="green"
        title={t('address.certActive', { date: formatLongDate(a.certificate?.notAfter ?? '') })}
        actions={
          <Button variant="outline" size={phone ? 'touch' : 'sm'} className={cn(phone && 'w-full')} render={<a href={url} target="_blank" rel="noreferrer" />}>
            {openLabel(url, phone)}
            <ExternalLinkIcon />
          </Button>
        }
      >
        {t('address.certActiveBody')}
      </ResultBlock>,
    )
  } else if (cert === 'problem') {
    const p = certProblemText(a, now)
    if (p) {
      blocks.push(
        <ResultBlock
          key="cert"
          tone="red"
          title={p.title}
          actions={
            <>
              <RetryButton busy={certBusy} touch={phone} onClick={onCertificate} />
              {p.port80 && <OutLink href={t('onboarding.check.firewallUrl')}>{t('address.openPort')}</OutLink>}
            </>
          }
          footer={!a.termsAccepted && <TermsLine a={a} />}
        >
          {p.body}
        </ResultBlock>,
      )
    }
  }
  return <div className={cn('divide-y divide-border overflow-hidden border border-border', phone ? 'rounded-3xl bg-white' : 'mt-2 rounded-2xl')}>{blocks}</div>
}

/** An own domain: its steps until it works, then what players type, with change and stop. */
export function OwnDomain(props: AddressProps) {
  const { id, a, refresh } = props
  const phone = useIsPhone()
  const now = useNow(10_000)
  const [editing, setEditing] = useState(false)
  const [stopping, setStopping] = useState(false)
  const [busy, setBusy] = useState<'stop' | 'certificate'>()
  const domain = a.host ?? ''

  async function stop() {
    setBusy('stop')
    try {
      await del<Address>(machineApi(id, '/address'))
      setStopping(false)
      toastManager.add({ title: t('address.stopped', { domain }), type: 'success' })
      await refresh()
    } catch (e) {
      toastManager.add({ title: refusal(e), type: 'error' })
    } finally {
      setBusy(undefined)
    }
  }
  async function certificate() {
    setBusy('certificate')
    try {
      await post<Address>(machineApi(id, '/address/certificate'), { acceptTerms: true })
      await refresh()
    } catch (e) {
      toastManager.add({ title: refusal(e), type: 'error' })
    } finally {
      setBusy(undefined)
    }
  }

  const dialog = (
    <ConfirmDialog
      open={stopping}
      onOpenChange={setStopping}
      title={t('address.stopTitle', { domain })}
      body={a.ip ? t('address.stopBody', { ip: a.ip }) : t('address.stopBodyNoIp')}
      confirm={t('address.stopUsing')}
      busy={busy === 'stop'}
      onConfirm={() => void stop()}
    />
  )

  if (!editing && ownDone(a, now)) {
    const open = dashboardURL(domain, a.panelPort)
    const rows: AddressRowData[] = [...(a.servers ?? []).flatMap((s) => (s.address ? [{ id: s.serverId, label: s.name, value: s.address }] : [])), dashboardRow(domain, a.panelPort)]
    return (
      <>
        <DoneView
          header={<DoneHeader pip="cheer" title={t('address.ready', { domain })} sub={t('address.certActiveShort')} open={open} openLabel={openLabel(open, phone)} openEnabled />}
          notices={<CertificateNotice a={a} now={now} busy={busy === 'certificate'} onRetry={() => void certificate()} />}
          rows={rows}
          ip={a.ip}
          footer={
            <>
              <Button variant="outline" onClick={() => setEditing(true)}>
                <PencilIcon />
                {t('address.changeDomain')}
              </Button>
              <Button variant="ghost" onClick={() => setStopping(true)}>
                {t('address.stopUsing')}
              </Button>
            </>
          }
          phoneFooter={
            <>
              <PhoneAction label={t('address.changeDomain')} onClick={() => setEditing(true)} />
              <PhoneAction label={t('address.stopUsing')} onClick={() => setStopping(true)} danger />
            </>
          }
        />
        {dialog}
      </>
    )
  }

  const stopAction = phone ? (
    <Group>
      <PhoneAction label={t('address.stopUsing')} onClick={() => setStopping(true)} danger />
    </Group>
  ) : (
    <Button variant="ghost" onClick={() => setStopping(true)}>
      {t('address.stopUsing')}
    </Button>
  )
  const steps = <OwnSteps {...props} onCancel={editing ? () => setEditing(false) : undefined} onChecked={() => setEditing(false)} footer={editing ? undefined : stopAction} />
  if (phone) {
    return (
      <>
        <div className="flex flex-1 flex-col gap-5 pt-2 pb-6">{steps}</div>
        {dialog}
      </>
    )
  }
  return (
    <>
      <Card>
        <CardTitle>{editing ? t('address.changeDomain') : t('address.title')}</CardTitle>
        {!editing && <CardHint>{a.ip ? t('address.lead', { ip: a.ip }) : t('address.leadNoIp')}</CardHint>}
        {steps}
      </Card>
      {dialog}
    </>
  )
}
