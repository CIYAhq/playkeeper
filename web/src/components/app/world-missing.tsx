import type { ServerStatus } from '@/api/types'
import { Notice } from '@/components/app/bits'
import { t } from '@/i18n'
import { controls } from '@/lib/phase'

/**
 * The world folder is missing because a restore didn't finish: where the
 * previous world is, and how to put it back. It stays for as long as the
 * folder is missing, unlike a failed job's notice.
 */
export function WorldMissingNotice({ server: s, className }: { server: ServerStatus; className?: string }) {
  const m = s.worldMissing
  if (!m) return null
  return (
    <Notice tone="error" stacked className={className} title={t('world.missingTitle', { server: s.name })}>
      {t('world.missingBody', { previous: m.previous, data: m.dataDir })}
    </Notice>
  )
}

/**
 * The world folder is back but the restore that didn't finish isn't settled
 * yet: why restores and Discard wait, and what finishes it. Playkeeper settles
 * it by itself once the server is stopped and no other job runs.
 */
export function RestoreUnsettledNotice({ server: s, className }: { server: ServerStatus; className?: string }) {
  const u = s.restoreUnsettled
  if (!u || s.worldMissing) return null
  const title = u.problem ? t('world.unsettledStuck') : controls(s).canStop ? t('world.unsettledStop', { server: s.name }) : t('world.unsettledSoon')
  return (
    <Notice tone="warning" stacked className={className} title={title}>
      {u.problem}
    </Notice>
  )
}
