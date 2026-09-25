import { Fragment, type ReactNode } from 'react'
import { interpolate, message, type MessageKey, type Vars } from '.'

type Tags = Record<string, (chunk: string) => ReactNode>

/**
 * Translates a message whose text marks parts with tags, like
 * "I accept the <link>Minecraft EULA</link>", rendering each part with the
 * matching function. Tags do not nest.
 */
export function rich(key: MessageKey, tags: Tags, vars?: Vars): ReactNode {
  const text = interpolate(message(key, vars), vars)
  const out: ReactNode[] = []
  const re = /<(\w+)>(.*?)<\/\1>/g
  let last = 0
  let m: RegExpExecArray | null
  while ((m = re.exec(text))) {
    if (m.index > last) out.push(text.slice(last, m.index))
    const render = tags[m[1] ?? '']
    out.push(<Fragment key={m.index}>{render ? render(m[2] ?? '') : m[2]}</Fragment>)
    last = m.index + m[0].length
  }
  if (last < text.length) out.push(text.slice(last))
  return <>{out}</>
}
