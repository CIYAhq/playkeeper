import { useEffect, useState } from 'react'
import { get } from '@/api/client'
import type { SellerCustomer, SellerMonth, SellerPlan, SellerStore, SellerView } from '@/api/types'
import { Spinner } from '@/components/ui/spinner'
import { formatLocale, t } from '@/i18n'
import { formatLongDate, formatMB } from '@/lib/format'

/**
 * A seller's view of their store on Playkeeper Cloud, inside their page in
 * their Whop dashboard: how the store stands, its plans, its customers and
 * what it earned. Its one call goes to a relative address, which carries
 * Whop's token.
 */
export function SellerStoreView({ store }: { store: string }) {
  const [view, setView] = useState<SellerView>()
  const [error, setError] = useState<string>()

  useEffect(() => {
    let cancelled = false
    get<SellerView>(`/api/public/whop/seller/${store}`)
      .then((v) => !cancelled && setView(v))
      .catch((err: unknown) => !cancelled && setError(err instanceof Error ? err.message : t('error.network')))
    return () => {
      cancelled = true
    }
  }, [store])

  if (error) return <p className="mt-4 text-sm text-destructive-foreground">{error}</p>
  if (!view) {
    return (
      <p className="mt-4 flex items-center gap-2 text-sm text-muted-foreground">
        <Spinner className="size-4" />
        {t('sellerView.loading')}
      </p>
    )
  }
  return (
    <div className="mt-4 flex flex-col gap-5">
      <StoreState store={view.store} />
      <section>
        <h2 className="text-[15px] font-semibold">{t('sellerView.plans')}</h2>
        {view.plans.length === 0 ? (
          <p className="mt-1 text-sm text-muted-foreground">{t('sellerView.noPlans')}</p>
        ) : (
          <ul className="mt-1 divide-y divide-border">
            {view.plans.map((p) => (
              <PlanRow key={p.id} plan={p} />
            ))}
          </ul>
        )}
      </section>
      <section>
        <h2 className="text-[15px] font-semibold">{t('sellerView.customers')}</h2>
        {view.customers.length === 0 ? (
          <p className="mt-1 text-sm text-muted-foreground">{t('sellerView.noCustomers')}</p>
        ) : (
          <ul className="mt-1 divide-y divide-border">
            {view.customers.map((c) => (
              <CustomerRow key={c.handle} customer={c} />
            ))}
          </ul>
        )}
      </section>
      <section>
        <h2 className="text-[15px] font-semibold">{t('sellerView.earnings')}</h2>
        {view.earnings.length === 0 ? (
          <p className="mt-1 text-sm text-muted-foreground">{t('sellerView.noEarnings')}</p>
        ) : (
          <ul className="mt-1 divide-y divide-border">
            {view.earnings.map((m) => (
              <MonthRow key={`${m.month}-${m.currency}`} month={m} />
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}

/** How the store stands, and the words for it when there are some. */
function StoreState({ store }: { store: SellerStore }) {
  let text: string
  switch (store.state) {
    case 'selling':
      text = t('sellerView.selling')
      break
    case 'closed':
      text = t('sellerView.closed')
      break
    case 'needsLook':
      text = t('sellerView.needsLook')
      break
    case 'suspended':
      text = t('sellerView.suspended')
      break
    case 'left':
      text = t('sellerView.left')
      break
    default: {
      const unknown: never = store.state
      text = unknown
    }
  }
  return (
    <div className="text-sm">
      <p className={store.state === 'selling' ? '' : 'font-medium text-warning-foreground'}>{text}</p>
      {store.why && <p className="mt-0.5 text-muted-foreground">{store.why}</p>}
    </div>
  )
}

function PlanRow({ plan: p }: { plan: SellerPlan }) {
  const parts = [
    p.price,
    t('sellerView.plan', { servers: t('unit.servers', { count: p.servers }), memory: formatMB(p.memoryMB) }),
    p.unlimitedStock ? t('sellerView.unlimited') : t('sellerView.stock', { count: p.stock }),
    t('sellerView.planCustomers', { count: p.customers }),
  ].filter(Boolean)
  return (
    <li className="py-2 text-sm">
      <span className="block font-medium">{p.title}</span>
      <span className="block text-xs text-muted-foreground">{parts.join(t('common.dot'))}</span>
    </li>
  )
}

/** A customer's status, as their seller reads it. */
function customerStatus(c: SellerCustomer): string {
  switch (c.status) {
    case 'active':
      return t('sellerView.status.active')
    case 'starting':
      return t('sellerView.status.starting')
    case 'paused':
      return t('sellerView.status.paused')
    case 'suspended':
      return t('sellerView.status.suspended')
    case 'ended':
      return t('sellerView.status.ended')
    default: {
      const unknown: never = c.status
      return unknown
    }
  }
}

function CustomerRow({ customer: c }: { customer: SellerCustomer }) {
  const parts = [c.plan ?? '', customerStatus(c), c.since ? t('sellerView.since', { when: formatLongDate(c.since) }) : ''].filter(Boolean)
  return (
    <li className="py-2 text-sm">
      <span className="block font-medium">{c.handle}</span>
      <span className="block text-xs text-muted-foreground">{parts.join(t('common.dot'))}</span>
    </li>
  )
}

/** An amount in its currency's smallest unit, such as cents, as money. */
export function money(amount: number, currency: string): string {
  const f = new Intl.NumberFormat(formatLocale(), { style: 'currency', currency: currency.toUpperCase() })
  return f.format(amount / 10 ** (f.resolvedOptions().maximumFractionDigits ?? 2))
}

function MonthRow({ month: m }: { month: SellerMonth }) {
  const name = new Date(`${m.month}-01T00:00:00Z`).toLocaleDateString(formatLocale(), { month: 'long', year: 'numeric', timeZone: 'UTC' })
  return (
    <li className="py-2 text-sm">
      <span className="block font-medium">{name}</span>
      <span className="block text-xs text-muted-foreground">{t('sellerView.month', { sales: money(m.sales, m.currency), share: money(m.share, m.currency), kept: money(m.kept, m.currency) })}</span>
    </li>
  )
}
