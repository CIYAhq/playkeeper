import type { CatalogEntry, ServerConfig, ServerStatus } from '@/api/types'
import { t } from '@/i18n'
import { preset } from '@/lib/styles'
import { compareMinecraft } from '@/lib/versions'

// Product names stay as their projects write them, in every language.
const typeNames: Record<string, string> = {
  paper: 'Paper',
  vanilla: 'Vanilla',
  purpur: 'Purpur',
  fabric: 'Fabric',
  quilt: 'Quilt',
  neoforge: 'NeoForge',
}

export function typeName(id: string | undefined): string {
  return typeNames[id || 'paper'] ?? id ?? ''
}

/** "Paper 26.1.2". */
export function softwareLabel(s: ServerStatus): string {
  const v = s.config?.minecraftVersion
  return v ? `${typeName(s.type)} ${v}` : typeName(s.type)
}

/** "Survival with friends", or nothing for servers made before play styles. */
export function styleTitle(cfg: ServerConfig | undefined): string | undefined {
  const p = preset(cfg?.playStyle)
  return p ? t(p.title) : undefined
}

/** The newest stable Minecraft version newer than the server's, if there is one. */
export function newerStable(cfg: ServerConfig | undefined, versions: CatalogEntry[] | undefined): CatalogEntry | undefined {
  if (!cfg || !versions) return undefined
  return versions.filter((v) => !v.experimental && v.supported && compareMinecraft(v.minecraftVersion, cfg.minecraftVersion) > 0).sort((a, b) => compareMinecraft(b.minecraftVersion, a.minecraftVersion) || b.paperBuild - a.paperBuild)[0]
}

/** The server's own icon, when one was uploaded. */
export function iconURL(s: ServerStatus): string | undefined {
  return s.config?.iconUpdatedAt ? `/api/servers/${s.id}/icon?v=${encodeURIComponent(s.config.iconUpdatedAt)}` : undefined
}

export function playersOnline(servers: ServerStatus[] | undefined): number {
  return (servers ?? []).reduce((n, s) => n + (s.phase === 'online' ? (s.players?.online ?? 0) : 0), 0)
}
