import { useEffect, useId, useState, type FormEvent } from 'react'
import { get, post } from '@/api/client'
import type { SellerOpened, SellerPrice, SellerPrices } from '@/api/types'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupAddon, InputGroupInput, InputGroupText } from '@/components/ui/input-group'
import { Spinner } from '@/components/ui/spinner'
import { t } from '@/i18n'
import { formatMB } from '@/lib/format'
import { money } from './whop-seller-view'

/** A price in cents as the seller edits it, such as 12.00. */
const typed = (cents: number) => (cents / 100).toFixed(2)

const errorText = (err: unknown) => (err instanceof Error ? err.message : t('error.network'))

/**
 * A seller's prices and Open the store, on their page inside Whop: each
 * hosting plan's monthly price, which they set at or above the floor, and
 * Open the store while the store is closed, or Update the store once it's
 * open. onChange tells the page the store changed, so its view reads it
 * again. Its calls go to relative addresses, which carry Whop's token.
 */
export function SellerPricesCard({ store, onChange }: { store: string; onChange: () => void }) {
  const [prices, setPrices] = useState<SellerPrices>()
  const [error, setError] = useState<string>()
  const [opening, setOpening] = useState(false)
  const [opened, setOpened] = useState<string>()
  const [openError, setOpenError] = useState<string>()

  useEffect(() => {
    let cancelled = false
    get<SellerPrices>(`/api/public/whop/seller/${store}/prices`)
      .then((v) => !cancelled && setPrices(v))
      .catch((err: unknown) => !cancelled && setError(errorText(err)))
    return () => {
      cancelled = true
    }
  }, [store])

  /** Open the store, or Update the store once it's open: the same call. */
  async function open(update: boolean) {
    setOpening(true)
    setOpenError(undefined)
    try {
      const done = await post<SellerOpened>(`/api/public/whop/seller/${store}/sell`)
      setOpened(done.open ? t(update ? 'sellerPrices.updated' : 'sellerPrices.opened') : t('sellerPrices.stillClosed', { why: done.why ?? '' }))
      setPrices((v) => v && { ...v, canOpen: !done.open, canUpdate: done.open })
      onChange()
    } catch (err) {
      setOpenError(errorText(err))
    } finally {
      setOpening(false)
    }
  }

  if (error) return <p className="mt-4 text-sm text-destructive-foreground">{error}</p>
  if (!prices) {
    return (
      <p className="mt-4 flex items-center gap-2 text-sm text-muted-foreground">
        <Spinner className="size-4" />
        {t('sellerPrices.loading')}
      </p>
    )
  }
  return (
    <section className="mt-4">
      <h2 className="text-[15px] font-semibold">{t('sellerPrices.title')}</h2>
      <p className="mt-1 text-sm text-muted-foreground">{t('sellerPrices.about')}</p>
      {prices.plans.length === 0 ? (
        <p className="mt-2 text-sm text-muted-foreground">{t('sellerPrices.none')}</p>
      ) : (
        <ul className="mt-1 divide-y divide-border">
          {prices.plans.map((p) => (
            <PriceRow
              key={p.id}
              store={store}
              plan={p}
              onSaved={(v) => {
                setPrices(v)
                onChange()
              }}
            />
          ))}
        </ul>
      )}
      {prices.problem && <p className="mt-2 text-sm font-medium text-warning-foreground">{prices.problem}</p>}
      {prices.canOpen && (
        <div className="mt-3 flex flex-col items-start gap-1.5">
          <Button loading={opening} onClick={() => void open(false)}>
            {t('sellerPrices.open')}
          </Button>
          <p className="text-xs text-muted-foreground">{t('sellerPrices.openAbout')}</p>
        </div>
      )}
      {prices.canUpdate && (
        <div className="mt-3 flex flex-col items-start gap-1.5">
          <Button variant="outline" loading={opening} onClick={() => void open(true)}>
            {t('sellerPrices.update')}
          </Button>
          <p className="text-xs text-muted-foreground">{t('sellerPrices.updateAbout')}</p>
        </div>
      )}
      {openError && <p className="mt-2 text-sm text-destructive-foreground">{openError}</p>}
      {opened && <p className="mt-2 text-sm font-medium">{opened}</p>}
    </section>
  )
}

/** One hosting plan, with its monthly price to set when it can be set here. */
function PriceRow({ store, plan: p, onSaved }: { store: string; plan: SellerPrice; onSaved: (v: SellerPrices) => void }) {
  const id = useId()
  const [value, setValue] = useState(() => typed(p.price))
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState<string>()

  async function save(e: FormEvent) {
    e.preventDefault()
    setSaving(true)
    setSaved(false)
    setError(undefined)
    try {
      const v = await post<SellerPrices>(`/api/public/whop/seller/${store}/prices`, { plan: p.id, price: value.trim() })
      const now = v.plans.find((x) => x.id === p.id)
      if (now) setValue(typed(now.price))
      setSaved(true)
      onSaved(v)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setSaving(false)
    }
  }

  const detail = t('sellerPrices.detail', {
    allowance: t('sellerView.plan', { servers: t('unit.servers', { count: p.servers }), memory: formatMB(p.memoryMB) }),
    floor: money(p.floor, 'usd'),
    share: money(p.share, 'usd'),
  })
  return (
    <li className="py-3">
      <form onSubmit={(e) => void save(e)} className="flex flex-col gap-1.5">
        {p.settable ? (
          <label htmlFor={id} className="text-sm font-medium">
            {p.title}
          </label>
        ) : (
          <span className="text-sm font-medium">{p.title}</span>
        )}
        <span className="text-xs text-muted-foreground">{detail}</span>
        {p.settable ? (
          <div className="flex flex-wrap items-center gap-2">
            <InputGroup className="w-36">
              <InputGroupAddon>
                <InputGroupText>$</InputGroupText>
              </InputGroupAddon>
              <InputGroupInput
                id={id}
                value={value}
                inputMode="decimal"
                autoComplete="off"
                onChange={(e) => {
                  setValue(e.target.value)
                  setSaved(false)
                  setError(undefined)
                }}
                aria-invalid={error ? true : undefined}
                aria-describedby={error ? `${id}-error` : undefined}
              />
            </InputGroup>
            <span className="text-sm text-muted-foreground">{t('sellerPrices.perMonth')}</span>
            <Button type="submit" size="sm" variant="outline" loading={saving} disabled={value.trim() === typed(p.price)}>
              {t('sellerPrices.save')}
            </Button>
            {saved && <span className="text-sm text-success-strong">{t('sellerPrices.saved')}</span>}
          </div>
        ) : (
          <span className="text-sm">{money(p.price, p.currency)}</span>
        )}
        {error && (
          <p id={`${id}-error`} className="text-sm text-destructive-foreground">
            {error}
          </p>
        )}
        {p.problem && <p className="text-sm text-warning-foreground">{p.problem}</p>}
      </form>
    </li>
  )
}
