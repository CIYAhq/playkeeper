import { useState, type ReactNode } from 'react'
import { CheckIcon, CopyIcon, DownloadIcon, ExternalLinkIcon, LinkIcon, Share2Icon, XIcon } from 'lucide-react'
import { packFileUrl, packLink, useMachineAddress, usePackShare } from '@/api/packs'
import type { PackShare, ServerStatus, ShareYourself } from '@/api/types'
import { errorText, useServerMachine, useWorkspace } from '@/api/workspace'
import { Emblem } from '@/components/app/art'
import { copyText, CopyButton, Notice } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { PackIcon } from '@/components/app/modpacks'
import { LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { Sheet, SheetDescription, SheetPanel, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { namedDashboard } from '@/lib/address'
import { fileHolds, shareText, yourselfLink, yourselfReason } from '@/lib/packs'
import { linkProps } from '@/lib/router'
import { iconURL } from '@/lib/servers'
import { cn } from '@/lib/utils'

type PackShareState = ReturnType<typeof usePackShare>

/**
 * The Mods tab's notice: what friends need to join, with Share with friends.
 * It shows nothing for a server whose type runs no mods.
 */
export function PackShareNotice({ server }: { server: ServerStatus }) {
  const phone = useIsPhone()
  const pack = usePackShare(server.id)
  const [open, setOpen] = useState(false)
  if (pack.unsupported) return null
  if (pack.loading) {
    return (
      <div className={cn('flex flex-col gap-2', phone ? 'rounded-2xl bg-card p-4 shadow-card' : 'px-1 py-0.5')}>
        <LoadingLabel />
        <Skeleton className="h-4 w-64 max-w-full" />
        <Skeleton className="h-3.5 w-48 max-w-full" />
      </div>
    )
  }
  if (!pack.share) {
    return (
      <Notice tone="error" title={t('packShare.error')} action={<Button variant="outline" size="sm" onClick={pack.reload}>{t('common.tryAgain')}</Button>} className="px-1">
        {pack.error}
      </Notice>
    )
  }
  const title = shareText(pack.share.share.notice)
  const openButton = (
    <Button variant="outline" size={phone ? 'touch' : 'default'} className={phone ? 'w-full' : undefined} onClick={() => setOpen(true)}>
      <Share2Icon />
      {t('packShare.open')}
    </Button>
  )
  return (
    <>
      {phone ? (
        <div className="animate-fade rounded-2xl bg-card p-4 shadow-card">
          <p className="text-[15px] leading-5 font-semibold">{title}</p>
          <p className="mt-1 text-[13px] text-muted-foreground">{t('packShare.noticeBodyPhone')}</p>
          <div className="mt-3">{openButton}</div>
        </div>
      ) : (
        <div className="flex animate-fade items-center gap-4 px-1">
          <div className="min-w-0 flex-1">
            <p className="text-[13px] leading-5 font-semibold">{title}</p>
            <p className="text-xs text-muted-foreground">{t('packShare.noticeBody')}</p>
          </div>
          {openButton}
        </div>
      )}
      <PackShareSheet server={server} pack={pack} open={open} onOpenChange={setOpen} />
    </>
  )
}

/**
 * Share with friends: before sharing, what the page shows and Share a link;
 * while shared, the link, what friends do, the mods they get themselves and
 * Stop sharing. Download the file works either way.
 */
export function PackShareSheet({ server, pack, open, onOpenChange }: { server: ServerStatus; pack: PackShareState; open: boolean; onOpenChange: (open: boolean) => void }) {
  const phone = useIsPhone()
  const [busy, setBusy] = useState<'on' | 'off'>()
  // The page is the dashboard's, whichever machine runs the server, so the
  // link takes the dashboard machine's name.
  const dashboard = useWorkspace().machine?.id
  const address = useMachineAddress(open ? dashboard : undefined)
  const ps = pack.share
  if (!ps) return null
  const named = namedDashboard(address, Date.now())
  const link = ps.public && ps.token ? packLink(ps.token, named) : ''
  const setPublic = async (on: boolean) => {
    setBusy(on ? 'on' : 'off')
    try {
      await pack.setPublic(on)
      if (!on) toastManager.add({ title: t('packShare.stopped'), type: 'success' })
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(undefined)
    }
  }
  const title = t('packShare.title')
  const holds = fileHolds(ps.share, ps.loaderName)
  const download = (
    <Button variant="ghost" size={phone ? 'touch' : 'default'} className={phone ? 'w-full' : undefined} render={<a href={packFileUrl(server.id)} download={ps.file} />}>
      <DownloadIcon />
      {t('packShare.download')}
    </Button>
  )
  const body = (
    <div key={link ? 'shared' : 'off'} className="flex animate-fade flex-col gap-4">
      {link ? <SharedBody ps={ps} server={server} link={link} phone={phone} /> : <OffBody server={server} />}
      {address && !named && dashboard && <NameFirst machineId={dashboard} />}
    </div>
  )

  if (phone) {
    return (
      <Sheet open={open} onOpenChange={onOpenChange}>
        <SheetPopup side="bottom" variant="inset" showCloseButton closeProps={{ className: 'absolute end-3 top-[21px]' }}>
          <SheetPanel className="flex flex-col gap-4 px-5 pt-3">
            <div className="pr-8">
              <SheetTitle className="text-xl font-bold">{title}</SheetTitle>
              <SheetDescription className={cn('mt-0.5 text-[13px]', link && 'sr-only')}>{holds}</SheetDescription>
            </div>
            {body}
          </SheetPanel>
          <div className="flex flex-col gap-1 px-5 pt-4 pb-5">
            {link ? (
              <>
                <CopyButton text={link} variant="default" size="touch" className="w-full" label={t('packShare.copyLink')} />
                <Button variant="ghost" size="touch" className="w-full" loading={busy === 'off'} onClick={() => void setPublic(false)}>
                  {t('packShare.stop')}
                </Button>
              </>
            ) : (
              <>
                <Button size="touch" className="w-full" loading={busy === 'on'} onClick={() => void setPublic(true)}>
                  <LinkIcon />
                  {t('packShare.share')}
                </Button>
                {download}
              </>
            )}
          </div>
        </SheetPopup>
      </Sheet>
    )
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[560px]" showCloseButton={false}>
        <div className="flex items-center gap-3.5 px-6 pt-6 pb-1">
          {server.machineId && server.config?.modpack ? <PackIcon machineId={server.machineId} url={server.config.modpack.iconUrl} size={44} /> : <Emblem size={44} icon={iconURL(server)} name={server.name} />}
          <div className="min-w-0">
            <DialogTitle className="text-xl leading-7 font-bold">{title}</DialogTitle>
            <DialogDescription className="text-[13px]">{holds}</DialogDescription>
          </div>
        </div>
        <DialogPanel className="flex flex-col gap-4 pt-4">{body}</DialogPanel>
        <DialogFooter variant="bare" className="mx-6 items-center border-t border-border px-0 pt-4 sm:justify-between">
          {link ? (
            <Button variant="ghost" loading={busy === 'off'} onClick={() => void setPublic(false)}>
              <XIcon />
              {t('packShare.stop')}
            </Button>
          ) : (
            download
          )}
          <div className="flex gap-2">
            {link ? (
              <>
                {download}
                <Button variant="outline" onClick={() => onOpenChange(false)}>
                  {t('common.done')}
                </Button>
              </>
            ) : (
              <>
                <Button variant="ghost" onClick={() => onOpenChange(false)}>
                  {t('common.cancel')}
                </Button>
                <Button loading={busy === 'on'} onClick={() => void setPublic(true)}>
                  <LinkIcon />
                  {t('packShare.share')}
                </Button>
              </>
            )}
          </div>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}

function OffBody({ server }: { server: ServerStatus }) {
  return (
    <div>
      <h3 className="text-[13px] font-semibold max-sm:text-[15px]">{t('packShare.offTitle')}</h3>
      <p className="mt-0.5 text-xs text-muted-foreground max-sm:text-[13px]">{t('packShare.offBody', { server: server.name })}</p>
    </div>
  )
}

function SharedBody({ ps, server, link, phone }: { ps: PackShare; server: ServerStatus; link: string; phone: boolean }) {
  const address = useServerMachine(server).join.address
  const steps = [t('share.step.open_link'), t('share.step.import'), address ? t('share.step.play_join', { address }) : t('share.step.play')]
  const yourself = ps.share.yourself ?? []
  return (
    <>
      <div>
        {phone ? (
          <div className="flex items-center gap-2 rounded-xl bg-muted py-1.5 pr-1.5 pl-3.5">
            <span className="min-w-0 flex-1 truncate text-[15px]">{link.replace(/^https?:\/\//, '')}</span>
            <CopyIconButton text={link} label={t('packShare.copyLink')} />
          </div>
        ) : (
          <div className="flex items-center gap-2">
            <InputGroup className="flex-1">
              <InputGroupAddon>
                <LinkIcon aria-hidden="true" />
              </InputGroupAddon>
              <InputGroupInput value={link} readOnly aria-label={t('packShare.link')} onFocus={(e) => e.currentTarget.select()} />
            </InputGroup>
            <CopyButton text={link} variant="default" label={t('packShare.copyLink')} />
          </div>
        )}
        <p className="mt-1.5 text-xs text-muted-foreground max-sm:text-[13px]">{phone ? t('packShare.linkHintPhone') : t('packShare.linkHint')}</p>
      </div>
      <Section title={t('packShare.steps')}>
        <ol className="flex list-decimal flex-col gap-1 pl-5 text-[13px] leading-5 max-sm:gap-1.5 max-sm:text-sm">
          {steps.map((s) => (
            <li key={s} className="pl-1">
              {s}
            </li>
          ))}
        </ol>
      </Section>
      {yourself.length > 0 && (
        <Section title={t('packShare.yourself')}>
          <ul className="divide-y divide-border overflow-hidden rounded-xl border border-border">
            {yourself.map((y) => (
              <li key={y.path} className="flex items-center gap-3 px-3 py-2.5 max-sm:py-3">
                <div className="min-w-0 flex-1">
                  <div className="text-[13px] font-semibold max-sm:text-[15px] max-sm:font-medium">{y.name}</div>
                  <div className="text-xs text-muted-foreground max-sm:text-[13px]">{yourselfReason(y, 'dialog')}</div>
                </div>
                {y.page && <YourselfLink y={y} phone={phone} />}
              </li>
            ))}
          </ul>
        </Section>
      )}
    </>
  )
}

/** Without a name the link carries the dashboard's IP: suggest giving its machine one first; sharing works either way. */
function NameFirst({ machineId }: { machineId: string }) {
  return (
    <Notice
      title={t('packShare.nameFirst')}
      action={
        <Button variant="outline" size="sm" render={<a {...linkProps({ name: 'machine-settings', id: machineId })} />}>
          {t('machine.settings')}
        </Button>
      }
    >
      {t('packShare.nameFirstBody')}
    </Notice>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-[13px] font-semibold max-sm:text-[15px]">{title}</h3>
      {children}
    </section>
  )
}

function YourselfLink({ y, phone }: { y: ShareYourself; phone: boolean }) {
  const label = yourselfLink(y)
  const aria = t('common.external', { label: t('packs.linkFor', { link: label, name: y.name }) })
  if (phone) {
    return (
      <Button variant="ghost" size="icon-lg" aria-label={aria} render={<a href={y.page} target="_blank" rel="noreferrer noopener" />}>
        <ExternalLinkIcon />
      </Button>
    )
  }
  return (
    <a href={y.page} target="_blank" rel="noreferrer noopener" aria-label={aria} className="inline-flex shrink-0 items-center gap-1 text-xs font-medium text-success-strong hover:underline">
      {label}
      <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
    </a>
  )
}

/** Copy with only an icon, for phones. */
export function CopyIconButton({ text, label, variant = 'ghost', size = 'icon-lg', className }: { text: string; label: string; variant?: 'ghost' | 'outline'; size?: 'icon-lg' | 'icon-xl'; className?: string }) {
  const [done, setDone] = useState(false)
  async function copy() {
    if (!(await copyText(text))) {
      toastManager.add({ title: t('toast.copyFailed'), type: 'error' })
      return
    }
    setDone(true)
    window.setTimeout(() => setDone(false), 1800)
  }
  return (
    <Button variant={variant} size={size} className={className} onClick={() => void copy()} aria-label={done ? t('common.copied') : label}>
      {done ? <CheckIcon className="animate-fade" /> : <CopyIcon />}
    </Button>
  )
}
