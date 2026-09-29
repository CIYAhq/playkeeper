// Builds the renderer into dist/: prismarine-viewer with the patches in
// patches/, the texture atlas and block states of the Minecraft version the
// capture bot speaks (from minecraft-assets, which carries Minecraft's own
// textures), and the page render.mjs drives. Run after npm ci: npm run build.
const fs = require('fs')
const path = require('path')
const { execFileSync } = require('child_process')
const { VERSION } = require('./version')

const dist = path.join(__dirname, 'dist')
const pv = path.dirname(require.resolve('prismarine-viewer/package.json'))

// Each patch names the line it adds, so a second build skips it.
const patches = {
  'prismarine-viewer-1.33.0.patch': ["'1.21.4', '26.1']", 'viewer/lib/version.js'],
  'prismarine-viewer-1.33.0-negative-y.patch': ['y < 320', 'viewer/lib/worldrenderer.js'],
  'prismarine-viewer-1.33.0-stairs.patch': ["block.name.endsWith('_air')", 'viewer/lib/models.js']
}
for (const [file, [marker, target]] of Object.entries(patches)) {
  if (fs.readFileSync(path.join(pv, target), 'utf8').includes(marker)) continue
  execFileSync('patch', ['-p0', '--forward', '--quiet', '-i', path.join(__dirname, 'patches', file)], { cwd: __dirname, stdio: 'inherit' })
}

fs.mkdirSync(path.join(dist, 'textures'), { recursive: true })
fs.mkdirSync(path.join(dist, 'blocksStates'), { recursive: true })
const { makeTextureAtlas } = require(path.join(pv, 'viewer/lib/atlas'))
const { prepareBlocksStates } = require(path.join(pv, 'viewer/lib/modelsBuilder'))
const assets = require('minecraft-assets')(VERSION)
const atlas = makeTextureAtlas(assets)
// The game darkens deep water; the viewer lights the sea floor through it
// as if it were shallow, so its water is drawn less see-through.
const g = atlas.canvas.getContext('2d')
for (const name of ['water_still', 'water_flow', 'water_overlay']) {
  const t = atlas.json.textures[name]
  if (!t) continue
  const [x, y, w, h] = [t.u * atlas.canvas.width, t.v * atlas.canvas.height, t.su * atlas.canvas.width, t.sv * atlas.canvas.height].map(Math.round)
  const px = g.getImageData(x, y, w, h)
  for (let i = 3; i < px.data.length; i += 4) if (px.data[i]) px.data[i] = Math.max(px.data[i], 222)
  g.putImageData(px, x, y)
}
fs.writeFileSync(path.join(dist, 'textures', VERSION + '.png'), atlas.canvas.toBuffer('image/png'))
fs.writeFileSync(path.join(dist, 'blocksStates', VERSION + '.json'), JSON.stringify(prepareBlocksStates(assets, atlas)))
for (const f of ['index.html', 'skin.png']) fs.copyFileSync(path.join(__dirname, 'render', f), path.join(dist, f))

execFileSync(process.execPath, [require.resolve('webpack-cli/bin/cli.js'), '--config', path.join(__dirname, 'render', 'webpack.config.js')], { cwd: __dirname, stdio: 'inherit' })
console.log(`built dist/ for Minecraft ${VERSION}: ${Object.keys(atlas.json.textures).length} textures`)
