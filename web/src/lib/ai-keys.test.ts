import { describe, expect, it } from 'vitest'
import { aiKeyMessage, aiKeyProblem, aiProviders, hasAIKey, isAIBuildBattle, isAIKeyReason, type AIKeyReason } from './ai-keys'

const key = (length: number) => 'sk-or-v1-' + 'a'.repeat(length - 'sk-or-v1-'.length)

describe('aiKeyProblem', () => {
  it.each([
    { name: 'nothing', key: '', reason: 'ai_key_missing' },
    { name: 'only spaces', key: ' \n\t ', reason: 'ai_key_missing' },
    { name: 'another provider’s key', key: 'sk-proj-' + 'a'.repeat(40), reason: 'ai_key_prefix' },
    { name: 'the prefix in capitals', key: 'SK-OR-V1-' + 'a'.repeat(40), reason: 'ai_key_prefix' },
    { name: 'a key without its prefix that has a space too', key: 'abc def ghi jkl mno pqr', reason: 'ai_key_prefix' },
    { name: 'a space inside', key: 'sk-or-v1-abcdef ghijklmnopq', reason: 'ai_key_characters' },
    { name: 'a letter outside ASCII', key: 'sk-or-v1-abcdéfghijklmnopq', reason: 'ai_key_characters' },
    { name: 'a control character', key: 'sk-or-v1-abcdef\u0000ghijklmnopq', reason: 'ai_key_characters' },
    { name: 'a key cut short', key: key(19), reason: 'ai_key_short' },
    { name: 'two keys run together', key: key(257), reason: 'ai_key_long' },
  ])('refuses $name as the agent does', ({ key, reason }) => {
    expect(aiKeyProblem('openrouter', key)).toBe(reason)
  })

  it('takes a key of 20 to 256 characters, without the spaces and newline pasted around it', () => {
    expect(aiKeyProblem('openrouter', key(20))).toBeUndefined()
    expect(aiKeyProblem('openrouter', key(256))).toBeUndefined()
    expect(aiKeyProblem('openrouter', `  ${key(73)}\n`)).toBeUndefined()
  })
})

describe('aiKeyMessage', () => {
  it.each([
    ['ai_key_missing', 'Paste your OpenRouter key.'],
    ['ai_key_prefix', 'That isn’t an OpenRouter key: those start with sk-or-.'],
    ['ai_key_characters', 'Keys have no spaces or characters like that. Copy it again from openrouter.ai/keys.'],
    ['ai_key_short', 'That key is too short. Copy all of it from openrouter.ai/keys.'],
    ['ai_key_long', 'That key is too long. Copy only the key from openrouter.ai/keys.'],
  ] as [AIKeyReason, string][])('says what the agent says for %s', (reason, message) => {
    expect(aiKeyMessage('openrouter', reason)).toBe(message)
  })
})

describe('isAIKeyReason', () => {
  it('knows the agent’s reasons and nothing else, not even what every object has', () => {
    for (const reason of ['ai_key_missing', 'ai_key_prefix', 'ai_key_characters', 'ai_key_short', 'ai_key_long']) expect(isAIKeyReason(reason)).toBe(true)
    for (const reason of [undefined, '', 'ai_key_other', 'toString', 'constructor', '__proto__', 'hasOwnProperty']) expect(isAIKeyReason(reason)).toBe(false)
  })
})

describe('isAIBuildBattle', () => {
  it('is the plugin that ships in Playkeeper, by source and project', () => {
    expect(isAIBuildBattle({ source: 'playkeeper', projectId: 'ai-build-battle' })).toBe(true)
    expect(isAIBuildBattle({ source: 'modrinth', projectId: 'ai-build-battle' })).toBe(false)
    expect(isAIBuildBattle({ source: 'playkeeper', projectId: 'something-else' })).toBe(false)
    expect(isAIBuildBattle(undefined)).toBe(false)
  })
})

describe('hasAIKey', () => {
  it('is whether any provider has a key saved', () => {
    expect(hasAIKey({ keys: { openrouter: { set: false } }, pending: false, available: true })).toBe(false)
    expect(hasAIKey({ keys: { openrouter: { set: true } }, pending: true, available: true })).toBe(true)
  })
})

describe('aiProviders', () => {
  it('sends the owner to each provider’s own page for keys, over https', () => {
    for (const p of Object.values(aiProviders)) expect(new URL(p.keysUrl).protocol).toBe('https:')
    expect(aiProviders.openrouter.keysUrl).toBe('https://openrouter.ai/keys')
  })
})
