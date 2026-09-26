import { useState, type FormEvent, type ReactNode } from 'react'
import { ArrowRightIcon, BanIcon, ChevronLeftIcon, ChevronRightIcon, EllipsisIcon, LogOutIcon, MessageCircleIcon, SendIcon, ShieldCheckIcon, ShieldOffIcon, UserMinusIcon } from 'lucide-react'
import { get, post } from '@/api/client'
import type { PlayerDay, PlayerProfile, ServerStatus, Session } from '@/api/types'
import { errorText, serverApi, useServerMachine, useWorkspace } from '@/api/workspace'
import { Card, CardHint, CardTitle, Dot, Marker, Notice, PlayerFace, SectionLabel, useNow } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Menu, MenuItem, MenuPopup, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { formatLocale, t } from '@/i18n'
import { can } from '@/lib/access'
import { formatClock, formatDate, formatDuration, localTimeZone, relativeTime } from '@/lib/format'
import { linkProps } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { listLocked, playerAction } from './players'

const shownSessions = { desktop: 4, phone: 3 }

function localDay(date: string): Date {
  const [y = 1970, m = 1, d = 1] = date.split('-').map(Number)
  return new Date(y, m - 1, d)
}

/** "Today", "Yesterday", a weekday this week, else the date. */
function dayName(iso: string, now: Date): string {
  const d = new Date(iso)
  const start = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime()
  const days = Math.round((start(now) - start(d)) / 86_400_000)
  if (days === 0) return t('profile.today')
  if (days === 1) return t('profile.yesterday')
  if (days < 7) return new Intl.DateTimeFormat(formatLocale(), { weekday: 'long' }).format(d)
  return formatDate(iso)
}

function sessionRange(s: Session): string {
  return t('profile.range', { from: formatClock(s.start), to: s.end ? formatClock(s.end) : t('profile.now') })
}

function sessionLength(s: Session): string {
  const d = formatDuration(s.durationSeconds)
  return s.startUncertain || s.endUncertain ? t('players.approx', { time: d }) : d
}

function partText(part: NonNullable<PlayerProfile['mostly']>): string {
  switch (part) {
    case 'morning':
      return t('profile.part.morning')
    case 'afternoon':
      return t('profile.part.afternoon')
    case 'evening':
      return t('profile.part.evening')
    case 'night':
      return t('profile.part.night')
    default: {
      const unreachable: never = part
      return unreachable
    }
  }
}

/** "About 1 h 10 m a day, mostly evenings", averaged over the days they played. */
function summary(p: PlayerProfile): string {
  const played = p.days.filter((d) => d.playtimeSeconds > 0)
  if (!p.mostly || played.length === 0) return t('profile.noPlay')
  const avg = played.reduce((a, d) => a + d.playtimeSeconds, 0) / played.length
  return t('profile.summary', { time: formatDuration(avg), part: partText(p.mostly) })
}

function listedText(p: PlayerProfile): { text: string; tone: 'green' | 'muted' | 'red' } {
  if (p.banned) return { text: t('profile.banned'), tone: 'red' }
  if (p.allowlisted) return { text: p.operator ? t('profile.listedOp') : t('profile.listed'), tone: 'green' }
  if (p.operator) return { text: t('players.access.operator'), tone: 'green' }
  return { text: t('players.access.none'), tone: 'muted' }
}

/** The phone's one line under the status: "On the allowlist since 22 Sep", or the marker alone. */
function listedSinceText(p: PlayerProfile): string {
  if (p.banned || !p.allowlisted || !p.joined?.at) return listedText(p).text
  const date = formatDate(p.joined.at)
  return p.operator ? t('profile.listedOpSince', { date }) : t('profile.listedSince', { date })
}

function joinedText(p: PlayerProfile): string | undefined {
  if (!p.joined) return undefined
  return p.joined.at ? t('profile.joinedOn', { how: p.joined.text, date: formatDate(p.joined.at) }) : p.joined.text
}

/** A player's page under Players: who they are, what you can do, and how they play. */
export function PlayerProfilePage({ server: s, name }: { server: ServerStatus; name: string }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const now = useNow(30_000)
  const profile = usePoll(() => get<PlayerProfile>(serverApi(s.id, `/players/profile?name=${encodeURIComponent(name)}&tz=${encodeURIComponent(localTimeZone())}`)), 30_000, `${s.id}:${name}`)
  const [messaging, setMessaging] = useState(false)
  const [banning, setBanning] = useState(false)
  const { stale, offline } = useServerMachine(s)
  const manage = can(ws.me, 'players.manage')
  const up = !stale && s.phase === 'online'
  const blocked = listLocked(s, offline)
  const p = profile.data
  const refresh = () => void profile.refresh()

  const back = !phone && (
    <a {...linkProps({ name: 'server', slug: s.slug, tab: 'players' })} className="inline-flex w-fit items-center gap-0.5 rounded-sm text-[13px] font-semibold text-success-strong outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
      <ChevronLeftIcon className="size-4" aria-hidden="true" />
      {t('tab.players')}
    </a>
  )

  if (!p) {
    if (profile.error) {
      return (
        <>
          {back}
          <Notice
            tone="error"
            title={profile.error.status === 404 ? t('profile.notFound', { name, server: s.name }) : errorText(profile.error)}
            action={
              profile.error.status === 404 ? undefined : (
                <Button variant="outline" size="sm" onClick={refresh}>
                  {t('common.tryAgain')}
                </Button>
              )
            }
          />
        </>
      )
    }
    return (
      <>
        {back}
        <ProfileSkeleton phone={phone} />
      </>
    )
  }

  const listed = listedText(p)
  const joined = joinedText(p)
  const onlineSeconds = p.onlineSince ? Math.max(0, (now - Date.parse(p.onlineSince)) / 1000) : undefined
  const lastEnd = p.recent.find((x) => x.end)?.end
  const kick = () => void playerAction(s, 'POST', '/kick', p.name, t('players.kickedToast', { name: p.name }), refresh)
  const opToggle = () =>
    p.operator
      ? void playerAction(s, 'DELETE', '/operators', p.name, t('players.deopToast', { name: p.name }), refresh)
      : void playerAction(s, 'POST', '/operators', p.name, t('players.opToast', { name: p.name }), refresh)
  const unlist = () => void playerAction(s, 'DELETE', '/whitelist', p.name, t('players.removedToast', { name: p.name }), refresh)
  const canTalk = manage && up && p.online
  const dialogs = manage && (
    <>
      <MessageDialog server={s} name={p.name} open={messaging} onOpenChange={setMessaging} />
      <BanDialog server={s} name={p.name} open={banning} onOpenChange={setBanning} onBanned={refresh} />
    </>
  )

  if (phone) {
    const status = p.online ? (onlineSeconds !== undefined ? t('profile.onlineShort', { duration: formatDuration(onlineSeconds) }) : t('status.online')) : lastEnd ? t('players.lastSeen', { time: relativeTime(lastEnd, now) }) : t('profile.neverPlayed')
    return (
      <>
        <Card className="p-4">
          <div className="flex items-center gap-4">
            <PlayerFace name={p.name} uuid={p.uuid} size={56} />
            <div className="min-w-0 flex-1">
              <h1 className="truncate text-[20px] leading-6 font-bold">{p.name}</h1>
              <p className="mt-0.5 flex items-center gap-1.5 text-[15px]">
                {p.online && <Dot tone="online" />}
                <span className={cn(!p.online && 'text-muted-foreground')}>{status}</span>
              </p>
              <p className={cn('mt-0.5 truncate text-[13px]', p.banned ? 'text-destructive-foreground' : 'text-muted-foreground')}>{listedSinceText(p)}</p>
            </div>
          </div>
          {canTalk && (
            <div className="mt-4 grid grid-cols-2 gap-2">
              <Button variant="outline" size="touch" onClick={() => setMessaging(true)}>
                <MessageCircleIcon />
                {t('profile.messageShort')}
              </Button>
              <Button variant="outline" size="touch" onClick={kick}>
                <LogOutIcon />
                {t('profile.kick')}
              </Button>
            </div>
          )}
        </Card>
        <div className="grid grid-cols-2 gap-3">
          <Stat label={t('players.col.sessions')} value={String(p.sessions)} phone />
          <Stat label={t('players.col.playtime')} value={p.playtimeUncertain ? t('players.approx', { time: formatDuration(p.playtimeSeconds) }) : formatDuration(p.playtimeSeconds)} phone />
        </div>
        <section aria-labelledby="recent-sessions">
          <SectionLabel className="px-4">
            <span id="recent-sessions">{t('profile.recent')}</span>
          </SectionLabel>
          <RecentSessions profile={p} now={now} phone />
        </section>
        {manage && (
          <ul className="overflow-hidden rounded-3xl border border-border bg-white">
            <li className={cn(!p.banned && 'border-b border-border')}>
              <button type="button" onClick={opToggle} disabled={!!blocked} title={blocked} className="flex min-h-16 w-full items-center gap-3 px-4 py-2 text-left disabled:opacity-50">
                <span className="min-w-0 flex-1">
                  <span className="block text-base">{p.operator ? t('players.removeOp') : t('players.makeOp')}</span>
                  <span className="block text-[13px] text-muted-foreground">{p.operator ? t('players.removeOpHint') : t('players.makeOpHint')}</span>
                </span>
                <ChevronRightIcon className="size-5 text-muted-foreground" aria-hidden="true" />
              </button>
            </li>
            {!p.banned && (
              <li>
                <button type="button" onClick={() => setBanning(true)} disabled={!!blocked} title={blocked} className="flex min-h-14 w-full items-center px-4 py-2 text-left text-base text-destructive-foreground disabled:opacity-50">
                  {t('profile.ban', { server: s.name })}
                </button>
              </li>
            )}
          </ul>
        )}
        {dialogs}
      </>
    )
  }

  return (
    <>
      {back}
      <Card className="flex-row flex-wrap items-center gap-4 p-5">
        <PlayerFace name={p.name} uuid={p.uuid} size={64} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-baseline gap-x-2.5">
            <h1 className="truncate text-[22px] leading-7 font-bold tracking-[-0.01em]">{p.name}</h1>
            <Marker tone={listed.tone}>{listed.text}</Marker>
          </div>
          <p className="mt-0.5 flex items-center gap-1.5 text-[13px]">
            {p.online && <Dot tone="online" />}
            <span className={cn(!p.online && 'text-muted-foreground')}>
              {p.online ? (onlineSeconds !== undefined ? t('profile.onlineFor', { server: s.name, duration: formatDuration(onlineSeconds) }) : t('players.onlineNow')) : lastEnd ? t('players.lastSeen', { time: relativeTime(lastEnd, now) }) : t('profile.neverPlayed')}
            </span>
          </p>
          {joined && <p className="mt-0.5 truncate text-xs text-muted-foreground">{joined}</p>}
        </div>
        {manage && (
          <div className="flex items-center gap-2">
            {canTalk && (
              <>
                <Button variant="outline" onClick={() => setMessaging(true)}>
                  <MessageCircleIcon />
                  {t('profile.message')}
                </Button>
                <Button variant="outline" onClick={kick}>
                  <LogOutIcon />
                  {t('profile.kick')}
                </Button>
              </>
            )}
            <Menu>
              <MenuTrigger disabled={!!blocked} render={<Button variant="ghost" size="icon" aria-label={t('players.menuFor', { name: p.name })} disabledReason={blocked} />}>
                <EllipsisIcon />
              </MenuTrigger>
              <MenuPopup align="end" className="min-w-56">
                <MenuItem onClick={opToggle} className="items-start py-1.5">
                  {p.operator ? <ShieldOffIcon className="mt-0.5" /> : <ShieldCheckIcon className="mt-0.5" />}
                  <span>
                    <span className="block">{p.operator ? t('players.removeOp') : t('players.makeOp')}</span>
                    <span className="block text-xs text-muted-foreground">{p.operator ? t('players.removeOpHint') : t('players.makeOpHint')}</span>
                  </span>
                </MenuItem>
                {(!p.banned || p.allowlisted) && <MenuSeparator />}
                {!p.banned && (
                  <MenuItem variant="destructive" onClick={() => setBanning(true)}>
                    <BanIcon />
                    {t('profile.ban', { server: s.name })}
                  </MenuItem>
                )}
                {p.allowlisted && (
                  <MenuItem variant="destructive" onClick={unlist}>
                    <UserMinusIcon />
                    {t('players.unlist')}
                  </MenuItem>
                )}
              </MenuPopup>
            </Menu>
          </div>
        )}
      </Card>
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat label={t('profile.firstJoined')} value={p.firstSeen ? formatDate(p.firstSeen) : t('profile.notYet')} />
        <Stat label={t('players.col.sessions')} value={String(p.sessions)} />
        <Stat label={t('players.col.playtime')} value={p.playtimeUncertain ? t('players.approx', { time: formatDuration(p.playtimeSeconds) }) : formatDuration(p.playtimeSeconds)} />
        <Stat label={t('profile.longest')} value={formatDuration(p.longestSeconds)} />
      </div>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1.27fr)_minmax(0,1fr)]">
        <PlaytimeChart profile={p} />
        <Card>
          <CardTitle id="recent-sessions">{t('profile.recent')}</CardTitle>
          <RecentSessions profile={p} now={now} />
        </Card>
      </div>
      {dialogs}
    </>
  )
}

function Stat({ label, value, phone }: { label: string; value: ReactNode; phone?: boolean }) {
  return (
    <Card className={cn('gap-1', phone ? 'p-4' : 'px-4 py-4')}>
      <span className={cn('text-muted-foreground', phone ? 'text-[13px]' : 'text-xs')}>{label}</span>
      <span className={cn('font-bold tabular-nums', phone ? 'text-[20px] leading-6' : 'text-lg leading-6')}>{value}</span>
    </Card>
  )
}

/** Fourteen bars, today's darker, and a one-line summary under them. */
function PlaytimeChart({ profile: p }: { profile: PlayerProfile }) {
  const max = Math.max(1, ...p.days.map((d) => d.playtimeSeconds))
  const last = p.days.length - 1
  const dayLabel = (d: PlayerDay, i: number) => (i === last ? t('profile.chartToday') : formatDate(localDay(d.date).toISOString()))
  const line = summary(p)
  return (
    <Card>
      <CardTitle>{t('profile.chartTitle')}</CardTitle>
      <CardHint>{t('profile.chartHint')}</CardHint>
      <div className="mt-5" role="img" aria-label={line}>
        <div className="flex h-[120px] items-end gap-1.5 border-b border-border">
          {p.days.map((d, i) => (
            <div key={d.date} title={t('profile.dayPlaytime', { day: dayLabel(d, i), time: formatDuration(d.playtimeSeconds) })} className="flex h-full min-w-0 flex-1 items-end">
              {d.playtimeSeconds > 0 ? (
                <div className={cn('w-full rounded-t-[3px] transition-[height] duration-(--motion-slow) ease-standard', i === last ? 'bg-primary/80' : 'bg-primary/40')} style={{ height: `${Math.max(4, (d.playtimeSeconds / max) * 100)}%` }} />
              ) : (
                <div className="h-[2px] w-full rounded-full bg-primary/25" />
              )}
            </div>
          ))}
        </div>
        <div className="mt-1.5 flex justify-between text-[11px] text-muted-foreground" aria-hidden="true">
          {p.days[0] && <span>{dayLabel(p.days[0], 0)}</span>}
          <span>{t('profile.chartToday')}</span>
        </div>
      </div>
      <p className="mt-auto border-t border-border pt-3 text-xs text-muted-foreground max-lg:mt-5">{line}</p>
    </Card>
  )
}

function RecentSessions({ profile: p, now, phone }: { profile: PlayerProfile; now: number; phone?: boolean }) {
  const [all, setAll] = useState(false)
  const first = phone ? shownSessions.phone : shownSessions.desktop
  const list = all ? p.recent : p.recent.slice(0, first)
  const today = new Date(now)
  let more: string | undefined
  if (p.recent.length > first) {
    if (all) more = t('profile.fewer')
    else if (p.sessions > p.recent.length) more = t('profile.latestSessions', { count: p.recent.length })
    else more = t('profile.allSessions', { count: p.sessions })
  }
  if (p.recent.length === 0) return <p className={cn('text-muted-foreground', phone ? 'mt-2 px-4 text-[15px]' : 'mt-3 text-[13px]')}>{t('profile.noSessions')}</p>
  if (phone) {
    return (
      <>
        <ul className="mt-2 overflow-hidden rounded-3xl border border-border bg-white">
          {list.map((x) => (
            <li key={x.id} className="border-b border-border px-4 py-1.5 last:border-b-0">
              <span className="block text-base">{dayName(x.start, today)}</span>
              <span className="block text-[13px] text-muted-foreground tabular-nums">
                {sessionRange(x)}
                {t('common.dot')}
                {sessionLength(x)}
              </span>
            </li>
          ))}
        </ul>
        {more && (
          <Button variant="ghost" size="sm" className="mt-1 ml-2 text-success-strong" onClick={() => setAll((v) => !v)}>
            {more}
          </Button>
        )}
      </>
    )
  }
  return (
    <>
      <ul className="mt-3 flex flex-col">
        {list.map((x) => (
          <li key={x.id} className="flex items-center gap-3 border-t border-border py-2.5 first:border-t-0">
            <span className="min-w-0 flex-1">
              <span className="block text-[13px] font-semibold">{dayName(x.start, today)}</span>
              <span className="block text-xs text-muted-foreground tabular-nums">{sessionRange(x)}</span>
            </span>
            <span className="text-[13px] font-semibold tabular-nums">{sessionLength(x)}</span>
          </li>
        ))}
      </ul>
      {more && (
        <button type="button" onClick={() => setAll((v) => !v)} className="mt-auto inline-flex w-fit items-center gap-1 rounded-sm pt-3 text-xs font-medium text-success-strong outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
          {more}
          {!all && <ArrowRightIcon className="size-3.5" aria-hidden="true" />}
        </button>
      )}
    </>
  )
}

function ProfileSkeleton({ phone }: { phone: boolean }) {
  return (
    <div className="flex flex-col gap-4" aria-busy="true">
      <Card className={cn('flex-row items-center gap-4', phone ? 'p-4' : 'p-5')}>
        <Skeleton className={cn('shrink-0 rounded-xl', phone ? 'size-14' : 'size-16')} />
        <span className="flex flex-1 flex-col gap-2">
          <Skeleton className="h-5 w-36" />
          <Skeleton className="h-3 w-56" />
        </span>
      </Card>
      <div className={cn('grid gap-3', phone ? 'grid-cols-2' : 'grid-cols-2 lg:grid-cols-4')}>
        {Array.from({ length: phone ? 2 : 4 }, (_, i) => (
          <Skeleton key={i} className="h-[72px] rounded-3xl" />
        ))}
      </div>
      {!phone && <Skeleton className="h-64 rounded-3xl" />}
    </div>
  )
}

function MessageDialog({ server, name, open, onOpenChange }: { server: ServerStatus; name: string; open: boolean; onOpenChange: (open: boolean) => void }) {
  const phone = useIsPhone()
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  async function send(e: FormEvent) {
    e.preventDefault()
    if (!text.trim()) return
    setBusy(true)
    setError(undefined)
    try {
      await post(serverApi(server.id, '/players/message'), { name, message: text.trim() })
      toastManager.add({ title: t('profile.sentToast', { name }), type: 'success' })
      setText('')
      onOpenChange(false)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[440px]">
        <form onSubmit={send} className="contents" noValidate>
          <DialogHeader>
            <DialogTitle className="text-lg font-bold">{t('profile.messageTitle', { name })}</DialogTitle>
          </DialogHeader>
          <DialogPanel className="flex flex-col gap-1.5">
            <Input value={text} onChange={(e) => setText(e.target.value)} maxLength={200} placeholder={t('profile.messagePlaceholder')} aria-label={t('profile.messageLabel')} aria-invalid={error ? true : undefined} autoComplete="off" autoFocus={!phone} />
            {error ? (
              <span className="text-xs text-destructive-foreground" role="alert">
                {error}
              </span>
            ) : (
              <span className="text-xs text-muted-foreground">{t('profile.messageHint', { name })}</span>
            )}
          </DialogPanel>
          <DialogFooter variant="bare" className="border-t border-border pt-4">
            <Button type="button" variant="ghost" size={phone ? 'touch' : 'default'} onClick={() => onOpenChange(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" size={phone ? 'touch' : 'default'} loading={busy} disabledReason={text.trim() ? undefined : t('profile.typeFirst')}>
              <SendIcon />
              {t('profile.send')}
            </Button>
          </DialogFooter>
        </form>
      </DialogPopup>
    </Dialog>
  )
}

function BanDialog({ server, name, open, onOpenChange, onBanned }: { server: ServerStatus; name: string; open: boolean; onOpenChange: (open: boolean) => void; onBanned: () => void }) {
  const phone = useIsPhone()
  const [busy, setBusy] = useState(false)
  async function ban() {
    setBusy(true)
    try {
      await post(serverApi(server.id, '/ban'), { name })
      toastManager.add({ title: t('profile.bannedToast', { name }), type: 'success' })
      onOpenChange(false)
      onBanned()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[440px]">
        <DialogHeader>
          <DialogTitle className="text-lg font-bold">{t('profile.banTitle', { name, server: server.name })}</DialogTitle>
          <DialogDescription className="text-[13px]">{t('profile.banBody', { name })}</DialogDescription>
        </DialogHeader>
        <DialogFooter variant="bare" className="border-t border-border pt-4">
          <Button variant="ghost" size={phone ? 'touch' : 'default'} onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button variant="destructive" size={phone ? 'touch' : 'default'} onClick={() => void ban()} loading={busy}>
            <BanIcon />
            {t('profile.banConfirm', { name })}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}
