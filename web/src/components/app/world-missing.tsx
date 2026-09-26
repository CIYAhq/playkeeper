import type { ServerStatus } from '@/api/types'
import { Notice } from '@/components/app/bits'
import { t } from '@/i18n'

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
