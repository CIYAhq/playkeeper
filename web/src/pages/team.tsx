import { useId, useState, type FormEvent, type ReactNode } from 'react'
import { CheckIcon, ChevronRightIcon, EllipsisIcon, LinkIcon, ServerIcon, ShieldCheckIcon, UnlinkIcon, UserMinusIcon, UserPlusIcon } from 'lucide-react'
import { del, get, post, put } from '@/api/client'
import type { CreatedTeamInvite, Grant, ProjectRole, Scope, TeamInvite, TeamMember, TeamResponse } from '@/api/types'
import { errorText, usePhoneServer, useWorkspace } from '@/api/workspace'
import { Card, CardTitle, CopyButton, Notice } from '@/components/app/bits'
import { confirmAdmin, ConfirmAdminNotice } from '@/components/app/confirm-admin'
import { CardGroup, ChoiceCard, ChoiceSelect, useIsPhone } from '@/components/app/controls'
import { Avatar } from '@/components/app/shell'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Menu, MenuItem, MenuPopup, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { Radio, RadioGroupPrimitive } from '@/components/ui/radio-group'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t, type MessageKey } from '@/i18n'
import { rich } from '@/i18n/rich'
import { can, projectRoles, roleHint, roleName, scopeText } from '@/lib/access'
import { relativeTime, timeUntil } from '@/lib/format'
import { usePending } from '@/lib/optimistic'
import { linkProps } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

type Editing = { kind: 'add' } | { kind: 'member'; member: TeamMember } | { kind: 'invite'; invite: TeamInvite }

const rank: Record<ProjectRole, number> = { viewer: 0, moderator: 1, admin: 2 }

/** The role table's rows: what a role lets someone do, and the least role that may. */
const abilities: { key: MessageKey; role: ProjectRole }[] = [
  { key: 'team.can.view', role: 'viewer' },
  { key: 'team.can.run', role: 'moderator' },
  { key: 'team.can.players', role: 'moderator' },
  { key: 'team.can.console', role: 'moderator' },
  { key: 'team.can.backup', role: 'moderator' },
  { key: 'team.can.restore', role: 'admin' },
  { key: 'team.can.settings', role: 'admin' },
  { key: 'team.can.servers', role: 'admin' },
  { key: 'team.can.team', role: 'admin' },
]

const avatarLetters = ['text-primary', 'text-info-foreground', 'text-warning-foreground']

/** Every avatar is the same pale green disc; the letter's colour tells people apart. */
function avatarTone(name: string): string {
  return cn('bg-primary/10 ring-0', avatarLetters[[...name].reduce((a, c) => a + c.charCodeAt(0), 0) % avatarLetters.length])
}

function inviteName(inv: TeamInvite): string {
  return inv.label || t('team.inviteLink')
}

/** Settings › Team: who can use this dashboard, with which role and servers. */
export function TeamSection() {
  const phone = useIsPhone()
  const team = usePoll(() => get<TeamResponse>('/api/team'), 15_000)
  const [grant, setGrant] = useState<{ editing: Editing; n: number }>()
  const [grantOpen, setGrantOpen] = useState(false)
  const [removing, setRemoving] = useState<TeamMember>()
  const data = team.data

  function edit(editing: Editing) {
    setGrant((g) => ({ editing, n: (g?.n ?? 0) + 1 }))
    setGrantOpen(true)
  }

  async function confirm(m: TeamMember) {
    await confirmAdmin(m)
    await team.refresh()
  }

  async function turnOff(inv: TeamInvite) {
    try {
      await del(`/api/team/invites/${inv.id}`)
      toastManager.add({ title: t('team.turnedOffToast'), type: 'success' })
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    }
    await team.refresh()
  }

  if (team.error) {
    return (
      <Notice
        tone="error"
        title={team.error.message}
        action={
          <Button variant="outline" size="sm" onClick={() => void team.refresh()}>
            {t('common.tryAgain')}
          </Button>
        }
      />
    )
  }
  if (!data) return <TeamSkeleton phone={phone} />

  const waiting = data.members.find((m) => m.canConfirm)
  const notice = waiting && <ConfirmAdminNotice member={waiting} onConfirmed={team.refresh} />
  const dialogs = (
    <>
      <Dialog open={grantOpen} onOpenChange={setGrantOpen}>
        <DialogPopup className="sm:max-w-[540px]">
          {grant && (
            <GrantForm
              key={grant.n}
              team={data}
              editing={grant.editing}
              onClose={() => setGrantOpen(false)}
              onChanged={team.refresh}
              onRemove={(m) => {
                setGrantOpen(false)
                setRemoving(m)
              }}
              onTurnOff={(inv) => {
                setGrantOpen(false)
                void turnOff(inv)
              }}
            />
          )}
        </DialogPopup>
      </Dialog>
      <RemoveDialog member={removing} onClose={() => setRemoving(undefined)} onRemoved={team.refresh} />
    </>
  )

  if (phone) return <PhoneTeam team={data} notice={notice} dialogs={dialogs} onEdit={edit} />

  return (
    <>
      {notice}
      <Card aria-labelledby="team-title">
        <div className="flex items-center justify-between gap-3">
          <CardTitle id="team-title">{t('team.title')}</CardTitle>
          <Button size="sm" onClick={() => edit({ kind: 'add' })}>
            <UserPlusIcon />
            {t('team.add')}
          </Button>
        </div>
        <ul className="mt-2 flex flex-col">
          {data.members.map((m) => (
            <MemberRow key={m.id} member={m} team={data} onChanged={team.refresh} onEdit={edit} onConfirm={confirm} onRemove={setRemoving} />
          ))}
          {data.invites.map((inv) => (
            <InviteRow key={inv.id} invite={inv} team={data} onChanged={team.refresh} onEdit={edit} onTurnOff={turnOff} />
          ))}
        </ul>
      </Card>
      <RoleTable />
      {dialogs}
    </>
  )
}

function TeamSkeleton({ phone }: { phone: boolean }) {
  const rows = [0, 1, 2].map((i) => (
    <div key={i} className="flex min-h-[60px] items-center gap-3 border-t border-border first:border-t-0">
      <Skeleton className="size-7 rounded-full max-sm:size-9" />
      <span className="flex flex-1 flex-col gap-1.5">
        <Skeleton className="h-3 w-24" />
        <Skeleton className="h-2.5 w-40" />
      </span>
    </div>
  ))
  if (phone) return <div className="rounded-3xl border border-border bg-white px-4" aria-busy="true">{rows}</div>
  return (
    <Card aria-busy="true">
      <Skeleton className="h-4 w-16" />
      <div className="mt-3">{rows}</div>
    </Card>
  )
}

/** The second line under a member's name. */
function memberLine(m: TeamMember): string {
  if (m.owner) return m.you ? t('team.youOwner') : t('team.owner')
  const parts = [t('team.added', { time: relativeTime(m.addedAt) }), t(m.twoFactor ? 'team.twoFactorOn' : 'team.twoFactorOff')]
  if (m.you) parts.unshift(t('team.you'))
  if (m.waiting) parts.push(t('team.waiting'))
  return parts.join(t('common.dot'))
}

/** A role only the owner may give (Admin, when an admin signs in); the role someone has now stays pickable. */
function ownerOnly(team: TeamResponse, role: ProjectRole, current: ProjectRole): boolean {
  return role !== current && !team.grantableRoles.includes(role)
}

function roleChoices(team: TeamResponse, current: ProjectRole) {
  return projectRoles.map((r) => ({ value: r, label: roleName(r), hint: roleHint(r), disabled: ownerOnly(team, r, current), reason: t('team.ownerOnly') }))
}

/** A row's role, showing a new pick at once while it saves; a refused pick goes back and says why. */
function useRole(saved: ProjectRole, save: (role: ProjectRole) => Promise<unknown>, reload: () => Promise<void>) {
  const pending = usePending<{ role: ProjectRole }>()
  async function pick(role: ProjectRole, done?: string) {
    try {
      await pending.run({ role }, () => save(role), reload)
      if (done) toastManager.add({ title: done, type: 'success' })
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    }
  }
  return { role: pending.changes.at(-1)?.role ?? saved, pick }
}

function MemberRow({ member: m, team, onChanged, onEdit, onConfirm, onRemove }: { member: TeamMember; team: TeamResponse; onChanged: () => Promise<void>; onEdit: (e: Editing) => void; onConfirm: (m: TeamMember) => Promise<void>; onRemove: (m: TeamMember) => void }) {
  const { role, pick } = useRole(m.role, (r) => put(`/api/team/members/${m.id}`, { role: r, servers: m.servers } satisfies Grant), onChanged)
  return (
    <li className="grid min-h-[60px] grid-cols-[minmax(0,1fr)_160px_160px_28px] items-center gap-3 border-t border-border py-2 first:border-t-0">
      <span className="flex min-w-0 items-center gap-3">
        <Avatar name={m.username} className={avatarTone(m.username)} />
        <span className="min-w-0">
          <span className="block truncate text-[13px] font-semibold">{m.username}</span>
          <span className={cn('block truncate text-xs', m.waiting ? 'text-warning-foreground' : 'text-muted-foreground')}>{memberLine(m)}</span>
        </span>
      </span>
      <span className="truncate text-[13px] text-muted-foreground">{scopeText(m.servers, team.servers)}</span>
      {m.owner ? (
        <span className="text-right text-[13px] font-semibold">{t('team.ownerAdmin')}</span>
      ) : m.canEdit ? (
        <ChoiceSelect value={role} onChange={(r) => void pick(r, t('team.roleToast', { name: m.username, role: roleName(r) }))} options={roleChoices(team, m.role)} label={t('team.roleFor', { name: m.username })} className="w-full min-w-0" />
      ) : (
        <span className="text-[13px]">{roleName(m.role)}</span>
      )}
      {(m.canEdit || m.canConfirm) && !m.owner ? (
        <Menu>
          <MenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label={t('team.menuFor', { name: m.username })} />}>
            <EllipsisIcon />
          </MenuTrigger>
          <MenuPopup align="end" className="min-w-52">
            {m.canConfirm && (
              <MenuItem onClick={() => void onConfirm(m)}>
                <ShieldCheckIcon />
                {t('team.confirm')}
              </MenuItem>
            )}
            {m.canEdit && (
              <>
                <MenuItem onClick={() => onEdit({ kind: 'member', member: m })}>
                  <ServerIcon />
                  {t('team.changeServers')}
                </MenuItem>
                <MenuSeparator />
                <MenuItem variant="destructive" onClick={() => onRemove(m)}>
                  <UserMinusIcon />
                  {t('team.remove')}
                </MenuItem>
              </>
            )}
          </MenuPopup>
        </Menu>
      ) : (
        <span />
      )}
    </li>
  )
}

function InviteRow({ invite: inv, team, onChanged, onEdit, onTurnOff }: { invite: TeamInvite; team: TeamResponse; onChanged: () => Promise<void>; onEdit: (e: Editing) => void; onTurnOff: (inv: TeamInvite) => Promise<void> }) {
  const name = inviteName(inv)
  const saved = inv.role ?? 'viewer'
  const { role, pick } = useRole(saved, (r) => put(`/api/team/invites/${inv.id}`, { role: r, servers: inv.servers ?? {} } satisfies Grant), onChanged)
  return (
    <li className="grid min-h-[60px] grid-cols-[minmax(0,1fr)_160px_160px_28px] items-center gap-3 border-t border-border py-2 first:border-t-0">
      <span className="flex min-w-0 items-center gap-3">
        <Avatar name={name} className={avatarTone(name)} />
        <span className="min-w-0">
          <span className="block truncate text-[13px] font-semibold">{name}</span>
          <span className="block truncate text-xs text-muted-foreground">{inv.expiresAt ? t('team.inviteLine', { when: timeUntil(inv.expiresAt) }) : t('team.inviteWaiting')}</span>
        </span>
      </span>
      <span className="truncate text-[13px] text-muted-foreground">{scopeText(inv.servers ?? {}, team.servers)}</span>
      {inv.canEdit ? (
        <ChoiceSelect value={role} onChange={(r) => void pick(r)} options={roleChoices(team, saved)} label={t('team.roleFor', { name })} className="w-full min-w-0" />
      ) : (
        <span className="text-[13px]">{roleName(role)}</span>
      )}
      {inv.canEdit ? (
        <Menu>
          <MenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label={t('team.menuFor', { name })} />}>
            <EllipsisIcon />
          </MenuTrigger>
          <MenuPopup align="end" className="min-w-52">
            <MenuItem onClick={() => onEdit({ kind: 'invite', invite: inv })}>
              <ServerIcon />
              {t('team.changeServers')}
            </MenuItem>
            <MenuSeparator />
            <MenuItem variant="destructive" onClick={() => void onTurnOff(inv)}>
              <UnlinkIcon />
              {t('team.turnOff')}
            </MenuItem>
          </MenuPopup>
        </Menu>
      ) : (
        <span />
      )}
    </li>
  )
}

function RoleTable() {
  const columns = [...projectRoles].reverse()
  return (
    <section aria-labelledby="roles-title" className="mt-1">
      <h2 id="roles-title" className="text-[15px] font-semibold">
        {t('team.rolesTitle')}
      </h2>
      <p className="mt-0.5 text-xs text-muted-foreground">{t('team.adminsTwoFactor')}</p>
      <div className="mt-3 overflow-hidden rounded-2xl border border-border bg-card">
        <table className="w-full text-[13px]">
          <thead className="bg-muted text-xs text-muted-foreground">
            <tr className="h-9">
              <th className="px-3 text-left font-medium">
                <span className="sr-only">{t('team.col.ability')}</span>
              </th>
              {columns.map((r) => (
                <th key={r} className="w-[104px] px-3 text-center font-medium">
                  {roleName(r)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {abilities.map((a) => (
              <tr key={a.key} className="h-10 border-t border-border">
                <td className="px-3">{t(a.key)}</td>
                {columns.map((r) => {
                  const yes = rank[r] >= rank[a.role]
                  return (
                    <td key={r} className="px-3 text-center">
                      {yes ? <CheckIcon className="mx-auto size-4 text-success-foreground" aria-hidden="true" /> : <span className="text-muted-foreground" aria-hidden="true">–</span>}
                      <span className="sr-only">{yes ? t('team.yesSr') : t('team.noSr')}</span>
                    </td>
                  )
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function PhoneTeam({ team, notice, dialogs, onEdit }: { team: TeamResponse; notice: ReactNode; dialogs: ReactNode; onEdit: (e: Editing) => void }) {
  const tabs = !!usePhoneServer()
  const rows = [
    ...team.members.map((m) => ({
      key: `m${m.id}`,
      name: m.username,
      line: m.owner ? t('team.phone.owner') : `${roleName(m.role)}${t('common.dot')}${m.waiting ? t('team.waiting') : scopeText(m.servers, team.servers)}`,
      edit: m.canEdit && !m.owner ? ({ kind: 'member', member: m } as const) : undefined,
    })),
    ...team.invites.map((inv) => ({
      key: `i${inv.id}`,
      name: inviteName(inv),
      line: `${roleName(inv.role ?? 'viewer')}${t('common.dot')}${t('team.phone.invite')}`,
      edit: inv.canEdit ? ({ kind: 'invite', invite: inv } as const) : undefined,
    })),
  ]
  return (
    <>
      {notice}
      <ul className="overflow-hidden rounded-3xl border border-border bg-white">
        {rows.map((r) => {
          const inner = (
            <>
              <Avatar name={r.name} className={cn('size-9 text-[15px]', avatarTone(r.name))} />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-[17px] leading-6">{r.name}</span>
                <span className="block truncate text-[13px] text-muted-foreground">{r.line}</span>
              </span>
              {r.edit && <ChevronRightIcon className="size-5 text-muted-foreground" aria-hidden="true" />}
            </>
          )
          return (
            <li key={r.key} className="border-b border-border last:border-b-0">
              {r.edit ? (
                <button type="button" onClick={() => r.edit && onEdit(r.edit)} className="flex min-h-[60px] w-full items-center gap-3 px-4 py-2 text-left active:bg-muted">
                  {inner}
                </button>
              ) : (
                <div className="flex min-h-[60px] items-center gap-3 px-4 py-2">{inner}</div>
              )}
            </li>
          )
        })}
      </ul>
      <p className="px-1 text-[13px] text-muted-foreground">{t('team.adminsTwoFactor')}</p>
      <div className="h-16" aria-hidden="true" />
      <div className={cn('fixed inset-x-4 z-30', tabs ? 'bottom-[calc(64px+env(safe-area-inset-bottom))]' : 'bottom-[max(env(safe-area-inset-bottom),16px)]')}>
        <Button size="touch" className="w-full" onClick={() => onEdit({ kind: 'add' })}>
          <UserPlusIcon />
          {t('team.add')}
        </Button>
      </div>
      {dialogs}
    </>
  )
}

/**
 * Add a team member, or change what a member or an unused link gives: a
 * role, and all servers or some. A new link is shown once, here.
 */
function GrantForm({ team, editing, onClose, onChanged, onRemove, onTurnOff }: { team: TeamResponse; editing: Editing; onClose: () => void; onChanged: () => Promise<void>; onRemove: (m: TeamMember) => void; onTurnOff: (inv: TeamInvite) => void }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const start: { role: ProjectRole; servers: Scope } =
    editing.kind === 'member'
      ? { role: editing.member.role, servers: editing.member.servers }
      : editing.kind === 'invite'
        ? { role: editing.invite.role ?? 'viewer', servers: editing.invite.servers ?? {} }
        : { role: team.grantableRoles.includes('moderator') ? 'moderator' : (team.grantableRoles[0] ?? 'viewer'), servers: team.servers[0] ? { servers: [team.servers[0].id] } : { all: true } }
  const allWhy = ws.me.access.servers.all ? undefined : t('team.allNotYours')
  const allWhyId = useId()
  const [role, setRole] = useState<ProjectRole>(start.role)
  const [mode, setMode] = useState<'all' | 'some'>(start.servers.all ? 'all' : 'some')
  const [picked, setPicked] = useState<string[]>(start.servers.servers ?? [])
  const [label, setLabel] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const [created, setCreated] = useState<{ url: string; friendly: boolean }>()
  const servers: Scope = mode === 'all' ? { all: true } : { servers: team.servers.filter((s) => picked.includes(s.id)).map((s) => s.id) }
  const valid = mode === 'all' || (servers.servers?.length ?? 0) > 0

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (!valid) return
    setBusy(true)
    setError(undefined)
    try {
      switch (editing.kind) {
        case 'add': {
          const res = await post<CreatedTeamInvite>('/api/team/invites', { role, servers, label: label.trim() || undefined } satisfies Grant)
          setCreated({ url: res.link.base + res.path, friendly: res.link.friendly })
          break
        }
        case 'member':
          await put(`/api/team/members/${editing.member.id}`, { role, servers } satisfies Grant)
          toastManager.add({ title: t('team.savedToast', { name: editing.member.username }), type: 'success' })
          onClose()
          break
        case 'invite':
          await put(`/api/team/invites/${editing.invite.id}`, { role, servers } satisfies Grant)
          toastManager.add({ title: t('team.linkSavedToast'), type: 'success' })
          onClose()
          break
        default: {
          const unreachable: never = editing
          return unreachable
        }
      }
      await onChanged()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  if (created) {
    const who = label.trim()
    return (
      <>
        <DialogHeader>
          <DialogTitle className="text-lg font-bold">{who ? t('team.createdTitle', { name: who }) : t('team.createdTitleUnnamed')}</DialogTitle>
          <DialogDescription className="text-[13px]">{t('team.createdBody')}</DialogDescription>
        </DialogHeader>
        <DialogPanel className="flex flex-col gap-3">
          <div className="flex gap-2">
            <Input value={created.url} readOnly aria-label={t('team.inviteLink')} className="flex-1 font-mono text-xs" onFocus={(e) => e.currentTarget.select()} />
            <CopyButton text={created.url} size={phone ? 'lg' : 'default'} toast={t('toast.copied')} />
          </div>
          {!created.friendly && can(ws.me, 'machine.manage') && ws.machine && (
            <p className="text-xs text-muted-foreground">
              {rich('invites.noAddress', {
                a: (chunk) => (
                  <a {...linkProps({ name: 'machine', id: ws.machine?.id ?? '' })} className="font-medium text-success-strong hover:underline">
                    {chunk}
                  </a>
                ),
              })}
            </p>
          )}
        </DialogPanel>
        <DialogFooter variant="bare" className="border-t border-border pt-4">
          <Button onClick={onClose}>{t('common.done')}</Button>
        </DialogFooter>
      </>
    )
  }

  const title =
    editing.kind === 'add'
      ? t('team.add')
      : editing.kind === 'member'
        ? t('team.editMember', { name: editing.member.username })
        : editing.invite.label
          ? t('team.editInvite', { name: editing.invite.label })
          : t('team.inviteLink')
  return (
    <form onSubmit={submit} className="contents" noValidate>
      <DialogHeader>
        <DialogTitle className="text-lg font-bold">{title}</DialogTitle>
      </DialogHeader>
      <DialogPanel className="flex flex-col gap-4">
        {editing.kind === 'add' && (
          <label className="flex flex-col gap-1.5">
            <span className="text-[13px] font-semibold">{t('invites.nameLabel')}</span>
            <Input value={label} onChange={(e) => setLabel(e.target.value)} maxLength={64} placeholder={t('team.namePlaceholder')} autoComplete="off" />
            <span className="text-xs text-muted-foreground">{t('invites.nameHint')}</span>
          </label>
        )}
        <fieldset className="flex flex-col gap-1.5">
          <legend className="mb-1.5 text-[13px] font-semibold">{t('team.role')}</legend>
          <CardGroup value={role} onChange={setRole} label={t('team.role')} className="flex flex-col gap-2">
            {projectRoles.map((r) => (
              <ChoiceCard key={r} value={r} radio="start" disabled={ownerOnly(team, r, start.role)} reason={t('team.ownerOnly')} className="gap-3 px-3.5 py-3">
                <span className="block text-[13px] font-semibold">{roleName(r)}</span>
                <span className="block text-xs text-muted-foreground">{roleHint(r)}</span>
              </ChoiceCard>
            ))}
          </CardGroup>
        </fieldset>
        <fieldset className="flex flex-col">
          <legend className="mb-2 text-[13px] font-semibold">{t('team.servers')}</legend>
          <RadioGroupPrimitive value={mode} onValueChange={(v) => setMode(v as 'all' | 'some')} aria-label={t('team.servers')} className="flex flex-col gap-3">
            <label className="flex items-center gap-2.5 text-[13px] max-sm:text-[15px]" title={allWhy}>
              <Radio value="all" disabled={!!allWhy} aria-describedby={allWhy ? allWhyId : undefined} />
              {t('scope.all')}
              {allWhy && (
                <span id={allWhyId} className="sr-only">
                  {allWhy}
                </span>
              )}
            </label>
            <label className="flex items-center gap-2.5 text-[13px] max-sm:text-[15px]">
              <Radio value="some" />
              {t('team.onlyThese')}
            </label>
          </RadioGroupPrimitive>
          {mode === 'some' && (
            <div className="mt-2.5 flex flex-wrap gap-x-5 gap-y-2 pl-6.5">
              {team.servers.map((s) => (
                <label key={s.id} className="flex items-center gap-2 text-[13px] max-sm:text-[15px]">
                  <Checkbox checked={picked.includes(s.id)} onCheckedChange={(c) => setPicked((p) => (c ? [...p, s.id] : p.filter((x) => x !== s.id)))} />
                  {s.name}
                </label>
              ))}
              {team.servers.length === 0 && <span className="text-xs text-muted-foreground">{t('team.noServers')}</span>}
            </div>
          )}
        </fieldset>
        {error && (
          <p className="text-[13px] text-destructive-foreground" role="alert">
            {error}
          </p>
        )}
      </DialogPanel>
      <DialogFooter variant="bare" className="items-center border-t border-border pt-4 sm:justify-between">
        {editing.kind === 'add' ? (
          <span className="text-xs text-muted-foreground max-sm:order-last max-sm:text-center">{t('team.linkRule')}</span>
        ) : phone && editing.kind === 'member' ? (
          <Button type="button" variant="destructive-outline" size="touch" onClick={() => onRemove(editing.member)}>
            <UserMinusIcon />
            {t('team.remove')}
          </Button>
        ) : phone && editing.kind === 'invite' ? (
          <Button type="button" variant="destructive-outline" size="touch" onClick={() => onTurnOff(editing.invite)}>
            <UnlinkIcon />
            {t('team.turnOff')}
          </Button>
        ) : (
          <span />
        )}
        <div className="flex gap-2 max-sm:flex-col-reverse">
          <Button type="button" variant="ghost" size={phone ? 'touch' : 'default'} onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" size={phone ? 'touch' : 'default'} loading={busy} disabledReason={valid ? undefined : t('team.pickServer')}>
            {editing.kind === 'add' ? <LinkIcon /> : null}
            {editing.kind === 'add' ? t('invites.create') : t('common.save')}
          </Button>
        </div>
      </DialogFooter>
    </form>
  )
}

function RemoveDialog({ member, onClose, onRemoved }: { member: TeamMember | undefined; onClose: () => void; onRemoved: () => Promise<void> }) {
  const [busy, setBusy] = useState(false)
  const [shown, setShown] = useState<TeamMember>()
  if (member && member !== shown) setShown(member)
  const m = member ?? shown
  async function remove() {
    if (!m) return
    setBusy(true)
    try {
      await del(`/api/team/members/${m.id}`)
      toastManager.add({ title: t('team.removedToast', { name: m.username }), type: 'success' })
      onClose()
      await onRemoved()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open={!!member} onOpenChange={(o) => !o && onClose()}>
      <DialogPopup className="sm:max-w-[440px]">
        <DialogHeader>
          <DialogTitle className="text-lg font-bold">{t('team.removeTitle', { name: m?.username ?? '' })}</DialogTitle>
          <DialogDescription className="text-[13px]">{t('team.removeBody')}</DialogDescription>
        </DialogHeader>
        <DialogFooter variant="bare" className="border-t border-border pt-4">
          <Button variant="ghost" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button variant="destructive" onClick={() => void remove()} loading={busy}>
            <UserMinusIcon />
            {t('team.removeConfirm', { name: m?.username ?? '' })}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}
