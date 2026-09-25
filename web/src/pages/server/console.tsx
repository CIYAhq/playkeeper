import { useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { ArrowUpRightIcon, CloudRainIcon, DownloadIcon, MessageSquareIcon, SaveIcon, SearchIcon, SendIcon, SunIcon, UsersIcon } from 'lucide-react'
import { ApiError, get, post } from '@/api/client'
import type { LogLine, LogsResponse, ServerStatus } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { Card, CardTitle } from '@/components/app/bits'
import { Segmented, useIsPhone } from '@/components/app/controls'
import { lineWidth, LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { t, type MessageKey } from '@/i18n'
import { behindSeconds, parseLine, type LineKind } from '@/lib/console'
import { formatClock, formatTime } from '@/lib/format'
import { whyNot } from '@/lib/phase'
import { cn } from '@/lib/utils'

type Filter = 'all' | 'chat' | 'players' | 'problems'

interface Sent {
  ts: string
  command: string
  reply?: string
  error?: boolean
}

interface Row {
  key: string
  ts: string
  text: string
  level?: string
  kind: LineKind | 'sent' | 'reply'
}

const keep = 2000

const quick: { command: string; key: MessageKey; icon: ReactNode; fillOnly?: boolean }[] = [
  { command: 'list', key: 'console.quick.list', icon: <UsersIcon /> },
  { command: 'time set day', key: 'console.quick.day', icon: <SunIcon /> },
  { command: 'weather clear', key: 'console.quick.rain', icon: <CloudRainIcon /> },
  { command: 'save-all', key: 'console.quick.save', icon: <SaveIcon /> },
  { command: 'say ', key: 'console.quick.say', icon: <MessageSquareIcon />, fillOnly: true },
]

function useLog(server: ServerStatus) {
  const [lines, setLines] = useState<LogLine[]>([])
  const [loaded, setLoaded] = useState(false)
  const [truncated, setTruncated] = useState(false)
  const cursor = useRef<{ epoch: string; next: number }>({ epoch: '', next: 0 })
  useEffect(() => {
    let stopped = false
    cursor.current = { epoch: '', next: 0 }
    setLines([])
    setLoaded(false)
    async function poll() {
      try {
        const c = cursor.current
        const r = await get<LogsResponse>(serverApi(server.id, `/logs?epoch=${encodeURIComponent(c.epoch)}&after=${c.next}&limit=500`))
        if (stopped) return
        const reset = r.epoch !== c.epoch
        cursor.current = { epoch: r.epoch, next: r.next }
        if (r.truncated && !reset) setTruncated(true)
        setLines((prev) => (reset ? r.lines : [...prev, ...r.lines]).slice(-keep))
      } catch {
        // The next poll tries again; the agent being away shows elsewhere.
      }
      if (!stopped) setLoaded(true)
    }
    void poll()
    const id = window.setInterval(() => document.visibilityState === 'visible' && void poll(), 2000)
    return () => {
      stopped = true
      window.clearInterval(id)
    }
  }, [server.id])
  return { lines, loaded, truncated }
}

export function ConsolePage({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const { lines, loaded, truncated } = useLog(s)
  const [filter, setFilter] = useState<Filter>('all')
  const [query, setQuery] = useState('')
  const [follow, setFollow] = useState(true)
  const [sent, setSent] = useState<Sent[]>([])
  const [command, setCommand] = useState('')
  const [sending, setSending] = useState(false)
  const input = useRef<HTMLInputElement>(null)
  const scroller = useRef<HTMLDivElement>(null)

  const rows = useMemo<Row[]>(() => {
    const out: Row[] = lines.map((l) => {
      const p = parseLine(l.text)
      return { key: `l${l.seq}`, ts: l.ts, text: p.text, level: p.level, kind: p.kind }
    })
    sent.forEach((c, i) => {
      out.push({ key: `s${i}`, ts: c.ts, text: `> ${c.command}`, kind: 'sent' })
      if (c.reply !== undefined) out.push({ key: `r${i}`, ts: c.ts, text: c.reply || t('console.noReply'), kind: 'reply', level: c.error ? 'ERROR' : undefined })
    })
    return out.sort((a, b) => a.ts.localeCompare(b.ts))
  }, [lines, sent])

  const shown = rows.filter((r) => {
    if (filter === 'chat' && r.kind !== 'chat') return false
    if (filter === 'players' && r.kind !== 'players') return false
    if (filter === 'problems' && r.kind !== 'problem') return false
    return !query || r.text.toLowerCase().includes(query.toLowerCase())
  })
  const warning = [...rows].reverse().find((r) => behindSeconds(r.text) !== undefined)
  const lastReply = [...sent].reverse().find((c) => c.reply !== undefined)

  useEffect(() => {
    const el = scroller.current
    if (follow && el) el.scrollTop = el.scrollHeight
  }, [shown.length, follow])

  async function send(e?: FormEvent, text = command) {
    e?.preventDefault()
    const cmd = text.trim()
    if (!cmd || sending) return
    const entry: Sent = { ts: new Date().toISOString(), command: cmd.replace(/^\//, '') }
    setSent((p) => [...p, entry])
    setCommand('')
    setSending(true)
    try {
      const r = await post<{ output: string }>(serverApi(s.id, '/command'), { command: cmd })
      setSent((p) => p.map((x) => (x === entry ? { ...x, reply: r.output } : x)))
    } catch (err) {
      setSent((p) => p.map((x) => (x === entry ? { ...x, reply: err instanceof ApiError ? err.message : errorText(err), error: true } : x)))
    } finally {
      setSending(false)
      input.current?.focus()
    }
  }

  function fill(text: string) {
    setCommand(text)
    window.requestAnimationFrame(() => {
      const el = input.current
      if (!el) return
      el.focus()
      el.setSelectionRange(text.length, text.length)
    })
  }

  function download() {
    const text = lines.map((l) => `${l.ts} ${l.text}`).join('\n')
    const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }))
    const a = document.createElement('a')
    a.href = url
    a.download = `${s.slug}-console.log`
    a.click()
    window.setTimeout(() => URL.revokeObjectURL(url), 1000)
  }

  const disabledReason = whyNot(s, 'command', ws.stale)
  const sendBlocked = disabledReason ?? (command.trim() ? undefined : t('console.typeFirst'))
  const filters = [
    { value: 'all' as const, label: phone ? t('console.filter.allShort') : t('console.filter.all') },
    { value: 'chat' as const, label: t('console.filter.chat') },
    { value: 'players' as const, label: t('console.filter.players') },
    { value: 'problems' as const, label: t('console.filter.problems') },
  ]

  const log = (
    <div
      ref={scroller}
      onScroll={(e) => {
        const el = e.currentTarget
        if (el.scrollHeight - el.scrollTop - el.clientHeight > 40 && follow) setFollow(false)
      }}
      className={cn('min-h-0 flex-1 overflow-y-auto rounded-3xl bg-console px-3 py-3 font-mono text-[13px] leading-5 text-[#e8e8e0]', phone ? 'h-[calc(100dvh-330px)] min-h-[260px]' : 'h-[calc(100dvh-330px)] min-h-[380px]')}
      role="log"
      aria-live={follow ? 'polite' : 'off'}
      aria-label={t('console.log')}
      tabIndex={0}
    >
      {truncated && <p className="px-2 pb-2 text-xs text-[#a3a89c]">{t('console.truncated', { count: keep })}</p>}
      {!loaded ? (
        <div>
          <LoadingLabel />
          {Array.from({ length: 8 }, (_, i) => (
            <div key={i} className="flex h-6 items-center gap-3 px-2">
              <Skeleton className="h-3 w-[4.5rem] shrink-0 bg-white/10 max-sm:w-11" />
              <Skeleton className={cn('h-3 bg-white/10', lineWidth(i))} />
            </div>
          ))}
        </div>
      ) : shown.length === 0 ? (
        <p className="px-2 py-1 text-[#a3a89c]">{rows.length ? t('console.noMatch') : t('console.empty', { server: s.name })}</p>
      ) : (
        shown.map((r) => (
          <div key={r.key} className={cn('flex gap-3 rounded-md px-2 py-0.5', r.level === 'WARN' && 'bg-[#f5b94a]/10', (r.level === 'ERROR' || r.level === 'FATAL') && 'bg-[#f87171]/10')}>
            <span className="w-[4.5rem] shrink-0 text-[#a3a89c] tabular-nums max-sm:w-11">{phone ? formatClock(r.ts) : formatTime(r.ts)}</span>
            {!phone && <span className={cn('w-12 shrink-0 text-xs leading-5 font-semibold', r.level === 'WARN' ? 'text-[#f5b94a]' : 'text-[#f87171]')}>{r.level === 'WARN' ? t('console.warnTag') : r.level === 'ERROR' || r.level === 'FATAL' ? t('console.errorTag') : ''}</span>}
            <span className={cn('min-w-0 break-words whitespace-pre-wrap', r.kind === 'sent' && 'text-[#93b4f5]', r.level === 'WARN' && 'text-[#f5b94a]', (r.level === 'ERROR' || r.level === 'FATAL') && 'text-[#f87171]')}>{r.text}</span>
          </div>
        ))
      )}
      {warning && filter !== 'chat' && filter !== 'players' && (
        <div className="mt-3 max-w-[560px] px-2 font-sans sm:pl-[8.75rem]">
          <p className="text-xs font-semibold text-[#f5b94a]">{t('console.warnTitle', { time: formatClock(warning.ts) })}</p>
          <p className="mt-0.5 text-xs text-[#b8bdb2]">{t('console.warnBody', { count: Math.max(1, Math.round(behindSeconds(warning.text) ?? 1)) })}</p>
        </div>
      )}
    </div>
  )

  const form = (
    <form onSubmit={send} className="flex items-center gap-2">
      {!phone && (
        <span className="w-4 font-mono text-sm font-semibold text-primary" aria-hidden="true">
          {'>'}
        </span>
      )}
      <InputGroup className="flex-1">
        <InputGroupInput
          ref={input}
          value={command}
          onChange={(e) => setCommand(e.target.value)}
          placeholder={disabledReason ?? t('console.placeholder')}
          aria-label={t('console.input')}
          disabled={!!disabledReason}
          maxLength={256}
          spellCheck={false}
          autoComplete="off"
          className="font-mono"
        />
      </InputGroup>
      {phone ? (
        <Button type="submit" size="icon-xl" aria-label={t('console.send')} disabledReason={sendBlocked} loading={sending}>
          <SendIcon />
        </Button>
      ) : (
        <Button type="submit" disabledReason={sendBlocked} loading={sending}>
          <SendIcon />
          {t('console.send')}
        </Button>
      )}
    </form>
  )

  if (phone) {
    return (
      <div className="flex flex-col gap-3">
        <Segmented value={filter} onChange={setFilter} options={filters} label={t('console.filter')} className="self-start" />
        {log}
        <div className="-mx-4 flex gap-2 overflow-x-auto px-4 pb-1">
          {quick.map((q) => (
            <button key={q.command} type="button" disabled={!!disabledReason} title={disabledReason} onClick={() => fill(q.command)} className="shrink-0 rounded-full border border-border bg-white px-3.5 py-2 text-sm font-medium disabled:cursor-not-allowed disabled:opacity-60">
              {q.fillOnly ? `${q.command.trim()} …` : q.command}
            </button>
          ))}
        </div>
        {form}
      </div>
    )
  }

  return (
    <div className="grid flex-1 gap-4 xl:grid-cols-[1fr_280px]">
      <div className="flex min-w-0 flex-col gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <Segmented value={filter} onChange={setFilter} options={filters} label={t('console.filter')} />
          <InputGroup className="w-56">
            <InputGroupAddon>
              <SearchIcon aria-hidden="true" />
            </InputGroupAddon>
            <InputGroupInput value={query} onChange={(e) => setQuery(e.target.value)} placeholder={t('console.search')} aria-label={t('console.search')} type="search" />
          </InputGroup>
          <label className="ml-auto flex items-center gap-2 text-[13px] font-medium">
            <Switch checked={follow} onCheckedChange={setFollow} />
            {t('console.follow')}
          </label>
          <Button variant="outline" size="sm" onClick={download} disabledReason={lines.length ? undefined : t('console.nothingYet')}>
            <DownloadIcon />
            {t('console.downloadLog')}
          </Button>
        </div>
        {log}
        {form}
        <p className="text-xs text-muted-foreground">{t('console.help')}</p>
      </div>
      <Card className="self-start">
        <CardTitle>{t('console.quick')}</CardTitle>
        <ul className="mt-3 flex flex-col gap-2">
          {quick.map((q) => (
            <li key={q.command}>
              <button
                type="button"
                disabled={!!disabledReason}
                title={disabledReason}
                onClick={() => fill(q.command)}
                className="flex w-full items-center gap-3 rounded-xl border border-border px-3 py-2 text-left outline-none not-disabled:hover:border-input not-disabled:hover:bg-accent/40 focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60 [&>svg]:size-4 [&>svg]:shrink-0 [&>svg]:text-muted-foreground"
              >
                {q.icon}
                <span className="min-w-0 flex-1">
                  <span className="block text-[13px] font-medium">{t(q.key)}</span>
                  <span className="block font-mono text-xs text-muted-foreground">{q.fillOnly ? `${q.command.trim()} …` : q.command}</span>
                </span>
                <ArrowUpRightIcon />
              </button>
            </li>
          ))}
        </ul>
        {lastReply && (
          <div className="mt-4 border-t border-border pt-3">
            <div className="text-xs font-semibold">{t('console.lastReply')}</div>
            <p className={cn('mt-1 text-[13px] break-words', lastReply.error ? 'text-destructive-foreground' : 'text-foreground')}>{lastReply.reply || t('console.noReply')}</p>
          </div>
        )}
      </Card>
    </div>
  )
}
