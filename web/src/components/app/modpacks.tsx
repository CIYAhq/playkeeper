import { useEffect, useId, useState } from 'react'
import { ArrowUpRightIcon, PackageIcon, RefreshCwIcon, SearchIcon } from 'lucide-react'
import { modpackIcon, useModpackDetail, useModpackPreview, useModpacks, type ModpackSort } from '@/api/modpacks'
import type { ModpackCard, ModpackDetail, ModpackSource } from '@/api/types'
import { TypeLogo } from '@/components/app/art'
import { Notice } from '@/components/app/bits'
import { ChoiceSelect } from '@/components/app/controls'
import { InlineSkeleton, ListSkeleton, LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { Sheet, SheetDescription, SheetPanel, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { t, type MessageKey } from '@/i18n'
import { rich } from '@/i18n/rich'
import { formatBytes, formatCompact, formatMB, relativeTime } from '@/lib/format'
import { linkPath } from '@/lib/router'
import { typeName } from '@/lib/servers'
import { cn } from '@/lib/utils'

/** The pack a new server is made from. */
export interface ModpackChoice {
  source: ModpackSource
  projectId: string
  /** Empty takes the newest version Playkeeper can set up. */
  versionId: string
  name: string
  type: string
  minecraftVersion: string
  memoryMB: number
  /** The mods the version bundles; 0 or missing when unknown. */
  mods?: number
}

function choiceOf(card: ModpackCard, detail?: ModpackDetail): ModpackChoice {
  const newest = detail?.versions.find((v) => v.id === detail.newest)
  return {
    source: card.source,
    projectId: card.projectId,
    versionId: newest?.id ?? '',
    name: card.name,
    type: newest?.type ?? card.types[0] ?? '',
    minecraftVersion: newest?.minecraftVersion ?? card.minecraftVersions[0] ?? '',
    memoryMB: detail?.memoryMB ?? card.memoryMB ?? 0,
    mods: newest?.mods ?? detail?.mods ?? card.mods ?? 0,
  }
}

/** "Fabric Loader 0.17.2", "NeoForge 21.1.72". */
export function loaderLabel(type: string, version?: string): string {
  const name = type === 'fabric' ? t('modpacks.loader.fabric') : type === 'quilt' ? t('modpacks.loader.quilt') : typeName(type)
  return version ? `${name} ${version}` : name
}

export function sourceName(source: ModpackSource): string {
  return t(source === 'curseforge' ? 'modpacks.source.curseforge' : 'modpacks.source.modrinth')
}

export function PackIcon({ machineId, url, size = 40, className }: { machineId: string; url?: string; size?: number; className?: string }) {
  const [broken, setBroken] = useState(false)
  return (
    <span className={cn('flex shrink-0 items-center justify-center overflow-hidden rounded-xl bg-muted text-muted-foreground', className)} style={{ width: size, height: size }}>
      {url && !broken ? <img src={modpackIcon(machineId, url)} alt="" width={size} height={size} className="size-full object-cover" onError={() => setBroken(true)} /> : <PackageIcon className="size-1/2" aria-hidden="true" />}
    </span>
  )
}

const sorts: { value: ModpackSort; label: MessageKey }[] = [
  { value: 'downloads', label: 'modpacks.sort.downloads' },
  { value: 'relevance', label: 'modpacks.sort.relevance' },
  { value: 'updated', label: 'modpacks.sort.updated' },
  { value: 'newest', label: 'modpacks.sort.newest' },
]

/** The create flow's modpack list: search, sort, and a details sheet per pack. */
export function ModpackPicker({ machineId, value, onChange, onUse, phone }: { machineId: string; value?: ModpackChoice; onChange: (c: ModpackChoice) => void; onUse: (c: ModpackChoice) => void; phone: boolean }) {
  const [query, setQuery] = useState('')
  const [sort, setSort] = useState<ModpackSort>('downloads')
  const [open, setOpen] = useState<ModpackCard>()
  const [typed, setTyped] = useState('')
  const list = useModpacks(machineId, query, sort)
  const cards = (list.data?.cards ?? []).slice(0, 4)
  const curseforge = !!list.data && !list.data.sources.includes('curseforge')

  useEffect(() => {
    const timer = window.setTimeout(() => setQuery(typed.trim()), 300)
    return () => window.clearTimeout(timer)
  }, [typed])

  return (
    <div className="flex flex-col gap-3">
      <div className="flex gap-2">
        {/* The sort select's hidden input stays outside the form, so Enter in the one field searches. */}
        <form
          className="flex-1"
          role="search"
          onSubmit={(e) => {
            e.preventDefault()
            setQuery(typed.trim())
          }}
        >
          <InputGroup>
            <InputGroupAddon>
              <SearchIcon aria-hidden="true" />
            </InputGroupAddon>
            <InputGroupInput
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              placeholder={t('modpacks.search')}
              aria-label={t('modpacks.search')}
              type="search"
              enterKeyHint="search"
              maxLength={100}
              className={phone ? 'min-h-11' : undefined}
            />
          </InputGroup>
        </form>
        {!phone && <ChoiceSelect value={sort} onChange={setSort} options={sorts.map((s) => ({ value: s.value, label: t(s.label) }))} label={t('modpacks.sort')} className="min-w-40" />}
      </div>
      {list.error ? (
        <Notice
          tone="error"
          title={t('modpacks.error')}
          action={
            <Button variant="outline" size="sm" onClick={list.reload}>
              <RefreshCwIcon />
              {t('common.tryAgain')}
            </Button>
          }
        >
          {list.error}
        </Notice>
      ) : list.loading ? (
        <ListSkeleton rows={4} className="flex flex-col gap-2" rowClassName={cn('flex items-center gap-3 rounded-2xl border border-border bg-card pr-4 pl-3', phone ? 'h-[62px]' : 'h-[70px]')} face={cn('rounded-xl', phone ? 'size-10' : 'size-11')} />
      ) : cards.length === 0 ? (
        <p className="animate-fade py-6 text-center text-[13px] text-muted-foreground">{t('modpacks.empty', { query })}</p>
      ) : (
        <div key={`${query}\n${sort}`} role="radiogroup" aria-label={t('new.from.modpack')} className="flex animate-fade flex-col gap-2">
          {cards.map((card) => (
            <PackRow
              key={`${card.source}:${card.projectId}`}
              machineId={machineId}
              card={card}
              selected={value?.source === card.source && value.projectId === card.projectId}
              phone={phone}
              onPick={() => onChange(choiceOf(card))}
              onOpen={() => {
                onChange(choiceOf(card))
                setOpen(card)
              }}
            />
          ))}
        </div>
      )}
      {curseforge && !phone && (
        <p className="text-xs text-muted-foreground">
          {rich('modpacks.curseforge', {
            link: (chunk) => (
              <a {...linkPath('/settings#addon-sources')} className="ml-1 font-medium text-success-strong hover:underline">
                {chunk}
              </a>
            ),
          })}
        </p>
      )}
      <PackSheet
        machineId={machineId}
        card={open}
        phone={phone}
        onClose={() => setOpen(undefined)}
        onUse={(c) => {
          setOpen(undefined)
          onChange(c)
          onUse(c)
        }}
      />
    </div>
  )
}

function PackRow({ machineId, card, selected, phone, onPick, onOpen }: { machineId: string; card: ModpackCard; selected: boolean; phone: boolean; onPick: () => void; onOpen: () => void }) {
  const lineId = useId()
  const type = card.types[0] ?? ''
  const version = card.minecraftVersions[0] ?? ''
  const memory = card.memoryMB ? formatMB(card.memoryMB) : ''
  const facts = phone
    ? [[typeName(type), version].filter(Boolean).join(' '), memory && t('modpacks.needsPhone', { memory }), formatCompact(card.downloads)]
    : [version && t('server.minecraft', { version }), card.mods ? t('modpacks.mods', { count: card.mods }) : '']
  return (
    <div className={cn('flex items-center rounded-2xl border transition-[box-shadow,border-color,background-color]', selected ? 'border-primary/55 bg-selected shadow-selected' : 'border-border bg-card hover:border-input', card.unavailable && 'opacity-60')}>
      <button type="button" onClick={onOpen} className="flex min-w-0 flex-1 items-center gap-3 rounded-2xl py-3 pl-3 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring max-sm:py-2.5">
        <PackIcon machineId={machineId} url={card.iconUrl} size={phone ? 40 : 44} />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-sm font-semibold max-sm:text-base">{card.name}</span>
          <span className="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground max-sm:text-[13px]">
            {!phone && type && (
              <>
                <TypeLogo type={type} size={16} />
                <span>{typeName(type)}</span>
                <span aria-hidden="true">·</span>
              </>
            )}
            <span id={lineId} className="truncate">
              {card.unavailable ? card.unavailable.message : facts.filter(Boolean).join(t('common.dot'))}
            </span>
          </span>
        </span>
        {!phone && (
          <span className="shrink-0 pl-3 text-right">
            {memory && <span className="block text-[13px] font-semibold">{t('modpacks.needs', { memory })}</span>}
            <span className="block text-xs text-muted-foreground">{t('modpacks.downloads', { count: formatCompact(card.downloads) })}</span>
          </span>
        )}
      </button>
      <button
        type="button"
        role="radio"
        aria-checked={selected}
        aria-label={t('modpacks.pick', { name: card.name })}
        disabled={!!card.unavailable}
        title={card.unavailable?.message}
        aria-describedby={card.unavailable ? lineId : undefined}
        onClick={onPick}
        className="flex size-11 shrink-0 items-center justify-center rounded-full outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed"
      >
        <span className={cn('flex size-[18px] items-center justify-center rounded-full border transition-colors duration-(--motion-fast) ease-standard', selected ? 'border-primary bg-primary' : 'border-input bg-white')}>{selected && <span className="size-1.5 animate-fade rounded-full bg-white" />}</span>
      </button>
    </div>
  )
}

function PackSheet({ machineId, card, phone, onClose, onUse }: { machineId: string; card?: ModpackCard; phone: boolean; onClose: () => void; onUse: (c: ModpackChoice) => void }) {
  const detail = useModpackDetail(machineId, card?.source, card?.projectId)
  const d = detail.data
  const preview = useModpackPreview(machineId, card?.source, card?.projectId, d?.newest)
  const p = preview.data
  const newest = d?.versions.find((v) => v.id === d.newest)
  const source = card ? sourceName(card.source) : ''
  const blocker = p && !p.ready ? p.blockers[0] : undefined
  const unavailable = d?.unavailable ?? (d && !d.newest ? d.versions[0]?.unsupported : undefined)
  const mods = d?.mods ?? card?.mods ?? 0
  const fact = (label: string, value: string | undefined, loading?: boolean, detail?: string) => (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 text-sm font-semibold">{loading ? <Skeleton className="h-5 w-24" /> : <span className="animate-fade">{value || '—'}</span>}</dd>
      {detail && !loading && <dd className="animate-fade text-xs text-muted-foreground">{detail}</dd>}
    </div>
  )
  const why = !d ? t('common.loading') : (unavailable ?? blocker)?.message
  return (
    <Sheet open={!!card} onOpenChange={(o) => !o && onClose()}>
      <SheetPopup side={phone ? 'bottom' : 'right'} variant="inset" showCloseButton className={phone ? undefined : 'w-[420px] max-w-full'}>
        {card && (
          <>
            <SheetPanel className="flex flex-col gap-5 px-6 pt-6">
              <div className="flex items-start gap-3 pr-8">
                <PackIcon machineId={machineId} url={card.iconUrl} size={52} />
                <div className="min-w-0">
                  <SheetTitle className="text-xl font-bold">{card.name}</SheetTitle>
                  <SheetDescription className="text-[13px]">{card.author ? t('modpacks.by', { author: card.author, source }) : source}</SheetDescription>
                </div>
              </div>
              <p className="text-sm leading-5">{card.summary}</p>
              {!d && !detail.error && <LoadingLabel />}
              {detail.error ? (
                <Notice tone="error" title={t('modpacks.error')} action={<Button variant="outline" size="sm" onClick={detail.reload}>{t('common.tryAgain')}</Button>}>
                  {detail.error}
                </Notice>
              ) : (
                <dl className="grid grid-cols-2 gap-x-6 gap-y-3 border-b border-border pb-5" aria-busy={!d || (!p && !preview.error)}>
                  {fact(t('modpacks.fact.minecraft'), p?.minecraftVersion || newest?.minecraftVersion, !d)}
                  {fact(t('modpacks.fact.runsOn'), p ? loaderLabel(p.type, p.loaderVersion) : preview.error ? loaderLabel(newest?.type ?? '') : undefined, !p && !preview.error, p?.java ? t('modpacks.java', { java: p.java, version: p.minecraftVersion }) : undefined)}
                  {fact(t('modpacks.fact.mods'), mods ? String(mods) : undefined, !d)}
                  {fact(t('modpacks.fact.needs'), d?.memoryMB ? t('modpacks.needsMemory', { memory: formatMB(d.memoryMB) }) : undefined, !d)}
                  {fact(t('modpacks.fact.updated'), relativeTime(card.updated))}
                  {fact(t('modpacks.fact.download'), p ? formatBytes(p.downloadSize) : undefined, !p && !preview.error)}
                </dl>
              )}
              {(unavailable ?? blocker) && (
                <Notice tone="warning" title={t('modpacks.cantUse')}>
                  {(unavailable ?? blocker)?.message}
                  {(unavailable ?? blocker)?.hint && <span className="mt-1 block">{(unavailable ?? blocker)?.hint}</span>}
                </Notice>
              )}
              <section>
                <h3 className="text-sm font-semibold">{t('modpacks.inside')}</h3>
                <p className="mt-1 text-[13px] text-muted-foreground">{!d ? <InlineSkeleton className="w-48" /> : d.headline && mods > 1 ? t('modpacks.insideHeadline', { headline: d.headline, count: mods - 1 }) : t('modpacks.insideCount', { count: mods })}</p>
              </section>
              <section>
                <h3 className="text-sm font-semibold">{t('modpacks.friends')}</h3>
                <p className="mt-1 text-[13px] text-muted-foreground">{t('modpacks.friendsBody')}</p>
              </section>
              <a href={card.pageUrl} target="_blank" rel="noreferrer noopener" className="inline-flex items-center gap-1 self-start text-[13px] font-semibold text-success-strong hover:underline">
                {t('modpacks.open', { source })}
                <ArrowUpRightIcon className="size-3.5" aria-hidden="true" />
              </a>
            </SheetPanel>
            <div className="px-6 pt-4 pb-5">
              <Button className="w-full" size={phone ? 'touch' : 'default'} disabledReason={why} onClick={() => onUse(choiceOf(card, d))}>
                {t('modpacks.use')}
              </Button>
              <p className="mt-2 text-center text-xs text-muted-foreground">{t('modpacks.checked', { source })}</p>
            </div>
          </>
        )}
      </SheetPopup>
    </Sheet>
  )
}
