import { en } from './en'

/** A message is text, or text per plural form (Intl.PluralRules categories). */
export type Plural = { zero?: string; one?: string; two?: string; few?: string; many?: string; other: string }
export type Message = string | Plural
export type MessageKey = keyof typeof en
export type Vars = Record<string, string | number>

export type Locale = 'en'

const catalogs: Record<Locale, Partial<Record<MessageKey, Message>>> = { en }

let locale: Locale = 'en'

export function getLocale(): Locale {
  return locale
}

/** Switches the language. Callers re-render from the root afterwards. */
export function setLocale(next: Locale) {
  locale = next
}

const pluralRules = new Map<string, Intl.PluralRules>()

function pluralForm(count: number): keyof Plural {
  let rules = pluralRules.get(locale)
  if (!rules) {
    rules = new Intl.PluralRules(locale)
    pluralRules.set(locale, rules)
  }
  return rules.select(count)
}

/** The text of a message in the current language (English when missing). */
export function message(key: MessageKey, vars?: Vars): string {
  const msg: Message = catalogs[locale][key] ?? en[key]
  if (typeof msg === 'string') return msg
  const count = Number(vars?.count ?? 0)
  // A zero form is for exactly none, even in languages whose rules have no "zero".
  if (count === 0 && msg.zero !== undefined) return msg.zero
  return msg[pluralForm(count)] ?? msg.other
}

// Ports and build numbers are identifiers, written without digit grouping.
const identifiers = new Set(['port', 'build'])

export function interpolate(text: string, vars?: Vars): string {
  if (!vars) return text
  return text.replace(/\{(\w+)\}/g, (whole, name: string) => {
    const v = vars[name]
    if (v === undefined) return whole
    if (typeof v !== 'number' || identifiers.has(name)) return String(v)
    return new Intl.NumberFormat(locale).format(v)
  })
}

/** Translates a key, filling {placeholders} from vars; vars.count picks the plural form. */
export function t(key: MessageKey, vars?: Vars): string {
  return interpolate(message(key, vars), vars)
}
