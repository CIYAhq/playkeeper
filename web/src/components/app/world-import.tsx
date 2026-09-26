import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent } from 'react'
import { CircleAlertIcon, CircleCheckIcon, CircleXIcon, FileArchiveIcon, FileUpIcon, RefreshCwIcon, UploadIcon } from 'lucide-react'
import { ApiError, del, writeHeaders } from '@/api/client'
import type { ImportMessage, ImportWorld, WorldImport, WorldImportPreview } from '@/api/types'
import { errorText, machineApi } from '@/api/workspace'
import { Notice, Progress } from '@/components/app/bits'
import { CardGroup, ChoiceCard, ChoiceSelect } from '@/components/app/controls'
import { Button } from '@/components/ui/button'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { t, type MessageKey } from '@/i18n'
import { rich } from '@/i18n/rich'
import { formatBytes, formatList, formatPercent, formatSpan } from '@/lib/format'
import { coord, packName } from '@/lib/map'
import { presenceProps, useListPresence } from '@/lib/presence'
import { uploadWorld, UploadSpeed } from '@/lib/upload'
import { cn } from '@/lib/utils'

// Starting a new server from a world someone already has: where it is now
// and how to get it, a resumable upload, then what's inside before anything
// changes.

export type WorldSource = 'singleplayer' | 'aternos' | 'minehut' | 'realms' | 'other'

const worldSources: WorldSource[] = ['singleplayer', 'aternos', 'minehut', 'realms', 'other']

interface SourceTexts {
  name: MessageKey
  from: MessageKey
  guide: MessageKey
  steps: [MessageKey, MessageKey, MessageKey]
  lastPhone: MessageKey
}

const sourceTexts: Record<WorldSource, SourceTexts> = {
  singleplayer: {
    name: 'import.source.singleplayer',
    from: 'import.from.singleplayer',
    guide: 'import.guide.singleplayer',
    steps: ['import.singleplayer.1', 'import.singleplayer.2', 'import.singleplayer.3'],
    lastPhone: 'import.singleplayer.3Phone',
  },
  aternos: { name: 'import.source.aternos', from: 'import.from.aternos', guide: 'import.guide.aternos', steps: ['import.aternos.1', 'import.aternos.2', 'import.aternos.3'], lastPhone: 'import.aternos.3Phone' },
  minehut: { name: 'import.source.minehut', from: 'import.from.minehut', guide: 'import.guide.minehut', steps: ['import.minehut.1', 'import.minehut.2', 'import.minehut.3'], lastPhone: 'import.minehut.3Phone' },
  realms: { name: 'import.source.realms', from: 'import.from.realms', guide: 'import.guide.realms', steps: ['import.realms.1', 'import.realms.2', 'import.realms.3'], lastPhone: 'import.realms.3Phone' },
  other: { name: 'import.source.other', from: 'import.from.other', guide: 'import.guide.other', steps: ['import.other.1', 'import.other.2', 'import.other.3'], lastPhone: 'import.other.3Phone' },
}

/** Where the world came from, for the summary: "Singleplayer", "Aternos". */
export function sourceFrom(s: WorldSource): string {
  return t(sourceTexts[s].from)
}

// Uploading

export type WorldUploadState =
  | { phase: 'idle' }
  | { phase: 'uploading'; files: File[]; sent: number; total: number; retrying: boolean; secondsLeft?: number }
  | { phase: 'done'; files: File[]; total: number; upload: WorldImport }
  | { phase: 'failed'; files: File[]; sent: number; total: number; error: string }

export interface WorldUpload {
  state: WorldUploadState
  start: (files: File[]) => void
  retry: () => void
  cancel: () => void
  /** A server was made from the upload, so leaving the page keeps it. */
  keep: () => void
}

/**
 * One world upload to a machine. Leaving the page, by reloading or closing
 * it too, stops it and deletes what arrived, unless a server was made from it.
 */
export function useWorldUpload(machineId: string | undefined): WorldUpload {
  const [state, setState] = useState<WorldUploadState>({ phase: 'idle' })
  const job = useRef<{ ctl: AbortController; files: File[]; upload?: WorldImport }>(undefined)
  const kept = useRef(false)

  /** Stops the upload and deletes it. leaving: the page is going away, so the request must outlive it. */
  const drop = useCallback(
    (leaving = false) => {
      const j = job.current
      job.current = undefined
      if (!j) return
      j.ctl.abort()
      if (!j.upload || !machineId || kept.current) return
      const path = machineApi(machineId, `/world-imports/${j.upload.id}`)
      if (leaving) void fetch(path, { method: 'DELETE', keepalive: true, headers: writeHeaders(), credentials: 'same-origin' }).catch(() => undefined)
      else void del(path).catch(() => undefined)
    },
    [machineId],
  )

  const run = useCallback(
    (files: File[], resume?: WorldImport) => {
      if (!machineId) return
      const j = { ctl: new AbortController(), files, upload: resume }
      job.current = j
      const total = files.reduce((n, f) => n + f.size, 0)
      const speed = new UploadSpeed()
      let sent = 0
      setState({ phase: 'uploading', files, sent, total, retrying: false })
      uploadWorld({
        base: machineApi(machineId, '/world-imports'),
        files,
        signal: j.ctl.signal,
        resume,
        onStart: (imp) => {
          j.upload = imp
        },
        onProgress: (p) => {
          if (j.ctl.signal.aborted) return
          sent = p.sent
          if (p.retrying) speed.reset()
          else speed.add(p.sent, Date.now())
          setState({ phase: 'uploading', files, sent, total: p.total, retrying: p.retrying, secondsLeft: speed.secondsLeft(p.total) })
        },
      }).then(
        (upload) => {
          if (j.ctl.signal.aborted) return
          j.upload = upload
          setState({ phase: 'done', files, total, upload })
        },
        (e: unknown) => {
          if (!j.ctl.signal.aborted) setState({ phase: 'failed', files, sent, total, error: errorText(e) })
        },
      )
    },
    [machineId],
  )

  const start = useCallback(
    (files: File[]) => {
      drop()
      kept.current = false
      run(files)
    },
    [drop, run],
  )
  const retry = useCallback(() => {
    const j = job.current
    if (j) run(j.files, j.upload)
  }, [run])
  const cancel = useCallback(() => {
    drop()
    setState({ phase: 'idle' })
  }, [drop])
  const keep = useCallback(() => {
    kept.current = true
  }, [])

  useEffect(() => {
    // React doesn't unmount on a reload or a closed tab, so the page says it's going.
    const leave = () => {
      drop(true)
      setState({ phase: 'idle' })
    }
    window.addEventListener('pagehide', leave)
    return () => {
      window.removeEventListener('pagehide', leave)
      drop()
    }
  }, [drop])
  return { state, start, retry, cancel, keep }
}

const archiveName = /\.(zip|tar\.gz|tgz|tar)$/i

/** The upload's name for the summary: its first file without the extension. */
export function uploadName(files: File[]): string {
  return (files[0]?.name ?? '').replace(archiveName, '')
}

export interface CheckError {
  title: string
  hint?: string
}

export function checkError(e: unknown): CheckError {
  return { title: errorText(e), hint: e instanceof ApiError ? e.hint : undefined }
}

function CheckErrorNotice({ error }: { error: CheckError }) {
  return (
    <Notice tone="error" title={error.title} className="animate-fade">
      {error.hint}
    </Notice>
  )
}

// Step 1 with "A world"

export function WorldSourceStep({ source, onSource, upload, phone, error }: { source: WorldSource; onSource: (s: WorldSource) => void; upload: WorldUpload; phone: boolean; error?: CheckError }) {
  const texts = sourceTexts[source]
  const steps = phone ? [texts.steps[0], texts.steps[1], texts.lastPhone] : texts.steps
  return (
    <div className="flex flex-col gap-3.5">
      <div>
        <h3 id="world-source" className="text-[15px] font-semibold max-sm:text-[13px] max-sm:font-medium">
          {phone ? t('import.wherePhone') : t('import.where')}
        </h3>
        {phone ? (
          <ChoiceSelect
            value={source}
            onChange={onSource}
            options={worldSources.map((s) => ({ value: s, label: t(sourceTexts[s].name) }))}
            label={t('import.wherePhone')}
            className="mt-2 min-h-11 w-full justify-between rounded-2xl border border-border bg-white px-4 text-base"
          />
        ) : (
          <ToggleGroup value={[source]} onValueChange={(v) => v[0] && onSource(v[0] as WorldSource)} aria-labelledby="world-source" className="mt-2.5 grid w-full grid-cols-5 gap-2">
            {worldSources.map((s) => (
              <ToggleGroupItem
                key={s}
                value={s}
                className="h-11 rounded-xl border border-border bg-card px-2 text-[13px] font-medium text-foreground hover:border-input hover:bg-card data-pressed:border-primary/55 data-pressed:bg-selected data-pressed:font-semibold data-pressed:shadow-selected sm:h-11 sm:text-[13px]"
              >
                {t(sourceTexts[s].name)}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
        )}
      </div>
      <div key={source} className="animate-fade rounded-2xl border border-border bg-card p-4">
        <h3 className="text-[15px] font-semibold max-sm:text-base">{t(texts.guide)}</h3>
        <ol className="mt-2 flex flex-col gap-1.5">
          {steps.map((k, i) => (
            <li key={k} className="flex gap-2.5 text-[13px] leading-5 max-sm:text-[15px] max-sm:leading-[22px]">
              <span className="w-4 shrink-0 font-semibold text-success-strong tabular-nums">{i + 1}.</span>
              <span>{t(k)}</span>
            </li>
          ))}
        </ol>
      </div>
      <UploadBox upload={upload} phone={phone} />
      {error && <CheckErrorNotice error={error} />}
    </div>
  )
}

function UploadBox({ upload, phone }: { upload: WorldUpload; phone: boolean }) {
  const [over, setOver] = useState(false)
  const [wrong, setWrong] = useState(false)
  const input = useRef<HTMLInputElement>(null)
  const s = upload.state

  function take(list: FileList | null | undefined) {
    const files = Array.from(list ?? [])
    if (input.current) input.current.value = ''
    if (files.length === 0) return
    const ok = files.every((f) => archiveName.test(f.name))
    setWrong(!ok)
    if (ok) upload.start(files)
  }

  const picker = <input ref={input} type="file" multiple accept=".zip,.tar,.tgz,.gz" className="sr-only" tabIndex={-1} aria-label={t('import.choose')} onChange={(e) => take(e.target.files)} />
  const wrongType = wrong && (
    <p className="mt-2 text-xs text-destructive-foreground max-sm:text-[13px]" role="alert">
      {t('import.wrongType')}
    </p>
  )

  if (s.phase === 'idle') {
    if (phone) {
      return (
        <div>
          <Button variant="outline" size="touch" className="w-full bg-white text-[17px]" onClick={() => input.current?.click()}>
            <FileUpIcon />
            {t('import.choose')}
          </Button>
          {wrongType}
          {picker}
        </div>
      )
    }
    return (
      <div
        onDragOver={(e) => {
          e.preventDefault()
          setOver(true)
        }}
        onDragLeave={() => setOver(false)}
        onDrop={(e: DragEvent) => {
          e.preventDefault()
          setOver(false)
          take(e.dataTransfer.files)
        }}
        className={cn('flex min-h-[132px] flex-col items-center justify-center rounded-2xl border border-dashed border-input bg-warm px-6 py-8 text-center transition-colors duration-(--motion-fast) ease-standard', over && 'border-primary bg-selected')}
      >
        <UploadIcon className="size-5 text-primary" aria-hidden="true" />
        <p className="mt-3 text-sm font-semibold">{t('import.drop')}</p>
        <p className="mt-1 text-xs text-muted-foreground">
          {rich('import.orChoose', {
            choose: (chunk) => (
              <button type="button" className="font-semibold text-primary hover:underline" onClick={() => input.current?.click()}>
                {chunk}
              </button>
            ),
          })}
        </p>
        {wrongType}
        {picker}
      </div>
    )
  }

  const name = formatList(s.files.map((f) => f.name))
  const percent = s.phase === 'done' ? 100 : s.total > 0 ? Math.floor((s.sent / s.total) * 100) : 0
  const size = formatBytes(s.total)
  const detail =
    s.phase === 'done' ? t('import.uploaded', { size }) : s.phase === 'uploading' && !s.retrying && s.secondsLeft !== undefined ? t('import.left', { size, time: formatSpan(s.secondsLeft) }) : t('import.sizeOnly', { size })
  return (
    <div className="animate-fade rounded-2xl border border-dashed border-input bg-warm p-4">
      <div className="flex items-center gap-3">
        {s.phase === 'done' ? <CircleCheckIcon className="size-5 shrink-0 text-success-foreground" aria-hidden="true" /> : <FileArchiveIcon className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />}
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-semibold max-sm:text-[15px]">{name}</p>
          <p className="truncate text-xs text-muted-foreground tabular-nums max-sm:text-[13px]">{detail}</p>
        </div>
        {s.phase !== 'failed' && <span className="text-sm font-semibold tabular-nums">{formatPercent(percent)}</span>}
        <Button variant="ghost" size={phone ? 'default' : 'sm'} onClick={upload.cancel}>
          {t('common.cancel')}
        </Button>
      </div>
      <Progress value={percent} tone={s.phase === 'done' ? 'primary' : 'info'} className="mt-3" label={name} />
      {s.phase === 'failed' ? (
        <div className="mt-2.5 flex items-center gap-3">
          <p className="min-w-0 flex-1 text-xs text-destructive-foreground max-sm:text-[13px]" role="alert">
            {s.error}
          </p>
          <Button variant="outline" size="sm" onClick={upload.retry}>
            <RefreshCwIcon />
            {t('common.tryAgain')}
          </Button>
        </div>
      ) : (
        s.phase === 'uploading' && (
          <p className="mt-2.5 text-xs text-muted-foreground max-sm:text-[13px]" role="status">
            {s.retrying ? t('import.retrying') : t('import.resumes')}
          </p>
        )
      )}
    </div>
  )
}

// Step 2 with "A world": what's inside

/** The world to preview first: the one the upload names, else the only or the largest one. */
export function defaultWorld(imp: WorldImport): string | undefined {
  const worlds = imp.inspection?.worlds ?? []
  const named = worlds.filter((w) => w.default)
  if (named.length === 1) return named[0]?.id
  return [...worlds].sort((a, b) => b.sizeBytes - a.sizeBytes)[0]?.id
}

/** A world's own name: its level name, else its folder, else its archive. */
export function worldName(w: ImportWorld): string {
  return w.level?.name?.trim() || w.path.split('/').filter(Boolean).pop() || w.archive
}

/** A server name from a world's name: no colour codes or control characters, at most 32 characters. */
export function serverNameFrom(name: string): string {
  const clean = name
    .replace(/§./g, '')
    .replace(/[\p{Cc}\p{Cf}]/gu, '')
    .trim()
  return Array.from(clean).slice(0, 32).join('').trim()
}

// Minecraft's default world names say nothing about the server.
const genericNames = new Set(['world', 'new world'])

/** A name for the new server: the world's own, else the file's, skipping "world" when there's better. */
export function suggestedName(imp: WorldImport, world: string | undefined, files: File[]): string {
  const w = imp.inspection?.worlds.find((x) => x.id === world)
  const names = [w ? worldName(w) : '', uploadName(files)].map(serverNameFrom).filter(Boolean)
  return names.find((n) => !genericNames.has(n.toLowerCase())) ?? names[0] ?? ''
}

// Warnings the check screen shows some other way, or that only say how Playkeeper lays the files out.
const quietKinds = new Set(['world_upgrade', 'paper_files_moved', 'bukkit_split', 'layout_upgrade', 'dimensions_merged', 'stale_dimension', 'addons_kept', 'operators_kept'])

const dimensionIds = { overworld: 'minecraft:overworld', nether: 'minecraft:the_nether', end: 'minecraft:the_end' }

type RowTone = 'ok' | 'note' | 'problem'

interface CheckRow {
  tone: RowTone
  title: string
  detail?: string
}

function sentence(s: string): string {
  return s.charAt(0).toLocaleUpperCase() + s.slice(1)
}

function packLabel(id: string): string {
  const words = packName(id).replace(/[-_\s]+/g, ' ').trim()
  return words ? sentence(words) : packName(id)
}

/** What the check screen lists: the world, data packs, plugins, the version it was made in, then warnings and problems. */
export function checkRows(check: WorldImportPreview, upload?: WorldImport): CheckRow[] {
  const p = check.preview
  const ids = p.dimensions.map((d) => d.id)
  const vanilla = Object.values(dimensionIds)
  const parts: string[] = []
  if (ids.includes(dimensionIds.overworld)) parts.push(t('import.dimWorld'))
  if (ids.includes(dimensionIds.nether)) parts.push(t('import.dimNether'))
  if (ids.includes(dimensionIds.end)) parts.push(t('import.dimEnd'))
  const custom = new Set(ids.filter((id) => !vanilla.includes(id))).size
  if (custom > 0) parts.push(t('import.dimCustom', { count: custom }))
  const spawn = p.world.level?.spawn
  const size = formatBytes(p.sizeBytes)
  const rows: CheckRow[] = [
    {
      tone: 'ok',
      title: sentence(formatList(parts.length > 0 ? parts : [t('import.dimWorld')])),
      detail: spawn ? t('import.worldDetail', { size, x: coord(spawn.x), z: coord(spawn.z) }) : t('import.worldDetailNoSpawn', { size }),
    },
  ]
  const packs = (p.dataPacks ?? []).filter((id) => id.startsWith('file/')).map(packLabel)
  if (packs.length > 0) rows.push({ tone: 'ok', title: t('import.packs', { count: packs.length }), detail: t('import.packsDetail', { names: formatList(packs) }) })
  const addons = p.leftOut?.some((l) => l.kind === 'addons')
  rows.push({ tone: 'note', title: addons ? t('import.pluginsLeft') : t('import.noPlugins'), detail: t('import.pluginsHint') })
  const made = p.world.level?.version
  rows.push({ tone: p.version?.compat === 'same' ? 'ok' : 'note', title: made ? t('import.madeIn', { version: made }) : t('import.madeInUnknown') })
  const seen = new Set<string>()
  const notes = (list: ImportMessage[] | undefined, tone: RowTone) => {
    for (const m of list ?? []) {
      if ((tone === 'note' && quietKinds.has(m.kind)) || seen.has(m.text)) continue
      seen.add(m.text)
      rows.push({ tone, title: m.text, detail: m.hint })
    }
  }
  notes(upload?.inspection?.warnings, 'note')
  notes(p.warnings, 'note')
  notes(p.problems, 'problem')
  return rows
}

function RowIcon({ tone }: { tone: RowTone }) {
  switch (tone) {
    case 'ok':
      return <CircleCheckIcon className="mt-px size-[18px] shrink-0 text-success-foreground" aria-hidden="true" />
    case 'note':
      return <CircleAlertIcon className="mt-px size-[18px] shrink-0 text-warning" aria-hidden="true" />
    case 'problem':
      return <CircleXIcon className="mt-px size-[18px] shrink-0 text-destructive" aria-hidden="true" />
    default: {
      const unreachable: never = tone
      return unreachable
    }
  }
}

export function WorldCheck({
  check,
  upload,
  world,
  onWorld,
  onVersion,
  busy,
  phone,
  error,
}: {
  check: WorldImportPreview
  upload: WorldImport
  world?: string
  onWorld: (id: string) => void
  onVersion: (id: string) => void
  busy: boolean
  phone: boolean
  error?: CheckError
}) {
  const worlds = upload.inspection?.worlds ?? []
  const versions = check.versions ?? []
  const upgrade = versions.find((v) => !v.keep)
  const keep = versions.find((v) => v.keep)
  const found = useMemo(() => checkRows(check, upload), [check, upload])
  const rows = useListPresence(found, (r) => `${r.tone}:${r.title}`)
  const checking = busy ? t('reason.busy', { what: t('import.checking') }) : undefined
  return (
    <div className="flex animate-fade flex-col gap-4">
      <div>
        <h2 className={cn(phone ? 'text-[26px] leading-8 font-extrabold tracking-[-0.02em]' : 'text-lg font-bold')}>{t('import.insideTitle', { file: check.preview.world.archive })}</h2>
        <p className="mt-0.5 text-[13px] text-muted-foreground max-sm:text-[15px]">{t('import.nothingChanged')}</p>
      </div>
      {worlds.length > 1 && (
        <section>
          <h3 className="mb-2 text-[15px] font-semibold">{t('import.whichWorld')}</h3>
          <CardGroup value={world ?? ''} onChange={onWorld} label={t('import.whichWorld')} className="flex flex-col gap-2">
            {worlds.map((w) => (
              <ChoiceCard key={w.id} value={w.id} radio="start" className="gap-3 px-4 py-3">
                <span className="block text-sm font-semibold">{worldName(w)}</span>
                <span className="block text-xs text-muted-foreground">{t('import.sizeOnly', { size: formatBytes(w.sizeBytes) })}</span>
              </ChoiceCard>
            ))}
          </CardGroup>
        </section>
      )}
      <ul className={cn('flex flex-col rounded-2xl border border-border bg-card px-4 transition-opacity', busy && 'opacity-60')} aria-busy={busy}>
        {rows.map(({ key, item: r, state }) => (
          <li key={key} {...presenceProps(state)} className="flex gap-3 border-b border-border py-3.5 last:border-b-0">
            <RowIcon tone={r.tone} />
            <span className="min-w-0 flex-1">
              <span className={cn('block text-sm max-sm:text-[15px]', r.tone === 'ok' || r.title.length < 60 ? 'font-semibold' : 'font-medium')}>{r.title}</span>
              {r.detail && <span className="mt-0.5 block text-xs text-muted-foreground max-sm:text-[13px]">{r.detail}</span>}
            </span>
          </li>
        ))}
      </ul>
      {upgrade && keep && (
        <section>
          <h3 className="mb-2 text-[15px] font-semibold">{t('import.whichVersion')}</h3>
          <CardGroup value={check.versionId} onChange={onVersion} label={t('import.whichVersion')} className="flex flex-col gap-2">
            <ChoiceCard value={upgrade.id} radio="start" disabled={busy} reason={checking} className="gap-3 px-4 py-3">
              <span className="flex items-baseline gap-2">
                <span className="text-sm font-semibold">{t('import.upgradeTo', { version: upgrade.minecraftVersion })}</span>
                <span className="text-xs text-muted-foreground">{t('common.recommended')}</span>
              </span>
              <span className="block text-xs text-muted-foreground max-sm:text-[13px]">{t('import.upgradeHint')}</span>
            </ChoiceCard>
            <ChoiceCard value={keep.id} radio="start" disabled={busy} reason={checking} className="gap-3 px-4 py-3">
              <span className="block text-sm font-semibold">{t('import.keep', { version: keep.minecraftVersion })}</span>
              <span className="block text-xs text-muted-foreground max-sm:text-[13px]">{t('import.keepHint', { version: keep.minecraftVersion })}</span>
            </ChoiceCard>
          </CardGroup>
        </section>
      )}
      {error && <CheckErrorNotice error={error} />}
    </div>
  )
}

/** The summary's version: "1.21.4 → 26.1.2" for an upgrade, else the one version. */
export function versionChange(check: WorldImportPreview): string {
  const target = check.preview.target.minecraftVersion
  const from = check.preview.world.level?.version
  return from && from !== target && check.preview.version?.compat === 'upgrade' ? t('import.versionChange', { from, to: target }) : target
}
