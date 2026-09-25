import type { AddonNotice, TemplateContents, TemplateSettings } from '@/api/types'
import { t, type MessageKey } from '@/i18n'
import { addonKind } from '@/lib/software'

const settingKeys: [keyof TemplateSettings, MessageKey][] = [
  ['difficulty', 'template.setting.difficulty'],
  ['pvp', 'template.setting.pvp'],
  ['viewDistance', 'template.setting.viewDistance'],
  ['motd', 'template.setting.motd'],
  ['maxPlayers', 'template.setting.maxPlayers'],
  ['gameMode', 'template.setting.gameMode'],
  ['hardcore', 'template.setting.hardcore'],
  ['levelType', 'template.setting.levelType'],
  ['playStyle', 'template.setting.playStyle'],
  ['memoryMB', 'template.setting.memory'],
]

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}

/** "Difficulty, PvP, view distance, server list message": the settings a template carries, four at most. */
export function settingNames(s: TemplateSettings): string {
  const names = settingKeys.filter(([k]) => s[k] !== undefined && s[k] !== '').map(([, key]) => t(key))
  if (names.length === 0) return ''
  const shown = names.slice(0, 4).join(', ')
  return capitalize(names.length > 4 ? t('template.setting.more', { names: shown, count: names.length - 4 }) : shown)
}

/** "Normal difficulty · friends can't hurt each other · view distance 10 · up to 10 players". */
export function settingsSummary(s: TemplateSettings): string {
  const parts: string[] = []
  if (s.difficulty) parts.push(t('template.summary.difficulty', { difficulty: t(`settings.difficulty.${s.difficulty}` as MessageKey) }))
  if (s.hardcore) parts.push(t('template.summary.hardcore'))
  if (s.pvp !== undefined) parts.push(t(s.pvp ? 'template.summary.pvpOn' : 'template.summary.pvpOff'))
  if (s.viewDistance) parts.push(t('template.summary.viewDistance', { count: s.viewDistance }))
  if (s.maxPlayers) parts.push(t('template.summary.maxPlayers', { count: s.maxPlayers }))
  return capitalize(parts.join(t('common.dot')))
}

/** "5 plugins", or "Cobblemon and 3 more mods" for a modpack server. */
export function addonsLine(c: TemplateContents): string {
  const mods = addonKind(c.type) === 'mods'
  const count = c.addons.length
  if (c.modpack) return count ? t('template.addons.pack', { pack: c.modpack.name, count }) : c.modpack.name
  return t(mods ? 'template.addons.mods' : 'template.addons.plugins', { count })
}

/** "Faithful 32x and 3 data packs". */
export function packsLine(c: TemplateContents): string {
  const resource = c.resourcePacks > 0 ? c.packs[0] : undefined
  if (resource && c.dataPacks > 0) return t('template.packs.both', { name: resource, count: c.dataPacks })
  if (resource) return resource
  return t('template.packs.data', { count: c.dataPacks })
}

/** Whether every add-on keeps the version the server ran. */
export function pinned(c: TemplateContents): boolean {
  return c.addons.every((a) => !!a.versionNumber) && (!c.modpack || !!c.modpack.versionNumber)
}

const leftOutAddonKinds = new Set(['left_out_addon_upload', 'left_out_addon_missing', 'left_out_addon_invalid', 'left_out_modpack'])

/** The names of the add-ons an export left out. */
export function leftOutAddons(notices: AddonNotice[]): string[] {
  return notices.filter((n) => leftOutAddonKinds.has(n.kind) && n.params?.name).map((n) => n.params?.name ?? '')
}

/** A template link's data from the create page's address ("#template=…"). */
export function templateFromHash(hash: string): string | undefined {
  const m = /^#template=(.+)$/.exec(hash)
  return m?.[1]
}

/** The sign-in page's address; a shared template's data stays after #, which browsers never send. */
export function signInPath(location: { hash: string }): string {
  return templateFromHash(location.hash) ? `/login${location.hash}` : '/login'
}

/** Where signing in continues: the create page when a shared template was opened. */
export function afterSignIn(location: { hash: string }): string {
  return templateFromHash(location.hash) ? `/servers/new${location.hash}` : '/'
}

/** The size of the template file as downloaded, in bytes. */
export function fileSize(text: string): number {
  return new TextEncoder().encode(text).length
}
