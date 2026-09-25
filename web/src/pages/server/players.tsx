import { useEffect, useRef, useState, type FormEvent } from 'react'
import { ArrowRightIcon, CopyIcon, EllipsisIcon, PlusIcon, ShieldCheckIcon, ShieldOffIcon, UserMinusIcon, UserPlusIcon, UserXIcon } from 'lucide-react'
import { del, get, post } from '@/api/client'
import type { Activity, OperatorEntry, PlayersSummary, ServerStatus, SessionsResponse, WhitelistEntry } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { EmptyArt } from '@/components/app/art'
import { Card, CardHint, CardTitle, copyText, CopyButton, PlayerFace, SectionLabel } from '@/components/app/bits'
import { Segmented, useIsPhone } from '@/components/app/controls'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { Menu, MenuItem, MenuPopup, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { formatDay, formatDuration, joinAddress, localTimeZone, relativeTime } from '@/lib/format'
import { linkProps } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

const reName = /^[A-Za-z0-9_]{3,16}$/

type Days = '1' | '7' | '30'

function usePlayers(s: ServerStatus, days: Days) {
  const whitelist = usePoll(() => get<WhitelistEntry[]>(serverApi(s.id, '/whitelist')), 10_000, s.id)
  const operators = usePoll(() => get<OperatorEntry[]>(serverApi(s.id, '/operators')), 10_000, s.id)
  const summary = usePoll(() => get<PlayersSummary>(serverApi(s.id, `/players/summary?days=${days}&tz=${encodeURIComponent(localTimeZone())}`)), 30_000, `${s.id}:${days}`)
  const sessions = usePoll(() => get<SessionsResponse>(serverApi(s.id, '/players/sessions?range=24h')), 30_000, s.id)
  const activity = usePoll(() => get<Activity[]>(serverApi(s.id, '/activity?limit=200')), 60_000, s.id)
  const refresh = async () => {
    await Promise.all([whitelist.refresh(), operators.refresh(), activity.refresh()])
  }
  return { whitelist: whitelist.data, operators: operators.data, summary: summary.data, sessions: sessions.data, activity: activity.data, refresh }
}

/** The "add a player" field and button, used on the page, in the empty state and on phones. */
function AddPlayer({ server, onAdded, big, placeholder, iconButton }: { server: ServerStatus; onAdded: () => void; big?: boolean; placeholder: string; iconButton?: boolean }) {
  const ws = useWorkspace()
  const [name, setName] = useState('')
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const input = useRef<HTMLInputElement>(null)
  const online = !ws.stale && server.phase === 'online'
  const hash = window.location.hash

  useEffect(() => {
    if (hash === '#add') input.current?.focus()
  }, [hash])

  async function add(e: FormEvent) {
    e.preventDefault()
    const n = name.trim()
    if (!reName.test(n)) {
      setError(t('players.nameRule'))
      return
    }
    setBusy(true)
    setError(undefined)
    try {
      await post(serverApi(server.id, '/whitelist'), { name: n })
      toastManager.add({ title: t('players.addedToast', { name: n }), type: 'success' })
      setName('')
      onAdded()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={add} className="flex flex-col gap-1.5" noValidate>
      <div className="flex gap-2">
        <InputGroup className={cn('flex-1', big && 'max-sm:h-11')}>
          <InputGroupAddon>
            <UserPlusIcon aria-hidden="true" />
          </InputGroupAddon>
          <InputGroupInput
            ref={input}
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={online ? placeholder : t('players.startToChange', { server: server.name })}
            aria-label={t('players.nameLabel')}
            aria-invalid={error ? true : undefined}
            disabled={!online}
            autoComplete="off"
            spellCheck={false}
            maxLength={16}
          />
        </InputGroup>
        {iconButton ? (
          <Button type="submit" size="icon-xl" aria-label={t('players.add')} loading={busy} disabled={!online}>
            <PlusIcon />
          </Button>
        ) : (
          <Button type="submit" loading={busy} disabled={!online}>
            <PlusIcon />
            {t('players.add')}
          </Button>
        )}
      </div>
      {error && (
        <p className="text-xs text-destructive-foreground" role="alert">
          {error}
        </p>
      )}
    </form>
  )
}

async function playerAction(server: ServerStatus, method: 'POST' | 'DELETE', path: string, name: string, done: string, after: () => void) {
  try {
    if (method === 'POST') await post(serverApi(server.id, path), { name })
    else await del(serverApi(server.id, `${path}/${encodeURIComponent(name)}`))
    toastManager.add({ title: done, type: 'success' })
    after()
  } catch (e) {
    toastManager.add({ title: errorText(e), type: 'error' })
  }
}

function PlayerMenu({ server, name, op, online, after, phone }: { server: ServerStatus; name: string; op: boolean; online: boolean; after: () => void; phone?: boolean }) {
  const ws = useWorkspace()
  const up = !ws.stale && server.phase === 'online'
  return (
    <Menu>
      <MenuTrigger render={<Button variant="ghost" size={phone ? 'icon-lg' : 'icon-sm'} aria-label={t('players.menuFor', { name })} disabled={!up} />}>
        <EllipsisIcon />
      </MenuTrigger>
      <MenuPopup align="end" className="min-w-60">
        {op ? (
          <MenuItem onClick={() => void playerAction(server, 'DELETE', '/operators', name, t('players.deopToast', { name }), after)} className="items-start py-1.5">
            <ShieldOffIcon className="mt-0.5" />
            <span>
              <span className="block">{t('players.removeOp')}</span>
              <span className="block text-xs text-muted-foreground">{t('players.removeOpHint')}</span>
            </span>
          </MenuItem>
        ) : (
          <MenuItem onClick={() => void playerAction(server, 'POST', '/operators', name, t('players.opToast', { name }), after)} className="items-start py-1.5">
            <ShieldCheckIcon className="mt-0.5" />
            <span>
              <span className="block">{t('players.makeOp')}</span>
              <span className="block text-xs text-muted-foreground">{t('players.makeOpHint')}</span>
            </span>
          </MenuItem>
        )}
        {online && (
          <MenuItem onClick={() => void playerAction(server, 'POST', '/kick', name, t('players.kickedToast', { name }), after)} className="items-start py-1.5">
            <UserXIcon className="mt-0.5" />
            <span>
              <span className="block">{t('players.kick')}</span>
              <span className="block text-xs text-muted-foreground">{t('players.kickHint')}</span>
            </span>
          </MenuItem>
        )}
        <MenuSeparator />
        <MenuItem variant="destructive" onClick={() => void playerAction(server, 'DELETE', '/whitelist', name, t('players.removedToast', { name }), after)}>
          <UserMinusIcon />
          {t('players.unlist')}
        </MenuItem>
      </MenuPopup>
    </Menu>
  )
}

export function PlayersPage({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const [days, setDays] = useState<Days>('7')
  const p = usePlayers(s, days)
  const address = joinAddress(window.location.hostname, s.gamePort)
  const online = !ws.stale && s.phase === 'online'
  const onlineNames = online ? (s.players?.names ?? []) : []
  const isOnline = (n: string) => onlineNames.some((o) => o.toLowerCase() === n.toLowerCase())
  const ops = new Set((p.operators ?? []).map((o) => o.name.toLowerCase()))
  const openFor = new Map((p.sessions?.sessions ?? []).filter((x) => !x.end).map((x) => [x.player.toLowerCase(), x.durationSeconds]))
  const addedAt = new Map<string, string>()
  for (const a of p.activity ?? []) if (a.kind === 'allowlisted' && a.player && !addedAt.has(a.player.toLowerCase())) addedAt.set(a.player.toLowerCase(), a.ts)
  const whitelist = p.whitelist ?? []
  const played = p.summary?.players ?? []

  if (p.whitelist && p.summary && whitelist.length === 0 && played.length === 0) return <EmptyPlayers server={s} address={address} onAdded={p.refresh} phone={phone} />

  const onList = (n: string) => whitelist.some((w) => w.name.toLowerCase() === n.toLowerCase())
  const access = (n: string) => (ops.has(n.toLowerCase()) ? t('players.access.operator') : onList(n) ? t('players.access.allowed') : t('players.access.none'))
  const onlineFor = (n: string) => (openFor.has(n.toLowerCase()) ? t('players.onlineFor', { duration: formatDuration(openFor.get(n.toLowerCase()) ?? 0) }) : t('players.onlineNow'))

  if (phone) {
    const names = new Map<string, { name: string; uuid?: string }>()
    for (const pl of played) names.set(pl.name.toLowerCase(), { name: pl.name, uuid: pl.uuid })
    for (const w of whitelist) if (!names.has(w.name.toLowerCase())) names.set(w.name.toLowerCase(), { name: w.name, uuid: w.uuid })
    const rows = [...names.values()].sort((a, b) => Number(isOnline(b.name)) - Number(isOnline(a.name)))
    return (
      <div className="flex flex-col gap-4">
        <Card className="p-4">
          <CardTitle className="text-[17px]">{t('players.whoCanJoin')}</CardTitle>
          <p className="mt-2 text-[15px] leading-5 text-muted-foreground">{t('players.whoHintPhone')}</p>
          <div className="mt-3">
            <AddPlayer server={s} onAdded={p.refresh} placeholder={t('players.namePlaceholderShort')} iconButton big />
          </div>
        </Card>
        <section aria-labelledby="everyone">
          <SectionLabel className="px-4">
            <span id="everyone">{t('players.everyone')}</span>
          </SectionLabel>
          <ul className="mt-2 overflow-hidden rounded-3xl border border-border bg-white">
            {rows.map((r) => {
              const stat = played.find((x) => x.name.toLowerCase() === r.name.toLowerCase())
              const line = isOnline(r.name) ? onlineFor(r.name) : ops.has(r.name.toLowerCase()) ? t('players.access.operator') : stat ? t('players.lastSeen', { time: relativeTime(stat.lastSeen) }) : t('players.onList')
              return (
                <li key={r.name} className="flex min-h-14 items-center gap-3 border-b border-border py-1.5 pr-1 pl-4 last:border-b-0">
                  <PlayerFace name={r.name} uuid={r.uuid} size={36} />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-base">{r.name}</span>
                    <span className="block truncate text-[13px] text-muted-foreground">{line}</span>
                  </span>
                  {onList(r.name) && <PlayerMenu server={s} name={r.name} op={ops.has(r.name.toLowerCase())} online={isOnline(r.name)} after={p.refresh} phone />}
                </li>
              )
            })}
          </ul>
        </section>
        <div className="flex items-center gap-3 pt-2">
          <p className="min-w-0 flex-1 text-[13px] text-muted-foreground">{t('players.tellPhone', { address })}</p>
          <CopyButton text={t('players.inviteMessage', { address })} size="lg" toast={t('toast.copied')} />
        </div>
      </div>
    )
  }

  return (
    <>
      <div className="grid gap-4 lg:grid-cols-[1.25fr_1fr]">
        <Card>
          <div className="flex items-baseline justify-between gap-3">
            <CardTitle>{t('players.whoCanJoin')}</CardTitle>
            <span className="text-xs text-muted-foreground">{t('players.people', { count: whitelist.length })}</span>
          </div>
          <CardHint>{t('players.whoHint')}</CardHint>
          <div className="mt-3">
            <AddPlayer server={s} onAdded={p.refresh} placeholder={t('players.namePlaceholder')} />
          </div>
          <ul className="mt-3 flex flex-col">
            {whitelist.map((w) => {
              const op = ops.has(w.name.toLowerCase())
              const added = addedAt.get(w.name.toLowerCase())
              return (
                <li key={w.name} className="flex min-h-12 items-center gap-3 border-t border-border py-2">
                  <PlayerFace name={w.name} uuid={w.uuid} size={28} />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-[13px] font-semibold">{w.name}</span>
                    <span className="block truncate text-xs text-muted-foreground">{op ? t('players.operator') : added ? t('players.added', { time: relativeTime(added) }) : t('players.onList')}</span>
                  </span>
                  <PlayerMenu server={s} name={w.name} op={op} online={isOnline(w.name)} after={p.refresh} />
                </li>
              )
            })}
          </ul>
        </Card>
        <Card>
          <div className="flex items-baseline justify-between gap-3">
            <CardTitle>{t('players.playing')}</CardTitle>
            <a {...linkProps({ name: 'server', slug: s.slug, tab: 'console' })} className="inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline">
              {t('players.consoleLink')}
              <ArrowRightIcon className="size-3.5" aria-hidden="true" />
            </a>
          </div>
          <CardHint>{online ? t('players.playingMeta', { online: onlineNames.length, max: s.players?.max ?? 0, server: s.name }) : t('players.playingOffline', { server: s.name })}</CardHint>
          <ul className="mt-3 flex flex-col gap-3">
            {onlineNames.map((n) => (
              <li key={n} className="flex items-center gap-3">
                <PlayerFace name={n} size={28} />
                <span className="min-w-0">
                  <span className="block truncate text-[13px] font-semibold">{n}</span>
                  <span className="block text-xs text-muted-foreground">{onlineFor(n)}</span>
                </span>
              </li>
            ))}
            {online && onlineNames.length === 0 && <li className="text-[13px] text-muted-foreground">{t('players.nobodyOnline')}</li>}
          </ul>
          <div className="mt-auto border-t border-border pt-4">
            <h3 className="text-[13px] font-semibold">{t('players.tell')}</h3>
            <p className="mt-1 text-xs text-muted-foreground">{t('players.tellBody', { address })}</p>
            <div className="mt-3 flex flex-wrap gap-2">
              <CopyButton text={t('players.inviteMessage', { address })} label={t('players.copyInvite')} toast={t('toast.copied')} />
              <Button
                variant="ghost"
                size="sm"
                onClick={async () => {
                  const ok = await copyText(address)
                  toastManager.add(ok ? { title: t('toast.copied'), type: 'success' } : { title: t('toast.copyFailed'), type: 'error' })
                }}
              >
                <CopyIcon />
                {t('players.copyAddress')}
              </Button>
            </div>
          </div>
        </Card>
      </div>
      <section aria-labelledby="everyone" className="mt-2">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <h2 id="everyone" className="text-[15px] font-semibold">
              {t('players.everyone')}
            </h2>
            <p className="mt-0.5 text-xs text-muted-foreground">{t('players.everyoneHint')}</p>
          </div>
          <Segmented
            value={days}
            onChange={setDays}
            label={t('players.range')}
            options={[
              { value: '1', label: t('overview.range.24h') },
              { value: '7', label: t('overview.range.7d') },
              { value: '30', label: t('overview.range.30d') },
            ]}
          />
        </div>
        <div className="mt-3 overflow-x-auto rounded-2xl border border-border" tabIndex={0} role="region" aria-labelledby="everyone">
          <table className="w-full min-w-[560px] text-[13px]">
            <thead className="bg-muted text-left text-xs text-muted-foreground">
              <tr className="h-9">
                <th className="px-3 font-medium">{t('players.col.player')}</th>
                <th className="px-3 font-medium">{t('players.col.lastSeen')}</th>
                <th className="px-3 text-right font-medium">{t('players.col.sessions')}</th>
                <th className="px-3 text-right font-medium">{t('players.col.playtime')}</th>
                <th className="px-3 font-medium">{t('players.col.access')}</th>
              </tr>
            </thead>
            <tbody>
              {played.length === 0 && (
                <tr>
                  <td colSpan={5} className="px-3 py-4 text-muted-foreground">
                    {t('players.noneYet')}
                  </td>
                </tr>
              )}
              {played.map((pl) => (
                <tr key={pl.name} className="h-12 border-t border-border">
                  <td className="px-3">
                    <span className="flex items-center gap-2.5">
                      <PlayerFace name={pl.name} uuid={pl.uuid} size={24} />
                      <span>
                        <span className="block font-semibold">{pl.name}</span>
                        {pl.online && <span className="block text-xs text-muted-foreground">{t('players.onlineNow')}</span>}
                      </span>
                    </span>
                  </td>
                  <td className="px-3">{pl.online ? t('players.now') : formatDay(pl.lastSeen)}</td>
                  <td className="px-3 text-right tabular-nums">{pl.sessions}</td>
                  <td className="px-3 text-right tabular-nums">{pl.playtimeUncertain ? t('players.approx', { time: formatDuration(pl.playtimeSeconds) }) : formatDuration(pl.playtimeSeconds)}</td>
                  <td className="px-3">{access(pl.name)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {played.some((pl) => pl.playtimeUncertain) && <p className="mt-2 text-xs text-muted-foreground">{t('players.approxNote')}</p>}
      </section>
    </>
  )
}

function EmptyPlayers({ server: s, address, onAdded, phone }: { server: ServerStatus; address: string; onAdded: () => void; phone: boolean }) {
  const steps = [
    { title: t('players.step1'), hint: t('players.step1Hint') },
    { title: t('players.step2'), hint: t('players.step2Hint') },
    { title: t('players.step3'), hint: t('players.step3Hint') },
  ]
  return (
    <div className="flex flex-1 flex-col items-center py-6 text-center max-sm:py-2">
      <EmptyArt kind="players" scale={phone ? 7 : 5} className="rounded-2xl" />
      <h2 className="mt-5 text-title font-extrabold tracking-[-0.015em] max-sm:text-[22px]">{t('players.emptyTitle')}</h2>
      <p className="mt-2 max-w-[520px] text-sm text-muted-foreground max-sm:text-[15px]">{t('players.emptyBody', { server: s.name })}</p>
      <div className="mt-5 w-full max-w-[420px] text-left">
        <AddPlayer server={s} onAdded={onAdded} placeholder={t('players.emptyPlaceholder')} big />
      </div>
      <p className="mt-3 flex items-center gap-2 text-xs text-muted-foreground">
        {t('players.theirAddress')}
        <span className="font-semibold text-foreground">{address}</span>
        <CopyButton text={address} size="xs" toast={t('toast.copied')} />
      </p>
      <ol className="mt-8 grid w-full max-w-[720px] gap-4 border-t border-border pt-5 text-left sm:grid-cols-3">
        {steps.map((st, i) => (
          <li key={st.title}>
            <div className="text-[13px] font-semibold">
              <span className="mr-1.5 text-success-strong">{i + 1}.</span>
              {st.title}
            </div>
            <p className="mt-1 text-xs text-muted-foreground">{st.hint}</p>
          </li>
        ))}
      </ol>
    </div>
  )
}
