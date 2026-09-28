import type { FileEntry } from '@/api/types'
import type { MessageKey } from '@/i18n'

// The file browser's paths are slash-separated and relative to the server's
// folder, as the agent names them: '' is the server's folder itself.

/** The folder a path is in, '' for the server's folder. */
export function parentOf(path: string): string {
  const i = path.lastIndexOf('/')
  return i < 0 ? '' : path.slice(0, i)
}

/** The last name in a path. */
export function baseName(path: string): string {
  return path.slice(path.lastIndexOf('/') + 1)
}

/** name in folder. */
export function joinPath(folder: string, name: string): string {
  return folder ? `${folder}/${name}` : name
}

/** Each folder on the way to path and path itself, for the breadcrumb. */
export function crumbs(path: string): { name: string; path: string }[] {
  const out: { name: string; path: string }[] = []
  let at = ''
  for (const name of path.split('/').filter(Boolean)) {
    at = joinPath(at, name)
    out.push({ name, path: at })
  }
  return out
}

/** Whether path is a world folder or in one. */
export function inWorld(path: string, worlds: string[]): boolean {
  return worlds.some((w) => path === w || path.startsWith(`${w}/`))
}

/** Whether a folder is, or is inside, the folder moving: it can't move there. */
export function inside(folder: string, moving: string): boolean {
  return folder === moving || folder.startsWith(`${moving}/`)
}

/** The lists the server keeps in memory while it runs and writes over. */
const playerLists = new Set(['whitelist.json', 'ops.json', 'banned-players.json', 'banned-ips.json'])

export function isPlayerList(path: string): boolean {
  return playerLists.has(path)
}

export type FileKind = 'folder' | 'text' | 'archive' | 'image' | 'binary' | 'link' | 'special'

const archiveExtensions = new Set(['jar', 'zip', 'gz', 'tgz', 'tar', 'xz', 'zst', 'bz2', '7z', 'rar', 'mrpack'])
const imageExtensions = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'ico', 'bmp'])
const binaryExtensions = new Set([
  'mca', 'mcr', 'mcc', 'dat', 'dat_old', 'nbt', 'schem', 'schematic', 'litematic', 'class', 'so', 'dll', 'dylib', 'exe', 'bin', 'db',
  'sqlite', 'sqlite3', 'lock', 'ogg', 'mp3', 'wav', 'ttf', 'otf', 'woff', 'woff2', 'pdf', 'sig', 'jfr', 'hprof',
])

function extension(name: string): string {
  const i = name.lastIndexOf('.')
  return i <= 0 ? '' : name.slice(i + 1).toLowerCase()
}

/** What an entry is, for its icon and what opening it does: a text file opens in the editor, anything else is only downloaded. */
export function fileKind(e: Pick<FileEntry, 'name' | 'type'>): FileKind {
  if (e.type !== 'file') return e.type
  const ext = extension(e.name)
  if (archiveExtensions.has(ext)) return 'archive'
  if (imageExtensions.has(ext)) return 'image'
  if (binaryExtensions.has(ext)) return 'binary'
  return 'text'
}

/** Whether the editor opens a file of this name without asking the machine first. Unknown kinds are text until the machine says otherwise. */
export function opensAsText(e: Pick<FileEntry, 'name' | 'type'>): boolean {
  return fileKind(e) === 'text'
}

/** The editor's language for a file, by its name. */
export type Language = 'properties' | 'yaml' | 'json' | 'json5' | 'toml' | 'ini' | 'shell' | 'javascript' | 'plain'

export function languageOf(name: string): Language {
  const ext = extension(name)
  switch (ext) {
    case 'properties':
    case 'lang':
      return 'properties'
    case 'yml':
    case 'yaml':
    case 'yamlconfig':
      return 'yaml'
    case 'json':
    case 'mcmeta':
      return 'json'
    case 'json5':
    case 'jsonc':
      return 'json5'
    case 'toml':
      return 'toml'
    case 'cfg':
    case 'conf':
    case 'config':
    case 'ini':
    case 'hocon':
      return 'ini'
    case 'sh':
    case 'bash':
      return 'shell'
    case 'js':
    case 'mjs':
    case 'cjs':
    case 'ts':
    case 'zs':
      return 'javascript'
  }
  return 'plain'
}

export const languageNames: Record<Language, MessageKey> = {
  properties: 'files.lang.properties',
  yaml: 'files.lang.yaml',
  json: 'files.lang.json',
  json5: 'files.lang.json5',
  toml: 'files.lang.toml',
  ini: 'files.lang.ini',
  shell: 'files.lang.shell',
  javascript: 'files.lang.javascript',
  plain: 'files.lang.plain',
}

export type SortKey = 'name' | 'size' | 'modified'

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })

/** Folders first, then by the column chosen, names as people read them: numbers in order, case ignored. */
export function sortEntries(list: FileEntry[], key: SortKey = 'name', descending = false): FileEntry[] {
  const dir = descending ? -1 : 1
  return [...list].sort((a, b) => {
    const folders = Number(b.type === 'folder') - Number(a.type === 'folder')
    if (folders) return folders
    let by = 0
    if (key === 'size') by = a.size - b.size
    else if (key === 'modified') by = Date.parse(a.modifiedAt) - Date.parse(b.modifiedAt)
    return (by || collator.compare(a.name, b.name)) * dir
  })
}

/** Why a name can't be given to a new file or folder, or undefined when it can. */
export function nameProblem(name: string, taken: string[] = []): MessageKey | undefined {
  if (!name.trim()) return 'files.name.empty'
  if (name.includes('/') || name.includes('\\')) return 'files.name.slash'
  if (name === '.' || name === '..') return 'files.name.dots'
  // Control characters show wrongly in the dashboard, a terminal or a log.
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(name)) return 'files.name.control'
  if (new TextEncoder().encode(name).length > 255) return 'files.name.long'
  if (taken.includes(name)) return 'files.name.taken'
  return undefined
}

/** A file picked or dropped, with its path in the folder it goes into. */
export interface Picked {
  file: File
  path: string
}

/** The files a file input picked: a folder picked keeps its folders. */
export function pickedFiles(list: FileList | File[]): Picked[] {
  return Array.from(list).map((file) => ({ file, path: file.webkitRelativePath || file.name }))
}

/** The top-level names of what's picked: a file's name, or the folder a dropped folder's files are in. */
export function topNames(picked: Picked[]): string[] {
  return [...new Set(picked.map((p) => p.path.split('/')[0] ?? p.path))]
}

/** Every file in a dropped entry, a folder's files too, with their paths from the drop. */
async function walkEntry(entry: FileSystemEntry, into: Picked[], at: string): Promise<void> {
  const path = at ? `${at}/${entry.name}` : entry.name
  if (entry.isFile) {
    const file = await new Promise<File>((ok, fail) => (entry as FileSystemFileEntry).file(ok, fail))
    into.push({ file, path })
    return
  }
  if (!entry.isDirectory) return
  const reader = (entry as FileSystemDirectoryEntry).createReader()
  for (;;) {
    // A reader hands out a folder's entries a hundred or so at a time.
    const batch = await new Promise<FileSystemEntry[]>((ok, fail) => reader.readEntries(ok, fail))
    if (batch.length === 0) break
    for (const e of batch) await walkEntry(e, into, path)
  }
}

/** The files a drop brings, with the folders they are in when folders were dropped. */
export async function droppedFiles(data: DataTransfer): Promise<Picked[]> {
  const entries = Array.from(data.items ?? [])
    .map((item) => (item.kind === 'file' ? item.webkitGetAsEntry() : null))
    .filter((e): e is FileSystemEntry => !!e)
  if (entries.length === 0) return pickedFiles(data.files)
  const out: Picked[] = []
  for (const e of entries) await walkEntry(e, out, '')
  return out
}
