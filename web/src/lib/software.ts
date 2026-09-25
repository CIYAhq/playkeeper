import type { CatalogEntry, ServerConfig, SoftwareCheck, SoftwarePin } from '@/api/types'
import { formatLocale, t, type MessageKey } from '@/i18n'

/** The server types in the order the dashboard offers them. */
export const serverTypeIds = ['paper', 'vanilla', 'purpur', 'fabric', 'quilt', 'neoforge'] as const

/** What a server type can add: plugins, mods, or neither (data packs only). */
export type AddonKind = 'plugins' | 'mods' | 'none'

export function addonKind(type: string | undefined): AddonKind {
  switch (type || 'paper') {
    case 'paper':
    case 'purpur':
      return 'plugins'
    case 'fabric':
    case 'quilt':
    case 'neoforge':
      return 'mods'
    default:
      return 'none'
  }
}

/** Types with a build to pick besides the Minecraft version. */
export function hasBuilds(type: string): boolean {
  return type === 'purpur' || type === 'fabric' || type === 'quilt' || type === 'neoforge'
}

/** The server type a catalog entry installs. */
export function entryType(e: CatalogEntry): string {
  return e.software?.type ?? 'paper'
}

/** The build a pin names: a Purpur build, a loader, or a NeoForge version. */
export function pinBuild(p: SoftwarePin | undefined): string {
  if (!p) return ''
  if (p.purpurBuild) return String(p.purpurBuild)
  return p.fabricLoader ?? p.quiltLoader ?? p.neoforgeVersion ?? ''
}

/** A server's build as people say it: "build 41", "loader 0.17.2", or nothing for Vanilla. */
export function buildLabel(type: string, build: string | number): string {
  if (!build) return ''
  switch (type) {
    case 'paper':
    case 'purpur':
      return t('mcupdate.build', { build })
    case 'fabric':
    case 'quilt':
      return t('soft.loader', { build })
    case 'neoforge':
      return t('soft.neoforge', { build })
    default:
      return ''
  }
}

/** The build a server config runs, whichever type it is. */
export function configBuild(cfg: ServerConfig | undefined): string {
  if (!cfg) return ''
  return cfg.software ? pinBuild(cfg.software) : cfg.paperBuild ? String(cfg.paperBuild) : ''
}

/** A Minecraft release date as the version rows show it: "2 Sep 2026". */
export function formatReleased(iso: string): string {
  return new Date(iso).toLocaleDateString(formatLocale(), { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' })
}

/** A checksum shortened to groups of four at both ends: "9f3c 1a0e … 7b2d a17e". */
export function shortHash(h: string): string {
  if (h.length <= 16) return h.replace(/(.{4})(?=.)/g, '$1 ')
  return t('soft.hash', { start: `${h.slice(0, 4)} ${h.slice(4, 8)}`, end: `${h.slice(-8, -4)} ${h.slice(-4)}` })
}

const checkKeys: Record<SoftwareCheck, { label: MessageKey; short: MessageKey; className: string; shortClassName?: string }> = {
  full: { label: 'types.check.full', short: 'types.check.full', className: 'text-success-foreground' },
  weak_hash: { label: 'types.check.weak', short: 'types.check.weak', className: 'text-warning-foreground' },
  recorded_outputs: { label: 'types.check.recorded', short: 'types.check.recordedShort', className: 'text-foreground', shortClassName: 'text-muted-foreground' },
}

/** How a type's download is checked: the marker's text and colour. */
export function checkMarker(check: SoftwareCheck | undefined, short = false): { label: string; className: string } {
  const k = checkKeys[check ?? 'full']
  return { label: t(short ? k.short : k.label), className: (short && k.shortClassName) || k.className }
}

interface TypeText {
  /** The card's last line: what the server runs. */
  runs: MessageKey
  /** The comparison's "Good for". */
  goodFor: MessageKey
  /** The comparison's add-ons column. */
  addons: MessageKey
  /** The comparison's add-ons column on a phone. */
  addonsShort: MessageKey
  /** How the download is checked, in one line. */
  check: MessageKey
}

const typeText: Record<string, TypeText> = {
  paper: { runs: 'types.runs.plugins', goodFor: 'types.goodFor.paper', addons: 'types.runs.plugins', addonsShort: 'types.runs.plugins', check: 'types.checkLine.paper' },
  vanilla: { runs: 'types.runs.vanilla', goodFor: 'types.goodFor.vanilla', addons: 'types.addons.vanilla', addonsShort: 'types.addons.vanilla', check: 'types.checkLine.vanilla' },
  purpur: { runs: 'types.runs.plugins', goodFor: 'types.goodFor.purpur', addons: 'types.runs.plugins', addonsShort: 'types.runs.plugins', check: 'types.checkLine.purpur' },
  fabric: { runs: 'types.runs.mods', goodFor: 'types.goodFor.fabric', addons: 'types.runs.mods', addonsShort: 'types.addons.mods', check: 'types.checkLine.fabric' },
  quilt: { runs: 'types.runs.quilt', goodFor: 'types.goodFor.quilt', addons: 'types.runs.quilt', addonsShort: 'types.addons.quilt', check: 'types.checkLine.quilt' },
  neoforge: { runs: 'types.runs.mods', goodFor: 'types.goodFor.neoforge', addons: 'types.runs.mods', addonsShort: 'types.addons.mods', check: 'types.checkLine.neoforge' },
}

export function typeTexts(type: string): TypeText | undefined {
  return typeText[type]
}
