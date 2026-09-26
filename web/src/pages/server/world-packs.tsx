import { useId, useRef, useState } from 'react'
import { ChevronRightIcon, EllipsisIcon, PackageIcon, Trash2Icon, UploadIcon } from 'lucide-react'
import { api, ApiError, del, get, post } from '@/api/client'
import type { DataPack, DataPacks, ResourcePack, ResourcePackOffer, ServerStatus } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { Card, CardTitle, Notice, SectionLabel, Spinner } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { ListSkeleton, LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Menu, MenuItem, MenuPopup, MenuTrigger } from '@/components/ui/menu'
import { Sheet, SheetFooter, SheetPanel, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { formatBytes, relativeTime } from '@/lib/format'
import { opLabel, whyNot } from '@/lib/phase'
import { presenceProps, useListPresence, type Presence } from '@/lib/presence'
import { usePoll, type Poll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'
import { maxPackBytes, PhoneActionBar, useZipPicker, WorldSubHeader, ZipDropZone } from './world-sub'

/** A pack's file name as people say it: "Faithful 32x" for "Faithful_32x.zip". */
export function packTitle(fileName: string): string {
  return fileName.replace(/\.zip$/i, '').replace(/_/g, ' ').trim() || fileName
}

/** Whether host only reaches the machine it's used on, so players' games couldn't download a pack from it. */
export function isLocalHost(host: string): boolean {
  const h = host
    .toLowerCase()
    .replace(/^\[(.*)\]$/, '$1')
    .replace(/\.$/, '')
  return h === 'localhost' || h.endsWith('.localhost') || h.startsWith('127.') || h === '::1' || h === '0.0.0.0' || h === '::' || h.startsWith('169.254.') || h.startsWith('fe80:')
}

/** The host players download the offered pack from, when this page is open at another one. */
export function otherHost(offer: ResourcePackOffer, here: string): string | undefined {
  try {
    const host = new URL(offer.url).hostname
    return host.toLowerCase() === here.toLowerCase() ? undefined : host
  } catch {
    return undefined
  }
}

/** Why a file can't be uploaded as a pack, if it can't. */
export function zipProblem(file: File): string | undefined {
  if (!/\.zip$/i.test(file.name)) return t('packs.notZip')
  if (file.size > maxPackBytes) return t('packs.tooBig', { size: formatBytes(maxPackBytes) })
  return undefined
}

/** The second line of the World card's packs row, such as "Faithful 32x · 3 of 4 data packs on". */
export function packsLine(rp: ResourcePack | undefined, dp: DataPacks | undefined): string {
  const parts: string[] = []
  if (rp?.offer) parts.push(rp.problem ? t('packs.rowProblem', { name: packTitle(rp.offer.fileName) }) : packTitle(rp.offer.fileName))
  if (dp && dp.packs.length > 0) {
    const count = dp.packs.length
    parts.push(dp.live ? t('packs.rowDataOn', { on: dp.packs.filter((p) => p.enabled).length, count }) : t('packs.rowData', { count }))
  }
  return parts.length > 0 ? parts.join(t('common.dot')) : t('world.packsHint')
}

function useResourcePack(s: ServerStatus, every: number): Poll<ResourcePack> {
  return usePoll(() => get<ResourcePack>(serverApi(s.id, '/resourcepack')), every, `${s.id}:${s.phase}`)
}

function useDataPacks(s: ServerStatus, every: number): Poll<DataPacks> {
  return usePoll(() => get<DataPacks>(serverApi(s.id, '/datapacks')).then(oldestFirst), every, `${s.id}:${s.phase}`)
}

function oldestFirst(dp: DataPacks): DataPacks {
  const at = (p: DataPack) => Date.parse(p.addedAt) || 0
  return { ...dp, packs: [...dp.packs].sort((a, b) => at(a) - at(b) || a.name.localeCompare(b.name)) }
}

/** packsLine for a server, or undefined until it's known. */
export function usePacksLine(s: ServerStatus): string | undefined {
  const rp = useResourcePack(s, 60_000)
  const dp = useDataPacks(s, 60_000)
  if ((!rp.data && !rp.error) || (!dp.data && !dp.error)) return undefined
  return packsLine(rp.data, dp.data)
}

function failed(e: unknown) {
  toastManager.add({ title: errorText(e), description: e instanceof ApiError ? e.hint : undefined, type: 'error' })
}

function without<T>(set: ReadonlySet<T>, value: T): ReadonlySet<T> {
  const next = new Set(set)
  next.delete(value)
  return next
}

function offerIcon(s: ServerStatus, offer: ResourcePackOffer): string | undefined {
  return offer.icon ? `${serverApi(s.id, '/resourcepack/icon')}?v=${offer.sha1}` : undefined
}

function dataIcon(s: ServerStatus, p: DataPack): string | undefined {
  return p.icon ? `${serverApi(s.id, `/datapacks/${encodeURIComponent(p.name)}/icon`)}?v=${encodeURIComponent(p.addedAt)}` : undefined
}

interface PackSettings {
  required: boolean
  prompt: string
}

function useResourcePackActions(s: ServerStatus, poll: Poll<ResourcePack>) {
  const [uploading, setUploading] = useState(false)
  const [removing, setRemoving] = useState(false)
  const [draft, setDraft] = useState<PackSettings>()
  const latest = useRef<PackSettings | undefined>(undefined)
  const queue = useRef(Promise.resolve())

  async function upload(file: File) {
    const problem = zipProblem(file)
    if (problem) {
      toastManager.add({ title: problem, type: 'error' })
      return
    }
    setUploading(true)
    try {
      await api<ResourcePack>('POST', serverApi(s.id, `/resourcepack?name=${encodeURIComponent(file.name)}`), undefined, file)
      await poll.refresh()
    } catch (e) {
      failed(e)
    } finally {
      setUploading(false)
    }
  }

  /** Shows a settings change at once and saves changes one after another; a failed save puts the saved settings back. */
  function change(offer: ResourcePackOffer, patch: Partial<PackSettings>) {
    const next = { required: offer.required, prompt: offer.prompt ?? '', ...latest.current, ...patch }
    latest.current = next
    setDraft(next)
    queue.current = queue.current.then(async () => {
      if (latest.current !== next) return
      try {
        await post(serverApi(s.id, '/resourcepack/settings'), next)
        await poll.refresh()
      } catch (e) {
        failed(e)
      }
      if (latest.current === next) {
        latest.current = undefined
        setDraft(undefined)
      }
    })
  }

  async function remove() {
    setRemoving(true)
    try {
      await del(serverApi(s.id, '/resourcepack'))
      await poll.refresh()
    } catch (e) {
      failed(e)
    } finally {
      setRemoving(false)
    }
  }

  return { uploading, removing, draft, upload, change, remove }
}

function useDataPackActions(s: ServerStatus, poll: Poll<DataPacks>) {
  const [uploading, setUploading] = useState(false)
  const [flips, setFlips] = useState<ReadonlyMap<string, boolean>>(() => new Map())
  const [gone, setGone] = useState<ReadonlySet<string>>(() => new Set())
  const queue = useRef(Promise.resolve())
  // The server reloads its data for every change, so changes wait their turn.
  const next = (job: () => Promise<void>) => {
    queue.current = queue.current.then(job)
  }

  async function upload(file: File) {
    const problem = zipProblem(file)
    if (problem) {
      toastManager.add({ title: problem, type: 'error' })
      return
    }
    const had = new Set(poll.data?.packs.map((p) => p.name))
    setUploading(true)
    try {
      const out = await api<DataPacks>('POST', serverApi(s.id, `/datapacks?name=${encodeURIComponent(file.name)}`), undefined, file)
      await poll.refresh()
      const name = packTitle(out.added ?? file.name)
      if (out.notEnabled) toastManager.add({ title: t('packs.notEnabled', { name }), description: out.problem, type: 'error' })
      else if (out.added && had.has(out.added)) toastManager.add({ title: t('packs.dataUpdated', { name }), type: 'success' })
    } catch (e) {
      failed(e)
    } finally {
      setUploading(false)
    }
  }

  function toggle(p: DataPack, on: boolean) {
    if (flips.has(p.name)) return
    setFlips((f) => new Map(f).set(p.name, on))
    next(async () => {
      try {
        await post(serverApi(s.id, `/datapacks/${encodeURIComponent(p.name)}/${on ? 'enable' : 'disable'}`))
        await poll.refresh()
      } catch (e) {
        failed(e)
      }
      setFlips((f) => {
        const rest = new Map(f)
        rest.delete(p.name)
        return rest
      })
    })
  }

  function remove(p: DataPack) {
    next(async () => {
      try {
        await del(serverApi(s.id, `/datapacks/${encodeURIComponent(p.name)}`))
        setGone((g) => new Set(g).add(p.name))
        await poll.refresh()
      } catch (e) {
        failed(e)
      }
      setGone((g) => without(g, p.name))
    })
  }

  return { uploading, flips, gone, upload, toggle, remove }
}

/** The data packs to show: a removed one goes as soon as the server has deleted it, before the list reloads. */
function shownPacks(dp: Poll<DataPacks>, data: DataActions): DataPack[] | undefined {
  return dp.data?.packs.filter((p) => !data.gone.has(p.name))
}

type ResourceActions = ReturnType<typeof useResourcePackActions>
type DataActions = ReturnType<typeof useDataPackActions>

interface Gate {
  /** Why nothing can change right now, such as the agent being out of reach or another job running. */
  blocked?: string
  here: string
  /** Why players' games couldn't download a pack from the address this page is open at. */
  local?: string
}

function useGate(s: ServerStatus): Gate {
  const ws = useWorkspace()
  const here = window.location.hostname
  return { blocked: whyNot(s, 'change', ws.stale), here, local: isLocalHost(here) ? t('packs.localHost', { host: here }) : undefined }
}

interface PacksProps {
  server: ServerStatus
  rp: Poll<ResourcePack>
  dp: Poll<DataPacks>
  res: ResourceActions
  data: DataActions
}

export function PacksPage({ server: s }: { server: ServerStatus }) {
  const phone = useIsPhone()
  const rp = useResourcePack(s, 15_000)
  const dp = useDataPacks(s, 15_000)
  const res = useResourcePackActions(s, rp)
  const data = useDataPackActions(s, dp)
  const props: PacksProps = { server: s, rp, dp, res, data }
  return (
    <>
      <WorldSubHeader server={s} title={t('packs.phoneTitle')} />
      {phone ? (
        <PhonePacks {...props} />
      ) : (
        <div className="grid items-stretch gap-4 lg:grid-cols-2">
          <ResourcePackCard {...props} />
          <DataPacksCard {...props} />
        </div>
      )}
    </>
  )
}

/** A pack's own icon, drawn crisp, or a plain box when it has none. */
function PackIcon({ src, size }: { src?: string; size: number }) {
  const [broken, setBroken] = useState<string>()
  if (!src || broken === src) {
    return (
      <span className="grid shrink-0 place-items-center rounded-lg bg-muted text-muted-foreground" style={{ width: size, height: size }} aria-hidden="true">
        <PackageIcon style={{ width: size / 2, height: size / 2 }} />
      </span>
    )
  }
  return <img src={src} width={size} height={size} alt="" draggable={false} onError={() => setBroken(src)} className="shrink-0 rounded-lg [image-rendering:pixelated]" />
}

/** A card's last line: when changes apply, or the job that holds them back. */
function Footnote({ server: s, children, className }: { server: ServerStatus; children: string; className?: string }) {
  const op = s.operation
  const text = op ? opLabel(op, s) : children
  return (
    <p className={className}>
      <span key={text} className="inline-flex animate-fade items-center gap-1.5">
        {op && <Spinner />}
        {text}
      </span>
    </p>
  )
}

/** When the resource pack applies, or that players keep the previous one. */
function resourceFootnote(s: ServerStatus, rp: ResourcePack | undefined): string {
  if (rp?.problem) return t('packs.offeredBefore', { server: s.name })
  return rp?.pending ? t('packs.appliesOnRestart', { server: s.name }) : t('packs.appliesOnJoin')
}

/** Why the server can't offer the stored pack, and how to fix it. */
function OfferProblem({ server: s, problem, className }: { server: ServerStatus; problem?: string; className?: string }) {
  if (!problem) return null
  return (
    <Notice className={className} tone="warning" title={t('packs.cantOffer', { server: s.name })}>
      {problem}
    </Notice>
  )
}

function LoadError({ error, onRetry, className }: { error: ApiError; onRetry: () => Promise<void>; className?: string }) {
  return (
    <Notice
      className={className}
      tone="error"
      title={errorText(error)}
      action={
        <Button variant="outline" size="sm" onClick={() => void onRetry()}>
          {t('common.tryAgain')}
        </Button>
      }
    />
  )
}

function ResourcePackCard({ server: s, rp, res }: PacksProps) {
  const gate = useGate(s)
  const offer = rp.data?.offer
  const view = rp.data ? (offer?.sha1 ?? 'empty') : rp.error ? 'error' : 'loading'
  return (
    <Card>
      <div className="flex min-h-7 items-center">
        <CardTitle>{t('packs.resourcePack')}</CardTitle>
      </div>
      <div key={view} className="flex animate-fade flex-col">
        {view === 'loading' && (
          <>
            <LoadingLabel />
            <div className="mt-4 flex items-center gap-3">
              <Skeleton className="size-10 rounded-lg" />
              <div className="flex-1">
                <Skeleton className="h-4 w-32" />
                <Skeleton className="mt-1.5 h-3 w-44" />
              </div>
            </div>
            <Skeleton className="mt-5 h-4 w-56" />
            <Skeleton className="mt-5 h-8 w-full rounded-lg" />
            <Skeleton className="mt-4 h-[62px] w-full rounded-2xl" />
          </>
        )}
        {view === 'error' && rp.error && <LoadError className="mt-4" error={rp.error} onRetry={rp.refresh} />}
        {offer && <OfferDetails server={s} offer={offer} res={res} gate={gate} />}
        {view === 'empty' && <ZipDropZone tall label={t('packs.drop')} onFile={(f) => void res.upload(f)} busy={res.uploading} disabledReason={gate.blocked ?? gate.local} className="mt-4" />}
      </div>
      <OfferProblem server={s} problem={rp.data?.problem} className="mt-4" />
      {gate.local && rp.data && <p className="mt-3 text-xs text-warning-foreground">{gate.local}</p>}
      <Footnote server={s} className="mt-auto pt-4 text-xs text-muted-foreground">
        {resourceFootnote(s, rp.data)}
      </Footnote>
    </Card>
  )
}

function OfferDetails({ server: s, offer, res, gate }: { server: ServerStatus; offer: ResourcePackOffer; res: ResourceActions; gate: Gate }) {
  const promptId = useId()
  const other = otherHost(offer, gate.here)
  return (
    <>
      <div className="mt-4 flex items-center gap-3">
        <PackIcon src={offerIcon(s, offer)} size={40} />
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-semibold">{packTitle(offer.fileName)}</p>
          <p className="text-xs text-muted-foreground">{t('packs.added', { size: formatBytes(offer.size), time: relativeTime(offer.addedAt) })}</p>
          {other && <p className="truncate text-xs text-muted-foreground">{t('packs.servedFrom', { host: other })}</p>}
        </div>
        <Button variant="destructive-outline" size="sm" onClick={() => void res.remove()} loading={res.removing} disabledReason={gate.blocked}>
          <Trash2Icon />
          {t('common.remove')}
        </Button>
      </div>
      <label className="mt-4 flex cursor-pointer items-center gap-3 self-start text-[13px] font-semibold" title={gate.blocked}>
        <Switch checked={res.draft?.required ?? offer.required} onCheckedChange={(on) => res.change(offer, { required: on })} disabled={!!gate.blocked} />
        {t('packs.mustAccept')}
      </label>
      <label htmlFor={promptId} className="mt-4 self-start text-xs font-semibold">
        {t('packs.prompt')}
      </label>
      <PromptInput key={`${offer.sha1}:${offer.prompt ?? ''}`} id={promptId} offer={offer} onSave={(prompt) => res.change(offer, { prompt })} disabled={!!gate.blocked} className="mt-1.5" />
      <ZipDropZone label={t('packs.replaceDrop')} onFile={(f) => void res.upload(f)} busy={res.uploading} disabledReason={gate.blocked ?? gate.local} className="mt-4" />
    </>
  )
}

/** The message players see with the pack; saved when the field loses focus or on Enter. */
function PromptInput({ offer, onSave, id, disabled, className }: { offer: ResourcePackOffer; onSave: (prompt: string) => void; id: string; disabled: boolean; className?: string }) {
  const [text, setText] = useState(offer.prompt ?? '')
  const save = () => {
    const next = text.trim()
    if (next !== (offer.prompt ?? '')) onSave(next)
  }
  return (
    <Input
      id={id}
      value={text}
      maxLength={200}
      placeholder={t('packs.promptPlaceholder')}
      disabled={disabled}
      className={className}
      onChange={(e) => setText(e.target.value)}
      onBlur={save}
      onKeyDown={(e) => {
        if (e.key === 'Enter') e.currentTarget.blur()
      }}
    />
  )
}

function DataPacksCard({ server: s, dp, data }: PacksProps) {
  const gate = useGate(s)
  const picker = useZipPicker((f) => void data.upload(f))
  const packs = shownPacks(dp, data)
  const rows = useListPresence(packs, (p) => p.name)
  const live = dp.data ? dp.data.live : s.phase === 'online'
  const view = !packs ? (dp.error ? 'error' : 'loading') : rows.length > 0 ? 'list' : 'empty'
  return (
    <Card>
      <div className="flex min-h-7 items-center gap-3">
        <CardTitle className="flex-1">{t('packs.dataPacks')}</CardTitle>
        <Button variant="outline" size="sm" onClick={picker.open} loading={data.uploading} disabledReason={gate.blocked}>
          <UploadIcon />
          {t('packs.uploadData')}
        </Button>
        {picker.input}
      </div>
      <div key={view} className="flex flex-1 animate-fade flex-col">
        {view === 'loading' && <ListSkeleton className="mt-2" rowClassName="flex min-h-[52px] items-center gap-3 border-b border-border py-2 last:border-b-0" face="size-7 rounded-lg" trailing={<Skeleton className="h-[18px] w-[30px] shrink-0 rounded-full" />} />}
        {view === 'error' && dp.error && <LoadError className="mt-4" error={dp.error} onRetry={dp.refresh} />}
        {view === 'list' && (
          <ul className="mt-2">
            {rows.map(({ key, item, state }) => (
              <DataPackRow key={key} server={s} pack={item} state={state} live={live} data={data} gate={gate} />
            ))}
          </ul>
        )}
        {view === 'empty' && (
          <div className="flex items-center gap-4 pt-4">
            <Pip pose="box" size={56} />
            <div>
              <p className="text-sm font-semibold">{t('packs.noData')}</p>
              <p className="mt-0.5 text-xs text-muted-foreground">{t('packs.noDataHint')}</p>
            </div>
          </div>
        )}
      </div>
      <Footnote server={s} className="mt-auto pt-4 text-xs text-muted-foreground">
        {live ? t('packs.appliesNow') : t('packs.appliesOnStart', { server: s.name })}
      </Footnote>
    </Card>
  )
}

function DataPackRow({ server: s, pack: p, state, live, data, gate, phone }: { server: ServerStatus; pack: DataPack; state: Presence; live: boolean; data: DataActions; gate: Gate; phone?: boolean }) {
  const title = packTitle(p.name)
  const pending = data.flips.has(p.name)
  const on = data.flips.get(p.name) ?? p.enabled
  return (
    <li {...presenceProps(state)} className={cn('flex items-center gap-3 border-b border-border last:border-b-0', phone ? 'min-h-12 py-1.5 pr-2 pl-4' : 'py-2')}>
      <PackIcon src={dataIcon(s, p)} size={28} />
      <div className="min-w-0 flex-1">
        <p className={cn('truncate', phone ? 'text-base' : 'text-[13px] font-semibold')}>{title}</p>
        {!phone && p.description && <p className="truncate text-xs text-muted-foreground">{p.description}</p>}
      </div>
      {pending && <Spinner />}
      {live && on !== undefined && <Switch checked={on} onCheckedChange={(v) => data.toggle(p, v)} disabled={!!gate.blocked} title={gate.blocked} aria-label={title} />}
      {p.folder ? (
        <span className="size-8 shrink-0 sm:size-7" aria-hidden="true" />
      ) : (
        <Menu>
          <MenuTrigger disabled={!!gate.blocked} render={<Button variant="ghost" size="icon-sm" className="text-muted-foreground" aria-label={t('packs.menuFor', { name: title })} disabledReason={gate.blocked} />}>
            <EllipsisIcon />
          </MenuTrigger>
          <MenuPopup align="end">
            <MenuItem variant="destructive" onClick={() => data.remove(p)}>
              <Trash2Icon />
              {t('common.remove')}
            </MenuItem>
          </MenuPopup>
        </Menu>
      )}
    </li>
  )
}

function PhonePacks({ server: s, rp, dp, res, data }: PacksProps) {
  const gate = useGate(s)
  const [editing, setEditing] = useState(false)
  const [text, setText] = useState('')
  const resPicker = useZipPicker((f) => void res.upload(f))
  const dataPicker = useZipPicker((f) => void data.upload(f))
  const offer = rp.data?.offer
  const packs = shownPacks(dp, data)
  const rows = useListPresence(packs, (p) => p.name)
  const live = dp.data ? dp.data.live : s.phase === 'online'
  // The usual timing rides in the section labels; a footnote only shows for anything else.
  const resourceTiming = rp.data?.problem || rp.data?.pending ? undefined : t('packs.labelNextJoin')
  const prompt = res.draft?.prompt ?? offer?.prompt ?? ''
  const row = 'flex w-full items-center gap-3 px-4 text-left active:bg-accent/60 disabled:opacity-64 [&>svg]:size-5 [&>svg]:shrink-0'
  const card = 'mt-2 overflow-hidden rounded-3xl border border-border bg-white'
  const uploadBlocked = gate.blocked ?? gate.local

  const upload = (
    <button type="button" className={cn(row, 'min-h-[52px] text-primary')} disabled={!!uploadBlocked || res.uploading} title={uploadBlocked} onClick={resPicker.open}>
      {res.uploading ? <Spinner className="size-5" /> : <UploadIcon aria-hidden="true" />}
      <span className="text-base">{res.uploading ? t('world.uploading') : offer ? t('packs.replace') : t('packs.chooseResource')}</span>
    </button>
  )

  return (
    <div className="flex flex-col gap-5">
      <section aria-labelledby="packs-resource">
        <SectionLabel className="px-4">
          <span id="packs-resource">{t('packs.resourcePack')}</span>
          {resourceTiming && t('common.dot') + resourceTiming}
        </SectionLabel>
        {!rp.data ? (
          rp.error ? (
            <div className={cn(card, 'p-4')}>
              <LoadError error={rp.error} onRetry={rp.refresh} />
            </div>
          ) : (
            <>
              <LoadingLabel />
              <Skeleton className="mt-2 h-[262px] rounded-3xl" />
            </>
          )
        ) : (
          <ul key={offer?.sha1 ?? 'empty'} className={cn(card, 'animate-fade divide-y divide-border')}>
            {offer && (
              <>
                <li className="flex min-h-16 items-center gap-3 px-4 py-2.5">
                  <PackIcon src={offerIcon(s, offer)} size={36} />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-[17px]">{packTitle(offer.fileName)}</span>
                    <span className="block text-[13px] text-muted-foreground">{t('packs.added', { size: formatBytes(offer.size), time: relativeTime(offer.addedAt) })}</span>
                  </span>
                </li>
                <li>
                  <label className={cn(row, 'min-h-[52px] cursor-pointer')} title={gate.blocked}>
                    <span className="min-w-0 flex-1 text-base">{t('packs.mustAcceptShort')}</span>
                    <Switch checked={res.draft?.required ?? offer.required} onCheckedChange={(on) => res.change(offer, { required: on })} disabled={!!gate.blocked} />
                  </label>
                </li>
                <li>
                  <button
                    type="button"
                    className={cn(row, 'min-h-14 py-2')}
                    disabled={!!gate.blocked}
                    title={gate.blocked}
                    onClick={() => {
                      setText(prompt)
                      setEditing(true)
                    }}
                  >
                    <span className="min-w-0 flex-1">
                      <span className="block text-base">{t('packs.prompt')}</span>
                      {prompt && <span className="block truncate text-[13px] text-muted-foreground">{prompt}</span>}
                    </span>
                    <ChevronRightIcon className="text-muted-foreground" aria-hidden="true" />
                  </button>
                </li>
                <li>{upload}</li>
                <li>
                  <button type="button" className={cn(row, 'min-h-[52px] text-destructive-foreground')} disabled={!!gate.blocked || res.removing} title={gate.blocked} onClick={() => void res.remove()}>
                    {res.removing ? <Spinner className="size-5" /> : <Trash2Icon aria-hidden="true" />}
                    <span className="text-base">{t('packs.removeResource')}</span>
                  </button>
                </li>
              </>
            )}
            {!offer && <li>{upload}</li>}
          </ul>
        )}
        <OfferProblem server={s} problem={rp.data?.problem} className="px-4 pt-2" />
        {gate.local && rp.data && <p className="px-4 pt-2 text-[13px] text-warning-foreground">{gate.local}</p>}
        {(s.operation || !resourceTiming) && (
          <Footnote server={s} className="px-4 pt-2 text-[13px] text-muted-foreground">
            {resourceFootnote(s, rp.data)}
          </Footnote>
        )}
      </section>

      <section aria-labelledby="packs-data">
        <SectionLabel className="px-4">
          <span id="packs-data">{t('packs.dataPacks')}</span>
          {live && t('common.dot') + t('packs.labelRightAway')}
        </SectionLabel>
        {!packs ? (
          dp.error ? (
            <div className={cn(card, 'p-4')}>
              <LoadError error={dp.error} onRetry={dp.refresh} />
            </div>
          ) : (
            <ListSkeleton className={card} rowClassName="flex min-h-12 items-center gap-3 border-b border-border py-1.5 pr-2 pl-4 last:border-b-0" face="size-7 rounded-lg" lines={1} trailing={<Skeleton className="h-[22px] w-[38px] shrink-0 rounded-full" />} />
          )
        ) : rows.length > 0 ? (
          <ul className={cn(card, 'animate-fade')}>
            {rows.map(({ key, item, state }) => (
              <DataPackRow key={key} server={s} pack={item} state={state} live={live} data={data} gate={gate} phone />
            ))}
          </ul>
        ) : (
          <div className={cn(card, 'flex animate-fade items-center gap-4 px-4 py-4')}>
            <Pip pose="box" size={48} />
            <div className="min-w-0">
              <p className="text-base font-semibold">{t('packs.noData')}</p>
              <p className="text-[13px] text-muted-foreground">{t('packs.noDataHint')}</p>
            </div>
          </div>
        )}
        {(s.operation || !live) && (
          <Footnote server={s} className="px-4 pt-2 text-[13px] text-muted-foreground">
            {live ? t('packs.appliesNow') : t('packs.appliesOnStart', { server: s.name })}
          </Footnote>
        )}
      </section>

      <PhoneActionBar label={t('packs.phoneTitle')}>
        <Button variant="outline" size="touch" className="w-full" onClick={dataPicker.open} loading={data.uploading} disabledReason={gate.blocked}>
          <UploadIcon />
          {t('packs.addData')}
        </Button>
      </PhoneActionBar>
      {resPicker.input}
      {dataPicker.input}
      {offer && (
        <PromptSheet
          open={editing}
          onOpenChange={setEditing}
          text={text}
          onText={setText}
          onSave={() => {
            if (text.trim() !== (offer.prompt ?? '')) res.change(offer, { prompt: text.trim() })
            setEditing(false)
          }}
        />
      )}
    </div>
  )
}

function PromptSheet({ open, onOpenChange, text, onText, onSave }: { open: boolean; onOpenChange: (open: boolean) => void; text: string; onText: (text: string) => void; onSave: () => void }) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetPopup side="bottom">
        <div className="px-5 pt-3">
          <SheetTitle className="text-lg font-bold">{t('packs.prompt')}</SheetTitle>
        </div>
        <SheetPanel className="px-5 pt-4">
          <Input
            size="lg"
            value={text}
            maxLength={200}
            placeholder={t('packs.promptPlaceholder')}
            aria-label={t('packs.prompt')}
            onChange={(e) => onText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') onSave()
            }}
          />
        </SheetPanel>
        <SheetFooter variant="bare" className="px-5">
          <Button size="touch" className="w-full" onClick={onSave}>
            {t('common.save')}
          </Button>
        </SheetFooter>
      </SheetPopup>
    </Sheet>
  )
}
