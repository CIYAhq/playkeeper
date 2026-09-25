// Made-up Minecraft faces for the demo's players: 8 × 8 pixel SVGs, picked by
// name. The demo build writes them next to the app as faces/<n>.svg, because
// playkeeper.io's Content-Security-Policy loads images from the site only.

const skins = ['#f1c9a5', '#dba577', '#bb7f51', '#8d5b3d', '#f7dcc1', '#c89064']
const hairs = ['#4a2f1e', '#231c18', '#b98a35', '#7c3f1c', '#dcc37e', '#58586a', '#a13d2d']
const hoods = ['#3d6fa6', '#2e8a6c', '#8a4190', '#c15f2c']
const irises = ['#3a5fc0', '#2f7d4f', '#5b3a22', '#43535f']

export const faceCount = 12

type Pixel = 'hair' | 'skin' | 'white' | 'iris' | 'shade' | 'mouth'

// Each style says what one pixel of the face is.
const styles: ((x: number, y: number) => Pixel)[] = [
  // Short hair and a beard.
  (x, y) => (y < 2 || (y < 4 && (x === 0 || x === 7)) || (y === 6 && x > 1 && x < 6) ? 'hair' : face(x, y)),
  // A fringe and long hair.
  (x, y) => (y < 2 || (y === 2 && (x < 3 || x > 4)) || x === 0 || x === 7 ? 'hair' : y === 6 && (x === 3 || x === 4) ? 'mouth' : face(x, y)),
  // A hood.
  (x, y) => (y < 2 || x === 0 || x === 7 || (y === 2 && (x === 1 || x === 6)) ? 'hair' : y === 6 && (x === 3 || x === 4) ? 'mouth' : face(x, y)),
]

function face(x: number, y: number): Pixel {
  if (y === 4) return x === 1 || x === 6 ? 'white' : x === 2 || x === 5 ? 'iris' : 'skin'
  if (y === 5 && (x === 3 || x === 4)) return 'shade'
  return 'skin'
}

function palette(n: number): Record<Pixel, string> {
  const style = n % styles.length
  const skin = skins[Math.floor(n / styles.length + n) % skins.length] ?? '#f1c9a5'
  return {
    hair: style === 2 ? (hoods[n % hoods.length] ?? '#3d6fa6') : (hairs[(n * 5) % hairs.length] ?? '#4a2f1e'),
    skin,
    white: '#ffffff',
    iris: irises[(n * 3) % irises.length] ?? '#3a5fc0',
    shade: shadeOf(skin),
    mouth: '#8e4a3c',
  }
}

function shadeOf(hex: string): string {
  const dim = (i: number) => Math.round(parseInt(hex.slice(i, i + 2), 16) * 0.85)
    .toString(16)
    .padStart(2, '0')
  return `#${dim(1)}${dim(3)}${dim(5)}`
}

/** Face number n (0 to faceCount − 1) as an SVG file, one path per colour. */
export function faceSvg(n: number): string {
  const style = styles[n % styles.length] ?? face
  const colours = palette(n)
  const paths = new Map<string, string>()
  for (let y = 0; y < 8; y++) {
    for (let x = 0; x < 8; ) {
      const colour = colours[style(x, y)]
      let end = x + 1
      while (end < 8 && colours[style(end, y)] === colour) end++
      paths.set(colour, `${paths.get(colour) ?? ''}M${x} ${y}h${end - x}v1h-${end - x}z`)
      x = end
    }
  }
  const body = [...paths].map(([fill, d]) => `<path fill="${fill}" d="${d}"/>`).join('')
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8" shape-rendering="crispEdges">${body}</svg>\n`
}

/** Which face a player gets: the same one for the same name, every time. */
export function faceIndex(name: string): number {
  let h = 0x811c9dc5
  for (const c of name.toLowerCase()) h = Math.imul(h ^ c.charCodeAt(0), 0x01000193)
  return (h >>> 0) % faceCount
}
