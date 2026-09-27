import { api, get, post } from './client'
import type { FileContent, FileDeleteResult, FileInfo, Files } from './types'
import { serverApi } from './workspace'

// Each server's file browser, through the panel to the machine that runs it.

const filesApi = (serverId: string, rest = '') => serverApi(serverId, `/files${rest}`)

/** The most paths one move or delete sends; a larger selection goes in turns. */
const batch = 200

export function listFiles(serverId: string, path: string): Promise<Files> {
  return get<Files>(filesApi(serverId, `?path=${encodeURIComponent(path)}`))
}

export function openFile(serverId: string, path: string): Promise<FileContent> {
  return get<FileContent>(filesApi(serverId, `/content?path=${encodeURIComponent(path)}`))
}

/**
 * Saves text over a file. expect is the version it was opened with, so a
 * file that changed since is refused; 'new' makes a file that must not exist
 * yet; '' saves over whatever is there.
 */
export function saveFile(serverId: string, path: string, text: string, expect: string): Promise<FileInfo> {
  const q = `?path=${encodeURIComponent(path)}${expect ? `&expect=${encodeURIComponent(expect)}` : ''}`
  return api<FileInfo>('PUT', filesApi(serverId, `/content${q}`), undefined, new Blob([text], { type: 'text/plain; charset=utf-8' }))
}

export function makeFolder(serverId: string, path: string): Promise<unknown> {
  return post(filesApi(serverId, '/folder'), { path })
}

export async function moveFiles(serverId: string, items: { from: string; to: string }[]): Promise<void> {
  for (let i = 0; i < items.length; i += batch) await post(filesApi(serverId, '/move'), { items: items.slice(i, i + batch) })
}

/** Deletes files and folders. continuing says the machine carries on after answering, as it does with a folder of very many files. */
export async function deleteFiles(serverId: string, paths: string[]): Promise<{ continuing: boolean }> {
  let continuing = false
  for (let i = 0; i < paths.length; i += batch) {
    const done = await post<FileDeleteResult | undefined>(filesApi(serverId, '/delete'), { paths: paths.slice(i, i + batch) })
    if (done?.continuing) continuing = true
  }
  return { continuing }
}

/** A link that downloads a file, or a zip of folders or of several files in one folder. */
export function downloadHref(serverId: string, paths: string[]): string {
  return filesApi(serverId, `/download?${paths.map((p) => `path=${encodeURIComponent(p)}`).join('&')}`)
}

/** Asks whether a download would start, or why not, such as a zip of too many files: a link can't show why it failed. */
export function checkDownload(serverId: string, paths: string[]): Promise<unknown> {
  return get(`${downloadHref(serverId, paths)}&check=1`)
}

/** Where the server's uploads open. */
export const uploadsApi = (serverId: string) => filesApi(serverId, '/uploads')
