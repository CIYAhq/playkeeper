import { useId, useState, type ReactNode } from 'react'
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
  /** Why a disabled option can't be chosen. */
  reason?: string
}

export function useIsPhone(): boolean {
  return useMediaQuery('max-sm')
}

/** An option's second line: why it can't be chosen, else its hint. Disabled options take no pointer, so a title alone would go unseen. */
function secondLine<T extends string>(o: Choice<T>): string | undefined {
  return (o.disabled && o.reason) || o.hint
}

/** A ChoiceSelect stacked in a form: full width, and outlined like the fields around it on phones. */
export const fieldSelectClass = 'w-full justify-between max-sm:border max-sm:border-input max-sm:bg-background max-sm:px-3 max-sm:shadow-xs/5'

/**
 * A select: a popup list on desktop, a bottom sheet with large rows on
 * phones. Each option can carry a second line. Like Button, a
 * `disabledReason` disables it and says why.
 */
export function ChoiceSelect<T extends string>({
  value,
  onChange,
  options,
  label,
  className,
  disabled: disabledProp,
  disabledReason,
  id,
}: {
  value: T
  onChange: (v: T) => void
  options: Choice<T>[]
  label: string
  className?: string
  disabled?: boolean
  disabledReason?: string
  id?: string
}) {
  const phone = useIsPhone()
  const [open, setOpen] = useState(false)
  const hintId = useId()
  const current = options.find((o) => o.value === value)
  // A disabled option's hint is why it can't be picked.
  const why = (o: Choice<T>, i: number) => (o.disabled && secondLine(o) ? `${hintId}-${i}` : undefined)
  const disabled = !!disabledProp || !!disabledReason
  if (phone) {
    return (
      <>
        <button
          type="button"
          id={id}
          disabled={disabled}
          title={disabledReason}
          aria-label={label}
          aria-haspopup="dialog"
          onClick={() => setOpen(true)}
          className={cn('inline-flex min-h-11 items-center gap-1.5 rounded-lg px-2 text-[15px] text-foreground disabled:opacity-60', disabledReason && 'disabled:cursor-not-allowed', className)}
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
              {options.map((o, i) => (
                <button
                  type="button"
                  role="option"
                  aria-selected={o.value === value}
                  key={o.value}
                  disabled={o.disabled}
                  title={o.disabled ? o.reason : undefined}
                  aria-describedby={why(o, i)}
                  onClick={() => {
                    onChange(o.value)
                    setOpen(false)
                  }}
                  className="flex min-h-[60px] w-full items-center gap-3 border-b border-border px-4 text-left last:border-b-0 disabled:opacity-60"
                >
                  <span className="min-w-0 flex-1">
                    <span className="block text-base">{o.label}</span>
                    {secondLine(o) && (
                      <span id={why(o, i)} className="block text-[13px] text-muted-foreground">
                        {secondLine(o)}
                      </span>
                    )}
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
      <SelectTrigger id={id} aria-label={label} title={disabledReason} className={cn('w-auto min-w-48', disabledReason && 'data-disabled:pointer-events-auto data-disabled:cursor-not-allowed', className)}>
        <SelectValue />
      </SelectTrigger>
      <SelectPopup alignItemWithTrigger={false}>
        {options.map((o, i) => (
          <SelectItem key={o.value} value={o.value} disabled={o.disabled} title={o.disabled ? o.reason : undefined} aria-describedby={why(o, i)} className="py-1.5">
            <span className="flex flex-col">
              <span className="flex items-center gap-2">
                {o.label}
                {o.marker}
              </span>
              {secondLine(o) && (
                <span id={why(o, i)} className="text-xs text-muted-foreground">
                  {secondLine(o)}
                </span>
              )}
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

/** One card in a CardGroup. The radio sits where `radio` puts it (default: top right); a disabled card says why with `reason`. */
export function ChoiceCard<T extends string>({ value, disabled, reason, className, children, radio = 'end' }: { value: T; disabled?: boolean; reason?: string; className?: string; children: ReactNode; radio?: 'end' | 'start' | 'none' }) {
  const reasonId = useId()
  const why = disabled ? reason : undefined
  const button = (cls: string) => <Radio value={value} disabled={disabled} aria-describedby={why ? reasonId : undefined} className={cls} />
  return (
    <label className={cn(choiceCardClass, className)} title={why}>
      {radio === 'start' && button('mt-0.5 shrink-0')}
      <span className="min-w-0 flex-1">{children}</span>
      {radio === 'end' && button('shrink-0')}
      {radio === 'none' && button('sr-only')}
      {why && (
        <span id={reasonId} className="sr-only">
          {why}
        </span>
      )}
    </label>
  )
}

/** A small segmented control on a muted track; a `disabledReason` disables it and says why. */
export function Segmented<T extends string>({ value, onChange, options, label, className, itemClassName, disabledReason }: { value: T; onChange: (v: T) => void; options: { value: T; label: string }[]; label: string; className?: string; itemClassName?: string; disabledReason?: string }) {
  return (
    <ToggleGroup
      value={[value]}
      onValueChange={(v) => v[0] && onChange(v[0] as T)}
      aria-label={label}
      disabled={!!disabledReason}
      className={cn('gap-0.5 rounded-[9px] bg-muted p-0.5', className)}
    >
      {options.map((o) => (
        <ToggleGroupItem
          key={o.value}
          value={o.value}
          title={disabledReason}
          className={cn(
            'h-7 min-w-7 rounded-[7px] border-0 px-2.5 text-[13px] font-medium text-muted-foreground hover:bg-transparent hover:text-foreground data-pressed:bg-white data-pressed:font-semibold data-pressed:text-foreground data-pressed:shadow-outline sm:h-7 sm:min-w-7 sm:text-[13px]',
            disabledReason && 'disabled:pointer-events-auto disabled:cursor-not-allowed',
            itemClassName,
          )}
        >
          {o.label}
        </ToggleGroupItem>
      ))}
    </ToggleGroup>
  )
}

/** One setting: its name, a plain-language hint, and the control. */
export function SettingRow({ label, hint, changed, control, htmlFor, wide, className }: { label: ReactNode; hint?: ReactNode; changed?: boolean; control: ReactNode; htmlFor?: string; wide?: boolean; className?: string }) {
  return (
    <div className={cn('flex flex-wrap items-center justify-between gap-x-6 gap-y-3 border-b border-border py-3.5 last:border-b-0 max-sm:min-h-14', wide && 'max-sm:flex-col max-sm:items-stretch', className)}>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2 text-sm font-semibold">
          {htmlFor ? <label htmlFor={htmlFor}>{label}</label> : label}
          {changed && <span className="text-xs font-medium text-warning-foreground">{t('settings.changed')}</span>}
        </div>
        {hint && <div className="mt-0.5 text-[13px] leading-[18px] text-muted-foreground">{hint}</div>}
      </div>
      <div className={cn('shrink-0', wide && 'max-sm:w-full')}>{control}</div>
    </div>
  )
}

/** Numbered steps joined by lines: done steps are green, the current one is ringed. */
export function Stepper({ steps, current, label, className, skip }: { steps: string[]; current: number; label: string; className?: string; skip?: number }) {
  return (
    <ol className={cn('flex items-center gap-2', className)} aria-label={label}>
      {steps.map((s, i) => {
        const state = i === skip ? 'todo' : i < current ? 'done' : i === current ? 'current' : 'todo'
        return (
          <li key={s} className="flex min-w-0 flex-1 items-center gap-2 last:flex-none" aria-current={state === 'current' ? 'step' : undefined}>
            <span
              className={cn(
                'inline-flex size-[22px] shrink-0 items-center justify-center rounded-full border-[1.5px] text-[11px] font-semibold transition-colors duration-(--motion-standard) ease-standard',
                state === 'done' && 'border-primary bg-primary text-primary-foreground',
                state === 'current' && 'border-primary text-primary',
                state === 'todo' && 'border-transparent bg-muted text-muted-foreground',
              )}
            >
              {state === 'done' ? <CheckIcon className="size-3.5 animate-fade" aria-hidden="true" /> : i + 1}
            </span>
            <span className={cn('truncate text-[13px] font-medium transition-colors duration-(--motion-standard) ease-standard', state === 'todo' ? 'text-muted-foreground' : 'text-foreground')}>{s}</span>
            {i < steps.length - 1 && <span className={cn('h-0.5 min-w-4 flex-1 rounded-full transition-colors duration-(--motion-slow) ease-standard', i < current ? 'bg-primary' : 'bg-border')} aria-hidden="true" />}
          </li>
        )
      })}
    </ol>
  )
}
