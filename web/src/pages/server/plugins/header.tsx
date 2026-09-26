import { ChevronLeftIcon } from 'lucide-react'
import type { ServerStatus } from '@/api/types'
import { t } from '@/i18n'
import { linkProps, type Route, type ServerSub } from '@/lib/router'

/** "‹ More · Plugins", or "‹ Plugins · Browse" in the library. Apart from the tab, so it shows before the tab's code has loaded. */
export function PluginsPhoneHeader({ server, tab, sub }: { server: ServerStatus; tab: 'plugins' | 'mods'; sub?: ServerSub }) {
  const name = tab === 'mods' ? t('tab.mods') : t('tab.plugins')
  const browsing = sub === 'browse'
  const back: Route = browsing ? { name: 'server', slug: server.slug, tab } : { name: 'more' }
  return (
    <header className="grid grid-cols-[1fr_auto_1fr] items-center gap-2 pt-2 pb-2">
      <a {...linkProps(back)} className="-ml-2 inline-flex min-h-11 items-center gap-0.5 justify-self-start rounded-lg px-1 text-[15px] font-medium text-success-strong">
        <ChevronLeftIcon className="size-5" aria-hidden="true" />
        {browsing ? name : t('nav.more')}
      </a>
      <h1 className="text-[17px] font-semibold">{browsing ? t('addons.browseTitle') : name}</h1>
    </header>
  )
}
