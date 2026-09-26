import { useMemo, useState, type FormEvent, type ReactNode } from 'react'
import { ChevronRightIcon, EllipsisIcon, KeyRoundIcon, PlusIcon, Trash2Icon } from 'lucide-react'
import { del, get, post } from '@/api/client'
import type { AgentActivity, ApiToken, MachineLinkInfo, NewToken, TokenRole } from '@/api/types'
import { errorText, useWorkspace } from '@/api/workspace'
import { Card, CardHint, CardTitle, CopyButton, SectionLabel } from '@/components/app/bits'
import { ChoiceSelect, useIsPhone, type Choice } from '@/components/app/controls'
import { LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { Label } from '@/components/ui/label'
import { Menu, MenuItem, MenuPopup, MenuTrigger } from '@/components/ui/menu'
import { Sheet, SheetHeader, SheetPanel, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { tokenRoles } from '@/lib/access'
import { formatDate, formatWhen, relativeTime } from '@/lib/format'
import { presenceProps, useListPresence } from '@/lib/presence'
import { agentPhrase, elideSecret, mcpAddress, mcpSnippet, runsOutText, tokenDays, tokenExpired, tokenRoleHint, tokenRoleText, tokenServersText, type TokenDays } from '@/lib/tokens'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

/** Settings › AI agents: the MCP address, the tokens and what agents did lately. */
export function AiAgentsSection() {
  const phone = useIsPhone()
  const link = usePoll(() => get<MachineLinkInfo>('/api/machines/link'), 60_000)
  const tokens = usePoll(() => get<ApiToken[]>('/api/tokens'), 30_000)
  const lately = usePoll(() => get<AgentActivity[]>('/api/tokens/activity'), 15_000)
  const [creating, setCreating] = useState(false)
  const [open, setOpen] = useState<ApiToken>()
  const [revoking, setRevoking] = useState<ApiToken>()
  const address = mcpAddress(link.data?.addresses, window.location.origin)
  const refresh = () => Promise.all([tokens.refresh(), lately.refresh()])
  const tokensError = tokens.error ? errorText(tokens.error) : undefined
  const latelyError = lately.error ? errorText(lately.error) : undefined

  const dialogs = (
    <>
      <NewTokenDialog open={creating} address={address} onOpenChange={setCreating} onCreated={() => void tokens.refresh()} />
      <RevokeDialog token={revoking} onClose={() => setRevoking(undefined)} onRevoked={() => void refresh()} />
      {phone && <TokenSheet token={open} onClose={() => setOpen(undefined)} onRevoke={(tk) => setRevoking(tk)} />}
    </>
  )

  if (phone) {
    return (
      <>
        <section className="rounded-3xl border border-border bg-white p-4" aria-label={t('ai.connectTitle')}>
          <p className="text-[13px] text-muted-foreground">{t('ai.address')}</p>
          <p className="mt-0.5 text-[17px] font-bold break-all">{address}</p>
          <CopyButton text={address} label={t('ai.copyAddress')} toast={t('ai.addressCopied')} size="touch" className="mt-3 w-full" />
        </section>
        <section aria-labelledby="ai-tokens-label">
          <SectionLabel className="px-4 pb-2">
            <span id="ai-tokens-label">{t('ai.tokens')}</span>
          </SectionLabel>
          <PhoneTokens tokens={tokens.data} error={tokensError} onOpen={setOpen} />
        </section>
        <section aria-labelledby="ai-lately-label">
          <SectionLabel className="px-4 pb-2">
            <span id="ai-lately-label">{t('ai.lately')}</span>
          </SectionLabel>
          <PhoneLately items={lately.data} error={latelyError} />
        </section>
        <div className="h-16" aria-hidden="true" />
        <div className="fixed inset-x-4 bottom-[calc(64px+env(safe-area-inset-bottom))] z-30">
          <Button size="touch" className="w-full shadow-popup" onClick={() => setCreating(true)}>
            <PlusIcon />
            {t('ai.newToken')}
          </Button>
        </div>
        {dialogs}
      </>
    )
  }

  return (
    <>
      <Card aria-labelledby="ai-connect-title">
        <CardTitle id="ai-connect-title">{t('ai.connectTitle')}</CardTitle>
        <CardHint>{t('ai.connectHint')}</CardHint>
        <div className="mt-4 flex items-center gap-3 rounded-2xl bg-muted px-4 py-3">
          <div className="min-w-0 flex-1">
            <p className="text-xs text-muted-foreground">{t('ai.address')}</p>
            <p className="truncate text-[15px] font-semibold">{address}</p>
          </div>
          <CopyButton text={address} toast={t('ai.addressCopied')} className="bg-white" />
        </div>
      </Card>
      <section aria-labelledby="ai-tokens-title" className="flex flex-col gap-3">
        <div className="flex items-end justify-between gap-3">
          <div>
            <h2 id="ai-tokens-title" className="text-[15px] font-semibold">
              {t('ai.tokens')}
            </h2>
            <p className="text-[13px] text-muted-foreground">{t('ai.tokensHint')}</p>
          </div>
          <Button onClick={() => setCreating(true)}>
            <PlusIcon />
            {t('ai.newToken')}
          </Button>
        </div>
        <TokenTable tokens={tokens.data} error={tokensError} onRevoke={setRevoking} />
      </section>
      <Card aria-labelledby="ai-lately-title">
        <CardTitle id="ai-lately-title">{t('ai.lately')}</CardTitle>
        <Lately items={lately.data} error={latelyError} />
      </Card>
      {dialogs}
    </>
  )
}

function lastUsed(tk: ApiToken): string {
  return tk.lastUsedAt ? t('ai.lastUsed', { time: relativeTime(tk.lastUsedAt) }) : t('ai.neverUsed')
}

const tokenKey = (tk: ApiToken) => tk.id
const latelyKey = (a: AgentActivity) => `${a.tokenId}:${a.tool}:${a.serverId ?? ''}:${a.at}`

function TokenTable({ tokens, error, onRevoke }: { tokens: ApiToken[] | undefined; error?: string; onRevoke: (tk: ApiToken) => void }) {
  const { servers } = useWorkspace()
  const rows = useListPresence(tokens, tokenKey)
  return (
    <div className="overflow-hidden rounded-2xl border border-border bg-white">
      <table className="w-full text-[13px]">
        <thead className="bg-muted text-left text-xs text-muted-foreground">
          <tr className="h-9">
            <th className="px-3 font-medium">{t('ai.col.name')}</th>
            <th className="px-3 font-medium">{t('ai.col.canDo')}</th>
            <th className="px-3 font-medium">{t('ai.col.servers')}</th>
            <th className="px-3 font-medium">{t('ai.col.runsOut')}</th>
            <th className="w-12 px-3">
              <span className="sr-only">{t('ai.col.actions')}</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {!tokens && !error && [0, 1].map((i) => <SkeletonRow key={i} first={i === 0} />)}
          {error && (
            <tr className="border-t border-border">
              <td colSpan={5} className="px-3 py-4 text-destructive-foreground">
                {error}
              </td>
            </tr>
          )}
          {tokens && rows.length === 0 && (
            <tr className="border-t border-border">
              <td colSpan={5} className="px-3 py-4 text-muted-foreground">
                {t('ai.noTokens')}
              </td>
            </tr>
          )}
          {rows.map(({ key, item: tk, state }) => {
            const expired = tokenExpired(tk)
            return (
              <tr key={key} {...presenceProps(state)} className={cn('h-[52px] border-t border-border', expired && 'text-muted-foreground')}>
                <td className="px-3 py-2">
                  <span className="block font-semibold">{tk.name}</span>
                  <span className="block text-xs text-muted-foreground">
                    {!tk.mine && (
                      <>
                        {t('ai.tokenOf', { account: tk.account })}
                        {t('common.dot')}
                      </>
                    )}
                    {lastUsed(tk)}
                  </span>
                </td>
                <td className="px-3">{tokenRoleText(tk.role)}</td>
                <td className="px-3 text-muted-foreground">{tokenServersText(tk, servers ?? [])}</td>
                <td className={cn('px-3 text-muted-foreground', expired && 'text-warning-foreground')} title={formatDate(tk.expiresAt)}>
                  {runsOutText(tk.expiresAt)}
                </td>
                <td className="px-2 text-right">
                  <Menu>
                    <MenuTrigger render={<Button variant="ghost" size="icon-sm" aria-label={t('ai.menuFor', { name: tk.name })} />}>
                      <EllipsisIcon />
                    </MenuTrigger>
                    <MenuPopup align="end">
                      <MenuItem variant="destructive" onClick={() => onRevoke(tk)}>
                        <Trash2Icon />
                        {t('ai.revoke')}
                      </MenuItem>
                    </MenuPopup>
                  </Menu>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function SkeletonRow({ first }: { first?: boolean }) {
  return (
    <tr className="h-[52px] border-t border-border">
      <td className="px-3">
        {first && <LoadingLabel />}
        <Skeleton className="h-3.5 w-36" />
        <Skeleton className="mt-1.5 h-3 w-24" />
      </td>
      <td className="px-3">
        <Skeleton className="h-3.5 w-28" />
      </td>
      <td className="px-3">
        <Skeleton className="h-3.5 w-20" />
      </td>
      <td className="px-3">
        <Skeleton className="h-3.5 w-16" />
      </td>
      <td />
    </tr>
  )
}

const latelyRow = 'grid min-h-10 grid-cols-[180px_minmax(0,1fr)_auto] items-center gap-4 border-t border-border py-2 text-[13px] first:border-t-0'

function Lately({ items, error }: { items: AgentActivity[] | undefined; error?: string }) {
  const rows = useListPresence(items?.slice(0, 8), latelyKey)
  if (error) return <p className="mt-2 text-[13px] text-destructive-foreground">{error}</p>
  if (!items) {
    return (
      <ul className="mt-2 flex flex-col">
        {[0, 1, 2].map((i) => (
          <li key={i} className={latelyRow}>
            {i === 0 && <LoadingLabel />}
            <Skeleton className="h-3.5 w-32" />
            <Skeleton className={cn('h-3.5', i % 2 ? 'w-1/2' : 'w-2/3')} />
            <Skeleton className="h-3 w-10" />
          </li>
        ))}
      </ul>
    )
  }
  if (rows.length === 0) return <p className="mt-2 text-[13px] text-muted-foreground">{t('ai.latelyEmpty')}</p>
  return (
    <ul className="mt-2 flex flex-col">
      {rows.map(({ key, item: a, state }) => (
        <li key={key} {...presenceProps(state)} className={latelyRow}>
          <span className="truncate font-semibold">{a.tokenName}</span>
          <span className="truncate">
            {agentPhrase(a)}
            {a.count > 1 && (
              <span className="text-muted-foreground">
                {t('common.dot')}
                {t('ai.times', { count: a.count })}
              </span>
            )}
          </span>
          <time dateTime={a.at} className="text-xs text-muted-foreground">
            {formatWhen(a.at)}
          </time>
        </li>
      ))}
    </ul>
  )
}

function PhoneGroup({ children, label }: { children: ReactNode; label?: string }) {
  return (
    <ul aria-label={label} className="overflow-hidden rounded-3xl border border-border bg-white [&>li]:border-b [&>li]:border-border [&>li:last-child]:border-b-0">
      {children}
    </ul>
  )
}

function PhoneTokens({ tokens, error, onOpen }: { tokens: ApiToken[] | undefined; error?: string; onOpen: (tk: ApiToken) => void }) {
  const rows = useListPresence(tokens, tokenKey)
  if (error) return <p className="px-4 text-[15px] text-destructive-foreground">{error}</p>
  if (!tokens) {
    return (
      <PhoneGroup>
        {[0, 1].map((i) => (
          <li key={i} className="flex min-h-16 items-center gap-3.5 px-4">
            {i === 0 && <LoadingLabel />}
            <Skeleton className="size-6 rounded-full" />
            <span className="flex-1">
              <Skeleton className="h-4 w-40" />
              <Skeleton className="mt-1.5 h-3 w-52" />
            </span>
          </li>
        ))}
      </PhoneGroup>
    )
  }
  if (rows.length === 0) return <p className="px-4 text-[15px] text-muted-foreground">{t('ai.noTokens')}</p>
  return (
    <PhoneGroup>
      {rows.map(({ key, item: tk, state }) => (
        <li key={key} {...presenceProps(state)}>
          <button type="button" onClick={() => onOpen(tk)} className="flex min-h-16 w-full items-center gap-3.5 px-4 py-2 text-left">
            <KeyRoundIcon className="size-[22px] shrink-0 text-muted-foreground" aria-hidden="true" />
            <span className="min-w-0 flex-1">
              <span className="block truncate text-base">{tk.name}</span>
              <span className="block truncate text-[13px] text-muted-foreground">
                {tokenRoleText(tk.role)}
                {t('common.dot')}
                {tokenExpired(tk) ? t('ai.ranOut') : tk.lastUsedAt ? t('ai.usedShort', { time: relativeTime(tk.lastUsedAt) }) : t('ai.neverUsedShort')}
              </span>
            </span>
            <ChevronRightIcon className="size-5 text-muted-foreground" aria-hidden="true" />
          </button>
        </li>
      ))}
    </PhoneGroup>
  )
}

function PhoneLately({ items, error }: { items: AgentActivity[] | undefined; error?: string }) {
  const rows = useListPresence(items?.slice(0, 6), latelyKey)
  if (error) return <p className="px-4 text-[15px] text-destructive-foreground">{error}</p>
  if (!items) {
    return (
      <PhoneGroup>
        <li className="px-4 py-3">
          <LoadingLabel />
          <Skeleton className="h-4 w-56" />
          <Skeleton className="mt-1.5 h-3 w-40" />
        </li>
      </PhoneGroup>
    )
  }
  if (rows.length === 0) return <p className="px-4 text-[15px] text-muted-foreground">{t('ai.latelyEmpty')}</p>
  return (
    <PhoneGroup>
      {rows.map(({ key, item: a, state }) => (
        <li key={key} {...presenceProps(state)} className="px-4 py-2.5">
          <span className="block text-base first-letter:uppercase">{agentPhrase(a)}</span>
          <span className="block text-[13px] text-muted-foreground">
            {a.tokenName}
            {t('common.dot')}
            {formatWhen(a.at)}
          </span>
        </li>
      ))}
    </PhoneGroup>
  )
}

function TokenSheet({ token, onClose, onRevoke }: { token: ApiToken | undefined; onClose: () => void; onRevoke: (tk: ApiToken) => void }) {
  const { servers } = useWorkspace()
  const [shown, setShown] = useState<ApiToken>()
  if (token && token !== shown) setShown(token)
  const tk = token ?? shown
  const facts: [string, string][] = tk
    ? [
        [t('ai.canDo'), tokenRoleText(tk.role)],
        [t('ai.servers'), tokenServersText(tk, servers ?? [])],
        [t('ai.lastUsedLabel'), tk.lastUsedAt ? relativeTime(tk.lastUsedAt) : t('ai.neverUsedShort')],
        [t('ai.runsOut'), `${runsOutText(tk.expiresAt)}${t('common.dot')}${formatDate(tk.expiresAt)}`],
        ...(tk.mine ? [] : ([[t('ai.account'), tk.account]] as [string, string][])),
      ]
    : []
  return (
    <Sheet open={!!token} onOpenChange={(o) => !o && onClose()}>
      <SheetPopup side="bottom" showCloseButton>
        <SheetHeader className="pb-2">
          <SheetTitle className="text-lg">{tk?.name}</SheetTitle>
        </SheetHeader>
        <SheetPanel className="flex flex-col gap-4 px-4 pb-4">
          <dl className="overflow-hidden rounded-2xl border border-border">
            {facts.map(([k, v]) => (
              <div key={k} className="flex min-h-12 items-center justify-between gap-3 border-b border-border px-4 last:border-b-0">
                <dt className="text-[15px] text-muted-foreground">{k}</dt>
                <dd className="text-right text-[15px]">{v}</dd>
              </div>
            ))}
          </dl>
          {tk && (
            <Button
              variant="destructive-outline"
              size="touch"
              className="w-full"
              onClick={() => {
                onClose()
                onRevoke(tk)
              }}
            >
              <Trash2Icon />
              {t('ai.revoke')}
            </Button>
          )}
        </SheetPanel>
      </SheetPopup>
    </Sheet>
  )
}

function RevokeDialog({ token, onClose, onRevoked }: { token: ApiToken | undefined; onClose: () => void; onRevoked: () => void }) {
  const [busy, setBusy] = useState(false)
  const [shown, setShown] = useState<ApiToken>()
  if (token && token !== shown) setShown(token)
  const tk = token ?? shown
  async function revoke() {
    if (!tk) return
    setBusy(true)
    try {
      await del(`/api/tokens/${tk.id}`)
      toastManager.add({ title: t('ai.revoked', { name: tk.name }), type: 'success' })
      onClose()
      onRevoked()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog open={!!token} onOpenChange={(o) => !o && onClose()}>
      <DialogPopup className="sm:max-w-[440px]">
        <DialogHeader>
          <DialogTitle className="text-lg font-bold">{t('ai.revokeTitle', { name: tk?.name ?? '' })}</DialogTitle>
          <DialogDescription>{t('ai.revokeBody')}</DialogDescription>
        </DialogHeader>
        <DialogFooter variant="bare" className="border-t border-border pt-4">
          <Button variant="ghost" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button variant="destructive" onClick={() => void revoke()} loading={busy}>
            <Trash2Icon />
            {t('ai.revoke')}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}

function expiryDate(days: number, now: number = Date.now()): string {
  return formatDate(new Date(now + days * 86_400_000).toISOString())
}

/** Makes a token, then shows it once with the MCP settings that use it. */
function NewTokenDialog({ open, address, onOpenChange, onCreated }: { open: boolean; address: string; onOpenChange: (open: boolean) => void; onCreated: () => void }) {
  const ws = useWorkspace()
  const roles = tokenRoles(ws.me)
  const servers = useMemo(() => ws.servers ?? [], [ws.servers])
  const [name, setName] = useState('')
  const [role, setRole] = useState<TokenRole>('viewer')
  const [scope, setScope] = useState<'all' | 'some'>('all')
  const [picked, setPicked] = useState<string[]>([])
  const [days, setDays] = useState<TokenDays>(60)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const [made, setMade] = useState<NewToken>()

  function reset() {
    setName('')
    setRole('viewer')
    setScope('all')
    setPicked([])
    setDays(60)
    setError(undefined)
    setMade(undefined)
  }

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (scope === 'some' && picked.length === 0) {
      setError(t('ai.pickOne'))
      return
    }
    setBusy(true)
    setError(undefined)
    try {
      const res = await post<NewToken>('/api/tokens', { name: name.trim(), role, allServers: scope === 'all', servers: scope === 'all' ? [] : picked, days })
      setMade(res)
      onCreated()
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  const roleChoices: Choice<TokenRole>[] = roles.map((r) => ({ value: r, label: tokenRoleText(r), hint: tokenRoleHint(r) }))
  const scopeChoices: Choice<'all' | 'some'>[] = [
    { value: 'all', label: t('ai.allServers') },
    { value: 'some', label: t('ai.someServers'), disabled: servers.length === 0, hint: servers.length === 0 ? t('ai.noServersYet') : undefined },
  ]
  const dayChoices: Choice<string>[] = tokenDays.map((d) => ({ value: String(d), label: t('ai.daysOption', { days: d, date: expiryDate(d) }) }))

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        onOpenChange(o)
        if (!o) window.setTimeout(reset, 250)
      }}
    >
      <DialogPopup className="sm:max-w-[520px]">
        {made ? (
          <CreatedToken made={made} address={address} onDone={() => onOpenChange(false)} />
        ) : (
          <form onSubmit={submit} className="contents">
            <DialogHeader>
              <DialogTitle className="text-lg font-bold">{t('ai.newToken')}</DialogTitle>
              <DialogDescription>{t('ai.newHint')}</DialogDescription>
            </DialogHeader>
            <DialogPanel className="flex flex-col gap-4">
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="token-name">{t('ai.name')}</Label>
                <Input id="token-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={t('ai.namePlaceholder')} maxLength={40} required autoFocus autoComplete="off" />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="token-role">{t('ai.canDo')}</Label>
                <ChoiceSelect id="token-role" value={role} onChange={setRole} options={roleChoices} label={t('ai.canDo')} className="w-full justify-between" />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="token-scope">{t('ai.servers')}</Label>
                <ChoiceSelect id="token-scope" value={scope} onChange={setScope} options={scopeChoices} label={t('ai.servers')} className="w-full justify-between" />
                {scope === 'some' && (
                  <fieldset className="mt-1 flex animate-enter flex-col gap-2 rounded-2xl border border-border p-3">
                    <legend className="sr-only">{t('ai.pickServers')}</legend>
                    {servers.map((s) => (
                      <Label key={s.id} className="flex items-center gap-2.5 text-sm font-normal">
                        <Checkbox checked={picked.includes(s.id)} onCheckedChange={(c) => setPicked((p) => (c ? [...p, s.id] : p.filter((x) => x !== s.id)))} />
                        {s.name}
                      </Label>
                    ))}
                  </fieldset>
                )}
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="token-days">{t('ai.runsOut')}</Label>
                <ChoiceSelect id="token-days" value={String(days)} onChange={(d) => setDays(Number(d) as TokenDays)} options={dayChoices} label={t('ai.runsOut')} className="w-full justify-between" />
              </div>
              {error && (
                <p className="text-[13px] text-destructive-foreground" role="alert">
                  {error}
                </p>
              )}
            </DialogPanel>
            <DialogFooter variant="bare" className="border-t border-border pt-4">
              <Button variant="ghost" onClick={() => onOpenChange(false)}>
                {t('common.cancel')}
              </Button>
              <Button type="submit" loading={busy} disabledReason={!name.trim() ? t('ai.nameFirst') : scope === 'some' && picked.length === 0 ? t('ai.pickOne') : undefined}>
                {t('ai.make')}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogPopup>
    </Dialog>
  )
}

function CreatedToken({ made, address, onDone }: { made: NewToken; address: string; onDone: () => void }) {
  const { servers } = useWorkspace()
  const tk = made.token
  const scope = tk.allServers ? t('ai.allServersLower') : tokenServersText(tk, servers ?? [])
  return (
    <div className="flex min-h-0 animate-fade flex-col">
      <DialogHeader>
        <DialogTitle className="text-lg font-bold">{t('ai.createdTitle', { name: tk.name })}</DialogTitle>
        <DialogDescription>{t('ai.createdMeta', { role: tokenRoleText(tk.role), servers: scope, date: formatDate(tk.expiresAt) })}</DialogDescription>
      </DialogHeader>
      <DialogPanel className="flex flex-col gap-4">
        <div>
          <p className="text-[13px]">
            <Label htmlFor="token-secret" className="inline font-semibold">
              {t('ai.copyNow')}
            </Label>{' '}
            <span className="text-muted-foreground">{t('ai.copyNowHint')}</span>
          </p>
          <div className="mt-1.5 flex gap-2">
            <InputGroup className="min-w-0 flex-1">
              <InputGroupAddon>
                <KeyRoundIcon aria-hidden="true" />
              </InputGroupAddon>
              <InputGroupInput id="token-secret" value={made.secret} readOnly className="font-mono text-[13px]" onFocus={(e) => e.currentTarget.select()} aria-label={t('ai.secretLabel')} />
            </InputGroup>
            <CopyButton text={made.secret} variant="default" size="default" />
          </div>
        </div>
        <div>
          <p className="text-[13px] font-semibold">{t('ai.snippetTitle')}</p>
          <div className="relative mt-1.5 rounded-2xl bg-console px-4 py-3.5">
            <pre tabIndex={0} className="overflow-x-auto rounded-sm font-mono text-xs leading-5 text-[#e8e8e0] outline-none focus-visible:ring-2 focus-visible:ring-ring">{mcpSnippet(address, elideSecret(made.secret))}</pre>
            <CopyButton text={mcpSnippet(address, made.secret)} aria-label={t('ai.copySnippet')} className="absolute top-3 right-3 bg-white" />
          </div>
        </div>
      </DialogPanel>
      <DialogFooter variant="bare" className="border-t border-border pt-4">
        <Button variant="outline" onClick={onDone}>
          {t('ai.done')}
        </Button>
      </DialogFooter>
    </div>
  )
}
