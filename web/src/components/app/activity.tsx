import type { ReactNode } from 'react'
import { ArchiveIcon, CircleAlertIcon, CircleArrowUpIcon, DownloadIcon, HistoryIcon, LogInIcon, MemoryStickIcon, PlayIcon, PowerIcon, RotateCwIcon, ShieldCheckIcon, ShieldOffIcon, SlidersHorizontalIcon, SproutIcon, SquareIcon, UserMinusIcon, UserPlusIcon, UserXIcon } from 'lucide-react'
import type { Activity, ActivityKind, ProjectRole, ServerStatus } from '@/api/types'
import { useWorkspace } from '@/api/workspace'
import { ListSkeleton } from '@/components/app/skeletons'
import { Skeleton } from '@/components/ui/skeleton'
import { t } from '@/i18n'
import { projectRoles, roleName } from '@/lib/access'
import { relativeTime } from '@/lib/format'
import { cn } from '@/lib/utils'

function icon(kind: ActivityKind): ReactNode {
  switch (kind) {
    case 'joined':
      return <LogInIcon />
    case 'crashed':
      return <CircleAlertIcon />
    case 'crashed_memory':
      return <MemoryStickIcon />
    case 'created':
      return <SproutIcon />
    case 'restored':
      return <HistoryIcon />
    case 'version':
      return <CircleArrowUpIcon />
    case 'stopped_outside':
      return <PowerIcon />
    case 'allowlisted':
      return <UserPlusIcon />
    case 'unlisted':
      return <UserMinusIcon />
    case 'operator':
      return <ShieldCheckIcon />
    case 'deoperator':
      return <ShieldOffIcon />
    case 'kicked':
      return <UserXIcon />
    case 'backup':
      return <ArchiveIcon />
    case 'downloaded':
      return <DownloadIcon />
    case 'started':
      return <PlayIcon />
    case 'stopped':
      return <SquareIcon />
    case 'restarted':
      return <RotateCwIcon />
    case 'settings':
      return <SlidersHorizontalIcon />
    case 'team_joined':
      return <UserPlusIcon />
    default: {
      const unreachable: never = kind
      return unreachable
    }
  }
}

/** One activity entry as a sentence. `here` drops the server's name where it's obvious. */
export function activityText(a: Activity, server: string, me: string, here = false): string {
  const actor = !a.actor || a.actor === me ? t('activity.you') : a.actor
  const player = a.player ?? ''
  switch (a.kind) {
    case 'joined':
      return here ? t('activity.joinedHere', { player }) : t('activity.joined', { player, server })
    case 'crashed':
      return t('activity.crashed', { server })
    case 'crashed_memory':
      return t('activity.crashedMemory', { server })
    case 'created':
      return a.detail ? t('activity.created', { server, detail: a.detail }) : t('activity.createdPlain', { server })
    case 'restored':
      return t('activity.restored', { server })
    case 'version':
      return a.detail ? t('activity.version', { server, detail: a.detail }) : t('activity.versionPlain', { server })
    case 'stopped_outside':
      return t('activity.stopped_outside', { server })
    case 'allowlisted':
      return a.actor?.startsWith('invite:') ? t('activity.allowlistedByLink', { player }) : t('activity.allowlisted', { actor, player })
    case 'unlisted':
      return t('activity.unlisted', { actor, player })
    case 'operator':
      return t('activity.operator', { actor, player })
    case 'deoperator':
      return t('activity.deoperator', { actor, player })
    case 'kicked':
      return t('activity.kicked', { actor, player })
    case 'backup':
      return t('activity.backup', { actor, server })
    case 'downloaded':
      return t('activity.downloaded', { actor, server })
    case 'started':
      return t('activity.started', { server })
    case 'stopped':
      return t('activity.stopped', { server })
    case 'restarted':
      return t('activity.restarted', { server })
    case 'settings':
      return t('activity.settings', { actor, server })
    case 'team_joined': {
      const role = projectRoles.find((r): r is ProjectRole => r === a.detail)
      return t('activity.teamJoined', { name: a.actor ?? '', role: role ? roleName(role) : (a.detail ?? '') })
    }
    default: {
      const unreachable: never = a.kind
      return unreachable
    }
  }
}

export function ActivityList({ items, servers, here, empty, className }: { items: Activity[] | undefined; servers: ServerStatus[]; here?: boolean; empty: string; className?: string }) {
  const { me } = useWorkspace()
  if (!items) return <ListSkeleton rows={5} lines={1} face="size-4 rounded" rowClassName="flex min-h-[26px] items-center gap-3 py-[5px]" trailing={<Skeleton className="h-2.5 w-10 shrink-0" />} className={cn('flex flex-col', className)} />
  if (items.length === 0) return <p className={cn('flex-1 py-2 text-[13px] text-muted-foreground', className)}>{empty}</p>
  return (
    <ul className={cn('flex flex-col', className)}>
      {items.map((a, i) => {
        const name = servers.find((s) => s.id === a.serverId)?.name ?? ''
        return (
          <li key={`${a.ts}-${a.kind}-${i}`} className="flex min-h-[26px] items-center gap-3 py-[5px] text-[13px] leading-[18px]">
            <span className="shrink-0 text-muted-foreground [&_svg]:size-4" aria-hidden="true">
              {icon(a.kind)}
            </span>
            <span className="min-w-0 flex-1 truncate">{activityText(a, name, me.user.username, here)}</span>
            <time dateTime={a.ts} className="shrink-0 text-xs text-muted-foreground">
              {relativeTime(a.ts)}
            </time>
          </li>
        )
      })}
    </ul>
  )
}
