import { Fragment, useEffect, useRef, type ChangeEvent, type ClipboardEvent, type KeyboardEvent } from 'react'
import { t } from '@/i18n'
import { cn } from '@/lib/utils'

const length = 6

/**
 * Six boxes for a code from an authenticator app. Typing moves on, Backspace
 * moves back, and pasting or autofilling a code fills every box. The value
 * has no gaps: digits are always the first boxes. After a wrong code, typing
 * starts a new one in the first box.
 */
export function CodeField({
  value,
  onChange,
  onComplete,
  invalid,
  disabled,
  autoFocus,
  labelledBy,
  className,
}: {
  value: string
  onChange: (code: string) => void
  /** Called when a change fills the last box. */
  onComplete?: (code: string) => void
  invalid?: boolean
  disabled?: boolean
  autoFocus?: boolean
  labelledBy?: string
  className?: string
}) {
  const refs = useRef<(HTMLInputElement | null)[]>([])
  // Moving focus during a change runs the new box's focus handler before
  // React re-renders, so it reads the value from here.
  const latest = useRef(value)
  useEffect(() => {
    latest.current = value
  }, [value])
  const focus = (i: number) => refs.current[Math.max(0, Math.min(length - 1, i))]?.focus()

  /** A new code (pasted, autofilled, or typed after a wrong one) is sent even over a full field. */
  function set(next: string, at: number, fresh = false) {
    latest.current = next
    onChange(next)
    focus(at)
    if (next.length === length && (fresh || value.length < length)) onComplete?.(next)
  }

  function change(i: number, e: ChangeEvent<HTMLInputElement>) {
    const typed = e.target.value.replace(/\D/g, '')
    if (!typed) return
    const had = value[i]
    // A box that already held a digit gets both when the caret sat beside it.
    const digits = had && typed.length === 2 ? (typed[0] === had ? typed.slice(1) : typed.slice(0, 1)) : typed
    enter(i, digits)
  }

  function enter(i: number, digits: string) {
    if (invalid || digits.length >= length) {
      const code = digits.slice(-length)
      set(code, code.length, true)
      return
    }
    const at = Math.min(i, value.length)
    set((value.slice(0, at) + digits + value.slice(at + digits.length)).slice(0, length), at + digits.length)
  }

  function key(i: number, e: KeyboardEvent<HTMLInputElement>) {
    switch (e.key) {
      case 'Backspace':
        e.preventDefault()
        if (value[i]) set(value.slice(0, i) + value.slice(i + 1), i)
        else if (i > 0) set(value.slice(0, i - 1) + value.slice(i), i - 1)
        break
      case 'Delete':
        e.preventDefault()
        if (value[i]) set(value.slice(0, i) + value.slice(i + 1), i)
        break
      case 'ArrowLeft':
        e.preventDefault()
        focus(i - 1)
        break
      case 'ArrowRight':
        e.preventDefault()
        focus(Math.min(i + 1, value.length))
        break
      default:
        // A box selects its digit on focus, and typing that same digit over
        // it leaves the value as it was, so no change event would come.
        if (/^[0-9]$/.test(e.key) && e.key === value[i] && !e.ctrlKey && !e.metaKey && !e.altKey) {
          e.preventDefault()
          enter(i, e.key)
        }
    }
  }

  function paste(e: ClipboardEvent<HTMLInputElement>) {
    const digits = e.clipboardData.getData('text').replace(/\D/g, '').slice(0, length)
    e.preventDefault()
    if (digits) set(digits, digits.length, true)
  }

  return (
    <div role="group" aria-labelledby={labelledBy} className={cn('flex items-center justify-center gap-2', className)}>
      {Array.from({ length }, (_, i) => (
        <Fragment key={i}>
          {i === length / 2 && <span className="h-px w-2.5 shrink-0 bg-muted-foreground/40" aria-hidden="true" />}
          <input
            ref={(el) => {
              refs.current[i] = el
            }}
            value={value[i] ?? ''}
            onChange={(e) => change(i, e)}
            onKeyDown={(e) => key(i, e)}
            onPaste={paste}
            onFocus={(e) => {
              const filled = latest.current.length
              if (i > filled) focus(filled)
              else e.currentTarget.select()
            }}
            onClick={(e) => e.currentTarget.select()}
            inputMode="numeric"
            pattern="[0-9]*"
            autoComplete={i === 0 ? 'one-time-code' : 'off'}
            autoFocus={autoFocus && i === 0}
            disabled={disabled}
            aria-invalid={invalid || undefined}
            aria-label={t('code.digit', { n: i + 1, total: length })}
            className={cn(
              'h-[52px] w-11 min-w-0 rounded-[10px] border bg-white text-center text-[22px] leading-none font-semibold tabular-nums shadow-xs/5 caret-primary outline-none transition-[border-color,box-shadow] max-sm:w-[42px] motion-reduce:transition-none',
              invalid ? 'border-destructive/64 ring-[3px] ring-destructive/12' : 'border-input focus:border-ring focus:ring-[3px] focus:ring-ring/24',
              disabled && 'border-border bg-muted text-muted-foreground shadow-none',
            )}
          />
        </Fragment>
      ))}
    </div>
  )
}
