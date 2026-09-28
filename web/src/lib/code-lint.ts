import { yamlLanguage } from '@codemirror/lang-yaml'
import type { MessageKey } from '@/i18n'
import type { Language } from './files'

// Problems the editor finds before a file is saved: JSON that doesn't parse,
// YAML that doesn't read or is indented with tabs, which a server or plugin
// would fail to load. Only in the editor's chunk: it brings the YAML parser.

export interface Problem {
  /** The offset in the text where the problem is, and where it ends. */
  from: number
  to: number
  message: MessageKey
}

/** The offset of line (1-based) and column (1-based) in text. */
function offsetOf(text: string, line: number, column: number): number {
  let at = 0
  for (let l = 1; l < line; l++) {
    const next = text.indexOf('\n', at)
    if (next < 0) return text.length
    at = next + 1
  }
  return Math.min(text.length, at + Math.max(0, column - 1))
}

/** Where a JSON.parse error says the problem is, as browsers word it. */
export function jsonErrorOffset(text: string, message: string): number {
  const pos = /position (\d+)/.exec(message)
  if (pos) return Math.min(text.length, Number(pos[1]))
  const lc = /line (\d+) column (\d+)/.exec(message)
  if (lc) return offsetOf(text, Number(lc[1]), Number(lc[2]))
  return text.trimEnd().length
}

function jsonProblems(text: string): Problem[] {
  if (!text.trim()) return []
  try {
    JSON.parse(text)
    return []
  } catch (e) {
    const at = jsonErrorOffset(text, e instanceof Error ? e.message : '')
    return [{ from: at, to: Math.min(text.length, at + 1), message: 'files.editor.jsonProblem' }]
  }
}

function yamlProblems(text: string): Problem[] {
  const out: Problem[] = []
  const tab = /^[ ]*\t/m.exec(text)
  if (tab) out.push({ from: tab.index, to: tab.index + tab[0].length, message: 'files.editor.yamlTab' })
  const tree = yamlLanguage.parser.parse(text)
  tree.iterate({
    enter: (n) => {
      if (!n.type.isError || out.length > 0) return
      out.push({ from: n.from, to: Math.max(n.to, Math.min(text.length, n.from + 1)), message: 'files.editor.yamlProblem' })
    },
  })
  return out
}

/** The problems in text, for a file of this language; none for the kinds that aren't checked. */
export function problemsIn(text: string, language: Language): Problem[] {
  switch (language) {
    case 'json':
      return jsonProblems(text)
    case 'yaml':
      return yamlProblems(text)
    case 'properties':
    case 'json5':
    case 'toml':
    case 'ini':
    case 'shell':
    case 'javascript':
    case 'plain':
      return []
    default: {
      const unreachable: never = language
      return unreachable
    }
  }
}

/** The line (1-based) an offset is on. */
export function lineOf(text: string, offset: number): number {
  let line = 1
  for (let i = 0; i < offset && i < text.length; i++) if (text.charCodeAt(i) === 10) line++
  return line
}

/** The lines of server.properties that set a key Playkeeper sets at each start: each key's offset and its length. */
export function managedLines(text: string, managed: string[]): { from: number; to: number; key: string }[] {
  if (managed.length === 0) return []
  const keys = new Set(managed)
  const out: { from: number; to: number; key: string }[] = []
  let at = 0
  for (const line of text.split('\n')) {
    const m = /^\s*([^#!\s=:][^=:\s]*)\s*[=:]/.exec(line)
    if (m?.[1] && keys.has(m[1])) {
      const from = at + line.indexOf(m[1])
      out.push({ from, to: from + m[1].length, key: m[1] })
    }
    at += line.length + 1
  }
  return out
}
