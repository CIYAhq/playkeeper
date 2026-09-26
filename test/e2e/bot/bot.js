#!/usr/bin/env node
// Playkeeper end-to-end protocol bot (mineflayer). Offline-mode test harness
// only: it proves real protocol logins, block placement and world state, but it
// is not an official Minecraft client with a genuine account.
//
//   node bot.js place  --host H --port P --name PkBotBuilder --nonce N --out marker.json --panel URL --cacert C --state S
//   node bot.js verify --host H --port P --name PkBotBuilder --marker marker.json
//   node bot.js visit  --host H --port P --name PkBotFriend --stay 20 [--say "text"]
'use strict'
const fs = require('fs')
const https = require('https')
const mineflayer = require('mineflayer')
const { Vec3 } = require('vec3')

function args () {
  const out = { _: process.argv[2] }
  for (let i = 3; i < process.argv.length; i += 2) out[process.argv[i].replace(/^--/, '')] = process.argv[i + 1]
  return out
}

const a = args()
const log = (...m) => console.log(new Date().toISOString(), ...m)

// Runs a console command through Playkeeper's authenticated, audited API, on
// the server the test client's state file names.
function panelCommand (command) {
  const state = JSON.parse(fs.readFileSync(a.state, 'utf8'))
  const url = new URL(`/api/servers/${state.server}/command`, a.panel)
  const body = JSON.stringify({ command })
  return new Promise((resolve, reject) => {
    const req = https.request(url, {
      method: 'POST',
      ca: fs.readFileSync(a.cacert),
      headers: { 'Content-Type': 'application/json', Cookie: state.cookie, 'X-CSRF-Token': state.csrf, Origin: url.origin, 'Content-Length': Buffer.byteLength(body) }
    }, res => {
      let data = ''
      res.on('data', d => { data += d })
      res.on('end', () => res.statusCode === 200 ? resolve(JSON.parse(data).output) : reject(new Error(`console ${res.statusCode}: ${data}`)))
    })
    req.on('error', reject)
    req.end(body)
  })
}

function connect (name) {
  return new Promise((resolve, reject) => {
    const bot = mineflayer.createBot({ host: a.host, port: Number(a.port || 25565), username: name, version: a.version || '26.1', auth: 'offline' })
    const timer = setTimeout(() => reject(new Error('timed out waiting for spawn')), 90000)
    bot.once('spawn', () => { clearTimeout(timer); resolve(bot) })
    bot.once('kicked', r => { clearTimeout(timer); reject(new Error('kicked: ' + JSON.stringify(r))) })
    bot.once('error', e => { clearTimeout(timer); reject(e) })
  })
}

// placeOn puts item on top of the block at below, trying again a few times: right
// after the console's fill, tp and give, the server now and then refuses the
// first placement. A block that is already there counts, in case an earlier
// try went through but its confirmation was lost.
async function placeOn (bot, below, item) {
  const target = below.offset(0, 1, 0)
  for (let attempt = 1; ; attempt++) {
    if (bot.blockAt(target)?.name === item) return
    try {
      await bot.equip(bot.inventory.items().find(i => i.name === item), 'hand')
      await bot.placeBlock(bot.blockAt(below), new Vec3(0, 1, 0))
      return
    } catch (e) {
      if (attempt >= 4) throw e
      log(`placing ${item} at ${target} failed (${e.message}); trying again`)
      await bot.waitForTicks(20)
    }
  }
}

async function place () {
  const bot = await connect(a.name)
  await bot.waitForTicks(20)
  const p = bot.entity.position.floored()
  const [x, y, z] = [p.x, p.y, p.z]
  log('spawned', a.name, 'at', x, y, z)
  for (const cmd of [
    `fill ${x - 1} ${y - 1} ${z - 1} ${x + 3} ${y - 1} ${z + 1} minecraft:stone`,
    `fill ${x - 1} ${y} ${z - 1} ${x + 3} ${y + 3} ${z + 1} minecraft:air`,
    `tp ${a.name} ${x}.5 ${y} ${z}.5 -90 30`,
    `give ${a.name} minecraft:gold_block 1`,
    `give ${a.name} minecraft:oak_sign 1`
  ]) log('console>', cmd, '=>', await panelCommand(cmd))
  await bot.waitForTicks(30)
  await placeOn(bot, new Vec3(x + 2, y - 1, z), 'gold_block')
  const gold = bot.blockAt(new Vec3(x + 2, y, z))
  log('client placed', gold.name, 'at', x + 2, y, z)
  await placeOn(bot, new Vec3(x + 2, y, z), 'oak_sign').catch(e => log('sign placement note:', e.message))
  await bot.waitForTicks(10)
  const sign = bot.blockAt(new Vec3(x + 2, y + 1, z))
  if (sign.name !== 'oak_sign') throw new Error('sign was not placed: ' + sign.name)
  bot.updateSign(sign, a.nonce)
  await bot.waitForTicks(30)
  const marker = { gold: [x + 2, y, z], sign: [x + 2, y + 1, z], nonce: a.nonce, placedBy: a.name, at: new Date().toISOString() }
  fs.writeFileSync(a.out, JSON.stringify(marker, null, 2))
  log('marker written', JSON.stringify(marker))
  bot.quit('done')
  await new Promise(resolve => setTimeout(resolve, 1500))
}

async function verify () {
  const marker = JSON.parse(fs.readFileSync(a.marker, 'utf8'))
  const bot = await connect(a.name)
  await bot.waitForTicks(20)
  const [gx, gy, gz] = marker.gold
  const [sx, sy, sz] = marker.sign
  let gold = null
  let sign = null
  for (let i = 0; i < 20 && !(gold && sign); i++) {
    await bot.waitForTicks(10)
    gold = bot.blockAt(new Vec3(gx, gy, gz))
    sign = bot.blockAt(new Vec3(sx, sy, sz))
  }
  const text = sign && (sign.getSignText ? sign.getSignText()[0] : sign.signText)
  const result = { gold: gold && gold.name, sign: sign && sign.name, signText: text, expectedNonce: marker.nonce }
  result.ok = result.gold === 'gold_block' && result.sign === 'oak_sign' && String(text).split('\n')[0] === marker.nonce
  log('client view', JSON.stringify(result))
  bot.quit('done')
  await new Promise(resolve => setTimeout(resolve, 1500))
  if (!result.ok) process.exit(1)
}

// Stays for --stay seconds, or until the server ends the connection (a backup
// or crash with the player online), which is logged rather than treated as an error.
async function visit () {
  const bot = await connect(a.name)
  log('joined', a.name)
  let ended = false
  bot.on('kicked', r => log('server disconnected', a.name + ':', JSON.stringify(r)))
  const gone = new Promise(resolve => bot.once('end', r => { ended = true; log('connection ended', a.name + ':', String(r)); resolve() }))
  if (a.say) { await bot.waitForTicks(20); bot.chat(a.say); log('said', JSON.stringify(a.say)) }
  await Promise.race([new Promise(resolve => setTimeout(resolve, Number(a.stay || 10) * 1000)), gone])
  if (!ended) {
    bot.quit('done')
    log('left', a.name)
  }
  await new Promise(resolve => setTimeout(resolve, 1500))
}

const cmds = { place, verify, visit }
if (!cmds[a._]) { console.error('usage: bot.js place|verify|visit ...'); process.exit(2) }
cmds[a._]().then(() => process.exit(0)).catch(e => { console.error('bot error:', e.message); process.exit(1) })
