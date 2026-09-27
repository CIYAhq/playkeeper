// The live demo's file browser: each server's folder (sample-files.ts), and
// what the dashboard's changes do to it, by the agent's rules
// (internal/agent/files.go): paths stay inside the server's folder, its world
// is read-only while the game runs, nothing changes while the server is busy,
// and each change is a line in the recent activity and a row in the audit log.
// Uploads send no bytes (upload.ts): a file arrives as its size, and a small
// one with its text, so it opens in the editor.

import { ApiError } from '@/api/client'
import type { FileContent, FileEntry, FileInfo, FileUpload, Files, ServerStatus } from '@/api/types'
import { baseName, inWorld, joinPath, parentOf } from '@/lib/files'
import { addonKind } from '@/lib/software'
import { demoUser, fakeSha, iso, library, serverOf, type DemoFile, type DemoState, type DemoUpload, type Request, type Routes } from './data'
import { managedKeys, sampleFiles, seedOf, type Tree } from './sample-files'
import { audit, busy, note } from './shared'
import { demoToast } from './toast'

const minute = 60_000
const maxEditBytes = 2 << 20
const maxBatch = 1000
const maxUploads = 8
const uploadIdle = 60 * minute
/** How far apart uploads into one folder may be to show as one line in the recent activity. */
const uploadsTogether = 15 * minute
const hintMove = 'Move it out of the way, then try again.'

/** The routes whose requests send text as their body: a file saved in the editor, and an uploaded file put in place again. */
export const textRoutes = new Set(['PUT /api/servers/:id/files/content', 'PUT /api/servers/:id/files/uploads/:up/files/:n'])

const runningPhases = new Set(['starting_container', 'downloading_server', 'starting', 'preparing_world', 'online', 'stopping'])
const running = (srv: ServerStatus) => runningPhases.has(srv.phase)

function worldsOf(srv: ServerStatus): string[] {
  const level = srv.config?.levelName ?? 'world'
  return [level, `${level}_nether`, `${level}_the_end`]
}

function treeOf(s: DemoState, srv: ServerStatus, now: number): Tree {
  return (s.files[srv.id] ??= sampleFiles(s, srv, now))
}

const bytesOf = (text: string) => new TextEncoder().encode(text).length
const quote = (p: string) => JSON.stringify(p.length > 120 ? `${p.slice(0, 60)}…${p.slice(-50)}` : p)
const where = (p: string) => (p ? `${p} in the server's files` : "The server's folder")
const invalid = (error: string) => new ApiError(400, { error, code: 'invalid_request' })
const conflict = (error: string, hint: string) => new ApiError(409, { error, code: 'conflict', hint })
// Control characters show wrongly in the dashboard, a terminal or a log.
// eslint-disable-next-line no-control-regex
const control = /[\u0000-\u001f\u007f-\u009f]/

/** A path the dashboard names, checked as the agent's filePath checks it: '' is the server's folder. */
function pathOf(raw: string | null | undefined, rootOK = false): string {
  const p = raw ?? ''
  if (p === '' || p === '.') {
    if (rootOK) return ''
    throw invalid("Name a file or folder in the server's files.")
  }
  if (p.length > 4096 || p.includes('\0') || p.split('/').some((x) => x === '' || x === '.' || x === '..')) throw invalid(`${quote(p)} is not a path inside the server's files.`)
  if (p.split('/').some((x) => bytesOf(x) > 255)) throw invalid(`${quote(p)} has a name longer than 255 bytes.`)
  return p
}

function newName(p: string) {
  if (control.test(baseName(p))) throw invalid(`${quote(p)} has a control character in its name.`)
}

/** Refuses a change at these paths while the server is busy, or in its world while the game runs. */
function refuse(srv: ServerStatus, paths: string[]) {
  if (srv.operation) throw busy(srv)
  const p = paths.find((x) => inWorld(x, worldsOf(srv)))
  if (p !== undefined && running(srv)) {
    throw new ApiError(409, { error: `${srv.name} is running, so its world is in use and ${quote(p)} can't change now.`, code: 'world_in_use', hint: 'Stop the server first: a world changed while the game writes it can break.', params: { path: p } })
  }
}

function gone(couldNot: string, p: string): ApiError {
  return new ApiError(404, { error: `${couldNot} ${quote(p)} isn't in the server's files anymore.`, code: 'not_found', hint: 'Reload the folder to see what is there now.', params: { path: p } })
}

function refusal(code: string, couldNot: string, p: string, msg: string, hint: string, params: Record<string, string> = {}): ApiError {
  return new ApiError(409, { error: `${couldNot} ${msg}`, code, hint, params: { path: p, ...params } })
}

const exists = (couldNot: string, p: string) => refusal('exists', couldNot, p, `${where(p)} already exists.`, 'Choose another name, or move or delete what is there first.')

/** Refuses a path with a file where a folder on its way should be, or a folder on its way missing. */
function checkWay(tree: Tree, p: string, couldNot: string) {
  const up = parentOf(p)
  if (up && !tree[up]) throw gone(couldNot, up)
  for (let at = up; at; at = parentOf(at)) {
    if (!tree[at]?.folder) throw refusal('not_a_folder', couldNot, at, `${where(at)} is not a folder.`, hintMove)
  }
}

const sizeOf = (f: DemoFile) => (f.text !== undefined ? bytesOf(f.text) : (f.size ?? 0))
const versionOf = (f: DemoFile) => fakeSha(seedOf(f.text ?? `${f.size ?? 0}:${f.modified}`))

function entryOf(p: string, f: DemoFile): FileEntry {
  return { name: baseName(p), type: f.folder ? 'folder' : 'file', size: f.folder ? 0 : sizeOf(f), modifiedAt: iso(f.modified) }
}

/** A path and everything in it. */
const within = (tree: Tree, p: string) => Object.keys(tree).filter((k) => k === p || k.startsWith(`${p}/`))

/** The folder p is in changes now, as adding or removing an entry changes it. */
function touch(tree: Tree, p: string, at: number) {
  const up = tree[parentOf(p)]
  if (up) up.modified = at
}

/** Many paths for an audit row: all of them, or as many as fit and how many more there are. */
function listed(paths: string[]): string {
  let out = ''
  for (const [i, p] of paths.entries()) {
    if (out.length > 300) return `${out} +${paths.length - i}`
    out += (i ? ', ' : '') + p
  }
  return out
}

/** Forgets the add-ons whose jars left the plugins or mods folder, so the Add-ons tab agrees with the Files tab. */
function forgetAddons(s: DemoState, srv: ServerStatus, removed: string[]) {
  const own = s.addons[srv.id]
  if (!own) return
  const folder = addonKind(srv.type) === 'plugins' ? 'plugins' : 'mods'
  const left = (fileName: string) => removed.some((p) => p === folder || p === `${folder}/${fileName}`)
  own.installed = own.installed.filter((inst) => {
    const a = library.find((x) => x.source === inst.source && x.projectId === inst.projectId)
    return !a || !left(a.jar.replace('{v}', inst.version))
  })
  own.byHand = own.byHand.filter((h) => !left(h.fileName))
}

function list(s: DemoState, r: Request): Files {
  const srv = serverOf(s, r)
  const p = pathOf(r.query.get('path'), true)
  const tree = treeOf(s, srv, r.now)
  const couldNot = 'The folder could not be opened.'
  const f = tree[p]
  if (p && !f) throw gone(couldNot, p)
  if (f && !f.folder) throw refusal('not_a_folder', couldNot, p, `${where(p)} is not a folder.`, hintMove)
  const prefix = p ? `${p}/` : ''
  const entries = Object.entries(tree)
    .filter(([k]) => k.startsWith(prefix) && k.length > prefix.length && !k.slice(prefix.length).includes('/'))
    .map(([k, x]) => entryOf(k, x))
  return { path: p, entries, running: running(srv), worlds: worldsOf(srv) }
}

function open(s: DemoState, r: Request): FileContent {
  const srv = serverOf(s, r)
  const p = pathOf(r.query.get('path'))
  const f = treeOf(s, srv, r.now)[p]
  const couldNot = 'The file could not be opened.'
  if (!f) throw gone(couldNot, p)
  if (f.folder) throw refusal('not_a_file', couldNot, p, `${where(p)} is a folder, not a file.`, hintMove)
  const size = sizeOf(f)
  if (size > maxEditBytes) throw new ApiError(409, { error: `${quote(p)} is larger than 2 MB, the most the editor opens.`, code: 'too_large', hint: 'Download it to open it on your computer.', params: { path: p, limit: String(maxEditBytes) } })
  const out: FileContent = { path: p, size, modifiedAt: iso(f.modified), version: versionOf(f), text: f.text ?? '', running: running(srv) }
  if (f.text === undefined) out.binary = true
  if (out.running && inWorld(p, worldsOf(srv))) out.readOnly = 'world_in_use'
  if (p === 'server.properties') out.managed = managedKeys(s, srv)
  return out
}

function fileChanged(p: string, deleted: boolean): ApiError {
  return new ApiError(409, {
    error: deleted ? `${quote(p)} was deleted since you opened it.` : `${quote(p)} changed since you opened it.`,
    code: 'file_changed',
    hint: 'Open it again to see what is there now, or save yours over it.',
    params: { path: p, gone: deleted },
  })
}

/** Saves the editor's text: over the version it opened, as a new file with expect=new, or over whatever is there. */
function save(s: DemoState, r: Request): FileInfo {
  const srv = serverOf(s, r)
  const p = pathOf(r.query.get('path'))
  const expect = r.query.get('expect') ?? ''
  if (expect === 'new') newName(p)
  const text = r.text ?? ''
  if (bytesOf(text) > maxEditBytes) throw new ApiError(413, { error: 'The file would be larger than 2 MB, the most the editor saves.', code: 'too_large', hint: 'Make it smaller, or upload it instead.', params: { path: p, limit: String(maxEditBytes) } })
  refuse(srv, [p])
  const tree = treeOf(s, srv, r.now)
  const couldNot = 'The file could not be saved.'
  const had = tree[p]
  if (expect === 'new' && had) throw new ApiError(409, { error: `${quote(p)} already exists.`, code: 'exists', hint: 'Choose another name.', params: { path: p } })
  if (had?.folder) throw refusal('not_a_file', couldNot, p, `${where(p)} is a folder, not a file.`, hintMove)
  if (expect && expect !== 'new' && !had) throw fileChanged(p, true)
  if (expect && expect !== 'new' && had && versionOf(had) !== expect) throw fileChanged(p, false)
  checkWay(tree, p, couldNot)
  const f: DemoFile = { text, modified: r.now }
  tree[p] = f
  touch(tree, p, r.now)
  const created = expect === 'new'
  audit(s, r.now, created ? 'files.created' : 'files.saved', srv, p, p)
  note(s, r.now, srv.id, created ? 'file_created' : 'file_saved', { actor: demoUser, detail: p })
  return { path: p, size: sizeOf(f), modifiedAt: iso(f.modified), version: versionOf(f) }
}

function makeFolder(s: DemoState, r: Request): FileEntry {
  const srv = serverOf(s, r)
  const p = pathOf((r.body as { path?: string } | undefined)?.path)
  newName(p)
  refuse(srv, [p])
  const tree = treeOf(s, srv, r.now)
  const couldNot = 'The folder could not be made.'
  if (tree[p]) throw exists(couldNot, p)
  for (let up = parentOf(p); up; up = parentOf(up)) {
    const f = tree[up]
    if (f && !f.folder) throw refusal('not_a_folder', couldNot, up, `${where(up)} is not a folder.`, hintMove)
    if (!f) tree[up] = { folder: true, modified: r.now }
  }
  const f: DemoFile = { folder: true, modified: r.now }
  tree[p] = f
  touch(tree, p, r.now)
  audit(s, r.now, 'files.folder_made', srv, p, p)
  note(s, r.now, srv.id, 'folder_made', { actor: demoUser, detail: p })
  return entryOf(p, f)
}

function move(s: DemoState, r: Request) {
  const srv = serverOf(s, r)
  const items = (r.body as { items?: { from?: string; to?: string }[] } | undefined)?.items ?? []
  if (items.length === 0 || items.length > maxBatch) throw invalid(`Move between 1 and ${maxBatch} files at once.`)
  const checked = items.map((it) => {
    const to = pathOf(it.to)
    newName(to)
    return { from: pathOf(it.from), to }
  })
  refuse(srv, checked.flatMap((it) => [it.from, it.to]))
  const tree = treeOf(s, srv, r.now)
  const moved: { from: string; to: string }[] = []
  let failed: unknown
  for (const it of checked) {
    const couldNot = `Could not move ${quote(baseName(it.from))}.`
    try {
      if (!tree[it.from]) throw gone(couldNot, it.from)
      if (it.to.startsWith(`${it.from}/`)) throw refusal('into_itself', couldNot, it.from, `${where(it.from)} can't be moved into itself.`, 'Choose a folder outside it.', { to: it.to })
      if (tree[it.to]) throw exists(couldNot, it.to)
      checkWay(tree, it.to, couldNot)
    } catch (e) {
      failed = e
      break
    }
    for (const k of within(tree, it.from)) {
      const f = tree[k]
      delete tree[k]
      if (f) tree[it.to + k.slice(it.from.length)] = f
    }
    touch(tree, it.from, r.now)
    touch(tree, it.to, r.now)
    moved.push(it)
  }
  const [first] = moved
  if (first && moved.length === 1) {
    const renamed = parentOf(first.from) === parentOf(first.to)
    const detail = `${first.from} → ${first.to}`
    audit(s, r.now, renamed ? 'files.renamed' : 'files.moved', srv, first.from, detail)
    note(s, r.now, srv.id, renamed ? 'file_renamed' : 'file_moved', { actor: demoUser, detail })
  } else if (first) {
    const detail = `${listed(moved.map((m) => m.from))} → ${parentOf(first.to) || '.'}`
    audit(s, r.now, 'files.moved', srv, parentOf(first.from) || '.', detail)
    note(s, r.now, srv.id, 'file_moved', { actor: demoUser, detail })
  }
  forgetAddons(s, srv, moved.map((m) => m.from))
  if (failed) throw failed
  return { moved: moved.length }
}

function remove(s: DemoState, r: Request) {
  const srv = serverOf(s, r)
  const raw = (r.body as { paths?: string[] } | undefined)?.paths ?? []
  if (raw.length === 0 || raw.length > maxBatch) throw invalid(`Delete between 1 and ${maxBatch} files at once.`)
  const paths = raw.map((p) => pathOf(p))
  refuse(srv, paths)
  const tree = treeOf(s, srv, r.now)
  for (const p of paths) {
    for (const k of within(tree, p)) delete tree[k]
    touch(tree, p, r.now)
  }
  const [first] = paths
  const detail = paths.length === 1 ? (first ?? '') : listed(paths)
  audit(s, r.now, 'files.deleted', srv, paths.length === 1 ? first : parentOf(first ?? '') || '.', detail)
  note(s, r.now, srv.id, 'file_deleted', { actor: demoUser, detail })
  forgetAddons(s, srv, paths)
  return { deleted: paths.length }
}

/** A download link was clicked: there are no bytes to send, so say so and count it as downloaded. */
function download(s: DemoState, r: Request) {
  const srv = serverOf(s, r)
  const raw = r.query.getAll('path')
  if (raw.length === 0 || raw.length > maxBatch) throw invalid(`Download between 1 and ${maxBatch} files at once.`)
  const paths = raw.map((p) => pathOf(p, true))
  const base = parentOf(paths[0] ?? '')
  if (paths.length > 1 && paths.includes('')) throw invalid("The server's folder is downloaded on its own.")
  if (paths.some((p) => parentOf(p) !== base)) throw invalid('Files downloaded together must be in the same folder.')
  const tree = treeOf(s, srv, r.now)
  const missing = paths.find((p) => p && !tree[p])
  if (missing !== undefined) throw gone('The download could not start.', missing)
  const shown = paths.map((p) => p || '.')
  audit(s, r.now, 'files.downloaded', srv, paths.length > 1 ? base || '.' : shown[0], listed(shown))
  demoToast('fileDownload')
  return {}
}

function uploadOf(s: DemoState, r: Request): DemoUpload {
  const up = s.fileUploads[r.params.up ?? '']
  if (!up || up.serverId !== r.params.id) throw new ApiError(404, { error: 'This upload isn’t here anymore.', code: 'not_found', hint: 'Choose the files again.' })
  return up
}

function openUpload(s: DemoState, r: Request): FileUpload {
  const srv = serverOf(s, r)
  const folder = pathOf((r.body as { folder?: string } | undefined)?.folder, true)
  for (const [id, up] of Object.entries(s.fileUploads)) if (r.now - Date.parse(up.view.createdAt) > uploadIdle) delete s.fileUploads[id]
  if (Object.keys(s.fileUploads).length >= maxUploads) throw conflict('Too many uploads are open on this machine.', 'Wait for one to finish, or cancel it, then try again.')
  const id = fakeSha(seedOf(`${srv.id}:${r.now}:${s.seq++}`)).slice(0, 16)
  const view: FileUpload = { id, folder, createdAt: iso(r.now), files: [], limitBytes: s.machine.live?.diskFreeBytes ?? 0 }
  s.fileUploads[id] = { serverId: srv.id, view, replace: [], text: [] }
  return view
}

/** Refuses to upload over something an upload may not replace: a folder, or a file it wasn't asked to. */
function freeFor(tree: Tree, dest: string, replace: boolean) {
  const f = tree[dest]
  if (!f) return
  if (f.folder) throw conflict(`${quote(dest)} is a folder, a link or a special file, so an upload can't replace it.`, 'Move it out of the way first.')
  if (!replace) throw new ApiError(409, { error: `${quote(dest)} already exists.`, code: 'exists', hint: 'Choose another name.', params: { path: dest } })
}

function announce(s: DemoState, r: Request): FileUpload {
  const srv = serverOf(s, r)
  const up = uploadOf(s, r)
  const b = (r.body ?? {}) as { name?: string; size?: number; replace?: boolean }
  const name = pathOf(b.name)
  if (control.test(name)) throw invalid(`${quote(name)} has a control character in its name.`)
  const size = Number(b.size)
  if (!Number.isFinite(size) || size < 0) throw invalid(`${quote(name)} has no size.`)
  const dest = pathOf(joinPath(up.view.folder, name))
  refuse(srv, [dest])
  freeFor(treeOf(s, srv, r.now), dest, !!b.replace)
  if (size > up.view.limitBytes) throw new ApiError(413, { error: `${quote(name)} is larger than the free disk space allows.`, code: 'insufficient_space', hint: 'Free disk space and try again.' })
  if (up.view.files.length >= maxBatch) throw conflict(`One upload takes at most ${maxBatch} files.`, 'Upload the rest in another one.')
  if (up.view.files.some((f) => f.name === name)) throw conflict(`${quote(name)} is in this upload already.`, '')
  const index = up.view.files.length
  up.view.files.push({ index, name, size, received: 0 })
  up.replace.push(!!b.replace)
  up.text.push(null)
  if (size === 0) place(s, srv, up, index, r.now)
  return up.view
}

/** What upload.ts says arrived of a file, and a small file's text; a file that is whole goes in place. */
function arrived(s: DemoState, r: Request): FileUpload {
  const srv = serverOf(s, r)
  const up = uploadOf(s, r)
  const n = Number(r.params.n)
  const f = up.view.files[n]
  if (!f) throw new ApiError(404, { error: 'That file isn’t part of the upload.', code: 'not_found' })
  const b = r.body as { received?: number; text?: string } | undefined
  if (b?.received !== undefined) {
    f.received = Math.min(f.size, Math.max(f.received, Number(b.received) || 0))
    if (typeof b.text === 'string') up.text[n] = b.text
  }
  if (f.received === f.size && !f.placed) place(s, srv, up, n, r.now)
  return up.view
}

/** Text as the editor takes it: no NUL, and nothing the browser couldn't read as UTF-8. */
const isText = (text: string) => !text.includes('\0') && !text.includes('\uFFFD')

/** Puts an uploaded file in place, or says in the file why it couldn't go there. */
function place(s: DemoState, srv: ServerStatus, up: DemoUpload, n: number, now: number) {
  const f = up.view.files[n]
  if (!f) return
  const dest = joinPath(up.view.folder, f.name)
  const tree = treeOf(s, srv, now)
  const couldNot = 'It could not be put in place.'
  try {
    refuse(srv, [dest])
    freeFor(tree, dest, up.replace[n] ?? false)
    for (let at = parentOf(dest); at; at = parentOf(at)) {
      const on = tree[at]
      if (on && !on.folder) throw refusal('not_a_folder', couldNot, at, `${where(at)} is not a folder.`, hintMove)
    }
  } catch (e) {
    f.error = e instanceof Error ? e.message : String(e)
    return
  }
  for (let at = parentOf(dest); at && !tree[at]; at = parentOf(at)) tree[at] = { folder: true, modified: now }
  const text = f.size === 0 ? '' : up.text[n]
  tree[dest] = text != null && isText(text) && bytesOf(text) === f.size ? { text, modified: now } : { size: f.size, modified: now }
  touch(tree, dest, now)
  f.placed = true
  delete f.error
  audit(s, now, 'files.uploaded', srv, dest, dest)
  // Uploads into one folder close together make one line, as the agent merges them.
  const last = s.activity[0]
  const folder = parentOf(dest) || '.'
  const lastFolder = last?.count ? last.detail : parentOf(last?.detail ?? '') || '.'
  if (last?.kind === 'file_uploaded' && last.serverId === srv.id && last.actor === demoUser && lastFolder === folder && now - Date.parse(last.ts) <= uploadsTogether) {
    Object.assign(last, { count: Math.max(last.count ?? 0, 1) + 1, detail: folder, ts: iso(now) })
    return
  }
  note(s, now, srv.id, 'file_uploaded', { actor: demoUser, detail: dest })
}

export const fileRoutes: Routes = {
  'GET /api/servers/:id/files': list,
  'GET /api/servers/:id/files/content': open,
  'PUT /api/servers/:id/files/content': save,
  'POST /api/servers/:id/files/folder': makeFolder,
  'POST /api/servers/:id/files/move': move,
  'POST /api/servers/:id/files/delete': remove,
  'GET /api/servers/:id/files/download': download,
  'POST /api/servers/:id/files/uploads': openUpload,
  'GET /api/servers/:id/files/uploads/:up': (s, r) => uploadOf(s, r).view,
  'DELETE /api/servers/:id/files/uploads/:up': (s, r) => {
    uploadOf(s, r)
    delete s.fileUploads[r.params.up ?? '']
    return undefined
  },
  'POST /api/servers/:id/files/uploads/:up/files': announce,
  'PUT /api/servers/:id/files/uploads/:up/files/:n': arrived,
}
