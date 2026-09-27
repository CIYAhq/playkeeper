import { useState } from 'react'
import { EyeIcon, EyeOffIcon, KeyRoundIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { t } from '@/i18n'
import { cn } from '@/lib/utils'

type Strength = 'short' | 'weak' | 'okay' | 'strong'

export function passwordStrength(pw: string): Strength {
  if (pw.length < 10) return 'short'
  const classes = [/[a-z]/, /[A-Z]/, /\d/, /[^A-Za-z0-9]/].filter((r) => r.test(pw)).length
  if (pw.length >= 16 || (pw.length >= 12 && classes >= 3)) return 'strong'
  if (pw.length >= 12 || classes >= 3) return 'okay'
  return 'weak'
}

const strengthWidth: Record<Strength, number> = { short: 15, weak: 35, okay: 65, strong: 90 }

export function PasswordField({
  id,
  value,
  onChange,
  autoComplete,
  meter,
  label,
  labelClassName,
  autoFocus,
  error,
}: {
  id: string
  value: string
  onChange: (v: string) => void
  autoComplete: string
  meter?: boolean
  label: string
  labelClassName?: string
  autoFocus?: boolean
  /** Shown under the field, which is then marked invalid. */
  error?: string
}) {
  const [show, setShow] = useState(false)
  const s = passwordStrength(value)
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={id} className={cn('text-[13px] font-medium max-sm:text-[15px]', labelClassName)}>
        {label}
      </label>
      <InputGroup className="max-sm:h-11">
        <InputGroupAddon>
          <KeyRoundIcon aria-hidden="true" />
        </InputGroupAddon>
        <InputGroupInput
          id={id}
          type={show ? 'text' : 'password'}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          autoComplete={autoComplete}
          required
          minLength={meter ? 10 : undefined}
          maxLength={256}
          autoFocus={autoFocus}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? `${id}-error` : undefined}
        />
        <InputGroupAddon align="inline-end">
          <Button type="button" variant="ghost" size="icon-xs" onClick={() => setShow((v) => !v)} aria-label={show ? t('onboarding.hidePassword') : t('onboarding.showPassword')}>
            {show ? <EyeOffIcon /> : <EyeIcon />}
          </Button>
        </InputGroupAddon>
      </InputGroup>
      {meter && (
        <>
          <p className="text-xs text-muted-foreground">{t('onboarding.passwordHint')}</p>
          {value && (
            <div className="mt-1 flex items-center gap-3">
              <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-foreground/8" role="meter" aria-label={t('onboarding.strength')} aria-valuemin={0} aria-valuemax={100} aria-valuenow={strengthWidth[s]} aria-valuetext={t(`onboarding.strength.${s}`)}>
                <div className={cn('h-full rounded-full transition-[width]', s === 'short' || s === 'weak' ? 'bg-warning' : 'bg-primary')} style={{ width: `${strengthWidth[s]}%` }} />
              </div>
              <span className={cn('w-14 text-right text-xs font-medium', s === 'short' || s === 'weak' ? 'text-warning-foreground' : 'text-success-foreground')}>{t(`onboarding.strength.${s}`)}</span>
            </div>
          )}
        </>
      )}
      {error && (
        <p id={`${id}-error`} className="text-[13px] text-destructive-foreground" role="alert">
          {error}
        </p>
      )}
    </div>
  )
}
