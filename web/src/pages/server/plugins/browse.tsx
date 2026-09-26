import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ArrowLeftIcon, DownloadIcon, RefreshCwIcon, SearchIcon } from 'lucide-react'
import { ApiError, get } from '@/api/client'
import type { AddonBrowse, AddonCard, AddonDetails, AddonNotice } from '@/api/types'
import { Pip } from '@/components/app/art'
import { Marker, Notice, SectionLabel } from '@/components/app/bits'
import { ChoiceSelect, useIsPhone } from '@/components/app/controls'
import { ListSkeleton, LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { Skeleton } from '@/components/ui/skeleton'
import { t, type MessageKey } from '@/i18n'
import { alsoInstalls, appendCards, browseSorts, compactCount, footerFor, maxSearch, searchPath, type BrowseSort } from '@/lib/addons'
import { busyReason } from '@/lib/phase'
import { presenceProps, useListPresence, type Presence } from '@/lib/presence'
import { linkProps } from '@/lib/router'
import { softwareLabel } from '@/lib/servers'
import { cn } from '@/lib/utils'
import { CuratedPicks } from './curated'
import { AddonIcon, detailsPath, useAddons } from './state'

const categoryKeys: Record<string, MessageKey> = {
  admin: 'addons.category.admin',
  chat: 'addons.category.chat',
  economy: 'addons.category.economy',
  gameplay: 'addons.category.gameplay',
  minigames: 'addons.category.minigames',
  world: 'addons.category.world',
  protection: 'addons.category.protection',
  optimization: 'addons.category.optimization',
  utility: 'addons.category.utility',
  library: 'addons.category.library',
  adventure: 'addons.category.adventure',
  technology: 'addons.category.technology',
  magic: 'addons.category.magic',
  storage: 'addons.category.storage',
  mobs: 'addons.category.mobs',
}

const sortKeys: Record<BrowseSort, MessageKey> = {
  downloads: 'addons.sort.downloads',
  relevance: 'addons.sort.relevance',
  updated: 'addons.sort.updated',
}

const allCategories = 'all'
const norm = (s: string) => s.toLowerCase().replace(/[^\p{L}\p{N}]/gu, '')
const cardKey = (c: AddonCard) => `${c.source}:${c.projectId}`

/** Library errors that mean the sources didn't answer, rather than a bad request. */
function unreachable(e: ApiError): boolean {
  return e.status === 502 || e.status === 503 || e.status === 504 || e.status === 429
}

export function BrowseView() {
  const a = useAddons()
  const phone = useIsPhone()
  const [q, setQ] = useState('')
  const [text, setText] = useState('')
  const [category, setCategory] = useState(allCategories)
  const [sort, setSort] = useState<BrowseSort>('downloads')
  const [cards, setCards] = useState<AddonCard[]>()
  const [unanswered, setUnanswered] = useState<AddonNotice[]>([])
  const [error, setError] = useState<ApiError>()
  const [more, setMore] = useState(false)
  const [page, setPage] = useState(0)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  // Each new search is a new list that fades in as a whole; later pages join it row by row.
  const [listId, setListId] = useState(0)
  const req = useRef(0)

  useEffect(() => {
    const timer = window.setTimeout(() => setText(q), 300)
    return () => window.clearTimeout(timer)
  }, [q])

  const serverId = a.server.id
  const load = useCallback(
    async (p: number) => {
      const id = ++req.current
      if (p === 0) setLoading(true)
      else setLoadingMore(true)
      try {
        const res = await get<AddonBrowse>(searchPath(serverId, { q: text, category: category === allCategories ? '' : category, sort }, p))
        if (id !== req.current) return
        setCards((prev) => (p === 0 ? appendCards([], res.cards) : appendCards(prev ?? [], res.cards)))
        if (p === 0) setListId((n) => n + 1)
        setMore(res.more)
        setUnanswered(res.unanswered)
        setPage(p)
        setError(undefined)
      } catch (e) {
        if (id !== req.current) return
        if (p === 0) {
          setCards(undefined)
          setError(e instanceof ApiError ? e : new ApiError(0, { error: String(e), code: 'internal' }))
        } else {
          setMore(false)
        }
      } finally {
        if (id === req.current) {
          setLoading(false)
          setLoadingMore(false)
        }
      }
    },
    [serverId, text, category, sort],
  )

  useEffect(() => {
    void load(0)
  }, [load])

  const sentinel = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = sentinel.current
    if (!el || !more || loading || loadingMore || typeof IntersectionObserver === 'undefined') return
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) void load(page + 1)
    }, { rootMargin: '600px 0px' })
    io.observe(el)
    return () => io.disconnect()
  }, [more, loading, loadingMore, page, load])

  // A card installed since the page loaded shows as installed once the list
  // refreshes after the install.
  const installed = useMemo(() => {
    const keys = new Set<string>()
    const names = new Set<string>()
    for (const r of a.rows) {
      if (r.addon && r.state !== 'identified') keys.add(`${r.addon.source}:${r.addon.projectId}`)
      if (r.state !== 'identified' && r.state !== 'unknown') names.add(norm(r.name))
    }
    return (c: AddonCard) => c.installed || keys.has(`${c.source}:${c.projectId}`) || names.has(norm(c.name))
  }, [a.rows])

  const categories = a.addons?.target.categories ?? []
  const categoryOptions = [{ value: allCategories, label: t('addons.allCategories') }, ...categories.filter((c) => categoryKeys[c]).map((c) => ({ value: c, label: t(categoryKeys[c] ?? 'addons.allCategories') }))]
  const sortOptions = browseSorts.map((s) => ({ value: s, label: t(sortKeys[s]) }))
  const searchLabel = a.kind === 'mod' ? t('addons.searchMods') : t('addons.search')
  // Before a search, Playkeeper's picks come first.
  const picks = !text.trim() && category === allCategories
  const clear = () => {
    setQ('')
    setText('')
    setCategory(allCategories)
  }

  const filters = (
    <div className={cn('flex gap-2', phone && 'flex-col')}>
      <InputGroup className={phone ? 'h-11' : 'flex-1'}>
        <InputGroupAddon>
          <SearchIcon aria-hidden="true" />
        </InputGroupAddon>
        <InputGroupInput type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder={searchLabel} aria-label={searchLabel} maxLength={maxSearch} autoComplete="off" spellCheck={false} />
      </InputGroup>
      <div className={cn('flex gap-2', phone && 'grid grid-cols-2')}>
        <ChoiceSelect value={category} onChange={setCategory} options={categoryOptions} label={t('addons.categoryLabel')} className={phone ? 'w-full justify-between rounded-xl border border-border bg-white' : 'min-w-44'} />
        <ChoiceSelect value={sort} onChange={setSort} options={sortOptions} label={t('addons.sortLabel')} className={phone ? 'w-full justify-between rounded-xl border border-border bg-white' : 'min-w-44'} />
      </div>
    </div>
  )

  let results
  if (error) {
    results = unreachable(error) ? (
      <div className="flex animate-fade flex-col items-center py-14 text-center">
        <Pip pose="sleep" size={88} />
        <h3 className="mt-4 text-lg font-bold">{a.kind === 'mod' ? t('addons.unreachableMods') : t('addons.unreachable')}</h3>
        <p className="mt-1 text-sm text-muted-foreground">{a.kind === 'mod' ? t('addons.keepWorkingMods') : t('addons.keepWorking')}</p>
        <Button variant="outline" className="mt-5" onClick={() => void load(0)} loading={loading}>
          <RefreshCwIcon />
          {t('common.tryAgain')}
        </Button>
      </div>
    ) : (
      <Notice tone="error" title={error.message} action={<Button variant="outline" onClick={() => void load(0)}>{t('common.tryAgain')}</Button>}>
        {error.hint}
      </Notice>
    )
  } else if (!cards) {
    results = <ResultsSkeleton phone={phone} />
  } else if (cards.length === 0) {
    results = (
      <div className="flex animate-fade flex-col items-center py-14 text-center">
        <Pip pose="search" size={88} />
        <h3 className="mt-4 text-lg font-bold">{text.trim() ? t('addons.nothingMatches', { q: text.trim() }) : t('cmd.emptyPlain')}</h3>
        <button type="button" onClick={clear} className="mt-2 text-sm font-medium text-success-strong hover:underline">
          {t('addons.clearSearch')}
        </button>
      </div>
    )
  } else {
    results = (
      <div className={cn('transition-opacity duration-(--motion-standard) ease-standard', loading && 'opacity-60')} aria-busy={loading}>
        {unanswered.length > 0 && <p className="mb-3 text-[13px] text-muted-foreground">{unanswered.map((n) => n.message).join(' ')}</p>}
        <CardList key={listId} cards={cards} installed={installed} phone={phone} />
        {more && <div ref={sentinel} className="h-1" aria-hidden="true" />}
        {loadingMore && <ResultsSkeleton phone={phone} count={phone ? 2 : 3} className="mt-3" />}
      </div>
    )
  }

  return (
    <section className="flex flex-col gap-4 pb-6" aria-labelledby={phone ? undefined : 'browse-title'}>
      {!phone && (
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="icon-sm" aria-label={t('common.back')} render={<a {...linkProps({ name: 'server', slug: a.server.slug, tab: a.tab })} />}>
            <ArrowLeftIcon />
          </Button>
          <h2 id="browse-title" className="text-lg font-bold tracking-[-0.01em]">
            {a.kind === 'mod' ? t('addons.browseMods') : t('addons.browse')}
          </h2>
          {!picks && <span className="text-[13px] text-muted-foreground">{t('addons.forSoftware', { software: softwareLabel(a.server) })}</span>}
        </div>
      )}
      {filters}
      {picks && <CuratedPicks phone={phone} installed={installed} />}
      {picks && (phone ? <SectionLabel className="-mb-2 px-4">{t(sortKeys[sort])}</SectionLabel> : <h3 className="-mb-1 text-[15px] font-semibold">{t(sortKeys[sort])}</h3>)}
      {results}
    </section>
  )
}

/** Install from a card: straight away when it's one file, else through the detail sheet. */
function useQuickInstall(card: AddonCard) {
  const a = useAddons()
  const [busy, setBusy] = useState(false)
  const key = { source: card.source, projectId: card.projectId }
  const run = async () => {
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
  return { busy, run, open: () => a.openDetail({ key }), blocked: busyReason(a.server) }
}

function CardList({ cards, installed, phone }: { cards: AddonCard[]; installed: (c: AddonCard) => boolean; phone: boolean }) {
  const rows = useListPresence(cards, cardKey)
  return phone ? (
    <ul className="animate-fade overflow-hidden rounded-3xl border border-border bg-white">
      {rows.map((p) => (
        <PhoneCard key={p.key} card={p.item} installed={installed(p.item)} presence={p.state} />
      ))}
    </ul>
  ) : (
    <ul className="grid animate-fade grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
      {rows.map((p) => (
        <DesktopCard key={p.key} card={p.item} installed={installed(p.item)} presence={p.state} />
      ))}
    </ul>
  )
}

function DesktopCard({ card: c, installed, presence }: { card: AddonCard; installed: boolean; presence: Presence }) {
  const q = useQuickInstall(c)
  return (
    <li {...presenceProps(presence)} className="flex min-h-[172px] flex-col rounded-2xl border border-border bg-card p-4 shadow-card transition-shadow duration-(--motion-fast) ease-standard hover:shadow-[0_2px_8px_rgba(29,33,28,0.08)]">
      <button type="button" onClick={q.open} className="-m-1 flex min-w-0 items-start gap-3 rounded-lg p-1 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring">
        <AddonIcon url={c.iconUrl} />
        <span className="min-w-0 pt-0.5">
          <span className="block truncate text-[15px] leading-5 font-semibold">{c.name}</span>
          {c.author && <span className="block truncate text-xs text-muted-foreground">{t('addons.by', { author: c.author })}</span>}
        </span>
      </button>
      <p className="mt-3 line-clamp-2 text-[13px] leading-[18px] text-foreground/80">{c.summary}</p>
      <div className="mt-auto flex items-center justify-between gap-2 pt-4">
        <span className="text-xs text-muted-foreground">{t('addons.downloadsCount', { count: c.downloads, value: compactCount(c.downloads) })}</span>
        {installed ? (
          <Marker tone="green" className="text-[13px]">
            {t('addons.installed')}
          </Marker>
        ) : (
          <Button variant="outline" size="sm" onClick={() => void q.run()} loading={q.busy} disabledReason={q.blocked} aria-label={t('addons.install', { name: c.name })}>
            <DownloadIcon />
            {t('addons.installShort')}
          </Button>
        )}
      </div>
    </li>
  )
}

function PhoneCard({ card: c, installed, presence }: { card: AddonCard; installed: boolean; presence: Presence }) {
  const q = useQuickInstall(c)
  return (
    <li {...presenceProps(presence)} className={phoneCardClass}>
      <button type="button" onClick={q.open} className="flex min-w-0 flex-1 items-center gap-3 text-left">
        <AddonIcon url={c.iconUrl} />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-base leading-5 font-medium">{c.name}</span>
          <span className="line-clamp-2 text-[13px] leading-[18px] text-muted-foreground">{[c.summary, compactCount(c.downloads)].filter(Boolean).join(t('common.dot'))}</span>
        </span>
      </button>
      {installed ? (
        <Marker tone="green" className="text-[13px]">
          {t('addons.installed')}
        </Marker>
      ) : (
        <Button variant="outline" className="h-11 rounded-xl px-3 text-[15px]" onClick={() => void q.run()} loading={q.busy} disabledReason={q.blocked} aria-label={t('addons.install', { name: c.name })}>
          {t('addons.installShort')}
        </Button>
      )}
    </li>
  )
}

const phoneCardClass = 'flex min-h-16 items-center gap-3 border-b border-border px-3 py-2 last:border-b-0'

/** Grey cards, or rows on a phone, where the results will be. */
function ResultsSkeleton({ phone, count = 6, className }: { phone: boolean; count?: number; className?: string }) {
  if (phone) {
    return <ListSkeleton rows={count} face="size-10 rounded-[10px]" trailing={<Skeleton className="h-11 w-[72px] shrink-0 rounded-xl" />} className={cn('overflow-hidden rounded-3xl border border-border bg-white', className)} rowClassName={phoneCardClass} />
  }
  return (
    <div className={cn('grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3', className)}>
      <LoadingLabel />
      {Array.from({ length: count }, (_, i) => (
        <div key={i} className="flex min-h-[172px] flex-col rounded-2xl border border-border bg-card p-4">
          <div className="flex items-center gap-3">
            <Skeleton className="size-10 rounded-[10px]" />
            <div className="flex-1">
              <Skeleton className="h-4 w-28" />
              <Skeleton className="mt-1.5 h-3 w-20" />
            </div>
          </div>
          <Skeleton className="mt-4 h-3 w-full" />
          <Skeleton className="mt-2 h-3 w-2/3" />
          <div className="mt-auto flex items-center justify-between pt-4">
            <Skeleton className="h-3 w-24" />
            <Skeleton className="h-7 w-20 rounded-lg" />
          </div>
        </div>
      ))}
    </div>
  )
}
