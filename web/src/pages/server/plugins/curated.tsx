import { useEffect, useState } from 'react'
import { ChevronRightIcon, DownloadIcon } from 'lucide-react'
import { get } from '@/api/client'
import type { AddonCard, AddonDetails, CuratedAddon, CuratedAddons } from '@/api/types'
import { serverApi } from '@/api/workspace'
import { Marker, SectionLabel } from '@/components/app/bits'
import { Button } from '@/components/ui/button'
import { t, type MessageKey } from '@/i18n'
import { alsoInstalls, footerFor, keyFrom, sourceNames } from '@/lib/addons'
import { busyReason } from '@/lib/phase'
import { softwareLabel } from '@/lib/servers'
import { AddonIcon, detailsPath, useAddons } from './state'

/** What each pick does, and one practical note. */
const copy: Record<string, [MessageKey, MessageKey]> = {
  'voice-chat': ['curated.voice-chat.does', 'curated.voice-chat.note'],
  rollback: ['curated.rollback.does', 'curated.rollback.note'],
  pregenerate: ['curated.pregenerate.does', 'curated.pregenerate.note'],
  'newer-clients': ['curated.newer-clients.does', 'curated.newer-clients.note'],
  essentials: ['curated.essentials.does', 'curated.essentials.note'],
  permissions: ['curated.permissions.does', 'curated.permissions.note'],
  'lag-finder': ['curated.lag-finder.does', 'curated.lag-finder.note'],
}

/** How many picks the library shows before a search. */
const shown = 4

function lines(p: CuratedAddon): [string, string] {
  const c = copy[p.id]
  return c ? [t(c[0]), t(c[1])] : [p.card.summary, '']
}

/**
 * "Picked by Playkeeper": the curated add-ons that fit the server, before a
 * search. Nothing shows when none fit or the list can't be had.
 */
export function CuratedPicks({ phone, installed }: { phone: boolean; installed: (c: AddonCard) => boolean }) {
  const a = useAddons()
  const [picks, setPicks] = useState<CuratedAddon[]>()
  const serverId = a.server.id
  useEffect(() => {
    let stale = false
    get<CuratedAddons>(serverApi(serverId, '/addons/curated'))
      .then((r) => !stale && setPicks(r.picks.slice(0, shown)))
      .catch(() => !stale && setPicks([]))
    return () => {
      stale = true
    }
  }, [serverId])
  if (!picks?.length) return null

  if (phone) {
    return (
      <section className="animate-fade" aria-labelledby="curated-title">
        <SectionLabel className="px-4 pb-2">
          <span id="curated-title">{t('curated.title')}</span>
        </SectionLabel>
        <ul className="overflow-hidden rounded-3xl border border-border bg-white">
          {picks.map((p) => (
            <li key={p.id} className="border-b border-border last:border-b-0">
              <button type="button" onClick={() => a.openDetail({ key: keyFrom(p.card) })} className="flex min-h-16 w-full items-center gap-3 px-3 py-2 text-left active:bg-accent/60">
                <AddonIcon url={p.card.iconUrl} />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-base leading-5">{p.card.name}</span>
                  <span className="line-clamp-2 text-[13px] leading-[18px] text-muted-foreground">{lines(p)[0]}</span>
                </span>
                {installed(p.card) ? (
                  <Marker tone="green" className="text-[13px]">
                    {t('addons.installed')}
                  </Marker>
                ) : (
                  <ChevronRightIcon className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
                )}
              </button>
            </li>
          ))}
        </ul>
      </section>
    )
  }
  return (
    <section className="flex animate-fade flex-col gap-3" aria-labelledby="curated-title">
      <h3 className="flex flex-wrap items-baseline gap-x-2">
        <span id="curated-title" className="text-[15px] font-semibold">
          {t('curated.title')}
        </span>
        <span className="text-[13px] text-muted-foreground">{t('curated.for', { software: softwareLabel(a.server) })}</span>
      </h3>
      <ul className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4">
        {picks.map((p) => (
          <PickCard key={p.id} pick={p} installed={installed(p.card)} />
        ))}
      </ul>
    </section>
  )
}

function PickCard({ pick: p, installed }: { pick: CuratedAddon; installed: boolean }) {
  const a = useAddons()
  const [busy, setBusy] = useState(false)
  const key = keyFrom(p.card)
  const [does, note] = lines(p)
  // One file installs straight away (voice chat asks about its port first);
  // anything more opens the detail sheet to confirm.
  async function install() {
    setBusy(true)
    try {
      const d = await get<AddonDetails>(detailsPath(a.server.id, key))
      const f = footerFor(d)
      if (f.kind === 'install' && alsoInstalls(d).length === 0 && (d.plan?.warnings.length ?? 0) === 0) await a.install(key, d.card.name, f.fingerprint)
      else a.openDetail({ key, details: d })
    } catch {
      a.openDetail({ key })
    } finally {
      setBusy(false)
    }
  }
  return (
    <li className="flex min-h-[152px] flex-col rounded-2xl border border-border bg-card p-4 shadow-card">
      <button type="button" onClick={() => a.openDetail({ key })} className="-m-1 flex min-w-0 items-center gap-3 rounded-lg p-1 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring">
        <AddonIcon url={p.card.iconUrl} />
        <span className="min-w-0">
          <span className="block truncate text-[15px] leading-5 font-semibold">{p.card.name}</span>
          <span className="block truncate text-xs text-muted-foreground">{sourceNames[key.source]}</span>
        </span>
      </button>
      <p className="mt-3 text-[13px] leading-[18px] text-foreground/80">{does}</p>
      {note && <p className="mt-1.5 text-xs leading-4 text-muted-foreground">{note}</p>}
      <div className="mt-auto flex justify-end pt-3">
        {installed ? (
          <Marker tone="green" className="text-[13px]">
            {t('addons.installed')}
          </Marker>
        ) : (
          <Button variant="outline" size="sm" onClick={() => void install()} loading={busy} disabledReason={busyReason(a.server)} aria-label={t('addons.install', { name: p.card.name })}>
            <DownloadIcon />
            {t('addons.installShort')}
          </Button>
        )}
      </div>
    </li>
  )
}
