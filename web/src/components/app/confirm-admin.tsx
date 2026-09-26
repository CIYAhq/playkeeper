import { useState } from 'react'
import { ShieldCheckIcon } from 'lucide-react'
import { post } from '@/api/client'
import type { TeamMember } from '@/api/types'
import { errorText } from '@/api/workspace'
import { Notice } from '@/components/app/bits'
import { Button } from '@/components/ui/button'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'

// Wave 5: confirming a new Admin, which Home and Settings › Team both offer;
// apart from the Team page (pages/team.tsx) so Home loads none of it.

/** Gives a member who turned on two-factor sign-in the Admin rights they wait for. */
export async function confirmAdmin(m: TeamMember) {
  try {
    await post(`/api/team/members/${m.id}/confirm-admin`)
    toastManager.add({ title: t('team.confirmedToast', { name: m.username }), type: 'success' })
  } catch (e) {
    toastManager.add({ title: errorText(e), type: 'error' })
  }
}

/** A member who turned on two-factor sign-in and waits for their Admin rights: one click confirms them. */
export function ConfirmAdminNotice({ member, onConfirmed }: { member: TeamMember; onConfirmed: () => Promise<void> }) {
  const [busy, setBusy] = useState(false)
  async function confirm() {
    setBusy(true)
    await confirmAdmin(member)
    await onConfirmed()
    setBusy(false)
  }
  return (
    <Notice
      title={t('team.confirmTitle', { name: member.username })}
      className="animate-enter"
      action={
        <Button size="sm" loading={busy} onClick={() => void confirm()}>
          <ShieldCheckIcon />
          {t('team.confirm')}
        </Button>
      }
    >
      {t('team.confirmBody')}
    </Notice>
  )
}
