import type { Address } from '@/api/types'
import { t } from '@/i18n'
import { rich } from '@/i18n/rich'
import { cn } from '@/lib/utils'

/** Let's Encrypt's terms, accepted by the button next to it until an admin has. */
export function TermsLine({ a, className }: { a: Address; className?: string }) {
  if (a.termsAccepted) return null
  return (
    <p className={cn('text-xs text-muted-foreground', className)}>
      {rich('address.terms', {
        link: (chunk) => (
          <a href={t('address.termsUrl')} target="_blank" rel="noreferrer" className="font-medium text-success-foreground hover:underline max-sm:text-success-strong">
            {chunk}
          </a>
        ),
      })}
    </p>
  )
}
