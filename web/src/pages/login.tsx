import { useState, type FormEvent } from 'react'
import { LogInIcon, UserRoundIcon } from 'lucide-react'
import { ApiError, post } from '@/api/client'
import type { Me } from '@/api/types'
import { Pip } from '@/components/app/art'
import { useIsPhone } from '@/components/app/controls'
import { Frame, FrameCard } from '@/components/app/frame'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { t } from '@/i18n'
import { rich } from '@/i18n/rich'
import { PasswordField } from './onboarding'

export function LoginPage({ onDone }: { onDone: (m: Me) => void }) {
  const phone = useIsPhone()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setError(undefined)
    setBusy(true)
    try {
      onDone(await post<Me>('/api/auth/login', { username: username.trim(), password }))
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Frame>
      <FrameCard className="max-sm:rounded-3xl max-sm:border max-sm:bg-white max-sm:p-5 max-sm:mt-6">
        <div className="flex items-center gap-3">
          <Pip pose="wave" size={48} />
          <h1 className="text-xl font-bold">{t('login.title')}</h1>
        </div>
        <form className="mt-5 flex flex-col gap-4" onSubmit={submit}>
          <div className="flex flex-col gap-1.5">
            <label htmlFor="login-username" className="text-[13px] font-medium max-sm:text-[15px]">
              {t('login.username')}
            </label>
            <InputGroup className="max-sm:h-11">
              <InputGroupAddon>
                <UserRoundIcon aria-hidden="true" />
              </InputGroupAddon>
              <InputGroupInput id="login-username" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" required autoFocus />
            </InputGroup>
          </div>
          <PasswordField id="login-password" label={t('login.password')} value={password} onChange={setPassword} autoComplete="current-password" />
          {error && (
            <p className="text-[13px] text-destructive-foreground" role="alert">
              {error}
            </p>
          )}
          <Button type="submit" size={phone ? 'touch' : 'lg'} loading={busy} disabledReason={username.trim() && password ? undefined : t('reason.fillIn')}>
            <LogInIcon />
            {busy ? t('login.submitting') : t('login.submit')}
          </Button>
          <p className="text-xs text-muted-foreground">{rich('login.forgot', { code: (chunk) => <code className="rounded bg-muted px-1 py-0.5 text-[11px]">{chunk}</code> }, { command: 'sudo playkeeper reset-password <username>' })}</p>
        </form>
      </FrameCard>
    </Frame>
  )
}
