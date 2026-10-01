import { useId, useState, type FormEvent } from 'react'
import { get, post } from '@/api/client'
import type { SellerFixed, SellerPrice, SellerPrices } from '@/api/types'
import { Button } from '@/components/ui/button'
import { InputGroup, InputGroupAddon, InputGroupInput, InputGroupText } from '@/components/ui/input-group'
import { t } from '@/i18n'
import { formatMB } from '@/lib/format'
import { errorText, money, StepHeader } from './whop-seller-step'

/** A price in cents as the seller edits it, such as 15.00. */
const typed = (cents: number) => (cents / 100).toFixed(2)

/** A price as the seller wrote it, in cents, or undefined when it isn't one. */
function centsOf(s: string): number | undefined {
  const m = /^\$?\s*(\d{1,5})(?:\.(\d{1,2}))?$/.exec(s.trim())
  return m ? Number(m[1]) * 100 + Number((m[2] ?? '').padEnd(2, '0')) : undefined
}

/** What the page offers for a plan: its price when that's at or above the floor, else the price it suggests. */
const offered = (p: SellerPrice) => (p.price >= p.floor ? p.price : p.suggested)

/**
 * The step that checks a seller's prices. While a plan breaks a rule Fix my
 * plans puts right, it offers that, and nothing else; while one breaks a
 * rule it can't, it says what to change in Whop, in a plain line each.
 * Otherwise it lists each plan at the price the page offers, and Looks good
 * saves the ones that changed. edit is the same once the store is open, to
 * change its prices, except that it always lists them, with Back: Fix my
 * plans is for a store that isn't open yet, and Update the store says what
 * a plan added since needs. notice is what Fix my plans last changed.
 * onPrices hands the page new prices to start the step again with, from
 * Fix my plans or Check again; onSaved hands it the prices saved so far
 * when a later one failed, and the step stays as it is, saying why.
 */
export function PricesStep({
  store,
  prices,
  notice,
  edit,
  onPrices,
  onSaved,
  onDone,
  onBack,
}: {
  store: string
  prices: SellerPrices
  notice?: string
  edit?: boolean
  onPrices: (prices: SellerPrices, changed?: string) => void
  onSaved: (prices: SellerPrices) => void
  onDone: (prices: SellerPrices) => void
  onBack?: () => void
}) {
  const [drafts, setDrafts] = useState<Record<string, string>>(() => Object.fromEntries(prices.plans.map((p) => [p.id, typed(offered(p))])))
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState<string>()
  const title = edit ? t('sellerFlow.prices.editTitle') : t('sellerFlow.prices.title')
  const step = edit ? undefined : 0

  async function fix() {
    setBusy(true)
    setFailed(undefined)
    try {
      const done = await post<SellerFixed>(`/api/public/whop/seller/${store}/fix`)
      onPrices(done.prices, done.changed)
    } catch (err) {
      setFailed(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  async function checkAgain() {
    setBusy(true)
    setFailed(undefined)
    try {
      onPrices(await get<SellerPrices>(`/api/public/whop/seller/${store}/prices`))
    } catch (err) {
      setFailed(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  async function save(e: FormEvent) {
    e.preventDefault()
    const wrong: Record<string, string> = {}
    for (const p of prices.plans) {
      const cents = centsOf(drafts[p.id] ?? '')
      if (cents === undefined) wrong[p.id] = t('sellerFlow.prices.notAPrice')
      else if (cents < p.floor) wrong[p.id] = t('sellerFlow.prices.atLeast', { floor: money(p.floor, 'usd') })
    }
    setErrors(wrong)
    setFailed(undefined)
    if (Object.keys(wrong).length > 0) return
    setBusy(true)
    let latest = prices
    let at = ''
    try {
      for (const p of prices.plans) {
        const cents = centsOf(drafts[p.id] ?? '') ?? p.price
        if (cents === p.price) continue
        at = p.id
        latest = await post<SellerPrices>(`/api/public/whop/seller/${store}/prices`, { plan: p.id, price: typed(cents) })
      }
      onDone(latest)
    } catch (err) {
      setErrors({ [at]: errorText(err) })
      if (latest !== prices) onPrices(latest)
    } finally {
      setBusy(false)
    }
  }

  const told = notice && (
    <p role="status" className="mt-4 text-sm font-medium text-success-strong">
      {t('sellerFlow.fix.done', { changed: notice })}
    </p>
  )
  const problem = failed && (
    <p role="alert" className="mt-4 text-sm text-destructive-foreground">
      {failed}
    </p>
  )
  const back = onBack && (
    <Button type="button" variant="ghost" className="max-sm:w-full" onClick={onBack}>
      {t('sellerFlow.back')}
    </Button>
  )

  if (prices.plans.length === 0) {
    return (
      <>
        <StepHeader step={step} title={title} lead={t('sellerFlow.prices.none')} />
        <div className="mt-6 flex flex-col gap-2 sm:flex-row sm:items-center">
          <Button size="lg" className="max-sm:w-full" loading={busy} onClick={() => void checkAgain()}>
            {t('sellerFlow.checkAgain')}
          </Button>
          {back}
        </div>
        {problem}
      </>
    )
  }
  if (!edit && prices.fixable) {
    return (
      <>
        <StepHeader step={step} title={title} lead={t('sellerFlow.fix.lead')} />
        <Button size="lg" className="mt-6 max-sm:w-full" loading={busy} onClick={() => void fix()}>
          {t('sellerFlow.fix.button')}
        </Button>
        {problem}
      </>
    )
  }
  if (!edit && prices.blocked?.length) {
    return (
      <>
        <StepHeader step={step} title={title} lead={t('sellerFlow.blocked.lead')} />
        {told}
        <ul className="mt-4 flex flex-col gap-2 text-[15px]">
          {prices.blocked.map((line) => (
            <li key={line} className="rounded-lg bg-muted px-3 py-2">
              {line}
            </li>
          ))}
        </ul>
        <Button size="lg" className="mt-6 max-sm:w-full" loading={busy} onClick={() => void checkAgain()}>
          {t('sellerFlow.checkAgain')}
        </Button>
        {problem}
      </>
    )
  }
  return (
    <>
      <StepHeader step={step} title={title} lead={t('sellerFlow.prices.lead')} />
      {told}
      <form onSubmit={(e) => void save(e)} noValidate className="mt-4">
        <ul className="divide-y divide-border">
          {prices.plans.map((p) => (
            <PlanPrice
              key={p.id}
              plan={p}
              value={drafts[p.id] ?? ''}
              error={errors[p.id]}
              onChange={(v) => {
                setDrafts((d) => ({ ...d, [p.id]: v }))
                setErrors((errs) => Object.fromEntries(Object.entries(errs).filter(([plan]) => plan !== p.id)))
              }}
            />
          ))}
        </ul>
        <div className="mt-6 flex flex-col gap-2 sm:flex-row sm:items-center">
          <Button type="submit" size="lg" className="max-sm:w-full" loading={busy}>
            {edit ? t('sellerFlow.prices.save') : t('sellerFlow.prices.looksGood')}
          </Button>
          {back}
        </div>
      </form>
      {problem}
    </>
  )
}

/** One plan, what it gives, and its monthly price to check. */
function PlanPrice({ plan: p, value, error, onChange }: { plan: SellerPrice; value: string; error?: string; onChange: (value: string) => void }) {
  const id = useId()
  return (
    <li className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1.5 py-3">
      <div className="min-w-0">
        <label htmlFor={id} className="block text-[15px] font-medium">
          {p.title}
        </label>
        <span id={`${id}-gives`} className="block text-[13px] text-muted-foreground">
          {t('sellerFlow.prices.gives', { servers: t('unit.servers', { count: p.servers }), memory: formatMB(p.memoryMB) })}
        </span>
      </div>
      <InputGroup className="w-40">
        <InputGroupAddon>
          <InputGroupText>$</InputGroupText>
        </InputGroupAddon>
        <InputGroupInput
          id={id}
          value={value}
          inputMode="decimal"
          autoComplete="off"
          onChange={(e) => onChange(e.target.value)}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? `${id}-gives ${id}-error` : `${id}-gives`}
        />
        <InputGroupAddon align="inline-end">
          <InputGroupText>{t('sellerFlow.prices.perMonth')}</InputGroupText>
        </InputGroupAddon>
      </InputGroup>
      {error && (
        <p id={`${id}-error`} role="alert" className="w-full text-[13px] text-destructive-foreground">
          {error}
        </p>
      )}
    </li>
  )
}
