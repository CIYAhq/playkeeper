/** Markup that goes into a page as it is. */
export class Html {
  constructor(readonly value: string) {}
  toString() {
    return this.value
  }
}

const entities: Record<string, string> = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }

export function escape(text: string) {
  return text.replace(/[&<>"']/g, (c) => entities[c] ?? c)
}

function part(value: unknown): string {
  if (value instanceof Html) return value.value
  if (Array.isArray(value)) return value.map(part).join('')
  if (value === null || value === undefined || value === false) return ''
  return escape(String(value))
}

/**
 * A template of markup. What it puts in is escaped, apart from other
 * templates; lists are joined, and null, undefined and false leave nothing.
 */
export function html(strings: TemplateStringsArray, ...values: unknown[]) {
  let out = strings[0] ?? ''
  values.forEach((v, i) => {
    out += part(v) + (strings[i + 1] ?? '')
  })
  return new Html(out)
}
