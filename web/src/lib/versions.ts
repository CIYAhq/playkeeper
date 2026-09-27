import type { CatalogEntry, ServerConfig } from '../api/types'
import { configBuild, entryType } from './software'

/** Compares Minecraft release versions ("26.2", "1.21.11") part by part. */
export function compareMinecraft(a: string, b: string): number {
  const pa = a.split('.').map(Number)
  const pb = b.split('.').map(Number)
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const d = (pa[i] ?? 0) - (pb[i] ?? 0)
    if (d !== 0) return Math.sign(d)
  }
  return 0
}

/** The versions a server can move to: a newer Minecraft version, or a newer build of its own. */
export function upgradeTargets(cfg: ServerConfig, versions: CatalogEntry[]): CatalogEntry[] {
  const type = cfg.type || 'paper'
  return versions.filter((v) => {
    if (entryType(v) !== type) return false
    const c = compareMinecraft(v.minecraftVersion, cfg.minecraftVersion)
    if (c !== 0) return c > 0
    if (type === 'paper') return v.paperBuild > cfg.paperBuild
    return !!v.build && compareMinecraft(v.build, configBuild(cfg)) > 0
  })
}
