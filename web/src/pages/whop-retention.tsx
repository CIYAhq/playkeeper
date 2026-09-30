import { useId } from 'react'
import { get, put } from '@/api/client'
import type { CustomerRetentionView } from '@/api/types'
import { errorText } from '@/api/workspace'
import { ChoiceSelect } from '@/components/app/controls'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { usePending } from '@/lib/optimistic'
import { usePoll } from '@/lib/usePoll'

/** The days offered, besides the setting now and its default. */
const retentionChoices = [30, 60, 90, 180, 365, 730]

/**
 * How long after a customer's servers were deleted their account and
 * personal records are deleted too, for the owner to set. A customer who
 * asks is deleted sooner, from the Team page.
 */
export function CustomerRetention() {
  const id = useId()
  const setting = usePoll(() => get<CustomerRetentionView>('/api/customers/retention'), 60_000)
  const pending = usePending<{ days: number }>()
  const v = setting.data
  if (!v) return setting.error ? <p className="mt-4 text-xs text-destructive-foreground">{setting.error.message}</p> : null
  const days = pending.changes.at(-1)?.days ?? v.days
  const choices = [...new Set([...retentionChoices, v.days, v.default])].filter((n) => n >= v.min && n <= v.max).sort((a, b) => a - b)

  async function pick(value: string) {
    const n = Number(value)
    try {
      await pending.run({ days: n }, () => put('/api/customers/retention', { days: n }), setting.refresh)
      toastManager.add({ title: t('whop.retention.savedToast', { count: n }), type: 'success' })
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    }
  }

  return (
    <section aria-labelledby={`${id}-title`} className="mt-4">
      <h3 id={`${id}-title`} className="text-[13px] font-semibold">
        {t('whop.retention.title')}
      </h3>
      <p className="mt-1 text-xs text-muted-foreground">{t('whop.retention.body')}</p>
      <div className="mt-2.5 flex items-center justify-between gap-3 max-sm:flex-col max-sm:items-stretch">
        <span className="text-[13px] font-medium">{t('whop.retention.label')}</span>
        <ChoiceSelect value={String(days)} onChange={(x) => void pick(x)} options={choices.map((n) => ({ value: String(n), label: t('whop.retention.days', { count: n }) }))} label={t('whop.retention.label')} className="w-60 max-sm:w-full" />
      </div>
      <p className="mt-1.5 text-xs text-muted-foreground">{t('whop.retention.hint', { min: v.min })}</p>
    </section>
  )
}
