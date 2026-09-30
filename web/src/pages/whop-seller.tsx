import { useEffect, useState } from 'react'
import { ExternalLinkIcon } from 'lucide-react'
import { ApiError, post } from '@/api/client'
import type { WhopSellerOpen } from '@/api/types'
import { Frame, FrameCard } from '@/components/app/frame'
import { Spinner } from '@/components/ui/spinner'
import { t } from '@/i18n'
import { SellerPricesCard } from './whop-seller-prices'
import { SellerStoreView } from './whop-seller-view'

type State =
  | { kind: 'opening' }
  | { kind: 'open'; open: WhopSellerOpen }
  | { kind: 'unapproved'; installUrl: string }
  | { kind: 'refused'; text: string }

/**
 * A seller's page inside their Whop dashboard, which Whop shows through its
 * proxy with a token saying who's looking. Opening it registers the business
 * as a store on Playkeeper Cloud, once the business approved everything the
 * app asks for, and then shows the seller's view of it. Its calls go to
 * relative addresses, which carry Whop's token.
 */
export function WhopSellerPage({ store }: { store: string }) {
  const [state, setState] = useState<State>(() => (store ? { kind: 'opening' } : { kind: 'refused', text: t('whopSeller.notABusiness') }))
  const [viewed, setViewed] = useState(0)

  useEffect(() => {
    if (!store) return
    let cancelled = false
    post<WhopSellerOpen>(`/api/public/whop/seller/${store}/open`)
      .then((open) => !cancelled && setState({ kind: 'open', open }))
      .catch((err: unknown) => {
        if (cancelled) return
        if (err instanceof ApiError && err.code === 'whop_not_approved') setState({ kind: 'unapproved', installUrl: String(err.params?.installUrl ?? '') })
        else if (err instanceof ApiError && err.code === 'whop_not_team') setState({ kind: 'refused', text: t('whopSeller.notTeam') })
        else if (err instanceof ApiError && err.code === 'whop_token') setState({ kind: 'refused', text: t('whopSeller.noToken') })
        else setState({ kind: 'refused', text: err instanceof Error ? err.message : t('error.network') })
      })
    return () => {
      cancelled = true
    }
  }, [store])

  return (
    <Frame>
      <FrameCard>
        <h1 className="text-xl font-bold">{t('whopSeller.title')}</h1>
        {state.kind === 'opening' && (
          <p className="mt-3 flex items-center gap-2 text-sm text-muted-foreground">
            <Spinner className="size-4" />
            {t('whopSeller.opening')}
          </p>
        )}
        {state.kind === 'open' && (
          <>
            <p className="mt-2 text-sm">{t(state.open.new ? 'whopSeller.connected' : 'whopSeller.open', { store: state.open.store.title || state.open.store.id })}</p>
            {state.open.store.problem && <p className="mt-2 text-sm text-muted-foreground">{t('whopSeller.problem', { problem: state.open.store.problem })}</p>}
            <SellerPricesCard store={store} onChange={() => setViewed((n) => n + 1)} />
            <SellerStoreView key={viewed} store={store} />
          </>
        )}
        {state.kind === 'unapproved' && (
          <>
            <p className="mt-2 text-sm">{t('whopSeller.unapproved')}</p>
            {state.installUrl && (
              <a href={state.installUrl} target="_blank" rel="noreferrer" className="mt-3 inline-flex items-center gap-1 text-sm font-medium text-success-strong hover:underline">
                {t('whopSeller.approve')}
                <ExternalLinkIcon className="size-3" aria-hidden="true" />
              </a>
            )}
          </>
        )}
        {state.kind === 'refused' && <p className="mt-2 text-sm text-destructive-foreground">{state.text}</p>}
      </FrameCard>
    </Frame>
  )
}
