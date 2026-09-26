import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { ArrowDownIcon, ArrowUpRightIcon, CloudRainIcon, DownloadIcon, MessageSquareIcon, SaveIcon, SearchIcon, SendIcon, SunIcon, UsersIcon } from 'lucide-react'
import { ApiError, get, post } from '@/api/client'
import type { LogLine, LogsResponse, ServerStatus } from '@/api/types'
import { errorText, serverApi, useServerMachine } from '@/api/workspace'
import { Card, CardTitle } from '@/components/app/bits'
import { Segmented, useIsPhone } from '@/components/app/controls'
import { lineWidth, LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { t, type MessageKey } from '@/i18n'
import { behindSeconds, followAfterScroll, isAtBottom, keepTail, mergeByTime, parseLine, type LineKind } from '@/lib/console'
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

/** A line of server output, parsed once when it arrives. */
interface OutputRow extends Row {
  /** The line as the server printed it, for the downloaded log. */
  raw: string
}

const keep = 2000

const quick: { command: string; key: MessageKey; icon: ReactNode; fillOnly?: boolean }[] = [
  { command: 'list', key: 'console.quick.list', icon: <UsersIcon /> },
  { command: 'time set day', key: 'console.quick.day', icon: <SunIcon /> },
  { command: 'weather clear', key: 'console.quick.rain', icon: <CloudRainIcon /> },
  { command: 'save-all', key: 'console.quick.save', icon: <SaveIcon /> },
  { command: 'say ', key: 'console.quick.say', icon: <MessageSquareIcon />, fillOnly: true },
]

function toRow(l: LogLine): OutputRow {
  const p = parseLine(l.text)
  return { key: `l${l.seq}`, ts: l.ts, text: p.text, raw: l.text, level: p.level, kind: p.kind }
}

function useLog(server: ServerStatus) {
  const [rows, setRows] = useState<OutputRow[]>([])
  const [loaded, setLoaded] = useState(false)
  const [truncated, setTruncated] = useState(false)
  const cursor = useRef<{ epoch: string; next: number }>({ epoch: '', next: 0 })
  useEffect(() => {
    let stopped = false
    // One read at a time: two in flight would both append the same lines.
    let reading = false
    cursor.current = { epoch: '', next: 0 }
    setRows([])
    setLoaded(false)
    async function poll() {
      if (reading) return
      reading = true
      try {
        const c = cursor.current
        const r = await get<LogsResponse>(serverApi(server.id, `/logs?epoch=${encodeURIComponent(c.epoch)}&after=${c.next}&limit=500`))
        if (stopped) return
        const reset = r.epoch !== c.epoch
        cursor.current = { epoch: r.epoch, next: r.next }
        if (r.truncated && !reset) setTruncated(true)
        setRows((prev) => keepTail(prev, r.lines.map(toRow), reset, keep))
      } catch {
        // The next poll tries again; the agent being away shows elsewhere.
      } finally {
        reading = false
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
  return { rows, loaded, truncated }
}

/**
 * Keeps the log on its newest line while the reader is at the bottom, and on
 * the line they're reading once they scroll up, as new lines arrive and the
 * oldest drop off. Safari has no native scroll anchoring, so the log turns it
 * off and keeps the reader's place itself, the same way in every browser.
 * `layout` changes when the log is rendered into a different element.
 */
function useFollow(layout: unknown) {
  const scroller = useRef<HTMLDivElement>(null)
  const list = useRef<HTMLDivElement>(null)
  const [following, setFollowing] = useState(true)
  const state = useRef({ following: true, paused: false, jump: 0, anchor: undefined as { row: Element; top: number } | undefined })

  const follow = useCallback((on: boolean) => {
    state.current.following = on
    setFollowing(on)
  }, [])

  const remember = useCallback(() => {
    const el = scroller.current
    const rows = list.current?.children
    if (!el || !rows?.length) return
    const top = el.getBoundingClientRect().top
    let lo = 0
    let hi = rows.length - 1
    while (lo < hi) {
      const mid = (lo + hi) >> 1
      if ((rows[mid] as Element).getBoundingClientRect().bottom <= top) lo = mid + 1
      else hi = mid
    }
    const row = rows[lo] as Element
    state.current.anchor = { row, top: row.getBoundingClientRect().top - top }
  }, [])

  const settle = useCallback(() => {
    const el = scroller.current
    if (!el) return
    const s = state.current
    if (s.following) {
      if (s.jump) el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
      else el.scrollTop = el.scrollHeight
      return
    }
    const a = s.anchor
    if (!a) return
    if (!a.row.isConnected) {
      el.scrollTop = 0
      return
    }
    const moved = a.row.getBoundingClientRect().top - el.getBoundingClientRect().top - a.top
    if (Math.abs(moved) >= 1) el.scrollTop += moved
  }, [])

  const stopJump = useCallback(() => {
    window.clearTimeout(state.current.jump)
    state.current.jump = 0
  }, [])

  const endJump = useCallback(() => {
    stopJump()
    const el = scroller.current
    if (el && state.current.following && !isAtBottom(el)) el.scrollTop = el.scrollHeight
  }, [stopJump])

  const onScroll = useCallback(() => {
    const el = scroller.current
    if (!el) return
    const s = state.current
    const bottom = isAtBottom(el)
    if (bottom && s.jump) stopJump()
    if (!bottom) s.paused = false
    const on = !s.paused && followAfterScroll(s.following, bottom, s.jump !== 0)
    if (on !== s.following) follow(on)
    if (!on) remember()
  }, [follow, remember, stopJump])

  const jump = useCallback(() => {
    const el = scroller.current
    if (!el) return
    stopJump()
    state.current.paused = false
    follow(true)
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
      el.scrollTop = el.scrollHeight
      return
    }
    state.current.jump = window.setTimeout(endJump, 1000)
    el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
  }, [endJump, follow, stopJump])

  const pause = useCallback(() => {
    stopJump()
    state.current.paused = true
    follow(false)
    remember()
  }, [follow, remember, stopJump])

  const resume = useCallback(() => {
    stopJump()
    state.current.paused = false
    follow(true)
  }, [follow, stopJump])

  useEffect(() => {
    const el = scroller.current
    const rows = list.current
    if (!el || !rows) return
    const observer = new ResizeObserver(() => settle())
    observer.observe(el)
    observer.observe(rows)
    return () => observer.disconnect()
  }, [settle, layout])

  return { scroller, list, following, onScroll, stopJump, jump, pause, resume, settle }
}

const Line = memo(function Line({ row: r, phone }: { row: Row; phone: boolean }) {
  const warn = r.level === 'WARN'
  const error = r.level === 'ERROR' || r.level === 'FATAL'
  return (
    <div className={cn('flex gap-3 rounded-md px-2 py-0.5', warn && 'bg-[#f5b94a]/10', error && 'bg-[#f87171]/10')}>
      <span className="w-[4.5rem] shrink-0 text-[#a3a89c] tabular-nums max-sm:w-11">{phone ? formatClock(r.ts) : formatTime(r.ts)}</span>
      {!phone && <span className={cn('w-12 shrink-0 text-xs leading-5 font-semibold', warn ? 'text-[#f5b94a]' : 'text-[#f87171]')}>{warn ? t('console.warnTag') : error ? t('console.errorTag') : ''}</span>}
      <span className={cn('min-w-0 break-words whitespace-pre-wrap', r.kind === 'sent' && 'text-[#93b4f5]', warn && 'text-[#f5b94a]', error && 'text-[#f87171]')}>{r.text}</span>
    </div>
  )
})

function QuickChips({ reason, onPick, className }: { reason: string | undefined; onPick: (command: string) => void; className?: string }) {
  return (
    <div className={cn('flex gap-2 overflow-x-auto pb-1', className)}>
      {quick.map((q) => (
        <button key={q.command} type="button" disabled={!!reason} title={reason} onClick={() => onPick(q.command)} className="shrink-0 rounded-full border border-border bg-white px-3.5 py-2 text-sm font-medium disabled:cursor-not-allowed disabled:opacity-60">
          {q.fillOnly ? `${q.command.trim()} …` : q.command}
        </button>
      ))}
    </div>
  )
}

export function ConsolePage({ server }: { server: ServerStatus }) {
  return <Console key={server.id} server={server} />
}

function Console({ server: s }: { server: ServerStatus }) {
  const { offline } = useServerMachine(s)
  const phone = useIsPhone()
  const { rows, loaded, truncated } = useLog(s)
  const [filter, setFilter] = useState<Filter>('all')
  const [query, setQuery] = useState('')
  const [sent, setSent] = useState<Sent[]>([])
  const [command, setCommand] = useState('')
  const [sending, setSending] = useState(false)
  const input = useRef<HTMLInputElement>(null)
  const { scroller, list, following, onScroll, stopJump, jump, pause, resume, settle } = useFollow(phone)

  const sentRows = useMemo<Row[]>(
    () =>
      sent.flatMap((c, i) => {
        const out: Row[] = [{ key: `s${i}`, ts: c.ts, text: `> ${c.command}`, kind: 'sent' }]
        if (c.reply !== undefined) out.push({ key: `r${i}`, ts: c.ts, text: c.reply || t('console.noReply'), kind: 'reply', level: c.error ? 'ERROR' : undefined })
        return out
      }),
    [sent],
  )
  const all = useMemo(() => mergeByTime<Row>(rows, sentRows), [rows, sentRows])
  const shown = useMemo(() => {
    if (filter === 'all' && !query) return all
    const q = query.toLowerCase()
    return all.filter((r) => {
      if (filter === 'chat' && r.kind !== 'chat') return false
      if (filter === 'players' && r.kind !== 'players') return false
      if (filter === 'problems' && r.kind !== 'problem') return false
      return !q || r.text.toLowerCase().includes(q)
    })
  }, [all, filter, query])
  const warning = useMemo(() => all.findLast((r) => behindSeconds(r.text) !== undefined), [all])
  const lastReply = sent.findLast((c) => c.reply !== undefined)

  useLayoutEffect(() => settle(), [settle, shown, truncated, warning])

  function show(f: Filter) {
    setFilter(f)
    resume()
  }

  function search(q: string) {
    setQuery(q)
    resume()
  }

  async function send(e?: FormEvent, text = command) {
    e?.preventDefault()
    const cmd = text.trim()
    if (!cmd || sending) return
    const entry: Sent = { ts: new Date().toISOString(), command: cmd.replace(/^\//, '') }
    setSent((p) => [...p, entry])
    setCommand('')
    setSending(true)
    resume()
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
    const text = rows.map((r) => `${r.ts} ${r.raw}`).join('\n')
    const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }))
    const a = document.createElement('a')
    a.href = url
    a.download = `${s.slug}-console.log`
    a.click()
    window.setTimeout(() => URL.revokeObjectURL(url), 1000)
  }

  const disabledReason = whyNot(s, 'command', offline)
  const sendBlocked = disabledReason ?? (command.trim() ? undefined : t('console.typeFirst'))
  const filters = [
    { value: 'all' as const, label: phone ? t('console.filter.allShort') : t('console.filter.all') },
    { value: 'chat' as const, label: t('console.filter.chat') },
    { value: 'players' as const, label: t('console.filter.players') },
    { value: 'problems' as const, label: t('console.filter.problems') },
  ]

  const log = (
    <div className={cn('relative flex-1', phone ? 'min-h-40' : 'min-h-48')}>
      <div
        ref={scroller}
        onScroll={onScroll}
        onWheel={stopJump}
        onPointerDown={stopJump}
        className="absolute inset-0 overflow-y-auto overscroll-contain rounded-3xl bg-console px-3 py-3 font-mono text-[13px] leading-5 text-[#e8e8e0] [overflow-anchor:none]"
        role="log"
        aria-live={following ? 'polite' : 'off'}
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
        ) : (
          shown.length === 0 && <p className="px-2 py-1 text-[#a3a89c]">{all.length ? t('console.noMatch') : t('console.empty', { server: s.name })}</p>
        )}
        <div ref={list}>
          {shown.map((r) => (
            <Line key={r.key} row={r} phone={phone} />
          ))}
        </div>
        {warning && filter !== 'chat' && filter !== 'players' && (
          <div className="mt-3 max-w-[560px] px-2 font-sans sm:pl-[8.75rem]">
            <p className="text-xs font-semibold text-[#f5b94a]">{t('console.warnTitle', { time: formatClock(warning.ts) })}</p>
            <p className="mt-0.5 text-xs text-[#b8bdb2]">{t('console.warnBody', { count: Math.max(1, Math.round(behindSeconds(warning.text) ?? 1)) })}</p>
          </div>
        )}
      </div>
      {!following && (
        <Button
          size="sm"
          variant="secondary"
          onClick={() => {
            jump()
            scroller.current?.focus({ preventScroll: true })
          }}
          className="absolute bottom-3 left-1/2 -translate-x-1/2 rounded-full shadow-popup transition-[opacity,translate] duration-(--motion-fast) ease-enter starting:translate-y-1 starting:opacity-0"
        >
          <ArrowDownIcon />
          {t('console.jumpToLatest')}
        </Button>
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
      <div className="flex flex-1 flex-col gap-3">
        <Segmented value={filter} onChange={show} options={filters} label={t('console.filter')} className="self-start" />
        {log}
        <QuickChips reason={disabledReason} onPick={fill} className="-mx-4 px-4" />
        {form}
      </div>
    )
  }

  return (
    <div className="grid flex-1 grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1fr)_280px]">
      <div className="flex min-w-0 flex-col gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <Segmented value={filter} onChange={show} options={filters} label={t('console.filter')} />
          <InputGroup className="w-56">
            <InputGroupAddon>
              <SearchIcon aria-hidden="true" />
            </InputGroupAddon>
            <InputGroupInput value={query} onChange={(e) => search(e.target.value)} placeholder={t('console.search')} aria-label={t('console.search')} type="search" />
          </InputGroup>
          <label className="ml-auto flex items-center gap-2 text-[13px] font-medium">
            <Switch checked={following} onCheckedChange={(on) => (on ? jump() : pause())} />
            {t('console.follow')}
          </label>
          <Button variant="outline" size="sm" onClick={download} disabledReason={rows.length ? undefined : t('console.nothingYet')}>
            <DownloadIcon />
            {t('console.downloadLog')}
          </Button>
        </div>
        {log}
        <QuickChips reason={disabledReason} onPick={fill} className="xl:hidden" />
        {form}
        <p className="text-xs text-muted-foreground">{t('console.help')}</p>
      </div>
      <Card className="max-xl:hidden">
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
