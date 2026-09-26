import { useCallback, useEffect, useId, useState, type FormEvent, type ReactNode } from 'react'
import { ChevronRightIcon, KeyRoundIcon, LogOutIcon, RefreshCwIcon } from 'lucide-react'
import { ApiError, get, post } from '@/api/client'
import type { TwoFactorStatus } from '@/api/types'
import { errorText, useWorkspace } from '@/api/workspace'
import { Card, CardTitle, Marker } from '@/components/app/bits'
import { SettingRow, useIsPhone } from '@/components/app/controls'
import { PasswordField } from '@/components/app/password-field'
import { Avatar, PageBody, PageHeader, PhoneBackHeader, roleLabel } from '@/components/app/shell'
import { InlineSkeleton, LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogPopup } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { formatDate, relativeAge } from '@/lib/format'
import { linkProps, navigate, type Route } from '@/lib/router'
import { cn } from '@/lib/utils'
import { Group } from './more'
import { DialogButtons, DialogHeading, ErrorLine, NewCodesDialog, recoveryCodeCount, refusal, SetupDialog, SetupPage, TurnOffDialog } from './two-factor'

type AccountDialog = 'password' | 'new-codes' | 'turn-off'

/** The signed-in user: their password, two-factor sign-in and recovery codes. */
export function AccountPage({ section }: { section?: 'two-factor' }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const [status, setStatus] = useState<TwoFactorStatus>()
  const [loadError, setLoadError] = useState<string>()
  const [dialog, setDialog] = useState<AccountDialog>()
  const hash = window.location.hash

  const refresh = useCallback(async () => {
    try {
      setStatus(await get<TwoFactorStatus>('/api/auth/2fa'))
      setLoadError(undefined)
    } catch (e) {
      setLoadError(errorText(e))
    }
  }, [])
  // Leaving the setup may have left one started, so the status is read again.
  useEffect(() => {
    void refresh()
  }, [refresh, section])

  // "Change password" in the notice after signing in links to #password.
  useEffect(() => {
    if (hash !== '#password') return
    setDialog('password')
    window.history.replaceState(null, '', window.location.pathname)
  }, [hash])

  const on = status?.state === 'on'
  useEffect(() => {
    if (section === 'two-factor' && on) navigate({ name: 'account' }, true)
  }, [section, on])

  const closeSetup = () => navigate({ name: 'account' }, true)
  function finishSetup() {
    toastManager.add({ title: t('twofa.onToast'), type: 'success' })
    closeSetup()
  }
  async function everywhere() {
    try {
      await post('/api/auth/logout-all')
    } finally {
      await ws.signOut()
    }
  }
  const dialogProps = (d: AccountDialog) => ({ open: dialog === d, onOpenChange: (open: boolean) => setDialog((cur) => (open ? d : cur === d ? undefined : cur)) })

  if (phone && section === 'two-factor') {
    if (status && !on) return <SetupPage pending={status.state === 'pending'} onDone={finishSetup} />
    return (
      <>
        <PhoneBackHeader to={{ name: 'account' }} label={t('account.phoneTitle')} title={t('twofa.phoneTitle')} />
        {loadError ? (
          <div className="px-1 pt-2">
            <ErrorLine text={loadError} className="mt-0" />
            <Button variant="outline" className="mt-3" onClick={() => void refresh()}>
              {t('common.tryAgain')}
            </Button>
          </div>
        ) : (
          <div className="flex flex-col pt-1">
            <LoadingLabel />
            <Skeleton className="mx-1 h-3 w-20" />
            <Skeleton className="mt-3 h-[124px] rounded-3xl" />
          </div>
        )}
      </>
    )
  }

  const name = ws.me.user.username
  const role = roleLabel(ws.me)
  const changedAt = ws.me.passwordChangedAt
  const left = status?.recoveryCodesLeft ?? 0
  const retry = (
    <Button variant="outline" size="sm" onClick={() => void refresh()}>
      {t('common.tryAgain')}
    </Button>
  )

  return (
    <>
      {phone ? <PhoneBackHeader to={{ name: 'more' }} label={t('nav.more')} title={t('account.phoneTitle')} /> : <PageHeader title={t('account.title')} subtitle={`${t('account.signedInAs', { name })}${t('common.dot')}${role}`} />}
      <PageBody className="flex flex-col gap-4 max-sm:gap-5 max-sm:pt-2 max-sm:pb-6">
        {phone ? (
          <>
            <Group label={t('account.you')}>
              <li className="flex min-h-14 items-center gap-3.5 px-4 py-2">
                <Avatar name={name} className="size-8" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-base">{name}</span>
                  <span className="block text-[13px] text-muted-foreground">{role}</span>
                </span>
              </li>
            </Group>
            <Group label={t('account.signingIn')}>
              <li>
                <PhoneRow title={t('account.password')} hint={changedAt && t('account.passwordAgeShort', { time: relativeAge(changedAt) })} chevron onClick={() => setDialog('password')} />
              </li>
              <li>
                {!status ? (
                  <PhoneRow title={t('account.twoFactor')} hint={loadError} value={loadError ? t('common.tryAgain') : <InlineSkeleton className="h-4 w-12" />} onClick={loadError ? () => void refresh() : undefined} />
                ) : on ? (
                  <PhoneRow title={t('account.twoFactor')} hint={status.confirmedAt && t('account.onSinceShort', { date: formatDate(status.confirmedAt) })} value={t('account.on')} />
                ) : (
                  <PhoneRow title={t('account.twoFactor')} hint={t('account.off')} value={t('account.turnOn')} to={{ name: 'account', section: 'two-factor' }} />
                )}
              </li>
              {on && (
                <li>
                  <PhoneRow title={t('account.recoveryCodes')} hint={t('account.codesLeftShort', { count: left, total: recoveryCodeCount })} value={t('account.newCodes')} onClick={() => setDialog('new-codes')} />
                </li>
              )}
            </Group>
            <Group>
              {on && (
                <li>
                  <PhoneRow title={t('account.turnOffTwoFactor')} danger onClick={() => setDialog('turn-off')} />
                </li>
              )}
              <li>
                <PhoneRow title={t('account.signOutEverywhere')} danger onClick={() => void everywhere()} />
              </li>
            </Group>
          </>
        ) : (
          <>
            <Card aria-labelledby="account-you">
              <CardTitle id="account-you">{t('account.you')}</CardTitle>
              <div className="mt-1">
                <SettingRow label={t('account.username')} control={<span className="text-sm font-semibold">{name}</span>} />
                <SettingRow label={t('account.role')} control={<span className="text-sm font-semibold">{role}</span>} />
              </div>
            </Card>
            <Card aria-labelledby="account-signing-in">
              <CardTitle id="account-signing-in">{t('account.signingIn')}</CardTitle>
              <div className="mt-1">
                <SettingRow
                  label={t('account.password')}
                  hint={changedAt && t('account.passwordAge', { time: relativeAge(changedAt) })}
                  control={
                    <Button variant="outline" size="sm" onClick={() => setDialog('password')}>
                      <KeyRoundIcon />
                      {t('account.changePassword')}
                    </Button>
                  }
                />
                {!status ? (
                  <SettingRow label={t('account.twoFactor')} hint={loadError ? <span className="text-destructive-foreground">{loadError}</span> : t('account.twoFactorHint')} control={
                      loadError ? (
                        retry
                      ) : (
                        <>
                          <LoadingLabel />
                          <Skeleton className="h-7 w-20 rounded-lg" />
                        </>
                      )
                    }
                  />
                ) : on ? (
                  <SettingRow
                    label={
                      <>
                        {t('account.twoFactor')}
                        <Marker tone="green">{t('account.on')}</Marker>
                      </>
                    }
                    hint={status.confirmedAt && t('account.onSince', { date: formatDate(status.confirmedAt) })}
                    control={
                      <Button variant="outline" size="sm" onClick={() => setDialog('turn-off')}>
                        {t('account.turnOff')}
                      </Button>
                    }
                  />
                ) : (
                  <SettingRow
                    label={t('account.twoFactor')}
                    hint={t('account.twoFactorHint')}
                    control={
                      <Button size="sm" render={<a {...linkProps({ name: 'account', section: 'two-factor' })} />}>
                        {t('account.turnOn')}
                      </Button>
                    }
                  />
                )}
                {on && (
                  <SettingRow
                    label={t('account.recoveryCodes')}
                    hint={t('account.codesLeft', { count: left, total: recoveryCodeCount })}
                    control={
                      <Button variant="outline" size="sm" onClick={() => setDialog('new-codes')}>
                        <RefreshCwIcon />
                        {t('account.makeNewCodes')}
                      </Button>
                    }
                  />
                )}
                <SettingRow
                  label={t('account.sessions')}
                  hint={t('account.sessionsHint')}
                  control={
                    <Button variant="outline" size="sm" onClick={() => void everywhere()}>
                      <LogOutIcon />
                      {t('account.signOutEverywhere')}
                    </Button>
                  }
                />
              </div>
            </Card>
          </>
        )}
      </PageBody>
      {!phone && status && <SetupDialog open={section === 'two-factor' && !on} pending={status.state === 'pending'} onClose={closeSetup} onDone={finishSetup} />}
      <ChangePasswordDialog {...dialogProps('password')} />
      <NewCodesDialog {...dialogProps('new-codes')} left={left} onChanged={() => void refresh()} />
      <TurnOffDialog {...dialogProps('turn-off')} onChanged={() => void refresh()} />
    </>
  )
}

function PhoneRow({ title, hint, value, chevron, to, onClick, danger }: { title: string; hint?: string; value?: ReactNode; chevron?: boolean; to?: Route; onClick?: () => void; danger?: boolean }) {
  const body = (
    <>
      <span className="min-w-0 flex-1">
        <span className={cn('block text-base', danger && 'text-destructive-foreground')}>{title}</span>
        {hint && <span className="block truncate text-[13px] text-muted-foreground">{hint}</span>}
      </span>
      {value && <span className="shrink-0 text-[15px] text-success-foreground">{value}</span>}
      {chevron && <ChevronRightIcon className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />}
    </>
  )
  const cls = 'flex min-h-14 w-full items-center gap-3 px-4 py-2 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset'
  if (to) {
    return (
      <a {...linkProps(to)} className={cls}>
        {body}
      </a>
    )
  }
  if (onClick) {
    return (
      <button type="button" onClick={onClick} className={cls}>
        {body}
      </button>
    )
  }
  return <div className={cls}>{body}</div>
}

function ChangePasswordDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const phone = useIsPhone()
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[480px]" showCloseButton={phone}>
        <ChangePassword onClose={() => onOpenChange(false)} />
      </DialogPopup>
    </Dialog>
  )
}

function ChangePassword({ onClose }: { onClose: () => void }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const currentId = useId()
  const nextId = useId()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [currentError, setCurrentError] = useState<string>()
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const reason = !current || !next ? t('reason.fillIn') : next.length < 10 ? t('reason.passwordShort') : undefined
  const ready = !reason

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (!ready || busy) return
    setBusy(true)
    setCurrentError(undefined)
    setError(undefined)
    try {
      await post('/api/auth/password', { currentPassword: current, newPassword: next })
      toastManager.add({ title: t('account.passwordChanged'), type: 'success' })
      onClose()
      void ws.reloadMe().catch(() => undefined)
    } catch (err) {
      if (err instanceof ApiError && err.status === 403) setCurrentError(err.message)
      else setError(refusal(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={submit} noValidate className="overflow-y-auto px-6 pt-6 max-sm:px-5 max-sm:pt-4">
      <DialogHeading title={t('account.changePassword')}>{t('account.changePasswordBody')}</DialogHeading>
      <div className="mt-4 flex flex-col gap-4">
        <PasswordField
          id={currentId}
          label={t('account.currentPassword')}
          value={current}
          onChange={(v) => {
            setCurrent(v)
            setCurrentError(undefined)
          }}
          autoComplete="current-password"
          error={currentError}
        />
        <PasswordField id={nextId} label={t('account.newPassword')} value={next} onChange={setNext} autoComplete="new-password" meter />
      </div>
      <ErrorLine text={error} />
      <DialogButtons>
        {!phone && (
          <Button type="button" variant="ghost" onClick={onClose}>
            {t('common.cancel')}
          </Button>
        )}
        <Button type="submit" size={phone ? 'touch' : 'default'} loading={busy} disabledReason={reason}>
          {t('account.changePassword')}
        </Button>
      </DialogButtons>
    </form>
  )
}
