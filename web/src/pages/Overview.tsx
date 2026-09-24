import { useState } from 'react'
import { ApiError, get, post } from '../api/client'
import type { MetricsResponse } from '../api/types'
import type { PageProps } from '../App'
import { Chart, ChartLegend } from '../components/Chart'
import { Banner, Card, CopyButton, Empty, Segmented, Spinner, Stat, StatusPill } from '../components/ui'
import { formatBytes, formatDateTime, formatDuration, formatMs, formatPercent, joinAddress, relativeTime } from '../lib/format'
import { controls, phaseLabel } from '../lib/phase'
import { navigate } from '../lib/router'
import { usePoll } from '../lib/usePoll'

type Range = '1h' | '24h' | '7d'
const rangeText: Record<Range, string> = { '1h': 'the last hour', '24h': 'the last 24 hours', '7d': 'the last 7 days' }
const bucketText: Record<Range, string> = { '1h': '1-minute', '24h': '10-minute', '7d': '1-hour' }

export function Overview({ status, statusError, refresh }: PageProps) {
  const [chosen, setRange] = useState<Range>()
  // Until the user picks a range, new installs show the last hour.
  const range: Range = chosen ?? (status?.collectingSince && Date.now() - new Date(status.collectingSince).getTime() < 3 * 3600_000 ? '1h' : '24h')
  const metrics = usePoll(() => get<MetricsResponse>(`/api/metrics?range=${range}`), 30_000, range)
  const [actionError, setActionError] = useState<ApiError>()
  const [pending, setPending] = useState(false)

  if (statusError?.code === 'agent_unavailable') {
    return (
      <>
        <Head />
        <Card title="Server">
          <Empty title="No live status">The agent is not running, so Playkeeper shows nothing rather than stale numbers.</Empty>
          <ServerControls canStart={false} canStop={false} canRestart={false} pending={false} onAct={() => undefined} disabledReason="Controls are disabled until the agent is back." />
        </Card>
      </>
    )
  }
  if (!status) return statusError ? <Banner tone="bad" title={statusError.message} /> : <Spinner />

  const c = controls(status)
  const address = joinAddress(window.location.hostname, status.gamePort)
  const online = status.phase === 'online'
  const res = status.resources
  const uptime = status.startedAt && online ? (Date.now() - new Date(status.startedAt).getTime()) / 1000 : undefined

  async function act(verb: 'start' | 'stop' | 'restart') {
    setPending(true)
    setActionError(undefined)
    try {
      await post(`/api/server/${verb}`)
    } catch (e) {
      setActionError(e as ApiError)
    } finally {
      setPending(false)
      await refresh()
    }
  }

  const divergence = status.desired === 'running' && (status.phase === 'stopped' || status.phase === 'crashed')
  return (
    <>
      <Head />
      {status.operation && (
        <Banner tone="busy" title={`${opText(status.operation.kind)}: ${phaseText(status.operation.phase)}`}>
          Started {relativeTime(status.operation.startedAt)} by {status.operation.actor}. Controls are paused until it finishes.
        </Banner>
      )}
      {status.lastError && !status.operation && (
        <Banner tone="bad" title={status.lastError}>
          {status.lastErrorHint}
        </Banner>
      )}
      {status.diskWarning && (
        <Banner tone={status.diskWarning.status === 'fail' ? 'bad' : 'warn'} title={`Low disk space: ${status.diskWarning.detail}`}>
          {status.diskWarning.fix}
        </Banner>
      )}
      {divergence && !status.operation && !status.lastError && (
        <Banner tone="warn" title="The server should be running but is not">
          Playkeeper expected it to be running ({status.phase}). Press Start to try again, or check the Console.
        </Banner>
      )}
      {status.pendingRestart && (
        <Banner tone="info" title="Settings changed">
          Restart the server to apply them.
        </Banner>
      )}
      {actionError && (
        <Banner tone="bad" title={actionError.message}>
          {actionError.hint}
        </Banner>
      )}
      <section className="hero" aria-label="Server status">
        <div className="card stack">
          <div className="actions" style={{ justifyContent: 'space-between' }}>
            <div className="actions">
              <StatusPill phase={status.phase} />
              {status.config && <span className="muted small">Paper {status.config.minecraftVersion} · build {status.config.paperBuild}</span>}
            </div>
            <span className="muted small">Desired: {status.desired}</span>
          </div>
          <div>
            <div className="muted small">Join address</div>
            <div className="copy">
              <span className="join-address">{address}</span>
              <CopyButton text={address} label="Copy address" />
            </div>
          </div>
          <p className="muted small" style={{ margin: 0 }}>
            {online && status.reachable
              ? `Joinable: the game port ${status.gamePort} answered a Minecraft status check on this server ${relativeTime(status.reachableAt)}. Internet reachability depends on your provider's firewall and is not checked.`
              : online
                ? 'The server says it is ready but the game port did not answer the last status check yet.'
                : `Not joinable: the server is ${phaseLabel[status.phase].toLowerCase()}.`}
          </p>
          <ServerControls canStart={c.canStart} canStop={c.canStop} canRestart={c.canRestart} pending={pending} onAct={act} disabledReason={c.busy ? 'Paused while an operation runs.' : status.phase === 'docker_unavailable' ? 'Docker is not responding.' : undefined} />
        </div>
        <div className="card stack">
          <h2>Players online</h2>
          {online && status.players ? (
            <>
              <div className="stat">
                <span className="value">
                  {status.players.online} <span className="muted small">of {status.players.max}</span>
                </span>
              </div>
              {status.players.names.length > 0 ? (
                <ul className="plain">
                  {status.players.names.map((n) => (
                    <li key={n}>{n}</li>
                  ))}
                </ul>
              ) : (
                <p className="muted small">Nobody is playing right now.</p>
              )}
              <p className="muted small" style={{ margin: 0 }}>
                Source: {status.players.source}, {relativeTime(status.players.at)}.
              </p>
            </>
          ) : (
            <Empty title="No player data">{online ? 'Waiting for the first sample.' : ['stopped', 'crashed', 'not_created', 'docker_unavailable'].includes(status.phase) ? 'The server is not running.' : 'Shown once the server is online.'}</Empty>
          )}
        </div>
      </section>
      <section className="grid grid-stats" aria-label="Server numbers">
        <Stat label="Uptime" value={uptime !== undefined ? formatDuration(uptime) : '—'} meta={status.startedAt && online ? `since ${formatDateTime(status.startedAt)}` : 'not running'} />
        <Stat label="CPU" value={res?.cpuPercent !== undefined ? formatPercent(res.cpuPercent) : '—'} meta="Docker stats, 15 s average; 100% = one core" />
        <Stat label="Memory" value={res?.memBytes !== undefined ? formatBytes(res.memBytes) : '—'} meta={status.config ? `limit ${formatBytes((res?.memLimitBytes ?? status.config.memoryMB * 1048576))} · Java heap ${status.config.heapMB} MB` : undefined} />
        <Stat label="Disk free" value={formatBytes(res?.diskFreeBytes)} meta={res?.diskTotalBytes ? `of ${formatBytes(res.diskTotalBytes)}` : undefined} />
        <Stat
          label="Last backup"
          value={status.lastBackup ? relativeTime(status.lastBackup.createdAt) : 'None yet'}
          meta={
            status.lastBackup ? (
              <>
                Verified · on this server only · downtime {formatMs(status.lastBackup.downtimeMs)}{' '}
                <a href="/world" onClick={(e) => { e.preventDefault(); navigate('/world') }}>World</a>
              </>
            ) : (
              <a href="/world" onClick={(e) => { e.preventDefault(); navigate('/world') }}>Make a backup</a>
            )
          }
        />
      </section>
      <div className="page-head">
        <h2>Activity and performance</h2>
        <Segmented label="Chart time range" value={range} onChange={setRange} options={[{ value: '1h', label: '1 hour' }, { value: '24h', label: '24 hours' }, { value: '7d', label: '7 days' }]} />
      </div>
      <div className="grid grid-2">
        <Card title={`Players over ${rangeText[range]}`} hint={`Most players online in each ${bucketText[range]} period`}>
          {metrics.data ? (
            <>
              <Chart buckets={metrics.data.buckets} bucketSeconds={metrics.data.bucketSeconds} value={(b) => b.playersMax} format={(v) => String(Math.round(v))} label={`Players online over ${rangeText[range]}`} integer />
              <ChartLegend />
              <p className="provenance">
                Source: {metrics.data.source}; sampled every {metrics.data.sampleIntervalSeconds} s. {metrics.data.collectingSince ? `Collecting since ${formatDateTime(metrics.data.collectingSince)}.` : 'No samples yet.'}
              </p>
            </>
          ) : metrics.error ? (
            <Empty title="Chart unavailable">{metrics.error.message}</Empty>
          ) : (
            <Spinner />
          )}
        </Card>
        <Card title={`Server CPU over ${rangeText[range]}`} hint={`Average per ${bucketText[range]} period (Docker stats; 100% = one core)`}>
          {metrics.data ? (
            <>
              <Chart buckets={metrics.data.buckets} bucketSeconds={metrics.data.bucketSeconds} value={(b) => b.cpuAvg} format={(v) => `${Math.round(v)}%`} label={`Server CPU over ${rangeText[range]}`} />
              <ChartLegend />
            </>
          ) : (
            <Spinner />
          )}
        </Card>
      </div>
    </>
  )
}

function ServerControls({ canStart, canStop, canRestart, pending, onAct, disabledReason }: { canStart: boolean; canStop: boolean; canRestart: boolean; pending: boolean; onAct: (verb: 'start' | 'stop' | 'restart') => void; disabledReason?: string }) {
  return (
    <div className="actions" aria-label="Server controls" role="group">
      <button type="button" className="btn primary" disabled={!canStart || pending} onClick={() => onAct('start')}>
        Start
      </button>
      <button type="button" className="btn" disabled={!canStop || pending} onClick={() => onAct('stop')}>
        Stop
      </button>
      <button type="button" className="btn" disabled={!canRestart || pending} onClick={() => onAct('restart')}>
        Restart
      </button>
      {disabledReason && <span className="muted small">{disabledReason}</span>}
    </div>
  )
}

function Head() {
  return (
    <div className="page-head">
      <div>
        <h1>Overview</h1>
        <div className="sub">Live state of your Minecraft server.</div>
      </div>
    </div>
  )
}

export function opText(kind: string) {
  return (
    {
      create: 'Creating the server',
      start: 'Starting',
      stop: 'Stopping',
      restart: 'Restarting',
      backup: 'Backup in progress',
      restore: 'Restore in progress',
      recover: 'Restarting after an outside stop',
      'auto-restart': 'Restarting after a crash',
    }[kind] ?? kind
  )
}

export function phaseText(p: string) {
  return (
    {
      pulling_image: 'downloading the runtime image',
      downloading_server: 'downloading Paper',
      verifying_download: 'verifying the download checksum',
      starting_container: 'starting',
      starting: 'starting',
      preparing_world: 'preparing the world',
      online: 'online',
      stopping: 'saving and stopping',
      archiving: 'writing the archive (server offline)',
      restarting: 'starting the server again',
      verifying: 'verifying the archive',
      saving_rollback: 'saving a rollback archive',
      replacing_world: 'replacing the world',
      reverting: 'putting the previous world back',
      '': 'working',
    }[p] ?? p
  )
}
