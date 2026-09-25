import type { ReactNode } from 'react'
import { ExternalLinkIcon } from 'lucide-react'
import { BrandMark } from '@/components/app/art'
import { Stepper, useIsPhone } from '@/components/app/controls'
import { t } from '@/i18n'
import { cn } from '@/lib/utils'

/**
 * The full-screen page for signing in and first-run setup: the brand, the
 * setup steps when there are some, Help, and the legal line at the bottom.
 */
export function Frame({ step, version, children, className }: { step?: number; version?: string; children: ReactNode; className?: string }) {
  const phone = useIsPhone()
  const steps = [t('onboarding.step.account'), t('onboarding.step.check'), t('onboarding.step.first')]
  return (
    <div className="flex min-h-dvh flex-col bg-sidebar">
      <a href="#main" className="skip-link rounded-lg bg-white px-3 py-2 text-sm font-medium shadow-popup">
        {t('nav.skip')}
      </a>
      <header className="flex h-14 shrink-0 items-center gap-4 px-6 max-sm:h-14 max-sm:gap-2.5 max-sm:px-4">
        <span className="flex items-center gap-2 text-[15px] font-bold">
          <BrandMark size={phone ? 26 : 24} />
          {(!phone || step === undefined) && t('brand.name')}
        </span>
        {step !== undefined && phone && <span className="text-[13px] text-muted-foreground">{t('onboarding.stepOf', { n: step + 1, total: steps.length })}</span>}
        {step !== undefined && !phone && <Stepper steps={steps} current={step} label={t('onboarding.steps')} className="mx-auto w-full max-w-[400px]" />}
        <a
          href={t('onboarding.helpUrl')}
          target="_blank"
          rel="noreferrer"
          aria-label={t('common.external', { label: t('common.help') })}
          className="ml-auto inline-flex min-h-11 items-center gap-1 rounded-lg px-1 text-[13px] text-muted-foreground hover:text-foreground max-sm:text-[15px]"
        >
          {t('common.help')}
          {!phone && <ExternalLinkIcon className="size-3.5" aria-hidden="true" />}
        </a>
      </header>
      <main id="main" tabIndex={-1} className={cn('flex flex-1 flex-col items-center justify-center px-6 py-8 outline-none max-sm:justify-start max-sm:px-4 max-sm:pt-2 max-sm:pb-6', className)}>
        {children}
      </main>
      <footer className="flex shrink-0 justify-between gap-6 px-6 py-4 text-xs text-muted-foreground max-sm:hidden">
        <span>{t('footer.notOfficial')}</span>
        {version && <span>{t('footer.version', { version })}</span>}
      </footer>
    </div>
  )
}

/** A white card in the middle of the frame. */
export function FrameCard({ children, className, wide }: { children: ReactNode; className?: string; wide?: boolean }) {
  return (
    <div className={cn('w-full rounded-4xl border border-border bg-card p-6 shadow-popup max-sm:rounded-none max-sm:border-0 max-sm:bg-transparent max-sm:p-0 max-sm:shadow-none', wide ? 'max-w-[640px]' : 'max-w-[460px]', className)}>
      {children}
    </div>
  )
}

/** On phones, the main action sits at the bottom of the screen. */
export function PhoneActions({ children }: { children: ReactNode }) {
  return (
    <div className="fixed inset-x-0 bottom-0 z-10 flex flex-col gap-2 border-t border-border bg-white px-4 pt-3 pb-[max(env(safe-area-inset-bottom),16px)] sm:hidden">
      {children}
    </div>
  )
}
