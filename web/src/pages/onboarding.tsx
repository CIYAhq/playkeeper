import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react'
import { ArrowLeftIcon, ArrowRightIcon, CircleAlertIcon, CircleCheckIcon, CircleXIcon, ExternalLinkIcon, RefreshCwIcon, UserPlusIcon, UserRoundIcon } from 'lucide-react'
import { useCatalog } from '@/api/catalog'
import { ApiError, get, post } from '@/api/client'
import type { LogsResponse, Me, Operation, Preflight, PreflightCheck, ServerStatus } from '@/api/types'
import { errorText, machineApi, serverApi, useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { CopyButton } from '@/components/app/bits'
import { ChoiceSelect, useIsPhone } from '@/components/app/controls'
import { cardStyles, createBlocked, createRequest, EulaCheck, freeName, memoryOptions, MoreOptions, recommendedVersion, StyleCards, styleMemory, versionCards, type CreateChoices } from '@/components/app/create'
import { Frame, FrameCard, PhoneActions } from '@/components/app/frame'
import { PasswordField } from '@/components/app/password-field'
import { CardsSkeleton, ListSkeleton } from '@/components/app/skeletons'
import { JobSteps, type StepState } from '@/components/app/update'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogFooter, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { formatBytes, formatMB, serverJoinAddress } from '@/lib/format'
import { createStepOf, isSettingUp } from '@/lib/phase'
import { navigate } from '@/lib/router'
import { typeName } from '@/lib/servers'
import { preset } from '@/lib/styles'
import { cn } from '@/lib/utils'
import { ConsoleTail } from './server/overview'

function codeFromHash(): string {
  const m = /[#&]code=([a-z0-9-]+)/i.exec(window.location.hash)
  if (m) window.history.replaceState(null, '', window.location.pathname)
  return m?.[1] ?? ''
}

// Other pages still find these here.
export { PasswordField, passwordStrength } from '@/components/app/password-field'

/** Step 1, before anyone is signed in: the admin account, with the installer's setup code. */
export function AccountStep({ onDone }: { onDone: (m: Me) => void }) {
  const phone = useIsPhone()
  const [hashCode] = useState(codeFromHash)
  const [code, setCode] = useState(hashCode)
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setError(undefined)
    setBusy(true)
    try {
      onDone(await post<Me>('/api/setup', { token: code.trim(), username: username.trim(), password }))
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  const list = [
    { title: t('onboarding.list.account'), hint: t('onboarding.list.accountHint') },
    { title: t('onboarding.list.check'), hint: t('onboarding.list.checkHint') },
    { title: t('onboarding.list.first'), hint: t('onboarding.list.firstHint') },
  ]
  return (
    <Frame step={0}>
      <div className="grid w-full max-w-[920px] grid-cols-1 items-center gap-12 md:grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)] max-sm:gap-6">
        <div>
          <Pip pose="wave" size={phone ? 80 : 112} />
          <h1 className="mt-5 text-display font-extrabold tracking-[-0.025em] max-sm:mt-3 max-sm:text-[28px] max-sm:leading-[34px]">{t('onboarding.hi')}</h1>
          <p className="mt-3 text-base text-muted-foreground max-sm:text-[15px]">{t('onboarding.hiBody')}</p>
          {!phone && (
            <ol className="mt-6 flex flex-col gap-3">
              {list.map((l, i) => (
                <li key={l.title} className="flex items-start gap-3">
                  <span className={cn('inline-flex size-[22px] shrink-0 items-center justify-center rounded-full text-[11px] font-semibold', i === 0 ? 'bg-primary text-primary-foreground' : 'border border-border bg-white text-muted-foreground')}>{i + 1}</span>
                  <span>
                    <span className="block text-[13px] font-semibold">{l.title}</span>
                    <span className="block text-xs text-muted-foreground">{l.hint}</span>
                  </span>
                </li>
              ))}
            </ol>
          )}
        </div>
        <FrameCard className="max-sm:rounded-3xl max-sm:border max-sm:bg-white max-sm:p-5">
          <h2 className="text-lg font-bold">{t('onboarding.accountTitle')}</h2>
          <p className="mt-1 text-[13px] text-muted-foreground">{t('onboarding.accountLead')}</p>
          <form className="mt-5 flex flex-col gap-4" onSubmit={submit}>
            {!hashCode && (
              <div className="flex flex-col gap-1.5">
                <label htmlFor="code" className="text-[13px] font-medium">
                  {t('onboarding.code')}
                </label>
                <Input id="code" value={code} onChange={(e) => setCode(e.target.value)} autoComplete="one-time-code" spellCheck={false} required />
                <p className="text-xs text-muted-foreground">{t('onboarding.codeHint', { command: 'sudo playkeeper setup-code' })}</p>
              </div>
            )}
            <div className="flex flex-col gap-1.5">
              <label htmlFor="username" className="text-[13px] font-medium max-sm:text-[15px]">
                {t('onboarding.username')}
              </label>
              <InputGroup className="max-sm:h-11">
                <InputGroupAddon>
                  <UserRoundIcon aria-hidden="true" />
                </InputGroupAddon>
                <InputGroupInput id="username" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" required minLength={3} maxLength={32} />
              </InputGroup>
            </div>
            <PasswordField id="password" label={t('onboarding.password')} value={password} onChange={setPassword} autoComplete="new-password" meter />
            {error && (
              <p className="text-[13px] text-destructive-foreground" role="alert">
                {error}
              </p>
            )}
            <Button type="submit" size={phone ? 'touch' : 'lg'} loading={busy} disabledReason={!code.trim() || !username.trim() || !password ? t('reason.fillIn') : password.length < 10 ? t('reason.passwordShort') : undefined}>
              {busy ? t('onboarding.creating') : t('onboarding.create')}
              <ArrowRightIcon />
            </Button>
            <p className="text-center text-xs text-muted-foreground">{t('onboarding.stored')}</p>
          </form>
        </FrameCard>
      </div>
    </Frame>
  )
}

type Stage = 'check' | 'first' | 'style' | 'creating' | 'online'

/** Steps 2 and 3, signed in: check the machine, then create the first server or skip it. */
export function Onboarding() {
  const ws = useWorkspace()
  const [stage, setStage] = useState<Stage>('check')
  const [serverId, setServerId] = useState<string>()
  const server = ws.servers?.find((s) => s.id === serverId)

  useEffect(() => {
    if (serverId || !ws.servers || ws.servers.length === 0) return
    const first = ws.servers[0]
    if (first && isSettingUp(first)) {
      setServerId(first.id)
      setStage('creating')
    } else navigate({ name: 'home' }, true)
  }, [serverId, ws.servers])

  useEffect(() => {
    if (stage === 'creating' && server && server.phase === 'online' && !server.operation) setStage('online')
  }, [stage, server])

  const step = stage === 'check' ? 1 : 2
  return (
    <Frame step={step} version={ws.me.version}>
      {/* The check comes in with the frame; each later stage animates in itself. */}
      <div key={stage} className={cn('flex w-full flex-col items-center', stage !== 'check' && 'animate-page')}>
        {stage === 'check' && <CheckStage onNext={() => setStage('first')} />}
        {stage === 'first' && <FirstStage onCreate={() => setStage('style')} />}
        {stage === 'style' && (
          <StyleStage
            onBack={() => setStage('first')}
            onCreated={(op) => {
              setServerId(op.serverId)
              setStage('creating')
            }}
          />
        )}
        {stage === 'creating' && server && <CreatingStage server={server} />}
        {stage === 'online' && server && <OnlineStage server={server} />}
      </div>
    </Frame>
  )
}

function checkText(c: PreflightCheck, memoryMB: number | undefined, disk: number | undefined, port: number): { title: string; hint?: string } {
  if (c.status === 'fail' || c.status === 'warn') return { title: c.label, hint: [c.detail, c.fix].filter(Boolean).join(' ') }
  switch (c.id) {
    case 'memory':
      return { title: t('onboarding.check.memory', { memory: formatMB(memoryMB ?? 0) }), hint: (memoryMB ?? 0) >= 6144 ? t('onboarding.check.memoryOk') : t('onboarding.check.memoryLow') }
    case 'disk':
      return { title: t('onboarding.check.disk', { disk: formatBytes(disk) }), hint: t('onboarding.check.diskOk') }
    case 'docker':
      return { title: t('onboarding.check.docker') }
    case 'port':
      return { title: c.detail.includes('Playkeeper server') ? t('onboarding.check.portOurs', { port }) : t('onboarding.check.port', { port }) }
    case 'egress':
      return { title: t('onboarding.check.egress'), hint: t('onboarding.check.egressHint') }
    default:
      return { title: c.label, hint: c.detail }
  }
}

function CheckIcon({ status }: { status: PreflightCheck['status'] }) {
  switch (status) {
    case 'pass':
      return <CircleCheckIcon className="size-[18px] text-success-foreground" aria-hidden="true" />
    case 'warn':
    case 'info':
      return <CircleAlertIcon className="size-[18px] text-warning" aria-hidden="true" />
    case 'fail':
      return <CircleXIcon className="size-[18px] text-destructive" aria-hidden="true" />
    default: {
      const unreachable: never = status
      return unreachable
    }
  }
}

const checkListClass = 'mt-4 flex flex-col max-sm:rounded-3xl max-sm:border max-sm:border-border max-sm:bg-white max-sm:px-4'
const checkRowClass = 'flex gap-3 border-t border-border py-3 first:border-t-0 sm:first:border-t'

function CheckStage({ onNext }: { onNext: () => void }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const [pre, setPre] = useState<Preflight>()
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [checked, setChecked] = useState(false)
  const live = ws.machine?.live
  const id = ws.machine?.id

  /** Runs the checks; false when they could not be run, which `error` then says. */
  const run = useCallback(async () => {
    if (!id) return false
    setBusy(true)
    try {
      setPre(await get<Preflight>(machineApi(id, '/preflight')))
      setError(undefined)
      return true
    } catch (e) {
      setError(errorText(e))
      return false
    } finally {
      setBusy(false)
    }
  }, [id])
  useEffect(() => {
    void run()
  }, [run])
  // The checks usually come back the same, so the button says they ran.
  async function recheck() {
    if (!(await run())) return
    setChecked(true)
    window.setTimeout(() => setChecked(false), 1800)
  }
  const again = (
    <>
      {checked ? <CircleCheckIcon /> : <RefreshCwIcon />}
      {checked ? t('onboarding.checked') : t('onboarding.checkAgain')}
    </>
  )

  const port = live?.defaultGamePort ?? 25565
  const rows = useMemo(() => {
    if (!pre) return []
    const out = pre.checks.map((c) => ({ key: c.id, status: c.status, ...checkText(c, live?.memoryTotalMB, live?.diskFreeBytes, port) }))
    if (live) out.splice(3, 0, { key: 'os', status: 'pass' as const, title: t('onboarding.check.os', { os: live.os, arch: live.arch }), hint: t('onboarding.check.osHint') })
    out.push({ key: 'firewall', status: 'info' as const, title: t('onboarding.check.firewall'), hint: t('onboarding.check.firewallHint', { port }) })
    return out
  }, [pre, live, port])
  const ok = rows.filter((r) => r.status === 'pass').length
  const checkBlocked = !pre ? t('onboarding.checking') : pre.ok ? undefined : t('onboarding.checkBlocked')

  return (
    // On a phone the list scrolls clear of the two buttons fixed at the bottom.
    <FrameCard wide className="max-w-[560px] max-sm:pb-40">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-xl font-bold max-sm:text-[26px] max-sm:leading-8 max-sm:font-extrabold">{t('onboarding.checkTitle')}</h1>
          <p className="mt-1 text-[13px] text-muted-foreground max-sm:text-[15px]">{t('onboarding.checkLead')}</p>
        </div>
        {pre && <span className="shrink-0 text-xs text-muted-foreground">{t('onboarding.checkCount', { ok, total: rows.length })}</span>}
      </div>
      {error && (
        <p className="mt-4 text-[13px] text-destructive-foreground" role="alert">
          {error}
        </p>
      )}
      {pre ? (
        <ul className={checkListClass}>
          {rows.map((r) => (
            <li key={r.key} className={checkRowClass}>
              <span className="mt-px">
                <CheckIcon status={r.status} />
              </span>
              <span className="min-w-0">
                <span className="block text-[13px] font-semibold max-sm:text-[15px]">{r.title}</span>
                {r.hint && <span className="block text-xs text-muted-foreground max-sm:text-[13px]">{r.hint}</span>}
                {r.key === 'firewall' && (
                  <a href={t('onboarding.check.firewallUrl')} target="_blank" rel="noreferrer" className="mt-1 inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline">
                    {t('onboarding.check.firewallLink')}
                    <ExternalLinkIcon className="size-3" aria-hidden="true" />
                  </a>
                )}
              </span>
            </li>
          ))}
        </ul>
      ) : (
        !error && <ListSkeleton rows={7} face="mt-px size-[18px] rounded-full" rowClassName={checkRowClass} className={checkListClass} label={t('onboarding.checking')} />
      )}
      {pre && !pre.ok && <p className="mt-3 text-[13px] text-destructive-foreground">{t('onboarding.checkBlocked')}</p>}
      {phone ? (
        <PhoneActions>
          <Button size="touch" onClick={onNext} disabledReason={checkBlocked}>
            {t('onboarding.looksGood')}
            <ArrowRightIcon />
          </Button>
          <Button size="touch" variant="ghost" onClick={recheck} loading={busy} aria-live="polite">
            {again}
          </Button>
        </PhoneActions>
      ) : (
        <div className="mt-5 flex items-center justify-between gap-3">
          <Button variant="ghost" onClick={recheck} loading={busy} aria-live="polite">
            {again}
          </Button>
          <Button onClick={onNext} disabledReason={checkBlocked}>
            {t('onboarding.looksGood')}
            <ArrowRightIcon />
          </Button>
        </div>
      )}
    </FrameCard>
  )
}

function FirstStage({ onCreate }: { onCreate: () => void }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const skip = () => navigate({ name: 'home' }, true)
  if (phone) {
    return (
      <div className="w-full">
        <Pip pose="wave" size={96} />
        <h1 className="mt-4 text-[26px] leading-8 font-extrabold tracking-[-0.02em]">{t('onboarding.readyTitle', { machine: ws.machineName })}</h1>
        <p className="mt-2 text-[15px] text-muted-foreground">{t('onboarding.readyLead')}</p>
        <dl className="mt-6 flex flex-col gap-4">
          <div>
            <dt className="text-base font-semibold">{t('onboarding.createButton')}</dt>
            <dd className="mt-0.5 text-[13px] text-muted-foreground">{t('onboarding.createFirstHintPhone')}</dd>
          </div>
          <div>
            <dt className="text-base font-semibold">{t('onboarding.skip')}</dt>
            <dd className="mt-0.5 text-[13px] text-muted-foreground">{t('onboarding.skipHintPhone')}</dd>
          </div>
        </dl>
        <PhoneActions>
          <Button size="touch" onClick={onCreate}>
            {t('onboarding.createButton')}
            <ArrowRightIcon />
          </Button>
          <Button size="touch" variant="ghost" onClick={skip}>
            {t('onboarding.skip')}
          </Button>
        </PhoneActions>
      </div>
    )
  }
  return (
    <FrameCard wide className="max-w-[560px]">
      <div className="flex items-start gap-4">
        <Pip pose="wave" size={64} />
        <div className="pt-1">
          <h1 className="text-xl font-bold">{t('onboarding.readyTitle', { machine: ws.machineName })}</h1>
          <p className="mt-1 text-[13px] text-muted-foreground">{t('onboarding.readyLead')}</p>
        </div>
      </div>
      <div className="mt-5 grid grid-cols-2 gap-3">
        <div className="flex flex-col rounded-2xl border border-primary/55 bg-selected p-4 shadow-selected">
          <h2 className="text-sm font-semibold">{t('onboarding.createFirst')}</h2>
          <p className="mt-1 text-xs text-muted-foreground">{t('onboarding.createFirstHint')}</p>
          <Button className="mt-auto w-full" onClick={onCreate}>
            {t('onboarding.createButton')}
            <ArrowRightIcon />
          </Button>
        </div>
        <div className="flex flex-col rounded-2xl border border-border p-4">
          <h2 className="text-sm font-semibold">{t('onboarding.skip')}</h2>
          <p className="mt-1 mb-4 text-xs text-muted-foreground">{t('onboarding.skipHint')}</p>
          <Button variant="outline" className="mt-auto w-full" onClick={skip}>
            {t('onboarding.skip')}
          </Button>
        </div>
      </div>
    </FrameCard>
  )
}

function StyleStage({ onBack, onCreated }: { onBack: () => void; onCreated: (op: Operation) => void }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const { catalog, error, reload } = useCatalog(ws.machine?.id, { fresh: true })
  const [c, setC] = useState<CreateChoices>()
  const [changing, setChanging] = useState(false)
  const [busy, setBusy] = useState(false)
  const [createError, setCreateError] = useState<string>()
  const [nameEdited, setNameEdited] = useState(false)

  useEffect(() => {
    if (!catalog || c) return
    setC({ type: 'paper', versionId: recommendedVersion(catalog)?.id ?? '', acceptExperimental: false, style: 'friends', hardcore: false, levelType: 'normal', memoryMB: styleMemory(catalog, 'friends'), name: freeName(t('style.friends.name'), ws.servers), motd: '', eula: false, build: '' })
  }, [catalog, c, ws.servers])

  const update = (patch: Partial<CreateChoices>) => setC((prev) => (prev ? { ...prev, ...patch } : prev))
  const version = catalog?.versions.find((v) => v.id === c?.versionId)

  async function create() {
    if (!c || !ws.machine) return
    setBusy(true)
    setCreateError(undefined)
    try {
      const op = await post<Operation>(machineApi(ws.machine.id, '/servers'), createRequest(c))
      await ws.refresh()
      onCreated(op)
    } catch (e) {
      setCreateError(errorText(e))
    } finally {
      setBusy(false)
    }
  }

  if (error) {
    return (
      <FrameCard>
        <p className="text-[13px] text-destructive-foreground" role="alert">
          {t('new.versionsError')}
        </p>
        <Button className="mt-4" variant="outline" onClick={reload}>
          <RefreshCwIcon />
          {t('common.tryAgain')}
        </Button>
      </FrameCard>
    )
  }
  const heading = (
    <>
      <h1 className="text-[26px] leading-8 font-extrabold tracking-[-0.02em] sm:text-2xl sm:font-bold">{t('style.question')}</h1>
      <p className="mt-1 text-[13px] text-muted-foreground max-sm:text-[15px]">{t('style.lead')}</p>
    </>
  )
  if (!c || !catalog) {
    const loading = (
      <>
        {heading}
        <CardsSkeleton count={cardStyles.length} className={cn('mt-4 grid gap-2.5', phone ? 'grid-cols-1' : 'grid-cols-3')} card={phone ? 'h-[70px]' : 'h-56'} />
      </>
    )
    return phone ? (
      <div className="w-full pb-28">{loading}</div>
    ) : (
      <FrameCard wide className="max-w-[640px]">
        {loading}
      </FrameCard>
    )
  }

  const summary = phone
    ? t('onboarding.summaryPhone', { type: typeName(c.type), version: version?.minecraftVersion ?? '', world: t(`style.world.${c.levelType}`).toLowerCase(), memory: formatMB(c.memoryMB) })
    : t('style.summary', { type: typeName(c.type), version: version?.minecraftVersion ?? '', memory: formatMB(c.memoryMB), total: formatMB(catalog.hostMemoryMB), name: c.name })
  const blocked = createBlocked(c, version)
  const { cards, older } = versionCards(catalog.versions, ws.servers)
  const versionChoices = [...cards.map((x) => x.entry), ...older].map((e) => ({ value: e.id, label: e.minecraftVersion, hint: e.experimental ? t('common.experimental') : e.recommended ? t('new.latestStable') : t('new.build', { build: e.paperBuild }) }))

  const body = (
    <>
      {heading}
      <div className="mt-4 flex flex-col gap-3">
        <StyleCards
          catalog={catalog}
          value={c.style}
          onChange={(style) => {
            const p = preset(style)
            update({ style, memoryMB: styleMemory(catalog, style), ...(nameEdited || !p ? {} : { name: freeName(t(p.name), ws.servers) }) })
          }}
          phone={phone}
        />
        <MoreOptions hardcore={c.hardcore} onHardcore={(hardcore) => update({ hardcore })} level={c.levelType} onLevel={(levelType) => update({ levelType })} phone={phone} />
        <EulaCheck checked={c.eula} onChange={(eula) => update({ eula })} short={phone} className={cn(phone ? 'min-h-14 rounded-2xl border border-border bg-white px-4 py-3' : 'px-1 pt-1')} />
        {createError && (
          <p className="text-[13px] text-destructive-foreground" role="alert">
            {createError}
          </p>
        )}
      </div>
    </>
  )

  const change = (
    <Dialog open={changing} onOpenChange={setChanging}>
      <DialogPopup className="sm:max-w-[440px]">
        <DialogHeader>
          <DialogTitle className="text-lg font-bold">{t('onboarding.changeTitle')}</DialogTitle>
        </DialogHeader>
        <DialogPanel className="flex flex-col gap-4">
          <label className="flex flex-col gap-1.5 text-[13px] font-medium">
            {t('onboarding.changeVersion')}
            <ChoiceSelect value={c.versionId} onChange={(versionId) => update({ versionId, acceptExperimental: false })} options={versionChoices} label={t('onboarding.changeVersion')} className="w-full" />
          </label>
          {version?.experimental && <ExperimentalConsent checked={c.acceptExperimental} onChange={(acceptExperimental) => update({ acceptExperimental })} version={version.minecraftVersion} />}
          <label className="flex flex-col gap-1.5 text-[13px] font-medium">
            {t('onboarding.changeMemory')}
            <ChoiceSelect value={String(c.memoryMB)} onChange={(mb) => update({ memoryMB: Number(mb) })} options={memoryOptions(catalog).map((mb) => ({ value: String(mb), label: formatMB(mb) }))} label={t('onboarding.changeMemory')} className="w-full" />
          </label>
          <label className="flex flex-col gap-1.5 text-[13px] font-medium">
            {t('onboarding.changeName')}
            <Input
              value={c.name}
              maxLength={32}
              onChange={(e) => {
                setNameEdited(true)
                update({ name: e.target.value })
              }}
            />
          </label>
        </DialogPanel>
        <DialogFooter variant="bare" className="border-t border-border pt-4">
          <Button onClick={() => setChanging(false)}>{t('common.done')}</Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )

  if (phone) {
    return (
      <div className="w-full pb-28">
        {body}
        <p className="mt-6 text-[13px] text-muted-foreground">
          {summary}{' '}
          <button type="button" className="font-semibold text-success-strong" onClick={() => setChanging(true)}>
            {t('common.change')}
          </button>
        </p>
        <PhoneActions>
          <Button size="touch" onClick={create} loading={busy} disabledReason={blocked}>
            {t('onboarding.createMine')}
            <ArrowRightIcon />
          </Button>
        </PhoneActions>
        {change}
      </div>
    )
  }
  return (
    <FrameCard wide className="max-w-[640px]">
      {body}
      <div className="mt-5 flex items-center gap-3 border-t border-border pt-4">
        <Button variant="ghost" onClick={onBack}>
          <ArrowLeftIcon />
          {t('common.back')}
        </Button>
        <span className="ml-auto min-w-0 truncate text-xs text-muted-foreground">{summary}</span>
        <button type="button" className="shrink-0 text-xs font-semibold text-primary hover:underline" onClick={() => setChanging(true)}>
          {t('common.change')}
        </button>
        <Button onClick={create} loading={busy} disabledReason={blocked}>
          {t('onboarding.createMine')}
          <ArrowRightIcon />
        </Button>
      </div>
      {change}
    </FrameCard>
  )
}

function ExperimentalConsent({ checked, onChange, version }: { checked: boolean; onChange: (v: boolean) => void; version: string }) {
  return (
    <label className="flex items-start gap-2.5 text-[13px]">
      <Checkbox checked={checked} onCheckedChange={(v) => onChange(v === true)} className="mt-0.5" />
      {t('new.experimentalConsent', { version })}
    </label>
  )
}

function CreatingStage({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const [tail, setTail] = useState<string[]>([])
  useEffect(() => {
    let stopped = false
    const poll = () =>
      get<LogsResponse>(serverApi(s.id, '/logs?limit=3'))
        .then((r) => !stopped && setTail(r.lines.map((l) => l.text)))
        .catch(() => undefined)
    void poll()
    const id = window.setInterval(poll, 2000)
    return () => {
      stopped = true
      window.clearInterval(id)
    }
  }, [s.id])
  const op = s.operation ?? s.lastOperation
  const failed = !s.operation && op?.status === 'failed'
  const at = createStepOf(failed ? (op?.phase ?? '') : s.phase)
  const state = (i: number): StepState => (i < at ? 'done' : i === at ? (failed ? 'failed' : 'current') : 'todo')
  const pct = /(\d{1,3})\s*%/.exec(s.phaseDetail ?? '')?.[1]
  const type = typeName(s.type)
  const version = s.config?.minecraftVersion ?? ''
  return (
    <FrameCard wide className="max-w-[540px] max-sm:rounded-3xl max-sm:border max-sm:bg-white max-sm:p-5">
      <div className="flex items-start gap-4">
        <Pip pose={failed ? 'hurt' : 'hardhat'} size={64} />
        <div className="pt-1">
          <h1 className="text-lg font-bold">{failed ? t('creating.failedTitle', { server: s.name }) : t('creating.title', { server: s.name })}</h1>
          <p className="mt-1 text-[13px] text-muted-foreground">{failed ? op?.error : t('creating.lead')}</p>
        </div>
      </div>
      <div className="mt-5 border-t border-border pt-5">
        <JobSteps
          steps={[
            { title: t('creating.checked', { machine: ws.machineName }), hint: t('creating.checkedDetail', { memory: formatMB(s.config?.memoryMB ?? 0), disk: formatBytes(ws.machine?.live?.diskFreeBytes) }), state: state(0) },
            { title: at > 1 ? t('creating.downloaded', { type, version }) : t('creating.downloading', { type, version }), hint: t('creating.downloadedDetail'), state: state(1) },
            { title: t('creating.starting'), hint: pct ? t('creating.startingPercent', { percent: pct }) : t('creating.startingDetail'), state: state(2), progress: pct ? Number(pct) : undefined },
            { title: t('creating.reachable', { port: s.gamePort }), state: state(3) },
          ]}
        />
      </div>
      {tail.length > 0 && (
        <div className="mt-5">
          <div className="text-xs font-semibold">{t('creating.output')}</div>
          <ConsoleTail lines={tail} className="mt-2" />
        </div>
      )}
      <div className="mt-5 flex justify-end">
        <Button variant={failed ? 'default' : 'outline'} onClick={() => navigate({ name: 'server', slug: s.slug, tab: 'overview' })}>
          {t('onboarding.toDashboard')}
          <ArrowRightIcon />
        </Button>
      </div>
    </FrameCard>
  )
}

const confettiColors = ['#15803d', '#2563eb', '#ffc83d', '#ef4444', '#86c79c', '#f59e0b']

function Confetti() {
  const bits = useMemo(
    () =>
      Array.from({ length: 34 }, (_, i) => ({
        left: (i * 37) % 100,
        top: (i * 53) % 42,
        color: confettiColors[i % confettiColors.length],
        rotate: (i * 47) % 180,
        wide: i % 3 === 0,
      })),
    [],
  )
  return (
    <div className="pointer-events-none absolute inset-x-0 top-0 h-40 overflow-hidden" aria-hidden="true">
      {bits.map((b, i) => (
        <span key={i} className={cn('confetti absolute rounded-full', b.wide ? 'h-1 w-2.5' : 'size-1.5')} style={{ left: `${b.left}%`, top: `${b.top}%`, background: b.color, transform: `rotate(${b.rotate}deg)`, animationDelay: `${(i % 7) * 90}ms` }} />
      ))}
    </div>
  )
}

function OnlineStage({ server: s }: { server: ServerStatus }) {
  const phone = useIsPhone()
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [added, setAdded] = useState<string[]>([])
  const address = serverJoinAddress(s)
  const dashboard = () => navigate({ name: 'server', slug: s.slug, tab: 'overview' }, true)

  async function invite(e: FormEvent) {
    e.preventDefault()
    const n = name.trim()
    if (!/^[A-Za-z0-9_]{3,16}$/.test(n)) {
      toastManager.add({ title: t('players.nameRule'), type: 'error' })
      return
    }
    setBusy(true)
    try {
      await post(serverApi(s.id, '/whitelist'), { name: n })
      setAdded((a) => [...a, n])
      setName('')
      toastManager.add({ title: t('players.addedToast', { name: n }), type: 'success' })
    } catch (err) {
      toastManager.add({ title: errorText(err), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  return (
    <FrameCard wide className="relative max-w-[520px] overflow-hidden text-center max-sm:overflow-visible">
      <Confetti />
      <Pip pose="cheer" size={phone ? 96 : 88} className="relative mx-auto mt-2" />
      <h1 className="relative mt-4 text-[28px] leading-9 font-extrabold tracking-[-0.02em]">{t('creating.online', { server: s.name })}</h1>
      <p className="mt-2 text-sm text-muted-foreground">{t('creating.onlineLead')}</p>
      <div className="mt-5 rounded-2xl border border-border bg-warm px-4 py-4">
        <div className="section-label">{t('onboarding.joinAddress')}</div>
        <div className="mt-2 flex flex-wrap items-center justify-center gap-3">
          <span className="text-2xl font-extrabold tabular-nums" data-testid="join-address">
            {address}
          </span>
          <CopyButton text={address} variant="default" size="sm" toast={t('toast.copied')} />
        </div>
        <p className="mt-2 text-xs text-muted-foreground">{t('onboarding.joinHint')}</p>
      </div>
      <form onSubmit={invite} className="mt-5 text-left">
        <label htmlFor="invite" className="text-[13px] font-semibold">
          {t('onboarding.inviteFirst')}
        </label>
        <div className="mt-1.5 flex gap-2">
          <InputGroup className="flex-1">
            <InputGroupAddon>
              <UserPlusIcon aria-hidden="true" />
            </InputGroupAddon>
            <InputGroupInput id="invite" value={name} onChange={(e) => setName(e.target.value)} placeholder={t('onboarding.invitePlaceholder')} autoComplete="off" spellCheck={false} maxLength={16} />
          </InputGroup>
          <Button type="submit" variant="outline" loading={busy}>
            {t('onboarding.inviteButton')}
          </Button>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">{added.length ? t('onboarding.invited', { names: added.join(', ') }) : t('onboarding.inviteHint')}</p>
      </form>
      <div className="mt-5 flex items-center justify-between gap-3 border-t border-border pt-4">
        <Button variant="ghost" size="sm" onClick={dashboard}>
          {t('onboarding.skipInvite')}
        </Button>
        <Button onClick={dashboard}>
          {t('onboarding.toDashboard')}
          <ArrowRightIcon />
        </Button>
      </div>
    </FrameCard>
  )
}
