import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { ArrowRightIcon, CopyIcon, EllipsisIcon, LinkIcon, PlusIcon, ShieldCheckIcon, ShieldOffIcon, UserMinusIcon, UserPlusIcon, UserXIcon } from 'lucide-react'
import { del, get, post } from '@/api/client'
import type { Activity, Invite, OperatorEntry, PlayersSummary, ServerStatus, SessionsResponse, WhitelistEntry } from '@/api/types'
import { errorText, serverApi, useServerMachine, useWorkspace } from '@/api/workspace'
import { EmptyArt } from '@/components/app/art'
import { Card, CardHint, CardTitle, copyText, CopyButton, PlayerFace, SectionLabel } from '@/components/app/bits'
import { Segmented, useIsPhone } from '@/components/app/controls'
import { ListSkeleton, TableSkeleton } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { Menu, MenuItem, MenuPopup, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { can } from '@/lib/access'
import { formatDay, formatDuration, localTimeZone, relativeTime } from '@/lib/format'
import type { Join } from '@/lib/machines'
import { usePending, withChanges, type ListChange } from '@/lib/optimistic'
import { presenceProps, useListPresence } from '@/lib/presence'
import { linkProps, rePlayerName } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { InviteLinks, JoinRequestNotice, NewInviteDialog, useInvites } from './invites'

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
  const loading = (whitelist.loading && !whitelist.data) || (summary.loading && !summary.data)
  return { whitelist: whitelist.data, operators: operators.data, summary: summary.data, sessions: sessions.data, activity: activity.data, refresh, loading }
}

/** A player's name that opens their profile under Players. */
export function PlayerLink({ server, name, className, children }: { server: ServerStatus; name: string; className?: string; children?: ReactNode }) {
  if (!rePlayerName.test(name)) return <span className={className}>{children ?? name}</span>
  return (
    <a {...linkProps({ name: 'player', slug: server.slug, player: name })} className={cn('rounded-sm outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring', className)}>
      {children ?? name}
    </a>
  )
}

/** Why the allowlist and operators can't change right now: they go through the running server. */
export function listLocked(server: ServerStatus, offline: string | undefined): string | undefined {
  if (offline) return offline
  return server.phase === 'online' ? undefined : t('players.startToChange', { server: server.name })
}

const nameKey = (p: { name: string }) => p.name.toLowerCase()

type PlayerAction = 'op' | 'deop' | 'kick' | 'unlist'

function actionText(action: PlayerAction, name: string): { done: string; failed: string } {
  switch (action) {
    case 'op':
      return { done: t('players.opToast', { name }), failed: t('players.opFailed', { name }) }
    case 'deop':
      return { done: t('players.deopToast', { name }), failed: t('players.deopFailed', { name }) }
    case 'kick':
      return { done: t('players.kickedToast', { name }), failed: t('players.kickFailed', { name }) }
    case 'unlist':
      return { done: t('players.removedToast', { name }), failed: t('players.unlistFailed', { name }) }
    default: {
      const unreachable: never = action
      return unreachable
    }
  }
}

interface AddForm {
  name: string
  setName: (name: string) => void
  error: string | undefined
  add: (e: FormEvent) => Promise<void>
}

/**
 * The allowlist and operators with changes shown before the server confirms
 * them, and the actions that change them. It lives on the page, so a name
 * that couldn't be added is still in the field when the empty state returns.
 */
function usePlayerLists(server: ServerStatus, whitelist: WhitelistEntry[] | undefined, operators: OperatorEntry[] | undefined, reload: () => Promise<void>) {
  const listing = usePending<ListChange<WhitelistEntry>>()
  const opping = usePending<ListChange<OperatorEntry>>()
  const [name, setName] = useState('')
  const [error, setError] = useState<string>()

  async function add(e: FormEvent) {
    e.preventDefault()
    const n = name.trim()
    if (!reName.test(n)) {
      setError(t('players.nameRule'))
      return
    }
    setError(undefined)
    setName('')
    try {
      await listing.run({ add: { name: n } }, () => post(serverApi(server.id, '/whitelist'), { name: n }), reload)
      toastManager.add({ title: t('players.addedToast', { name: n }), type: 'success' })
    } catch (err) {
      setName((typed) => typed || n)
      setError(errorText(err))
    }
  }

  function save(action: PlayerAction, who: string): Promise<void> {
    const path = (list: string) => serverApi(server.id, `/${list}/${encodeURIComponent(who)}`)
    switch (action) {
      case 'op':
        return opping.run({ add: { name: who, level: 4 } }, () => post(serverApi(server.id, '/operators'), { name: who }), reload)
      case 'deop':
        return opping.run({ remove: nameKey({ name: who }) }, () => del(path('operators')), reload)
      case 'unlist':
        return listing.run({ remove: nameKey({ name: who }) }, () => del(path('whitelist')), reload)
      case 'kick':
        return post(serverApi(server.id, '/kick'), { name: who }).then(reload)
      default: {
        const unreachable: never = action
        return unreachable
      }
    }
  }

  async function act(action: PlayerAction, who: string) {
    const text = actionText(action, who)
    try {
      await save(action, who)
      toastManager.add({ title: text.done, type: 'success' })
    } catch (e) {
      toastManager.add({ title: text.failed, description: errorText(e), type: 'error' })
    }
  }

  const form: AddForm = { name, setName, error, add }
  return { whitelist: withChanges(whitelist, listing.changes, nameKey), operators: withChanges(operators, opping.changes, nameKey), form, act }
}

/** The "add a player" field and button, used on the page, in the empty state and on phones. */
function AddPlayer({ server, form, big, placeholder, iconButton, outline }: { server: ServerStatus; form: AddForm; big?: boolean; placeholder: string; iconButton?: boolean; outline?: boolean }) {
  const { offline } = useServerMachine(server)
  const input = useRef<HTMLInputElement>(null)
  const blocked = listLocked(server, offline)
  const hash = window.location.hash

  useEffect(() => {
    if (hash === '#add') input.current?.focus()
  }, [hash])

  return (
    <form onSubmit={form.add} className="flex flex-col gap-1.5" noValidate>
      <div className="flex gap-2">
        <InputGroup className={cn('flex-1', big && 'max-sm:h-11')}>
          <InputGroupAddon>
            <UserPlusIcon aria-hidden="true" />
          </InputGroupAddon>
          <InputGroupInput
            ref={input}
            value={form.name}
            onChange={(e) => form.setName(e.target.value)}
            placeholder={blocked ?? placeholder}
            aria-label={t('players.nameLabel')}
            aria-invalid={form.error ? true : undefined}
            disabled={!!blocked}
            autoComplete="off"
            spellCheck={false}
            maxLength={16}
          />
        </InputGroup>
        {iconButton ? (
          <Button type="submit" size="icon-xl" aria-label={t('players.add')} disabledReason={blocked}>
            <PlusIcon />
          </Button>
        ) : (
          <Button type="submit" variant={outline ? 'outline' : 'default'} disabledReason={blocked}>
            <PlusIcon />
            {t('players.add')}
          </Button>
        )}
      </div>
      {form.error && (
        <p className="text-xs text-destructive-foreground" role="alert">
          {form.error}
        </p>
      )}
    </form>
  )
}

export async function playerAction(server: ServerStatus, method: 'POST' | 'DELETE', path: string, name: string, done: string, after: () => void) {
  try {
    if (method === 'POST') await post(serverApi(server.id, path), { name })
    else await del(serverApi(server.id, `${path}/${encodeURIComponent(name)}`))
    toastManager.add({ title: done, type: 'success' })
    after()
  } catch (e) {
    toastManager.add({ title: errorText(e), type: 'error' })
  }
}

function PlayerMenu({ server, name, op, online, onAction, phone }: { server: ServerStatus; name: string; op: boolean; online: boolean; onAction: (action: PlayerAction) => void; phone?: boolean }) {
  const { offline } = useServerMachine(server)
  const blocked = listLocked(server, offline)
  return (
    <Menu>
      <MenuTrigger disabled={!!blocked} render={<Button variant="ghost" size={phone ? 'icon-lg' : 'icon-sm'} aria-label={t('players.menuFor', { name })} disabledReason={blocked} />}>
        <EllipsisIcon />
      </MenuTrigger>
      <MenuPopup align="end" className="min-w-60">
        {op ? (
          <MenuItem onClick={() => onAction('deop')} className="items-start py-1.5">
            <ShieldOffIcon className="mt-0.5" />
            <span>
              <span className="block">{t('players.removeOp')}</span>
              <span className="block text-xs text-muted-foreground">{t('players.removeOpHint')}</span>
            </span>
          </MenuItem>
        ) : (
          <MenuItem onClick={() => onAction('op')} className="items-start py-1.5">
            <ShieldCheckIcon className="mt-0.5" />
            <span>
              <span className="block">{t('players.makeOp')}</span>
              <span className="block text-xs text-muted-foreground">{t('players.makeOpHint')}</span>
            </span>
          </MenuItem>
        )}
        {online && (
          <MenuItem onClick={() => onAction('kick')} className="items-start py-1.5">
            <UserXIcon className="mt-0.5" />
            <span>
              <span className="block">{t('players.kick')}</span>
              <span className="block text-xs text-muted-foreground">{t('players.kickHint')}</span>
            </span>
          </MenuItem>
        )}
        <MenuSeparator />
        <MenuItem variant="destructive" onClick={() => onAction('unlist')}>
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
  const manage = can(ws.me, 'players.manage')
  const [days, setDays] = useState<Days>('7')
  const p = usePlayers(s, days)
  const lists = usePlayerLists(s, p.whitelist, p.operators, p.refresh)
  const invites = useInvites(s, manage)
  const [newOpen, setNewOpen] = useState(false)
  const [fresh, setFresh] = useState<string>()
  useEffect(() => {
    if (!fresh) return
    const id = window.setTimeout(() => setFresh(undefined), 4000)
    return () => window.clearTimeout(id)
  }, [fresh])
  const { stale, join } = useServerMachine(s)
  const address = join.address
  const online = !stale && s.phase === 'online'
  const onlineNames = online ? (s.players?.names ?? []) : []
  const isOnline = (n: string) => onlineNames.some((o) => o.toLowerCase() === n.toLowerCase())
  const ops = new Set((lists.operators ?? []).map(nameKey))
  const openFor = new Map((p.sessions?.sessions ?? []).filter((x) => !x.end).map((x) => [x.player.toLowerCase(), x.durationSeconds]))
  const addedAt = new Map<string, string>()
  for (const a of p.activity ?? []) if (a.kind === 'allowlisted' && a.player && !addedAt.has(a.player.toLowerCase())) addedAt.set(a.player.toLowerCase(), a.ts)
  const whitelist = lists.whitelist ?? []
  const played = p.summary?.players ?? []
  const everyone: { name: string; uuid?: string }[] | undefined =
    lists.whitelist && p.summary ? [...played, ...whitelist.filter((w) => !played.some((pl) => nameKey(pl) === nameKey(w)))].sort((a, b) => Number(isOnline(b.name)) - Number(isOnline(a.name))) : undefined
  const listed = useListPresence(lists.whitelist, nameKey)
  const everyoneRows = useListPresence(everyone, nameKey)
  const playing = useListPresence(onlineNames, (n) => n.toLowerCase())

  const onCreated = async (inv: Invite) => {
    setFresh(inv.id)
    await invites.refresh()
  }
  const decided = async () => {
    await Promise.all([p.refresh(), invites.refresh()])
  }
  const notice = manage && <JoinRequestNotice server={s} onDecided={decided} />
  const dialog = manage && <NewInviteDialog server={s} open={newOpen} onOpenChange={setNewOpen} data={invites.data} onCreated={onCreated} />

  if (p.loading || (manage && invites.loading && !invites.data)) return <PlayersSkeleton phone={phone} />

  const empty = !!lists.whitelist && !!p.summary && whitelist.length === 0 && played.length === 0 && !invites.data?.invites.length

  const onList = (n: string) => whitelist.some((w) => w.name.toLowerCase() === n.toLowerCase())
  const access = (n: string) => (ops.has(n.toLowerCase()) ? t('players.access.operator') : onList(n) ? t('players.access.allowed') : t('players.access.none'))
  const onlineFor = (n: string) => (openFor.has(n.toLowerCase()) ? t('players.onlineFor', { duration: formatDuration(openFor.get(n.toLowerCase()) ?? 0) }) : t('players.onlineNow'))
  const listLine = (w: WhitelistEntry) => {
    if (ops.has(w.name.toLowerCase())) return t('players.operator')
    if (w.joined) return w.joined.text
    const added = addedAt.get(w.name.toLowerCase())
    return added ? t('players.added', { time: relativeTime(added) }) : t('players.onList')
  }
  const copyAddress = async () => {
    const ok = await copyText(address)
    toastManager.add(ok ? { title: t('toast.copied'), type: 'success' } : { title: t('toast.copyFailed'), type: 'error' })
  }

  let body: ReactNode
  if (empty) {
    body = <EmptyPlayers server={s} join={join} form={lists.form} phone={phone} manage={manage} onNewLink={() => setNewOpen(true)} />
  } else if (phone) {
    body = (
      <>
        <Card className="p-4">
          <CardTitle className="text-[17px]">{t('players.whoCanJoin')}</CardTitle>
          <p className="mt-2 text-[15px] leading-5 text-muted-foreground">{manage ? t('players.whoHintPhone') : t('players.whoHint')}</p>
          {manage && (
            <div className="mt-3">
              <AddPlayer server={s} form={lists.form} placeholder={t('players.namePlaceholderShort')} iconButton big />
            </div>
          )}
        </Card>
        {everyoneRows.length > 0 && (
          <section aria-labelledby="everyone">
            <SectionLabel className="px-4">
              <span id="everyone">{t('players.everyone')}</span>
            </SectionLabel>
            <ul className="mt-2 overflow-hidden rounded-3xl border border-border bg-white">
              {everyoneRows.map(({ key, item: r, state }) => {
                const stat = played.find((x) => x.name.toLowerCase() === r.name.toLowerCase())
                const line = isOnline(r.name) ? onlineFor(r.name) : ops.has(r.name.toLowerCase()) ? t('players.access.operator') : stat ? t('players.lastSeen', { time: relativeTime(stat.lastSeen) }) : t('players.onList')
                return (
                  <li key={key} {...presenceProps(state)} className="flex min-h-14 items-center gap-1 border-b border-border py-1.5 pr-1 pl-4 last:border-b-0">
                    <PlayerLink server={s} name={r.name} className="flex min-w-0 flex-1 items-center gap-3 self-stretch hover:no-underline">
                      <PlayerFace name={r.name} uuid={r.uuid} size={36} />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-base">{r.name}</span>
                        <span className="block truncate text-[13px] text-muted-foreground">{line}</span>
                      </span>
                    </PlayerLink>
                    {manage && onList(r.name) && <PlayerMenu server={s} name={r.name} op={ops.has(r.name.toLowerCase())} online={isOnline(r.name)} onAction={(a) => void lists.act(a, r.name)} phone />}
                  </li>
                )
              })}
            </ul>
          </section>
        )}
        {manage && <InviteLinks server={s} data={invites.data} onNew={() => setNewOpen(true)} onChanged={invites.refresh} fresh={fresh} />}
        <div className="mt-auto flex items-center gap-3 pt-2">
          <p className="min-w-0 flex-1 text-[13px] text-muted-foreground">{address ? t('players.tellPhone', { address }) : join.reason}</p>
          {address && <CopyButton text={t('players.inviteMessage', { address })} size="lg" toast={t('toast.copied')} />}
        </div>
      </>
    )
  } else {
    body = (
      <>
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1.25fr)_minmax(0,1fr)]">
          <Card>
            <div className="flex items-baseline justify-between gap-3">
              <CardTitle>{t('players.whoCanJoin')}</CardTitle>
              <span className="text-xs text-muted-foreground">{t('players.people', { count: whitelist.length })}</span>
            </div>
            <CardHint>{t('players.whoHint')}</CardHint>
            {manage && (
              <div className="mt-3">
                <AddPlayer server={s} form={lists.form} placeholder={t('players.namePlaceholder')} outline />
              </div>
            )}
            <ul className="mt-3 flex flex-col">
              {listed.map(({ key, item: w, state }) => (
                <li key={key} {...presenceProps(state)} className="flex min-h-12 items-center gap-3 border-t border-border py-2">
                  <PlayerFace name={w.name} uuid={w.uuid} size={28} />
                  <span className="min-w-0 flex-1">
                    <PlayerLink server={s} name={w.name} className="block w-fit max-w-full truncate text-[13px] font-semibold" />
                    <span className="block truncate text-xs text-muted-foreground">{listLine(w)}</span>
                  </span>
                  {manage && <PlayerMenu server={s} name={w.name} op={ops.has(w.name.toLowerCase())} online={isOnline(w.name)} onAction={(a) => void lists.act(a, w.name)} />}
                </li>
              ))}
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
            <ul className="mt-3 mb-4 flex flex-col gap-3">
              {playing.map(({ key, item: n, state }) => (
                <li key={key} {...presenceProps(state)} className="flex items-center gap-3">
                  <PlayerFace name={n} size={28} />
                  <span className="min-w-0">
                    <PlayerLink server={s} name={n} className="block w-fit max-w-full truncate text-[13px] font-semibold" />
                    <span className="block text-xs text-muted-foreground">{onlineFor(n)}</span>
                  </span>
                </li>
              ))}
              {online && playing.length === 0 && <li className="text-[13px] text-muted-foreground">{t('players.nobodyOnline')}</li>}
            </ul>
            {manage ? (
              <div className="mt-auto border-t border-border pt-4">
                <h3 className="text-[13px] font-semibold">{t('invites.friendsTitle')}</h3>
                <p className="mt-1 text-xs text-muted-foreground">{t('invites.friendsBody')}</p>
                <div className="mt-3 flex flex-wrap gap-2">
                  <Button variant="outline" size="sm" onClick={() => setNewOpen(true)}>
                    <LinkIcon />
                    {t('invites.new')}
                  </Button>
                  <Button variant="ghost" size="sm" onClick={copyAddress} disabledReason={address ? undefined : join.reason}>
                    {t('players.copyAddress')}
                  </Button>
                </div>
              </div>
            ) : (
              <div className="mt-auto border-t border-border pt-4">
                <h3 className="text-[13px] font-semibold">{t('players.tell')}</h3>
                <p className="mt-1 text-xs text-muted-foreground">{address ? t('players.tellBody', { address }) : join.reason}</p>
                {address && (
                  <div className="mt-3 flex flex-wrap gap-2">
                    <CopyButton text={t('players.inviteMessage', { address })} label={t('players.copyInvite')} toast={t('toast.copied')} />
                    <Button variant="ghost" size="sm" onClick={copyAddress}>
                      <CopyIcon />
                      {t('players.copyAddress')}
                    </Button>
                  </div>
                )}
              </div>
            )}
          </Card>
        </div>
        {manage && <InviteLinks server={s} data={invites.data} onNew={() => setNewOpen(true)} onChanged={invites.refresh} fresh={fresh} />}
        <section aria-labelledby="everyone" className="mt-2">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <h2 id="everyone" className="text-[15px] font-semibold">
              {t('players.everyone')}
            </h2>
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
                {!p.summary && <TableSkeleton cols={['start', 'start', 'end', 'end', 'start']} rowClassName="h-12 border-t border-border" />}
                {p.summary && played.length === 0 && (
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
                          <PlayerLink server={s} name={pl.name} className="block w-fit font-semibold" />
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

  return (
    <>
      {notice}
      {body}
      {dialog}
    </>
  )
}

/** The Players tab while its lists load: the shapes of the cards, never a spinner. */
function PlayersSkeleton({ phone }: { phone: boolean }) {
  if (phone) {
    return (
      <div className="flex flex-col gap-4" aria-busy="true">
        <Skeleton className="h-36 w-full rounded-3xl" />
        <ListSkeleton rows={4} face="size-9 rounded-md" rowClassName="flex min-h-14 items-center gap-3 border-b border-border py-1.5 pr-1 pl-4 last:border-b-0" className="overflow-hidden rounded-3xl border border-border bg-white" />
      </div>
    )
  }
  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1.25fr)_minmax(0,1fr)]" aria-busy="true">
      <Card>
        <Skeleton className="h-4 w-32" />
        <Skeleton className="mt-2 h-3 w-56" />
        <Skeleton className="mt-4 h-9 w-full" />
        <ListSkeleton rows={4} face="size-7 rounded-md" rowClassName="flex min-h-12 items-center gap-3 border-t border-border py-2" className="mt-3 flex flex-col" />
      </Card>
      <Card>
        <Skeleton className="h-4 w-28" />
        <Skeleton className="mt-2 h-3 w-44" />
        <ListSkeleton face="size-7 rounded-md" rowClassName="flex items-center gap-3" className="mt-3 flex flex-col gap-3" />
      </Card>
    </div>
  )
}

function EmptyPlayers({ server: s, join, form, phone, manage, onNewLink }: { server: ServerStatus; join: Join; form: AddForm; phone: boolean; manage: boolean; onNewLink: () => void }) {
  const steps = [
    { title: t('players.step1'), hint: t('players.step1Hint') },
    { title: t('players.step2'), hint: t('players.step2Hint') },
    { title: t('players.step3'), hint: t('players.step3Hint') },
  ]
  return (
    <div className="flex flex-1 flex-col items-center py-6 text-center max-sm:py-2">
      <EmptyArt kind="players" scale={phone ? 7 : 5} className="rounded-2xl" />
      <h2 className="mt-5 text-title font-extrabold tracking-[-0.015em] max-sm:text-[22px]">{t('players.emptyTitle')}</h2>
      <p className="mt-2 max-w-[520px] text-sm text-muted-foreground max-sm:text-[15px]">{t('players.emptyBody')}</p>
      {manage && (
        <div className="mt-5 w-full max-w-[420px] text-left">
          <AddPlayer server={s} form={form} placeholder={phone ? t('players.namePlaceholderShort') : t('players.emptyPlaceholder')} iconButton={phone} big />
        </div>
      )}
      {join.address ? (
        <p className="mt-3 flex items-center gap-2 text-xs text-muted-foreground">
          {t('players.theirAddress')}
          <span className="font-semibold text-foreground">{join.address}</span>
          <CopyButton text={join.address} size="xs" toast={t('toast.copied')} />
        </p>
      ) : (
        <p className="mt-3 text-xs text-muted-foreground">{join.reason}</p>
      )}
      {manage && (
        <Button variant="ghost" size={phone ? 'lg' : 'sm'} className="mt-1 text-success-strong" onClick={onNewLink}>
          <LinkIcon />
          {t('invites.new')}
        </Button>
      )}
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
