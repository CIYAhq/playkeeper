import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { ArchiveIcon, CircleArrowUpIcon, RotateCwIcon, SaveIcon, SquareIcon, Trash2Icon, UploadIcon } from 'lucide-react'
import { useCatalog } from '@/api/catalog'
import { api, ApiError, get, post } from '@/api/client'
import type { Backup, CatalogEntry, Difficulty, GameMode, Gameplay, MemoryAdvice, ServerStatus } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { Emblem, Pip } from '@/components/app/art'
import { Card, CardHint, CardTitle, Progress, SectionLabel } from '@/components/app/bits'
import { ChoiceSelect, SettingRow, useIsPhone, type Choice } from '@/components/app/controls'
import { InlineSkeleton } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogDescription, DialogFooter, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { NumberField, NumberFieldDecrement, NumberFieldGroup, NumberFieldIncrement, NumberFieldInput } from '@/components/ui/number-field'
import { Slider } from '@/components/ui/slider'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { rich } from '@/i18n/rich'
import { formatDate, formatMB, localTimeZone } from '@/lib/format'
import { memoryAdviceLine, memoryOffers, memoryOptionHint, memoryProgress } from '@/lib/memory'
import { busyReason, whyNot } from '@/lib/phase'
import { navigate } from '@/lib/router'
import { iconURL, newerStable, typeName } from '@/lib/servers'
import { cn } from '@/lib/utils'
import { usePoll } from '@/lib/usePoll'
import { upgradeTargets } from '@/lib/versions'
import { serverAction } from '.'

interface Draft {
  name: string
  motd: string
  maxPlayers: number
  memoryMB: number
  difficulty: Difficulty
  pvp: boolean
  gameMode: GameMode
  viewDistance: number
}

function baseOf(s: ServerStatus): Draft {
  const g = s.gameplay
  return {
    name: s.name,
    motd: s.config?.motd ?? '',
    maxPlayers: s.config?.maxPlayers ?? 10,
    memoryMB: s.config?.memoryMB ?? 0,
    difficulty: g.difficulty ?? 'easy',
    pvp: g.pvp ?? true,
    gameMode: g.gameMode ?? 'survival',
    viewDistance: g.viewDistance ?? 10,
  }
}

const sections = [
  { id: 'game', key: 'settings.game' },
  { id: 'list', key: 'settings.list' },
  { id: 'memory', key: 'settings.memory' },
  { id: 'version', key: 'settings.version' },
  { id: 'danger', key: 'settings.danger' },
] as const

const sectionIds = sections.map((x) => x.id)

/**
 * A change another page asked for, like "Give it 6 GB" on How it's running:
 * ?memory=6144 or ?view=10. It shows as an unsaved change, never saved by itself.
 */
function askedFor(): { memoryMB?: number; viewDistance?: number } {
  const q = new URLSearchParams(window.location.search)
  const memory = Number(q.get('memory'))
  const view = Number(q.get('view'))
  return {
    memoryMB: Number.isInteger(memory) && memory > 0 ? memory : undefined,
    viewDistance: Number.isInteger(view) && view >= 3 && view <= 32 ? view : undefined,
  }
}

/**
 * The section at the top of the screen, for the settings nav. The section in
 * the address wins while it is near the top: near the end of the page it
 * can't scroll all the way up, so the one above it is still in view.
 */
function useActiveSection(ids: readonly string[]): string | undefined {
  const asked = useCallback(() => {
    const hash = window.location.hash.slice(1)
    return ids.includes(hash) ? hash : undefined
  }, [ids])
  const [active, setActive] = useState<string | undefined>(() => asked() ?? ids[0])
  useEffect(() => {
    if (typeof IntersectionObserver === 'undefined') return
    const visible = new Set<string>()
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (e.isIntersecting) visible.add(e.target.id)
          else visible.delete(e.target.id)
        }
        const want = asked()
        const next = want && visible.has(want) ? want : ids.find((id) => visible.has(id))
        if (next) setActive(next)
      },
      { rootMargin: '0px 0px -65% 0px' },
    )
    for (const id of ids) {
      const el = document.getElementById(id)
      if (el) io.observe(el)
    }
    const onHash = () => {
      const want = asked()
      if (want) setActive(want)
    }
    window.addEventListener('hashchange', onHash)
    return () => {
      io.disconnect()
      window.removeEventListener('hashchange', onHash)
    }
  }, [ids, asked])
  return active
}

export function difficultyChoices(): Choice<Difficulty>[] {
  return (['peaceful', 'easy', 'normal', 'hard'] as const).map((d) => ({ value: d, label: t(`settings.difficulty.${d}`), hint: t(`settings.difficulty.${d}.hint`) }))
}

export function modeChoices(): Choice<GameMode>[] {
  return (['survival', 'creative', 'adventure', 'spectator'] as const).map((m) => ({ value: m, label: t(`settings.mode.${m}`), hint: t(`settings.mode.${m}.hint`) }))
}

const maxIconBytes = 64 * 1024
const pngSignature = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]

/** Whether a PNG's header says 64 × 64 and it fits in 64 KB, which is all the agent keeps. */
async function isIcon(b: Blob): Promise<boolean> {
  if (b.size > maxIconBytes) return false
  const head = new DataView(await b.slice(0, 24).arrayBuffer())
  return head.byteLength === 24 && pngSignature.every((v, i) => head.getUint8(i) === v) && head.getUint32(12) === 0x49484452 && head.getUint32(16) === 64 && head.getUint32(20) === 64
}

/** Draws a picture onto a 64 × 64 PNG, cropped to a square from the middle. */
async function iconPNG(file: File): Promise<{ blob: Blob; url: string }> {
  const bitmap = await createImageBitmap(file)
  const canvas = document.createElement('canvas')
  canvas.width = 64
  canvas.height = 64
  const ctx = canvas.getContext('2d')
  if (!ctx) throw new Error(t('settings.iconBad'))
  const side = Math.min(bitmap.width, bitmap.height)
  ctx.imageSmoothingQuality = 'high'
  ctx.drawImage(bitmap, (bitmap.width - side) / 2, (bitmap.height - side) / 2, side, side, 0, 0, 64, 64)
  const url = canvas.toDataURL('image/png')
  const blob = await new Promise<Blob>((resolve, reject) => canvas.toBlob((b) => (b ? resolve(b) : reject(new Error(t('settings.iconBad')))), 'image/png'))
  return { blob, url }
}

export function ServerSettingsPage({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const base = useMemo(() => baseOf(s), [s])
  const [asked, setAsked] = useState(askedFor)
  const [draft, setDraft] = useState<Partial<Draft>>({})
  const [saving, setSaving] = useState(false)
  const { catalog } = useCatalog(ws.machine?.id, { server: s.id, fresh: true })
  const memoryPoll = usePoll(() => get<MemoryAdvice>(serverApi(s.id, `/memory?tz=${encodeURIComponent(localTimeZone())}`)), 300_000, s.id)
  const advice = memoryPoll.data
  const offers = memoryOffers(base.memoryMB, advice, catalog)
  const linked: Partial<Draft> = {}
  if (asked.viewDistance !== undefined) linked.viewDistance = asked.viewDistance
  const askedMB = asked.memoryMB
  if (askedMB !== undefined && offers.some((o) => o.memoryMB === askedMB && o.fits)) linked.memoryMB = askedMB
  const edits: Partial<Draft> = { ...linked, ...draft }
  const v = { ...base, ...edits }
  const changed = (k: keyof Draft) => k in edits && edits[k] !== base[k]
  const keys = (Object.keys(edits) as (keyof Draft)[]).filter(changed)
  const restartNeeded = keys.some((k) => k !== 'name')
  const online = !ws.stale && s.phase === 'online'
  const set = <K extends keyof Draft>(k: K, value: Draft[K]) => setDraft((d) => ({ ...d, [k]: value }))
  const discard = () => {
    setDraft({})
    setAsked({})
  }
  const active = useActiveSection(sectionIds)
  const hash = window.location.hash
  useEffect(() => {
    if (hash) document.getElementById(hash.slice(1))?.scrollIntoView({ block: 'start' })
  }, [hash])

  async function save() {
    setSaving(true)
    const body: Record<string, unknown> = {}
    const gameplay: Gameplay = {}
    for (const k of keys) {
      if (k === 'difficulty' || k === 'pvp' || k === 'gameMode' || k === 'viewDistance') Object.assign(gameplay, { [k]: v[k] })
      else if (k === 'name') body.name = v.name.trim()
      else body[k] = v[k]
    }
    if (Object.keys(gameplay).length) body.gameplay = gameplay
    const restart = restartNeeded && online
    if (restart) body.restart = true
    try {
      await post(serverApi(s.id, '/settings'), body)
      discard()
      toastManager.add({ title: restart ? t('settings.savedRestartToast', { server: v.name }) : t('settings.savedToast'), type: 'success' })
      await ws.refresh()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setSaving(false)
    }
  }

  const memoryChoices: Choice<string>[] = offers.map((o) => ({ value: String(o.memoryMB), label: formatMB(o.memoryMB), hint: memoryOptionHint(o, advice, ws.machineName), disabled: !o.fits }))
  const progress = advice && memoryProgress(advice)
  const memoryHint = advice ? (
    <>
      {memoryAdviceLine(advice, ws.machineName)}
      {advice.verdict !== 'not_enough_data' && advice.days.length > 0 && <MemoryDays advice={advice} />}
      {progress && (
        <div className="mt-2 flex items-center gap-3">
          <Progress value={(progress.day / progress.of) * 100} className="w-[140px]" label={t('settings.memoryDay', progress)} />
          <span className="text-xs font-medium text-foreground" aria-hidden="true">
            {t('settings.memoryDay', progress)}
          </span>
        </div>
      )}
    </>
  ) : memoryPoll.loading ? (
    <InlineSkeleton className="w-72 max-w-full" />
  ) : undefined

  const game = (
    <>
      <SettingRow label={t('settings.difficulty')} changed={changed('difficulty')} control={<ChoiceSelect value={v.difficulty} onChange={(d) => set('difficulty', d)} options={difficultyChoices()} label={t('settings.difficulty')} />} />
      <SettingRow
        label={t('settings.pvp')}
        hint={phone ? t('settings.pvpHintShort') : t('settings.pvpHint')}
        changed={changed('pvp')}
        control={
          <label className="flex items-center gap-2.5 text-[13px] text-muted-foreground">
            {!phone && (v.pvp ? t('common.on') : t('common.off'))}
            <Switch checked={v.pvp} onCheckedChange={(c) => set('pvp', c)} aria-label={t('settings.pvp')} />
          </label>
        }
      />
      {(!phone || 'viewDistance' in edits) && (
        <SettingRow
          wide={phone}
          label={t('settings.view')}
          hint={t('settings.viewHint')}
          changed={changed('viewDistance')}
          control={
            <div className="flex w-[296px] items-center gap-4 max-sm:w-full">
              <Slider className="min-w-0 flex-1" value={v.viewDistance} onValueChange={(n) => set('viewDistance', Array.isArray(n) ? (n[0] ?? 10) : n)} min={3} max={32} step={1} aria-label={t('settings.view')} />
              <span className="w-[76px] shrink-0 text-right text-[13px] font-semibold tabular-nums">{t('unit.chunks', { count: v.viewDistance })}</span>
            </div>
          }
        />
      )}
      <SettingRow
        label={t('settings.maxPlayers')}
        changed={changed('maxPlayers')}
        control={
          <NumberField value={v.maxPlayers} onValueChange={(n) => n !== null && set('maxPlayers', n)} min={1} max={100} step={1}>
            <NumberFieldGroup className="w-[132px]">
              <NumberFieldDecrement aria-label={t('common.decrease')} />
              <NumberFieldInput className="text-center tabular-nums" aria-label={t('settings.maxPlayers')} />
              <NumberFieldIncrement aria-label={t('common.increase')} />
            </NumberFieldGroup>
          </NumberField>
        }
      />
      <SettingRow label={t('settings.mode')} hint={phone ? t('settings.modeHintShort') : undefined} changed={changed('gameMode')} control={<ChoiceSelect value={v.gameMode} onChange={(m) => set('gameMode', m)} options={modeChoices()} label={t('settings.mode')} />} />
    </>
  )

  const list = (
    <>
      <SettingRow wide label={t('settings.name')} hint={t('settings.nameHint')} changed={changed('name')} htmlFor="server-name" control={<Input id="server-name" value={v.name} onChange={(e) => set('name', e.target.value)} maxLength={32} className="w-[240px] max-sm:w-full" />} />
      <SettingRow
        wide
        label={t('settings.motd')}
        changed={changed('motd')}
        htmlFor="server-motd"
        className="items-start"
        control={
          <div className="w-[300px] max-sm:w-full">
            <Textarea id="server-motd" value={v.motd} onChange={(e) => set('motd', e.target.value.replace(/\n{2,}/g, '\n'))} maxLength={59} rows={2} className="resize-none" />
            <p className="mt-1 text-right text-xs text-muted-foreground tabular-nums">{t('settings.motdCount', { count: v.motd.length })}</p>
          </div>
        }
      />
      <SettingRow wide label={t('settings.preview')} control={<ListPreview server={s} name={v.name} motd={v.motd} />} />
      <IconRow server={s} />
    </>
  )

  const memory = (
    <>
      <SettingRow
        label={t('settings.memoryRow')}
        hint={memoryHint}
        changed={changed('memoryMB')}
        control={<ChoiceSelect value={String(v.memoryMB)} onChange={(mb) => set('memoryMB', Number(mb))} options={memoryChoices} label={t('settings.memoryRow')} />}
      />
      <SettingRow label={t('settings.sleep')} hint={t('settings.sleepHint')} control={<span className="text-xs text-muted-foreground">{t('common.comingLater')}</span>} />
    </>
  )

  const unsaved = keys.length > 0 && (
    <div className={cn('fixed z-30 flex items-center gap-4 rounded-2xl border border-border bg-white px-4 py-3 shadow-popup', phone ? 'inset-x-3 bottom-[calc(64px+env(safe-area-inset-bottom))]' : 'right-6 bottom-6')} role="status">
      <div className="min-w-0 flex-1">
        <div className="text-[13px] font-semibold">{t('settings.unsaved', { count: keys.length })}</div>
        <div className="text-xs text-muted-foreground">{restartNeeded ? (online ? t('settings.unsavedRestart', { server: v.name }) : t('settings.unsavedStopped', { server: v.name })) : t('settings.unsavedNow')}</div>
      </div>
      <Button variant="ghost" size="sm" onClick={discard}>
        {t('settings.discard')}
      </Button>
      <Button size="sm" onClick={save} loading={saving} disabledReason={v.name.trim() ? busyReason(s) : t('reason.nameFirst')}>
        {restartNeeded && online ? <RotateCwIcon /> : <SaveIcon />}
        {restartNeeded && online ? t('settings.saveRestart') : t('common.save')}
      </Button>
    </div>
  )

  if (phone) {
    const group = (id: string, label: string, body: ReactNode) => (
      <section aria-labelledby={`set-${id}`} id={id}>
        <SectionLabel className="px-4">
          <span id={`set-${id}`}>{label}</span>
        </SectionLabel>
        <div className="mt-2 rounded-3xl border border-border bg-white px-4">{body}</div>
      </section>
    )
    return (
      <div className="flex flex-col gap-5 pb-24">
        {group('game', t('settings.game'), game)}
        {group('list', t('settings.list'), list)}
        {group('memory', t('settings.memory'), memory)}
        {group('version', t('settings.version'), <VersionRows server={s} versions={catalog?.versions} />)}
        {group('danger', t('settings.danger'), <DangerRows server={s} />)}
        {unsaved}
      </div>
    )
  }

  return (
    <div className="grid gap-6 lg:grid-cols-[160px_1fr]">
      <nav aria-label={t('settings.sections')} className="sticky top-4 hidden flex-col gap-0.5 self-start lg:flex">
        {sections.map((x) => (
          <a
            key={x.id}
            href={`#${x.id}`}
            aria-current={active === x.id ? 'location' : undefined}
            className="rounded-lg px-2.5 py-1.5 text-[13px] font-medium text-muted-foreground hover:bg-accent hover:text-foreground aria-[current=location]:bg-accent aria-[current=location]:font-semibold aria-[current=location]:text-foreground"
          >
            {t(x.key)}
          </a>
        ))}
      </nav>
      <div className="flex min-w-0 flex-col gap-4 pb-20">
        <Section id="game" title={t('settings.game')}>
          {game}
        </Section>
        <Section id="list" title={t('settings.list')}>
          {list}
        </Section>
        <Section id="memory" title={t('settings.memory')}>
          {memory}
        </Section>
        <Section id="version" title={t('settings.version')} hint={t('settings.versionMeta', { type: typeName(s.type), version: s.config?.minecraftVersion ?? '', build: s.config?.paperBuild ?? 0 })}>
          <VersionRows server={s} versions={catalog?.versions} />
        </Section>
        <Section id="danger" title={t('settings.danger')}>
          <DangerRows server={s} />
        </Section>
      </div>
      {unsaved}
    </div>
  )
}

function Section({ id, title, hint, children }: { id: string; title: string; hint?: string; children: ReactNode }) {
  return (
    <Card as="section" id={id} aria-labelledby={`${id}-title`} className="scroll-mt-4 pb-2">
      <CardTitle id={`${id}-title`}>{title}</CardTitle>
      {hint && <CardHint>{hint}</CardHint>}
      <div className="mt-2">{children}</div>
    </Card>
  )
}

/** A YYYY-MM-DD day from the agent, as a local date like "25 Sep". */
function dayLabel(date: string): string {
  const [y, m, d] = date.split('-').map(Number)
  return formatDate(new Date(y ?? 0, (m ?? 1) - 1, d ?? 1).toISOString())
}

/** The most memory it needed each of the last 14 days, under its budget; today's bar is darker. */
function MemoryDays({ advice: a }: { advice: MemoryAdvice }) {
  const peak = Math.max(0, ...a.days.map((d) => d.peakMB))
  const top = Math.max(a.budgetMB, peak, 1)
  const last = a.days.length - 1
  return (
    <div className="mt-1.5 w-[288px] max-w-full" role="img" aria-label={t('settings.memoryChart', { count: a.days.length, peak: formatMB(peak), memory: formatMB(a.budgetMB) })}>
      <div className="relative mt-2.5 h-8">
        <div className="absolute inset-0 flex items-end gap-[5px]">
          {a.days.map((d, i) => (
            <div
              key={d.date}
              title={d.peakMB > 0 ? t('settings.memoryPeakOn', { date: dayLabel(d.date), peak: formatMB(d.peakMB) }) : t('settings.memoryNotMeasured', { date: dayLabel(d.date) })}
              className={cn('min-w-0 flex-1 rounded-[3px] transition-[height] duration-(--motion-slow) ease-standard', d.peakMB <= 0 ? 'bg-foreground/8' : i === last ? 'bg-primary/75' : 'bg-primary/40')}
              style={{ height: d.peakMB > 0 ? `${Math.max(8, (d.peakMB / top) * 100)}%` : 2 }}
            />
          ))}
        </div>
        <div className="pointer-events-none absolute inset-x-0 flex translate-y-1/2 items-center gap-1.5" style={{ bottom: `${(a.budgetMB / top) * 100}%` }}>
          <span className="flex-1 border-t border-dashed border-muted-foreground/45" />
          <span className="text-[10px] leading-none font-medium text-muted-foreground tabular-nums">{formatMB(a.budgetMB)}</span>
        </div>
      </div>
      <div className="mt-1.5 flex justify-between text-[11px] leading-none text-muted-foreground" aria-hidden="true">
        <span>{t('settings.memoryDaysAgo', { count: a.days.length })}</span>
        <span className="font-medium text-foreground/70">{t('settings.memoryPeaks')}</span>
        <span>{t('settings.memoryToday')}</span>
      </div>
    </div>
  )
}

/** How the server looks in a friend's Minecraft server list. */
function ListPreview({ server: s, name, motd }: { server: ServerStatus; name: string; motd: string }) {
  return (
    <div className="flex w-[300px] items-start gap-3 rounded-2xl bg-muted p-2.5 max-sm:w-full">
      <Emblem size={48} icon={iconURL(s)} name={name} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="min-w-0 flex-1 truncate text-[13px] font-semibold">{name}</span>
          <span className="text-xs text-muted-foreground tabular-nums">{s.players ? `${s.players.online}/${s.players.max}` : ''}</span>
          <span className="flex items-end gap-px" aria-hidden="true">
            {[4, 6, 8, 10, 12].map((h) => (
              <span key={h} className="w-[3px] rounded-[1px] bg-success" style={{ height: h }} />
            ))}
          </span>
        </div>
        <p className="mt-0.5 text-xs leading-4 whitespace-pre-line text-muted-foreground">{motd}</p>
      </div>
    </div>
  )
}

function IconRow({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const input = useRef<HTMLInputElement>(null)
  const [preview, setPreview] = useState<string>()
  const [problem, setProblem] = useState<string>()
  const [busy, setBusy] = useState(false)
  async function take(file: File | undefined) {
    if (input.current) input.current.value = ''
    if (!file) return
    setProblem(undefined)
    if (file.size > maxIconBytes) {
      setProblem(t('settings.iconRefused'))
      return
    }
    setBusy(true)
    try {
      const icon = await iconPNG(file).catch(() => undefined)
      if (!icon) {
        setProblem(t('settings.iconBad'))
        return
      }
      if (!(await isIcon(icon.blob))) {
        setProblem(t('settings.iconRefused'))
        return
      }
      setPreview(icon.url)
      await api('POST', serverApi(s.id, '/icon'), undefined, icon.blob)
      toastManager.add({ title: t('settings.iconUploaded', { server: s.name }), type: 'success' })
      await ws.refresh()
    } catch (e) {
      setPreview(undefined)
      if (e instanceof ApiError && e.code === 'icon_invalid') setProblem(t('settings.iconRefused'))
      else toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  return (
    <SettingRow
      label={t('settings.icon')}
      hint={
        problem ? (
          <span role="alert" className="text-destructive-foreground">
            {problem}
          </span>
        ) : (
          t('settings.iconHint')
        )
      }
      control={
        <div className="flex items-center gap-3">
          <Emblem size={36} icon={preview ?? iconURL(s)} name={s.name} />
          <Button variant="outline" size="sm" loading={busy} onClick={() => input.current?.click()}>
            <UploadIcon />
            {t('settings.iconUpload')}
          </Button>
          <input ref={input} type="file" accept="image/png,image/jpeg,image/webp" className="sr-only" tabIndex={-1} aria-label={t('settings.iconUpload')} onChange={(e) => void take(e.target.files?.[0])} />
        </div>
      }
    />
  )
}

function VersionRows({ server: s, versions }: { server: ServerStatus; versions: CatalogEntry[] | undefined }) {
  const [open, setOpen] = useState<CatalogEntry | 'pick'>()
  const cfg = s.config
  const newest = newerStable(cfg, versions)
  const targets = cfg && versions ? upgradeTargets(cfg, versions) : []
  const others = targets.filter((x) => x.id !== newest?.id)
  return (
    <>
      <SettingRow
        label={newest ? t('settings.updateAvailable') : t('settings.upToDate')}
        hint={newest ? t('settings.updateBody') : versions ? t('settings.upToDateBody') : undefined}
        control={
          newest && (
            <Button variant="outline" size="sm" onClick={() => setOpen(newest)} disabledReason={busyReason(s)}>
              <CircleArrowUpIcon />
              {t('settings.updateTo', { version: newest.minecraftVersion })}
            </Button>
          )
        }
      />
      {others.length > 0 && (
        <SettingRow
          label={t('settings.otherVersions')}
          hint={t('settings.otherVersionsBody')}
          control={
            <Button variant="outline" size="sm" onClick={() => setOpen('pick')} disabledReason={busyReason(s)}>
              {t('settings.chooseVersion')}
            </Button>
          }
        />
      )}
      <VersionDialog server={s} targets={targets} initial={open === 'pick' ? others[0] : open} open={open !== undefined} onClose={() => setOpen(undefined)} />
    </>
  )
}

function VersionDialog({ server: s, targets, initial, open, onClose }: { server: ServerStatus; targets: CatalogEntry[]; initial: CatalogEntry | undefined; open: boolean; onClose: () => void }) {
  const phone = useIsPhone()
  const [chosen, setChosen] = useState<string>()
  const [warn, setWarn] = useState(true)
  const [consent, setConsent] = useState(false)
  const [experimental, setExperimental] = useState(false)
  const [busy, setBusy] = useState(false)
  const target = targets.find((x) => x.id === (chosen ?? initial?.id)) ?? initial
  const current = s.config?.minecraftVersion ?? ''
  const type = typeName(s.type)
  const players = s.phase === 'online' ? (s.players?.online ?? 0) : 0
  const close = () => {
    setChosen(undefined)
    setConsent(false)
    setExperimental(false)
    onClose()
  }
  async function go() {
    if (!target) return
    setBusy(true)
    const ok = await (async () => {
      try {
        await post(serverApi(s.id, '/version'), { versionId: target.id, acceptExperimental: target.experimental ? experimental : false, warnPlayers: warn && players > 0 })
        return true
      } catch (e) {
        toastManager.add({ title: errorText(e), type: 'error' })
        return false
      }
    })()
    setBusy(false)
    if (ok) {
      toastManager.add({ title: t('mcupdate.started', { server: s.name }), type: 'success' })
      close()
    }
  }
  const options: Choice<string>[] = targets.map((x) => ({
    value: x.id,
    label: t('mcupdate.toOption', { type, version: x.minecraftVersion, label: x.experimental ? t('common.experimental') : x.recommended ? t('mcupdate.latest') : t('mcupdate.build', { build: x.paperBuild }) }),
    marker: x.experimental ? <span className="text-xs font-medium text-warning-foreground">{t('common.experimental')}</span> : undefined,
  }))
  const version = target?.minecraftVersion ?? ''
  const steps = [
    { title: t('mcupdate.step1'), hint: t('mcupdate.step1Hint', { server: s.name }) },
    { title: t('mcupdate.step2', { type, version }), hint: t('mcupdate.step2Hint') },
    { title: t('mcupdate.step3', { server: s.name, version }), hint: t('mcupdate.step3Hint') },
    { title: t('mcupdate.step4'), hint: t('mcupdate.step4Hint', { current }) },
  ]
  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()}>
      <DialogPopup className="sm:max-w-[560px]">
        <div className="flex items-start gap-4 px-6 pt-6 pb-2 max-sm:px-5">
          <Emblem size={44} icon={iconURL(s)} name={s.name} />
          <div className="min-w-0 pt-0.5">
            <DialogTitle className="text-xl leading-7 font-bold">{t('mcupdate.title', { server: s.name })}</DialogTitle>
            <DialogDescription className="mt-0.5 text-[13px]">{t('mcupdate.lead', { server: s.name, current })}</DialogDescription>
          </div>
        </div>
        <DialogPanel className="flex flex-col gap-4 pt-3 max-sm:px-5">
          <div>
            <div className="text-[13px] font-semibold">{t('mcupdate.to')}</div>
            <ChoiceSelect value={target?.id ?? ''} onChange={setChosen} options={options} label={t('mcupdate.to')} className="mt-1.5 w-full" />
          </div>
          <ol className="flex flex-col gap-3 rounded-2xl bg-warm p-4">
            {steps.map((st, i) => (
              <li key={i} className="flex gap-3">
                <span className={cn('inline-flex size-[22px] shrink-0 items-center justify-center rounded-full text-[11px] font-semibold', i === 0 ? 'bg-primary text-primary-foreground' : 'border border-border bg-white text-muted-foreground')}>{i + 1}</span>
                <span className="min-w-0">
                  <span className="block text-[13px] font-semibold">{st.title}</span>
                  <span className="block text-xs text-muted-foreground">{st.hint}</span>
                </span>
              </li>
            ))}
          </ol>
          <div>
            <p className="text-[13px] font-semibold text-warning-foreground">{t('mcupdate.noBack')}</p>
            <p className="mt-0.5 text-xs text-muted-foreground">{t('mcupdate.noBackBody')}</p>
          </div>
          {players > 0 && (
            <label className="flex items-start gap-3">
              <Switch checked={warn} onCheckedChange={setWarn} className="mt-0.5" />
              <span>
                <span className="block text-[13px] font-semibold">{t('mcupdate.warn')}</span>
                <span className="block text-xs text-muted-foreground">{t('mcupdate.warnHint')}</span>
              </span>
            </label>
          )}
          <label className="flex items-start gap-2.5 text-[13px]">
            <Checkbox checked={consent} onCheckedChange={(c) => setConsent(c === true)} className="mt-0.5" />
            {t('mcupdate.consent', { server: s.name, version })}
          </label>
          {target?.experimental && (
            <label className="flex items-start gap-2.5 text-[13px]">
              <Checkbox checked={experimental} onCheckedChange={(c) => setExperimental(c === true)} className="mt-0.5" />
              {t('mcupdate.experimental', { version })}
            </label>
          )}
        </DialogPanel>
        <DialogFooter variant="bare" className="items-center border-t border-border pt-4 sm:justify-between">
          {!phone && <span className="text-xs text-muted-foreground">{t('mcupdate.footer', { count: players })}</span>}
          <div className="flex gap-2 max-sm:flex-col-reverse">
            <Button variant="ghost" onClick={close}>
              {t('common.cancel')}
            </Button>
            <Button onClick={go} loading={busy} disabledReason={busyReason(s) ?? (!target ? t('reason.pickVersion') : !consent || (target.experimental && !experimental) ? t('mcupdate.tickFirst', { count: target.experimental ? 2 : 1 }) : undefined)}>
              <ArchiveIcon />
              {t('mcupdate.confirm')}
            </Button>
          </div>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}

function DangerRows({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const [open, setOpen] = useState(false)
  const [typed, setTyped] = useState('')
  const [busy, setBusy] = useState(false)
  const [stopping, setStopping] = useState(false)
  const list = usePoll(() => get<Backup[]>(serverApi(s.id, '/backups')), 30_000, s.id)
  const backups = list.data?.length ?? 0
  async function remove() {
    setBusy(true)
    try {
      await post(serverApi(s.id, '/delete'), { confirm: typed.trim() })
      setOpen(false)
      navigate({ name: 'home' })
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <SettingRow
        label={t('settings.stopTitle', { server: s.name })}
        hint={t('settings.stopHint')}
        control={
          <Button
            variant="outline"
            size="sm"
            loading={stopping}
            disabledReason={whyNot(s, 'stop', ws.stale)}
            onClick={async () => {
              setStopping(true)
              await serverAction(s, 'stop')
              setStopping(false)
            }}
          >
            <SquareIcon />
            {t('settings.stopButton')}
          </Button>
        }
      />
      <SettingRow
        label={t('settings.deleteTitle', { server: s.name })}
        hint={t('settings.deleteHint', { count: backups })}
        control={
          <Button variant="destructive-outline" size="sm" onClick={() => setOpen(true)} disabledReason={ws.stale ? t('reason.noAgent') : busyReason(s)}>
            <Trash2Icon />
            {t('settings.deleteButton')}
          </Button>
        }
      />
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogPopup className="sm:max-w-[480px]">
          <div className="flex items-start gap-4 px-6 pt-6 pb-2">
            <Pip pose="hurt" size={52} />
            <div className="min-w-0 pt-1">
              <DialogTitle className="text-lg font-bold">{t('settings.deleteDialog', { server: s.name })}</DialogTitle>
              <DialogDescription className="mt-0.5 text-[13px]">{t('settings.deleteDialogBody')}</DialogDescription>
            </div>
          </div>
          <DialogPanel className="pt-3">
            <label className="flex flex-col gap-1.5 text-[13px]">
              <span>{rich('settings.deleteType', { b: (chunk) => <strong className="font-semibold">{chunk}</strong> }, { server: s.name })}</span>
              <Input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} />
            </label>
          </DialogPanel>
          <DialogFooter variant="bare" className="border-t border-border pt-4">
            <Button variant="ghost" onClick={() => setOpen(false)}>
              {t('common.cancel')}
            </Button>
            <Button variant="destructive" onClick={remove} loading={busy} disabledReason={typed.trim() === s.name ? undefined : t('settings.deleteTypeFirst', { server: s.name })}>
              <Trash2Icon />
              {t('settings.deleteConfirm', { server: s.name })}
            </Button>
          </DialogFooter>
        </DialogPopup>
      </Dialog>
    </>
  )
}
