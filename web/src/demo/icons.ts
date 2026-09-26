// Made-up icons for the demo's plugins: 5 × 5 pixel patterns on a coloured
// tile. The demo build writes them next to the app as icons/<n>.svg, as it
// does the players' faces, because playkeeper.io's Content-Security-Policy
// loads images from the site only.

const patterns = [
  ['..#..', '.###.', '#####', '.###.', '..#..'],
  ['#####', '#...#', '#.#.#', '#...#', '#####'],
  ['#...#', '#####', '#...#', '#####', '#...#'],
  ['.#.#.', '#####', '.#.#.', '#####', '.#.#.'],
  ['..#..', '..#..', '#####', '..#..', '..#..'],
  ['#.#.#', '.#.#.', '#.#.#', '.#.#.', '#.#.#'],
  ['.###.', '#...#', '#.#.#', '#...#', '.###.'],
  ['#...#', '.#.#.', '..#..', '.#.#.', '#...#'],
]

// A tile colour and a lighter one for its pattern.
const colours: [string, string][] = [
  ['#2f7d4f', '#c8f0d4'],
  ['#3a5fc0', '#d4e0ff'],
  ['#b4532a', '#ffdcc8'],
  ['#6b46a8', '#e8dcff'],
  ['#9a6a12', '#ffe9b8'],
  ['#18717d', '#c2f1f6'],
  ['#8a2f55', '#ffd0e4'],
  ['#3f4a55', '#dfe5ea'],
]

/** Every icon is a different pattern and colour pair. */
export const iconCount = 16

/** Icon number n (0 to iconCount − 1) as an SVG file: the pattern with a pixel of tile around it. */
export function iconSvg(n: number): string {
  const rows = patterns[n % patterns.length] ?? patterns[0] ?? []
  const [tile, ink] = colours[(n + Math.floor(n / patterns.length) * 3) % colours.length] ?? ['#3f4a55', '#dfe5ea']
  let d = ''
  rows.forEach((row, y) => {
    for (let x = 0; x < row.length; x++) if (row[x] === '#') d += `M${x + 1} ${y + 1}h1v1h-1z`
  })
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 7 7" shape-rendering="crispEdges"><rect width="7" height="7" fill="${tile}"/><path fill="${ink}" d="${d}"/></svg>\n`
}
