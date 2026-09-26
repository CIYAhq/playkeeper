import { useState } from 'react'
import { ChevronDownIcon, ChevronRightIcon } from 'lucide-react'
import { usePackShare } from '@/api/packs'
import type { ServerModpack, ServerStatus } from '@/api/types'
import { useWorkspace } from '@/api/workspace'
import { Marker, SectionLabel } from '@/components/app/bits'
import { PackIcon } from '@/components/app/modpacks'
import { Button } from '@/components/ui/button'
import { Sheet, SheetDescription, SheetPanel, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { t } from '@/i18n'
import type { PackFile } from '@/lib/addons'
import { shareText } from '@/lib/packs'
import { cn } from '@/lib/utils'

/**
 * The Mods tab's "From the modpack": the pack as one row, with its mods
 * behind Show all (a sheet on phones). The pack keeps these files, so they
 * have no actions of their own.
 */
export function PackModsSection({ server, pack, files, folder, phone }: { server: ServerStatus; pack: ServerModpack; files: PackFile[]; folder: string; phone: boolean }) {
  const ws = useWorkspace()
  const share = usePackShare(server.id).share?.share
  const [open, setOpen] = useState(false)
  const count = pack.mods || files.length
  const need = share?.pack ? shareText(share.pack.label) : ''
  const labels = new Map<string, string>()
  for (const m of share?.mods ?? []) if (m.from === 'pack') labels.set(m.path, shareText(m.label))
  const labelOf = (f: PackFile) => labels.get(`${folder}/${f.fileName}`) ?? ''
  const icon = <PackIcon machineId={ws.machine?.id ?? ''} url={pack.iconUrl} size={40} className="rounded-[10px]" />
  const line = t('packMods.line', { count, version: pack.versionNumber })

  if (phone) {
    return (
      <section>
        <SectionLabel className="px-4 pb-2">{t('packMods.fromPack')}</SectionLabel>
        <ul className="overflow-hidden rounded-3xl border border-border bg-white">
          <li>
            <button type="button" onClick={() => setOpen(true)} className="flex min-h-14 w-full items-center gap-3 px-3 py-2 text-left active:bg-accent/60">
              {icon}
              <span className="min-w-0 flex-1">
                <span className="block truncate text-base leading-5">{pack.name}</span>
                <span className="block truncate text-[13px] text-muted-foreground">{need ? [t('modpacks.mods', { count }), need].join(t('common.dot')) : t('modpacks.mods', { count })}</span>
              </span>
              <ChevronRightIcon className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
            </button>
          </li>
        </ul>
        <Sheet open={open} onOpenChange={setOpen}>
          <SheetPopup side="bottom" showCloseButton>
            <div className="px-5 pt-5 pr-14">
              <SheetTitle className="text-lg font-bold">{pack.name}</SheetTitle>
              <SheetDescription className="text-[13px]">{line}</SheetDescription>
            </div>
            <SheetPanel className="px-4 pt-3 pb-6">
              <ul className="overflow-hidden rounded-2xl border border-border bg-white">
                {files.map((f) => (
                  <li key={f.fileName} className="flex min-h-14 items-center gap-3 border-b border-border px-4 py-2 last:border-b-0">
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-[15px] leading-5">{f.name}</span>
                      <span className="block truncate text-[13px] text-muted-foreground">{[f.version, labelOf(f)].filter(Boolean).join(t('common.dot'))}</span>
                    </span>
                  </li>
                ))}
              </ul>
            </SheetPanel>
          </SheetPopup>
        </Sheet>
      </section>
    )
  }

  return (
    <section className="flex flex-col gap-2" aria-label={t('packMods.fromPack')}>
      <SectionLabel>{t('packMods.fromPack')}</SectionLabel>
      <div className="overflow-hidden rounded-2xl border border-border bg-card shadow-card">
        <div className="flex min-h-16 items-center gap-3 px-4 py-2.5">
          {icon}
          <span className="min-w-0 flex-1">
            <span className="block truncate text-sm font-semibold">{pack.name}</span>
            <span className="block truncate text-[13px] text-muted-foreground">{line}</span>
          </span>
          {need && <Marker tone={share?.pack?.need === 'required' ? 'green' : 'muted'}>{need}</Marker>}
          {files.length > 0 && (
            <Button variant="ghost" size="sm" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="ml-3">
              {open ? t('packMods.showFewer') : t('packMods.showAll', { count: files.length })}
              <ChevronDownIcon className={cn('transition-transform duration-(--motion-standard) ease-standard', open && 'rotate-180')} aria-hidden="true" />
            </Button>
          )}
        </div>
        {open && (
          <ul className="animate-enter border-t border-border">
            {files.map((f) => (
              <li key={f.fileName} className="flex min-h-12 items-center gap-3 border-b border-border py-2 pr-4 pl-[68px] last:border-b-0">
                <span className="flex min-w-0 flex-1 items-baseline gap-1.5">
                  <span className="truncate text-[13px] font-semibold">{f.name}</span>
                  {f.version && <span className="shrink-0 text-xs text-muted-foreground">{f.version}</span>}
                </span>
                {labelOf(f) && <span className="shrink-0 text-xs text-muted-foreground">{labelOf(f)}</span>}
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  )
}