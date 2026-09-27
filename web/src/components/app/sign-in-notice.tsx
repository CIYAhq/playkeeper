import type { MouseEvent } from 'react'
import { XIcon } from 'lucide-react'
import { useWorkspace } from '@/api/workspace'
import { Card, Notice } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n'
import { linkPath, linkProps } from '@/lib/router'

/**
 * The one thing worth saying after signing in: wrong codes someone else
 * entered, or how many recovery codes are left. Inline text on a computer,
 * a card on a phone. Following its link dismisses it too.
 */
export function SignInNotice() {
  const { signInNotice: notice, dismissSignInNotice: dismiss } = useWorkspace()
  const phone = useIsPhone()
  if (!notice) return null
  const wrongCodes = notice.kind === 'failed_attempts'
  const title = wrongCodes ? t('signinNotice.wrongCodes', { count: notice.count }) : t('signinNotice.codesLeft', { count: notice.count })
  const body = wrongCodes ? t('signinNotice.wrongCodesBody', { count: notice.count }) : undefined
  const target = wrongCodes ? linkPath('/account#password') : linkProps({ name: 'account' })
  const link = (
    <a
      href={target.href}
      onClick={(e: MouseEvent<HTMLAnchorElement>) => {
        dismiss()
        target.onClick(e)
      }}
    />
  )
  const linkLabel = wrongCodes ? t('account.changePassword') : t('signinNotice.goToAccount')

  if (phone) {
    return (
      <Card className="p-4" role="status">
        <p className="text-[17px] leading-[22px] font-semibold text-warning-foreground">{title}</p>
        {body && <p className="mt-1.5 text-[15px] leading-5 text-muted-foreground">{body}</p>}
        <div className="mt-3.5 grid grid-cols-2 gap-2.5">
          <Button variant="outline" size="touch" onClick={dismiss}>
            {wrongCodes ? t('signinNotice.itWasMe') : t('common.dismiss')}
          </Button>
          <Button size="touch" render={link}>
            {linkLabel}
          </Button>
        </div>
      </Card>
    )
  }
  return (
    <Notice
      tone="warning"
      stacked
      title={title}
      action={
        <div className="flex items-center gap-2">
          {wrongCodes && (
            <Button variant="ghost" size="sm" onClick={dismiss}>
              {t('signinNotice.itWasMe')}
            </Button>
          )}
          <Button variant="outline" size="sm" render={link}>
            {linkLabel}
          </Button>
          {!wrongCodes && (
            <Button variant="ghost" size="icon-sm" aria-label={t('common.dismiss')} onClick={dismiss}>
              <XIcon />
            </Button>
          )}
        </div>
      }
    >
      {body}
    </Notice>
  )
}
