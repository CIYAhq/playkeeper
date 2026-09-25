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
