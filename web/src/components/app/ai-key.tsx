import { useId, useRef, useState, type FormEvent, type RefObject } from 'react'
import { ExternalLinkIcon, KeyRoundIcon } from 'lucide-react'
import { useAIKeys, type AIKeysState } from '@/api/ai-keys'
import { ApiError } from '@/api/client'
import type { AIProvider, ServerStatus } from '@/api/types'
import { errorText, useWorkspace } from '@/api/workspace'
import { Notice } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { SecretField } from '@/components/app/secret-field'
import { Button } from '@/components/ui/button'
import { Dialog, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Sheet, SheetPanel, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { can } from '@/lib/access'
import { aiKeyMessage, aiKeyProblem, aiProviders, buildBattleProvider, hasAIKey, isAIKeyReason } from '@/lib/ai-keys'
import { busyReason } from '@/lib/phase'
import { cn } from '@/lib/utils'

/** Why the agent didn't take a key, in the owner's language, when it says which check the key failed. */
function refusal(provider: AIProvider, e: unknown): string | undefined {
  return e instanceof ApiError && e.field === 'key' && isAIKeyReason(e.reason) ? aiKeyMessage(provider, e.reason) : undefined
}

/**
 * The owner's key for one AI provider. A saved key never comes back: the
 * field is then empty and says it's saved, a key typed in replaces it, and
 * Remove key deletes it.
 */
export function AIKeyEditor({
  server,
  keys,
  provider,
  phone,
  inputRef,
  onSaved,
}: {
  server: ServerStatus
  keys: AIKeysState
  provider: AIProvider
  phone: boolean
  inputRef?: RefObject<HTMLInputElement | null>
  onSaved?: () => void
}) {
  const id = useId()
  const p = aiProviders[provider]
  const [draft, setDraft] = useState('')
  const [problem, setProblem] = useState<string>()
  const [busy, setBusy] = useState<'save' | 'remove'>()
  const loaded = !!keys.keys || !!keys.error
  const set = keys.keys?.keys[provider]?.set === true
  const pending = set && keys.keys?.pending === true
  const blocked = busyReason(server)
  const lineId = `${id}-line`

  async function save(e: FormEvent) {
    e.preventDefault()
    const reason = aiKeyProblem(provider, draft)
    if (reason) {
      setProblem(aiKeyMessage(provider, reason))
      return
    }
    setProblem(undefined)
    setBusy('save')
    try {
      const now = await keys.save(provider, draft.trim())
      setDraft('')
      toastManager.add({ title: t('aiKey.savedToast', { provider: p.name }), description: now.pending ? t('aiKey.pending') : undefined, type: 'success' })
      onSaved?.()
    } catch (err) {
      const why = refusal(provider, err)
      if (why) setProblem(why)
      else toastManager.add({ title: errorText(err), type: 'error' })
    } finally {
      setBusy(undefined)
    }
  }

  async function remove() {
    setBusy('remove')
    try {
      await keys.remove(provider)
      setDraft('')
      setProblem(undefined)
      toastManager.add({ title: t('aiKey.removedToast', { provider: p.name }), type: 'success' })
    } catch (err) {
      toastManager.add({ title: errorText(err), type: 'error' })
    } finally {
      setBusy(undefined)
    }
  }

  const line = problem ?? keys.error?.message
  return (
    <form onSubmit={(e) => void save(e)} className="flex flex-col gap-1.5">
      <div className="flex items-baseline justify-between gap-3">
        <label htmlFor={id} className="text-[13px] font-semibold max-sm:text-[15px]">
          {t('aiKey.label', { provider: p.name })}
        </label>
        <a href={p.keysUrl} target="_blank" rel="noreferrer" className="inline-flex shrink-0 items-center gap-1 text-[13px] font-medium text-success-strong hover:underline" aria-label={t('common.external', { label: t('aiKey.getKey') })}>
          {t('aiKey.getKey')}
          <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
        </a>
      </div>
      <div className={cn('flex gap-2', phone && 'flex-col')}>
        <SecretField
          id={id}
          inputRef={inputRef}
          value={draft}
          onChange={(v) => {
            setDraft(v)
            setProblem(undefined)
          }}
          label={t('aiKey.label', { provider: p.name })}
          placeholder={!loaded ? t('common.loading') : set ? t('aiKey.saved') : t('aiKey.placeholder', { provider: p.name })}
          describedBy={lineId}
          invalid={!!problem}
          disabled={!loaded}
          className="min-w-0 flex-1"
        />
        <Button
          type="submit"
          size={phone ? 'touch' : 'default'}
          className={phone ? 'w-full' : undefined}
          loading={busy === 'save'}
          disabled={busy === 'remove'}
          disabledReason={blocked ?? (draft.trim() ? undefined : t('reason.pasteKey'))}
        >
          {t('common.save')}
        </Button>
      </div>
      <div className="flex min-h-7 items-center gap-3">
        <p
          id={lineId}
          role={line ? 'alert' : undefined}
          className={cn('min-w-0 flex-1 text-xs', line ? 'animate-fade text-destructive-foreground' : pending ? 'font-medium text-warning-foreground' : 'text-muted-foreground')}
        >
          {line ?? (pending ? t('aiKey.pending') : t('aiKey.hint', { provider: p.name }))}
        </p>
        {set && (
          <Button type="button" variant="ghost" size="xs" className="-me-1.5 shrink-0" onClick={() => void remove()} loading={busy === 'remove'} disabled={busy === 'save'} disabledReason={blocked}>
            {t('aiKey.remove')}
          </Button>
        )}
      </div>
    </form>
  )
}

/** The editor on its own: a dialog, or a sheet from the bottom on phones, that closes once the key is saved. */
export function AIKeyDialog({ server, keys, provider, open, onOpenChange }: { server: ServerStatus; keys: AIKeysState; provider: AIProvider; open: boolean; onOpenChange: (open: boolean) => void }) {
  const phone = useIsPhone()
  const input = useRef<HTMLInputElement>(null)
  const title = t('aiKey.title', { provider: aiProviders[provider].name })
  // Opened by touch, the key's field waits for a tap rather than bringing up the keyboard.
  const focus = (how: string) => (how === 'touch' ? true : input.current)
  const editor = <AIKeyEditor server={server} keys={keys} provider={provider} phone={phone} inputRef={input} onSaved={() => onOpenChange(false)} />
  return phone ? (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetPopup side="bottom" initialFocus={focus}>
        <div className="px-5 pt-4 pr-14">
          <SheetTitle className="text-xl leading-7 font-bold">{title}</SheetTitle>
        </div>
        <SheetPanel className="px-5 pt-3 pb-4">{editor}</SheetPanel>
      </SheetPopup>
    </Sheet>
  ) : (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup className="sm:max-w-[480px]" initialFocus={focus}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <DialogPanel className="pb-5">{editor}</DialogPanel>
      </DialogPopup>
    </Dialog>
  )
}

/** The overview's line while the server has AI Build Battle and no key, for an account that may add one. */
export function AIKeyNotice({ server }: { server: ServerStatus }) {
  const ws = useWorkspace()
  const keys = useAIKeys(server.id, can(ws.me, 'files.edit'), server.phase)
  const [open, setOpen] = useState(false)
  const provider = buildBattleProvider
  const needs = !!keys.keys?.available && !hasAIKey(keys.keys)
  return (
    <>
      {needs && (
        <Notice
          title={t('aiKey.prompt', { provider: aiProviders[provider].name })}
          action={
            <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
              <KeyRoundIcon />
              {t('aiKey.add')}
            </Button>
          }
        />
      )}
      <AIKeyDialog server={server} keys={keys} provider={provider} open={open} onOpenChange={setOpen} />
    </>
  )
}
