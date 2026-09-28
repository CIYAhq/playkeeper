import { useState, type ReactNode } from 'react'
import { ArrowUpRightIcon } from 'lucide-react'
import { get, post } from '@/api/client'
import type { PublicPageView, ServerStatus } from '@/api/types'
import { errorText, serverApi, useServerMachine, useWorkspace } from '@/api/workspace'
import { SettingRow } from '@/components/app/controls'
import { InlineSkeleton } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { can } from '@/lib/access'
import { linkPath } from '@/lib/router'
import { pageReach, portProblem } from '@/lib/server-page'
import { usePoll } from '@/lib/usePoll'

// 0.4.3: the public page at the machine's address. Settings' "Public page"
// group: whether the server is on it, whether it names who's playing, and a
// line on whether browsers reach it.

export function usePublicPage(serverId: string) {
  return usePoll(() => get<PublicPageView>(serverApi(serverId, '/public-page')), 30_000, serverId)
}

export function PublicPageRows({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const { stale } = useServerMachine(s)
  const view = usePublicPage(s.id)
  const [pending, setPending] = useState<{ enabled?: boolean; players?: boolean }>()
  const v = view.data
  const enabled = pending?.enabled ?? v?.enabled ?? false
  const players = pending?.players ?? v?.players ?? false
  const locked = stale ? t('reason.noAgent') : !v ? t('common.loading') : pending ? t('reason.saving') : !can(ws.me, 'servers.manage') ? t('publicPage.notAllowed') : !v.ports ? t('publicPage.otherMachine') : undefined

  async function save(next: { enabled?: boolean; players?: boolean }) {
    setPending(next)
    try {
      const r = await post<PublicPageView>(serverApi(s.id, '/public-page'), next)
      const title = next.enabled !== undefined ? (r.enabled ? t('publicPage.onToast', { server: s.name }) : t('publicPage.offToast', { server: s.name })) : r.players ? t('publicPage.playersOnToast') : t('publicPage.playersOffToast')
      toastManager.add({ title, type: 'success' })
      // The page's ports open a moment after it's turned on.
      window.setTimeout(() => void view.refresh(), 1500)
      await view.refresh()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setPending(undefined)
    }
  }

  return (
    <>
      <SettingRow
        label={t('publicPage.show')}
        hint={v ? <Reach view={{ ...v, enabled }} server={s} onRetry={view.refresh} /> : <InlineSkeleton className="w-64 max-w-full" />}
        control={<Switch checked={enabled} onCheckedChange={(c) => void save({ enabled: c })} disabled={!!locked} title={locked} aria-label={t('publicPage.show')} />}
      />
      <SettingRow
        label={t('publicPage.players')}
        hint={t('publicPage.playersHint')}
        control={<Switch checked={enabled && players} onCheckedChange={(c) => void save({ players: c })} disabled={!!locked || !enabled} title={locked ?? (!enabled ? t('publicPage.pageFirst') : undefined)} aria-label={t('publicPage.players')} />}
      />
    </>
  )
}

/** The page row's line: where it answers, or why browsers can't reach it. */
function Reach({ view, server: s, onRetry }: { view: PublicPageView; server: ServerStatus; onRetry: () => Promise<void> }) {
  const r = pageReach(view)
  switch (r.kind) {
    case 'off':
      return <>{t('publicPage.offHint')}</>
    case 'otherMachine':
      return <>{t('publicPage.otherMachine')}</>
    case 'noAddress':
      return <NoAddress server={s} />
    case 'live':
      return (
        <>
          {t('publicPage.liveHint')} <PageLink url={r.url} />
        </>
      )
    case 'plainOnly':
      return (
        <>
          {t('publicPage.liveHint')} <PageLink url={r.url} />
          <span className="mt-1 block">
            {portProblem(r.https)} <Retry server={s} onRetry={onRetry} />
          </span>
        </>
      )
    case 'opening':
      return <>{t('publicPage.opening')}</>
    case 'waiting':
      return <>{t('publicPage.waiting')}</>
    case 'blocked':
      return (
        <>
          {portProblem(r.port)} {r.port.state !== 'denied' && <Retry server={s} onRetry={onRetry} />}
        </>
      )
    default: {
      const unreachable: never = r
      return unreachable
    }
  }
}

function PageLink({ url }: { url: string }) {
  return (
    <a href={url} target="_blank" rel="noreferrer noopener" aria-label={t('common.external', { label: url })} className="inline-flex items-center gap-0.5 font-medium text-success-strong hover:underline">
      {url.replace(/^https?:\/\//, '')}
      <ArrowUpRightIcon className="size-3.5" aria-hidden="true" />
    </a>
  )
}

function NoAddress({ server: s }: { server: ServerStatus }) {
  const { machine } = useServerMachine(s)
  const link: ReactNode = machine ? (
    <a {...linkPath(`/machines/${machine.id}/settings`)} className="font-medium text-success-strong hover:underline">
      {t('publicPage.addressLink')}
    </a>
  ) : null
  return (
    <>
      {t('publicPage.noAddress')} {link}
    </>
  )
}

/** Try the ports again, once the owner freed them. */
function Retry({ server: s, onRetry }: { server: ServerStatus; onRetry: () => Promise<void> }) {
  const ws = useWorkspace()
  const [busy, setBusy] = useState(false)
  if (!can(ws.me, 'servers.manage')) return null
  async function retry() {
    setBusy(true)
    try {
      await post(serverApi(s.id, '/public-page/retry'), {})
      await new Promise((resolve) => window.setTimeout(resolve, 1500))
      await onRetry()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  return (
    <Button variant="link" size="xs" className="h-auto px-0 align-baseline text-[13px] font-medium text-success-strong sm:h-auto sm:text-[13px]" onClick={retry} loading={busy}>
      {t('publicPage.retry')}
    </Button>
  )
}
