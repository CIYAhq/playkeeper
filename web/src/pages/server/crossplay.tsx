import { useState, type ReactNode } from 'react'
import { ExternalLinkIcon } from 'lucide-react'
import { get, post } from '@/api/client'
import type { Crossplay, ServerStatus } from '@/api/types'
import { errorText, serverApi, useServerMachine, useWorkspace } from '@/api/workspace'
import { CopyButton } from '@/components/app/bits'
import { SettingRow, useIsPhone } from '@/components/app/controls'
import { InlineSkeleton } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Sheet, SheetDescription, SheetPanel, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { bedrockConsolesUrl } from '@/lib/machines'
import { busyReason } from '@/lib/phase'
import { linkProps } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

function useCrossplay(server: ServerStatus) {
  const op = server.operation?.kind
  // Asked again as a switch starts and ends, and while one runs.
  return usePoll(() => get<Crossplay>(serverApi(server.id, '/crossplay')), op === 'crossplay_on' || op === 'crossplay_off' ? 4000 : 120_000, `${server.id}:${op ?? ''}:${server.config?.crossplayPort ?? 0}`)
}

/**
 * Settings' crossplay switch: Bedrock friends join through Geyser and
 * Floodgate, with a UDP port of their own. Turning it on or off asks first,
 * as it installs or removes two plugins and restarts a running server; once
 * on, where Bedrock friends join and what's left to do show under it.
 */
export function CrossplayRows({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const view = useCrossplay(s)
  const place = useServerMachine(s)
  const [asking, setAsking] = useState<'on' | 'off'>()
  const c = view.data
  const op = s.operation?.kind
  const switching = op === 'crossplay_on' || op === 'crossplay_off'
  const on = switching ? op === 'crossplay_on' : (c?.on ?? !!s.config?.crossplayPort)
  const locked = ws.stale ? t('reason.noAgent') : !c ? t('common.loading') : (busyReason(s) ?? (!c.on && !c.available ? c.notice?.message : undefined))
  const bedrock = place.bedrock
  const port = s.config?.crossplayPort ?? c?.port

  const toggle = (
    <Switch
      checked={on}
      onCheckedChange={(v) => setAsking(v ? 'on' : 'off')}
      disabled={!!locked}
      title={locked}
      aria-label={t('crossplay.label')}
      className={cn(switching && 'opacity-70')}
    />
  )
  const hint = !c && view.loading ? <InlineSkeleton className="w-64 max-w-full" /> : c && !c.on && !c.available ? c.notice?.message : t('crossplay.hint')

  // The rows below the switch open and close with it.
  const reveal = (children: ReactNode) => (
    <div className={cn('grid transition-[grid-template-rows,opacity] duration-(--motion-standard) ease-standard', on && !switching ? 'grid-rows-[1fr] opacity-100' : 'grid-rows-[0fr] opacity-0')} inert={!on || switching}>
      <div className="min-h-0 overflow-hidden">{children}</div>
    </div>
  )
  const where = bedrock?.host ? (
    <div className="flex min-w-0 items-center gap-2">
      <span className="min-w-0 truncate text-[15px] font-semibold tabular-nums max-sm:text-base">{bedrock.host}</span>
      <span className="shrink-0 text-[13px] text-muted-foreground tabular-nums">{t('crossplay.port', { port: bedrock.port })}</span>
      <CopyButton text={bedrock.host} variant="ghost" size="sm" className="-my-1 ml-auto shrink-0" label={t('common.copy')} toast={t('toast.copied')} />
    </div>
  ) : (
    <p className="text-[13px] text-muted-foreground">{bedrock?.reason ?? ''}</p>
  )
  const todo = (
    <ul className="flex flex-col gap-1 text-[13px] leading-[18px] text-muted-foreground">
      {port && place.name ? <li>{t('crossplay.firewall', { port })}</li> : null}
      <li>
        {t('crossplay.consoles')}{' '}
        <a href={bedrockConsolesUrl} target="_blank" rel="noreferrer" className="inline-flex items-center gap-0.5 font-medium text-success-strong hover:underline">
          {t('crossplay.learnMore')}
          <ExternalLinkIcon className="size-3" aria-hidden="true" />
        </a>
      </li>
      <li>
        {t('crossplay.names', { prefix: c?.prefix ?? '.' })}{' '}
        <a {...linkProps({ name: 'server', slug: s.slug, tab: 'players' })} className="font-medium text-success-strong hover:underline">
          {t('crossplay.namesLink')}
        </a>
      </li>
    </ul>
  )

  const dialog = <CrossplayDialog server={s} ask={asking} view={c} onClose={() => setAsking(undefined)} onDone={() => Promise.all([view.refresh(), ws.refresh()]).then(() => undefined)} />
  if (phone) {
    return (
      <>
        <SettingRow label={t('crossplay.label')} hint={hint} control={toggle} className="border-b-0" />
        {reveal(
          <>
            <SettingRow wide label={<span className="whitespace-nowrap">{t('crossplay.where')}</span>} control={where} className="border-t" />
            <SettingRow label={t('crossplay.before')} hint={todo} control={null} />
          </>,
        )}
        {dialog}
      </>
    )
  }
  return (
    <>
      <SettingRow label={t('crossplay.label')} hint={hint} control={toggle} className="border-b-0" />
      {reveal(
        <div className="grid grid-cols-[minmax(0,280px)_minmax(0,1fr)] gap-6 pb-3.5">
          <div className="min-w-0">
            <div className="text-[13px] font-semibold">{t('crossplay.where')}</div>
            <div className="mt-1">{where}</div>
          </div>
          <div className="min-w-0">
            <div className="text-[13px] font-semibold">{t('crossplay.before')}</div>
            <div className="mt-1">{todo}</div>
          </div>
        </div>,
      )}
      {dialog}
    </>
  )
}

/** Turning crossplay on or off, after saying what it does. A bottom sheet on phones. */
function CrossplayDialog({ server: s, ask, view, onClose, onDone }: { server: ServerStatus; ask?: 'on' | 'off'; view?: Crossplay; onClose: () => void; onDone: () => Promise<void> }) {
  const phone = useIsPhone()
  const open = !!ask
  const onOpenChange = (o: boolean) => !o && onClose()
  const body = ask && <CrossplayBody server={s} on={ask === 'on'} view={view} phone={phone} onClose={onClose} onDone={onDone} />
  return phone ? (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetPopup side="bottom" showCloseButton>
        {body}
      </SheetPopup>
    </Sheet>
  ) : (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[520px]" showCloseButton={false}>
        {body}
      </DialogPopup>
    </Dialog>
  )
}

function CrossplayBody({ server: s, on, view, phone, onClose, onDone }: { server: ServerStatus; on: boolean; view?: Crossplay; phone: boolean; onClose: () => void; onDone: () => Promise<void> }) {
  const place = useServerMachine(s)
  const [busy, setBusy] = useState(false)
  const port = s.config?.crossplayPort ?? view?.port
  const restarts =
    on && s.desired === 'sleeping'
      ? t('crossplay.wakes', { server: s.name })
      : s.phase !== 'online'
        ? ''
        : on && s.sleep?.enabled
          ? t('crossplay.restartsAwake', { server: s.name })
          : t('crossplay.restarts', { server: s.name })
  const blocked = busyReason(s) ?? (on && view && !view.available ? view.notice?.message : undefined)

  async function go() {
    setBusy(true)
    try {
      await post(serverApi(s.id, '/crossplay'), { on })
      onClose()
      await onDone()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  const title = on ? (phone ? t('crossplay.titlePhone') : t('crossplay.title')) : t('crossplay.offTitle')
  const sub = on ? t('crossplay.sub') : t('crossplay.offSub', { port: port ?? '' })
  const portBox =
    on && port ? (
      <div className="animate-fade rounded-2xl border border-border bg-muted/50 p-4">
        {phone ? (
          <>
            <p className="flex items-baseline gap-2">
              <span className="text-lg font-bold tracking-[-0.01em]">{t('voice.port', { port })}</span>
              <span className="text-[13px] text-muted-foreground">{t('voice.onePort')}</span>
            </p>
            <p className="mt-1 text-[15px] leading-5 text-muted-foreground">{place.name ? t('voice.firewallPhone', { port }) : t('voice.firewallHidden')}</p>
          </>
        ) : (
          <div className="flex gap-5">
            <div className="w-24 shrink-0">
              <p className="text-lg leading-6 font-bold tracking-[-0.01em]">{t('voice.port', { port })}</p>
              <p className="text-xs text-muted-foreground">{t('voice.onePort')}</p>
            </div>
            <div className="min-w-0 flex-1">
              <p className="text-[13px] font-semibold">{t('crossplay.ownPort')}</p>
              <p className="mt-0.5 text-xs leading-[18px] text-muted-foreground">{place.name ? t('voice.firewall', { machine: place.name, port }) : t('voice.firewallHidden')}</p>
              {place.name && (
                <a href={t('onboarding.check.firewallUrl')} target="_blank" rel="noreferrer" className="mt-1.5 inline-flex items-center gap-1 text-xs font-medium text-success-strong hover:underline">
                  {t('onboarding.check.firewallLink')}
                  <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
                </a>
              )}
            </div>
          </div>
        )}
      </div>
    ) : null
  const friends = on ? (
    <div className={phone ? 'mt-4' : ''}>
      {!phone && <p className="text-[13px] font-semibold">{t('crossplay.friendsTitle')}</p>}
      <p className={cn('mt-0.5', phone ? 'text-[15px] text-muted-foreground' : 'text-xs leading-[18px] text-muted-foreground')}>{t('crossplay.friends', { port: port ?? 19132 })}</p>
    </div>
  ) : null

  const primary = (
    <Button size={phone ? 'touch' : 'default'} variant={on ? 'default' : 'destructive'} className={phone ? 'w-full' : undefined} onClick={() => void go()} loading={busy} disabledReason={blocked}>
      {on ? t('crossplay.turnOn') : t('crossplay.turnOff')}
    </Button>
  )

  if (phone) {
    return (
      <>
        <div className="px-5 pt-5 pr-14">
          <SheetTitle className="text-xl leading-7 font-bold">{title}</SheetTitle>
          <SheetDescription className="text-[15px]">{sub}</SheetDescription>
        </div>
        <SheetPanel className="px-5 pt-4 pb-6">
          {portBox}
          {friends}
          <div className="mt-8 flex flex-col gap-2">
            {primary}
            {restarts && <p className="text-center text-[13px] text-muted-foreground">{restarts}</p>}
          </div>
        </SheetPanel>
      </>
    )
  }
  return (
    <>
      <div className="px-6 pt-6">
        <DialogTitle className="text-xl leading-7 font-bold">{title}</DialogTitle>
        <DialogDescription className="mt-1 text-[13px]">{sub}</DialogDescription>
      </div>
      {on && (
        <DialogPanel className="flex flex-col gap-4 pt-4">
          {portBox}
          {friends}
        </DialogPanel>
      )}
      <DialogFooter variant="bare" className={cn('border-t border-border pt-4 sm:items-center', !on && 'mt-5')}>
        <p className="mr-auto text-xs text-muted-foreground">{restarts}</p>
        <Button variant="ghost" onClick={onClose}>
          {t('common.cancel')}
        </Button>
        {primary}
      </DialogFooter>
    </>
  )
}

/**
 * The Join card's line for Bedrock players, while crossplay is on: the
 * address and port they type, and one quiet line on consoles.
 */
export function BedrockJoin({ server: s }: { server: ServerStatus }) {
  const place = useServerMachine(s)
  const b = place.bedrock
  if (!b) return null
  return (
    <div className="mt-3 border-t border-border pt-3">
      <div className="flex items-center gap-2">
        <span className="shrink-0 text-xs font-semibold text-muted-foreground max-sm:text-[13px]">{t('crossplay.bedrock')}</span>
        {b.host ? (
          <>
            <span className="min-w-0 truncate text-[15px] font-bold tabular-nums max-sm:text-base">{b.host}</span>
            <span className="shrink-0 text-[13px] text-muted-foreground tabular-nums">{t('crossplay.port', { port: b.port })}</span>
            <CopyButton text={b.host} variant="ghost" size="sm" className="-my-1 ml-auto shrink-0" label={t('common.copy')} toast={t('toast.copied')} />
          </>
        ) : (
          <span className="min-w-0 text-[13px] text-muted-foreground">{b.reason}</span>
        )}
      </div>
      <p className="mt-0.5 text-xs text-muted-foreground max-sm:text-[13px]">
        {t('crossplay.joinHint')}{' '}
        <a href={bedrockConsolesUrl} target="_blank" rel="noreferrer" className="inline-flex items-center gap-0.5 font-medium text-success-strong hover:underline">
          {t('crossplay.consolesLink')}
          <ExternalLinkIcon className="size-3" aria-hidden="true" />
        </a>
      </p>
    </div>
  )
}
