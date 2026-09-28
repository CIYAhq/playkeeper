import { FileArchiveIcon, FileIcon, FileImageIcon, FileQuestionIcon, FileTextIcon, FolderIcon, Link2Icon } from 'lucide-react'
import type { FileEntry } from '@/api/types'
import { fileKind } from '@/lib/files'
import { cn } from '@/lib/utils'

/** An entry's icon, by what it is. */
export function EntryIcon({ entry, className }: { entry: Pick<FileEntry, 'name' | 'type'>; className?: string }) {
  const cls = cn('size-[18px] shrink-0', className)
  const kind = fileKind(entry)
  switch (kind) {
    case 'folder':
      return <FolderIcon className={cn(cls, 'fill-marigold/35 text-warning-strong')} aria-hidden="true" />
    case 'text':
      return <FileTextIcon className={cn(cls, 'text-muted-foreground')} aria-hidden="true" />
    case 'archive':
      return <FileArchiveIcon className={cn(cls, 'text-muted-foreground')} aria-hidden="true" />
    case 'image':
      return <FileImageIcon className={cn(cls, 'text-muted-foreground')} aria-hidden="true" />
    case 'binary':
      return <FileIcon className={cn(cls, 'text-muted-foreground')} aria-hidden="true" />
    case 'link':
      return <Link2Icon className={cn(cls, 'text-warning-foreground')} aria-hidden="true" />
    case 'special':
      return <FileQuestionIcon className={cn(cls, 'text-warning-foreground')} aria-hidden="true" />
    default: {
      const unreachable: never = kind
      return unreachable
    }
  }
}
