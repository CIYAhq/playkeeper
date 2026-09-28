import { CircleAlertIcon, CircleCheckIcon, CircleXIcon, RefreshCwIcon, UploadIcon, XIcon } from 'lucide-react'
import { Progress } from '@/components/app/bits'
import { Button } from '@/components/ui/button'
import { t } from '@/i18n'
import { baseName } from '@/lib/files'
import { formatBytes, formatPercent, formatSpan } from '@/lib/format'
import { cn } from '@/lib/utils'
import { folderName } from './dialogs'
import { unplaced, type FileUploads, type UploadBatch } from './uploads-state'

/** The Files tab's uploads: the one sending now, how many wait, and what finished. */
export function UploadPanel({ uploads, phone }: { uploads: FileUploads; phone: boolean }) {
  const waiting = uploads.batches.filter((b) => b.state === 'waiting')
  const shown = uploads.batches.filter((b) => b.state !== 'waiting')
  if (uploads.batches.length === 0) return null
  return (
    <div className="flex flex-col gap-2" aria-live="polite">
      {shown.map((b) => (
        <BatchCard key={b.id} batch={b} uploads={uploads} phone={phone} />
      ))}
      {waiting.length > 0 && <p className="px-1 text-xs text-muted-foreground max-sm:text-[13px]">{t('files.upload.queued', { count: waiting.length })}</p>}
    </div>
  )
}

function BatchCard({ batch: b, uploads, phone }: { batch: UploadBatch; uploads: FileUploads; phone: boolean }) {
  const failedFiles = unplaced(b)
  const first = baseName(b.picked[0]?.path ?? '')
  const folder = folderName(b.folder)
  const vars = { count: b.picked.length, name: first, folder }
  const percent = b.state === 'done' ? 100 : b.total > 0 ? Math.floor((b.sent / b.total) * 100) : 0
  const sizes = { sent: formatBytes(b.sent), total: formatBytes(b.total) }
  let icon = <UploadIcon className="size-5 shrink-0 text-primary" aria-hidden="true" />
  let title = b.picked.length === 1 ? t('files.upload.titleOne', vars) : t('files.upload.titleMany', vars)
  let detail = b.retrying ? t('files.upload.retrying') : b.secondsLeft !== undefined ? t('files.upload.left', { ...sizes, time: formatSpan(b.secondsLeft) }) : t('files.upload.progress', sizes)
  if (b.state === 'done') {
    icon = failedFiles.length ? <CircleAlertIcon className="size-5 shrink-0 text-warning-foreground" aria-hidden="true" /> : <CircleCheckIcon className="size-5 shrink-0 text-success-foreground" aria-hidden="true" />
    title = b.picked.length === 1 ? t('files.upload.doneOne', vars) : t('files.upload.doneMany', vars)
    detail = failedFiles.length ? t('files.upload.someFailed', { count: failedFiles.length }) : formatBytes(b.total)
  } else if (b.state === 'failed') {
    icon = <CircleXIcon className="size-5 shrink-0 text-destructive-foreground" aria-hidden="true" />
    title = t('files.upload.failed')
    detail = b.error ?? ''
  }
  const size = phone ? 'default' : 'sm'
  return (
    <div className="animate-enter rounded-2xl border border-border bg-card p-4 shadow-card max-sm:rounded-3xl">
      <div className="flex items-center gap-3">
        {icon}
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-semibold max-sm:text-[15px]">{title}</p>
          <p className={cn('truncate text-xs tabular-nums max-sm:text-[13px]', b.state === 'failed' ? 'text-destructive-foreground' : 'text-muted-foreground')} role={b.state === 'failed' ? 'alert' : undefined}>
            {detail}
          </p>
        </div>
        {b.state === 'uploading' && <span className="text-sm font-semibold tabular-nums">{formatPercent(percent)}</span>}
        {b.state === 'failed' && (
          <Button variant="outline" size={size} onClick={() => uploads.retry(b.id)}>
            <RefreshCwIcon />
            {t('common.tryAgain')}
          </Button>
        )}
        {b.state === 'uploading' || b.state === 'failed' ? (
          <Button variant="ghost" size={size} onClick={() => uploads.cancel(b.id)}>
            {t('common.cancel')}
          </Button>
        ) : (
          <Button variant="ghost" size={phone ? 'icon' : 'icon-sm'} aria-label={t('common.dismiss')} onClick={() => uploads.dismiss(b.id)}>
            <XIcon />
          </Button>
        )}
      </div>
      {b.state === 'uploading' && (
        <>
          <Progress value={percent} tone="info" className="mt-3" label={title} />
          {!b.retrying && <p className="mt-2 text-xs text-muted-foreground max-sm:text-[13px]">{t('files.upload.resumes')}</p>}
        </>
      )}
      {failedFiles.length > 0 && (
        <ul className="mt-3 flex flex-col gap-2 border-t border-border pt-3">
          {failedFiles.map((f) => (
            <li key={f.index} className="flex items-center gap-3">
              <div className="min-w-0 flex-1">
                <p className="truncate text-[13px] font-medium">{f.name}</p>
                <p className="text-xs wrap-anywhere text-muted-foreground max-sm:text-[13px]">{f.error}</p>
              </div>
              <Button variant="outline" size={size} onClick={() => void uploads.place(b.id, f.index)}>
                <RefreshCwIcon />
                {t('common.tryAgain')}
              </Button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
