import { useEffect, useRef } from 'react'
import { ApiError } from '@/api/client'
import { Stepper, useIsPhone } from '@/components/app/controls'
import { formatLocale, t } from '@/i18n'

/** The steps a seller's page walks them through, in order. */
const steps = () => [t('sellerFlow.step.prices'), t('sellerFlow.step.open'), t('sellerFlow.step.live')]

/**
 * A step's heading and its one short sentence. The heading takes the focus
 * when the step comes on screen, so a screen reader reads it. step is where
 * the seller is in the steps, from 0, or their number once they're all done,
 * or undefined off the steps.
 */
export function StepHeader({ title, lead, step }: { title: string; lead?: string; step?: number }) {
  const heading = useRef<HTMLHeadingElement>(null)
  const phone = useIsPhone()
  const names = steps()

  useEffect(() => {
    heading.current?.focus()
  }, [title])

  return (
    <>
      {step !== undefined &&
        (phone ? (
          <p className="text-[13px] text-muted-foreground">{t('sellerFlow.stepOf', { n: Math.min(step + 1, names.length), total: names.length })}</p>
        ) : (
          <Stepper steps={names} current={step} label={t('sellerFlow.steps')} className="mb-6" />
        ))}
      <h1 ref={heading} tabIndex={-1} className="mt-1 text-2xl font-bold outline-none">
        {title}
      </h1>
      {lead && <p className="mt-1.5 text-[15px] text-muted-foreground">{lead}</p>}
    </>
  )
}

/** Playkeeper Cloud's seller terms and privacy policy, on playkeeper.io. */
export const sellerTermsURL = 'https://playkeeper.io/cloud/seller-terms'
export const privacyURL = 'https://playkeeper.io/privacy'

/** The seller terms and the privacy policy, linked small at the foot of every screen and opened beside Whop. */
export function SellerLinks() {
  const link = (href: string, label: string) => (
    <a href={href} target="_blank" rel="noreferrer" aria-label={t('common.external', { label })} className="underline underline-offset-2 hover:text-foreground">
      {label}
    </a>
  )
  return (
    <p className="mt-8 flex gap-4 text-xs text-muted-foreground">
      {link(sellerTermsURL, t('sellerFlow.links.terms'))}
      {link(privacyURL, t('sellerFlow.links.privacy'))}
    </p>
  )
}

export const errorText = (err: unknown) => (err instanceof Error ? err.message : t('error.network'))

/** Why a store didn't open or update: its lines, and what to do, when there's something. */
export interface Refusal {
  lines: string[]
  fix?: string
}

export const refusalOf = (err: unknown): Refusal => ({ lines: errorText(err).split('\n'), fix: err instanceof ApiError ? err.hint : undefined })

export function RefusalText({ refusal }: { refusal: Refusal }) {
  return (
    <div role="alert" className="mt-4 text-sm text-warning-foreground">
      {refusal.lines.map((line, i) => (
        <p key={i}>{line}</p>
      ))}
      {refusal.fix && <p className="mt-1 font-medium text-foreground">{refusal.fix}</p>}
    </div>
  )
}

/** An amount in its currency's smallest unit, such as cents, as money, with its plain sign, such as $ rather than US$, since a seller's prices are all in one currency. */
export function money(amount: number, currency: string): string {
  const f = new Intl.NumberFormat(formatLocale(), { style: 'currency', currency: currency.toUpperCase(), currencyDisplay: 'narrowSymbol' })
  return f.format(amount / 10 ** (f.resolvedOptions().maximumFractionDigits ?? 2))
}
