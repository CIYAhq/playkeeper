import { useEffect, useRef, useState, type ReactNode } from 'react'
import { ChevronLeftIcon, ChevronRightIcon } from 'lucide-react'
import { get, post } from '@/api/client'
import type { BackupRulesView, OffsiteView, RetentionEstimate, RetentionRules, RetentionSettings, ServerStatus } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { Card, CardHint, CardTitle, SectionLabel } from '@/components/app/bits'
import { ChoiceSelect, Segmented } from '@/components/app/controls'
import { PhoneBackHeader } from '@/components/app/shell'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogFooter, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { NumberField, NumberFieldDecrement, NumberFieldGroup, NumberFieldIncrement, NumberFieldInput } from '@/components/ui/number-field'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import { toastManager } from '@/components/ui/toast'
import { t, type MessageKey } from '@/i18n'
import { formatBytes } from '@/lib/format'
import { linkProps } from '@/lib/router'
import { usePoll } from '@/lib/usePoll'
import { cn } from '@/lib/utils'

export const timeZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone

export function useBackupRules(serverId: string) {
  return usePoll(() => get<BackupRulesView>(serverApi(serverId, '/backup-rules')), 30_000, serverId)
}

export function useOffsite(serverId: string) {
  return usePoll(() => get<OffsiteView>(serverApi(serverId, '/offsite')), 5_000, serverId)
}

type Row = RetentionEstimate['rows'][number]

function ruleLabel(r: Row): string {
  switch (r.rule) {
    case 'keep_all':
      return t('backupRules.row.keepAll')
    case 'hours':
      return r.n >= 48 && r.n % 24 === 0 ? t('backupRules.row.hoursDays', { days: r.n / 24 }) : t('backupRules.row.hours', { count: r.n })
    case 'last':
      return t('backupRules.row.last', { count: r.n })
    case 'daily':
      return t('backupRules.row.daily', { count: r.n })
    case 'weekly':
      return r.n % 52 === 0 ? t('backupRules.row.weeklyYears', { count: r.n / 52 }) : t('backupRules.row.weekly', { count: r.n })
    case 'monthly':
      return r.n % 12 === 0 ? t('backupRules.row.monthlyYears', { count: r.n / 12 }) : t('backupRules.row.monthly', { count: r.n })
    case 'manual':
      return t('backupRules.row.manual')
    default: {
      const unknown: never = r.rule
      return unknown
    }
  }
}

function ruleValue(r: Row, includeManual: boolean | undefined): string {
  if (r.rule === 'manual') return includeManual ? t('backupRules.value.counted') : t('backupRules.value.manual')
  if (r.rule === 'keep_all') return t('backupRules.value.manual')
  return r.upTo ? t('backupRules.value.upTo', { count: r.count }) : String(r.count)
}

/** "About 13 backups · roughly 4.1 GB", with the place when there is one. */
export function totalText(e: RetentionEstimate, kind: 'backups' | 'copies', place?: string): string {
  if (e.summary.code === 'estimate_off') return t('backupRules.autoOff')
  if (e.count < 0) return t('backupRules.keepsAll')
  const vars = { count: e.count, size: formatBytes(e.bytes), place: place ?? '' }
  if (kind === 'copies') return t(place ? 'backupRules.totalCopiesOn' : 'backupRules.totalCopies', vars)
  return t(place ? 'backupRules.totalOn' : 'backupRules.total', vars)
}

function BackLink({ server: s }: { server: ServerStatus }) {
  return (
    <a {...linkProps({ name: 'server', slug: s.slug, tab: 'world' })} className="-mt-1 inline-flex items-center gap-1 self-start rounded text-[13px] font-medium text-primary outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
      <ChevronLeftIcon className="size-4" aria-hidden="true" />
      {t('tab.world')}
    </a>
  )
}

/** World › Backup rules: automatic backups, what to keep, and copies somewhere else. */
export function BackupRulesPage({ server: s, copies }: { server: ServerStatus; copies?: (changeRules: () => void) => ReactNode }) {
  const rules = useBackupRules(s.id)
  const [editing, setEditing] = useState(false)
  return (
    <>
      <BackLink server={s} />
      <h1 className="sr-only">{t('backupRules.title')}</h1>
      <div className="grid items-start gap-4 lg:grid-cols-2">
        <div className="flex flex-col gap-4">
          {rules.data ? (
            <>
              <AutomaticCard server={s} view={rules.data} onSaved={() => void rules.refresh()} />
              <KeepCard view={rules.data} onChange={() => setEditing(true)} />
            </>
          ) : rules.error ? (
            <Card>
              <p className="text-sm text-destructive-foreground">{rules.error.message}</p>
            </Card>
          ) : (
            <>
              <Skeleton className="h-36 rounded-3xl" />
              <Skeleton className="h-60 rounded-3xl" />
            </>
          )}
        </div>
        {copies?.(() => setEditing(true))}
      </div>
      {rules.data && editing && <RulesDialog server={s} view={rules.data} onClose={() => setEditing(false)} onSaved={() => void rules.refresh()} />}
    </>
  )
}

const everyChoices = [1, 2, 3, 4, 6, 8, 12, 24]

function AutomaticCard({ server: s, view, onSaved }: { server: ServerStatus; view: BackupRulesView; onSaved: () => void }) {
  const [auto, setAuto] = useState(view.automatic)
  useEffect(() => setAuto(view.automatic), [view.automatic])
  async function save(next: typeof auto) {
    const before = auto
    setAuto(next)
    try {
      await post(serverApi(s.id, '/backup-rules'), { automatic: { enabled: next.enabled, everyHours: next.everyHours, onlyIfPlayed: next.onlyIfPlayed }, timeZone: timeZone() })
      if (next.enabled !== before.enabled) toastManager.add({ title: t(next.enabled ? 'backupRules.autoOnToast' : 'backupRules.autoOffToast'), type: 'success' })
      onSaved()
    } catch (e) {
      setAuto(before)
      toastManager.add({ title: errorText(e), type: 'error' })
    }
  }
  return (
    <Card>
      <div className="flex items-start justify-between gap-4">
        <div>
          <CardTitle>{t('backupRules.auto')}</CardTitle>
          <CardHint>{t('backupRules.autoHint')}</CardHint>
        </div>
        <Switch checked={auto.enabled} onCheckedChange={(c) => void save({ ...auto, enabled: c })} aria-label={t('backupRules.auto')} />
      </div>
      <div className={cn('grid transition-[grid-template-rows,opacity] duration-200 motion-reduce:transition-none', auto.enabled ? 'grid-rows-[1fr] opacity-100' : 'grid-rows-[0fr] opacity-0')} inert={!auto.enabled}>
        <div className="overflow-hidden">
          <p className="mt-4 mb-1.5 text-[13px] font-medium">{t('backupRules.howOften')}</p>
          <ChoiceSelect
            className="w-full"
            label={t('backupRules.howOften')}
            value={String(auto.everyHours)}
            onChange={(v) => void save({ ...auto, everyHours: Number(v) })}
            options={everyChoices.map((h) => ({ value: String(h), label: h === 24 ? t('backupRules.daily') : t('backupRules.every', { count: h }) }))}
          />
          <label className="mt-3 flex cursor-pointer items-center gap-2.5 text-[13px]">
            <Checkbox checked={auto.onlyIfPlayed} onCheckedChange={(c) => void save({ ...auto, onlyIfPlayed: c === true })} />
            {t('backupRules.onlyIfPlayed')}
          </label>
        </div>
      </div>
    </Card>
  )
}

function KeepCard({ view, onChange }: { view: BackupRulesView; onChange: () => void }) {
  const ws = useWorkspace()
  const e = view.onHost
  return (
    <Card>
      <CardTitle>{t('backupRules.keep')}</CardTitle>
      <ul className="mt-2 divide-y divide-border">
        {e.rows.map((r) => (
          <li key={r.rule} className="flex items-center justify-between gap-4 py-2.5 text-[13px]">
            <span>{ruleLabel(r)}</span>
            <span className="shrink-0 text-xs font-medium text-muted-foreground">{ruleValue(r, view.rules.includeManual)}</span>
          </li>
        ))}
      </ul>
      <div className="mt-3 flex items-center justify-between gap-4 border-t border-border pt-4">
        <span className="text-[13px] font-medium">{totalText(e, 'backups', ws.machineName)}</span>
        <Button size="sm" variant="outline" onClick={onChange}>
          {t('backupRules.change')}
        </Button>
      </div>
    </Card>
  )
}

type Side = 'onHost' | 'offSite'
type Field = 'hours' | 'daily' | 'weekly' | 'monthly'
const fields: { field: Field; label: MessageKey; unit: MessageKey }[] = [
  { field: 'hours', label: 'backupRules.field.hours', unit: 'backupRules.unit.hours' },
  { field: 'daily', label: 'backupRules.field.daily', unit: 'backupRules.unit.days' },
  { field: 'weekly', label: 'backupRules.field.weekly', unit: 'backupRules.unit.weeks' },
  { field: 'monthly', label: 'backupRules.field.monthly', unit: 'backupRules.unit.months' },
]

/** The rules being changed, with totals the agent works out while editing. */
function useRulesDraft(server: ServerStatus, view: BackupRulesView) {
  const [draft, setDraft] = useState<RetentionSettings>(view.rules)
  const [est, setEst] = useState<Record<Side, RetentionEstimate>>({ onHost: view.onHost, offSite: view.offSite })
  const [saving, setSaving] = useState(false)
  const seq = useRef(0)
  useEffect(() => {
    const n = ++seq.current
    const timer = window.setTimeout(() => {
      post<Record<Side, RetentionEstimate>>(serverApi(server.id, '/backup-rules/estimate'), { rules: draft, timeZone: timeZone() })
        .then((r) => n === seq.current && setEst(r))
        .catch(() => undefined)
    }, 250)
    return () => window.clearTimeout(timer)
  }, [draft, server.id])
  function set(side: Side, field: Field, n: number) {
    setDraft((d) => ({ ...d, [side]: { ...d[side], keepAll: false, [field]: n } satisfies RetentionRules }))
  }
  async function save(): Promise<boolean> {
    setSaving(true)
    try {
      await post(serverApi(server.id, '/backup-rules'), { rules: draft, timeZone: timeZone() })
      toastManager.add({ title: t('backupRules.saved'), type: 'success' })
      return true
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
      return false
    } finally {
      setSaving(false)
    }
  }
  return { draft, setDraft, est, set, save, saving }
}

function sideTitle(side: Side, machine: string, offsite: OffsiteView | undefined): string {
  if (side === 'onHost') return t('backupRules.on', { place: machine })
  return offsite?.configured ? t('backupRules.on', { place: offsite.place }) : t('backupRules.offServer')
}

function RuleField({ label, unit, value, max, onChange, phone }: { label: string; unit: string; value: number; max: number; onChange: (n: number) => void; phone?: boolean }) {
  return (
    <div className={cn('flex items-center justify-between gap-3', phone ? 'min-h-[60px] border-b border-border py-2 last:border-b-0' : 'py-1.5')}>
      <span className={phone ? 'text-base' : 'text-[13px]'}>{label}</span>
      <NumberField className="w-auto shrink-0" value={value} onValueChange={(n) => onChange(Math.max(0, Math.min(max, n ?? 0)))} min={0} max={max} step={1} size={phone ? 'lg' : 'sm'}>
        <NumberFieldGroup className={phone ? 'w-[156px]' : 'w-[148px]'}>
          <NumberFieldDecrement aria-label={t('common.decrease')} />
          <span className="flex min-w-0 flex-1 items-center justify-center gap-1 border-x border-input">
            <NumberFieldInput className={cn('w-7 shrink-0 grow-0 px-0 text-right font-semibold tabular-nums', phone && 'w-8 text-base')} aria-label={label} />
            <span className={cn('text-muted-foreground', phone ? 'text-sm' : 'text-xs')}>{unit}</span>
          </span>
          <NumberFieldIncrement aria-label={t('common.increase')} />
        </NumberFieldGroup>
      </NumberField>
    </div>
  )
}

function SideFields({ view, draft, side, set, phone }: { view: BackupRulesView; draft: RetentionSettings; side: Side; set: (side: Side, field: Field, n: number) => void; phone?: boolean }) {
  return (
    <>
      {fields.map((f) => (
        <RuleField key={f.field} phone={phone} label={t(f.label)} unit={t(f.unit)} value={draft[side].keepAll ? 0 : (draft[side][f.field] ?? 0)} max={view.limits[f.field]} onChange={(n) => set(side, f.field, n)} />
      ))}
    </>
  )
}

function RulesDialog({ server: s, view, onClose, onSaved }: { server: ServerStatus; view: BackupRulesView; onClose: () => void; onSaved: () => void }) {
  const ws = useWorkspace()
  const offsite = useOffsite(s.id).data
  const d = useRulesDraft(s, view)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogPopup className="max-w-[838px]">
        <DialogHeader>
          <DialogTitle>{t('backupRules.editTitle')}</DialogTitle>
        </DialogHeader>
        <DialogPanel className="flex flex-col gap-4">
          <div className="grid gap-3 sm:grid-cols-2">
            {(['onHost', 'offSite'] as const).map((side) => (
              <section key={side} className="rounded-2xl border border-border p-3" aria-label={sideTitle(side, ws.machineName, offsite)}>
                <h3 className="mb-1.5 text-[13px] font-semibold">{sideTitle(side, ws.machineName, offsite)}</h3>
                <SideFields view={view} draft={d.draft} side={side} set={d.set} />
                <p className="mt-2 border-t border-border pt-3 text-xs font-semibold" aria-live="polite">
                  {totalText(d.est[side], side === 'onHost' ? 'backups' : 'copies')}
                </p>
              </section>
            ))}
          </div>
          <SwitchLine checked={!!d.draft.includeManual} onChange={(c) => d.setDraft((r) => ({ ...r, includeManual: c }))} title={t('backupRules.includeManual')} hint={t(d.draft.includeManual ? 'backupRules.includeManualOn' : 'backupRules.includeManualOff')} />
          <SwitchLine checked={!!d.draft.deleteOnlyCopies} onChange={(c) => d.setDraft((r) => ({ ...r, deleteOnlyCopies: c }))} title={t('backupRules.onlyCopies')} hint={t(d.draft.deleteOnlyCopies ? 'backupRules.onlyCopiesOn' : 'backupRules.onlyCopiesOff')} />
        </DialogPanel>
        <DialogFooter className="items-center sm:justify-between">
          <p className="text-xs text-muted-foreground">{t('backupRules.zeroOff')}</p>
          <div className="flex gap-2">
            <Button variant="ghost" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button
              loading={d.saving}
              onClick={async () => {
                if (await d.save()) {
                  onSaved()
                  onClose()
                }
              }}
            >
              {t('backupRules.save')}
            </Button>
          </div>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}

function SwitchLine({ checked, onChange, title, hint, phone }: { checked: boolean; onChange: (v: boolean) => void; title: string; hint: string; phone?: boolean }) {
  return (
    <label className={cn('flex cursor-pointer items-start gap-3', phone && 'min-h-[60px] flex-row-reverse items-center justify-between border-b border-border py-2 last:border-b-0')}>
      <Switch checked={checked} onCheckedChange={onChange} className={phone ? '' : 'mt-0.5'} />
      <span className="min-w-0">
        <span className={cn('block font-semibold', phone ? 'text-base font-normal' : 'text-[13px]')}>{title}</span>
        <span className={cn('block text-muted-foreground', phone ? 'text-[13px]' : 'text-xs')}>{hint}</span>
      </span>
    </label>
  )
}

/** Phone: World › Backup rules, one set of rules at a time. */
export function BackupRulesPhonePage({ server: s }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const rules = useBackupRules(s.id)
  const offsite = useOffsite(s.id).data
  return (
    <>
      <PhoneBackHeader to={{ name: 'server', slug: s.slug, tab: 'world' }} label={t('tab.world')} title={t('backupRules.title')} />
      {rules.data ? (
        <PhoneRules key={rules.data.custom ? 'custom' : 'default'} server={s} view={rules.data} machine={ws.machineName} offsite={offsite} onSaved={() => void rules.refresh()} />
      ) : rules.error ? (
        <p className="px-4 text-[15px] text-destructive-foreground">{rules.error.message}</p>
      ) : (
        <Skeleton className="mx-0 h-80 rounded-3xl" />
      )}
    </>
  )
}

function PhoneRules({ server: s, view, machine, offsite, onSaved }: { server: ServerStatus; view: BackupRulesView; machine: string; offsite: OffsiteView | undefined; onSaved: () => void }) {
  const d = useRulesDraft(s, view)
  const [side, setSide] = useState<Side>('onHost')
  const place = side === 'onHost' ? machine : offsite?.configured ? offsite.place : undefined
  return (
    <div className="flex flex-col gap-2 pb-28">
      <Segmented
        className="grid h-11 grid-cols-2 [&>*]:h-10"
        label={t('backupRules.sides')}
        value={side}
        onChange={setSide}
        options={(['onHost', 'offSite'] as const).map((v) => ({ value: v, label: sideTitle(v, machine, offsite) }))}
      />
      <SectionLabel className="mt-2 px-4">{t('backupRules.keep')}</SectionLabel>
      <div className="rounded-3xl border border-border bg-white px-4">
        <SideFields view={view} draft={d.draft} side={side} set={d.set} phone />
      </div>
      <p className="px-1 pt-1 text-[15px] font-semibold" aria-live="polite">
        {totalText(d.est[side], side === 'onHost' ? 'backups' : 'copies', place)}
      </p>
      <SectionLabel className="mt-2 px-4">{t('backupRules.bothPlaces')}</SectionLabel>
      <div className="rounded-3xl border border-border bg-white px-4">
        <SwitchLine phone checked={!!d.draft.includeManual} onChange={(c) => d.setDraft((r) => ({ ...r, includeManual: c }))} title={t('backupRules.phoneManual')} hint={d.draft.includeManual ? t('backupRules.phoneOn') : t('backupRules.phoneManualOff')} />
        <SwitchLine phone checked={!!d.draft.deleteOnlyCopies} onChange={(c) => d.setDraft((r) => ({ ...r, deleteOnlyCopies: c }))} title={t('backupRules.phoneOnlyCopies')} hint={d.draft.deleteOnlyCopies ? t('backupRules.phoneOn') : t('backupRules.phoneOnlyCopiesOff')} />
      </div>
      <a {...linkProps({ name: 'server', slug: s.slug, tab: 'world', sub: 'backup-copies' })} className="mt-2 flex min-h-[60px] items-center gap-3 rounded-3xl border border-border bg-white px-4 py-2 outline-none focus-visible:ring-2 focus-visible:ring-ring">
        <span className="min-w-0 flex-1">
          <span className="block text-base">{t('offsite.title')}</span>
          <span className="block truncate text-[13px] text-muted-foreground">{offsite?.enabled ? t('offsite.onAt', { place: offsite.place }) : t('offsite.off')}</span>
        </span>
        <ChevronRightIcon className="size-5 text-muted-foreground" aria-hidden="true" />
      </a>
      <div className="fixed inset-x-4 bottom-[calc(76px+env(safe-area-inset-bottom))] z-20">
        <Button size="touch" className="w-full" loading={d.saving} onClick={async () => (await d.save()) && onSaved()}>
          {t('backupRules.save')}
        </Button>
      </div>
    </div>
  )
}
