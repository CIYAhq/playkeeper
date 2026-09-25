import { useEffect, useId, useState, type FormEvent } from 'react'
import { KeyRoundIcon, LogInIcon, UserRoundIcon } from 'lucide-react'
import { ApiError, post } from '@/api/client'
import type { Challenge, LoginAnswer, Me } from '@/api/types'
import { Pip } from '@/components/app/art'
import { CodeField } from '@/components/app/code-field'
import { useIsPhone } from '@/components/app/controls'
import { Frame, FrameCard } from '@/components/app/frame'
import { Avatar } from '@/components/app/shell'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { t } from '@/i18n'
import { rich } from '@/i18n/rich'
import { formatCountdown } from '@/lib/format'
import { cn } from '@/lib/utils'
import { PasswordField } from './onboarding'

const phoneCard = 'max-sm:mt-6 max-sm:rounded-3xl max-sm:border max-sm:bg-white max-sm:p-5'

interface Pending {
  username: string
  challenge: Challenge
}

export function LoginPage({ onDone }: { onDone: (m: Me) => void }) {
  const [username, setUsername] = useState('')
  const [pending, setPending] = useState<Pending>()
  const [error, setError] = useState<string>()
  if (pending) {
    return (
      <SecondStep
        {...pending}
        onDone={onDone}
        onBack={(reason) => {
          setPending(undefined)
          setError(reason)
          if (!reason) setUsername('')
        }}
      />
    )
  }
  return (
    <PasswordStep
      username={username}
      setUsername={setUsername}
      error={error}
      setError={setError}
      onAnswer={(a) => {
        if ('secondFactor' in a) setPending({ username: a.user.username, challenge: a.secondFactor })
        else onDone(a)
      }}
    />
  )
}

function PasswordStep({ username, setUsername, error, setError, onAnswer }: { username: string; setUsername: (v: string) => void; error?: string; setError: (e?: string) => void; onAnswer: (a: LoginAnswer) => void }) {
  const phone = useIsPhone()
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setError(undefined)
    setBusy(true)
    try {
      onAnswer(await post<LoginAnswer>('/api/auth/login', { username: username.trim(), password }))
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Frame>
      <FrameCard className={phoneCard}>
        <div className="flex items-center gap-3">
          <Pip pose="wave" size={48} />
          <h1 className="text-xl font-bold">{t('login.title')}</h1>
        </div>
        <form className="mt-5 flex flex-col gap-4" onSubmit={submit}>
          <div className="flex flex-col gap-1.5">
            <label htmlFor="login-username" className="text-[13px] font-medium max-sm:text-[15px]">
              {t('login.username')}
            </label>
            <InputGroup className="max-sm:h-11">
              <InputGroupAddon>
                <UserRoundIcon aria-hidden="true" />
              </InputGroupAddon>
              <InputGroupInput id="login-username" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" required autoFocus />
            </InputGroup>
          </div>
          <PasswordField id="login-password" label={t('login.password')} value={password} onChange={setPassword} autoComplete="current-password" />
          {error && (
            <p className="text-[13px] text-destructive-foreground" role="alert">
              {error}
            </p>
          )}
          <Button type="submit" size={phone ? 'touch' : 'lg'} loading={busy} disabled={!username.trim() || !password}>
            <LogInIcon />
            {busy ? t('login.submitting') : t('login.submit')}
          </Button>
          <p className="text-xs text-muted-foreground">{rich('login.forgot', { code: (chunk) => <code className="rounded bg-muted px-1 py-0.5 text-[11px]">{chunk}</code> }, { command: 'sudo playkeeper reset-password <username>' })}</p>
        </form>
      </FrameCard>
    </Frame>
  )
}

/**
 * The second sign-in step: a code from the authenticator app, or a recovery
 * code. App codes pause after wrong tries and block after 100 in a row, while
 * recovery codes keep working.
 */
function SecondStep({ username, challenge, onDone, onBack }: Pending & { onDone: (m: Me) => void; onBack: (reason?: string) => void }) {
  const phone = useIsPhone()
  const [now, setNow] = useState(() => Date.now())
  const labelId = useId()
  const hasRecovery = challenge.methods.includes('recovery_code')
  const [blocked, setBlocked] = useState(challenge.appCodesBlocked)
  const [lockedUntil, setLockedUntil] = useState(() => (challenge.appCodesLockedUntil ? Date.parse(challenge.appCodesLockedUntil) : 0))
  const [recovery, setRecovery] = useState(false)
  const [code, setCode] = useState('')
  const [recoveryCode, setRecoveryCode] = useState('')
  const [wrong, setWrong] = useState(false)
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [seen, setSeen] = useState(0)
  const locked = !blocked && lockedUntil > now
  const byRecovery = recovery || blocked
  useEffect(() => {
    if (!locked) return
    const id = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(id)
  }, [locked])

  async function send(value: string) {
    setBusy(true)
    setError(undefined)
    setWrong(false)
    try {
      onDone(leaveOutOwnTries(await post<Me>('/api/auth/second-factor', { code: value }), seen))
    } catch (e) {
      if (!(e instanceof ApiError)) {
        setError(String(e))
        return
      }
      switch (e.code) {
        case 'code_wrong':
        case 'recovery_code_wrong':
          setSeen((n) => n + 1)
          setWrong(true)
          break
        case 'code_reused':
          setWrong(true)
          break
        case 'app_codes_locked': {
          const at = Date.now()
          setSeen((n) => n + 1)
          setNow(at)
          setLockedUntil(at + (e.retryAfter ?? 60) * 1000)
          setCode('')
          break
        }
        case 'app_codes_blocked':
          setSeen((n) => n + 1)
          setBlocked(true)
          setCode('')
          break
        case 'unauthorized':
          onBack(e.message)
          break
        default:
          setError(e.message)
      }
    } finally {
      setBusy(false)
    }
  }

  function cancel() {
    void post('/api/auth/second-factor/cancel').catch(() => undefined)
    onBack()
  }

  function submit(e: FormEvent) {
    e.preventDefault()
    if (byRecovery) void send(recoveryCode.trim())
    else if (code.length === 6 && !locked) void send(code)
  }

  function switchTo(toRecovery: boolean) {
    setRecovery(toRecovery)
    setWrong(false)
    setError(undefined)
  }

  const signingInAs = (
    <div className="flex h-10 items-center gap-2.5 rounded-xl bg-muted pr-2 pl-2.5 text-[13px] max-sm:h-11 max-sm:text-[15px]">
      <Avatar name={username} className="size-6 text-xs" />
      <span className="min-w-0 flex-1 truncate">{t('signin.as', { name: username })}</span>
      <button type="button" onClick={cancel} className="rounded-md px-1 py-0.5 text-xs font-medium text-success-strong hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none max-sm:text-[13px]">
        {t('signin.notYou')}
      </button>
    </div>
  )

  const link = 'self-center rounded-md px-1 text-xs font-medium text-success-strong hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none max-sm:text-[13px]'

  return (
    <Frame>
      <FrameCard className={cn('max-w-[420px]', phoneCard)}>
        <form className="flex flex-col" onSubmit={submit} noValidate>
          <h1 className="text-xl font-bold max-sm:text-2xl">{byRecovery ? t('signin.recoveryTitle') : t('signin.codeTitle')}</h1>
          <p className="mt-1 text-[13px] text-muted-foreground max-sm:text-[15px]">{blocked ? t('signin.blocked') : byRecovery ? t('signin.recoveryLead') : t('signin.codeLead')}</p>
          {(!byRecovery || blocked) && <div className="mt-4">{signingInAs}</div>}
          {byRecovery ? (
            <div className="mt-5 flex flex-col gap-1.5">
              <label htmlFor="recovery-code" className="text-[13px] font-medium max-sm:text-[15px]">
                {t('signin.recoveryLabel')}
              </label>
              <InputGroup className="max-sm:h-11">
                <InputGroupAddon>
                  <KeyRoundIcon aria-hidden="true" />
                </InputGroupAddon>
                <InputGroupInput
                  id="recovery-code"
                  value={recoveryCode}
                  onChange={(e) => {
                    setRecoveryCode(e.target.value)
                    setWrong(false)
                  }}
                  placeholder={t('signin.recoveryPlaceholder')}
                  autoComplete="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  maxLength={40}
                  aria-invalid={wrong || undefined}
                  aria-describedby={wrong ? 'second-step-error' : undefined}
                  autoFocus
                />
              </InputGroup>
            </div>
          ) : (
            <>
              <span id={labelId} className="sr-only">
                {t('signin.codeLabel')}
              </span>
              <CodeField
                key={String(locked)}
                value={code}
                onChange={(v) => {
                  setCode(v)
                  setWrong(false)
                }}
                onComplete={(v) => {
                  if (!busy && !locked) void send(v)
                }}
                invalid={wrong}
                disabled={locked}
                autoFocus
                labelledBy={labelId}
                className="mt-4"
              />
            </>
          )}
          {wrong && (
            <p id="second-step-error" className="mt-3 text-[13px] font-medium text-destructive-foreground" role="alert">
              {byRecovery ? t('signin.recoveryWrong') : t('signin.wrong')}
            </p>
          )}
          {locked && !byRecovery && (
            <div className="mt-4 text-[13px]" role="status">
              <p className="font-semibold">{t('signin.lockedTitle')}</p>
              <p className="mt-0.5 text-muted-foreground">{hasRecovery ? t('signin.locked', { time: formatCountdown((lockedUntil - now) / 1000) }) : t('signin.lockedNoRecovery', { time: formatCountdown((lockedUntil - now) / 1000) })}</p>
            </div>
          )}
          {blocked && !hasRecovery && (
            <p className="mt-3 text-[13px] text-muted-foreground">{rich('signin.blockedNoRecovery', { code: (chunk) => <code className="rounded bg-muted px-1 py-0.5 text-[11px]">{chunk}</code> }, { command: `sudo playkeeper reset-2fa ${username}` })}</p>
          )}
          {error && (
            <p className="mt-3 text-[13px] text-destructive-foreground" role="alert">
              {error}
            </p>
          )}
          <Button type="submit" size={phone ? 'touch' : 'lg'} className="mt-5" loading={busy} disabled={byRecovery ? !recoveryCode.trim() || (blocked && !hasRecovery) : code.length < 6 || locked}>
            {t('login.submit')}
          </Button>
          {!blocked && (byRecovery || hasRecovery) && (
            <button type="button" onClick={() => switchTo(!byRecovery)} className={cn(link, 'mt-4')}>
              {byRecovery ? t('signin.useApp') : t('signin.useRecovery')}
            </button>
          )}
        </form>
      </FrameCard>
    </Frame>
  )
}

/** The count of wrong codes includes the ones typed here; only someone else's are worth a notice. */
function leaveOutOwnTries(m: Me, seen: number): Me {
  if (!seen || !m.notices) return m
  return { ...m, notices: m.notices.flatMap((n) => (n.kind !== 'failed_attempts' ? [n] : n.count > seen ? [{ ...n, count: n.count - seen }] : [])) }
}
