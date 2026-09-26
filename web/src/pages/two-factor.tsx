import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { ArrowRightIcon, ChevronRightIcon, DownloadIcon, ExternalLinkIcon, KeyRoundIcon, QrCodeIcon, RefreshCwIcon } from 'lucide-react'
import { ApiError, del, get, post } from '@/api/client'
import type { TwoFactorSetup } from '@/api/types'
import { errorText, useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { CopyButton } from '@/components/app/bits'
import { CodeField } from '@/components/app/code-field'
import { useIsPhone } from '@/components/app/controls'
import { PasswordField } from '@/components/app/password-field'
import { PhoneBackHeader } from '@/components/app/shell'
import { LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { cn } from '@/lib/utils'

/** How many recovery codes a new set has (twofactor.RecoveryCodeCount). */
export const recoveryCodeCount = 10
const setupSteps = 3

/** A refusal's message, followed by what to do next when the panel says. */
export function refusal(e: unknown): string {
  return e instanceof ApiError && e.hint ? `${e.message} ${e.hint}` : errorText(e)
}

/** The buttons that end these dialogs, under a line; on phones, one wide button. */
export function DialogButtons({ children }: { children: ReactNode }) {
  return <div className="mt-5 flex justify-end gap-2 border-t border-border pt-4 pb-6 max-sm:mt-6 max-sm:border-t-0 max-sm:pt-0 max-sm:pb-0 max-sm:*:flex-1">{children}</div>
}

export function DialogHeading({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <>
      <DialogTitle className="pr-6 text-lg leading-6 font-bold max-sm:text-xl">{title}</DialogTitle>
      {children && <DialogDescription className="mt-1 text-[13px] max-sm:text-[15px]">{children}</DialogDescription>}
    </>
  )
}

export function ErrorLine({ text, className }: { text?: string; className?: string }) {
  if (!text) return null
  return (
    <p className={cn('mt-3 text-[13px] text-destructive-foreground', className)} role="alert">
      {text}
    </p>
  )
}

type Stage = { step: 'loading' } | { step: 'password' } | { step: 'scan'; setup: TwoFactorSetup } | { step: 'codes'; codes: string[] }

/**
 * Turning two-factor on: the password, then the QR code and a code from the
 * app, then the recovery codes. A setup started earlier and not finished
 * picks up at the QR code; only Cancel throws it away.
 */
function useSetup(pending: boolean, onCodes?: () => void) {
  const [resume] = useState(pending)
  const [stage, setStage] = useState<Stage>({ step: resume ? 'loading' : 'password' })
  const [password, setPasswordValue] = useState('')
  const [passwordError, setPasswordError] = useState<string>()
  const [code, setCodeValue] = useState('')
  const [wrong, setWrong] = useState(false)
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  // A second request before the first answers could undo its step.
  const sending = useRef(false)

  useEffect(() => {
    if (!resume) return
    let cancelled = false
    get<TwoFactorSetup>('/api/auth/2fa/setup').then(
      (setup) => {
        if (!cancelled) setStage({ step: 'scan', setup })
      },
      () => {
        if (!cancelled) setStage({ step: 'password' })
      },
    )
    return () => {
      cancelled = true
    }
  }, [resume])

  async function start(e: FormEvent) {
    e.preventDefault()
    if (!password || sending.current) return
    sending.current = true
    setBusy(true)
    setPasswordError(undefined)
    setError(undefined)
    try {
      const setup = await post<TwoFactorSetup>('/api/auth/2fa/setup', { password })
      setPasswordValue('')
      setCodeValue('')
      setStage({ step: 'scan', setup })
    } catch (err) {
      if (err instanceof ApiError && err.code === 'password_wrong') setPasswordError(err.message)
      else setError(refusal(err))
    } finally {
      sending.current = false
      setBusy(false)
    }
  }

  async function confirm(value: string) {
    if (value.length !== 6 || sending.current) return
    sending.current = true
    setBusy(true)
    setWrong(false)
    setError(undefined)
    try {
      const r = await post<{ recoveryCodes: string[] }>('/api/auth/2fa/confirm', { code: value })
      setStage({ step: 'codes', codes: r.recoveryCodes })
      onCodes?.()
    } catch (err) {
      const kind = err instanceof ApiError ? err.code : ''
      if (kind === 'code_wrong' || kind === 'code_reused') {
        setWrong(true)
      } else if (kind === 'setup_expired' || kind === 'setup_missing') {
        setCodeValue('')
        setStage({ step: 'password' })
        setError(refusal(err))
      } else {
        setError(refusal(err))
      }
    } finally {
      sending.current = false
      setBusy(false)
    }
  }

  return {
    stage,
    password,
    passwordError,
    code,
    wrong,
    error,
    busy,
    setPassword(v: string) {
      setPasswordValue(v)
      setPasswordError(undefined)
    },
    setCode(v: string) {
      setCodeValue(v)
      setWrong(false)
    },
    start,
    confirm,
    /** Throws away the setup that was started but not confirmed. */
    discard() {
      void del('/api/auth/2fa/setup').catch(() => undefined)
    },
  }
}

/** Turning two-factor on, on a computer. Once the recovery codes show, only "I've saved them" closes it. */
export function SetupDialog({ open, pending, onClose, onDone }: { open: boolean; pending: boolean; onClose: () => void; onDone: () => void }) {
  const [saving, setSaving] = useState(false)
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next && !saving) onClose()
      }}
      onOpenChangeComplete={(next) => {
        if (!next) setSaving(false)
      }}
    >
      <DialogPopup className="sm:w-auto sm:max-w-none" showCloseButton={false}>
        <SetupSteps pending={pending} onCodes={() => setSaving(true)} onClose={onClose} onDone={onDone} />
      </DialogPopup>
    </Dialog>
  )
}

function SetupHeading({ n }: { n: number }) {
  return (
    <>
      <DialogTitle className="text-lg leading-6 font-bold">{t('twofa.setupTitle')}</DialogTitle>
      <DialogDescription className="mt-0.5 text-xs">{t('twofa.step', { n, total: setupSteps })}</DialogDescription>
    </>
  )
}

function SetupSteps({ pending, onCodes, onClose, onDone }: { pending: boolean; onCodes: () => void; onClose: () => void; onDone: () => void }) {
  const s = useSetup(pending, onCodes)
  const passwordId = useId()
  const labelId = useId()
  const { stage } = s
  switch (stage.step) {
    case 'loading':
      return (
        <div className="px-6 pt-6 pb-6 sm:w-[600px]">
          <DialogTitle className="text-lg leading-6 font-bold">{t('twofa.setupTitle')}</DialogTitle>
          <LoadingLabel />
          <Skeleton className="mt-1.5 h-2.5 w-20" />
          <div className="mt-4 flex items-start gap-5">
            <Skeleton className="size-44 shrink-0 rounded-2xl" />
            <div className="min-w-0 flex-1 pt-5">
              <Skeleton className="h-3 w-3/5" />
              <Skeleton className="mt-6 h-3 w-2/5" />
              <Skeleton className="mt-2 h-11 rounded-xl" />
            </div>
          </div>
          <Skeleton className="mt-5 h-3 w-40" />
          <Skeleton className="mt-2 h-[52px] w-80 max-w-full rounded-[10px]" />
        </div>
      )
    case 'password':
      return (
        <form onSubmit={s.start} noValidate className="px-6 pt-6 sm:w-[480px]">
          <SetupHeading n={1} />
          <div className="mt-4">
            <PasswordField id={passwordId} label={t('twofa.passwordLabel')} value={s.password} onChange={s.setPassword} autoComplete="current-password" autoFocus error={s.passwordError} />
          </div>
          <ErrorLine text={s.error} />
          <DialogButtons>
            <Button type="button" variant="ghost" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={s.busy} disabledReason={s.password ? undefined : t('reason.passwordFirst')}>
              {t('common.continue')}
              <ArrowRightIcon />
            </Button>
          </DialogButtons>
        </form>
      )
    case 'scan':
      return (
        <form
          onSubmit={(e) => {
            e.preventDefault()
            void s.confirm(s.code)
          }}
          noValidate
          className="px-6 pt-6 sm:w-[600px]"
        >
          <SetupHeading n={2} />
          <div className="mt-4 flex items-start gap-5">
            <QrImage svg={stage.setup.qrCodeSvg} className="size-44 shrink-0 rounded-2xl border border-border bg-white p-2" />
            <div className="min-w-0 flex-1 pt-5">
              <p className="text-[13px] font-semibold">{t('twofa.scan')}</p>
              <p className="mt-5 text-xs font-semibold">{t('twofa.cantScan')}</p>
              <KeyBox value={stage.setup.manualKey} className="mt-2" />
            </div>
          </div>
          <p id={labelId} className="mt-5 text-[13px] font-semibold">
            {t('twofa.typeCode')}
          </p>
          <CodeField value={s.code} onChange={s.setCode} onComplete={(v) => void s.confirm(v)} invalid={s.wrong} autoFocus labelledBy={labelId} className="mt-2 justify-start" />
          <ErrorLine text={s.wrong ? t('signin.wrong') : s.error} />
          <DialogButtons>
            <Button
              type="button"
              variant="ghost"
              onClick={() => {
                s.discard()
                onClose()
              }}
            >
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={s.busy} disabledReason={s.code.length < 6 ? t('reason.sixDigits') : undefined}>
              {t('twofa.confirm')}
            </Button>
          </DialogButtons>
        </form>
      )
    case 'codes':
      return <CodesView codes={stage.codes} step inDialog onSaved={onDone} className="px-6 pt-6 sm:w-[560px]" />
    default: {
      const unreachable: never = stage
      return unreachable
    }
  }
}

/** Turning two-factor on, on a phone: a page of its own under Account. */
export function SetupPage({ pending, onDone }: { pending: boolean; onDone: () => void }) {
  const s = useSetup(pending)
  const [qr, setQr] = useState(false)
  const passwordId = useId()
  const labelId = useId()
  const { stage } = s
  if (stage.step === 'codes') {
    return (
      <div className="flex flex-1 flex-col pb-4">
        <h1 className="py-3 text-center text-[17px] font-semibold">{t('twofa.codesHeader')}</h1>
        <CodesView codes={stage.codes} step onSaved={onDone} className="flex-1" />
      </div>
    )
  }
  const bottom = (label: ReactNode, reason: string | undefined) => (
    <div className="mt-auto pt-6">
      <Button type="submit" size="touch" className="w-full" loading={s.busy} disabledReason={reason}>
        {label}
      </Button>
    </div>
  )
  return (
    <div className="flex flex-1 flex-col pb-4">
      <PhoneBackHeader to={{ name: 'account' }} label={t('account.phoneTitle')} title={t('twofa.phoneTitle')} />
      {stage.step === 'loading' ? (
        <SetupPageSkeleton />
      ) : (
        <>
          <p className="px-1 pt-1 text-[13px] text-muted-foreground">{t('twofa.step', { n: stage.step === 'scan' ? 2 : 1, total: setupSteps })}</p>
          {stage.step === 'password' ? (
            <form onSubmit={s.start} noValidate className="mt-2 flex flex-1 flex-col">
              <section className="rounded-3xl border border-border bg-white p-4">
                <PasswordField
                  id={passwordId}
                  label={t('twofa.passwordLabel')}
                  labelClassName="text-[17px] font-semibold max-sm:text-[17px]"
                  value={s.password}
                  onChange={s.setPassword}
                  autoComplete="current-password"
                  autoFocus
                  error={s.passwordError}
                />
                <ErrorLine text={s.error} />
              </section>
              {bottom(
                <>
                  {t('common.continue')}
                  <ArrowRightIcon />
                </>,
                s.password ? undefined : t('reason.passwordFirst'),
              )}
            </form>
          ) : (
            <form
              onSubmit={(e) => {
                e.preventDefault()
                void s.confirm(s.code)
              }}
              noValidate
              className="mt-2 flex flex-1 flex-col"
            >
              <section className="rounded-3xl border border-border bg-white p-4">
                <h2 className="text-[17px] leading-6 font-semibold">{t('twofa.addToApp')}</h2>
                <Button size="touch" className="mt-3 w-full" render={<a href={stage.setup.uri} />}>
                  {t('twofa.openApp')}
                  <ExternalLinkIcon />
                </Button>
                <p className="mt-3 text-[13px] text-muted-foreground">{t('twofa.orType')}</p>
                <KeyBox value={stage.setup.manualKey} className="mt-2" />
              </section>
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
              {bottom(t('twofa.confirm'), s.code.length < 6 ? t('reason.sixDigits') : undefined)}
            </form>
          )}
        </>
      )}
    </div>
  )
}

/** The phone setup while a started setup loads: the step line, the app card and the code boxes. */
function SetupPageSkeleton() {
  return (
    <div className="flex flex-1 flex-col">
      <LoadingLabel />
      <Skeleton className="mx-1 mt-2 h-3 w-20" />
      <Skeleton className="mt-3 h-[196px] rounded-3xl" />
      <Skeleton className="mt-3 h-14 rounded-3xl" />
      <Skeleton className="mx-1 mt-6 h-3.5 w-44" />
      <Skeleton className="mt-3 h-[52px] rounded-[10px]" />
    </div>
  )
}

function QrImage({ svg, className }: { svg: string; className?: string }) {
  return <img src={`data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`} alt={t('twofa.qrLabel')} className={className} draggable={false} />
}

function KeyBox({ value, className }: { value: string; className?: string }) {
  return (
    <div className={cn('flex items-center gap-3 rounded-xl bg-muted py-2.5 pr-2.5 pl-3.5', className)}>
      <code className="min-w-0 flex-1 font-mono text-[13px] leading-5 font-semibold tracking-wide break-words max-sm:text-sm">{value}</code>
      <CopyButton text={value.replace(/\s/g, '')} size="xs" className="shrink-0 bg-white" />
    </div>
  )
}

/** The new recovery codes, shown once: copy or download them, then say they're saved. */
function CodesView({ codes, step, inDialog, onSaved, className }: { codes: string[]; step?: boolean; inDialog?: boolean; onSaved: () => void; className?: string }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const title = phone ? t('twofa.codesTitleShort') : t('twofa.codesTitle')
  const titleClass = 'text-lg leading-6 font-bold max-sm:text-xl'
  const leadClass = 'mt-4 text-[13px] text-foreground max-sm:mt-3 max-sm:text-[15px]'
  return (
    <div className={cn('flex flex-col', className)}>
      <div className="flex items-center gap-3">
        <Pip pose="letter" size={phone ? 52 : 48} />
        <div className="min-w-0">
          {inDialog ? <DialogTitle className={titleClass}>{title}</DialogTitle> : <h2 className={titleClass}>{title}</h2>}
          {step && <p className="mt-0.5 text-xs text-muted-foreground max-sm:text-[13px]">{t('twofa.step', { n: setupSteps, total: setupSteps })}</p>}
        </div>
      </div>
      {inDialog ? <DialogDescription className={leadClass}>{t('twofa.codesLead')}</DialogDescription> : <p className={leadClass}>{t('twofa.codesLead')}</p>}
      <ol className="mt-4 grid gap-x-10 gap-y-2 rounded-2xl border border-border bg-warm px-5 py-4 font-mono text-[13px] font-semibold max-sm:mt-3 max-sm:gap-y-3 max-sm:text-[15px] sm:grid-flow-col sm:grid-cols-2 sm:grid-rows-5">
        {codes.map((c, i) => (
          <li key={c} className="flex items-baseline gap-3">
            <span className="w-5 shrink-0 text-right font-sans text-xs font-normal text-muted-foreground tabular-nums max-sm:text-[13px]" aria-hidden="true">
              {i + 1}.
            </span>
            {c}
          </li>
        ))}
      </ol>
      <div className="mt-3 flex gap-2 max-sm:grid max-sm:grid-cols-2">
        <CopyButton text={codes.join('\n')} label={t('twofa.copyAll')} size={phone ? 'touch' : 'sm'} className="bg-white" />
        <Button variant="outline" size={phone ? 'touch' : 'sm'} className="bg-white" onClick={() => download(codes, ws.me.user.username)}>
          <DownloadIcon />
          {phone ? t('twofa.downloadShort') : t('twofa.download')}
        </Button>
      </div>
      {phone ? (
        <div className="mt-auto pt-6">
          <Button size="touch" className="w-full" onClick={onSaved}>
            {t('twofa.saved')}
          </Button>
        </div>
      ) : (
        <DialogButtons>
          <Button onClick={onSaved}>{t('twofa.saved')}</Button>
        </DialogButtons>
      )}
    </div>
  )
}

function download(codes: string[], name: string) {
  const lines = [t('twofa.fileHeader', { name, host: window.location.hostname }), '', ...codes.map((c, i) => `${i + 1}. ${c}`), '']
  const url = URL.createObjectURL(new Blob([lines.join('\n')], { type: 'text/plain' }))
  const a = document.createElement('a')
  a.href = url
  a.download = `playkeeper-recovery-codes-${name}.txt`
  a.click()
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}

/**
 * The password and a code, asked before making new recovery codes or
 * turning two-factor off. A recovery code works in place of an app code.
 */
function ProofForm({ label, icon, destructive, onSubmit, onCancel }: { label: string; icon?: ReactNode; destructive?: boolean; onSubmit: (password: string, code: string) => Promise<void>; onCancel: () => void }) {
  const phone = useIsPhone()
  const passwordId = useId()
  const recoveryId = useId()
  const labelId = useId()
  const [password, setPassword] = useState('')
  const [passwordError, setPasswordError] = useState<string>()
  const [recovery, setRecovery] = useState(false)
  const [code, setCode] = useState('')
  const [recoveryCode, setRecoveryCode] = useState('')
  const [wrong, setWrong] = useState(false)
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const reason = !password ? t('reason.passwordFirst') : recovery ? (recoveryCode.trim() ? undefined : t('reason.recoveryCode')) : code.length < 6 ? t('reason.sixDigits') : undefined
  const ready = !reason

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (!ready || busy) return
    setBusy(true)
    setPasswordError(undefined)
    setWrong(false)
    setError(undefined)
    try {
      await onSubmit(password, recovery ? recoveryCode.trim() : code)
    } catch (err) {
      const kind = err instanceof ApiError ? err.code : ''
      if (kind === 'password_wrong') setPasswordError(errorText(err))
      else if (kind === 'code_wrong' || kind === 'code_reused' || kind === 'recovery_code_wrong') setWrong(true)
      else setError(refusal(err))
    } finally {
      setBusy(false)
    }
  }

  function switchTo(toRecovery: boolean) {
    setRecovery(toRecovery)
    setWrong(false)
    setError(undefined)
  }

  return (
    <form onSubmit={submit} noValidate className="flex flex-col">
      <div className="mt-4">
        <PasswordField
          id={passwordId}
          label={t('login.password')}
          labelClassName={phone ? 'sr-only' : undefined}
          value={password}
          onChange={(v) => {
            setPassword(v)
            setPasswordError(undefined)
          }}
          autoComplete="current-password"
          error={passwordError}
        />
      </div>
      {recovery ? (
        <div className="mt-4 flex flex-col gap-1.5">
          <label htmlFor={recoveryId} className="text-[13px] font-medium max-sm:text-[15px]">
            {t('signin.recoveryLabel')}
          </label>
          <InputGroup className="max-sm:h-11">
            <InputGroupAddon>
              <KeyRoundIcon aria-hidden="true" />
            </InputGroupAddon>
            <InputGroupInput
              id={recoveryId}
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
              autoFocus
            />
          </InputGroup>
        </div>
      ) : (
        <div className="mt-4">
          <p id={labelId} className="text-[13px] font-medium max-sm:text-[15px]">
            {t('twofa.appCode')}
          </p>
          <CodeField
            value={code}
            onChange={(v) => {
              setCode(v)
              setWrong(false)
            }}
            invalid={wrong}
            labelledBy={labelId}
            className="mt-2 sm:justify-start"
          />
        </div>
      )}
      <ErrorLine text={wrong ? (recovery ? t('signin.recoveryWrong') : t('signin.wrong')) : error} className="max-sm:text-center" />
      <button type="button" onClick={() => switchTo(!recovery)} className="mt-3 self-start rounded-md text-xs font-medium text-success-strong hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none max-sm:self-center max-sm:text-[13px]">
        {recovery ? t('signin.useApp') : t('signin.useRecovery')}
      </button>
      <DialogButtons>
        {!phone && (
          <Button type="button" variant="ghost" onClick={onCancel}>
            {t('common.cancel')}
          </Button>
        )}
        <Button type="submit" variant={destructive ? 'destructive' : 'default'} size={phone ? 'touch' : 'default'} loading={busy} disabledReason={reason}>
          {!phone && icon}
          {label}
        </Button>
      </DialogButtons>
    </form>
  )
}

const proofBody = 'overflow-y-auto px-6 pt-6 max-sm:px-5 max-sm:pt-4'

/** New recovery codes, after the password and a code. The old ones stop working. */
export function NewCodesDialog({ open, onOpenChange, left, onChanged }: { open: boolean; onOpenChange: (open: boolean) => void; left: number; onChanged: () => void }) {
  const phone = useIsPhone()
  const [codes, setCodes] = useState<string[]>()
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (next || !codes) onOpenChange(next)
      }}
      onOpenChangeComplete={(next) => {
        if (!next) setCodes(undefined)
      }}
    >
      <DialogPopup className={codes ? 'sm:max-w-[560px]' : 'sm:max-w-[480px]'} showCloseButton={phone && !codes}>
        {codes ? (
          <CodesView codes={codes} inDialog onSaved={() => onOpenChange(false)} className={proofBody} />
        ) : (
          <div className={proofBody}>
            <DialogHeading title={t('twofa.newCodesTitle')}>{t('twofa.newCodesBody', { count: left })}</DialogHeading>
            <ProofForm
              label={t('account.makeNewCodes')}
              icon={<RefreshCwIcon />}
              onCancel={() => onOpenChange(false)}
              onSubmit={async (password, code) => {
                const r = await post<{ recoveryCodes: string[] }>('/api/auth/2fa/recovery-codes', { password, code })
                setCodes(r.recoveryCodes)
                onChanged()
              }}
            />
          </div>
        )}
      </DialogPopup>
    </Dialog>
  )
}

export function TurnOffDialog({ open, onOpenChange, onChanged }: { open: boolean; onOpenChange: (open: boolean) => void; onChanged: () => void }) {
  const phone = useIsPhone()
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[480px]" showCloseButton={phone}>
        <div className={proofBody}>
          <DialogHeading title={t('twofa.turnOffTitle')}>{t('twofa.turnOffBody')}</DialogHeading>
          <ProofForm
            label={t('account.turnOffTwoFactor')}
            destructive
            onCancel={() => onOpenChange(false)}
            onSubmit={async (password, code) => {
              await post('/api/auth/2fa/disable', { password, code })
              toastManager.add({ title: t('twofa.offToast'), type: 'success' })
              onOpenChange(false)
              onChanged()
            }}
          />
        </div>
      </DialogPopup>
    </Dialog>
  )
}
