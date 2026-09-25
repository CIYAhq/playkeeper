import { useRef, useState, type DragEvent, type ReactNode } from 'react'
import { ChevronLeftIcon, UploadIcon } from 'lucide-react'
import type { ServerStatus } from '@/api/types'
import { Spinner } from '@/components/app/bits'
import { useIsPhone } from '@/components/app/controls'
import { t } from '@/i18n'
import { linkProps } from '@/lib/router'
import { cn } from '@/lib/utils'

/**
 * The top of a page under the World tab: a "‹ World" link above the content
 * on desktop, where the tab stays active, and a pushed header with the
 * page's title on phones.
 */
export function WorldSubHeader({ server, title }: { server: ServerStatus; title: string }) {
  const phone = useIsPhone()
  const back = linkProps({ name: 'server', slug: server.slug, tab: 'world' })
  if (phone) {
    return (
      <header className="grid grid-cols-[1fr_auto_1fr] items-center pt-2 pb-1">
        <a {...back} className="-ml-2 inline-flex min-h-11 items-center gap-0.5 justify-self-start rounded-lg px-1 text-[17px] text-success-strong transition-opacity active:opacity-60">
          <ChevronLeftIcon className="size-5" aria-hidden="true" />
          {t('tab.world')}
        </a>
        <h1 className="text-[17px] font-semibold">{title}</h1>
      </header>
    )
  }
  return (
    <a {...back} className="-mb-1 inline-flex items-center gap-1 self-start rounded-md text-[13px] font-semibold text-success-strong outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
      <ChevronLeftIcon className="size-4" aria-hidden="true" />
      {t('tab.world')}
    </a>
  )
}

/** Packs can be at most this big: players' games refuse larger resource packs. */
export const maxPackBytes = 262_144_000

/** A hidden file input for .zip files, and a function that opens it. */
export function useZipPicker(onFile: (file: File) => void) {
  const ref = useRef<HTMLInputElement>(null)
  const input = (
    <input
      ref={ref}
      type="file"
      accept=".zip,application/zip"
      className="sr-only"
      tabIndex={-1}
      aria-hidden="true"
      onChange={(e) => {
        const file = e.target.files?.[0]
        e.target.value = ''
        if (file) onFile(file)
      }}
    />
  )
  return { open: () => ref.current?.click(), input }
}

/** A dashed area that takes a dropped .zip, or opens the file chooser when clicked. */
export function ZipDropZone({ label, onFile, busy, disabled, tall, className }: { label: ReactNode; onFile: (file: File) => void; busy?: boolean; disabled?: boolean; tall?: boolean; className?: string }) {
  const [over, setOver] = useState(false)
  const picker = useZipPicker(onFile)
  const off = disabled || busy
  function drop(e: DragEvent) {
    e.preventDefault()
    setOver(false)
    const file = e.dataTransfer.files[0]
    if (file && !off) onFile(file)
  }
  return (
    <div
      onDragOver={(e) => {
        e.preventDefault()
        if (!off) setOver(true)
      }}
      onDragLeave={() => setOver(false)}
      onDrop={drop}
      className={className}
    >
      <button
        type="button"
        onClick={picker.open}
        disabled={off}
        className={cn(
          'flex h-full w-full items-center justify-center gap-2 rounded-2xl border border-dashed border-input bg-warm px-6 text-center text-[13px] font-medium outline-none transition-[border-color,background-color,scale] duration-200 not-disabled:hover:border-primary/50 not-disabled:active:scale-[0.99] focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-default disabled:text-muted-foreground',
          tall ? 'min-h-[88px] flex-col gap-3 py-6' : 'py-5',
          over && 'border-primary bg-selected',
        )}
      >
        {busy ? <Spinner className="size-4" /> : <UploadIcon className={cn('size-4 shrink-0', off ? 'text-muted-foreground' : 'text-primary')} aria-hidden="true" />}
        <span>{busy ? t('world.uploading') : label}</span>
      </button>
      {picker.input}
    </div>
  )
}

/**
 * The keys that joined a list after it first loaded, so only those rows
 * animate in; the first data shows as it is.
 */
export function useArrivals(keys: string[] | undefined): ReadonlySet<string> {
  const [seen, setSeen] = useState<{ sig?: string; known: ReadonlySet<string>; fresh: ReadonlySet<string> }>({ known: new Set(), fresh: new Set() })
  const sig = keys?.join('\n')
  if (keys && sig !== seen.sig) {
    const fresh = seen.sig === undefined ? new Set<string>() : new Set(keys.filter((k) => !seen.known.has(k)))
    setSeen({ sig, known: new Set(keys), fresh })
  }
  return seen.fresh
}

/** Pins a page's main action above the phone's tabs; the spacer keeps the content clear of it. */
export function PhoneActionBar({ children }: { children: ReactNode }) {
  return (
    <>
      <div className="h-20 shrink-0" aria-hidden="true" />
      <div className="fixed inset-x-0 bottom-[calc(53px+env(safe-area-inset-bottom))] z-30 flex gap-2 bg-sidebar/95 px-4 pt-2 pb-3 backdrop-blur animate-in fade-in-0 slide-in-from-bottom-2 duration-300">{children}</div>
    </>
  )
}
