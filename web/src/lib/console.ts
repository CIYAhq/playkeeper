export type LineKind = 'chat' | 'players' | 'problem' | 'info'

export interface ParsedLine {
  /** "18:41:07", from the server's own timestamp when the line has one. */
  time?: string
  level?: string
  text: string
  kind: LineKind
}

const reServer = /^\[(\d{2}:\d{2}:\d{2})(?: [^\]]*?)? ?(INFO|WARN|WARNING|ERROR|FATAL|DEBUG)\]:? ?(.*)$/
const reChat = /^(?:\[Not Secure\] )?<([A-Za-z0-9_]{1,16})> /
const rePlayers = /^[A-Za-z0-9_]{1,16} (joined|left) the game$|^[A-Za-z0-9_]{1,16} lost connection: |^Disconnecting [A-Za-z0-9_]{1,16}/

/** Splits a Minecraft server log line into its time, level and message, and says what kind it is. */
export function parseLine(raw: string): ParsedLine {
  const m = reServer.exec(raw)
  const time = m?.[1]
  const level = m?.[2] === 'WARNING' ? 'WARN' : m?.[2]
  const text = m ? (m[3] ?? '') : raw
  let kind: LineKind = 'info'
  if (level === 'WARN' || level === 'ERROR' || level === 'FATAL' || /Exception|OutOfMemoryError/.test(text)) kind = 'problem'
  else if (reChat.test(text)) kind = 'chat'
  else if (rePlayers.test(text)) kind = 'players'
  return { time, level, text, kind }
}

/** Did the server run out of memory, going by how it stopped? */
export function ranOutOfMemory(exitCode: number | undefined, lines: string[]): boolean {
  return exitCode === 137 || lines.some((l) => /OutOfMemoryError|out of memory/i.test(l))
}

/** "Can't keep up! Is the server overloaded? Running 5210ms or 104 ticks behind" → 5.2 seconds. */
export function behindSeconds(text: string): number | undefined {
  const m = /Running (\d+)ms or \d+ ticks behind/.exec(text)
  return m ? Number(m[1]) / 1000 : undefined
}

/** How close to the end of the log still counts as the bottom, in CSS pixels (subpixel scrolling never lands exactly). */
export const bottomSlack = 8

/** Is a scroll area scrolled to its end? */
export function isAtBottom(el: { scrollTop: number; scrollHeight: number; clientHeight: number }, slack = bottomSlack): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight <= slack
}

/**
 * Whether the log follows new lines after it scrolls: yes at the bottom, no
 * once the reader scrolls up, unchanged while a jump to the latest line is
 * still on its way down.
 */
export function followAfterScroll(following: boolean, atBottom: boolean, jumping: boolean): boolean {
  if (atBottom) return true
  return jumping ? following : false
}

/**
 * The lines kept after a poll: the new ones appended, or instead of the old
 * ones when the log restarted, at most `keep`. Nothing new keeps the same
 * array, so the page doesn't render again.
 */
export function keepTail<T>(kept: T[], incoming: T[], restarted: boolean, keep: number): T[] {
  if (!restarted && incoming.length === 0) return kept
  const all = restarted ? incoming : kept.concat(incoming)
  return all.length > keep ? all.slice(all.length - keep) : all
}

/** Merges two lists that are each in time order, `a` first when times are equal. */
export function mergeByTime<T extends { ts: string }>(a: T[], b: T[]): T[] {
  if (b.length === 0) return a
  if (a.length === 0) return b
  const out: T[] = []
  let j = 0
  for (const x of a) {
    const at = Date.parse(x.ts)
    while (j < b.length && Date.parse((b[j] as T).ts) < at) out.push(b[j++] as T)
    out.push(x)
  }
  while (j < b.length) out.push(b[j++] as T)
  return out
}
