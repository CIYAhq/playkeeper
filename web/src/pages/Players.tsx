import { useState } from 'react'
import { get } from '../api/client'
import type { MetricsResponse, PlayersSummary, SessionsResponse } from '../api/types'
import type { PageProps } from '../App'
import { Chart, ChartLegend } from '../components/Chart'
import { InviteFriends } from '../components/Invite'
import { Banner, Card, Empty, Segmented, Spinner } from '../components/ui'
import { coverageSummary } from '../lib/chart'
import { formatDateTime, formatDuration, relativeTime } from '../lib/format'
import { usePoll } from '../lib/usePoll'

type Range = '1h' | '24h' | '7d' | '30d'
const rangeText: Record<Range, string> = { '1h': 'last hour', '24h': 'last 24 hours', '7d': 'last 7 days', '30d': 'last 30 days' }
const days: Record<Range, number> = { '1h': 1, '24h': 1, '7d': 7, '30d': 30 }

/** New installs start on the last hour; older ones on the last day. */
export function defaultRange(collectingSince?: string): Range {
  return collectingSince && Date.now() - new Date(collectingSince).getTime() < 3 * 3600_000 ? '1h' : '24h'
}

const reasonText: Record<string, string> = {
  left: 'left',
  server_stopped: 'server stopped',
  server_crashed: 'server crashed; no leave event recorded',
  not_in_player_list: 'missing from the player list; exact time unknown',
  rejoined_without_leave: 'joined again without a leave event',
}

export function PlayersPage({ status }: PageProps) {
  const [chosen, setRange] = useState<Range>()
  const range: Range = chosen ?? defaultRange(status?.collectingSince)
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone
  const metrics = usePoll(() => get<MetricsResponse>(`/api/metrics?range=${range}`), 30_000, range)
  const sessions = usePoll(() => get<SessionsResponse>(`/api/players/sessions?range=${range}`), 15_000, range)
  const summary = usePoll(() => get<PlayersSummary>(`/api/players/summary?days=${days[range]}&tz=${encodeURIComponent(tz)}`), 60_000, range)
  const cov = metrics.data ? coverageSummary(metrics.data.buckets) : undefined

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Players</h1>
          <div className="sub">Who played and when, from what the server actually reported.</div>
        </div>
        <Segmented label="Time range" value={range} onChange={setRange} options={[{ value: '1h', label: '1 hour' }, { value: '24h', label: '24 hours' }, { value: '7d', label: '7 days' }, { value: '30d', label: '30 days' }]} />
      </div>
      <Card title={`Players online, ${rangeText[range]}`} hint="Most players online in each period">
        {metrics.data ? (
          <>
            <Chart buckets={metrics.data.buckets} bucketSeconds={metrics.data.bucketSeconds} value={(b) => b.playersMax} format={(v) => String(Math.round(v))} label={`Players online, ${rangeText[range]}`} integer />
            <ChartLegend />
            <p className="provenance">
              Source: RCON <code>list</code> every {metrics.data.sampleIntervalSeconds} s (Server List Ping as fallback) and join/leave lines in the server log.{' '}
              {metrics.data.collectingSince ? `Collecting since ${formatDateTime(metrics.data.collectingSince)}; nothing earlier is shown.` : 'No samples yet.'}
              {cov && cov.noData > 0 ? ` ${cov.noData} period(s) have no data because Playkeeper was not collecting.` : ''}
            </p>
          </>
        ) : metrics.error ? (
          <Empty title="Chart unavailable">{metrics.error.message}</Empty>
        ) : (
          <Spinner />
        )}
      </Card>
      <div className="grid grid-2">
        <Card title="Daily activity" hint={`Observed sessions only, days in ${tz}`}>
          {summary.data ? (
            summary.data.days.every((d) => d.sessions === 0) ? (
              <Empty title="No sessions observed yet">When players join, their sessions appear here.</Empty>
            ) : (
              <div className="table-wrap" tabIndex={0}>
                <table>
                  <thead>
                    <tr>
                      <th scope="col">Day</th>
                      <th scope="col" className="num">Players</th>
                      <th scope="col" className="num">Sessions</th>
                      <th scope="col" className="num">Observed playtime</th>
                      <th scope="col" className="num">Data collected</th>
                    </tr>
                  </thead>
                  <tbody>
                    {[...summary.data.days].reverse().map((d) => (
                      <tr key={d.date}>
                        <td>{d.date}</td>
                        <td className="num">{d.uniquePlayers}</td>
                        <td className="num">{d.sessions}</td>
                        <td className="num">
                          {d.playtimeLowerBound ? '≥ ' : ''}
                          {formatDuration(d.playtimeSeconds)}
                        </td>
                        <td className="num">
                          {Math.round(d.coverage * 100)}% {d.coverage < 0.95 && <span className="badge warn">incomplete</span>}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )
          ) : (
            <Spinner />
          )}
          {summary.data && (
            <p className="provenance">
              {summary.data.observedSessions} session(s) in range; {summary.data.uncertainSessions} with an uncertain start or end (marked ≥). Player sessions are kept for {summary.data.retentionDays} days. Player IP addresses are never stored.
            </p>
          )}
        </Card>
        <Card title="Invite friends">
          <InviteFriends online={status?.phase === 'online'} />
        </Card>
      </div>
      <Card title="Sessions" hint={`Join and leave times from the server log, ${rangeText[range]}`}>
        {sessions.error && <Banner tone="bad" title={sessions.error.message} />}
        {sessions.data &&
          (sessions.data.sessions.length === 0 ? (
            <Empty title="No sessions in this period" />
          ) : (
            <div className="table-wrap" tabIndex={0}>
              <table>
                <thead>
                  <tr>
                    <th scope="col">Player</th>
                    <th scope="col">Joined</th>
                    <th scope="col">Left</th>
                    <th scope="col" className="num">Duration</th>
                    <th scope="col">Notes</th>
                  </tr>
                </thead>
                <tbody>
                  {sessions.data.sessions.map((s) => (
                    <tr key={s.id}>
                      <td>{s.player}</td>
                      <td>
                        {formatDateTime(s.start)}
                        {s.startUncertain && <span className="badge warn"> start uncertain</span>}
                      </td>
                      <td>{s.end ? formatDateTime(s.end) : <span className="badge good">online now</span>}</td>
                      <td className="num">
                        {s.endUncertain ? '≤ ' : ''}
                        {formatDuration(s.durationSeconds)}
                      </td>
                      <td className="muted small">
                        {s.endReason ? reasonText[s.endReason] ?? s.endReason : ''}
                        {s.source === 'player_list' ? ' (seen in the player list; no join line)' : ''}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ))}
        {!sessions.data && !sessions.error && <Spinner />}
      </Card>
      {summary.data && summary.data.players.length > 0 && (
        <Card title="Players seen">
          <div className="table-wrap" tabIndex={0}>
            <table>
              <thead>
                <tr>
                  <th scope="col">Player</th>
                  <th scope="col">Last seen</th>
                  <th scope="col" className="num">Sessions</th>
                  <th scope="col" className="num">Observed playtime</th>
                </tr>
              </thead>
              <tbody>
                {summary.data.players.map((p) => (
                  <tr key={p.name}>
                    <td>
                      {p.name} {p.online && <span className="badge good">online</span>}
                    </td>
                    <td>{p.online ? 'now' : relativeTime(p.lastSeen)}</td>
                    <td className="num">{p.sessions}</td>
                    <td className="num">{formatDuration(p.playtimeSeconds)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
    </>
  )
}
