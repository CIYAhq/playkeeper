import { useState, type ReactNode } from 'react'
import { ArrowUpRightIcon } from 'lucide-react'
import { api, get, post } from '@/api/client'
import type { PagePort, PublicBoard, PublicPageView, ServerStatus } from '@/api/types'
import { errorText, serverApi, useServerMachine, useWorkspace } from '@/api/workspace'
import { useNow } from '@/components/app/bits'
import { SettingRow } from '@/components/app/controls'
import { InlineSkeleton } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { can } from '@/lib/access'
import { relativeTime } from '@/lib/format'
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
  const locked = stale ? t('reason.noAgent') : !v ? t('common.loading') : pending ? t('reason.saving') : !can(ws.me, 'servers.manage') ? t('publicPage.notAllowed') : undefined

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
      <TextRow
        id="page-about"
        label={t('publicPage.about')}
        hint={t('publicPage.aboutHint')}
        value={v?.about ?? ''}
        max={600}
        multiline
        saveLabel={t('publicPage.saveAbout')}
        disabledReason={locked ?? (!enabled ? t('publicPage.pageFirst') : undefined)}
        onSave={(about) => saveText(s, { about }, view.refresh)}
      />
      <TextRow
        id="page-stream"
        label={t('publicPage.stream')}
        hint={t('publicPage.streamHint')}
        value={v?.stream ?? ''}
        max={200}
        placeholder={t('publicPage.streamPlaceholder')}
        saveLabel={t('publicPage.saveStream')}
        disabledReason={locked ?? (!enabled ? t('publicPage.pageFirst') : undefined)}
        onSave={(stream) => saveText(s, { stream }, view.refresh)}
      />
      <BoardRow server={s} board={v?.board} enabled={enabled} onChange={view.refresh} />
    </>
  )
}

/** Saves the page's words or stream link; a refusal reaches the row that asked. */
async function saveText(s: ServerStatus, body: { about?: string; stream?: string }, refresh: () => Promise<void>) {
  await post<PublicPageView>(serverApi(s.id, '/public-page'), body)
  toastManager.add({ title: t('publicPage.savedToast', { server: s.name }), type: 'success' })
  await refresh()
}

/** A text setting with its own Save: the page's words, or the stream's link. */
function TextRow({ id, label, hint, value, max, multiline, placeholder, saveLabel, disabledReason, onSave }: {
  id: string
  label: string
  hint: string
  value: string
  max: number
  multiline?: boolean
  placeholder?: string
  saveLabel: string
  disabledReason?: string
  onSave: (value: string) => Promise<void>
}) {
  const [draft, setDraft] = useState<string>()
  const [problem, setProblem] = useState<string>()
  const [saving, setSaving] = useState(false)
  const current = draft ?? value
  const changed = draft !== undefined && draft.trim() !== value
  async function commit() {
    setSaving(true)
    setProblem(undefined)
    try {
      await onSave(current)
      setDraft(undefined)
    } catch (e) {
      setProblem(errorText(e))
    } finally {
      setSaving(false)
    }
  }
  const edit = (next: string) => {
    setDraft(next)
    setProblem(undefined)
  }
  return (
    <SettingRow
      wide
      htmlFor={id}
      className="items-start"
      label={label}
      hint={
        problem ? (
          <span role="alert" className="text-destructive-foreground">
            {problem}
          </span>
        ) : (
          hint
        )
      }
      control={
        <div className="flex w-[360px] flex-col gap-2 max-sm:w-full">
          {multiline ? (
            <Textarea id={id} value={current} onChange={(e) => edit(e.target.value)} maxLength={max} rows={5} className="resize-none" disabled={!!disabledReason} />
          ) : (
            <Input id={id} value={current} onChange={(e) => edit(e.target.value)} maxLength={max} placeholder={placeholder} disabled={!!disabledReason} />
          )}
          <div className="flex items-center justify-between gap-3">
            <span className="text-xs text-muted-foreground tabular-nums">{multiline ? t('publicPage.count', { count: current.length, max }) : ''}</span>
            <Button size="sm" variant="outline" aria-label={saveLabel} onClick={() => void commit()} loading={saving} disabledReason={disabledReason ?? (!changed ? t('publicPage.nothingToSave') : undefined)}>
              {t('common.save')}
            </Button>
          </div>
        </div>
      }
    />
  )
}

/** What the owner's tools last posted, and taking it off the page. */
function BoardRow({ server: s, board, enabled, onChange }: { server: ServerStatus; board?: PublicBoard; enabled: boolean; onChange: () => Promise<void> }) {
  const ws = useWorkspace()
  const now = useNow(60_000)
  const [busy, setBusy] = useState(false)
  if (!board) return <SettingRow label={t('publicPage.board')} hint={t('publicPage.boardNone')} control={null} />
  async function clear() {
    setBusy(true)
    try {
      await api('DELETE', serverApi(s.id, '/public-page/board'))
      toastManager.add({ title: t('publicPage.boardCleared', { server: s.name }), type: 'success' })
      await onChange()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  const hint = t('publicPage.boardPosted', { headline: board.headline || t('publicPage.boardUntitled'), when: relativeTime(board.updatedAt, now) })
  return (
    <SettingRow
      label={t('publicPage.board')}
      hint={enabled ? hint : `${hint} ${t('publicPage.boardHidden')}`}
      control={
        can(ws.me, 'servers.run') ? (
          <Button size="sm" variant="outline" onClick={() => void clear()} loading={busy}>
            {t('publicPage.boardClear')}
          </Button>
        ) : null
      }
    />
  )
}

/** The page row's line: where it answers, or why browsers can't reach it. */
function Reach({ view, server: s, onRetry }: { view: PublicPageView; server: ServerStatus; onRetry: () => Promise<void> }) {
  const r = pageReach(view)
  switch (r.kind) {
    case 'off':
      return <>{t('publicPage.offHint')}</>
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
            <PortLine port={r.https} server={s} onRetry={onRetry} />
          </span>
        </>
      )
    case 'opening':
      return <>{t('publicPage.opening')}</>
    case 'waiting':
      return <>{t('publicPage.waiting')}</>
    case 'blocked':
      return <PortLine port={r.port} server={s} onRetry={onRetry} />
    default: {
      const unreachable: never = r
      return unreachable
    }
  }
}

/**
 * Why a port can't serve the page, and what to do: try again once another program let it go, or, for a server on a
 * joined machine without a certificate for its address, the dashboard machine's setting that gives every server one.
 */
function PortLine({ port, server: s, onRetry }: { port: PagePort; server: ServerStatus; onRetry: () => Promise<void> }) {
  const ws = useWorkspace()
  if (port.state === 'no_certificate') {
    const at = ws.machines.find((m) => m.kind === 'local')
    return (
      <>
        {portProblem(port)}{' '}
        {at && can(ws.me, 'machines.view') && (
          <a {...linkPath(`/machines/${at.id}/settings`)} className="font-medium text-success-strong hover:underline">
            {t('publicPage.certificateLink')}
          </a>
        )}
      </>
    )
  }
  return (
    <>
      {portProblem(port)} {port.state !== 'denied' && <Retry server={s} onRetry={onRetry} />}
    </>
  )
}

function PageLink({ url }: { url: string }) {
  return (
    <a href={url} target="_blank" rel="noreferrer noopener" aria-label={t('common.external', { label: url })} className="inline-flex items-center gap-0.5 font-medium text-success-strong hover:underline">
      {url.replace(/^https?:\/\//, '')}
      <ArrowUpRightIcon className="size-3.5" aria-hidden="true" />
    </a>
  )
}

/**
 * Why the page has no address yet. A server on a joined machine has its page at its address without a port, which the
 * dashboard's machine gives it once it answers DNS for its domain; whoever manages machines is sent there.
 */
function NoAddress({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const { machine } = useServerMachine(s)
  const joined = machine?.kind === 'remote'
  const at = joined ? ws.machines.find((m) => m.kind === 'local') : machine
  const link: ReactNode = at && can(ws.me, 'machines.view') ? (
    <a {...linkPath(`/machines/${at.id}/settings`)} className="font-medium text-success-strong hover:underline">
      {joined ? t('publicPage.namesLink') : t('publicPage.addressLink')}
    </a>
  ) : null
  return (
    <>
      {joined ? t('publicPage.noNameYet') : t('publicPage.noAddress')} {link}
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
