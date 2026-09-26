import { useState } from 'react'
import { ChevronRightIcon, SearchIcon } from 'lucide-react'
import type { Catalog, CatalogEntry, LevelType, MemoryBudget, MemorySizing, PlayStyle, ServerStatus } from '@/api/types'
import { PlayArt, TypeLogo, WorldArt } from '@/components/app/art'
import { CardGroup, ChoiceCard } from '@/components/app/controls'
import { Checkbox } from '@/components/ui/checkbox'
import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Combobox, ComboboxEmpty, ComboboxInput, ComboboxItem, ComboboxList, ComboboxPopup } from '@/components/ui/combobox'
import { Sheet, SheetPanel, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { Slider } from '@/components/ui/slider'
import { Switch } from '@/components/ui/switch'
import { t, type MessageKey } from '@/i18n'
import { rich } from '@/i18n/rich'
import { formatMB } from '@/lib/format'
import { memorySegments, playersFor, share } from '@/lib/memory'
import { softwareName, typeName } from '@/lib/servers'
import { addonKind, formatReleased, hasBuilds, typeTexts } from '@/lib/software'
import { levelTypes, memoryForStyle, preset } from '@/lib/styles'
import { cn } from '@/lib/utils'
import { compareMinecraft } from '@/lib/versions'

/** The play styles offered as cards; hardcore is an option under "More options". */
export const cardStyles: PlayStyle[] = ['friends', 'creative', 'solo']

export interface CreateChoices {
  type: string
  versionId: string
  acceptExperimental: boolean
  style: PlayStyle
  hardcore: boolean
  levelType: LevelType
  memoryMB: number
  name: string
  motd: string
  eula: boolean
  /** The build to pin for types that have one; empty takes the recommended one. */
  build: string
}

/** A name nobody else on the machine uses: "Survival", then "Survival 2". */
export function freeName(base: string, servers: ServerStatus[] | undefined): string {
  const taken = new Set((servers ?? []).map((s) => s.name.toLowerCase()))
  if (!taken.has(base.toLowerCase())) return base
  for (let n = 2; ; n++) if (!taken.has(`${base} ${n}`.toLowerCase())) return `${base} ${n}`
}

export function recommendedVersion(catalog: Catalog | undefined): CatalogEntry | undefined {
  const vs = (catalog?.versions ?? []).filter((v) => v.supported)
  return vs.find((v) => v.recommended) ?? vs.find((v) => !v.experimental) ?? vs[0]
}

/** The memory options this machine can give a new server. */
export function memoryOptions(catalog: Catalog | undefined): number[] {
  return (catalog?.memoryOptionsMB ?? []).filter((mb) => mb <= (catalog?.maxMemoryMB ?? 0))
}

/** The sizing guide's suggestion for the style's players, or the largest offered budget below it. */
export function styleMemory(catalog: Catalog | undefined, style: PlayStyle): number {
  const players = preset(style)?.players ?? 10
  const suggestions = catalog?.sizing?.suggestions ?? []
  const want = (suggestions.find((s) => s.players >= players) ?? suggestions[suggestions.length - 1])?.memoryMB ?? catalog?.recommendedMemoryMB ?? 0
  return memoryForStyle(memoryOptions(catalog), want)
}

/** What the sizing guide says about one of the offered budgets. */
export function budgetAdvice(catalog: Catalog | undefined, memoryMB: number): MemoryBudget | undefined {
  return catalog?.sizing?.budgets.find((b) => b.memoryMB === memoryMB)
}

/** The body of the create request. */
export function createRequest(c: CreateChoices) {
  const p = preset(c.style)
  const gameplay = { ...p?.gameplay, levelType: c.levelType, ...(c.hardcore ? { hardcore: true, difficulty: 'hard' as const } : {}) }
  return {
    name: c.name.trim(),
    type: c.type,
    acceptEula: c.eula,
    versionId: c.versionId,
    acceptExperimental: c.acceptExperimental,
    memoryMB: c.memoryMB,
    motd: c.motd.trim() || c.name.trim(),
    maxPlayers: 10,
    playStyle: c.hardcore ? 'hardcore' : c.style,
    gameplay,
    ...(c.build && hasBuilds(c.type) ? { build: c.build } : {}),
  }
}

/** Why the chosen version can't be used yet; undefined when it can. */
export function versionBlocked(c: CreateChoices, version: CatalogEntry | undefined): string | undefined {
  if (!version) return t('reason.pickVersion')
  return version.experimental && !c.acceptExperimental ? t('reason.experimental', { version: version.minecraftVersion }) : undefined
}

/** Why the name step isn't finished; undefined when it is. */
export function nameBlocked(c: CreateChoices): string | undefined {
  return !c.name.trim() ? t('reason.nameFirst') : c.eula ? undefined : t('reason.eula')
}

/** Why the server can't be created yet; undefined when it can. */
export function createBlocked(c: CreateChoices, version: CatalogEntry | undefined): string | undefined {
  return versionBlocked(c, version) ?? nameBlocked(c)
}

const typeKeys: Record<string, { long: MessageKey; short: MessageKey }> = {
  paper: { long: 'new.type.paper', short: 'new.type.paper.short' },
  vanilla: { long: 'new.type.vanilla', short: 'new.type.vanilla.short' },
  purpur: { long: 'new.type.purpur', short: 'new.type.purpur.short' },
  fabric: { long: 'new.type.fabric', short: 'new.type.mods.short' },
  quilt: { long: 'new.type.quilt', short: 'new.type.mods.short' },
  neoforge: { long: 'new.type.neoforge', short: 'new.type.mods.short' },
}

export function TypeCards({ catalog, value, onChange, phone }: { catalog: Catalog | undefined; value: string; onChange: (v: string) => void; phone?: boolean }) {
  const types = catalog?.types ?? [{ id: 'paper', name: 'Paper', available: true }]
  return (
    <CardGroup value={value} onChange={onChange} label={t('new.typeTitle')} className={cn('grid gap-2.5', phone ? 'grid-cols-1' : 'grid-cols-2 xl:grid-cols-3')}>
      {types.map((ty) => {
        const keys = typeKeys[ty.id]
        const runs = typeTexts(ty.id)?.runs
        const soon = !ty.available
        if (phone) {
          return (
            <ChoiceCard key={ty.id} value={ty.id} disabled={soon} reason={t('common.comingSoon')} radio={soon ? 'none' : 'end'} className="min-h-[60px] items-center gap-3 px-3.5 py-2.5">
              <span className="flex items-center gap-3">
                <TypeLogo type={ty.id} size={40} />
                <span className="min-w-0 flex-1">
                  <span className="block text-base font-semibold">{typeName(ty.id)}</span>
                  {keys && <span className="block text-[13px] text-muted-foreground">{t(keys.short)}</span>}
                </span>
                {soon && (
                  <span className="text-xs text-muted-foreground" aria-hidden="true">
                    {t('common.soon')}
                  </span>
                )}
              </span>
            </ChoiceCard>
          )
        }
        return (
          <ChoiceCard key={ty.id} value={ty.id} disabled={soon} reason={t('common.comingSoon')} radio={soon ? 'none' : 'end'} className="min-h-[160px] gap-2 p-3.5">
            <span className="flex h-full flex-col">
              <span className="flex items-start justify-between">
                <TypeLogo type={ty.id} size={36} />
                {soon && (
                  <span className="text-xs text-muted-foreground" aria-hidden="true">
                    {t('common.comingSoon')}
                  </span>
                )}
              </span>
              <span className="mt-3.5 flex items-center gap-2 text-sm font-semibold">
                {typeName(ty.id)}
                {ty.id === 'paper' && <span className="text-xs font-medium text-muted-foreground">{t('common.recommended')}</span>}
              </span>
              {keys && <span className="mt-0.5 block text-xs leading-4 text-muted-foreground">{t(keys.long)}</span>}
              {runs && <span className="mt-auto block pt-4 text-xs leading-4 text-muted-foreground">{t(runs)}</span>}
            </span>
          </ChoiceCard>
        )
      })}
    </CardGroup>
  )
}

interface VersionCard {
  entry: CatalogEntry
  note: string
}

/** The note after a recommended version's release date. */
function recommendedNote(e: CatalogEntry, type: string, phone?: boolean): string {
  if (phone) return t('new.latestStablePhone')
  if (type === 'paper') return softwareName(type, e.paperBuild)
  if (type === 'purpur') return softwareName(type, e.build ?? '')
  return addonKind(type) === 'mods' ? t('new.modsSupport', { type: typeName(type) }) : ''
}

/** "Released 2 Sep 2026 · Paper build 41", or just the note without a date. */
export function versionLine(e: CatalogEntry, note: string): string {
  if (!e.releasedAt) return note
  const date = formatReleased(e.releasedAt)
  return note ? t('new.releasedNote', { date, note }) : t('new.released', { date })
}

/**
 * The versions worth a card of their own, and the older ones behind a search.
 * Older versions PaperMC no longer updates stay on offer, for friends or
 * plugins that still need them.
 */
export function versionCards(versions: CatalogEntry[], servers: ServerStatus[] | undefined, phone?: boolean, type = 'paper'): { cards: VersionCard[]; older: CatalogEntry[] } {
  const byVersion = (a: CatalogEntry, b: CatalogEntry) => compareMinecraft(b.minecraftVersion, a.minecraftVersion) || b.paperBuild - a.paperBuild
  const stable = versions.filter((v) => !v.experimental).sort(byVersion)
  const rec = versions.find((v) => v.recommended) ?? stable.find((v) => v.supported) ?? stable[0]
  const cards: VersionCard[] = []
  const add = (e: CatalogEntry | undefined, note: string) => {
    if (e && !cards.some((c) => c.entry.id === e.id)) cards.push({ entry: e, note })
  }
  if (rec) add(rec, recommendedNote(rec, type, phone))
  const exp = versions.filter((v) => v.experimental && v.supported && (!rec || compareMinecraft(v.minecraftVersion, rec.minecraftVersion) > 0)).sort(byVersion)[0]
  add(exp, phone ? t('new.experimentalShort') : addonKind(type) === 'mods' ? t('new.modsNotReady') : t('new.experimentalHint'))
  const next = stable.find((v) => rec && compareMinecraft(v.minecraftVersion, rec.minecraftVersion) < 0)
  add(next, '')
  for (const s of servers ?? []) {
    const v = s.config?.minecraftVersion
    const e = v ? stable.find((x) => x.minecraftVersion === v) : undefined
    add(e, t('new.sameAs', { server: s.name }))
  }
  const older = stable.filter((v) => !cards.some((c) => c.entry.id === v.id))
  return { cards, older }
}

export function VersionPicker({ catalog, servers, value, onChange, acceptExperimental, onAcceptExperimental, phone }: { catalog: Catalog | undefined; servers: ServerStatus[] | undefined; value: string; onChange: (id: string) => void; acceptExperimental: boolean; onAcceptExperimental: (v: boolean) => void; phone?: boolean }) {
  const [showOlder, setShowOlder] = useState(false)
  const type = catalog?.type ?? 'paper'
  const { cards, older } = versionCards(catalog?.versions ?? [], servers, phone, type)
  const chosen = catalog?.versions.find((v) => v.id === value)
  const olderChosen = older.find((v) => v.id === value)
  const olderLine = (v: CatalogEntry) => versionLine(v, v.releasedAt ? '' : type === 'paper' ? t('new.build', { build: v.paperBuild }) : '')
  const items = older.map((v) => ({ value: v.id, label: v.minecraftVersion, line: olderLine(v) }))
  return (
    <div className="flex flex-col gap-2.5">
      <CardGroup value={value} onChange={onChange} label={t('new.versionTitle')} className="flex flex-col gap-2.5">
        {cards.map((c) => (
          <ChoiceCard key={c.entry.id} value={c.entry.id} radio={phone ? 'end' : 'start'} className="items-center gap-3 px-4 py-3">
            <span className="flex flex-wrap items-baseline gap-x-2">
              <span className="text-[15px] font-semibold">{c.entry.minecraftVersion}</span>
              {!phone && c.entry.recommended && <span className="text-xs font-medium text-success-foreground">{t('new.latestStable')}</span>}
              {!phone && c.entry.experimental && <span className="text-xs font-medium text-warning-foreground">{t('common.experimental')}</span>}
            </span>
            <span className="mt-0.5 block text-xs text-muted-foreground max-sm:text-[13px]">{versionLine(c.entry, c.note)}</span>
          </ChoiceCard>
        ))}
        {olderChosen && (
          <ChoiceCard value={olderChosen.id} radio={phone ? 'end' : 'start'} className="items-center gap-3 px-4 py-3">
            <span className="text-[15px] font-semibold">{olderChosen.minecraftVersion}</span>
            <span className="mt-0.5 block text-xs text-muted-foreground max-sm:text-[13px]">{olderLine(olderChosen)}</span>
          </ChoiceCard>
        )}
      </CardGroup>
      {chosen?.experimental && (
        <label className="flex items-start gap-2.5 px-1 py-1 text-[13px]">
          <Checkbox checked={acceptExperimental} onCheckedChange={(c) => onAcceptExperimental(c === true)} className="mt-0.5" />
          {t('new.experimentalConsent', { version: chosen.minecraftVersion })}
        </label>
      )}
      {older.length > 0 &&
        (phone && !showOlder ? (
          <button type="button" onClick={() => setShowOlder(true)} className="min-h-11 text-[15px] font-semibold text-success-strong">
            {t('new.olderShow', { count: older.length })}
          </button>
        ) : (
          <div className="mt-2 max-w-[320px] max-sm:max-w-none">
            <div className="mb-1.5 text-[13px] font-semibold">{t('new.older')}</div>
            <Combobox items={items} value={items.find((i) => i.value === value) ?? null} onValueChange={(i) => i && onChange((i as { value: string }).value)}>
              <ComboboxInput placeholder={t('new.olderSearch', { count: older.length, type: typeName(type) })} aria-label={t('new.older')} startAddon={<SearchIcon />} />
              <ComboboxPopup>
                <ComboboxEmpty>{t('new.noMatch')}</ComboboxEmpty>
                <ComboboxList>
                  {(item: (typeof items)[number]) => (
                    <ComboboxItem key={item.value} value={item}>
                      <span className="flex flex-col">
                        <span>{item.label}</span>
                        {item.line && <span className="text-xs text-muted-foreground">{item.line}</span>}
                      </span>
                    </ComboboxItem>
                  )}
                </ComboboxList>
              </ComboboxPopup>
            </Combobox>
          </div>
        ))}
    </div>
  )
}

export function StyleCards({ catalog, value, onChange, phone }: { catalog: Catalog | undefined; value: PlayStyle; onChange: (v: PlayStyle) => void; phone?: boolean }) {
  return (
    <CardGroup value={value} onChange={onChange} label={t('style.question')} className={cn('grid gap-2.5', phone ? 'grid-cols-1' : 'grid-cols-3')}>
      {cardStyles.map((id) => {
        const p = preset(id)
        if (!p) return null
        const summary = t(p.summary, { memory: formatMB(styleMemory(catalog, id)) })
        if (phone) {
          return (
            <ChoiceCard key={id} value={id} className="items-center gap-3 p-3">
              <span className="flex items-center gap-3">
                <PlayArt style={id} scale={2} className="rounded-lg" />
                <span className="min-w-0">
                  <span className="block text-base font-semibold">{t(p.label)}</span>
                  <span className="block text-[13px] text-muted-foreground">{summary}</span>
                </span>
              </span>
            </ChoiceCard>
          )
        }
        return (
          <ChoiceCard key={id} value={id} radio="none" className="flex-col p-2">
            <PlayArt style={id} scale={6} className="h-auto w-full rounded-xl" />
            <span className="flex items-center justify-between gap-2 px-1.5 pt-3">
              <span className="text-sm font-semibold">{t(p.label)}</span>
              <span className="inline-flex size-[18px] items-center justify-center rounded-full border border-input bg-white group-has-[[data-checked]]/card:border-primary group-has-[[data-checked]]/card:bg-primary" aria-hidden="true">
                <span className="size-1.5 rounded-full bg-white opacity-0 group-has-[[data-checked]]/card:opacity-100" />
              </span>
            </span>
            <span className="block px-1.5 pt-1 text-xs text-muted-foreground">{t(p.desc)}</span>
            <span className="block px-1.5 pt-2 pb-1.5 text-xs font-medium">{summary}</span>
          </ChoiceCard>
        )
      })}
    </CardGroup>
  )
}

function OptionsBody({ hardcore, onHardcore, level, onLevel }: { hardcore: boolean; onHardcore: (v: boolean) => void; level: LevelType; onLevel: (v: LevelType) => void }) {
  return (
    <div className="flex flex-col gap-4">
      <label className="flex items-start gap-3">
        <Switch checked={hardcore} onCheckedChange={onHardcore} className="mt-0.5" />
        <span>
          <span className="block text-[13px] font-semibold max-sm:text-[15px]">{t('style.hardcoreToggle')}</span>
          <span className="block text-xs text-muted-foreground max-sm:text-[13px]">{t('style.hardcoreHint')}</span>
        </span>
      </label>
      <div>
        <div className="text-[13px] font-semibold max-sm:text-[15px]">{t('style.worldType')}</div>
        <CardGroup value={level} onChange={onLevel} label={t('style.worldType')} className="mt-2 grid grid-cols-2 gap-2 sm:grid-cols-4">
          {levelTypes.map((l) => (
            <ChoiceCard key={l} value={l} radio="none" className="flex-col p-1.5">
              <WorldArt type={l} scale={3} className="h-auto w-full rounded-lg" />
              <span className="block px-1 pt-1.5 text-[13px] font-semibold">{t(`style.world.${l}`)}</span>
              <span className="block px-1 pb-1 text-xs text-muted-foreground">{t(`style.world.${l}.desc`)}</span>
            </ChoiceCard>
          ))}
        </CardGroup>
      </div>
    </div>
  )
}

export function MoreOptions({ hardcore, onHardcore, level, onLevel, phone }: { hardcore: boolean; onHardcore: (v: boolean) => void; level: LevelType; onLevel: (v: LevelType) => void; phone?: boolean }) {
  const [open, setOpen] = useState(false)
  const summary = t('style.moreSummary', { world: t(`style.world.${level}`), hardcore: hardcore ? t('style.isHardcore') : t('style.notHardcore') })
  if (phone) {
    return (
      <>
        <button type="button" onClick={() => setOpen(true)} className="flex min-h-14 w-full items-center gap-2 rounded-2xl border border-border bg-white px-4 text-left">
          <span className="text-base font-semibold">{t('style.more')}</span>
          <span className="min-w-0 flex-1 truncate text-[13px] text-muted-foreground">{t('style.moreHint')}</span>
          <ChevronRightIcon className="size-5 text-muted-foreground" aria-hidden="true" />
        </button>
        <Sheet open={open} onOpenChange={setOpen}>
          <SheetPopup side="bottom">
            <div className="px-5 pt-3">
              <SheetTitle className="text-lg font-bold">{t('style.more')}</SheetTitle>
            </div>
            <SheetPanel className="px-5 pt-4">
              <OptionsBody hardcore={hardcore} onHardcore={onHardcore} level={level} onLevel={onLevel} />
            </SheetPanel>
          </SheetPopup>
        </Sheet>
      </>
    )
  }
  return (
    <Collapsible open={open} onOpenChange={setOpen} className="rounded-2xl border border-border">
      <CollapsibleTrigger className="flex min-h-11 w-full items-center gap-2 rounded-2xl px-3 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring">
        <ChevronRightIcon className={cn('size-4 text-muted-foreground transition-transform', open && 'rotate-90')} aria-hidden="true" />
        <span className="text-[13px] font-semibold">{t('style.more')}</span>
        <span className="text-xs text-muted-foreground">{t('style.moreHint')}</span>
        <span className="ml-auto text-xs text-muted-foreground">{summary}</span>
      </CollapsibleTrigger>
      <CollapsiblePanel>
        <div className="border-t border-border p-4">
          <OptionsBody hardcore={hardcore} onHardcore={onHardcore} level={level} onLevel={onLevel} />
        </div>
      </CollapsiblePanel>
    </Collapsible>
  )
}

export function EulaCheck({ checked, onChange, short, className }: { checked: boolean; onChange: (v: boolean) => void; short?: boolean; className?: string }) {
  return (
    <label className={cn('flex items-start gap-3', className)}>
      <Checkbox checked={checked} onCheckedChange={(c) => onChange(c === true)} className="mt-0.5" />
      <span>
        <span className="block text-[13px] font-semibold max-sm:text-[15px]">
          {short
            ? t('eula.acceptShort')
            : rich('eula.accept', {
                link: (chunk) => (
                  <a href={t('eula.url')} target="_blank" rel="noreferrer" className="text-primary underline underline-offset-2" onClick={(e) => e.stopPropagation()}>
                    {chunk}
                  </a>
                ),
              })}
        </span>
        <span className="block text-xs text-muted-foreground max-sm:text-[13px]">{t('eula.hint')}</span>
      </span>
    </label>
  )
}

const segmentColor = { system: 'bg-[#5f645a] text-white', running: 'bg-primary text-white', stopped: 'bg-[#8fc29f] text-foreground', new: 'bg-marigold text-foreground', free: 'bg-muted' } as const

/** How the machine's memory is shared, with the new server in marigold. */
export function MemoryBar({ catalog, memoryMB }: { catalog: Catalog; memoryMB: number }) {
  const segs = memorySegments(catalog.hostMemoryMB, catalog.systemReserveMB, catalog.servers, memoryMB)
  const free = segs.find((s) => s.kind === 'free')?.memoryMB ?? 0
  return (
    <div>
      <div className="flex h-9 overflow-hidden rounded-xl" role="img" aria-label={t('new.machineHas', { machine: '', total: formatMB(catalog.hostMemoryMB) })}>
        {segs.map((s) => {
          const w = share(s, catalog.hostMemoryMB)
          if (w <= 0) return null
          const text = s.kind === 'system' ? t('new.system') : s.kind === 'new' ? t('new.newServerSegment', { memory: formatMB(s.memoryMB) }) : s.kind === 'free' ? '' : `${s.label} ${formatMB(s.memoryMB)}`
          return (
            <div key={s.key} className={cn('flex min-w-0 items-center justify-center border-r border-white/60 px-2 text-xs font-medium whitespace-nowrap last:border-r-0', segmentColor[s.kind])} style={{ width: `${w}%` }} title={text}>
              <span className="truncate">{text}</span>
            </div>
          )
        })}
      </div>
      <div className="mt-2.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <span className="flex items-center gap-1.5">
          <span className="size-2 rounded-[2px] bg-[#5f645a]" aria-hidden="true" />
          {t('new.systemLegend', { memory: formatMB(catalog.systemReserveMB) })}
        </span>
        {catalog.servers.map((s) => (
          <span key={s.id} className="flex items-center gap-1.5">
            <span className={cn('size-2 rounded-[2px]', s.running ? 'bg-primary' : 'bg-[#8fc29f]')} aria-hidden="true" />
            {s.name}
          </span>
        ))}
        <span className="flex items-center gap-1.5">
          <span className="size-2 rounded-[2px] bg-marigold" aria-hidden="true" />
          {t('new.thisServer')}
        </span>
        <span className="ml-auto font-medium text-success-foreground">{t('new.leftFree', { memory: formatMB(free) })}</span>
      </div>
    </div>
  )
}

/** A slider that snaps to the offered memory sizes. */
export function MemorySlider({ options, value, onChange }: { options: number[]; value: number; onChange: (mb: number) => void }) {
  const index = Math.max(0, options.indexOf(value))
  return (
    <div>
      <Slider value={index} onValueChange={(v) => onChange(options[Array.isArray(v) ? (v[0] ?? 0) : v] ?? value)} min={0} max={Math.max(0, options.length - 1)} step={1} aria-label={t('new.memoryFor')} getAriaValueText={(_formatted: string, v: number) => formatMB(options[v] ?? value)} />
      <div className="relative mt-2 h-4 text-xs text-muted-foreground" aria-hidden="true">
        {options.map((mb, i) => (
          <span key={mb} className={cn('absolute -translate-x-1/2 whitespace-nowrap', mb === value && 'font-semibold text-foreground')} style={{ left: `${options.length > 1 ? (i / (options.length - 1)) * 100 : 0}%` }}>
            {formatMB(mb)}
          </span>
        ))}
      </div>
    </div>
  )
}

/** What a mod loader keeps outside the heap before its mods, and what each mod adds; match the agent's minecraft package. */
const loaderOverheadMB: Record<string, number> = { fabric: 768, quilt: 768, neoforge: 1024 }
const modOverheadMB = 6

/** Java's share of a memory budget on a server of the type with that many mods; matches minecraft.HeapFor in the agent. */
export function heapMB(budgetMB: number, type = 'paper', mods = 0): number {
  let overhead = Math.max(512, Math.floor(budgetMB / 4))
  const base = loaderOverheadMB[type]
  if (base !== undefined) overhead = Math.max(overhead, Math.min(base + modOverheadMB * Math.max(mods, 0), Math.floor(budgetMB / 2)))
  return budgetMB - overhead
}

export function MemoryReadout({ memoryMB, sizing, type, mods = 0, recommended, style }: { memoryMB: number; sizing?: MemorySizing; type?: string; mods?: number; recommended: boolean; style?: PlayStyle }) {
  const heap = heapMB(memoryMB, type, mods)
  const players = playersFor(memoryMB, sizing, type)
  return (
    <div>
      <div className="flex items-baseline gap-2">
        <span className="text-[34px] leading-10 font-extrabold tabular-nums">{formatMB(memoryMB)}</span>
        {recommended && <span className="text-xs font-medium text-success-foreground">{t('common.recommended')}</span>}
      </div>
      <p className="mt-1 text-[13px] font-medium">{players > 0 ? t('new.roomFor', { count: players }) : t('new.roomForTight')}</p>
      <p className="mt-0.5 text-xs text-muted-foreground">{t('new.javaGets', { heap: formatMB(heap) })}</p>
      {style && <span className="sr-only">{t(preset(style)?.title ?? 'style.friends.title')}</span>}
    </div>
  )
}
