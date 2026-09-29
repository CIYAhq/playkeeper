// Bundles render/thumb.js and prismarine-viewer's mesher worker into dist/.
// Of minecraft-data, only the files they read are kept: the capture
// version's blocks, biomes and states, and 1.16.2's tints.
const path = require('path')
const webpack = require('webpack')
const { VERSION } = require('../version')

const tool = path.resolve(__dirname, '..')
const pv = path.dirname(require.resolve('prismarine-viewer/package.json'))
const dataPaths = require('minecraft-data/minecraft-data/data/dataPaths.json')
const keep = new Set()
for (const v of [VERSION, '1.16.2']) {
  for (const [kind, dir] of Object.entries(dataPaths.pc[v])) {
    if (!['protocol', 'language', 'commands', 'proto', 'loginPacket'].includes(kind)) keep.add(`${dir}/${kind}`)
  }
}
function mcDataFilter ({ context, request }, cb) {
  if ((context || '').includes('minecraft-data') && request.endsWith('.json')) {
    const m = /data\/(pc|bedrock)\/([^/]+)\/([^/]+)\.json$/.exec(request)
    if (m && m[2] !== 'common' && !keep.has(`${m[1]}/${m[2]}/${m[3]}`)) return cb(null, '{}')
    if (!m && !/(versions|dataPaths|protocolVersions|features|legacy)\.json$/.test(request)) return cb(null, '{}')
  }
  cb()
}
const common = {
  mode: 'production',
  context: tool,
  resolve: { fallback: { zlib: false, fs: false, path: false }, modules: [path.join(tool, 'node_modules'), 'node_modules'] },
  externals: [mcDataFilter],
  plugins: [
    new webpack.ProvidePlugin({ process: 'process/browser', Buffer: ['buffer', 'Buffer'] }),
    new webpack.NormalModuleReplacementPlugin(/viewer[/|\\]lib[/|\\]utils/, './utils.web.js')
  ],
  performance: { hints: false }
}
module.exports = [
  { ...common, entry: path.join(__dirname, 'thumb.js'), output: { path: path.join(tool, 'dist'), filename: 'thumb.js' } },
  { ...common, entry: path.join(pv, 'viewer/lib/worker.js'), output: { path: path.join(tool, 'dist'), filename: 'worker.js' } }
]
