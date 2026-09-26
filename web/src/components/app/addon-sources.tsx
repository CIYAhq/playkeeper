import { useEffect, useId, useState, type FormEvent, type ReactNode } from 'react'
import { ExternalLinkIcon, KeyRoundIcon } from 'lucide-react'
import { ApiError, del, get, post } from '@/api/client'
import type { AddonSources } from '@/api/types'
import { errorText, machineApi, useWorkspace } from '@/api/workspace'
import { Card, CardTitle, Marker } from '@/components/app/bits'
import { LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { sourceNames } from '@/lib/addons'

const curseForgeConsole = 'https://console.curseforge.com/'
// Source names are shown as they are, never translated.
const curseForge = 'CurseForge'

/**
 * Settings › Add-on sources: Modrinth and Hangar are built in; CurseForge
 * needs a key, the owner's own unless the release carries one. Only the
 * owner can change it.
 */
export function AddonSourcesCard() {
  const ws = useWorkspace()
  const machineId = ws.machine?.id
  const [sources, setSources] = useState<AddonSources>()
  const [error, setError] = useState<string>()
  useEffect(() => {
    if (!machineId) return
    let cancelled = false
    get<AddonSources>(machineApi(machineId, '/addon-sources'))
      .then((s) => !cancelled && setSources(s))
      .catch((e: unknown) => !cancelled && setError(errorText(e)))
    return () => {
      cancelled = true
    }
  }, [machineId])

  return (
    <Card as="section" aria-labelledby="sources-title" id="addon-sources" className="scroll-mt-4">
      <CardTitle id="sources-title" className="max-sm:sr-only">
        {t('sources.title')}
      </CardTitle>
      <div className="mt-1 divide-y divide-border">
        <SourceRow name={sourceNames.modrinth} state={<Marker tone="green">{t('sources.on')}</Marker>}>
          {t('sources.builtIn')}
        </SourceRow>
        <SourceRow name={sourceNames.hangar} state={<Marker tone="green">{t('sources.on')}</Marker>}>
          {t('sources.builtIn')}
        </SourceRow>
        {sources ? (
          <CurseForgeRow key={sources.curseforge.key + (sources.curseforge.ending ?? '')} sources={sources} onChange={setSources} />
        ) : error ? (
          <SourceRow name={curseForge}>
            <span className="text-destructive-foreground">{error}</span>
          </SourceRow>
        ) : (
          <div className="py-3">
            <LoadingLabel />
            <Skeleton className="h-4 w-24" />
            <Skeleton className="mt-1.5 h-3.5 w-40" />
          </div>
        )}
      </div>
    </Card>
  )
}

function SourceRow({ name, state, action, children }: { name: string; state?: ReactNode; action?: ReactNode; children: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2 py-3.5">
      <div className="min-w-0 flex-1">
        <p className="flex items-baseline gap-2 text-[13px] leading-5 font-semibold">
          {name}
          {state}
        </p>
        <div className="mt-1 text-xs text-muted-foreground">{children}</div>
      </div>
      {action}
    </div>
  )
}

function CurseForgeRow({ sources, onChange }: { sources: AddonSources; onChange: (s: AddonSources) => void }) {
  const ws = useWorkspace()
  const cf = sources.curseforge
  const [replacing, setReplacing] = useState(false)
  const [removing, setRemoving] = useState(false)
  const locked = ws.me.user.role === 'owner' ? undefined : t('reason.ownerOnly')

  async function remove() {
    if (!ws.machine) return
    setRemoving(true)
    try {
      onChange(await del<AddonSources>(machineApi(ws.machine.id, '/addon-sources/curseforge')))
      toastManager.add({ title: t('sources.removed'), type: 'success' })
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
      setRemoving(false)
    }
  }

  if (cf.key === 'build') {
    return (
      <SourceRow name={curseForge} state={<Marker tone="green">{t('sources.on')}</Marker>}>
        {t('sources.builtIn')}
      </SourceRow>
    )
  }
  if (cf.key === 'file' && !replacing) {
    return (
      <SourceRow
        name={curseForge}
        state={<Marker tone="green">{t('sources.on')}</Marker>}
        action={
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => setReplacing(true)} disabledReason={locked}>
              {t('sources.replace')}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => void remove()} loading={removing} disabledReason={locked}>
              {t('common.remove')}
            </Button>
          </div>
        }
      >
        {t('sources.ending', { ending: cf.ending ?? '' })}
      </SourceRow>
    )
  }
  return (
    <div className="animate-fade py-3">
      <p className="flex items-baseline gap-2 text-[13px] leading-5 font-semibold">
        {curseForge}
        {cf.key === 'file' ? <Marker tone="green">{t('sources.on')}</Marker> : cf.key === 'disabled' ? <Marker>{t('sources.off')}</Marker> : <Marker>{t('sources.needsKey')}</Marker>}
      </p>
      <p className="text-xs text-muted-foreground">{cf.key === 'disabled' ? t('sources.turnedOff') : cf.key === 'file' ? t('sources.ending', { ending: cf.ending ?? '' }) : t('sources.needsKeyLine')}</p>
      {cf.problem && (
        <p className="mt-1 text-xs text-destructive-foreground" role="alert">
          {cf.problem}
        </p>
      )}
      <ol className="mt-3 flex flex-col gap-1.5 text-[13px]">
        <li className="flex gap-2">
          <span className="w-4 shrink-0 text-muted-foreground">1.</span>
          <span>
            {t('sources.step1')}
            <a href={curseForgeConsole} target="_blank" rel="noreferrer" className="ml-2 inline-flex items-center gap-1 font-medium text-success-strong hover:underline">
              {t('sources.openCurseForge')}
              <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
            </a>
          </span>
        </li>
        <li className="flex gap-2">
          <span className="w-4 shrink-0 text-muted-foreground">2.</span>
          {t('sources.step2')}
        </li>
        <li className="flex gap-2">
          <span className="w-4 shrink-0 text-muted-foreground">3.</span>
          {t('sources.step3')}
        </li>
      </ol>
      <KeyForm locked={locked} onSaved={onChange} onCancel={replacing ? () => setReplacing(false) : undefined} />
    </div>
  )
}

function KeyForm({ locked, onSaved, onCancel }: { locked?: string; onSaved: (s: AddonSources) => void; onCancel?: () => void }) {
  const ws = useWorkspace()
  const id = useId()
  const [key, setKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [refused, setRefused] = useState<string>()

  async function save(e: FormEvent) {
    e.preventDefault()
    if (!ws.machine || !key.trim()) return
    setBusy(true)
    setRefused(undefined)
    try {
      const s = await post<AddonSources>(machineApi(ws.machine.id, '/addon-sources/curseforge'), { key: key.trim() })
      toastManager.add({ title: t('sources.saved'), type: 'success' })
      onSaved(s)
    } catch (err) {
      if (err instanceof ApiError && err.code === 'curseforge_key_refused') setRefused(t('sources.refused'))
      else toastManager.add({ title: errorText(err), type: 'error' })
      setBusy(false)
    }
  }

  return (
    <form onSubmit={save} className="mt-3">
      <div className="flex gap-2 max-sm:flex-col sm:flex-wrap">
        <InputGroup className="max-sm:h-11 sm:min-w-[240px] sm:flex-1">
          <InputGroupAddon>
            <KeyRoundIcon aria-hidden="true" />
          </InputGroupAddon>
          <InputGroupInput
            type="password"
            value={key}
            onChange={(e) => {
              setKey(e.target.value)
              setRefused(undefined)
            }}
            placeholder={t('sources.keyPlaceholder')}
            aria-label={t('sources.keyLabel')}
            aria-invalid={refused ? true : undefined}
            aria-describedby={refused ? `${id}-refused` : `${id}-stays`}
            autoComplete="off"
            spellCheck={false}
            disabled={!!locked}
          />
        </InputGroup>
        {onCancel && (
          <Button type="button" variant="ghost" onClick={onCancel} className="max-sm:h-11">
            {t('common.cancel')}
          </Button>
        )}
        <Button type="submit" loading={busy} disabledReason={locked ?? (key.trim() ? undefined : t('reason.pasteKey'))} className="max-sm:h-11">
          {t('sources.save')}
        </Button>
      </div>
      {refused ? (
        <p id={`${id}-refused`} className="mt-2 animate-fade text-xs text-destructive-foreground" role="alert">
          {refused}
        </p>
      ) : (
        <p id={`${id}-stays`} className="mt-2 text-xs text-muted-foreground">
          {t('sources.stays')}
        </p>
      )}
    </form>
  )
}
