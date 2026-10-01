import { useCallback, useEffect, useState } from 'react'
import { ExternalLinkIcon } from 'lucide-react'
import { ApiError, get, post } from '@/api/client'
import type { SellerOpened, SellerPrices, SellerView, WhopSellerOpen } from '@/api/types'
import { Frame, FrameCard } from '@/components/app/frame'
import { buttonVariants, Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Spinner } from '@/components/ui/spinner'
import { t } from '@/i18n'
import { rich } from '@/i18n/rich'
import { cn } from '@/lib/utils'
import { PricesStep } from './whop-seller-prices'
import { errorText, refusalOf, RefusalText, SellerLinks, sellersHelpURL, sellerTermsURL, StepHeader, type Refusal } from './whop-seller-step'
import { LiveView } from './whop-seller-view'

type State =
  | { kind: 'opening' }
  | { kind: 'open'; open: WhopSellerOpen }
  | { kind: 'unapproved'; installUrl: string }
  | { kind: 'refused'; text: string }

/**
 * A seller's page inside their Whop dashboard, which Whop shows through its
 * proxy with a token saying who's looking. Opening it registers the business
 * as a store on Playkeeper Cloud, once the business approved everything the
 * app asks for, and then walks the seller through opening it (SellerFlow).
 * Its calls go to relative addresses, which carry Whop's token.
 */
export function WhopSellerPage({ store }: { store: string }) {
  const [state, setState] = useState<State>(() => (store ? { kind: 'opening' } : { kind: 'refused', text: t('whopSeller.notABusiness') }))

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
        else setState({ kind: 'refused', text: errorText(err) })
      })
    return () => {
      cancelled = true
    }
  }, [store])

  return (
    <Frame help={sellersHelpURL}>
      <FrameCard>
        {state.kind === 'opening' && <Waiting />}
        {state.kind === 'open' && <SellerFlow store={store} open={state.open} />}
        {state.kind === 'unapproved' && (
          <>
            <StepHeader title={t('whopSeller.approveTitle')} lead={t('whopSeller.unapproved')} />
            {state.installUrl && (
              <a
                href={state.installUrl}
                target="_blank"
                rel="noreferrer"
                aria-label={t('common.external', { label: t('whopSeller.approve') })}
                className={cn(buttonVariants({ size: 'lg' }), 'mt-6 max-sm:w-full')}
              >
                {t('whopSeller.approve')}
                <ExternalLinkIcon className="size-4" aria-hidden="true" />
              </a>
            )}
          </>
        )}
        {state.kind === 'refused' && (
          <>
            <h1 className="text-2xl font-bold">{t('whopSeller.title')}</h1>
            <p className="mt-2 text-[15px] text-destructive-foreground">{state.text}</p>
          </>
        )}
        <SellerLinks />
      </FrameCard>
    </Frame>
  )
}

function Waiting() {
  return (
    <p className="flex items-center gap-2 text-[15px] text-muted-foreground">
      <Spinner className="size-4" />
      {t('sellerFlow.waiting')}
    </p>
  )
}

/** Where the seller is on their page: checking prices, opening, their open store, or changing its prices. */
type Screen = 'prices' | 'open' | 'live' | 'edit'

/**
 * The seller's way through their page, one step on screen at a time: check
 * the prices, open the store, and then it's live, with what it earned and
 * its customers. A store Playkeeper suspended, or one that left, says so.
 */
function SellerFlow({ store, open }: { store: string; open: WhopSellerOpen }) {
  const [prices, setPrices] = useState<SellerPrices>()
  const [view, setView] = useState<SellerView>()
  const [screen, setScreen] = useState<Screen>()
  const [failed, setFailed] = useState<string>()
  const [notice, setNotice] = useState<string>()
  const [opened, setOpened] = useState(false)
  const [drafts, setDrafts] = useState(0)

  const read = useCallback(
    async (next?: Screen) => {
      try {
        const [p, v] = await Promise.all([get<SellerPrices>(`/api/public/whop/seller/${store}/prices`), get<SellerView>(`/api/public/whop/seller/${store}`)])
        setPrices(p)
        setView(v)
        setScreen(next ?? (p.canUpdate ? 'live' : 'prices'))
      } catch (err) {
        setFailed(errorText(err))
      }
    },
    [store],
  )

  useEffect(() => {
    void read()
  }, [read])

  if (failed) return <p className="text-[15px] text-destructive-foreground">{failed}</p>
  if (!prices || !view || !screen) return <Waiting />
  if (view.store.state === 'suspended') return <StepHeader title={t('sellerFlow.suspended.title')} lead={t('sellerFlow.suspended.lead')} />
  if (view.store.state === 'left') return <StepHeader title={t('sellerFlow.left.title')} lead={t('sellerFlow.left.lead')} />

  const repriced = (p: SellerPrices, changed?: string) => {
    setPrices(p)
    setNotice(changed)
    setDrafts((n) => n + 1)
  }
  const step = (() => {
    switch (screen) {
      case 'prices':
        return (
          <PricesStep
            key={drafts}
            store={store}
            prices={prices}
            notice={notice}
            onPrices={repriced}
            onSaved={setPrices}
            onDone={(p) => {
              setPrices(p)
              setNotice(undefined)
              setScreen('open')
            }}
          />
        )
      case 'open':
        return (
          <OpenStep
            store={store}
            onOpened={() => {
              setOpened(true)
              void read('live')
            }}
            onBack={() => setScreen('prices')}
          />
        )
      case 'live':
        return <LiveView store={store} route={open.store.route} view={view} step={opened ? 3 : undefined} onChangePrices={() => setScreen('edit')} onChange={() => void read('live')} />
      case 'edit':
        return (
          <PricesStep
            key={drafts}
            store={store}
            prices={prices}
            edit
            onPrices={repriced}
            onSaved={setPrices}
            onDone={(p) => {
              setPrices(p)
              setDrafts((n) => n + 1)
              setScreen('live')
            }}
            onBack={() => setScreen('live')}
          />
        )
      default: {
        const unknown: never = screen
        return unknown
      }
    }
  })()
  return (
    <>
      {open.store.problem && <p className="mb-4 rounded-lg bg-muted px-3 py-2 text-sm">{t('whopSeller.problem', { problem: open.store.problem })}</p>}
      {step}
    </>
  )
}

/** The step that opens the store, once the seller ticks that they accept the seller terms, with one button and a way back to the prices. */
function OpenStep({ store, onOpened, onBack }: { store: string; onOpened: () => void; onBack: () => void }) {
  const [accepted, setAccepted] = useState(false)
  const [opening, setOpening] = useState(false)
  const [refusal, setRefusal] = useState<Refusal>()

  async function openStore() {
    setOpening(true)
    setRefusal(undefined)
    try {
      const done = await post<SellerOpened>(`/api/public/whop/seller/${store}/sell`, { acceptTerms: accepted })
      if (done.open) onOpened()
      else setRefusal({ lines: [t('sellerFlow.open.almost', { why: done.why ?? '' })] })
    } catch (err) {
      setRefusal(refusalOf(err))
    } finally {
      setOpening(false)
    }
  }

  return (
    <>
      <StepHeader step={1} title={t('sellerFlow.open.title')} lead={t('sellerFlow.open.lead')} />
      <label className="mt-5 flex items-start gap-3 text-[15px]">
        <Checkbox checked={accepted} onCheckedChange={(c) => setAccepted(c === true)} className="mt-0.5" />
        <span>
          {rich('sellerFlow.open.terms', {
            link: (chunk) => (
              <a href={sellerTermsURL} target="_blank" rel="noreferrer" className="font-medium underline underline-offset-2" onClick={(e) => e.stopPropagation()}>
                {chunk}
              </a>
            ),
          })}
        </span>
      </label>
      <div className="mt-6 flex flex-col gap-2 sm:flex-row sm:items-center">
        <Button size="lg" className="max-sm:w-full" disabled={!accepted} loading={opening} onClick={() => void openStore()}>
          {t('sellerFlow.open.button')}
        </Button>
        <Button variant="ghost" className="max-sm:w-full" onClick={onBack}>
          {t('sellerFlow.back')}
        </Button>
      </div>
      {refusal && <RefusalText refusal={refusal} />}
    </>
  )
}
