import { useState } from 'react'
import { ExternalLinkIcon } from 'lucide-react'
import { post } from '@/api/client'
import type { SellerCustomer, SellerOpened, SellerView } from '@/api/types'
import { Button, buttonVariants } from '@/components/ui/button'
import { formatLocale, t } from '@/i18n'
import { cn } from '@/lib/utils'
import { money, refusalOf, RefusalText, StepHeader, type Refusal } from './whop-seller-step'

/** How many customers the page names before saying how many more there are. */
const customersShown = 5

/** How many months of earnings the page shows, newest first. */
const monthsShown = 3

/**
 * A seller's store once it's open, inside their page in Whop: a link to the
 * store, what it earned and its customers, kept short. Change prices and
 * Update the store, for a plan added since, sit below. step is the steps'
 * count right after the seller opened the store, so they see they're done.
 * onChange tells the page the store changed, so it reads it again.
 */
export function LiveView({
  store,
  route,
  view,
  step,
  onChangePrices,
  onChange,
}: {
  store: string
  route?: string
  view: SellerView
  step?: number
  onChangePrices: () => void
  onChange: () => void
}) {
  const [updating, setUpdating] = useState(false)
  const [updated, setUpdated] = useState(false)
  const [refusal, setRefusal] = useState<Refusal>()
  const months = [...view.earnings].sort((a, b) => b.month.localeCompare(a.month)).slice(0, monthsShown)

  async function update() {
    setUpdating(true)
    setUpdated(false)
    setRefusal(undefined)
    try {
      await post<SellerOpened>(`/api/public/whop/seller/${store}/sell`)
      setUpdated(true)
      onChange()
    } catch (err) {
      setRefusal(refusalOf(err))
    } finally {
      setUpdating(false)
    }
  }

  return (
    <>
      <StepHeader step={step} title={t('sellerFlow.live.title')} lead={t('sellerFlow.live.lead')} />
      {view.store.state === 'needsLook' && view.store.why && <p className="mt-3 text-sm text-warning-foreground">{view.store.why}</p>}
      {route && (
        <a
          href={`https://whop.com/${route}`}
          target="_blank"
          rel="noreferrer"
          aria-label={t('common.external', { label: t('sellerFlow.live.visit') })}
          className={cn(buttonVariants({ size: 'lg' }), 'mt-6 max-sm:w-full')}
        >
          {t('sellerFlow.live.visit')}
          <ExternalLinkIcon className="size-4" aria-hidden="true" />
        </a>
      )}
      <section className="mt-8" aria-labelledby="seller-earnings">
        <h2 id="seller-earnings" className="text-[15px] font-semibold">
          {t('sellerFlow.live.earnings')}
        </h2>
        {months.length === 0 ? (
          <p className="mt-1 text-sm text-muted-foreground">{t('sellerFlow.live.noEarnings')}</p>
        ) : (
          <ul className="mt-1 divide-y divide-border">
            {months.map((m) => (
              <li key={`${m.month}-${m.currency}`} className="flex items-baseline justify-between gap-4 py-2 text-sm">
                <span>{new Date(`${m.month}-01T00:00:00Z`).toLocaleDateString(formatLocale(), { month: 'long', year: 'numeric', timeZone: 'UTC' })}</span>
                <span className="font-semibold tabular-nums">{money(m.kept, m.currency)}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
      <section className="mt-6" aria-labelledby="seller-customers">
        <h2 id="seller-customers" className="text-[15px] font-semibold">
          {view.customers.length > 0 ? t('sellerFlow.live.customers', { count: view.customers.length }) : t('sellerFlow.live.customersTitle')}
        </h2>
        {view.customers.length === 0 ? (
          <p className="mt-1 text-sm text-muted-foreground">{t('sellerFlow.live.noCustomers')}</p>
        ) : (
          <ul className="mt-1 divide-y divide-border">
            {view.customers.slice(0, customersShown).map((c) => (
              <li key={c.handle} className="flex items-baseline justify-between gap-4 py-2 text-sm">
                <span className="truncate font-medium">{c.handle}</span>
                <span className="shrink-0 text-muted-foreground">{status(c)}</span>
              </li>
            ))}
          </ul>
        )}
        {view.customers.length > customersShown && <p className="mt-1 text-sm text-muted-foreground">{t('sellerFlow.live.more', { count: view.customers.length - customersShown })}</p>}
      </section>
      <div className="mt-8 flex flex-col gap-2 sm:flex-row sm:flex-wrap">
        <Button variant="outline" className="max-sm:w-full" onClick={onChangePrices}>
          {t('sellerFlow.live.changePrices')}
        </Button>
        <Button variant="outline" className="max-sm:w-full" loading={updating} onClick={() => void update()}>
          {t('sellerFlow.live.update')}
        </Button>
      </div>
      {updated && (
        <p role="status" className="mt-3 text-sm font-medium text-success-strong">
          {t('sellerFlow.live.updated')}
        </p>
      )}
      {refusal && <RefusalText refusal={refusal} />}
    </>
  )
}

/** A customer's status, in a word or two. */
function status(c: SellerCustomer): string {
  switch (c.status) {
    case 'active':
      return t('sellerFlow.status.active')
    case 'starting':
      return t('sellerFlow.status.starting')
    case 'paused':
      return t('sellerFlow.status.paused')
    case 'suspended':
      return t('sellerFlow.status.suspended')
    case 'ended':
      return t('sellerFlow.status.ended')
    default: {
      const unknown: never = c.status
      return unknown
    }
  }
}
