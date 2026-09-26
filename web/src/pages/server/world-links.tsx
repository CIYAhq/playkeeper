import type { ReactNode } from 'react'
import { ChevronRightIcon, MapIcon, PackageIcon } from 'lucide-react'
import type { Pregen, ServerStatus } from '@/api/types'
import { Spinner } from '@/components/app/bits'
import { InlineSkeleton } from '@/components/app/skeletons'
import { t } from '@/i18n'
import { worldMissingReason } from '@/lib/phase'
import { linkProps, type ServerSub } from '@/lib/router'
import { cn } from '@/lib/utils'
import { usePacksLine } from './world-packs'
import { pregenLine, usePregen } from './world-pregen'

function working(pg: Pregen | undefined): boolean {
  return pg?.state === 'starting' || pg?.state === 'running'
}

interface LinkProps {
  server: ServerStatus
  sub: ServerSub
  icon: ReactNode
  title: string
  /** The second line; undefined while it loads. */
  line: string | undefined
  /** Changes when the line says something new, rather than an updated number. */
  lineKey?: string
  busy?: boolean
  /** Why the page can't be opened now; the row isn't a link then. */
  disabledReason?: string
}

const desktopRow = '-mx-2 flex items-center gap-3 rounded-lg px-2 py-1 [&>svg]:size-4 [&>svg]:shrink-0 [&>svg]:text-muted-foreground'

function DesktopLink({ server, sub, icon, title, line, lineKey, busy, disabledReason }: LinkProps) {
  const content = (
    <>
      {icon}
      <span className="min-w-0 flex-1">
        <span className="block text-[13px] font-semibold">{title}</span>
        {line === undefined ? (
          <span className="block text-xs">
            <InlineSkeleton className="w-40" />
          </span>
        ) : (
          <span key={lineKey} className="flex animate-fade items-center gap-1.5 text-xs text-muted-foreground">
            {busy && <Spinner />}
            <span className="truncate">{line}</span>
          </span>
        )}
      </span>
      <ChevronRightIcon className="transition-transform duration-(--motion-fast) ease-standard group-hover:translate-x-0.5" aria-hidden="true" />
    </>
  )
  return (
    <li>
      {disabledReason ? (
        <span role="link" aria-disabled="true" title={disabledReason} className={cn(desktopRow, 'cursor-not-allowed opacity-64')}>
          {content}
        </span>
      ) : (
        <a {...linkProps({ name: 'server', slug: server.slug, tab: 'world', sub })} className={cn(desktopRow, 'group outline-none hover:bg-accent/60 focus-visible:ring-2 focus-visible:ring-ring active:bg-accent')}>
          {content}
        </a>
      )}
    </li>
  )
}

/** The World card's rows for pre-generating the map and for packs. */
export function WorldLinks({ server: s }: { server: ServerStatus }) {
  const pregen = usePregen(s)
  const packs = usePacksLine(s)
  const pg = pregen.data
  return (
    <>
      <DesktopLink server={s} sub="pregen" icon={<MapIcon />} title={t('world.pregen')} line={pg || pregen.error ? pregenLine(pg, s.name) : undefined} lineKey={pg?.state} busy={working(pg)} disabledReason={worldMissingReason(s)} />
      <DesktopLink server={s} sub="packs" icon={<PackageIcon />} title={t('world.packs')} line={packs} />
    </>
  )
}

/** A row of the phone's World list; the class matches the list's other rows. */
export const phoneRow = 'flex min-h-[52px] w-full items-center gap-3 px-4 py-2 text-left active:bg-accent/60 [&>svg]:size-5 [&>svg]:shrink-0 [&>svg]:text-muted-foreground'

function PhoneLink({ server, sub, icon, title, line, busy, disabledReason }: Omit<LinkProps, 'lineKey'>) {
  const content = (
    <>
      {icon}
      <span className="min-w-0 flex-1">
        <span className="block text-base">{title}</span>
        {line && (
          <span className="flex animate-fade items-center gap-1.5 text-[13px] text-muted-foreground">
            {busy && <Spinner />}
            <span className="truncate">{line}</span>
          </span>
        )}
      </span>
      <ChevronRightIcon aria-hidden="true" />
    </>
  )
  return (
    <li className="border-b border-border last:border-b-0">
      {disabledReason ? (
        <span role="link" aria-disabled="true" title={disabledReason} className={cn(phoneRow, 'cursor-not-allowed opacity-64 active:bg-transparent')}>
          {content}
        </span>
      ) : (
        <a {...linkProps({ name: 'server', slug: server.slug, tab: 'world', sub })} className={phoneRow}>
          {content}
        </a>
      )}
    </li>
  )
}

/** The phone's rows for pre-generating and packs; a running pre-generation shows its progress. */
export function PhoneWorldLinks({ server: s }: { server: ServerStatus }) {
  const pg = usePregen(s).data
  const active = pg?.state === 'starting' || pg?.state === 'running' || pg?.state === 'paused'
  return (
    <>
      <PhoneLink server={s} sub="pregen" icon={<MapIcon />} title={t('world.pregen')} line={active ? pregenLine(pg, s.name) : undefined} busy={working(pg)} disabledReason={worldMissingReason(s)} />
      <PhoneLink server={s} sub="packs" icon={<PackageIcon />} title={t('world.packs')} line={undefined} />
    </>
  )
}

/** The tab's pages, for a world without backups yet. */
export function WorldTools({ server, phone, className }: { server: ServerStatus; phone: boolean; className?: string }) {
  if (phone) {
    return (
      <ul className={cn('w-full overflow-hidden rounded-3xl border border-border bg-white text-left', className)}>
        <PhoneWorldLinks server={server} />
      </ul>
    )
  }
  return (
    <ul className={cn('grid w-full max-w-[720px] gap-x-8 border-t border-border pt-3 text-left sm:grid-cols-2', className)}>
      <WorldLinks server={server} />
    </ul>
  )
}
