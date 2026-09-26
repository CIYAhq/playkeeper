import type { FriendsShare, PackPage, PackPageMod, ShareText, ShareYourself } from '@/api/types'
import { t, type MessageKey } from '@/i18n'

// The texts of internal/modpacks/share that en.ts words. A key missing here
// shows the share's own English.
const shareKeys: readonly MessageKey[] = [
  'share.need.required',
  'share.need.optional',
  'share.need.server_only',
  'share.need.unknown',
  'share.notice.pack',
  'share.notice.pack_one',
  'share.notice.pack_two',
  'share.notice.pack_more',
  'share.notice.one',
  'share.notice.two',
  'share.notice.more',
  'share.notice.optional',
  'share.notice.none',
  'share.step.download',
  'share.step.open_link',
  'share.step.import',
  'share.step.play',
  'share.step.play_join',
  'share.launcher.modrinth_app.add',
  'share.launcher.modrinth_app.pick',
  'share.launcher.modrinth_app.play',
  'share.launcher.prism.add',
  'share.launcher.prism.pick',
  'share.launcher.prism.launch',
  'share.yourself.curseforge',
  'share.yourself.inside_pack',
  'share.yourself.other_host',
  'share.yourself.not_found',
]

/** A text from the share in the current language. */
export function shareText(text: ShareText): string {
  const key = shareKeys.find((k) => k === text.key)
  return key ? t(key, text.params) : text.text
}

/** Why friends get a mod themselves: the file can't link it. */
export type YourselfWhy = 'curseforge' | 'inside_pack' | 'other_host' | 'not_found'
const whys: readonly YourselfWhy[] = ['curseforge', 'inside_pack', 'other_host', 'not_found']

export function yourselfWhy(y: ShareYourself): YourselfWhy | undefined {
  return whys.find((w) => y.reason.key === `share.yourself.${w}`)
}

/** Why friends get a mod themselves: with where it goes on the page, in a few words in the share dialog. */
export function yourselfReason(y: ShareYourself, where: 'page' | 'dialog'): string {
  const why = yourselfWhy(y)
  if (!why) return shareText(y.reason)
  const folder = y.reason.params?.folder || 'mods'
  const page = where === 'page'
  switch (why) {
    case 'curseforge':
      return page ? t('packPage.why.curseforge', { folder }) : t('packShare.why.curseforge')
    case 'inside_pack':
      return page ? t('packPage.why.inside_pack', { folder }) : t('packShare.why.inside_pack')
    case 'other_host':
      return page ? t('packPage.why.other_host', { folder }) : t('packShare.why.other_host')
    case 'not_found':
      return page ? t('packPage.why.not_found', { folder }) : t('packShare.why.not_found')
    default: {
      const unreachable: never = why
      return unreachable
    }
  }
}

/** The name of the link to where friends get a mod themselves. */
export function yourselfLink(y: ShareYourself): string {
  const why = yourselfWhy(y) ?? 'not_found'
  switch (why) {
    case 'curseforge':
      return t('packs.link.curseforge')
    case 'inside_pack':
      return t('packs.link.inside_pack')
    case 'other_host':
      return t('packs.link.other_host')
    case 'not_found':
      return t('packs.link.not_found')
    default: {
      const unreachable: never = why
      return unreachable
    }
  }
}

/** What the friends' file holds, from the share's notice: "The pack, Waystones and Fabric in one file". */
export function fileHolds(share: FriendsShare, loader: string): string {
  const p = share.notice.params ?? {}
  const mod = p.mod ?? ''
  const other = p.other ?? ''
  const count = Number(p.count ?? 0)
  switch (share.notice.key) {
    case 'share.notice.pack':
      return t('packShare.file.pack', { loader })
    case 'share.notice.pack_one':
      return t('packShare.file.packOne', { mod, loader })
    case 'share.notice.pack_two':
      return t('packShare.file.packTwo', { mod, other, loader })
    case 'share.notice.pack_more':
      return t('packShare.file.packMore', { mod, count, loader })
    case 'share.notice.one':
      return t('packShare.file.one', { mod, loader })
    case 'share.notice.two':
      return t('packShare.file.two', { mod, other, loader })
    case 'share.notice.more':
      return t('packShare.file.more', { mod, count, loader })
    case 'share.notice.optional':
      return t('packShare.file.optional', { loader })
    default:
      return t('packShare.file.none', { loader })
  }
}

const needRank = (m: PackPageMod) => (m.need === 'required' ? 0 : m.need === 'unknown' ? 1 : 2)

/**
 * What the public page lists under "What friends get": the pack as one row
 * with its number of mods, then the mods the owner added that come in the
 * file, those friends need first and each dependency after the mod it's for.
 */
export function pageRows(page: PackPage): { packMods: number; mods: PackPageMod[] } {
  const mine = page.mods.filter((m) => m.from === 'user' && m.inFile)
  const byName = (a: PackPageMod, b: PackPageMod) => needRank(a) - needRank(b) || a.name.localeCompare(b.name)
  const isChild = (m: PackPageMod) => !!m.neededBy && mine.some((o) => o.name === m.neededBy && o !== m)
  const mods: PackPageMod[] = []
  for (const parent of mine.filter((m) => !isChild(m)).sort(byName)) {
    mods.push(parent, ...mine.filter((m) => isChild(m) && m.neededBy === parent.name).sort(byName))
  }
  return { packMods: page.mods.filter((m) => m.from === 'pack').length, mods }
}

/** A launcher's site as people read it: "modrinth.com/app". */
export function siteLabel(site: string): string {
  try {
    const u = new URL(site)
    return (u.host + u.pathname).replace(/\/$/, '')
  } catch {
    return site
  }
}
