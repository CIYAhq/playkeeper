import { useEffect, useRef, useState, type DragEvent, type ReactNode } from 'react'
import { CopyIcon, DownloadIcon, FileIcon, FileUpIcon, LinkIcon, RefreshCwIcon, Share2Icon } from 'lucide-react'
import { ApiError } from '@/api/client'
import { planTemplate, useTemplateExport } from '@/api/templates'
import type { AddonNotice, ServerStatus, TemplatePlan } from '@/api/types'
import { errorText } from '@/api/workspace'
import { Emblem, GameIcon, TypeLogo } from '@/components/app/art'
import { copyText, Notice } from '@/components/app/bits'
import { CardGroup, ChoiceCard } from '@/components/app/controls'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { MenuItem } from '@/components/ui/menu'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { rich } from '@/i18n/rich'
import { iconURL, typeName } from '@/lib/servers'
import { addonKind } from '@/lib/software'
import { addonsLine, fileSize, leftOutAddons, packsLine, pinned, settingNames, settingsSummary } from '@/lib/templates'
import { cn } from '@/lib/utils'

/** The server menu's entry for sharing the server as a template. */
export function TemplateMenuItem({ onClick }: { onClick: () => void }) {
  return (
    <MenuItem onClick={onClick}>
      <Share2Icon />
      {t('template.menu')}
    </MenuItem>
  )
}

function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'application/json' }))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  window.setTimeout(() => URL.revokeObjectURL(url), 1000)
}

function IncludedRow({ label, detail, checked, disabled, onChange }: { label: string; detail: ReactNode; checked: boolean; disabled?: boolean; onChange?: (v: boolean) => void }) {
  return (
    <label className={cn('flex items-start gap-3 py-1.5', !disabled && 'cursor-pointer')}>
      <Checkbox checked={checked} disabled={disabled} onCheckedChange={(c) => onChange?.(c === true)} className="mt-0.5" />
      <span className="min-w-0">
        <span className="block text-sm font-semibold">{label}</span>
        <span className="block text-xs text-muted-foreground">{detail}</span>
      </span>
    </label>
  )
}

/** Share a server as a template: what it carries, a link and a file. */
export function TemplateDialog({ server, open, onOpenChange }: { server: ServerStatus; open: boolean; onOpenChange: (open: boolean) => void }) {
  const [addons, setAddons] = useState(true)
  const [settings, setSettings] = useState(true)
  const [packs, setPacks] = useState(true)
  const [latest, setLatest] = useState(false)
  const exp = useTemplateExport(server.id, { addons, settings, packs, latest }, open)
  const x = exp.data
  const mods = addonKind(server.type) === 'mods'
  const all = x?.available
  const hasAddons = !!all && (all.addons.length > 0 || !!all.modpack)
  const packsTravel = !!all && all.packs.length > 0
  const size = x ? Math.max(1, Math.round(fileSize(x.file) / 1024)) : 0
  const long = !!x && (x.linkLong || !x.link)
  const left = x ? leftOutAddons(x.leftOut) : []
  const software = all ? `${typeName(all.type)} ${all.minecraftVersion}` : ''

  async function copy() {
    if (!x?.link) return
    const ok = await copyText(x.link)
    toastManager.add(ok ? { title: t('toast.copied'), type: 'success' } : { title: t('toast.copyFailed'), type: 'error' })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[560px]" showCloseButton={false}>
        <div className="flex items-center gap-3 px-6 pt-6 pb-1">
          <Emblem size={40} icon={iconURL(server)} name={server.name} />
          <DialogTitle className="text-lg font-bold">{t('template.title', { server: server.name })}</DialogTitle>
        </div>
        <DialogPanel className="flex flex-col gap-5 pt-4">
          {exp.error && !x ? (
            <Notice
              tone="error"
              title={t('template.exportError')}
              action={
                <Button variant="outline" size="sm" onClick={exp.reload}>
                  <RefreshCwIcon />
                  {t('common.tryAgain')}
                </Button>
              }
            >
              {exp.error}
            </Notice>
          ) : !x || !all ? (
            <div className="flex flex-col gap-3" aria-busy="true">
              <Skeleton className="h-4 w-28" />
              {[0, 1, 2].map((i) => (
                <Skeleton key={i} className="h-9 rounded-lg" />
              ))}
              <Skeleton className="mt-2 h-16 rounded-2xl" />
              <Skeleton className="mt-2 h-9 rounded-lg" />
            </div>
          ) : (
            <>
              <section>
                <h3 className="text-[13px] font-semibold">{t('template.included')}</h3>
                <div className="mt-1.5">
                  <IncludedRow label={t('template.row.type')} detail={t('template.alwaysIncluded', { software })} checked disabled />
                  {hasAddons && (
                    <>
                      <IncludedRow label={t(mods ? 'template.row.mods' : 'template.row.plugins')} detail={addonsLine(all)} checked={addons} onChange={setAddons} />
                      {left.length > 0 && <p className="-mt-1 pb-1 pl-7 text-xs text-muted-foreground">{t('template.leftOut', { names: left.join(', ') })}</p>}
                    </>
                  )}
                  <IncludedRow label={t('template.row.settings')} detail={settingNames(all.settings) || t('template.defaults')} checked={settings} onChange={setSettings} />
                  {x.packsHere > 0 && <IncludedRow label={t('template.row.packs')} detail={packsTravel ? packsLine(all) : t('template.packsStay')} checked={packsTravel && packs} disabled={!packsTravel} onChange={setPacks} />}
                </div>
                <p className="mt-1.5 text-xs text-muted-foreground">{t('template.worldStays')}</p>
              </section>
              {hasAddons && addons && (
                <section>
                  <h3 className="text-[13px] font-semibold">{t(mods ? 'template.versions.mods' : 'template.versions.plugins')}</h3>
                  <CardGroup value={latest ? 'latest' : 'exact'} onChange={(v) => setLatest(v === 'latest')} label={t(mods ? 'template.versions.mods' : 'template.versions.plugins')} className="mt-2 grid gap-2.5 sm:grid-cols-2">
                    <ChoiceCard value="exact" radio="start" className="gap-3 p-3.5">
                      <span className="block text-sm font-semibold">{t('template.pin')}</span>
                      <span className="mt-0.5 block text-xs text-muted-foreground">{t('template.pinHint', { server: server.name })}</span>
                    </ChoiceCard>
                    <ChoiceCard value="latest" radio="start" className="gap-3 p-3.5">
                      <span className="block text-sm font-semibold">{t('template.latest')}</span>
                      <span className="mt-0.5 block text-xs text-muted-foreground">{t('template.latestHint', { software })}</span>
                    </ChoiceCard>
                  </CardGroup>
                </section>
              )}
              <section>
                <h3 className="text-[13px] font-semibold">{t('template.link')}</h3>
                {x.link && (
                  <div className="mt-2 flex gap-2">
                    <InputGroup className="min-w-0 flex-1">
                      <InputGroupAddon>
                        <LinkIcon aria-hidden="true" />
                      </InputGroupAddon>
                      <InputGroupInput readOnly value={x.link} aria-label={t('template.link')} onFocus={(e) => e.currentTarget.select()} />
                    </InputGroup>
                    <Button variant={long ? 'outline' : 'default'} onClick={() => void copy()}>
                      <CopyIcon />
                      {t('template.copyLink')}
                    </Button>
                  </div>
                )}
                <p className="mt-1.5 text-xs text-muted-foreground">{t('template.namesOnly')}</p>
                {long && (
                  <div className="mt-3" role="status">
                    <p className="text-[13px] font-semibold text-warning-foreground">{x.link ? t('template.longLink', { count: x.link.length }) : t('template.noLink')}</p>
                    <p className="text-xs text-muted-foreground">{t('template.sendFile')}</p>
                  </div>
                )}
              </section>
            </>
          )}
          <div className="flex items-center justify-between gap-3 border-t border-border pt-4">
            {x ? (
              <Button variant={long ? 'default' : 'ghost'} className={cn(!long && '-ml-2.5')} onClick={() => download(x.fileName, x.file)}>
                <DownloadIcon />
                {t('template.download', { size: t('unit.kb', { value: size }) })}
              </Button>
            ) : (
              <span />
            )}
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              {t('common.done')}
            </Button>
          </div>
        </DialogPanel>
      </DialogPopup>
    </Dialog>
  )
}

/** A template planned on this machine, and where it came from. */
export interface TemplateChoice {
  plan: TemplatePlan
  /** The file's name; empty for a link. */
  fileName: string
  /** The file or link data, to plan again. */
  text: string
}

function Row({ label, value, detail, icon }: { label: string; value: ReactNode; detail?: ReactNode; icon?: ReactNode }) {
  return (
    <div className="grid grid-cols-[120px_1fr] gap-3 border-t border-border py-3 max-sm:grid-cols-1 max-sm:gap-0.5">
      <dt className="text-xs text-muted-foreground max-sm:text-[13px]">{label}</dt>
      <dd className="min-w-0">
        <span className="flex items-center gap-2 text-[13px] font-semibold max-sm:text-[15px]">
          {icon}
          {value}
        </span>
        {detail && <span className="mt-0.5 block text-xs text-muted-foreground max-sm:text-[13px]">{detail}</span>}
      </dd>
    </div>
  )
}

function readError(e: unknown): string {
  return e instanceof ApiError && e.hint ? `${e.message} ${e.hint}` : errorText(e)
}

function Notices({ items, tone }: { items: AddonNotice[]; tone: 'warning' | 'error' | 'default' }) {
  if (items.length === 0) return null
  return (
    <div className="flex flex-col gap-1.5 border-t border-border py-3">
      {items.map((n, i) => (
        <Notice key={`${n.kind}-${i}`} tone={tone} title={n.message}>
          {n.hint}
        </Notice>
      ))}
    </div>
  )
}

/**
 * Create step 1 with "A template": choose a template file (or take the
 * one a link handed over), then see what it makes on this machine.
 */
export function TemplatePicker({
  machineId,
  value,
  onChange,
  handoff,
  problem,
  acceptExperimental,
  onAcceptExperimental,
}: {
  machineId: string
  value?: TemplateChoice
  onChange: (c: TemplateChoice | undefined) => void
  handoff?: string
  problem?: string
  acceptExperimental: boolean
  onAcceptExperimental: (v: boolean) => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const [over, setOver] = useState(false)
  const input = useRef<HTMLInputElement>(null)
  const handed = useRef(false)

  async function read(text: string, fileName: string) {
    setBusy(true)
    setError(undefined)
    try {
      onChange({ plan: await planTemplate(machineId, text), fileName, text })
    } catch (e) {
      onChange(undefined)
      setError(readError(e))
    } finally {
      setBusy(false)
      if (input.current) input.current.value = ''
    }
  }

  async function take(file: File | undefined) {
    if (!file) return
    await read(await file.text(), file.name)
  }

  useEffect(() => {
    if (!handoff || handed.current) return
    handed.current = true
    setBusy(true)
    planTemplate(machineId, handoff)
      .then((plan) => onChange({ plan, fileName: '', text: handoff }))
      .catch((e: unknown) => setError(readError(e)))
      .finally(() => setBusy(false))
  }, [handoff, machineId, onChange])

  const choose = () => input.current?.click()
  const fileInput = <input ref={input} type="file" accept=".playkeeper-template,.json,application/json" className="sr-only" tabIndex={-1} aria-label={t('template.drop')} onChange={(e) => void take(e.target.files?.[0])} />

  if (busy) {
    return (
      <div className="flex flex-col gap-3 rounded-2xl border border-border bg-card p-4" aria-busy="true">
        <div className="flex items-center gap-3">
          <Skeleton className="size-10 rounded-lg" />
          <div className="flex flex-1 flex-col gap-1.5">
            <Skeleton className="h-4 w-40" />
            <Skeleton className="h-3 w-56" />
          </div>
        </div>
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-9 rounded-lg" />
        ))}
        <span className="sr-only">{t('template.reading')}</span>
      </div>
    )
  }

  if (!value) {
    return (
      <div className="flex flex-col gap-3">
        {error && (
          <Notice tone="error" title={t('template.readError')}>
            {error}
          </Notice>
        )}
        <div
          onDragOver={(e) => {
            e.preventDefault()
            setOver(true)
          }}
          onDragLeave={() => setOver(false)}
          onDrop={(e: DragEvent) => {
            e.preventDefault()
            setOver(false)
            void take(e.dataTransfer.files[0])
          }}
          className={cn('flex min-h-[150px] flex-col items-center justify-center rounded-2xl border border-dashed border-input bg-warm px-6 py-8 text-center transition-colors', over && 'border-primary bg-selected')}
        >
          <FileUpIcon className="size-5 text-primary" aria-hidden="true" />
          <p className="mt-3 text-sm font-semibold">{t('template.drop')}</p>
          <p className="mt-1 text-xs text-muted-foreground">
            {rich('world.chooseFile', {
              choose: (chunk) => (
                <button type="button" className="font-semibold text-primary hover:underline" onClick={choose}>
                  {chunk}
                </button>
              ),
            })}
          </p>
          {fileInput}
        </div>
      </div>
    )
  }

  const p = value.plan
  const c = p.contents
  const mods = addonKind(p.type || c.type) === 'mods'
  const names = c.addons.map((a) => a.name)
  const summary = settingsSummary(c.settings)
  return (
    <div className="flex flex-col gap-3">
      {problem && (
        <Notice tone="warning" title={problem}>
          {t('template.checkAgain')}
        </Notice>
      )}
      <div className="rounded-2xl border border-border bg-card px-4 pt-4 pb-3">
        <div className="flex items-center gap-3 pb-3">
          <GameIcon size={40} />
          <div className="min-w-0 flex-1">
            <h3 className="truncate text-[15px] font-semibold">{c.name}</h3>
            <p className="truncate text-xs text-muted-foreground">{value.fileName || t('template.fromLink')}</p>
          </div>
          <Button variant="ghost" size="sm" onClick={choose}>
            <FileIcon />
            {t('template.chooseAnother')}
          </Button>
          {fileInput}
        </div>
        <dl>
          <Row label={t('template.row.type')} icon={<TypeLogo type={p.type || c.type} size={20} />} value={`${typeName(p.type || c.type)} ${p.minecraftVersion || c.minecraftVersion}`} />
          {c.modpack && <Row label={t('template.row.modpack')} value={c.modpack.name} detail={c.modpack.versionNumber ?? t('template.newest')} />}
          {names.length > 0 && <Row label={t(mods ? 'template.row.mods' : 'template.row.plugins')} value={t(pinned(c) ? 'template.sameVersions' : 'template.newestVersions', { count: names.length })} detail={names.join(', ')} />}
          {summary && <Row label={t('template.row.settings')} value={summary} />}
          <Row label={t('template.row.world')} value={t('template.notIncluded')} detail={t('template.freshLand')} />
        </dl>
        <Notices items={p.blockers} tone="error" />
        <Notices items={p.warnings} tone="warning" />
        <Notices items={p.skipped} tone="default" />
        {p.experimental && p.ready && (
          <label className="flex items-start gap-2.5 border-t border-border py-3 text-[13px]">
            <Checkbox checked={acceptExperimental} onCheckedChange={(v) => onAcceptExperimental(v === true)} className="mt-0.5" />
            {t('new.experimentalConsent', { version: p.minecraftVersion ?? '' })}
          </label>
        )}
        <p className="border-t border-border pt-3 text-xs text-muted-foreground">{t(mods ? 'template.freshMods' : 'template.freshPlugins')}</p>
      </div>
    </div>
  )
}
