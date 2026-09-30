import { useState, type Ref } from 'react'
import { EyeIcon, EyeOffIcon, KeyRoundIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { t } from '@/i18n'
import { cn } from '@/lib/utils'

/** Whether the browser hides a text field's letters (.secret-field in styles.css); one that can't gets a password field instead. */
export function masksText(): boolean {
  return typeof CSS !== 'undefined' && typeof CSS.supports === 'function' && CSS.supports('-webkit-text-security', 'disc')
}

/**
 * A field for a secret that's pasted once and never shown again, like an API
 * key. It's a text field that hides its letters rather than a password
 * field, so neither the browser nor a password manager offers to save it,
 * fill it or reveal it; its own button shows what's typed, and only that.
 */
export function SecretField({
  id,
  value,
  onChange,
  label,
  placeholder,
  describedBy,
  invalid,
  disabled,
  inputRef,
  className,
}: {
  id: string
  value: string
  onChange: (v: string) => void
  label: string
  placeholder?: string
  describedBy?: string
  invalid?: boolean
  disabled?: boolean
  inputRef?: Ref<HTMLInputElement>
  className?: string
}) {
  const [show, setShow] = useState(false)
  const [masks] = useState(masksText)
  // An emptied field, as after a save, hides the next secret typed into it.
  if (show && value === '') setShow(false)
  const shown = show && value !== ''
  return (
    <InputGroup className={cn('secret-field max-sm:h-11', className)} data-shown={shown || undefined}>
      <InputGroupAddon>
        <KeyRoundIcon aria-hidden="true" />
      </InputGroupAddon>
      <InputGroupInput
        ref={inputRef}
        id={id}
        type={masks || shown ? 'text' : 'password'}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        aria-label={label}
        aria-describedby={describedBy}
        aria-invalid={invalid || undefined}
        placeholder={placeholder}
        disabled={disabled}
        autoComplete="off"
        autoCapitalize="none"
        autoCorrect="off"
        spellCheck={false}
        data-1p-ignore=""
        data-lpignore="true"
        data-bwignore=""
        data-form-type="other"
      />
      {value !== '' && (
        <InputGroupAddon align="inline-end">
          <Button type="button" variant="ghost" size="icon-xs" onClick={() => setShow(!shown)} aria-label={shown ? t('secret.hide') : t('secret.show')}>
            {shown ? <EyeOffIcon /> : <EyeIcon />}
          </Button>
        </InputGroupAddon>
      )}
    </InputGroup>
  )
}
