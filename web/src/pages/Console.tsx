import { useEffect, useRef, useState, type FormEvent } from 'react'
import { ApiError, get, post } from '../api/client'
import type { LogLine, LogsResponse } from '../api/types'
import type { PageProps } from '../App'
import { Banner, Card, Empty } from '../components/ui'
import { formatTime } from '../lib/format'

const MAX_LINES = 2000

export function ConsolePage({ status, statusError }: PageProps) {
  const [lines, setLines] = useState<LogLine[]>([])
  const [feedError, setFeedError] = useState<ApiError>()
  const [truncated, setTruncated] = useState(false)
  const [follow, setFollow] = useState(true)
  const [command, setCommand] = useState('')
  const [reply, setReply] = useState<{ cmd: string; text: string; error?: boolean }>()
  const [busy, setBusy] = useState(false)
  const box = useRef<HTMLDivElement>(null)
  const cursor = useRef({ epoch: '', next: 0 })

  useEffect(() => {
    let stop = false
    async function tick() {
      try {
        const q = new URLSearchParams({ after: String(cursor.current.next), epoch: cursor.current.epoch, limit: '500' })
        const r = await get<LogsResponse>(`/api/server/logs?${q}`)
        if (stop) return
        const reset = r.epoch !== cursor.current.epoch
        cursor.current = { epoch: r.epoch, next: r.next }
        setLines((old) => (reset ? r.lines : [...old, ...r.lines]).slice(-MAX_LINES))
        if (r.truncated) setTruncated(true)
        setFeedError(undefined)
      } catch (e) {
        if (!stop) setFeedError(e as ApiError)
      }
    }
    void tick()
    const id = window.setInterval(() => void tick(), 2000)
    return () => {
      stop = true
      window.clearInterval(id)
    }
  }, [])

  useEffect(() => {
    const el = box.current
    if (el && follow) el.scrollTop = el.scrollHeight
  }, [lines, follow])

  function onScroll() {
    const el = box.current
    if (!el) return
    setFollow(el.scrollHeight - el.scrollTop - el.clientHeight < 40)
  }

  const agentDown = statusError?.code === 'agent_unavailable' || feedError?.code === 'agent_unavailable'
  const online = status?.phase === 'online' && !status.operation
  const disabledReason = agentDown
    ? 'The Playkeeper agent is not running.'
    : !status
      ? 'Checking the server…'
      : status.operation
        ? 'Paused while an operation runs.'
        : status.phase !== 'online'
          ? `The server is ${status.phase.replace(/_/g, ' ')}; start it to send commands.`
          : undefined

  async function send(e: FormEvent) {
    e.preventDefault()
    const cmd = command.trim()
    if (!cmd) return
    setBusy(true)
    try {
      const r = await post<{ output: string }>('/api/server/command', { command: cmd })
      setReply({ cmd, text: r.output || '(no output)' })
      setCommand('')
    } catch (err) {
      setReply({ cmd, text: `${(err as ApiError).message} ${(err as ApiError).hint ?? ''}`, error: true })
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Console</h1>
          <div className="sub">Server output and Minecraft console commands.</div>
        </div>
      </div>
      {agentDown && <Banner tone="bad" title="Console unavailable">The Playkeeper agent is not running, so no output can be shown and commands cannot be sent.</Banner>}
      {feedError && !agentDown && <Banner tone="bad" title={feedError.message}>{feedError.hint}</Banner>}
      <Card
        title="Server output"
        hint={`Most recent ${lines.length} lines (Playkeeper keeps at most ${MAX_LINES}). IP addresses are hidden.${truncated ? ' Older lines were dropped.' : ''}`}
        actions={
          !follow ? (
            <button type="button" className="btn small" onClick={() => setFollow(true)}>
              Jump to latest
            </button>
          ) : undefined
        }
      >
        <div className="console" ref={box} onScroll={onScroll} role="log" aria-live="off" aria-label="Server output" tabIndex={0}>
          {lines.length === 0 ? (
            <Empty title="No output yet">{status?.exists ? 'Start the server to see its output here.' : 'Create a server first.'}</Empty>
          ) : (
            lines.map((l) => (
              <div key={`${l.seq}`} className={`line ${/\b(WARN|WARNING)\b/.test(l.text) ? 'warn' : /\b(ERROR|SEVERE|Exception)\b/.test(l.text) ? 'err' : ''}`}>
                <span className="ts">{formatTime(l.ts)}</span>
                {l.text}
              </div>
            ))
          )}
        </div>
        <form className="console-form" onSubmit={send}>
          <label htmlFor="cmd" className="sr-only">
            Minecraft command
          </label>
          <input id="cmd" type="text" value={command} onChange={(e) => setCommand(e.target.value)} placeholder={online ? 'Minecraft command, e.g. list or say Hello' : ''} maxLength={256} disabled={!online || busy} autoComplete="off" spellCheck={false} aria-describedby="cmd-help" />
          <button className="btn primary" type="submit" disabled={!online || busy || !command.trim()}>
            Send
          </button>
        </form>
        <p className="muted small" id="cmd-help" style={{ marginTop: 6 }}>
          {disabledReason ?? 'Commands go to the Minecraft server console only, never to your VPS shell. Every command is recorded in the audit log. Use the Stop and Restart buttons instead of the stop command.'}
        </p>
        {reply && (
          <div className="console-output" role="status">
            <strong>&gt; {reply.cmd}</strong>
            {'\n'}
            {reply.text}
          </div>
        )}
      </Card>
    </>
  )
}
