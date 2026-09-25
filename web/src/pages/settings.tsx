import { useEffect, useState, type FormEvent } from 'react'
import { CircleArrowUpIcon, ExternalLinkIcon, LogOutIcon, RefreshCwIcon } from 'lucide-react'
import { get, post } from '@/api/client'
import type { AuditEntry, UpdateInfo } from '@/api/types'
import { errorText, machineApi, useWorkspace } from '@/api/workspace'
import { Card, CardHint, CardTitle } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { PageBody, PageHeader, PhoneBackHeader, roleLabel } from '@/components/app/shell'
import { UpdateDialog, useUpdateInfo } from '@/components/app/update'
import { Button } from '@/components/ui/button'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { formatDateTime, relativeTime } from '@/lib/format'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { PasswordField } from './onboarding'

export function GlobalSettingsPage() {
  const phone = useIsPhone()
  const hash = window.location.hash
  useEffect(() => {
    if (!hash) return
    document.getElementById(hash.slice(1))?.scrollIntoView({ block: 'start' })
  }, [hash])
  return (
    <>
      {phone && <PhoneBackHeader to={{ name: 'more' }} label={t('nav.more')} />}
      <PageHeader title={t('global.title')} subtitle={t('global.lead')} />
      <PageBody className="flex max-w-[860px] flex-col gap-4">
        <AccountCard />
        <PlaykeeperCard />
        <AuditCard />
        <Card as="section" aria-labelledby="about-title">
          <CardTitle id="about-title">{t('global.about')}</CardTitle>
          <p className="mt-1 text-[13px] text-muted-foreground">{t('global.aboutBody')}</p>
          <p className="mt-3 text-xs text-muted-foreground">{t('footer.notOfficial')}</p>
          <a href={t('global.noticesUrl')} target="_blank" rel="noreferrer" className="mt-3 inline-flex items-center gap-1 self-start text-xs font-medium text-primary hover:underline">
            {t('global.notices')}
            <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
          </a>
        </Card>
      </PageBody>
    </>
  )
}

function AccountCard() {
  const ws = useWorkspace()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()

  async function change(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      await post('/api/auth/password', { currentPassword: current, newPassword: next })
      setCurrent('')
      setNext('')
      toastManager.add({ title: t('global.passwordChanged'), type: 'success' })
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  async function everywhere() {
    try {
      await post('/api/auth/logout-all')
    } finally {
      await ws.signOut()
    }
  }

  return (
    <Card as="section" aria-labelledby="account-title">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <CardTitle id="account-title">{t('global.account')}</CardTitle>
          <CardHint>
            {t('global.signedInAs', { name: ws.me.user.username })}
            {t('common.dot')}
            {roleLabel(ws.me.user.role)}
          </CardHint>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={() => void ws.signOut()}>
            <LogOutIcon />
            {t('nav.signOut')}
          </Button>
          <Button variant="ghost" size="sm" onClick={() => void everywhere()}>
            {t('global.signOutEverywhere')}
          </Button>
        </div>
      </div>
      <form onSubmit={change} className="mt-4 grid gap-4 border-t border-border pt-4 sm:grid-cols-2">
        <PasswordField id="current-password" label={t('global.currentPassword')} value={current} onChange={setCurrent} autoComplete="current-password" />
        <PasswordField id="new-password" label={t('global.newPassword')} value={next} onChange={setNext} autoComplete="new-password" meter />
        {error && (
          <p className="text-[13px] text-destructive-foreground sm:col-span-2" role="alert">
            {error}
          </p>
        )}
        <div className="sm:col-span-2">
          <Button type="submit" variant="outline" loading={busy} disabled={!current || next.length < 10}>
            {t('global.changePassword')}
          </Button>
        </div>
      </form>
    </Card>
  )
}

function PlaykeeperCard() {
  const ws = useWorkspace()
  const { info, setInfo, error } = useUpdateInfo(true)
  const [checking, setChecking] = useState(false)
  const [open, setOpen] = useState(false)

  async function check() {
    if (!ws.machine) return
    setChecking(true)
    try {
      setInfo(await post<UpdateInfo>(machineApi(ws.machine.id, '/update/check')))
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setChecking(false)
    }
  }

  const last = info?.lastResult
  return (
    <Card as="section" aria-labelledby="pk-title" id="updates" className="scroll-mt-4">
      <CardTitle id="pk-title">{t('global.playkeeper')}</CardTitle>
      <CardHint>{t('machine.version', { version: info?.current ?? ws.me.version })}</CardHint>
      <div className="mt-4 flex flex-wrap items-center gap-3 border-t border-border pt-4">
        <p className="min-w-0 flex-1 text-[13px]">
          {error ? (
            <span className="text-destructive-foreground">{error}</span>
          ) : !info ? (
            <span className="text-muted-foreground">{t('common.loading')}</span>
          ) : !info.supported ? (
            <span className="text-muted-foreground">{info.reason ?? t('update.unsupported')}</span>
          ) : ws.updating ? (
            <span className="font-medium text-info-foreground">{t('update.updatingTitle', { version: ws.updating })}</span>
          ) : info.available && info.latest ? (
            <span>
              <span className="font-semibold">{t('update.title', { version: info.latest })}</span>
              {info.checkError && <span className="block text-xs text-destructive-foreground">{info.checkError}</span>}
            </span>
          ) : (
            <span className="text-muted-foreground">{info.checkedAt ? t('update.latestChecked', { time: relativeTime(info.checkedAt) }) : t('update.latest')}</span>
          )}
          {last && (
            <span className={cn('mt-1 block text-xs', last.outcome === 'updated' ? 'text-muted-foreground' : 'text-destructive-foreground')}>
              {last.outcome === 'updated' ? t('update.lastUpdated', { from: last.from, to: last.to, time: relativeTime(last.finishedAt) }) : t('update.lastFailed', { to: last.to, from: last.from, error: last.error ?? '' })}
            </span>
          )}
        </p>
        {info?.supported && (
          <div className="flex gap-2">
            <Button variant="ghost" size="sm" onClick={check} loading={checking} disabled={!!ws.updating}>
              <RefreshCwIcon />
              {t('update.check')}
            </Button>
            {(info.available || ws.updating) && (
              <Button size="sm" onClick={() => setOpen(true)}>
                <CircleArrowUpIcon />
                {ws.updating ? t('nav.updating') : t('update.update')}
              </Button>
            )}
          </div>
        )}
      </div>
      <UpdateDialog open={open} onOpenChange={setOpen} />
    </Card>
  )
}

function AuditCard() {
  const ws = useWorkspace()
  const audit = usePoll(() => get<AuditEntry[]>('/api/audit'), 30_000)
  const serverName = (id?: string) => (id ? (ws.servers?.find((s) => s.id === id)?.name ?? '') : '')
  const rows = (audit.data ?? []).slice(0, 100)
  return (
    <Card as="section" aria-labelledby="audit-title" id="audit" className="scroll-mt-4">
      <CardTitle id="audit-title">{t('global.audit')}</CardTitle>
      <CardHint>{t('global.auditHint')}</CardHint>
      <div className="mt-4 max-h-[480px] overflow-auto rounded-2xl border border-border" tabIndex={0} role="region" aria-labelledby="audit-title">
        <table className="w-full min-w-[640px] text-[13px]">
          <thead className="sticky top-0 bg-muted text-left text-xs text-muted-foreground">
            <tr className="h-9">
              <th className="px-3 font-medium">{t('global.col.when')}</th>
              <th className="px-3 font-medium">{t('global.col.who')}</th>
              <th className="px-3 font-medium">{t('global.col.what')}</th>
              <th className="px-3 font-medium">{t('global.col.server')}</th>
              <th className="px-3 font-medium">{t('global.col.result')}</th>
              <th className="px-3 font-medium">{t('global.col.details')}</th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 && (
              <tr>
                <td colSpan={6} className="px-3 py-4 text-muted-foreground">
                  {audit.data ? t('global.auditEmpty') : t('common.loading')}
                </td>
              </tr>
            )}
            {rows.map((e) => (
              <tr key={`${e.source}-${e.id}`} className="h-11 border-t border-border align-top">
                <td className="px-3 py-2 whitespace-nowrap text-muted-foreground">{formatDateTime(e.ts)}</td>
                <td className="px-3 py-2">{e.actor}</td>
                <td className="px-3 py-2 font-mono text-xs">{e.action}</td>
                <td className="px-3 py-2">{serverName(e.serverId)}</td>
                <td className={cn('px-3 py-2', e.result === 'succeeded' ? 'text-success-foreground' : e.result === 'failed' || e.result === 'refused' ? 'text-destructive-foreground' : 'text-muted-foreground')}>{e.result}</td>
                <td className="max-w-[280px] px-3 py-2 break-words text-muted-foreground">{e.detail}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  )
}
