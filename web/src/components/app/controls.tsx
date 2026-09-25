import { useState, type ReactNode } from 'react'
import { CheckIcon, ChevronsUpDownIcon } from 'lucide-react'
import { RadioGroupPrimitive, Radio } from '@/components/ui/radio-group'
import { Select, SelectItem, SelectPopup, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Sheet, SheetHeader, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { useMediaQuery } from '@/hooks/use-media-query'
import { t } from '@/i18n'
import { cn } from '@/lib/utils'

export interface Choice<T extends string> {
  value: T
  label: string
  hint?: string
  marker?: ReactNode
  disabled?: boolean
}

export function useIsPhone(): boolean {
  return useMediaQuery('max-sm')
}

/**
 * A select: a popup list on desktop, a bottom sheet with large rows on
 * phones. Each option can carry a second line.
 */
export function ChoiceSelect<T extends string>({
  value,
  onChange,
  options,
  label,
  className,
  disabled,
  id,
}: {
  value: T
  onChange: (v: T) => void
  options: Choice<T>[]
  label: string
  className?: string
  disabled?: boolean
  id?: string
}) {
  const phone = useIsPhone()
  const [open, setOpen] = useState(false)
  const current = options.find((o) => o.value === value)
  if (phone) {
    return (
      <>
        <button
          type="button"
          id={id}
          disabled={disabled}
          aria-label={label}
          aria-haspopup="dialog"
          onClick={() => setOpen(true)}
          className={cn('inline-flex min-h-11 items-center gap-1.5 rounded-lg px-2 text-[15px] text-foreground disabled:opacity-60', className)}
        >
          <span className="truncate">{current?.label}</span>
          <ChevronsUpDownIcon className="size-4 opacity-70" aria-hidden="true" />
        </button>
        <Sheet open={open} onOpenChange={setOpen}>
          <SheetPopup side="bottom" showCloseButton>
            <SheetHeader className="pb-2">
              <SheetTitle className="text-lg">{label}</SheetTitle>
            </SheetHeader>
            <div className="mx-4 mb-4 overflow-hidden rounded-2xl border border-border" role="listbox" aria-label={label}>
              {options.map((o) => (
                <button
                  type="button"
                  role="option"
                  aria-selected={o.value === value}
                  key={o.value}
                  disabled={o.disabled}
                  onClick={() => {
                    onChange(o.value)
                    setOpen(false)
                  }}
                  className="flex min-h-[60px] w-full items-center gap-3 border-b border-border px-4 text-left last:border-b-0 disabled:opacity-60"
                >
                  <span className="min-w-0 flex-1">
                    <span className="block text-base">{o.label}</span>
                    {o.hint && <span className="block text-[13px] text-muted-foreground">{o.hint}</span>}
                  </span>
                  {o.value === value && <CheckIcon className="size-5 text-primary" aria-hidden="true" />}
                </button>
              ))}
            </div>
          </SheetPopup>
        </Sheet>
      </>
    )
  }
  return (
    <Select value={value} onValueChange={(v) => v !== null && onChange(v as T)} items={options.map((o) => ({ value: o.value, label: o.label }))} disabled={disabled}>
      <SelectTrigger id={id} aria-label={label} className={cn('w-auto min-w-48', className)}>
        <SelectValue />
      </SelectTrigger>
      <SelectPopup alignItemWithTrigger={false}>
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value} disabled={o.disabled} className="py-1.5">
            <span className="flex flex-col">
              <span className="flex items-center gap-2">
                {o.label}
                {o.marker}
              </span>
              {o.hint && <span className="text-xs text-muted-foreground">{o.hint}</span>}
            </span>
          </SelectItem>
        ))}
      </SelectPopup>
    </Select>
  )
}

/** A group of choice cards; the chosen one is tinted green. */
export function CardGroup<T extends string>({ value, onChange, label, className, children }: { value: T; onChange: (v: T) => void; label: string; className?: string; children: ReactNode }) {
  return (
    <RadioGroupPrimitive value={value} onValueChange={(v) => onChange(v as T)} aria-label={label} className={className}>
      {children}
    </RadioGroupPrimitive>
  )
}

export const choiceCardClass =
  'group/card relative flex cursor-pointer rounded-2xl border border-border bg-card text-left transition-[box-shadow,border-color,background-color] hover:border-input has-[[data-checked]]:border-primary/55 has-[[data-checked]]:bg-selected has-[[data-checked]]:shadow-selected has-[[data-disabled]]:cursor-default has-[[data-disabled]]:bg-muted/50 has-[[data-disabled]]:hover:border-border has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring'

/** One card in a CardGroup. The radio sits where `radio` puts it (default: top right). */
export function ChoiceCard<T extends string>({ value, disabled, className, children, radio = 'end' }: { value: T; disabled?: boolean; className?: string; children: ReactNode; radio?: 'end' | 'start' | 'none' }) {
  return (
    <label className={cn(choiceCardClass, className)}>
      {radio === 'start' && <Radio value={value} disabled={disabled} className="mt-0.5 shrink-0" />}
      <span className="min-w-0 flex-1">{children}</span>
      {radio === 'end' && <Radio value={value} disabled={disabled} className="shrink-0" />}
      {radio === 'none' && <Radio value={value} disabled={disabled} className="sr-only" />}
    </label>
  )
}

/** A small segmented control on a muted track. */
export function Segmented<T extends string>({ value, onChange, options, label, className }: { value: T; onChange: (v: T) => void; options: { value: T; label: string }[]; label: string; className?: string }) {
  return (
    <ToggleGroup
      value={[value]}
      onValueChange={(v) => v[0] && onChange(v[0] as T)}
      aria-label={label}
      className={cn('gap-0.5 rounded-[9px] bg-muted p-0.5', className)}
    >
      {options.map((o) => (
        <ToggleGroupItem
          key={o.value}
          value={o.value}
          className="h-7 rounded-[7px] border-0 px-2.5 text-[13px] font-medium text-muted-foreground hover:bg-transparent hover:text-foreground data-pressed:bg-white data-pressed:text-foreground data-pressed:shadow-outline"
        >
          {o.label}
        </ToggleGroupItem>
      ))}
    </ToggleGroup>
  )
}

/** One setting: its name, a plain-language hint, and the control. */
export function SettingRow({ label, hint, changed, control, htmlFor, className }: { label: ReactNode; hint?: ReactNode; changed?: boolean; control: ReactNode; htmlFor?: string; className?: string }) {
  return (
    <div className={cn('flex flex-wrap items-center justify-between gap-x-6 gap-y-3 border-b border-border py-3.5 last:border-b-0 max-sm:min-h-14', className)}>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2 text-sm font-semibold">
          {htmlFor ? <label htmlFor={htmlFor}>{label}</label> : label}
          {changed && <span className="text-xs font-medium text-warning-foreground">{t('settings.changed')}</span>}
        </div>
        {hint && <div className="mt-0.5 text-[13px] leading-[18px] text-muted-foreground">{hint}</div>}
      </div>
      <div className="shrink-0">{control}</div>
    </div>
  )
}

/** Numbered steps joined by lines: done steps are green, the current one is ringed. */
export function Stepper({ steps, current, label, className }: { steps: string[]; current: number; label: string; className?: string }) {
  return (
    <ol className={cn('flex items-center gap-2', className)} aria-label={label}>
      {steps.map((s, i) => {
        const state = i < current ? 'done' : i === current ? 'current' : 'todo'
        return (
          <li key={s} className="flex min-w-0 flex-1 items-center gap-2 last:flex-none" aria-current={state === 'current' ? 'step' : undefined}>
            <span
              className={cn(
                'inline-flex size-[22px] shrink-0 items-center justify-center rounded-full text-[11px] font-semibold',
                state === 'done' && 'bg-primary text-primary-foreground',
                state === 'current' && 'border-[1.5px] border-primary text-primary',
                state === 'todo' && 'bg-muted text-muted-foreground',
              )}
            >
              {state === 'done' ? <CheckIcon className="size-3.5" aria-hidden="true" /> : i + 1}
            </span>
            <span className={cn('truncate text-[13px] font-medium', state === 'todo' ? 'text-muted-foreground' : 'text-foreground')}>{s}</span>
            {i < steps.length - 1 && <span className={cn('h-0.5 min-w-4 flex-1 rounded-full', i < current ? 'bg-primary' : 'bg-border')} aria-hidden="true" />}
          </li>
        )
      })}
    </ol>
  )
}
