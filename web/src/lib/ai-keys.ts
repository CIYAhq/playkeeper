import type { AIKeys, AIProvider } from '@/api/types'
import { t, type MessageKey } from '@/i18n'

/** An AI provider whose key a server's plugins can build with. */
export interface AIProviderInfo {
  /** The provider's name, as its site writes it. */
  name: string
  /** What every one of its keys starts with. */
  prefix: string
  /** Where its owner makes a key. */
  keysUrl: string
}

/** The providers a server keeps a key for, as aiProviders in internal/agent/aikeys.go has them. */
export const aiProviders: Record<AIProvider, AIProviderInfo> = {
  openrouter: { name: 'OpenRouter', prefix: 'sk-or-', keysUrl: 'https://openrouter.ai/keys' },
}

/** The provider AI Build Battle asks for. */
export const buildBattleProvider: AIProvider = 'openrouter'

/** How many characters a key has, at least and at most, as the agent counts them. */
export const aiKeyLength = { min: 20, max: 256 }

/** Why the agent refuses a key (the reason of its answer); each one has a message here. */
export type AIKeyReason = 'ai_key_missing' | 'ai_key_prefix' | 'ai_key_characters' | 'ai_key_short' | 'ai_key_long'

const reasonMessages: Record<AIKeyReason, MessageKey> = {
  ai_key_missing: 'aiKey.missing',
  ai_key_prefix: 'aiKey.prefix',
  ai_key_characters: 'aiKey.characters',
  ai_key_short: 'aiKey.short',
  ai_key_long: 'aiKey.long',
}

export function isAIKeyReason(reason: string | undefined): reason is AIKeyReason {
  return reason !== undefined && Object.hasOwn(reasonMessages, reason)
}

/** Why key can't be one of the provider's, checked in the agent's order, or undefined when it can. */
export function aiKeyProblem(provider: AIProvider, key: string): AIKeyReason | undefined {
  const k = key.trim()
  if (k === '') return 'ai_key_missing'
  if (!k.startsWith(aiProviders[provider].prefix)) return 'ai_key_prefix'
  if (/[^!-~]/.test(k)) return 'ai_key_characters'
  if (k.length < aiKeyLength.min) return 'ai_key_short'
  if (k.length > aiKeyLength.max) return 'ai_key_long'
  return undefined
}

/** What to tell the owner about a refused key. No message quotes the key. */
export function aiKeyMessage(provider: AIProvider, reason: AIKeyReason): string {
  const p = aiProviders[provider]
  return t(reasonMessages[reason], { provider: p.name, prefix: p.prefix, site: p.keysUrl.replace(/^https:\/\//, '') })
}

/** Whether k is AI Build Battle, the plugin that ships in Playkeeper and builds with the owner's key. */
export function isAIBuildBattle(k: { source: string; projectId: string } | undefined): boolean {
  return k?.source === 'playkeeper' && k.projectId === 'ai-build-battle'
}

/** Whether the server has a key saved for any provider. */
export function hasAIKey(keys: AIKeys): boolean {
  return Object.values(keys.keys).some((k) => k.set)
}
