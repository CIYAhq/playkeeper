import { useRef, useState, type FormEvent } from 'react'
import { LinkIcon, SendIcon } from 'lucide-react'
import { ApiError, del, get, post, put } from '@/api/client'
import type { DiscordKind, DiscordSettings, ServerStatus } from '@/api/types'
import { errorText, useWorkspace } from '@/api/workspace'
import { BrandMark } from '@/components/app/art'
import { Card, CardHint, CardTitle, Marker, Notice, useNow } from '@/components/app/bits'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { toastManager } from '@/components/ui/toast'
import { t, type MessageKey } from '@/i18n'
import { formatClock, joinAddress, relativeTime } from '@/lib/format'
import { phaseTone } from '@/lib/phase'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

/** The alert switches, each for one or more kinds of alert. Kinds without a switch keep their setting. */
const alertRows: { kinds: DiscordKind[]; title: MessageKey; hint?: MessageKey }[] = [
  { kinds: ['crash', 'recovered'], title: 'discord.alert.crash' },
  { kinds: ['join_requested'], title: 'discord.alert.join', hint: 'discord.alert.joinHint' },
  { kinds: ['backup_failed'], title: 'discord.alert.backup' },
  { kinds: ['low_disk'], title: 'discord.alert.disk', hint: 'discord.alert.diskHint' },
  { kinds: ['update_available'], title: 'discord.alert.update', hint: 'discord.alert.updateHint' },
  { kinds: ['player_joined', 'player_left'], title: 'discord.alert.players', hint: 'discord.alert.playersHint' },
]

interface Problem {
  msg: string
  hint?: string
}

function problem(e: unknown): Problem {
  return e instanceof ApiError ? { msg: e.message, hint: e.hint } : { msg: errorText(e) }
}

/** Settings › Discord: a webhook for alerts and a live status message. */
export function DiscordSettingsSection() {
  const disc = usePoll(() => get<DiscordSettings>('/api/discord'), 20_000)
  const [local, setLocal] = useState<DiscordSettings>()
  const saving = useRef(0)
  const s = local ?? disc.data

  async function save(next: Pick<DiscordSettings, 'alerts' | 'liveStatus'>) {
    if (!s) return
    const n = ++saving.current
    setLocal({ ...s, ...next })
    try {
      const res = await put<DiscordSettings>('/api/discord', next)
      if (n !== saving.current) return
      setLocal(res)
      await disc.refresh()
      if (n === saving.current) setLocal(undefined)
    } catch (e) {
      if (n === saving.current) setLocal(undefined)
      toastManager.add({ title: errorText(e), type: 'error' })
    }
  }

  async function changed(next?: DiscordSettings) {
    saving.current++
    setLocal(next)
    await disc.refresh()
    setLocal(undefined)
  }

  if (disc.error) {
    return (
      <Notice
        tone="error"
        title={disc.error.message}
        action={
          <Button variant="outline" size="sm" onClick={() => void disc.refresh()}>
            {t('common.tryAgain')}
          </Button>
        }
      />
    )
  }
  if (!s) {
    return (
      <Card aria-busy="true">
        <Skeleton className="h-4 w-20" />
        <Skeleton className="mt-2 h-3 w-64" />
        <Skeleton className="mt-5 h-8 w-full" />
      </Card>
    )
  }
  if (!s.connected) return <ConnectCard onConnected={changed} />
  return (
    <>
      <ConnectedCard settings={s} onChanged={changed} />
      <AlertsCard settings={s} onSave={save} />
      <LiveStatusCard settings={s} onSave={save} />
    </>
  )
}

function ConnectCard({ onConnected }: { onConnected: (s: DiscordSettings) => Promise<void> }) {
  const [url, setUrl] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<Problem>()
  const steps: MessageKey[] = ['discord.step1', 'discord.step2', 'discord.step3']

  async function connect(e: FormEvent) {
    e.preventDefault()
    if (!url.trim()) return
    setBusy(true)
    setError(undefined)
    try {
      const res = await post<DiscordSettings>('/api/discord/connect', { webhookUrl: url.trim() })
      setUrl('')
      toastManager.add({ title: t('discord.connectedToast'), type: 'success' })
      await onConnected(res)
    } catch (err) {
      setError(problem(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card aria-labelledby="discord-title">
      <CardTitle id="discord-title">{t('discord.title')}</CardTitle>
      <CardHint>{t('discord.lead')}</CardHint>
      <ol className="mt-4 flex flex-col gap-1.5 text-[13px]">
        {steps.map((k, i) => (
          <li key={k} className="flex gap-2">
            <span className="w-4 shrink-0 text-muted-foreground tabular-nums">{`${i + 1}.`}</span>
            <span>{t(k)}</span>
          </li>
        ))}
      </ol>
      <form onSubmit={connect} className="mt-4 flex gap-2 max-sm:flex-col" noValidate>
        <InputGroup className="max-sm:h-11 sm:flex-1">
          <InputGroupAddon>
            <LinkIcon aria-hidden="true" />
          </InputGroupAddon>
          <InputGroupInput
            type="url"
            inputMode="url"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder={t('discord.placeholder')}
            aria-label={t('discord.webhookLabel')}
            aria-invalid={error ? true : undefined}
            autoComplete="off"
            spellCheck={false}
          />
        </InputGroup>
        <Button type="submit" loading={busy} disabledReason={url.trim() ? undefined : t('discord.pasteFirst')} className="max-sm:h-11">
          {t('discord.connect')}
        </Button>
      </form>
      {error ? (
        <p className="mt-2 text-xs text-destructive-foreground" role="alert">
          {error.msg}
          {error.hint && <span className="text-muted-foreground"> {error.hint}</span>}
        </p>
      ) : (
        <p className="mt-2 text-xs text-muted-foreground">{t('discord.private')}</p>
      )}
    </Card>
  )
}

function ConnectedCard({ settings: s, onChanged }: { settings: DiscordSettings; onChanged: (s?: DiscordSettings) => Promise<void> }) {
  const [testing, setTesting] = useState(false)
  const [leaving, setLeaving] = useState(false)
  const [result, setResult] = useState<{ ok: true } | { ok: false; problem: Problem }>()
  const d = s.delivery
  const failing = !result && d.failed && d.msg && (!d.sent || new Date(d.failed) > new Date(d.sent))

  async function test() {
    setTesting(true)
    try {
      const res = await post<DiscordSettings>('/api/discord/test')
      setResult({ ok: true })
      await onChanged(res)
    } catch (e) {
      setResult({ ok: false, problem: problem(e) })
    } finally {
      setTesting(false)
    }
  }

  async function disconnect() {
    setLeaving(true)
    try {
      await del('/api/discord')
      toastManager.add({ title: t('discord.disconnectedToast'), type: 'success' })
      await onChanged()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setLeaving(false)
    }
  }

  const since = s.connectedAt ? relativeTime(s.connectedAt) : ''
  return (
    <Card aria-labelledby="discord-title">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-baseline gap-2">
            <CardTitle id="discord-title">{t('discord.title')}</CardTitle>
            <Marker tone="green">{t('discord.connected')}</Marker>
          </div>
          <CardHint>{s.webhookName ? t('discord.webhook', { name: s.webhookName, time: since }) : t('discord.connectedSince', { time: since })}</CardHint>
        </div>
        <div className="flex gap-1.5">
          <Button variant="outline" size="sm" onClick={() => void test()} loading={testing}>
            <SendIcon />
            {t('discord.test')}
          </Button>
          <Button variant="ghost" size="sm" onClick={() => void disconnect()} loading={leaving}>
            {t('discord.disconnect')}
          </Button>
        </div>
      </div>
      {result?.ok && (
        <p className="mt-2.5 text-xs text-success-foreground" role="status">
          {t('discord.testSent')}
        </p>
      )}
      {result && !result.ok && (
        <p className="mt-2.5 text-xs text-destructive-foreground" role="alert">
          {result.problem.msg}
          {result.problem.hint && <span className="text-muted-foreground"> {result.problem.hint}</span>}
        </p>
      )}
      {failing && (
        <p className="mt-2.5 text-xs text-destructive-foreground" role="status">
          {d.msg}
          {d.hint && <span className="text-muted-foreground"> {d.hint}</span>}
        </p>
      )}
    </Card>
  )
}

function AlertsCard({ settings: s, onSave }: { settings: DiscordSettings; onSave: (next: Pick<DiscordSettings, 'alerts' | 'liveStatus'>) => Promise<void> }) {
  const rows = alertRows.filter((r) => r.kinds.every((k) => s.kinds.includes(k)))
  function toggle(kinds: DiscordKind[], on: boolean) {
    const rest = s.alerts.filter((k) => !kinds.includes(k as DiscordKind))
    void onSave({ alerts: on ? [...rest, ...kinds] : rest, liveStatus: s.liveStatus })
  }
  return (
    <Card aria-labelledby="alerts-title">
      <CardTitle id="alerts-title">{t('discord.alerts')}</CardTitle>
      <CardHint>{t('discord.alertsHint')}</CardHint>
      <ul className="mt-2">
        {rows.map((r) => {
          const on = r.kinds.some((k) => s.alerts.includes(k))
          const id = `alert-${r.kinds[0]}`
          return (
            <li key={r.title} className="flex min-h-12 items-center gap-4 border-b border-border py-2 last:border-b-0">
              <label htmlFor={id} className="min-w-0 flex-1">
                <span className="block text-[13px] font-semibold">{t(r.title)}</span>
                {r.hint && <span className="block text-xs text-muted-foreground">{t(r.hint)}</span>}
              </label>
              <Switch id={id} checked={on} onCheckedChange={(v) => toggle(r.kinds, v)} />
            </li>
          )
        })}
      </ul>
    </Card>
  )
}

function LiveStatusCard({ settings: s, onSave }: { settings: DiscordSettings; onSave: (next: Pick<DiscordSettings, 'alerts' | 'liveStatus'>) => Promise<void> }) {
  return (
    <Card aria-labelledby="live-title">
      <CardTitle id="live-title">{t('discord.live')}</CardTitle>
      <CardHint>{t('discord.liveHint')}</CardHint>
      <div className="mt-2 flex min-h-12 items-center gap-4 border-b border-border py-2">
        <label htmlFor="live-status" className="min-w-0 flex-1">
          <span className="block text-[13px] font-semibold">{t('discord.liveSwitch')}</span>
          <span className="block text-xs text-muted-foreground">{t('discord.liveSwitchHint')}</span>
        </label>
        <Switch id="live-status" checked={s.liveStatus} onCheckedChange={(v) => void onSave({ alerts: s.alerts, liveStatus: v })} />
      </div>
      <p className="mt-2.5 text-xs text-muted-foreground">{t('discord.previewLabel')}</p>
      <LivePreview className={cn('mt-2 transition-opacity duration-(--motion-standard) ease-standard', !s.liveStatus && 'opacity-60')} />
    </Card>
  )
}

function stateLine(st: ServerStatus): { dot: string; text: string } {
  const tone = phaseTone(st.phase)
  switch (tone) {
    case 'online': {
      const online = st.players?.online ?? 0
      const parts = [t('discord.state.online'), st.players?.max ? t('discord.count', { online, max: st.players.max }) : String(online)]
      if (st.players?.names?.length) parts.push(st.players.names.join(', '))
      return { dot: 'bg-success', text: parts.join(t('common.dot')) }
    }
    case 'busy':
      return { dot: 'bg-warning', text: t('discord.state.starting') }
    case 'crashed':
      return { dot: 'bg-destructive', text: t('discord.state.crashed') }
    case 'stopped':
    case 'unknown':
      return { dot: 'border-[1.5px] border-muted-foreground/60', text: t('discord.state.stopped') }
    default: {
      const unreachable: never = tone
      return unreachable
    }
  }
}

/** How the live status message looks in the channel: a plain box, built from the servers here. */
function LivePreview({ className }: { className?: string }) {
  const ws = useWorkspace()
  const now = useNow(30_000)
  const servers = ws.servers ?? []
  const joinable = servers.find((x) => phaseTone(x.phase) === 'online') ?? servers[0]
  const clock = formatClock(new Date(now).toISOString())
  return (
    <div className={cn('flex gap-3 rounded-2xl border border-border bg-muted p-3', className)} aria-label={t('discord.previewLabel')} role="img">
      <BrandMark size={28} className="self-start rounded-lg" />
      <div className="min-w-0 flex-1">
        <p className="text-[13px]">
          <span className="font-semibold">{t('discord.previewName')}</span>
          <span className="ml-2 text-[11px] text-muted-foreground">{t('discord.previewTime', { time: clock })}</span>
        </p>
        <div className="mt-1.5 rounded-xl border border-border bg-white px-3 py-2.5">
          {servers.length === 0 && <p className="text-xs text-muted-foreground">{t('discord.noServers')}</p>}
          <ul className="flex flex-col gap-2">
            {servers.slice(0, 25).map((x) => {
              const line = stateLine(x)
              return (
                <li key={x.id} className="flex min-w-0 items-center gap-2 text-[13px]">
                  <span className={cn('inline-block size-2 shrink-0 rounded-full', line.dot)} aria-hidden="true" />
                  <span className="w-[88px] shrink-0 truncate font-semibold">{x.name}</span>
                  <span className="min-w-0 truncate text-muted-foreground">{line.text}</span>
                </li>
              )
            })}
          </ul>
          {joinable && <p className="mt-2.5 text-xs text-muted-foreground">{t('discord.previewFooter', { address: joinAddress(window.location.hostname, joinable.gamePort), time: clock })}</p>}
        </div>
      </div>
    </div>
  )
}
