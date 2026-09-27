import { useState, type ReactNode } from 'react'
import { RefreshCwIcon } from 'lucide-react'
import { post } from '@/api/client'
import type { Catalog, ServerStatus, SoftwareBuild, SoftwareChange } from '@/api/types'
import { errorText, serverApi, useWorkspace } from '@/api/workspace'
import { Pip, TypeLogo } from '@/components/app/art'
import { Card, CardTitle } from '@/components/app/bits'
import { ChoiceSelect, useIsPhone, type Choice } from '@/components/app/controls'
import { LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogDescription, DialogFooter, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Sheet, SheetPanel, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t, type MessageKey } from '@/i18n'
import { formatClock, formatDate } from '@/lib/format'
import { busyReason } from '@/lib/phase'
import { typeName } from '@/lib/servers'
import { addonKind, buildLabel, checkMarker, configBuild, serverTypeIds, shortHash, typeTexts, type AddonKind } from '@/lib/software'
import { cn } from '@/lib/utils'

/** "What's the difference?": what each type can add and how its download is checked. */
export function TypeCompare({ catalog, open, onOpenChange }: { catalog: Catalog | undefined; open: boolean; onOpenChange: (open: boolean) => void }) {
  const phone = useIsPhone()
  const checks = new Map((catalog?.types ?? []).map((ty) => [ty.id, ty.check]))
  const types = serverTypeIds.filter((id) => (catalog?.types ?? []).some((ty) => ty.id === id && ty.available))
  if (phone) {
    return (
      <Sheet open={open} onOpenChange={onOpenChange}>
        <SheetPopup side="bottom" showCloseButton>
          <div className="px-5 pt-3">
            <SheetTitle className="text-lg font-bold">{t('new.difference')}</SheetTitle>
            <p className="mt-2 text-[15px] leading-[21px] text-muted-foreground">{t('types.compare.lead')}</p>
          </div>
          <SheetPanel className="px-5 pt-4">
            <ul className="overflow-hidden rounded-2xl border border-border">
              {types.map((id) => {
                const m = checkMarker(checks.get(id), true)
                const text = typeTexts(id)
                return (
                  <li key={id} className="flex min-h-[58px] items-center gap-3 border-b border-border px-3.5 py-2 last:border-b-0">
                    <TypeLogo type={id} size={32} />
                    <span className="min-w-0 flex-1">
                      <span className="block text-base font-medium">{typeName(id)}</span>
                      {text && <span className="block text-[13px] leading-[18px] text-muted-foreground">{t(text.addonsShort)}</span>}
                    </span>
                    <span className={cn('shrink-0 text-[13px] font-medium', m.className)}>{m.label}</span>
                  </li>
                )
              })}
            </ul>
            <p className="mt-3 text-[13px] text-muted-foreground">{t('types.compare.weakNote')}</p>
            <p className="mt-1.5 text-[13px] text-muted-foreground">{t('types.compare.recordedNote')}</p>
          </SheetPanel>
        </SheetPopup>
      </Sheet>
    )
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[980px]">
        <div className="px-6 pt-6 pb-2">
          <DialogTitle className="text-xl leading-7 font-bold">{t('new.difference')}</DialogTitle>
          <DialogDescription className="mt-0.5 text-[13px]">{t('types.compare.lead')}</DialogDescription>
        </div>
        <DialogPanel className="pt-2">
          <table className="w-full text-left text-[13px]">
            <thead>
              <tr className="border-b border-border text-muted-foreground">
                <th scope="col" className="w-[186px] py-2 text-xs font-semibold">
                  {t('types.compare.type')}
                </th>
                <th scope="col" className="py-2 pr-4 text-xs font-semibold">
                  {t('types.compare.goodFor')}
                </th>
                <th scope="col" className="py-2 pr-4 text-xs font-semibold">
                  {t('types.compare.addons')}
                </th>
                <th scope="col" className="w-[283px] py-2 text-xs font-semibold">
                  {t('types.compare.check')}
                </th>
              </tr>
            </thead>
            <tbody>
              {types.map((id) => {
                const m = checkMarker(checks.get(id))
                const text = typeTexts(id)
                return (
                  <tr key={id} className="border-b border-border align-top last:border-b-0">
                    <th scope="row" className="py-3 pr-3 align-top">
                      <span className="flex items-center gap-2.5 text-sm font-semibold">
                        <TypeLogo type={id} size={28} />
                        {typeName(id)}
                      </span>
                    </th>
                    <td className="py-3.5 pr-4 leading-[18px]">{text && t(text.goodFor)}</td>
                    <td className="py-3.5 pr-4 leading-[18px] text-muted-foreground">{text && t(text.addons)}</td>
                    <td className="py-3.5 leading-[18px]">
                      <span className={cn('block font-semibold', m.className)}>{m.label}</span>
                      {text && <span className="mt-0.5 block text-xs text-muted-foreground">{t(text.check)}</span>}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </DialogPanel>
        <DialogFooter variant="bare" className="items-center border-t border-border pt-4 sm:justify-between">
          <p className="text-xs text-muted-foreground">{t('types.compare.footer')}</p>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.close')}
          </Button>
        </DialogFooter>
      </DialogPopup>
    </Dialog>
  )
}

const buildLabels: Record<string, { label: MessageKey; hint: MessageKey }> = {
  purpur: { label: 'new.buildLabel.purpur', hint: 'new.buildHint.other' },
  fabric: { label: 'new.buildLabel.fabric', hint: 'new.buildHint.loader' },
  quilt: { label: 'new.buildLabel.quilt', hint: 'new.buildHint.loader' },
  neoforge: { label: 'new.buildLabel.neoforge', hint: 'new.buildHint.other' },
}

/** The loader, Purpur build or NeoForge version to run, newest preselected. */
export function BuildSelect({ type, builds, loading, error, onRetry, value, onChange }: { type: string; builds: SoftwareBuild[] | undefined; loading: boolean; error?: string; onRetry: () => void; value: string; onChange: (build: string) => void }) {
  const keys = buildLabels[type]
  if (!keys) return null
  const options: Choice<string>[] = (builds ?? []).map((b) => ({
    value: b.version,
    label: b.recommended ? t('new.buildNewest', { version: b.version }) : b.version,
    marker: b.channel !== 'stable' ? <span className="text-xs font-medium text-warning-foreground">{b.channel === 'beta' ? t('new.buildBeta') : t('common.experimental')}</span> : undefined,
  }))
  const chosen = value || builds?.find((b) => b.recommended)?.version || builds?.[0]?.version || ''
  const label = t(keys.label)
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-3 rounded-2xl border border-border bg-card px-4 py-3.5">
      <div className="min-w-0">
        <div className="text-[13px] font-semibold max-sm:text-[15px]">{label}</div>
        <div className="mt-0.5 text-xs text-muted-foreground max-sm:text-[13px]">{t(keys.hint)}</div>
      </div>
      {error ? (
        <div className="flex items-center gap-2 text-xs">
          <span className="text-destructive-foreground">{t('new.versionsErrorType', { type: typeName(type) })}</span>
          <Button variant="outline" size="sm" onClick={onRetry}>
            <RefreshCwIcon />
            {t('common.tryAgain')}
          </Button>
        </div>
      ) : loading || !builds ? (
        <span className="flex">
          <LoadingLabel />
          <Skeleton className="h-9 w-48 rounded-lg" />
        </span>
      ) : (
        <ChoiceSelect value={chosen} onChange={onChange} options={options} label={label} className="min-w-[220px] max-sm:w-full" />
      )}
    </div>
  )
}

/** "12 Sep at 14:02", "Today at 18:31". */
function whenText(iso: string, now = new Date()): string {
  const d = new Date(iso)
  const yesterday = new Date(now)
  yesterday.setDate(now.getDate() - 1)
  const same = (a: Date, b: Date) => a.toDateString() === b.toDateString()
  if (same(d, now)) return t('soft.todayAt', { time: formatClock(iso) })
  if (same(d, yesterday)) return t('soft.yesterdayAt', { time: formatClock(iso) })
  return t('soft.dayAt', { day: formatDate(iso), time: formatClock(iso) })
}

const stayKeys: Record<AddonKind, { desktop: MessageKey; phone: MessageKey }> = {
  plugins: { desktop: 'changed.stays.plugins', phone: 'changed.phone.plugins' },
  mods: { desktop: 'changed.stays.mods', phone: 'changed.phone.mods' },
  none: { desktop: 'changed.stays.none', phone: 'changed.phone.none' },
}

function CheckedTable({ change: c }: { change: SoftwareChange }) {
  const rows: { label: string; value: ReactNode }[] = [
    { label: t('changed.file'), value: <span className="break-all">{c.file}</span> },
    ...(c.installedAt ? [{ label: t('changed.installed'), value: t('changed.installedValue', { time: whenText(c.installedAt) }) }] : []),
    { label: t('changed.recorded'), value: <span className="tabular-nums">{shortHash(c.recorded)}</span> },
    { label: t('changed.found'), value: <span className="text-destructive-foreground tabular-nums">{c.found ? shortHash(c.found) : t('changed.missing')}</span> },
    ...(c.changedAt ? [{ label: t('changed.lastChanged'), value: t('changed.lastChangedValue', { time: whenText(c.changedAt) }) }] : []),
  ]
  return (
    <dl className="overflow-hidden rounded-xl border border-border bg-warm text-xs max-sm:text-[13px]">
      {rows.map((r) => (
        <div key={r.label} className="grid grid-cols-[100px_1fr] gap-3 border-b border-border px-3.5 py-2.5 last:border-b-0 max-sm:grid-cols-[92px_1fr]">
          <dt className="text-muted-foreground">{r.label}</dt>
          <dd className="min-w-0">{r.value}</dd>
        </div>
      ))}
    </dl>
  )
}

/**
 * A server whose software no longer matches what Playkeeper installed: it
 * stayed off, and the one way forward is to reinstall it.
 */
export function SoftwareChangedView({ server: s, change }: { server: ServerStatus; change: SoftwareChange }) {
  const ws = useWorkspace()
  const phone = useIsPhone()
  const [busy, setBusy] = useState(false)
  const stays = stayKeys[addonKind(s.type)]
  const type = s.type || 'paper'
  const shortName = (type === 'paper' || type === 'purpur') && buildLabel(type, configBuild(s.config))

  async function reinstall() {
    setBusy(true)
    try {
      await post(serverApi(s.id, '/software/reinstall'))
      await ws.refresh()
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  const action = (
    <>
      <Button className="w-full" size={phone ? 'touch' : 'lg'} loading={busy} onClick={reinstall} disabledReason={busyReason(s)}>
        <RefreshCwIcon />
        {t('changed.action', { server: s.name })}
      </Button>
      <p className="mt-2 text-center text-xs text-muted-foreground max-sm:text-[13px]">{t('changed.time')}</p>
    </>
  )
  return (
    <div className="grid gap-4 lg:grid-cols-[1.55fr_1fr]">
      <Card className="p-6 max-sm:p-4">
        <div className="flex items-start gap-5 max-sm:gap-3">
          <Pip pose="search" size={phone ? 56 : 80} />
          <div className="min-w-0">
            <h2 className="text-lg font-bold">{t('crash.what')}</h2>
            <p className="mt-1 text-sm max-sm:text-[15px]">{t('changed.body', { server: s.name })}</p>
            {!phone && <p className="mt-2 text-[13px] text-muted-foreground">{t('changed.security', { machine: ws.machineName })}</p>}
          </div>
        </div>
        {phone && <p className="mt-3 text-[13px] text-muted-foreground">{t('changed.security', { machine: ws.machineName })}</p>}
        <div className="mt-5 max-sm:mt-3">
          {!phone && <div className="mb-2 text-xs font-semibold">{t('changed.checked')}</div>}
          <CheckedTable change={change} />
        </div>
      </Card>
      <Card className="max-sm:p-4">
        <CardTitle>{t('crash.fix')}</CardTitle>
        <div className="mt-4 text-sm font-semibold max-sm:mt-3 max-sm:text-base">{t('changed.reinstall')}</div>
        {phone ? (
          <p className="mt-0.5 text-[13px] text-muted-foreground">{t(stays.phone, { software: shortName || change.software })}</p>
        ) : (
          <>
            <p className="mt-0.5 text-xs text-muted-foreground">{t('changed.reinstallHint', { software: change.software })}</p>
            <div className="mt-4 text-[13px] font-semibold">{t('changed.stays')}</div>
            <p className="mt-0.5 text-xs text-muted-foreground">{t(stays.desktop)}</p>
            <div className="mt-auto pt-6">{action}</div>
          </>
        )}
      </Card>
      {phone && <div>{action}</div>}
    </div>
  )
}
