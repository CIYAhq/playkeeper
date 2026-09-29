import { useCallback, useEffect, useId, useState, type FormEvent } from 'react'
import { ExternalLinkIcon, KeyRoundIcon } from 'lucide-react'
import { ApiError, del, get, put } from '@/api/client'
import type { HetznerPlace, HetznerStock } from '@/api/types'
import { errorText } from '@/api/workspace'
import { Card, CardTitle, Marker } from '@/components/app/bits'
import { ChoiceSelect } from '@/components/app/controls'
import { LoadingLabel } from '@/components/app/skeletons'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupAddon, InputGroupInput } from '@/components/ui/input-group'
import { Skeleton } from '@/components/ui/skeleton'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { formatDay, relativeTime } from '@/lib/format'
import { linkProps } from '@/lib/router'

const hetznerProjects = 'https://console.hetzner.com/projects'

/** How often the card reads the watch again while it's open: as often as the dashboard asks Hetzner. */
const refreshMs = 60_000

/**
 * Settings › Machines › Hetzner stock, the owner's: the dashboard asks
 * Hetzner once a minute where the server type they add can be bought, and
 * posts to Discord when it comes into stock, with a link that buys one.
 */
export function HetznerStockCard() {
  const [stock, setStock] = useState<HetznerStock>()
  const [error, setError] = useState<string>()
  const read = useCallback(async () => {
    try {
      setStock(await get<HetznerStock>('/api/hetzner'))
      setError(undefined)
    } catch (e) {
      setError(errorText(e))
    }
  }, [])
  useEffect(() => {
    void read()
    const id = window.setInterval(() => {
      if (document.visibilityState === 'visible') void read()
    }, refreshMs)
    return () => window.clearInterval(id)
  }, [read])

  return (
    <Card aria-labelledby="stock-title">
      <CardTitle id="stock-title">{t('stock.title')}</CardTitle>
      {stock ? (
        stock.connected ? (
          <Watching stock={stock} onChange={setStock} />
        ) : (
          <Start stock={stock} onChange={setStock} />
        )
      ) : error ? (
        <p className="mt-2 text-[13px] text-destructive-foreground">{error}</p>
      ) : (
        <div className="py-3">
          <LoadingLabel />
          <Skeleton className="h-4 w-48" />
          <Skeleton className="mt-1.5 h-3.5 w-64" />
        </div>
      )}
    </Card>
  )
}

function Start({ stock, onChange }: { stock: HetznerStock; onChange: (s: HetznerStock) => void }) {
  return (
    <div className="animate-fade">
      <p className="mt-1 text-[13px] text-muted-foreground">{t('stock.intro')}</p>
      <p className="mt-2 text-[13px]">
        {t('stock.step')}
        <a href={hetznerProjects} target="_blank" rel="noreferrer" className="ml-2 inline-flex items-center gap-1 font-medium text-success-strong hover:underline">
          {t('stock.openConsole')}
          <ExternalLinkIcon className="size-3.5" aria-hidden="true" />
        </a>
      </p>
      <WatchForm stock={stock} onSaved={onChange} />
    </div>
  )
}

/** The type to watch and the token: a new one to start, or to replace the one kept when editing. */
function WatchForm({ stock, onSaved, onCancel }: { stock: HetznerStock; onSaved: (s: HetznerStock) => void; onCancel?: () => void }) {
  const id = useId()
  const [type, setType] = useState(stock.serverType)
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  const [refused, setRefused] = useState<string>()
  const editing = !!onCancel
  const ready = token.trim() !== '' || (editing && type !== stock.serverType)

  async function save(e: FormEvent) {
    e.preventDefault()
    if (!ready) return
    setBusy(true)
    setRefused(undefined)
    try {
      const s = await put<HetznerStock>('/api/hetzner', { token: token.trim(), serverType: type })
      toastManager.add({ title: t('stock.watching', { type: s.serverType.toUpperCase() }), type: 'success' })
      onSaved(s)
    } catch (err) {
      if (err instanceof ApiError && err.code === 'hetzner_token_refused') setRefused(t('stock.refused'))
      else toastManager.add({ title: errorText(err), type: 'error' })
      setBusy(false)
    }
  }

  return (
    <form onSubmit={(e) => void save(e)} className="mt-3">
      <div className="flex gap-2 max-sm:flex-col sm:flex-wrap">
        <ChoiceSelect value={type} onChange={setType} options={stock.types.map((v) => ({ value: v, label: v.toUpperCase() }))} label={t('stock.typeLabel')} className="max-sm:h-11 sm:w-28" />
        <InputGroup className="max-sm:h-11 sm:min-w-[240px] sm:flex-1">
          <InputGroupAddon>
            <KeyRoundIcon aria-hidden="true" />
          </InputGroupAddon>
          <InputGroupInput
            type="password"
            value={token}
            onChange={(e) => {
              setToken(e.target.value)
              setRefused(undefined)
            }}
            placeholder={editing ? t('stock.tokenKeep', { ending: stock.tokenEnding ?? '' }) : t('stock.tokenPlaceholder')}
            aria-label={t('stock.tokenLabel')}
            aria-invalid={refused ? true : undefined}
            aria-describedby={refused ? `${id}-refused` : `${id}-stays`}
            autoComplete="off"
            spellCheck={false}
          />
        </InputGroup>
        {onCancel && (
          <Button type="button" variant="ghost" onClick={onCancel} className="max-sm:h-11">
            {t('common.cancel')}
          </Button>
        )}
        <Button type="submit" loading={busy} disabledReason={ready ? undefined : editing ? t('reason.changeSomething') : t('reason.pasteToken')} className="max-sm:h-11">
          {editing ? t('common.save') : t('stock.watch')}
        </Button>
      </div>
      {refused ? (
        <p id={`${id}-refused`} className="mt-2 animate-fade text-xs text-destructive-foreground" role="alert">
          {refused}
        </p>
      ) : (
        <p id={`${id}-stays`} className="mt-2 text-xs text-muted-foreground">
          {t('stock.tokenStays')}
        </p>
      )}
    </form>
  )
}

function Watching({ stock, onChange }: { stock: HetznerStock; onChange: (s: HetznerStock) => void }) {
  const [editing, setEditing] = useState(false)
  const [stopping, setStopping] = useState(false)
  const inStock = stock.places.some((p) => p.available)

  async function stop() {
    setStopping(true)
    try {
      onChange(await del<HetznerStock>('/api/hetzner'))
      toastManager.add({ title: t('stock.stopped'), type: 'success' })
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
      setStopping(false)
    }
  }

  return (
    <div className="animate-fade">
      <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-2 py-2">
        <div className="min-w-0 flex-1">
          <p className="flex items-baseline gap-2 text-[13px] leading-5 font-semibold">
            {stock.serverType.toUpperCase()}
            <Marker tone={stock.problem ? 'amber' : inStock ? 'green' : 'muted'}>{stock.problem ? t('stock.needsLook') : inStock ? t('stock.inStock') : t('stock.soldOut')}</Marker>
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            {t('stock.tokenEnding', { ending: stock.tokenEnding ?? '' })}
            {t('common.dot')}
            {stock.checkedAt ? t('stock.checkedAt', { when: relativeTime(stock.checkedAt) }) : t('stock.notChecked')}
          </p>
        </div>
        {!editing && (
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => setEditing(true)}>
              {t('stock.change')}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => void stop()} loading={stopping}>
              {t('stock.stop')}
            </Button>
          </div>
        )}
      </div>
      {editing && (
        <WatchForm
          stock={stock}
          onSaved={(s) => {
            setEditing(false)
            onChange(s)
          }}
          onCancel={() => setEditing(false)}
        />
      )}
      {stock.problem && (
        <p className="mt-1 text-xs text-destructive-foreground" role="alert">
          {stock.problem}
        </p>
      )}
      {!stock.discord && (
        <p className="mt-2 text-[13px] text-muted-foreground">
          {t('stock.discordOff')}
          <a {...linkProps({ name: 'discord' })} className="ml-2 font-medium text-success-strong hover:underline">
            {t('stock.connectDiscord')}
          </a>
        </p>
      )}
      {stock.places.length > 0 && (
        <ul className="mt-2 divide-y divide-border">
          {stock.places.map((p) => (
            <PlaceRow key={p.location} place={p} />
          ))}
        </ul>
      )}
    </div>
  )
}

function PlaceRow({ place }: { place: HetznerPlace }) {
  return (
    <li className="flex min-h-12 flex-wrap items-center gap-x-3 gap-y-1 py-2">
      <span className="min-w-0 flex-1 text-[13px] font-semibold">{place.city}</span>
      <span className="text-xs text-muted-foreground">
        <Marker tone={place.available ? 'green' : 'muted'}>{place.available ? t('stock.inStock') : t('stock.soldOut')}</Marker>
        {place.available && place.since && ` ${t('stock.since', { when: formatDay(place.since) })}`}
      </span>
      {place.buyUrl && (
        <Button variant="outline" size="sm" render={<a href={place.buyUrl} target="_blank" rel="noreferrer" />}>
          {t('stock.buy')}
          <ExternalLinkIcon aria-hidden="true" />
        </Button>
      )}
    </li>
  )
}
