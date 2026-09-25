import { useRef, useState, type DragEvent } from 'react'
import { UploadIcon } from 'lucide-react'
import { api, del, post } from '@/api/client'
import type { RestorePreview, ServerStatus } from '@/api/types'
import { errorText, machineApi, serverApi, useWorkspace } from '@/api/workspace'
import { Pip } from '@/components/app/art'
import { Spinner } from '@/components/app/bits'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogDescription, DialogFooter, DialogPanel, DialogPopup, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { toastManager } from '@/components/ui/toast'
import { t } from '@/i18n'
import { rich } from '@/i18n/rich'
import { formatBytes, formatDateTime, formatMB } from '@/lib/format'
import { navigate } from '@/lib/router'
import { softwareName } from '@/lib/servers'
import { cn } from '@/lib/utils'

const maxUpload = 20 << 30

/** Uploads a Playkeeper backup file, for a server or (without one) as a new server. */
export async function uploadBackup(file: File, machineId: string, server?: ServerStatus): Promise<RestorePreview> {
  const path = server ? serverApi(server.id, '/restore/upload') : machineApi(machineId, '/restore/upload')
  return api<RestorePreview>('POST', path, undefined, file)
}

/** A drop zone for a backup file; the preview opens once it's checked. */
export function RestoreDropZone({ server, onPreview, className, compact }: { server?: ServerStatus; onPreview: (p: RestorePreview) => void; className?: string; compact?: boolean }) {
  const ws = useWorkspace()
  const [over, setOver] = useState(false)
  const [busy, setBusy] = useState(false)
  const input = useRef<HTMLInputElement>(null)

  async function take(file: File | undefined) {
    if (!file || !ws.machine) return
    if (file.size > maxUpload) {
      toastManager.add({ title: t('restore.tooBig'), type: 'error' })
      return
    }
    setBusy(true)
    try {
      onPreview(await uploadBackup(file, ws.machine.id, server))
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
      if (input.current) input.current.value = ''
    }
  }

  function drop(e: DragEvent) {
    e.preventDefault()
    setOver(false)
    void take(e.dataTransfer.files[0])
  }

  return (
    <div
      onDragOver={(e) => {
        e.preventDefault()
        setOver(true)
      }}
      onDragLeave={() => setOver(false)}
      onDrop={drop}
      className={cn('flex flex-col items-center justify-center rounded-2xl border border-dashed border-input bg-warm px-6 text-center transition-colors', compact ? 'py-6' : 'min-h-[150px] py-8', over && 'border-primary bg-selected', className)}
    >
      {busy ? (
        <>
          <Spinner className="size-5" />
          <p className="mt-3 text-[13px] font-medium">{t('world.uploading')}</p>
        </>
      ) : (
        <>
          <UploadIcon className="size-5 text-primary" aria-hidden="true" />
          <p className="mt-3 text-sm font-semibold">{t('world.drop')}</p>
          <p className="mt-1 text-xs text-muted-foreground">
            {rich('world.chooseFile', {
              choose: (chunk) => (
                <button type="button" className="font-semibold text-primary hover:underline" onClick={() => input.current?.click()}>
                  {chunk}
                </button>
              ),
            })}
          </p>
        </>
      )}
      <input ref={input} type="file" accept=".tar.gz,.tgz,application/gzip" className="sr-only" tabIndex={-1} aria-label={t('world.drop')} onChange={(e) => void take(e.target.files?.[0])} />
    </div>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex gap-4 border-b border-border py-2 text-[13px] last:border-b-0">
      <dt className="w-32 shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 flex-1">{children}</dd>
    </div>
  )
}

/** What a backup holds and what restoring it does, before anything changes. */
export function RestoreDialog({ preview, server, onClose }: { preview: RestorePreview | undefined; server?: ServerStatus; onClose: () => void }) {
  const ws = useWorkspace()
  const [phrase, setPhrase] = useState('')
  const [name, setName] = useState('')
  const [eula, setEula] = useState(false)
  const [busy, setBusy] = useState(false)
  const creating = !!preview && !preview.serverId
  const m = preview?.manifest

  async function discard() {
    if (preview && ws.machine) await del(machineApi(ws.machine.id, `/restore/${preview.id}`)).catch(() => undefined)
    setPhrase('')
    onClose()
  }

  async function apply() {
    if (!preview || !ws.machine) return
    setBusy(true)
    try {
      await post(machineApi(ws.machine.id, `/restore/${preview.id}/apply`), creating ? { confirm: preview.confirmPhrase, acceptEula: eula, name: name.trim() } : { confirm: phrase.trim() })
      toastManager.add({ title: creating ? t('restore.startedNew') : t('restore.started', { server: server?.name ?? '' }), type: 'success' })
      setPhrase('')
      onClose()
      await ws.refresh()
      if (creating) navigate({ name: 'home' })
    } catch (e) {
      toastManager.add({ title: errorText(e), type: 'error' })
    } finally {
      setBusy(false)
    }
  }

  const ready = preview?.compatible && (creating ? eula && name.trim().length > 0 : phrase.trim() === preview.confirmPhrase)
  return (
    <Dialog open={!!preview} onOpenChange={(open) => !open && void discard()}>
      <DialogPopup className="sm:max-w-[580px]">
        {preview && (
          <>
            <div className="flex items-start gap-4 px-6 pt-6 pb-2 max-sm:px-5">
              <Pip pose="box" size={52} />
              <div className="min-w-0 pt-1">
                <DialogTitle className="text-xl leading-7 font-bold">{preview.compatible ? (creating ? t('restore.newTitle') : t('restore.title')) : t('restore.cannot')}</DialogTitle>
                <DialogDescription className="mt-0.5 text-[13px]">{t('restore.checked', { count: m?.fileCount ?? 0 })}</DialogDescription>
              </div>
            </div>
            <DialogPanel className="pt-3 max-sm:px-5">
              {preview.problems.length > 0 && (
                <ul className="mb-4 flex flex-col gap-1 text-[13px] text-destructive-foreground" role="alert">
                  {preview.problems.map((p) => (
                    <li key={p}>{p}</li>
                  ))}
                </ul>
              )}
              {m && (
                <dl>
                  <Row label={t('restore.world')}>{m.levelName}</Row>
                  <Row label={t('restore.made')}>{formatDateTime(m.createdAt)}</Row>
                  <Row label={t('restore.software')}>{t('restore.softwareValue', { version: m.minecraftVersion, software: softwareName(m.type, m.build || m.paperBuild) })}</Row>
                  <Row label={t('restore.size')}>{t('restore.sizeValue', { archive: formatBytes(preview.sizeBytes), files: formatBytes(m.totalBytes) })}</Row>
                  <Row label={t('restore.memory')}>{formatMB(preview.memoryMB)}</Row>
                </dl>
              )}
              {preview.warnings.length > 0 && (
                <ul className="mt-3 flex flex-col gap-1 text-[13px] text-warning-foreground">
                  {preview.warnings.map((w) => (
                    <li key={w}>{w}</li>
                  ))}
                </ul>
              )}
              {preview.compatible && preview.steps.length > 0 && (
                <>
                  <h3 className="mt-4 text-[13px] font-semibold">{t('restore.steps')}</h3>
                  <ol className="mt-1.5 flex list-decimal flex-col gap-1 pl-5 text-[13px] text-muted-foreground">
                    {preview.steps.map((st) => (
                      <li key={st}>{st}</li>
                    ))}
                  </ol>
                </>
              )}
              {preview.notRestored.length > 0 && (
                <p className="mt-3 text-xs text-muted-foreground">
                  <span className="font-semibold text-foreground">{t('restore.notIncluded')}: </span>
                  {preview.notRestored.join(', ')}
                </p>
              )}
              {preview.compatible &&
                (creating ? (
                  <div className="mt-4 flex flex-col gap-3">
                    <label className="flex flex-col gap-1.5 text-[13px] font-medium">
                      {t('restore.nameLabel')}
                      <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={32} autoComplete="off" />
                    </label>
                    <label className="flex items-start gap-2.5 text-[13px]">
                      <Checkbox checked={eula} onCheckedChange={(v) => setEula(v === true)} className="mt-0.5" />
                      <span>
                        {rich('eula.accept', {
                          link: (chunk) => (
                            <a href={t('eula.url')} target="_blank" rel="noreferrer" className="font-medium text-primary underline underline-offset-2">
                              {chunk}
                            </a>
                          ),
                        })}
                      </span>
                    </label>
                  </div>
                ) : (
                  <label className="mt-4 flex flex-col gap-1.5 text-[13px]">
                    <span>
                      {rich(
                        'restore.typeToConfirm',
                        { b: (chunk) => <strong className="font-semibold">{chunk}</strong> },
                        { server: server?.name ?? '', level: preview.currentWorld.levelName ?? m?.levelName ?? '', size: formatBytes(preview.currentWorld.sizeBytes), phrase: preview.confirmPhrase },
                      )}
                    </span>
                    <Input value={phrase} onChange={(e) => setPhrase(e.target.value)} aria-label={t('restore.confirmLabel')} autoComplete="off" spellCheck={false} />
                  </label>
                ))}
            </DialogPanel>
            <DialogFooter variant="bare" className="border-t border-border pt-4">
              <Button variant="ghost" onClick={() => void discard()}>
                {t('common.cancel')}
              </Button>
              {preview.compatible && (
                <Button onClick={apply} loading={busy} disabled={!ready}>
                  {creating ? t('restore.restoreNew') : t('restore.replace')}
                </Button>
              )}
            </DialogFooter>
          </>
        )}
      </DialogPopup>
    </Dialog>
  )
}
