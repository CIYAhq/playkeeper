import { useEffect, useState, type FormEvent } from 'react'
import { EllipsisIcon, LinkIcon, PlusIcon, UnlinkIcon } from 'lucide-react'
import { del, get, post } from '@/api/client'
import type { Approval, Expiry, Invite, InvitesResponse, JoinRequestView, NewInvite, ServerStatus } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { copyText, CopyButton, PlayerFace } from '@/components/app/bits'
import { CardGroup, ChoiceCard, ChoiceSelect, useIsPhone } from '@/components/app/controls'
import { Button } from '@/components/ui/button'
import { Dialog, DialogFooter, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Menu, MenuItem, MenuPopup, MenuTrigger } from '@/components/ui/menu'
import { NumberField, NumberFieldDecrement, NumberFieldGroup, NumberFieldIncrement, NumberFieldInput } from '@/components/ui/number-field'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { rich } from '@/i18n/rich'
import { can } from '@/lib/access'
import { relativeTime, timeUntil } from '@/lib/format'
import { presenceProps, useListPresence } from '@/lib/presence'
import { linkProps } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

const expiryLabel: Record<Expiry, () => string> = {
  '1d': () => t('invites.expiry.1d'),
  '7d': () => t('invites.expiry.7d'),
  '30d': () => t('invites.expiry.30d'),
  until_turned_off: () => t('invites.expiry.off'),
}

function approvalLabel(a: Approval | undefined): string {
  return a === 'after_yes' ? t('invites.afterYes') : t('invites.rightAway')
}

/** The full link, and the shorter form the list shows: host, /join/ and the code's start. */
export function inviteLink(base: string, inv: Invite): { url: string; short: string } | undefined {
  if (!inv.path) return undefined
  const code = inv.path.replace(/^\/join\//, '')
  return { url: base + inv.path, short: `${base.replace(/^https:\/\//, '')}/join/${code.slice(0, 6)}…` }
}

function usedText(inv: Invite): string {
  return inv.maxUses > 0 ? t('invites.used', { uses: inv.uses, max: inv.maxUses }) : t('invites.usedNoLimit', { count: inv.uses })
}

function runsOut(inv: Invite): string {
  switch (inv.status) {
    case 'active':
      return inv.expiresAt ? timeUntil(inv.expiresAt) : t('invites.untilOff')
    case 'used_up':
      return t('invites.usedUp')
    case 'expired':
      return t('invites.expired')
    case 'revoked':
      return t('invites.turnedOff')
    default: {
      const unreachable: never = inv.status
      return unreachable
    }
  }
}

/** A server's invite links, for accounts that may let players in. */
export function useInvites(server: ServerStatus, enabled: boolean) {
  return usePoll(() => (enabled ? get<InvitesResponse>(serverApi(server.id, '/invites')) : Promise.resolve(undefined)), 15_000, `${server.id}:${enabled}`)
}

async function turnOff(server: ServerStatus, inv: Invite, after: () => Promise<void>) {
  try {
    await del(serverApi(server.id, `/invites/${inv.id}`))
    toastManager.add({ title: inv.status === 'active' ? t('invites.turnedOffToast') : t('invites.removedToast'), type: 'success' })
  } catch (e) {
    toastManager.add({ title: errorText(e), type: 'error' })
  }
  await after()
}

function InviteMenu({ server, invite, after, phone }: { server: ServerStatus; invite: Invite; after: () => Promise<void>; phone?: boolean }) {
  const name = invite.label || t('invites.unnamed')
  return (
    <Menu>
      <MenuTrigger render={<Button variant="ghost" size={phone ? 'icon-lg' : 'icon-sm'} aria-label={t('invites.menuFor', { name })} />}>
        <EllipsisIcon />
      </MenuTrigger>
      <MenuPopup align="end" className="min-w-44">
        <MenuItem variant="destructive" onClick={() => void turnOff(server, invite, after)}>
          <UnlinkIcon />
          {invite.status === 'active' ? t('invites.turnOff') : t('common.remove')}
        </MenuItem>
      </MenuPopup>
    </Menu>
  )
}

/** The hint under the links when this machine has no address name yet. Copying still works. */
function NoAddressHint({ className }: { className?: string }) {
  const ws = useWorkspace()
  if (!ws.machine || !can(ws.me, 'machine.manage')) return null
  const id = ws.machine.id
  return (
    <p className={cn('text-xs text-muted-foreground', className)}>
      {rich('invites.noAddress', {
        a: (chunk) => (
          <a {...linkProps({ name: 'machine', id })} className="font-medium text-success-strong hover:underline">
            {chunk}
          </a>
        ),
      })}
    </p>
  )
}

/** Invite links under the Players tab's cards: a table on desktop, a list on phones. */
export function InviteLinks({ server, data, onNew, onChanged, fresh }: { server: ServerStatus; data: InvitesResponse | undefined; onNew: () => void; onChanged: () => Promise<void>; fresh?: string }) {
  const phone = useIsPhone()
  const rows = useListPresence(data?.invites, (inv) => inv.id)
  if (!data) return null
  if (phone) {
    return (
      <section aria-labelledby="invite-links">
        <div className="flex items-center justify-between px-4">
          <h2 id="invite-links" className="section-label">
            {t('invites.title')}
          </h2>
          <Button variant="ghost" size="sm" onClick={onNew} className="-mr-2 h-11 text-success-strong">
            <PlusIcon />
            {t('invites.new')}
          </Button>
        </div>
        {rows.length > 0 && (
          <ul className="mt-2 overflow-hidden rounded-3xl border border-border bg-white">
            {rows.map(({ key, item: inv, state }) => {
              const link = inviteLink(data.link.base, inv)
              const active = inv.status === 'active'
              return (
                <li key={key} {...presenceProps(state)} className={cn('flex min-h-14 items-center gap-2 border-b border-border py-1.5 pr-1 pl-4 transition-colors duration-(--motion-slow) ease-standard last:border-b-0', fresh === inv.id && 'bg-selected')}>
                  <span className="min-w-0 flex-1">
                    <span className={cn('block truncate text-base', !active && 'text-muted-foreground')}>{inv.label || t('invites.unnamed')}</span>
                    <span className="block truncate text-[13px] text-muted-foreground">{`${usedText(inv)}${t('common.dot')}${runsOut(inv)}`}</span>
                  </span>
                  {link && <CopyButton text={link.url} size="lg" disabledReason={active ? undefined : t('invites.linkOff')} toast={t('invites.copiedToast')} />}
                  <InviteMenu server={server} invite={inv} after={onChanged} phone />
                </li>
              )
            })}
          </ul>
        )}
        {!data.link.friendly && <NoAddressHint className="mt-2 px-4" />}
      </section>
    )
  }
  return (
    <section aria-labelledby="invite-links" className="mt-2">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 id="invite-links" className="text-[15px] font-semibold">
          {t('invites.title')}
        </h2>
        <Button onClick={onNew}>
          <PlusIcon />
          {t('invites.new')}
        </Button>
      </div>
      <div className="mt-3 overflow-x-auto rounded-2xl border border-border" tabIndex={0} role="region" aria-labelledby="invite-links">
        <table className="w-full min-w-[720px] table-fixed text-[13px]">
          <thead className="bg-muted text-left text-xs text-muted-foreground">
            <tr className="h-9">
              <th className="pr-3 pl-4 font-medium">{t('invites.col.link')}</th>
              <th className="w-24 px-3 text-right font-medium">{t('invites.col.used')}</th>
              <th className="w-32 px-3 font-medium">{t('invites.col.runsOut')}</th>
              <th className="w-36 px-3 font-medium">{t('invites.col.approval')}</th>
              <th className="w-36 pr-4 pl-3 text-right font-medium">{t('invites.col.actions')}</th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 && (
              <tr>
                <td colSpan={5} className="px-4 py-4 text-muted-foreground">
                  {t('invites.none')}
                </td>
              </tr>
            )}
            {rows.map(({ key, item: inv, state }) => {
              const link = inviteLink(data.link.base, inv)
              const active = inv.status === 'active'
              return (
                <tr key={key} {...presenceProps(state)} className={cn('h-12 border-t border-border transition-colors duration-(--motion-slow) ease-standard', fresh === inv.id && 'bg-selected', !active && 'text-muted-foreground')}>
                  <td className="max-w-0 py-1.5 pr-3 pl-4">
                    <span className={cn('block truncate font-semibold', active && 'text-foreground')}>{inv.label || t('invites.unnamed')}</span>
                    <span className="block truncate text-xs text-muted-foreground">
                      {link ? `${link.short}${t('common.dot')}` : ''}
                      {t('invites.created', { time: relativeTime(inv.createdAt) })}
                    </span>
                  </td>
                  <td className={cn('px-3 text-right whitespace-nowrap tabular-nums', active && 'font-semibold')}>{usedText(inv)}</td>
                  <td className="px-3 whitespace-nowrap">{runsOut(inv)}</td>
                  <td className="px-3 whitespace-nowrap">{approvalLabel(inv.approval)}</td>
                  <td className="pr-4 pl-3">
                    <span className="flex items-center justify-end gap-1">
                      {link && <CopyButton text={link.url} disabledReason={active ? undefined : t('invites.linkOff')} toast={t('invites.copiedToast')} />}
                      <InviteMenu server={server} invite={inv} after={onChanged} />
                    </span>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      {!data.link.friendly && <NoAddressHint className="mt-2" />}
    </section>
  )
}

/** "New invite link for Survival": a name only you see, how long, how many, and when people get in. */
export function NewInviteDialog({ server, open, onOpenChange, data, onCreated }: { server: ServerStatus; open: boolean; onOpenChange: (open: boolean) => void; data: InvitesResponse | undefined; onCreated: (inv: Invite) => Promise<void> }) {
  const [n, setN] = useState(0)
  useEffect(() => {
    if (open) setN((x) => x + 1)
  }, [open])
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[520px]" showCloseButton={false}>
        <NewInviteForm key={n} server={server} data={data} onClose={() => onOpenChange(false)} onCreated={onCreated} />
      </DialogPopup>
    </Dialog>
  )
}

function NewInviteForm({ server, data, onClose, onCreated }: { server: ServerStatus; data: InvitesResponse | undefined; onClose: () => void; onCreated: (inv: Invite) => Promise<void> }) {
  const phone = useIsPhone()
  const [spec, setSpec] = useState<NewInvite>({ label: '', expiry: '7d', maxUses: 5, unlimited: false, approval: 'right_away' })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const expiries = data?.expiries ?? (['1d', '7d', '30d', 'until_turned_off'] as Expiry[])

  async function create(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const inv = await post<Invite>(serverApi(server.id, '/invites'), { ...spec, label: spec.label.trim() })
      const link = data && inviteLink(data.link.base, inv)
      const copied = link ? await copyText(link.url) : false
      toastManager.add({ title: copied ? t('invites.createdCopied') : t('invites.createdToast'), type: 'success' })
      onClose()
      await onCreated(inv)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={create} className="contents" noValidate>
      <DialogHeader>
        <DialogTitle className="text-lg font-bold">{t('invites.dialogTitle', { server: server.name })}</DialogTitle>
      </DialogHeader>
      <DialogPanel className="flex flex-col gap-4">
        <label className="flex flex-col gap-1.5">
          <span className="text-[13px] font-semibold">{t('invites.nameLabel')}</span>
          <Input value={spec.label} onChange={(e) => setSpec({ ...spec, label: e.target.value })} maxLength={64} placeholder={t('invites.namePlaceholder')} autoComplete="off" autoFocus={!phone} className="max-sm:h-11 max-sm:[&>input]:h-full" />
          <span className="text-xs text-muted-foreground">{t('invites.nameHint')}</span>
        </label>
        <div className="grid gap-4 sm:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)]">
          <div className="flex flex-col gap-1.5">
            <span className="text-[13px] font-semibold" id="invite-expiry">
              {t('invites.worksFor')}
            </span>
            <ChoiceSelect value={spec.expiry} onChange={(expiry) => setSpec({ ...spec, expiry })} options={expiries.map((x) => ({ value: x, label: expiryLabel[x]() }))} label={t('invites.worksFor')} className="w-full min-w-0" />
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-[13px] font-semibold">{t('invites.howMany')}</span>
            <NumberField value={spec.maxUses} onValueChange={(v) => v !== null && setSpec({ ...spec, maxUses: v })} min={1} max={100} step={1}>
              <NumberFieldGroup className="max-sm:h-11">
                <NumberFieldDecrement aria-label={t('common.decrease')} className="border-e border-input" />
                <span className="flex min-w-0 flex-1 items-center justify-center gap-1">
                  <NumberFieldInput className="w-9 grow-0 px-0 text-right" aria-label={t('invites.howMany')} />
                  <span className="text-[13px] text-muted-foreground" aria-hidden="true">
                    {t('invites.friendsUnit', { count: spec.maxUses })}
                  </span>
                </span>
                <NumberFieldIncrement aria-label={t('common.increase')} className="border-s border-input" />
              </NumberFieldGroup>
            </NumberField>
          </div>
        </div>
        <fieldset className="flex flex-col">
          <legend className="mb-1.5 text-[13px] font-semibold">{t('invites.letting')}</legend>
          <CardGroup value={spec.approval} onChange={(approval) => setSpec({ ...spec, approval })} label={t('invites.letting')} className="flex flex-col gap-2">
            <ChoiceCard value="right_away" radio="start" className="gap-3 px-3.5 py-3.5">
              <span className="block text-[13px] font-semibold">{t('invites.rightAway')}</span>
              <span className="block text-xs text-muted-foreground">{t('invites.rightAwayHint')}</span>
            </ChoiceCard>
            <ChoiceCard value="after_yes" radio="start" className="gap-3 px-3.5 py-3.5">
              <span className="block text-[13px] font-semibold">{t('invites.afterYes')}</span>
              <span className="block text-xs text-muted-foreground">{t('invites.afterYesHint')}</span>
            </ChoiceCard>
          </CardGroup>
        </fieldset>
        {error && (
          <p className="text-[13px] text-destructive-foreground" role="alert">
            {error}
          </p>
        )}
      </DialogPanel>
      <DialogFooter variant="bare" className="border-t border-border pt-4 sm:mx-6 sm:px-0">
        <Button type="button" variant="ghost" size={phone ? 'touch' : 'default'} onClick={onClose}>
          {t('common.cancel')}
        </Button>
        <Button type="submit" size={phone ? 'touch' : 'default'} loading={busy}>
          <LinkIcon />
          {t('invites.create')}
        </Button>
      </DialogFooter>
    </form>
  )
}

/** The one notice at the top of the Players tab: the oldest join request, with Say no and Let in. */
export function JoinRequestNotice({ server, onDecided }: { server: ServerStatus; onDecided: () => Promise<void> }) {
  const phone = useIsPhone()
  const requests = usePoll(() => get<JoinRequestView[]>(serverApi(server.id, '/join-requests')), 10_000, server.id)
  const [busy, setBusy] = useState<'approve' | 'decline'>()
  const first = requests.data?.[0]
  if (!first) return null
  const r = first.request

  async function decide(how: 'approve' | 'decline') {
    setBusy(how)
    try {
      await post(serverApi(server.id, `/join-requests/${r.id}/${how}`))
      toastManager.add({ title: how === 'approve' ? t('invites.letInToast', { name: r.playerName }) : t('invites.saidNoToast', { name: r.playerName }), type: 'success' })
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    }
    await Promise.all([requests.refresh(), onDecided()])
    setBusy(undefined)
  }

  return (
    <div className={cn('flex animate-enter items-center gap-3', phone ? 'flex-wrap rounded-3xl border border-border bg-white p-4' : 'pb-1')} role="status">
      <PlayerFace name={r.playerName} uuid={r.playerUuid} size={phone ? 36 : 32} />
      <p className="min-w-0 flex-1">
        <span className={cn('block truncate font-semibold', phone ? 'text-base' : 'text-[13px]')}>{first.notice.title.text}</span>
        <span className="block truncate text-xs text-muted-foreground">
          {first.notice.detail.text}
          {t('common.dot')}
          {relativeTime(r.createdAt)}
        </span>
      </p>
      <div className={cn('flex gap-1.5', phone && 'w-full [&>*]:flex-1')}>
        <Button variant="ghost" size={phone ? 'lg' : 'sm'} onClick={() => void decide('decline')} loading={busy === 'decline'} disabledReason={busy === 'approve' ? t('invites.lettingIn', { name: r.playerName }) : undefined}>
          {t('invites.sayNo')}
        </Button>
        <Button size={phone ? 'lg' : 'sm'} onClick={() => void decide('approve')} loading={busy === 'approve'} disabledReason={busy === 'decline' ? t('invites.sayingNo', { name: r.playerName }) : undefined}>
          {t('invites.letIn')}
        </Button>
      </div>
    </div>
  )
}
