import { useEffect, useId, useState, type FormEvent, type HTMLAttributes, type ReactNode } from 'react'
import { ChevronRightIcon, ExternalLinkIcon, KeyRoundIcon, QrCodeIcon, UserRoundIcon, UsersRoundIcon } from 'lucide-react'
import { ApiError, get, post, setCsrfToken } from '@/api/client'
import type { AcceptResponse, Candidate, JoinInfo, JoinPreview, Me, MemberPreview, PlayerPreview } from '@/api/types'
import { BrandMark, Emblem, Pip, type PipPose } from '@/components/app/art'
import { CopyButton, Dot } from '@/components/app/bits'
import { CodeField } from '@/components/app/code-field'
import { Stepper, useIsPhone } from '@/components/app/controls'
import { LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n'
import { roleHint, roleName, welcomeKey } from '@/lib/access'
import { formatDate, formatList } from '@/lib/format'
import { linkPath, rePlayerName } from '@/lib/router'
import { cn } from '@/lib/utils'
import { PasswordField } from './onboarding'
import { CodesView, ErrorLine, KeyBox, QrImage, useSetup } from './two-factor'

// The page an invite link opens, for friends and new team members alike. It
// needs no account: the code travels only in POST bodies, and the page never
// shows who sent the link unless the panel gives a name meant for strangers.

type Kind = JoinPreview['kind']
type Refusal = 'not_working' | 'expired' | 'used_up'

const joinApi = (call: 'preview' | 'lookup' | 'redeem' | 'accept') => `/api/public/join/${call}`

function apiError(e: unknown): ApiError {
  return e instanceof ApiError ? e : new ApiError(0, { error: String(e), code: 'internal' })
}

/** The refusals that mean the link itself can't be used, which replace the whole page. */
function refusalOf(e: ApiError | undefined): Refusal | undefined {
  switch (e?.code) {
    case 'invite_not_working':
      return 'not_working'
    case 'invite_expired':
      return 'expired'
    case 'invite_used_up':
      return 'used_up'
    default:
      return undefined
  }
}

/** Friend links that ran out say so with their own details; team links don't. */
function refusedKind(e: ApiError, known: Kind | undefined): Kind | undefined {
  if (known) return known
  if (e.params?.maxUses || e.params?.expiredAt) return 'player'
  return e.code === 'invite_not_working' ? undefined : 'member'
}

export function JoinPage({ code, onSignedIn }: { code: string; onSignedIn: (me: Me, to?: string) => void }) {
  const [preview, setPreview] = useState<JoinPreview>()
  const [error, setError] = useState<ApiError>()
  const [attempt, setAttempt] = useState(0)
  const [admin, setAdmin] = useState<{ me: Me; password: string }>()

  useEffect(() => {
    if (!code) return
    let cancelled = false
    post<JoinPreview>(joinApi('preview'), { code })
      .then((p) => {
        if (cancelled) return
        setPreview(p)
        setError(undefined)
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(apiError(e))
      })
    return () => {
      cancelled = true
    }
  }, [code, attempt])

  if (admin) return <AdminStep me={admin.me} password={admin.password} onSignedIn={onSignedIn} />

  let body: ReactNode
  if (!code) body = <RefusalCard refusal="not_working" />
  else if (error && refusalOf(error)) body = <RefusalCard refusal={refusalOf(error) ?? 'not_working'} kind={refusedKind(error, preview?.kind)} error={error} inviter={preview?.inviter} />
  else if (error && !preview)
    body = (
      <TroubleCard
        error={error}
        onRetry={() => {
          setError(undefined)
          setAttempt((n) => n + 1)
        }}
      />
    )
  else if (!preview) body = <JoinSkeleton />
  else if (preview.kind === 'player') body = <FriendJoin code={code} preview={preview} onRefused={setError} />
  else body = <TeamJoin code={code} preview={preview} onRefused={setError} onJoined={(me, password) => (me.access.needsTwoFactor ? setAdmin({ me, password }) : onSignedIn(me))} />

  return <JoinShell>{body}</JoinShell>
}

const setupSteps = () => [t('join.stepAccount'), t('join.stepTwoFactor'), t('join.stepCodes')]

/** The public pages' frame: the brand and Help, the card, and the legal line with "Made with Playkeeper". */
function JoinShell({ step, children }: { step?: number; children: ReactNode }) {
  const phone = useIsPhone()
  return (
    <div className="flex min-h-dvh flex-col bg-sidebar">
      <a href="#main" className="skip-link rounded-lg bg-white px-3 py-2 text-sm font-medium shadow-popup">
        {t('nav.skip')}
      </a>
      {!phone && (
        <header className={cn('flex h-14 shrink-0 items-center gap-4 px-6', step !== undefined && 'grid grid-cols-[1fr_minmax(0,420px)_1fr]')}>
          <span className="flex items-center gap-2 text-[15px] font-bold">
            <BrandMark size={24} />
            {t('brand.name')}
          </span>
          {step !== undefined && <Stepper steps={setupSteps()} current={step} label={t('join.steps')} />}
          <a
            href={t('onboarding.helpUrl')}
            target="_blank"
            rel="noreferrer"
            aria-label={t('common.external', { label: t('common.help') })}
            className="ml-auto inline-flex min-h-11 items-center gap-1 justify-self-end rounded-lg px-1 text-[13px] text-muted-foreground hover:text-foreground"
          >
            {t('common.help')}
            <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
          </a>
        </header>
      )}
      <main id="main" tabIndex={-1} className="flex flex-1 flex-col items-center justify-center px-6 py-8 outline-none max-sm:justify-start max-sm:px-4 max-sm:pt-7 max-sm:pb-8">
        {children}
      </main>
      <footer className="flex shrink-0 items-center justify-between gap-6 px-6 py-4 text-xs text-muted-foreground max-sm:flex-col max-sm:gap-1.5 max-sm:px-8 max-sm:pt-4 max-sm:pb-[max(env(safe-area-inset-bottom),40px)] max-sm:text-center">
        <span className="max-sm:order-2 max-sm:text-[11px] max-sm:leading-[15px]">{t('footer.notOfficial')}</span>
        <span className="inline-flex items-center gap-1.5 max-sm:order-1 max-sm:text-[13px]">
          <BrandMark size={16} />
          {t('join.madeWith')}
        </span>
      </footer>
    </div>
  )
}

function JoinCard({ className, children, ...rest }: { className?: string; children: ReactNode } & HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn('w-full max-w-[460px] animate-page rounded-4xl border border-border bg-card p-6 shadow-popup max-sm:rounded-3xl max-sm:p-4 max-sm:pt-5 max-sm:shadow-none', className)}
      {...rest}
    >
      {children}
    </div>
  )
}

function JoinSkeleton() {
  return (
    <JoinCard aria-busy="true" aria-label={t('common.loading')} className="animate-none">
      <div className="flex items-center gap-3.5">
        <Skeleton className="size-12 rounded-[22%]" />
        <div className="flex-1">
          <Skeleton className="h-5 w-3/4" />
          <Skeleton className="mt-2 h-3.5 w-1/2" />
        </div>
      </div>
      <Skeleton className="mt-6 h-3.5 w-32" />
      <Skeleton className="mt-2 h-11 w-full rounded-lg" />
      <Skeleton className="mt-5 h-10 w-full rounded-lg max-sm:h-12" />
    </JoinCard>
  )
}

/** One line under a field or above the main button: what went wrong and what to do. */
function Problem({ error, id }: { error: ApiError; id?: string }) {
  return (
    <p id={id} className="animate-fade text-[13px] leading-5" role="alert">
      <span className="text-destructive-foreground">{error.message}</span>
      {error.hint && <span className="text-muted-foreground"> {error.hint}</span>}
    </p>
  )
}

type Lookup = { status: 'idle' } | { status: 'checking'; name: string } | { status: 'found'; name: string; candidate: Candidate } | { status: 'failed'; name: string; error: ApiError }

/** Looks a typed name up with Minecraft's account service once typing pauses. */
function useLookup(code: string, typed: string, onRefused: (e: ApiError) => void): Lookup {
  const [lookup, setLookup] = useState<Lookup>({ status: 'idle' })
  const name = typed.trim()
  const valid = rePlayerName.test(name)
  useEffect(() => {
    if (!valid) return
    let cancelled = false
    const timer = window.setTimeout(() => {
      post<Candidate>(joinApi('lookup'), { code, name })
        .then((candidate) => {
          if (!cancelled) setLookup({ status: 'found', name, candidate })
        })
        .catch((e: unknown) => {
          if (cancelled) return
          const err = apiError(e)
          if (refusalOf(err)) onRefused(err)
          else setLookup({ status: 'failed', name, error: err })
        })
    }, 450)
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [code, name, valid, onRefused])
  if (!valid) return { status: 'idle' }
  return lookup.status !== 'idle' && lookup.name === name ? lookup : { status: 'checking', name }
}

/** Why the join button waits: no name yet, a name Minecraft can't have, the lookup, or what it found wrong. */
function lookupReason(typed: string, lookup: Lookup): string | undefined {
  switch (lookup.status) {
    case 'idle':
      return typed.trim() ? t('players.nameRule') : t('join.nameFirst')
    case 'checking':
      return t('join.lookingUp', { name: lookup.name })
    case 'failed':
      return lookup.error.message
    case 'found':
      return undefined
    default: {
      const unreachable: never = lookup
      return unreachable
    }
  }
}

function FriendJoin({ code, preview, onRefused }: { code: string; preview: PlayerPreview; onRefused: (e: ApiError) => void }) {
  const phone = useIsPhone()
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState<ApiError>()
  const [joined, setJoined] = useState<JoinInfo>()
  const lookup = useLookup(code, name, onRefused)

  async function redeem(e: FormEvent) {
    e.preventDefault()
    if (lookup.status !== 'found' || busy) return
    setBusy(true)
    setFailed(undefined)
    try {
      setJoined(await post<JoinInfo>(joinApi('redeem'), { code, name: lookup.candidate.name }))
    } catch (err) {
      const ae = apiError(err)
      if (refusalOf(ae)) onRefused(ae)
      else setFailed(ae)
    } finally {
      setBusy(false)
    }
  }

  if (joined) return <JoinedCard info={joined} />

  const title = preview.inviter ? t('join.friendTitle', { inviter: preview.inviter, server: preview.server }) : t('join.friendTitleAnyone', { server: preview.server })
  const edition = preview.version ? t('join.edition', { version: preview.version }) : t('join.editionAny')
  const problem = failed ?? (lookup.status === 'failed' ? lookup.error : undefined)
  return (
    <JoinCard>
      <div className="flex items-center gap-3.5">
        <Emblem size={48} stopped={!preview.online} name={preview.server} />
        <div className="min-w-0">
          <h1 className="text-[22px] leading-7 font-bold tracking-[-0.01em] max-sm:text-xl max-sm:leading-6">{title}</h1>
          <p className="mt-1 flex items-center gap-1.5 text-[13px] text-muted-foreground max-sm:text-sm">
            <Dot tone={preview.online ? 'online' : 'stopped'} className="mx-0.5" />
            {edition}
            {t('common.dot')}
            {preview.online ? t('join.playingNow', { count: preview.playing }) : t('join.offline')}
          </p>
        </div>
      </div>
      <form className="mt-6 flex flex-col gap-4 max-sm:mt-5" onSubmit={redeem} noValidate>
        <div className="flex flex-col gap-1.5">
          <label htmlFor="join-name" className="text-[13px] font-medium max-sm:text-[15px]">
            {t('join.nameLabel')}
          </label>
          <InputGroup className="h-11">
            <InputGroupAddon>
              <UserRoundIcon aria-hidden="true" />
            </InputGroupAddon>
            <InputGroupInput
              id="join-name"
              value={name}
              onChange={(e) => {
                setName(e.target.value)
                setFailed(undefined)
              }}
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              maxLength={16}
              aria-invalid={problem ? true : undefined}
              aria-describedby={problem ? 'join-name-problem' : undefined}
              autoFocus={!phone}
              required
            />
          </InputGroup>
        </div>
        <div aria-live="polite" className="empty:hidden">
          {problem ? (
            <Problem error={problem} id="join-name-problem" />
          ) : lookup.status === 'found' ? (
            <div className="flex animate-enter items-center gap-3 rounded-2xl bg-warm p-2.5 max-sm:p-3">
              <Face candidate={lookup.candidate} size={40} />
              <div className="min-w-0">
                <p className="text-[13px] font-semibold max-sm:text-[15px]">{t('join.isThisYou')}</p>
                <p className="truncate text-xs text-muted-foreground max-sm:text-[13px]">{lookup.candidate.name}</p>
              </div>
            </div>
          ) : lookup.status === 'checking' ? (
            <div className="flex items-center gap-3 rounded-2xl bg-warm p-2.5 max-sm:p-3" aria-busy="true">
              <span className="sr-only">{t('join.lookingUp', { name: lookup.name })}</span>
              <Skeleton className="size-10 rounded-lg" />
              <div className="flex-1">
                <Skeleton className="h-3.5 w-24" />
                <Skeleton className="mt-1.5 h-3 w-16" />
              </div>
            </div>
          ) : null}
        </div>
        <Button type="submit" size={phone ? 'touch' : 'lg'} loading={busy} disabledReason={lookupReason(name, lookup)} className="w-full sm:h-10">
          {preview.approval === 'after_yes' ? t('join.askToJoin', { server: preview.server }) : t('join.addMe', { server: preview.server })}
        </Button>
      </form>
      <p className="mt-3.5 text-center text-xs text-muted-foreground max-sm:text-[13px]">{preview.version ? t('join.needs', { version: preview.version }) : t('join.needsAny')}</p>
    </JoinCard>
  )
}

/** The face from the player's skin, or their initial when Minecraft has none to give. */
function Face({ candidate, size }: { candidate: Candidate; size: number }) {
  const style = { width: size, height: size, borderRadius: Math.round(size / 5) }
  if (candidate.face) return <img src={candidate.face} alt="" width={size} height={size} className="pixelated shrink-0 ring-1 ring-black/10" style={style} />
  return (
    <span className="inline-flex shrink-0 items-center justify-center bg-primary/10 font-semibold text-primary" style={{ ...style, fontSize: Math.round(size * 0.45) }} aria-hidden="true">
      {candidate.name.slice(0, 1).toUpperCase()}
    </span>
  )
}

function JoinedCard({ info }: { info: JoinInfo }) {
  const phone = useIsPhone()
  return (
    <JoinCard>
      <div className="flex items-center gap-4">
        <Pip pose={info.waiting ? 'letter' : 'cheer'} size={64} />
        <div className="min-w-0">
          <h1 className="text-[22px] leading-7 font-bold tracking-[-0.01em] max-sm:text-xl max-sm:leading-6">{info.waiting ? t('join.waitingTitle') : t('join.doneTitle')}</h1>
          <p className="mt-1 text-[13px] text-muted-foreground max-sm:text-sm">
            {info.waiting ? t('join.waitingBody', { server: info.server, player: info.player }) : t('join.doneBody', { server: info.server, player: info.player })}
          </p>
        </div>
      </div>
      <div className="mt-5 rounded-2xl bg-warm p-3.5">
        <p className="text-xs text-muted-foreground max-sm:text-[13px]">{t('join.address')}</p>
        <p className="mt-1 text-[15px] font-bold break-all max-sm:text-[17px]">{info.address}</p>
        <CopyButton text={info.address} label={t('join.copyAddress')} variant="default" size={phone ? 'touch' : 'lg'} className="mt-3 w-full" />
      </div>
      <ol className="mt-4 flex flex-col gap-2 text-[13px] max-sm:text-[15px]">
        {info.steps.map((step, i) => (
          <li key={step.key} className="flex gap-3">
            <span className="w-4 shrink-0 font-semibold text-primary tabular-nums">{i + 1}.</span>
            <span>{step.text}</span>
          </li>
        ))}
      </ol>
    </JoinCard>
  )
}

function TeamJoin({ code, preview, onRefused, onJoined }: { code: string; preview: MemberPreview; onRefused: (e: ApiError) => void; onJoined: (me: Me, password: string) => void }) {
  const phone = useIsPhone()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [again, setAgain] = useState('')
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState<ApiError>()
  const role = roleName(preview.role)
  const mismatch = again !== '' && again !== password && again.length >= password.length
  const ready = username.trim() !== '' && password !== '' && again === password
  const nameProblem = failed && (failed.code === 'username_invalid' || failed.code === 'username_taken') ? failed : undefined
  const passwordProblem = failed?.code === 'password_invalid' ? failed : undefined
  const otherProblem = failed && !nameProblem && !passwordProblem ? failed : undefined
  const servers = preview.servers.all ? t('scope.all') : preview.serverNames.length ? formatList(preview.serverNames) : t('scope.none')

  async function accept(e: FormEvent) {
    e.preventDefault()
    if (!ready || busy) return
    setBusy(true)
    setFailed(undefined)
    try {
      const me = await post<AcceptResponse>(joinApi('accept'), { code, username: username.trim(), password })
      setCsrfToken(me.csrfToken)
      await post('/api/me/prefs', { [welcomeKey]: '1' }).catch(() => undefined)
      onJoined(me, password)
    } catch (err) {
      setBusy(false)
      const ae = apiError(err)
      if (refusalOf(ae)) onRefused(ae)
      else setFailed(ae)
    }
  }

  return (
    <JoinCard className="max-w-[500px]">
      <div className="flex items-center gap-3.5">
        <span className="flex size-11 shrink-0 items-center justify-center rounded-full bg-primary/10 text-[15px] font-semibold text-primary" aria-hidden="true">
          {preview.inviter ? preview.inviter.slice(0, 1).toUpperCase() : <UsersRoundIcon className="size-5" />}
        </span>
        <div className="min-w-0">
          <h1 className="text-[22px] leading-7 font-bold tracking-[-0.01em] max-sm:text-xl max-sm:leading-6">{preview.team ? t('join.teamTitle', { team: preview.team, role }) : t('join.teamTitleAny', { role })}</h1>
          <p className="mt-1 text-[13px] text-muted-foreground max-sm:text-sm">{preview.inviter ? t('join.teamFrom', { inviter: preview.inviter }) : t('join.teamFromAnyone')}</p>
        </div>
      </div>
      <dl className="mt-5 grid grid-cols-[100px_1fr] gap-x-4 gap-y-2.5 rounded-2xl border border-border bg-warm p-3.5 text-[13px] max-sm:grid-cols-[96px_1fr] max-sm:text-[15px]">
        <dt className="text-muted-foreground">{t('join.yourRole')}</dt>
        <dd className="font-semibold">
          {role}
          <span className="mt-0.5 block text-xs font-normal text-muted-foreground max-sm:text-[13px]">{roleHint(preview.role)}</span>
        </dd>
        <dt className="text-muted-foreground">{t('join.servers')}</dt>
        <dd className="font-semibold">{servers}</dd>
        <dt className="text-muted-foreground">{t('join.worksUntil')}</dt>
        <dd className="font-semibold">{t('join.worksUntilValue', { date: formatDate(preview.expiresAt) })}</dd>
      </dl>
      <form className="mt-5 flex flex-col gap-4" onSubmit={accept} noValidate>
        <div className="flex flex-col gap-1.5">
          <label htmlFor="join-username" className="text-[13px] font-medium max-sm:text-[15px]">
            {t('join.username')}
          </label>
          <InputGroup className="h-11">
            <InputGroupAddon>
              <UserRoundIcon aria-hidden="true" />
            </InputGroupAddon>
            <InputGroupInput
              id="join-username"
              value={username}
              onChange={(e) => {
                setUsername(e.target.value)
                if (nameProblem) setFailed(undefined)
              }}
              autoComplete="username"
              autoCapitalize="none"
              spellCheck={false}
              maxLength={32}
              aria-invalid={nameProblem ? true : undefined}
              aria-describedby={nameProblem ? 'join-username-problem' : undefined}
              autoFocus={!phone}
              required
            />
          </InputGroup>
          {nameProblem && <Problem error={nameProblem} id="join-username-problem" />}
        </div>
        <div className="flex flex-col gap-1.5 [&_[data-slot=input-group]]:h-11">
          <PasswordField
            id="join-password"
            label={t('join.password')}
            value={password}
            onChange={(v) => {
              setPassword(v)
              if (passwordProblem) setFailed(undefined)
            }}
            autoComplete="new-password"
          />
          {passwordProblem && <Problem error={passwordProblem} />}
        </div>
        <div className="flex flex-col gap-1.5">
          <label htmlFor="join-again" className="text-[13px] font-medium max-sm:text-[15px]">
            {t('join.passwordAgain')}
          </label>
          <InputGroup className="h-11">
            <InputGroupAddon>
              <KeyRoundIcon aria-hidden="true" />
            </InputGroupAddon>
            <InputGroupInput
              id="join-again"
              type="password"
              value={again}
              onChange={(e) => setAgain(e.target.value)}
              autoComplete="new-password"
              maxLength={256}
              aria-invalid={mismatch ? true : undefined}
              aria-describedby={mismatch ? 'join-again-problem' : undefined}
              required
            />
          </InputGroup>
          {mismatch && (
            <p id="join-again-problem" className="animate-fade text-[13px] text-destructive-foreground" role="alert">
              {t('join.mismatch')}
            </p>
          )}
        </div>
        {otherProblem && <Problem error={otherProblem} />}
        <Button type="submit" size={phone ? 'touch' : 'lg'} loading={busy} disabledReason={ready ? undefined : again && again !== password ? t('join.mismatch') : t('reason.fillIn')} className="w-full sm:h-10">
          {t('join.joinAs', { role })}
        </Button>
      </form>
      <p className="mt-2.5 text-center text-xs text-muted-foreground max-sm:text-[13px]">{t('join.once')}</p>
    </JoinCard>
  )
}

/**
 * After an Admin invite: turning on two-factor sign-in, which admins must
 * use, started with the password just chosen; or Moderator rights until it's
 * on. The owner or an admin then confirms their Admin rights.
 */
function AdminStep({ me, password, onSignedIn }: { me: Me; password: string; onSignedIn: (me: Me, to?: string) => void }) {
  const phone = useIsPhone()
  const s = useSetup(false, undefined, password)
  const [qr, setQr] = useState(false)
  const passwordId = useId()
  const labelId = useId()
  const { stage } = s

  function later() {
    s.discard()
    onSignedIn(me)
  }
  async function saved() {
    onSignedIn(await get<Me>('/api/auth/me').catch(() => me))
  }

  if (stage.step === 'codes') {
    return (
      <JoinShell step={2}>
        <JoinCard className="max-w-[560px]">
          <CodesView codes={stage.codes} name={me.user.username} step onSaved={() => void saved()} />
        </JoinCard>
      </JoinShell>
    )
  }
  const heading = (
    <>
      <div className="flex items-center gap-4 max-sm:gap-3.5">
        <Pip pose="hardhat" size={phone ? 48 : 52} />
        <div className="min-w-0">
          <h1 className="text-xl leading-7 font-bold tracking-[-0.01em] max-sm:text-[17px] max-sm:leading-[22px]">{t('join.adminTitle')}</h1>
          {!phone && <p className="mt-0.5 text-[13px] text-muted-foreground">{t('join.adminBody')}</p>}
        </div>
      </div>
      {phone && <p className="mt-3 text-sm text-muted-foreground">{t('join.adminBody')}</p>}
    </>
  )
  const laterButton = (
    <Button type="button" variant="ghost" size={phone ? 'touch' : 'sm'} className={cn('text-muted-foreground', phone ? 'w-full' : '-ml-2.5 text-[13px]')} onClick={later}>
      {t('join.adminLater')}
    </Button>
  )
  const confirmButton = (
    <Button type="submit" size={phone ? 'touch' : 'default'} className={cn(phone && 'w-full')} loading={s.busy} disabledReason={s.code.length < 6 ? t('reason.sixDigits') : undefined}>
      {t('twofa.confirm')}
    </Button>
  )
  let body: ReactNode
  switch (stage.step) {
    case 'loading':
      body = (
        <JoinCard className="max-w-none">
          {heading}
          <LoadingLabel />
          <div className="mt-5 flex items-center gap-4 max-sm:flex-col max-sm:items-stretch">
            <Skeleton className="size-[120px] shrink-0 rounded-2xl max-sm:h-12 max-sm:w-full" />
            <div className="min-w-0 flex-1">
              <Skeleton className="h-3 w-3/5" />
              <Skeleton className="mt-4 h-11 rounded-xl" />
            </div>
          </div>
          <Skeleton className="mt-5 h-[52px] w-72 max-w-full rounded-[10px]" />
          <div className="mt-5 border-t border-border pt-4 max-sm:border-t-0">{laterButton}</div>
        </JoinCard>
      )
      break
    case 'password':
      body = (
        <JoinCard className="max-w-none">
          <form onSubmit={s.start} noValidate>
            {heading}
            <div className="mt-5">
              <PasswordField id={passwordId} label={t('twofa.passwordLabel')} value={s.password} onChange={s.setPassword} autoComplete="current-password" autoFocus error={s.passwordError} />
            </div>
            <ErrorLine text={s.error} />
            <div className="mt-5 flex items-center justify-between gap-3 border-t border-border pt-4 max-sm:flex-col-reverse max-sm:items-stretch max-sm:gap-2 max-sm:border-t-0">
              {laterButton}
              <Button type="submit" size={phone ? 'touch' : 'default'} loading={s.busy} disabledReason={s.password ? undefined : t('reason.passwordFirst')}>
                {t('common.continue')}
              </Button>
            </div>
          </form>
        </JoinCard>
      )
      break
    case 'scan':
      body = (
        <form
          onSubmit={(e) => {
            e.preventDefault()
            void s.confirm(s.code)
          }}
          noValidate
          className={cn('flex flex-col', phone && 'flex-1')}
        >
          <JoinCard className="max-w-none">
            {heading}
            {phone ? (
              <>
                <Button size="touch" className="mt-4 w-full" render={<a href={stage.setup.uri} />}>
                  {t('twofa.openApp')}
                  <ExternalLinkIcon />
                </Button>
                <p className="mt-3 text-[13px] text-muted-foreground">{t('twofa.orType')}</p>
                <KeyBox value={stage.setup.manualKey} className="mt-2" />
              </>
            ) : (
              <div className="mt-5 flex items-center gap-4">
                <QrImage svg={stage.setup.qrCodeSvg} className="size-[120px] shrink-0 rounded-2xl border border-border bg-white p-2" />
                <div className="min-w-0 flex-1">
                  <p className="text-[13px] font-semibold">{t('twofa.scan')}</p>
                  <p className="mt-4 text-xs font-semibold">{t('twofa.cantScan')}</p>
                  <KeyBox value={stage.setup.manualKey} className="mt-2" />
                </div>
              </div>
            )}
            {!phone && (
              <>
                <p id={labelId} className="mt-5 text-[13px] font-semibold">
                  {t('twofa.typeCode')}
                </p>
                <CodeField value={s.code} onChange={s.setCode} onComplete={(v) => void s.confirm(v)} invalid={s.wrong} autoFocus labelledBy={labelId} className="mt-2 justify-start" />
                <ErrorLine text={s.wrong ? t('signin.wrong') : s.error} />
                <div className="mt-5 flex items-center justify-between gap-3 border-t border-border pt-4">
                  {laterButton}
                  <span className="flex shrink-0 items-center gap-3">
                    <span className="text-xs whitespace-nowrap text-muted-foreground">{t('join.adminNext')}</span>
                    {confirmButton}
                  </span>
                </div>
              </>
            )}
          </JoinCard>
          {phone && (
            <>
              <div className="mt-3 overflow-hidden rounded-3xl border border-border bg-white">
                <button type="button" aria-expanded={qr} onClick={() => setQr((v) => !v)} className="flex min-h-14 w-full items-center gap-3.5 px-4 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset">
                  <QrCodeIcon className="size-[22px] shrink-0 text-muted-foreground" aria-hidden="true" />
                  <span className="min-w-0 flex-1 text-base">{t('twofa.showQr')}</span>
                  <ChevronRightIcon className={cn('size-5 text-muted-foreground transition-transform duration-(--motion-standard) ease-standard', qr && 'rotate-90')} aria-hidden="true" />
                </button>
                {qr && (
                  <div className="flex justify-center border-t border-border p-4">
                    <QrImage svg={stage.setup.qrCodeSvg} className="size-48" />
                  </div>
                )}
              </div>
              <p id={labelId} className="mt-5 px-1 text-[15px] font-semibold">
                {t('twofa.typeCode')}
              </p>
              <CodeField value={s.code} onChange={s.setCode} onComplete={(v) => void s.confirm(v)} invalid={s.wrong} labelledBy={labelId} className="mt-3" />
              <ErrorLine text={s.wrong ? t('signin.wrong') : s.error} className="text-center" />
              <div className="mt-auto flex flex-col gap-1 pt-6">
                {confirmButton}
                {laterButton}
              </div>
            </>
          )}
        </form>
      )
      break
    default: {
      const unreachable: never = stage
      body = unreachable
    }
  }
  return (
    <JoinShell step={1}>
      <div className={cn('flex w-full max-w-[540px] flex-col', phone && 'flex-1')}>
        {phone && <p className="mb-2 px-1 text-[13px] text-muted-foreground">{t('join.stepOf', { n: 2, total: 3, step: t('join.stepTwoFactor') })}</p>}
        {body}
      </div>
    </JoinShell>
  )
}

/** A link that can't be used: it doesn't work, ran out, or was already used. Never names anyone the panel didn't. */
function RefusalCard({ refusal, kind, error, inviter }: { refusal: Refusal; kind?: Kind; error?: ApiError; inviter?: string }) {
  const phone = useIsPhone()
  const signInLink = (
    <a {...linkPath('/login')} className="rounded-sm text-[13px] font-medium text-primary outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring max-sm:text-[15px]">
      {t('join.signInLink')}
    </a>
  )
  const name = inviter || String(error?.params?.inviter ?? '')
  const askLink = name ? t('join.askNewLinkFrom', { inviter: name }) : t('join.askNewLink')
  switch (refusal) {
    case 'not_working':
      return <RefusalLayout pose="search" title={t('join.notWorking')} body={t('join.askNewLink')} action={kind === 'player' ? undefined : signInLink} phone={phone} />
    case 'expired':
      if (kind === 'player') return <RefusalLayout pose="sleep" title={t('join.ranOut')} body={askLink} phone={phone} />
      return <RefusalLayout pose="sleep" title={t('join.expired')} body={name ? t('join.askNewOneFrom', { inviter: name }) : t('join.askNewOne')} action={signInLink} phone={phone} />
    case 'used_up': {
      if (kind === 'player') {
        const max = Number(error?.params?.maxUses)
        return <RefusalLayout pose="sleep" title={t('join.ranOut')} sub={max > 0 ? t('join.ranOutFor', { count: max }) : undefined} body={askLink} phone={phone} />
      }
      const button = (
        <Button size={phone ? 'touch' : 'lg'} className="w-full" render={<a {...linkPath('/login')} />}>
          {t('join.signIn')}
        </Button>
      )
      return <RefusalLayout pose="letter" title={t('join.used')} body={t('join.usedBody')} action={button} phone={phone} />
    }
    default: {
      const unreachable: never = refusal
      return unreachable
    }
  }
}

function RefusalLayout({ pose, title, sub, body, action, phone }: { pose: PipPose; title: string; sub?: string; body?: string; action?: ReactNode; phone: boolean }) {
  return (
    <JoinCard className="max-w-[400px]">
      {phone ? (
        <div className="flex items-center gap-4">
          <Pip pose={pose} size={60} />
          <div className="min-w-0">
            <h1 className="text-xl leading-6 font-bold tracking-[-0.01em]">{title}</h1>
            {sub && <p className="mt-1 text-sm text-muted-foreground">{sub}</p>}
          </div>
        </div>
      ) : (
        <>
          <Pip pose={pose} size={64} />
          <h1 className="mt-5 text-xl leading-7 font-bold tracking-[-0.01em]">{title}</h1>
          {sub && <p className="mt-1.5 text-[13px] text-muted-foreground">{sub}</p>}
        </>
      )}
      {body && <p className={cn('mt-2.5 text-sm text-muted-foreground', phone && 'mt-4 text-[15px]', phone && sub && 'text-foreground')}>{body}</p>}
      {action && <div className="mt-8 max-sm:mt-4">{action}</div>}
    </JoinCard>
  )
}

/** The page couldn't ask about the link: too many tries, or the machine didn't answer. */
function TroubleCard({ error, onRetry }: { error: ApiError; onRetry: () => void }) {
  const phone = useIsPhone()
  const retry = (
    <Button variant="outline" size={phone ? 'touch' : 'default'} className="max-sm:w-full" onClick={onRetry}>
      {t('common.tryAgain')}
    </Button>
  )
  return <RefusalLayout pose="hardhat" title={error.message} body={error.hint} action={retry} phone={phone} />
}
