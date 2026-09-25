import { useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode } from 'react'
import { CheckIcon, ChevronsUpDownIcon, MinusIcon, PlusIcon } from 'lucide-react'
import type { MapPlayer, MapWorld } from '@/api/types'
import { Select, SelectGroup, SelectGroupLabel, SelectItem, SelectPopup, SelectSeparator, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Sheet, SheetPopup, SheetTitle } from '@/components/ui/sheet'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { t } from '@/i18n'
import { addedWorldsLabel, clampZoom, formatCoords, maxZoom, ownWorld, pixelsPerBlock, screenToWorld, visibleTiles, worldLabel, worldToScreen, zoomAround, type MapViewState } from '@/lib/map'
import { cn } from '@/lib/utils'

// Undrawn land: a flat two-tone checker, from the design spec.
const checkerA = '#E9EAE3'
const checkerB = '#E3E4DC'
const checkerSize = 48
const maxTiles = 300
const maxFetches = 6

interface Tile {
  url: string
  state: 'loading' | 'ok' | 'missing' | 'failed'
  bitmap?: ImageBitmap
  fetchedAt: number
  usedAt: number
  refreshing: boolean
}

/**
 * Tiles by URL. A tile is fetched once, then revalidated (its ETag makes
 * that a 304 when nothing changed) once it is older than the world's
 * refresh interval. Undrawn tiles answer 404 and stay "missing".
 */
class TileStore {
  private tiles = new Map<string, Tile>()
  private queue: Tile[] = []
  private active = 0
  private closed = false

  constructor(private onChange: () => void) {}

  get(url: string, refreshMs: number, now: number): Tile {
    let tile = this.tiles.get(url)
    if (!tile) {
      tile = { url, state: 'loading', fetchedAt: 0, usedAt: now, refreshing: false }
      this.tiles.set(url, tile)
      this.enqueue(tile)
      this.evict()
    } else {
      tile.usedAt = now
      const retry = tile.state === 'failed' ? 10_000 : refreshMs
      if (!tile.refreshing && tile.state !== 'loading' && now - tile.fetchedAt > retry) this.enqueue(tile)
    }
    return tile
  }

  /** A tile that is already loaded, without asking for it. */
  peek(url: string): Tile | undefined {
    return this.tiles.get(url)
  }

  close() {
    this.closed = true
    for (const tile of this.tiles.values()) tile.bitmap?.close()
    this.tiles.clear()
    this.queue = []
  }

  private enqueue(tile: Tile) {
    tile.refreshing = true
    this.queue.push(tile)
    this.pump()
  }

  private pump() {
    while (this.active < maxFetches && this.queue.length > 0) {
      const tile = this.queue.shift()
      if (!tile) break
      this.active++
      void this.load(tile).finally(() => {
        this.active--
        this.pump()
      })
    }
  }

  private async load(tile: Tile) {
    const first = tile.fetchedAt === 0
    try {
      const res = await fetch(tile.url, { credentials: 'same-origin', cache: first ? 'default' : 'no-cache' })
      if (this.closed) return
      if (res.status === 404) {
        tile.bitmap?.close()
        tile.bitmap = undefined
        tile.state = 'missing'
      } else if (res.ok && (res.headers.get('Content-Type') ?? '').startsWith('image/png')) {
        const bitmap = await createImageBitmap(await res.blob())
        if (this.closed) {
          bitmap.close()
          return
        }
        tile.bitmap?.close()
        tile.bitmap = bitmap
        tile.state = 'ok'
      } else if (tile.state === 'loading') {
        tile.state = 'failed'
      }
    } catch {
      if (tile.state === 'loading') tile.state = 'failed'
    }
    tile.fetchedAt = Date.now()
    tile.refreshing = false
    if (!this.closed) this.onChange()
  }

  private evict() {
    if (this.tiles.size <= maxTiles) return
    const oldest = [...this.tiles.values()].filter((x) => !x.refreshing).sort((a, b) => a.usedAt - b.usedAt)
    for (const tile of oldest.slice(0, this.tiles.size - maxTiles)) {
      tile.bitmap?.close()
      this.tiles.delete(tile.url)
    }
  }
}

/**
 * The block the coordinates readout shows. It lives outside React state so
 * that moving the map redraws the readout, not the page around it.
 */
export class MapCoords {
  private value = { x: 0, z: 0 }
  private listeners = new Set<() => void>()

  get = () => this.value

  set(x: number, z: number) {
    const next = { x: Math.round(x), z: Math.round(z) }
    if (next.x === this.value.x && next.z === this.value.z) return
    this.value = next
    for (const l of this.listeners) l()
  }

  subscribe = (l: () => void) => {
    this.listeners.add(l)
    return () => {
      this.listeners.delete(l)
    }
  }
}

/** "x 212 · z −148": the block under the pointer, or at the centre of the map. */
export function CoordsReadout({ coords, className }: { coords: MapCoords; className?: string }) {
  const c = useSyncExternalStore(coords.subscribe, coords.get)
  return <span className={cn('tabular-nums', className)}>{formatCoords(c.x, c.z)}</span>
}

function reducedMotion(): boolean {
  return typeof window !== 'undefined' && !!window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
}

export interface MapFocus {
  x: number
  z: number
  /** Bumped for every jump, so the same place can be jumped to twice. */
  seq: number
}

export interface MapViewProps {
  world: MapWorld
  tileSize: number
  tileURL: (world: string, zoom: number, x: number, z: number) => string
  /** Players to show; only those in this world are drawn. */
  players?: MapPlayer[]
  faceURL?: (p: MapPlayer) => string
  focus?: MapFocus
  coords?: MapCoords
  zoomButtons?: boolean
  caption?: ReactNode
  className?: string
}

type Gesture = { kind: 'pan' | 'pinch'; start: MapViewState; x: number; y: number; dist: number; moved: boolean }

/**
 * Playkeeper's own map viewer: squaremap's tiles on a canvas, with faces on
 * top. Drag to move; wheel, pinch, double-click or +/- to zoom; arrow keys
 * move too. Clicking a face centres the map on that player.
 */
export function MapView({ world, tileSize, tileURL, players, faceURL, focus, coords, zoomButtons, caption, className }: MapViewProps) {
  const box = useRef<HTMLDivElement>(null)
  const canvas = useRef<HTMLCanvasElement>(null)
  const [size, setSize] = useState({ width: 0, height: 0 })
  const [view, setViewState] = useState<MapViewState>(() => ({ x: world.spawn.x, z: world.spawn.z, zoom: world.zoom.default }))
  // Faces glide to new positions, but follow the map exactly while it moves.
  const [moving, setMoving] = useState(false)
  const [dragging, setDragging] = useState(false)
  const viewRef = useRef(view)
  const sizeRef = useRef(size)
  const worldRef = useRef(world)
  const views = useRef(new Map<string, MapViewState>())
  const store = useRef<TileStore | null>(null)
  const frame = useRef(0)
  const anim = useRef(0)
  const animTarget = useRef<MapViewState | null>(null)
  const settle = useRef(0)
  const hover = useRef<{ x: number; z: number } | null>(null)
  const pointers = useRef(new Map<number, { x: number; y: number }>())
  const gesture = useRef<Gesture | null>(null)
  const wheel = useRef(0)
  const coordsRef = useRef(coords)
  const tileURLRef = useRef(tileURL)
  useEffect(() => {
    coordsRef.current = coords
    tileURLRef.current = tileURL
  })

  const draw = useCallback(() => {
    frame.current = 0
    const c = canvas.current
    const st = store.current
    const { width, height } = sizeRef.current
    if (!c || !st || width === 0 || height === 0) return
    const ctx = c.getContext('2d')
    if (!ctx) return
    const dpr = window.devicePixelRatio || 1
    const w = worldRef.current
    const v = viewRef.current
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.fillStyle = checkerA
    ctx.fillRect(0, 0, width, height)
    const origin = worldToScreen(v, width, height, w, 0, 0)
    const period = checkerSize * 2
    const ox = (((origin.left % period) + period) % period) - period
    const oy = (((origin.top % period) + period) % period) - period
    ctx.fillStyle = checkerB
    for (let y = oy, row = 0; y < height; y += checkerSize, row++) {
      for (let x = ox + (row % 2) * checkerSize; x < width; x += period) ctx.fillRect(x, y, checkerSize, checkerSize)
    }
    const now = Date.now()
    const refreshMs = Math.max(10, w.refreshSeconds) * 1000
    const scale = pixelsPerBlock(v.zoom, w)
    for (const p of visibleTiles(v, width, height, w, tileSize)) {
      const tile = st.get(tileURLRef.current(w.name, p.tz, p.x, p.z), refreshMs, now)
      // Tiles drawn at their own size or larger stay crisp pixel art.
      ctx.imageSmoothingEnabled = scale * 2 ** (w.zoom.max - p.tz) < 1
      const left = Math.round(p.left)
      const top = Math.round(p.top)
      const sz = Math.round(p.left + p.size) - left
      if (tile.bitmap) {
        ctx.drawImage(tile.bitmap, left, top, sz, sz)
      } else if (tile.state === 'loading' && p.tz > 0) {
        const parent = st.peek(tileURLRef.current(w.name, p.tz - 1, Math.floor(p.x / 2), Math.floor(p.z / 2)))
        if (parent?.bitmap) {
          const half = parent.bitmap.width / 2
          ctx.imageSmoothingEnabled = false
          ctx.drawImage(parent.bitmap, (((p.x % 2) + 2) % 2) * half, (((p.z % 2) + 2) % 2) * half, half, half, left, top, sz, sz)
        }
      }
    }
  }, [tileSize])

  const schedule = useCallback(() => {
    if (!frame.current) frame.current = window.requestAnimationFrame(draw)
  }, [draw])

  useEffect(() => {
    const st = new TileStore(schedule)
    store.current = st
    schedule()
    return () => {
      st.close()
      store.current = null
      window.cancelAnimationFrame(frame.current)
      window.cancelAnimationFrame(anim.current)
      window.clearTimeout(settle.current)
      frame.current = 0
    }
  }, [schedule])

  const report = useCallback(() => {
    const v = viewRef.current
    const at = hover.current ?? v
    coordsRef.current?.set(at.x, at.z)
  }, [])

  const setView = useCallback(
    (next: MapViewState) => {
      viewRef.current = next
      setViewState(next)
      setMoving(true)
      window.clearTimeout(settle.current)
      settle.current = window.setTimeout(() => setMoving(false), 150)
      schedule()
      report()
    },
    [schedule, report],
  )

  const animateTo = useCallback(
    (target: MapViewState) => {
      window.cancelAnimationFrame(anim.current)
      const from = viewRef.current
      if (reducedMotion()) {
        animTarget.current = null
        setView(target)
        return
      }
      animTarget.current = target
      const start = performance.now()
      const step = (now: number) => {
        const k = Math.min(1, (now - start) / 220)
        const e = 1 - (1 - k) ** 3
        setView({ x: from.x + (target.x - from.x) * e, z: from.z + (target.z - from.z) * e, zoom: from.zoom + (target.zoom - from.zoom) * e })
        if (k < 1) anim.current = window.requestAnimationFrame(step)
        else animTarget.current = null
      }
      anim.current = window.requestAnimationFrame(step)
    },
    [setView],
  )

  const stopAnimation = useCallback(() => {
    window.cancelAnimationFrame(anim.current)
    animTarget.current = null
  }, [])

  // Each world keeps the view it had when you switched away.
  useEffect(() => {
    const prev = worldRef.current
    if (prev.name === world.name) {
      worldRef.current = world
      schedule()
      return
    }
    views.current.set(prev.name, viewRef.current)
    worldRef.current = world
    stopAnimation()
    setView(views.current.get(world.name) ?? { x: world.spawn.x, z: world.spawn.z, zoom: world.zoom.default })
  }, [world, schedule, setView, stopAnimation])

  const centreOn = useCallback(
    (x: number, z: number) => {
      const w = worldRef.current
      animateTo({ x, z, zoom: clampZoom(Math.max(Math.round(viewRef.current.zoom), w.zoom.default), w) })
    },
    [animateTo],
  )

  useEffect(() => {
    if (focus) centreOn(focus.x, focus.z)
  }, [focus, centreOn])

  useLayoutEffect(() => {
    const el = box.current
    if (!el) return
    const measure = () => {
      const r = el.getBoundingClientRect()
      const next = { width: Math.round(r.width), height: Math.round(r.height) }
      const dpr = window.devicePixelRatio || 1
      const c = canvas.current
      if (c) {
        c.width = Math.round(next.width * dpr)
        c.height = Math.round(next.height * dpr)
      }
      sizeRef.current = next
      setSize(next)
      schedule()
    }
    measure()
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [schedule])

  useEffect(() => {
    report()
  }, [report])

  const zoomBy = useCallback(
    (delta: number, left?: number, top?: number) => {
      const { width, height } = sizeRef.current
      const w = worldRef.current
      const base = animTarget.current?.zoom ?? viewRef.current.zoom
      const target = clampZoom(Math.round(base) + delta, w)
      if (target === base) return
      animateTo(zoomAround(viewRef.current, width, height, w, target, left, top))
    },
    [animateTo],
  )

  // Wheel zoom needs a non-passive listener to keep the page from scrolling.
  useEffect(() => {
    const el = box.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      const r = el.getBoundingClientRect()
      wheel.current += e.deltaMode === 1 ? e.deltaY * 40 : e.deltaY
      if (Math.abs(wheel.current) < 60) return
      const dir = wheel.current < 0 ? 1 : -1
      wheel.current = 0
      zoomBy(dir, e.clientX - r.left, e.clientY - r.top)
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [zoomBy])

  function local(e: React.PointerEvent) {
    const r = box.current?.getBoundingClientRect()
    return { x: e.clientX - (r?.left ?? 0), y: e.clientY - (r?.top ?? 0) }
  }

  function startGesture() {
    const pts = [...pointers.current.values()]
    stopAnimation()
    if (pts.length >= 2) {
      const [a, b] = pts as [{ x: number; y: number }, { x: number; y: number }]
      gesture.current = { kind: 'pinch', start: viewRef.current, x: (a.x + b.x) / 2, y: (a.y + b.y) / 2, dist: Math.hypot(a.x - b.x, a.y - b.y) || 1, moved: true }
      setDragging(true)
    } else if (pts.length === 1) {
      const [a] = pts as [{ x: number; y: number }]
      gesture.current = { kind: 'pan', start: viewRef.current, x: a.x, y: a.y, dist: 0, moved: false }
    } else {
      gesture.current = null
      setDragging(false)
    }
  }

  function onPointerDown(e: React.PointerEvent<HTMLDivElement>) {
    if ((e.target as HTMLElement).closest('button')) return
    e.currentTarget.setPointerCapture(e.pointerId)
    pointers.current.set(e.pointerId, local(e))
    startGesture()
  }

  function onPointerMove(e: React.PointerEvent<HTMLDivElement>) {
    const p = local(e)
    const { width, height } = sizeRef.current
    const w = worldRef.current
    if (e.pointerType === 'mouse') hover.current = screenToWorld(viewRef.current, width, height, w, p.x, p.y)
    const g = gesture.current
    if (!pointers.current.has(e.pointerId) || !g) {
      report()
      return
    }
    pointers.current.set(e.pointerId, p)
    if (g.kind === 'pan') {
      const dx = p.x - g.x
      const dy = p.y - g.y
      if (!g.moved && Math.hypot(dx, dy) < 3) return
      if (!g.moved) {
        g.moved = true
        setDragging(true)
      }
      const scale = pixelsPerBlock(g.start.zoom, w)
      setView({ ...g.start, x: g.start.x - dx / scale, z: g.start.z - dy / scale })
      return
    }
    const [a, b] = [...pointers.current.values()] as [{ x: number; y: number }, { x: number; y: number }]
    const mid = { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 }
    const zoom = clampZoom(g.start.zoom + Math.log2((Math.hypot(a.x - b.x, a.y - b.y) || 1) / g.dist), w)
    const zoomed = zoomAround(g.start, width, height, w, zoom, g.x, g.y)
    const scale = pixelsPerBlock(zoom, w)
    setView({ ...zoomed, x: zoomed.x - (mid.x - g.x) / scale, z: zoomed.z - (mid.y - g.y) / scale })
  }

  function onPointerUp(e: React.PointerEvent<HTMLDivElement>) {
    if (!pointers.current.delete(e.pointerId)) return
    const g = gesture.current
    if (g?.kind === 'pinch' && pointers.current.size < 2) {
      const { width, height } = sizeRef.current
      const v = viewRef.current
      animateTo(zoomAround(v, width, height, worldRef.current, Math.round(v.zoom)))
    }
    startGesture()
  }

  function onKeyDown(e: React.KeyboardEvent<HTMLDivElement>) {
    if (e.target !== e.currentTarget) return
    const { width, height } = sizeRef.current
    const w = worldRef.current
    const v = animTarget.current ?? viewRef.current
    const stepBlocks = Math.max(width, height) / 4 / pixelsPerBlock(v.zoom, w)
    const moves: Record<string, [number, number]> = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] }
    const move = moves[e.key]
    if (move) {
      e.preventDefault()
      animateTo({ ...v, x: v.x + move[0] * stepBlocks, z: v.z + move[1] * stepBlocks })
    } else if (e.key === '+' || e.key === '=') {
      e.preventDefault()
      zoomBy(1)
    } else if (e.key === '-' || e.key === '_') {
      e.preventDefault()
      zoomBy(-1)
    }
  }

  const here = (players ?? []).filter((p) => p.world === world.name)
  const zoomLevel = Math.round(view.zoom)
  return (
    <div
      ref={box}
      tabIndex={0}
      role="application"
      aria-roledescription={t('tab.map')}
      aria-label={t('map.canvas', { world: world.label || world.name })}
      className={cn('relative touch-none overflow-hidden rounded-2xl border border-border bg-[#E9EAE3] outline-none select-none focus-visible:ring-2 focus-visible:ring-ring', dragging ? 'cursor-grabbing' : 'cursor-grab', className)}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
      onPointerLeave={() => {
        hover.current = null
        report()
      }}
      onDoubleClick={(e) => {
        if ((e.target as HTMLElement).closest('button')) return
        const r = box.current?.getBoundingClientRect()
        zoomBy(1, e.clientX - (r?.left ?? 0), e.clientY - (r?.top ?? 0))
      }}
      onKeyDown={onKeyDown}
    >
      <canvas ref={canvas} className="absolute inset-0 size-full" aria-hidden="true" />
      {size.width > 0 &&
        here.map((p) => {
          const at = worldToScreen(view, size.width, size.height, world, p.x, p.z)
          if (at.left < -80 || at.top < -40 || at.left > size.width + 80 || at.top > size.height + 40) return null
          return (
            <button
              type="button"
              key={p.uuid || p.name}
              onClick={() => centreOn(p.x, p.z)}
              aria-label={t('map.find', { name: p.name })}
              className={cn(
                'absolute top-0 left-0 inline-flex cursor-pointer items-center gap-1.5 rounded-full bg-white py-[3px] pr-2.5 pl-[3px] text-xs font-semibold whitespace-nowrap text-foreground shadow-popup outline-none focus-visible:ring-2 focus-visible:ring-ring',
                !moving && 'transition-transform duration-700 ease-out motion-reduce:transition-none',
              )}
              style={{ transform: `translate(${Math.round(at.left)}px, ${Math.round(at.top)}px) translate(-50%, -50%)` }}
            >
              <MapFace name={p.name} src={faceURL?.(p)} size={18} />
              {p.name}
            </button>
          )
        })}
      {zoomButtons && (
        <div className="absolute top-3 left-3 flex flex-col overflow-hidden rounded-lg border border-border bg-white shadow-popup max-sm:hidden">
          <button type="button" onClick={() => zoomBy(1)} disabled={zoomLevel >= maxZoom(world)} aria-label={t('map.zoomIn')} className="flex size-8 items-center justify-center text-foreground outline-none hover:bg-accent focus-visible:bg-accent disabled:text-muted-foreground/50 [&_svg]:size-4">
            <PlusIcon />
          </button>
          <span className="h-px bg-border" aria-hidden="true" />
          <button type="button" onClick={() => zoomBy(-1)} disabled={zoomLevel <= 0} aria-label={t('map.zoomOut')} className="flex size-8 items-center justify-center text-foreground outline-none hover:bg-accent focus-visible:bg-accent disabled:text-muted-foreground/50 [&_svg]:size-4">
            <MinusIcon />
          </button>
        </div>
      )}
      {caption && <div className="pointer-events-none absolute bottom-3 left-3 rounded-full bg-white px-2.5 py-1 text-xs text-foreground shadow-popup">{caption}</div>}
    </div>
  )
}

const toggleItem = 'h-7 rounded-[7px] border-0 px-2.5 text-[13px] font-medium text-muted-foreground hover:bg-transparent hover:text-foreground data-pressed:bg-white data-pressed:text-foreground data-pressed:shadow-outline sm:h-7 sm:text-[13px]'

/**
 * Which world the map shows. Up to three worlds use a toggle; more use a
 * select grouped into the server's own worlds and the ones something added
 * (a bottom sheet on phones).
 */
export function WorldSwitch({ worlds, value, onChange, serverName, serverType, levelName, large }: { worlds: MapWorld[]; value: string; onChange: (name: string) => void; serverName: string; serverType: string; levelName?: string; large?: boolean }) {
  const [open, setOpen] = useState(false)
  if (worlds.length <= 3) {
    return (
      <ToggleGroup value={[value]} onValueChange={(v) => v[0] && onChange(v[0])} aria-label={t('map.worlds')} className={cn('gap-0.5 rounded-[9px] bg-muted p-0.5', large && 'w-full rounded-xl p-1')}>
        {worlds.map((w) => (
          <ToggleGroupItem key={w.name} value={w.name} className={cn(toggleItem, large && 'h-11 flex-1 rounded-[10px] text-[15px] sm:h-11 sm:text-[15px]')}>
            {worldLabel(w, levelName, large ? 'short' : 'toggle')}
          </ToggleGroupItem>
        ))}
      </ToggleGroup>
    )
  }
  const groups = [
    { label: t('map.worldsOf', { server: serverName }), worlds: worlds.filter((w) => ownWorld(w, levelName)) },
    { label: addedWorldsLabel(serverType), worlds: worlds.filter((w) => !ownWorld(w, levelName)) },
  ].filter((g) => g.worlds.length > 0)
  const current = worlds.find((w) => w.name === value)
  if (large) {
    return (
      <>
        <button type="button" onClick={() => setOpen(true)} aria-haspopup="dialog" aria-label={t('map.worlds')} className="flex h-11 w-full items-center gap-2 rounded-xl border border-border bg-white px-3.5 text-left text-[15px] shadow-outline">
          <span className="min-w-0 flex-1 truncate">{current ? worldLabel(current, levelName, 'long') : ''}</span>
          <ChevronsUpDownIcon className="size-4 text-muted-foreground" aria-hidden="true" />
        </button>
        <Sheet open={open} onOpenChange={setOpen}>
          <SheetPopup side="bottom" className="px-4">
            <div className="pt-3 pb-3">
              <SheetTitle className="text-lg font-bold">{t('map.worlds')}</SheetTitle>
            </div>
            <div className="flex flex-col gap-4 pb-2">
              {groups.map((g) => (
                <section key={g.label}>
                  <h3 className="section-label px-4 pb-2">{g.label}</h3>
                  <ul className="overflow-hidden rounded-3xl border border-border bg-white">
                    {g.worlds.map((w) => (
                      <li key={w.name} className="border-b border-border last:border-b-0">
                        <button
                          type="button"
                          onClick={() => {
                            onChange(w.name)
                            setOpen(false)
                          }}
                          aria-current={w.name === value ? 'true' : undefined}
                          className="flex min-h-14 w-full items-center gap-3 px-4 text-left text-base"
                        >
                          <span className="min-w-0 flex-1 truncate">{worldLabel(w, levelName, 'long')}</span>
                          {w.name === value && <CheckIcon className="size-5 text-primary" aria-hidden="true" />}
                        </button>
                      </li>
                    ))}
                  </ul>
                </section>
              ))}
            </div>
          </SheetPopup>
        </Sheet>
      </>
    )
  }
  return (
    <Select value={value} onValueChange={(v) => v !== null && onChange(v)} items={worlds.map((w) => ({ value: w.name, label: worldLabel(w, levelName, 'long') }))}>
      <SelectTrigger aria-label={t('map.worlds')} className="w-[220px]">
        <SelectValue />
      </SelectTrigger>
      <SelectPopup alignItemWithTrigger={false}>
        {groups.map((g, i) => (
          <SelectGroup key={g.label}>
            {i > 0 && <SelectSeparator />}
            <SelectGroupLabel>{g.label}</SelectGroupLabel>
            {g.worlds.map((w) => (
              <SelectItem key={w.name} value={w.name}>
                {worldLabel(w, levelName, 'long')}
              </SelectItem>
            ))}
          </SelectGroup>
        ))}
      </SelectPopup>
    </Select>
  )
}

const faceColors = ['#E3F1E6', '#FFF3D1', '#E7EEFB', '#F7E4E4', '#EDE7F6', '#E6F4F1']

/** A face on the map: the player's skin through the panel, or their initial. */
export function MapFace({ name, src, size = 18, className }: { name: string; src?: string; size?: number; className?: string }) {
  const [failed, setFailed] = useState(false)
  const color = faceColors[[...name].reduce((a, c) => a + c.charCodeAt(0), 0) % faceColors.length]
  return (
    <span className={cn('relative inline-flex shrink-0 items-center justify-center overflow-hidden rounded-full font-semibold text-foreground/80', className)} style={{ width: size, height: size, background: failed || !src ? color : undefined, fontSize: Math.round(size * 0.5) }} aria-hidden="true">
      {failed || !src ? name.replace(/^\./, '').slice(0, 1).toUpperCase() : <img src={src} width={size} height={size} alt="" className="pixelated size-full" onError={() => setFailed(true)} />}
    </span>
  )
}
