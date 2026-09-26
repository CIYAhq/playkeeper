import { useEffect, useState } from 'react'
import { DownloadIcon, ExternalLinkIcon } from 'lucide-react'
import { get } from '@/api/client'
import type { AddonDetails } from '@/api/types'
import { errorText, useWorkspace } from '@/api/workspace'
import { CopyButton } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Sheet, SheetDescription, SheetPanel, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n'
import { footerFor, sourceNames } from '@/lib/addons'
import { busyReason } from '@/lib/phase'
import { AddonIcon, detailsPath, useAddons, type VoiceAsk } from './state'

/** Where friends get the mod that lets them talk. */
const clientMod = 'https://modrinth.com/mod/simple-voice-chat'

/**
 * Installing voice chat: the extra UDP port Playkeeper opens, the provider's
 * firewall, and the mod friends add to talk. A bottom sheet on phones.
 */
export function VoiceChatDialog() {
  const a = useAddons()
  const phone = useIsPhone()
  const open = !!a.voice
  const onOpenChange = (o: boolean) => !o && a.closeVoice()
  return phone ? (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetPopup side="bottom" showCloseButton>
        {a.voice && <VoiceChatBody ask={a.voice} phone />}
      </SheetPopup>
    </Sheet>
  ) : (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[520px]" showCloseButton={false}>
        {a.voice && <VoiceChatBody ask={a.voice} phone={false} />}
      </DialogPopup>
    </Dialog>
  )
}

function VoiceChatBody({ ask, phone }: { ask: VoiceAsk; phone: boolean }) {
  const a = useAddons()
  const ws = useWorkspace()
  const [details, setDetails] = useState<AddonDetails>()
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const serverId = a.server.id
  const { key } = ask
  useEffect(() => {
    let stale = false
    get<AddonDetails>(detailsPath(serverId, key))
      .then((d) => !stale && setDetails(d))
      .catch((e: unknown) => !stale && setError(errorText(e)))
    return () => {
      stale = true
    }
  }, [serverId, key])

  const port = details?.ports?.find((p) => p.protocol === 'udp')?.port
  const f = details ? footerFor(details) : undefined
  const fingerprint = f?.kind === 'install' ? f.fingerprint : ask.fingerprint
  const blocked = busyReason(a.server) ?? (details && (f?.kind !== 'install' || !port) ? (details.planError?.message ?? details.notice?.message ?? t('voice.cantInstall')) : undefined)
  const sub = t('voice.sub', { name: details?.card.name ?? ask.name, source: sourceNames[key.source] })
  const restarts = a.server.phase === 'online' ? t('voice.restarts', { server: a.server.name }) : ''

  async function install() {
    setBusy(true)
    await a.install(key, details?.card.name ?? ask.name, fingerprint, true)
    setBusy(false)
  }

  const portBox = port ? (
    <div className="animate-fade rounded-2xl border border-border bg-muted/50 p-4">
      {phone ? (
        <>
          <p className="flex items-baseline gap-2">
            <span className="text-lg font-bold tracking-[-0.01em]">{t('voice.port', { port })}</span>
            <span className="text-[13px] text-muted-foreground">{t('voice.onePort')}</span>
          </p>
          <p className="mt-1 text-[15px] leading-5 text-muted-foreground">{t('voice.firewallPhone', { port })}</p>
        </>
      ) : (
        <div className="flex gap-5">
          <div className="w-24 shrink-0">
            <p className="text-lg leading-6 font-bold tracking-[-0.01em]">{t('voice.port', { port })}</p>
            <p className="text-xs text-muted-foreground">{t('voice.onePort')}</p>
          </div>
          <div className="min-w-0 flex-1">
            <p className="text-[13px] font-semibold">{t('voice.ownPort')}</p>
            <p className="mt-0.5 text-xs leading-[18px] text-muted-foreground">{t('voice.firewall', { machine: ws.machineName, port })}</p>
            <a href={t('onboarding.check.firewallUrl')} target="_blank" rel="noreferrer" className="mt-1.5 inline-flex items-center gap-1 text-xs font-medium text-success-strong hover:underline">
              {t('onboarding.check.firewallLink')}
              <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
            </a>
          </div>
        </div>
      )}
    </div>
  ) : error ? (
    <p className="text-[13px] text-destructive-foreground" role="alert">
      {error}
    </p>
  ) : (
    <div className="rounded-2xl border border-border p-4">
      <LoadingLabel />
      <Skeleton className="h-5 w-28" />
      <Skeleton className="mt-2 h-3.5 w-full" />
    </div>
  )

  const friends = (
    <div className={phone ? 'mt-4' : ''}>
      {!phone && <p className="text-[13px] font-semibold">{t('voice.friendsTitle')}</p>}
      <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-1">
        <p className={phone ? 'text-[15px] text-muted-foreground' : 'text-xs text-muted-foreground'}>{phone ? t('voice.friendsPhone') : t('voice.friends', { name: details?.card.name ?? ask.name })}</p>
        <CopyButton text={clientMod} label={t('voice.copyLink')} variant="ghost" size="sm" className="-ml-2 text-success-strong" />
      </div>
    </div>
  )

  const primary = (
    <Button size={phone ? 'touch' : 'default'} className={phone ? 'w-full' : undefined} onClick={() => void install()} loading={busy || (!details && !error)} disabledReason={blocked ?? (error ? error : undefined)}>
      <DownloadIcon />
      {t('voice.install')}
    </Button>
  )

  if (phone) {
    return (
      <>
        <div className="flex items-center gap-3 px-5 pt-5 pr-14">
          <AddonIcon url={details?.card.iconUrl} size={48} className="rounded-xl" />
          <div className="min-w-0">
            <SheetTitle className="text-xl leading-7 font-bold">{t('voice.titlePhone')}</SheetTitle>
            <SheetDescription className="text-[15px]">{sub}</SheetDescription>
          </div>
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
      <div className="flex items-center gap-3 px-6 pt-6">
        <AddonIcon url={details?.card.iconUrl} size={44} className="rounded-xl" />
        <div className="min-w-0">
          <DialogTitle className="text-xl leading-7 font-bold">{t('voice.title')}</DialogTitle>
          <DialogDescription className="text-[13px]">{sub}</DialogDescription>
        </div>
      </div>
      <DialogPanel className="flex flex-col gap-4 pt-4">
        {portBox}
        {friends}
      </DialogPanel>
      <DialogFooter variant="bare" className="border-t border-border pt-4 sm:items-center">
        <p className="mr-auto text-xs text-muted-foreground">{restarts}</p>
        <Button variant="ghost" onClick={() => a.closeVoice()}>
          {t('common.cancel')}
        </Button>
        {primary}
      </DialogFooter>
    </>
  )
}
