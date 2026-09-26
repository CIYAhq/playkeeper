import mark from '@/assets/brand/playkeeper-mark.svg'
import fabric from '@/assets/logos/fabric-icon.png'
import forge from '@/assets/logos/forge-apple-touch-icon.png'
import neoforge from '@/assets/logos/neoforged-logo.svg'
import paper from '@/assets/logos/papermc_logo.min.svg'
import purpur from '@/assets/logos/purpur.svg'
import quilt from '@/assets/logos/quilt_logo_dark.svg'
import pipBox from '@/assets/pip/pip-box.svg'
import pipCheer from '@/assets/pip/pip-cheer.svg'
import pipHardhat from '@/assets/pip/pip-hardhat.svg'
import pipHurt from '@/assets/pip/pip-hurt.svg'
import pipLetter from '@/assets/pip/pip-letter.svg'
import pipSearch from '@/assets/pip/pip-search.svg'
import pipSleep from '@/assets/pip/pip-sleep.svg'
import pipWave from '@/assets/pip/pip-wave.svg'
import emptyBackups from '@/assets/pixel-art/empty-no-backups.svg'
import emptyPlayers from '@/assets/pixel-art/empty-no-players.svg'
import playCreative from '@/assets/pixel-art/play-creative.svg'
import playHardcore from '@/assets/pixel-art/play-hardcore.svg'
import playJustMe from '@/assets/pixel-art/play-just-me.svg'
import playFriends from '@/assets/pixel-art/play-with-friends.svg'
import emblemStopped from '@/assets/pixel-art/server-emblem-stopped.svg'
import emblem from '@/assets/pixel-art/server-emblem.svg'
import vanilla from '@/assets/pixel-art/type-vanilla.svg'
import worldAmplified from '@/assets/pixel-art/world-amplified.svg'
import worldBigBiomes from '@/assets/pixel-art/world-big-biomes.svg'
import worldFlat from '@/assets/pixel-art/world-flat.svg'
import worldNormal from '@/assets/pixel-art/world-normal.svg'
import type { LevelType, PlayStyle } from '@/api/types'
import { t } from '@/i18n'
import { cn } from '@/lib/utils'

// Pip, the mascot, appears only in empty states, celebrations, onboarding
// and update or job dialogs. All art here is original to Playkeeper.
const pips = { wave: pipWave, cheer: pipCheer, box: pipBox, letter: pipLetter, hardhat: pipHardhat, sleep: pipSleep, hurt: pipHurt, search: pipSearch }
export type PipPose = keyof typeof pips

export function Pip({ pose, size = 64, className }: { pose: PipPose; size?: number; className?: string }) {
  return <img src={pips[pose]} width={size} height={size} alt="" className={cn('shrink-0 select-none', className)} draggable={false} />
}

export function BrandMark({ size = 28, className }: { size?: number; className?: string }) {
  return <img src={mark} width={size} height={size} alt="" className={cn('shrink-0', className)} />
}

const playArt: Record<PlayStyle, string> = { friends: playFriends, creative: playCreative, hardcore: playHardcore, solo: playJustMe }
const worldArt: Record<LevelType, string> = { normal: worldNormal, flat: worldFlat, amplified: worldAmplified, large_biomes: worldBigBiomes }

/** Pixel art is drawn on a small grid and always shown at a whole-number scale. */
function Pixel({ src, width, height, className }: { src: string; width: number; height: number; className?: string }) {
  return <img src={src} width={width} height={height} alt="" className={cn('pixelated shrink-0 select-none', className)} draggable={false} />
}

export function PlayArt({ style, scale = 8, className }: { style: PlayStyle; scale?: number; className?: string }) {
  return <Pixel src={playArt[style]} width={32 * scale} height={20 * scale} className={className} />
}

export function WorldArt({ type, scale = 2, className }: { type: LevelType; scale?: number; className?: string }) {
  return <Pixel src={worldArt[type]} width={32 * scale} height={20 * scale} className={className} />
}

export function EmptyArt({ kind, scale = 8, className }: { kind: 'backups' | 'players'; scale?: number; className?: string }) {
  return <Pixel src={kind === 'backups' ? emptyBackups : emptyPlayers} width={40 * scale} height={24 * scale} className={className} />
}

/** The server emblem; grey while the server is stopped. A custom server icon replaces it. */
export function Emblem({ size = 44, stopped, icon, name, className }: { size?: number; stopped?: boolean; icon?: string; name: string; className?: string }) {
  const scale = Math.max(1, Math.round(size / 12))
  const px = scale * 12
  return (
    <span className={cn('inline-flex shrink-0 overflow-hidden rounded-[22%] border border-black/10', className)} style={{ width: px, height: px }}>
      {icon ? (
        <img src={icon} width={px} height={px} alt={t('server.emblem', { server: name })} className="pixelated" />
      ) : (
        <img src={stopped ? emblemStopped : emblem} width={px} height={px} alt={t('server.emblem', { server: name })} className="pixelated" />
      )}
    </span>
  )
}

/** Minecraft: Java Edition, as a game to pick: the grass block in the logo tile. */
export function GameIcon({ size = 40, className }: { size?: number; className?: string }) {
  const scale = Math.max(1, Math.floor((size * 0.64) / 12))
  return (
    <span className={cn('inline-flex shrink-0 items-center justify-center overflow-hidden border border-border bg-white', className)} style={{ width: size, height: size, borderRadius: Math.round(size * 0.26) }}>
      <img src={emblem} width={12 * scale} height={12 * scale} alt="" className="pixelated" draggable={false} />
    </span>
  )
}

const logos: Record<string, { src: string; pixel?: boolean }> = {
  paper: { src: paper },
  vanilla: { src: vanilla, pixel: true },
  purpur: { src: purpur },
  fabric: { src: fabric, pixel: true },
  quilt: { src: quilt },
  neoforge: { src: neoforge },
  forge: { src: forge },
}

/** A server software logo in the shared white tile. Logo files are never altered. */
export function TypeLogo({ type, size = 40, className }: { type: string; size?: number; className?: string }) {
  const logo = logos[type]
  const pad = Math.round(size * 0.18)
  return (
    <span
      className={cn('inline-flex shrink-0 items-center justify-center border border-border bg-white', className)}
      style={{ width: size, height: size, borderRadius: Math.round(size * 0.26), padding: pad }}
    >
      {logo && <img src={logo.src} alt="" width={size - 2 * pad} height={size - 2 * pad} className={cn('h-full w-full object-contain', logo.pixel && 'pixelated')} draggable={false} />}
    </span>
  )
}
