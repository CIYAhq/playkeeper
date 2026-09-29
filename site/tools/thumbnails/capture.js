// Joins a template's server as a bot, does what the template's recipe says
// to reach its showpiece (recipes.json; spawn when it has none), and saves
// the world around it for render.mjs: every loaded chunk column within the
// radius, where the bot stood, and the world's spawn. The server must be in
// offline mode, with the bot on its whitelist and an operator, as
// cmd/template-check -shots arranges.
//
//   node capture.js --port 25565 --template skyblock --out /tmp/shots/skyblock.json.gz
const fs = require('fs')
const path = require('path')
const zlib = require('zlib')
const mineflayer = require('mineflayer')
const { VERSION } = require('./version')

function args () {
  const out = { host: '127.0.0.1', port: '25565', name: 'PkShot', radius: '10', recipes: path.join(__dirname, 'recipes.json') }
  for (let i = 2; i < process.argv.length; i += 2) out[process.argv[i].replace(/^--/, '')] = process.argv[i + 1]
  return out
}
const a = args()
if (!a.template || !a.out) {
  console.error('usage: node capture.js --port P --template ID --out FILE.json.gz [--host H] [--name BOT] [--radius CHUNKS]')
  process.exit(2)
}
const recipes = JSON.parse(fs.readFileSync(a.recipes, 'utf8'))
const recipe = recipes[a.template] || { steps: [] }
const radius = Number(a.radius)
const log = (...m) => console.log(`[${a.template}]`, ...m)
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

// text is what a chat component says, however it's nested.
function text (c) {
  if (c == null) return ''
  if (typeof c === 'string') return c
  if (Array.isArray(c)) return c.map(text).join('')
  if (typeof c === 'object') {
    if ('value' in c && 'type' in c) return text(c.value)
    return Object.entries(c).map(([k, v]) => (k === 'text' || k === 'extra' || k === '' ? text(v) : '')).join('')
  }
  return ''
}

const bot = mineflayer.createBot({ host: a.host, port: Number(a.port), username: a.name, version: VERSION, auth: 'offline', viewDistance: 'far' })
const chat = []
bot.on('messagestr', (m) => { chat.push(m); log('chat:', m.slice(0, 160)) })
bot.on('kicked', (r) => { console.error(`[${a.template}] kicked: ${(text(r) || JSON.stringify(r)).replace(/\s+/g, ' ')}`); process.exit(1) })
bot.on('error', (e) => { console.error(`[${a.template}] ${e.message}`); process.exit(1) })
setTimeout(() => { console.error(`[${a.template}] gave up after ${a.timeout || 300} s`); process.exit(1) }, Number(a.timeout || 300) * 1000).unref()

async function until (pattern, timeout) {
  const re = new RegExp(pattern, 'i')
  const deadline = Date.now() + timeout
  for (let seen = 0; Date.now() < deadline; await sleep(250)) {
    for (; seen < chat.length; seen++) if (re.test(chat[seen])) return
  }
  throw new Error(`the server never said /${pattern}/`)
}

async function click ({ item, name }) {
  const deadline = Date.now() + 10000
  while (!bot.currentWindow && Date.now() < deadline) await sleep(200)
  const w = bot.currentWindow
  if (!w) throw new Error('no window opened to click in')
  const items = w.slots.slice(0, w.inventoryStart).filter((s) => s && (!item || s.name === item))
  const named = items.find((s) => name && text(s.customName).toLowerCase().includes(name.toLowerCase()))
  const pick = named || items[0]
  if (!pick) throw new Error(`the window has no ${item || 'item'} to click`)
  log('clicking', pick.name, JSON.stringify(text(pick.customName)))
  await bot.clickWindow(pick.slot, 0, 0)
}

async function columns (cx, cz) {
  const want = []
  for (let i = -radius; i <= radius; i++) for (let j = -radius; j <= radius; j++) want.push([cx + i, cz + j])
  let have = 0
  let last = -1
  let still = Date.now()
  const deadline = Date.now() + 120000
  while (Date.now() < deadline) {
    await sleep(1000)
    have = want.filter(([x, z]) => bot.world.getColumn(x, z)).length
    if (have !== last) { last = have; still = Date.now() }
    if (have === want.length || Date.now() - still > 15000) break
  }
  log(`columns ${have}/${want.length}`)
  if (have < want.length * 0.5) throw new Error(`only ${have} of ${want.length} chunk columns loaded`)
  const out = []
  for (const [x, z] of want) {
    const col = bot.world.getColumn(x, z)
    if (col) out.push({ x: x * 16, z: z * 16, chunk: col.toJson() })
  }
  return out
}

bot.once('spawn', async () => {
  try {
    log('spawned at', bot.entity.position.floored().toString(), 'world spawn', bot.spawnPoint && bot.spawnPoint.toString())
    await sleep(2000)
    for (const s of recipe.steps) {
      if (s.chat) { log('>', s.chat); bot.chat(s.chat) }
      if (s.click) await click(s.click)
      if (s.until) await until(s.until, s.timeout || 60000)
      await sleep(s.wait ?? 1500)
    }
    const at = bot.entity.position.clone()
    const player = recipe.player ? { x: at.x, y: at.y, z: at.z } : null
    if (recipe.steps.length) {
      bot.chat('/gamemode spectator')
      await sleep(1500)
    }
    const focus = recipe.subject ? at : (bot.spawnPoint || at)
    const cols = await columns(Math.floor(focus.x / 16), Math.floor(focus.z / 16))
    const snapshot = { version: bot.version, template: a.template, subject: !!recipe.subject, focus: { x: focus.x, y: focus.y, z: focus.z }, player, radius, columns: cols }
    fs.mkdirSync(path.dirname(path.resolve(a.out)), { recursive: true })
    fs.writeFileSync(a.out, zlib.gzipSync(JSON.stringify(snapshot)))
    log('saved', a.out, cols.length, 'columns')
    bot.quit()
    setTimeout(() => process.exit(0), 500)
  } catch (e) {
    console.error(`[${a.template}] ${e.message}`)
    process.exit(1)
  }
})
