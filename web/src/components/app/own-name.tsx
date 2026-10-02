import { useId, type Ref } from 'react'
import { UserRoundIcon } from 'lucide-react'
import type { MachineView } from '@/api/types'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { t } from '@/i18n'
import { cn } from '@/lib/utils'
import { compareMinecraft } from '@/lib/versions'

/** The preference that keeps someone's own Minecraft name, which their new servers put on the allowlist and make an operator. */
export const ownNameKey = 'minecraft.name'

/** Whether Minecraft allows a username: 3–16 letters, numbers or underscores. */
export function validPlayerName(name: string): boolean {
  return /^[A-Za-z0-9_]{3,16}$/.test(name)
}

/** The players a create asks the machine to put on the allowlist and make operators once the server runs: someone's own Minecraft name, on an agent from 0.4.16, which takes them. */
export function ownOperators(name: string | undefined, machine: MachineView | undefined, panelVersion: string): { operators?: string[] } {
  const n = name?.trim() ?? ''
  const v = machine?.live?.agentVersion
  const takes = !!v && (v === panelVersion || compareMinecraft(v.split(/[-+]/)[0] ?? '', '0.4.16') >= 0)
  return validPlayerName(n) && takes ? { operators: [n] } : {}
}

/** Someone's own Minecraft name, which nobody has to give: it goes on the new server's allowlist as an operator, so they can join and run its commands. */
export function OwnNameField({ value, onChange, inputRef, className }: { value: string; onChange: (v: string) => void; inputRef?: Ref<HTMLInputElement>; className?: string }) {
  const id = useId()
  const problem = ownNameProblem(value)
  return (
    <div className={cn('flex flex-col gap-1.5', className)}>
      <label htmlFor={id} className="text-[13px] font-semibold max-sm:text-[15px]">
        {t('style.ownName')}
      </label>
      <InputGroup className="max-w-[320px] max-sm:h-11 max-sm:max-w-none">
        <InputGroupAddon>
          <UserRoundIcon aria-hidden="true" />
        </InputGroupAddon>
        <InputGroupInput ref={inputRef} id={id} value={value} onChange={(e) => onChange(e.target.value)} placeholder={t('style.ownNamePlaceholder')} autoComplete="off" spellCheck={false} maxLength={16} aria-invalid={problem ? true : undefined} aria-describedby={`${id}-hint`} />
      </InputGroup>
      <p id={`${id}-hint`} className={cn('text-xs max-sm:text-[13px]', problem ? 'text-destructive-foreground' : 'text-muted-foreground')}>
        {problem ?? t('style.ownNameHint')}
      </p>
    </div>
  )
}

/** Why an own Minecraft name can't be used; undefined when it can, or there is none. */
export function ownNameProblem(value: string): string | undefined {
  const n = value.trim()
  return n && !validPlayerName(n) ? t('players.nameRule') : undefined
}
