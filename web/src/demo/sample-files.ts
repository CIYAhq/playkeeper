// Each server's folder as its Files tab first shows it in the live demo: the
// files its software, plugins or mods and world keep there, written from what
// the other screens show of the server. Text files are short versions of the
// real ones; any other file has a size and no bytes.

import type { ServerStatus } from '@/api/types'
import { parentOf } from '@/lib/files'
import { addonKind } from '@/lib/software'
import { fakeSha, iso, library, noise, type DemoFile, type DemoState } from './data'

const kb = 1024
const mb = 1024 * kb
const minute = 60_000
const hour = 60 * minute
const day = 24 * hour

export type Tree = Record<string, DemoFile>

/** A number for a text, the same each time (FNV-1a). */
export function seedOf(text: string): number {
  let h = 0x811c9dc5
  for (let i = 0; i < text.length; i++) h = Math.imul(h ^ text.charCodeAt(i), 0x01000193)
  return h >>> 0
}

function uuidOf(name: string): string {
  const h = fakeSha(seedOf(name))
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-4${h.slice(13, 16)}-a${h.slice(17, 20)}-${h.slice(20, 32)}`
}

/** A moment as Java's Date prints it, which the server writes at the top of its .properties files. */
function javaDate(ms: number): string {
  const d = new Date(ms)
  const two = (n: number) => String(n).padStart(2, '0')
  const dayName = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'][d.getUTCDay()]
  const month = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'][d.getUTCMonth()]
  return `${dayName} ${month} ${two(d.getUTCDate())} ${two(d.getUTCHours())}:${two(d.getUTCMinutes())}:${two(d.getUTCSeconds())} UTC ${d.getUTCFullYear()}`
}

const json = (value: unknown) => `${JSON.stringify(value, null, 2)}\n`

/** The keys of server.properties Playkeeper sets from a server's settings each time it starts, as the agent reports them. */
export function managedKeys(s: DemoState, srv: ServerStatus): string[] {
  const keys = ['broadcast-rcon-to-ops', 'difficulty', 'enable-query', 'enable-rcon', 'enforce-whitelist', 'gamemode', 'hardcore', 'level-name', 'level-type', 'log-ips', 'max-players', 'motd', 'online-mode', 'pvp', 'rcon.password', 'rcon.port', 'server-port', 'view-distance', 'white-list']
  if (s.packs[srv.id]?.resource) keys.push('require-resource-pack', 'resource-pack', 'resource-pack-id', 'resource-pack-prompt', 'resource-pack-sha1')
  return keys.sort()
}

function properties(s: DemoState, srv: ServerStatus, at: number): string {
  const c = srv.config
  const g = srv.gameplay
  const pack = s.packs[srv.id]?.resource
  const values: Record<string, string> = {
    'accepts-transfers': 'false',
    'allow-flight': srv.type === 'paper' ? 'false' : 'true',
    'broadcast-console-to-ops': 'true',
    'broadcast-rcon-to-ops': 'false',
    difficulty: g.difficulty ?? 'normal',
    'enable-command-block': 'false',
    'enable-query': 'false',
    'enable-rcon': 'true',
    'enable-status': 'true',
    'enforce-secure-profile': 'true',
    'enforce-whitelist': 'true',
    'entity-broadcast-range-percentage': '100',
    'force-gamemode': 'false',
    'function-permission-level': '2',
    gamemode: g.gameMode ?? 'survival',
    'generate-structures': 'true',
    hardcore: String(g.hardcore ?? false),
    'hide-online-players': 'false',
    'level-name': c?.levelName ?? 'world',
    'level-seed': '',
    'level-type': `minecraft\\:${g.levelType ?? 'normal'}`,
    'log-ips': 'false',
    'max-players': String(c?.maxPlayers ?? 10),
    'max-tick-time': '60000',
    motd: c?.motd ?? srv.name,
    'network-compression-threshold': '256',
    'online-mode': 'true',
    'op-permission-level': '4',
    'pause-when-empty-seconds': '60',
    'player-idle-timeout': '0',
    pvp: String(g.pvp ?? true),
    'rcon.password': fakeSha(srv.gamePort).slice(0, 24),
    'rcon.port': '25575',
    'server-ip': '',
    'server-port': '25565',
    'simulation-distance': '10',
    'spawn-protection': '16',
    'sync-chunk-writes': 'true',
    'use-native-transport': 'true',
    'view-distance': String(g.viewDistance ?? 10),
    'white-list': 'true',
  }
  if (pack) {
    Object.assign(values, {
      'require-resource-pack': String(pack.required),
      'resource-pack': `https\\://demo.playkeeper.io\\:8443/packs/${pack.sha1}.zip`,
      'resource-pack-id': uuidOf(pack.sha1),
      'resource-pack-prompt': '',
      'resource-pack-sha1': pack.sha1,
    })
  }
  const lines = Object.keys(values)
    .sort()
    .map((k) => `${k}=${values[k]}`)
  return `#Minecraft server properties\n#${javaDate(at)}\n${lines.join('\n')}\n`
}

const bukkitYml = `settings:
  allow-end: true
  warn-on-overload: true
  permissions-file: permissions.yml
  update-folder: update
  plugin-profiling: false
  connection-throttle: 4000
  query-plugins: true
  deprecated-verbose: default
  shutdown-message: Server closed
  minimum-api: none
  use-map-color-cache: true
spawn-limits:
  monsters: 70
  animals: 10
  water-animals: 5
  water-ambient: 20
  water-underground-creature: 5
  axolotls: 5
  ambient: 15
chunk-gc:
  period-in-ticks: 600
ticks-per:
  animal-spawns: 400
  monster-spawns: 1
  water-spawns: 1
  water-ambient-spawns: 1
  water-underground-creature-spawns: 1
  axolotl-spawns: 1
  ambient-spawns: 1
  autosave: 6000
aliases: now-in-commands.yml
`

const spigotYml = `# This is the main configuration file for Spigot.
# As you can see, there's tons to configure. Some options may impact gameplay, so use
# with caution, and make sure you know what each option does before configuring.

settings:
  debug: false
  bungeecord: false
  save-user-cache-on-stop-only: false
  sample-count: 12
  player-shuffle: 0
  user-cache-size: 1000
  moved-wrongly-threshold: 0.0625
  moved-too-quickly-multiplier: 10.0
  timeout-time: 60
  restart-on-crash: false
  netty-threads: 4
  log-villager-deaths: true
  log-named-deaths: true
messages:
  whitelist: You are not whitelisted on this server!
  unknown-command: Unknown command. Type "/help" for help.
  server-full: The server is full!
  outdated-client: Outdated client! Please use {0}
  outdated-server: Outdated server! I'm still on {0}
  restart: Server is restarting
advancements:
  disable-saving: false
  disabled:
  - minecraft:story/disabled
world-settings:
  default:
    view-distance: default
    simulation-distance: default
    mob-spawn-range: 8
    item-despawn-rate: 6000
    merge-radius:
      item: 0.5
      exp: -1.0
    entity-activation-range:
      animals: 32
      monsters: 32
      raiders: 64
      misc: 16
      water: 16
      villagers: 32
      flying-monsters: 32
stats:
  disable-saving: false
  forced-stats: {}
commands:
  tab-complete: 0
  send-namespaced: true
  log: true
config-version: 12
`

const paperGlobalYml = `# This is the global configuration file for Paper.
# As you can see, there's a lot to configure. Some options may impact gameplay, so use
# with caution, and make sure you know what each option does before configuring.
#
# The world configuration options are inside their respective world folder.
# The files are named paper-world.yml.
#
# Docs: https://docs.papermc.io/

_version: 31
chunk-loading-basic:
  player-max-chunk-generate-rate: -1.0
  player-max-chunk-load-rate: 100.0
  player-max-chunk-send-rate: 75.0
chunk-system:
  gen-parallelism: default
  io-threads: -1
  worker-threads: -1
console:
  enable-brigadier-completions: true
  enable-brigadier-highlighting: true
  has-all-permissions: false
messages:
  kick:
    authentication-servers-down: <lang:multiplayer.disconnect.authservers_down>
    connection-throttle: Connection throttled! Please wait before reconnecting.
  no-permission: <red>I'm sorry, but you do not have permission to perform this command.
  use-display-name-in-quit-message: false
misc:
  max-joins-per-tick: 5
  region-file-cache-size: 256
proxies:
  velocity:
    enabled: false
    online-mode: true
    secret: ''
watchdog:
  early-warning-delay: 10000
  early-warning-every: 5000
`

const paperWorldDefaultsYml = `# This is the world defaults configuration file for Paper.
# Settings here apply to every world, unless a world's paper-world.yml says otherwise.
#
# Docs: https://docs.papermc.io/

_version: 31
anticheat:
  anti-xray:
    enabled: false
chunks:
  auto-save-interval: default
  delay-chunk-unloads-by: 10s
  max-auto-save-chunks-per-tick: 24
entities:
  spawning:
    per-player-mob-spawns: true
    despawn-ranges:
      monster:
        hard: default
        soft: default
environment:
  optimize-explosions: false
  treasure-maps:
    enabled: true
fixes:
  disable-unloaded-chunk-enderpearl-exploit: true
hopper:
  cooldown-when-full: true
  disable-move-event: false
`

const paperWorldYml = (level: string, dimension: string) => `# This is a world configuration file for Paper.
# This file may start empty but can be filled with settings to override ones in the config/paper-world-defaults.yml
#
# World: ${level} (${dimension})

_version: 31
`

const commandsYml = `# This is the commands configuration file for Bukkit.
# For documentation on how to make use of this file, check out the Bukkit Wiki at
# https://bukkit.fandom.com/wiki/Commands.yml

command-block-overrides: []
ignore-vanilla-permissions: false
aliases:
  icanhasbukkit:
  - version $1-
`

const helpYml = `# This is the help configuration file for Bukkit.
# By default you do not need to modify this file. Help topics for all plugin commands are automatically provided by
# or extracted from your installed plugins. You only need to modify this file if you wish to add new help pages to
# your server or override the help pages of existing plugin commands.

general-topics:
  Rules:
    shortText: The rules of this server
    fullText: |
      Be kind. Build near spawn only with a friend's say-so.
      Ask before you take from a chest that isn't yours.
`

const eulaTxt = (at: number) => `#By changing the setting below to TRUE you are indicating your agreement to our EULA (https://aka.ms/MinecraftEULA).
#${javaDate(at)}
eula=true
`

/** A plugin's own settings, beside its jar in plugins/. */
const pluginFiles: Record<string, (at: number) => [string, string | number][]> = {
  chunky: () => [
    [
      'config.yml',
      `version: 2
language: en
continue-on-restart: false
force-load-existing-chunks: false
silent: false
update-interval: 1
`,
    ],
  ],
  luckperms: () => [
    [
      'config.yml',
      `####################################################################################################
# +----------------------------------------------------------------------------------------------+ #
# |                                     LuckPerms Configuration                                  | #
# +----------------------------------------------------------------------------------------------+ #
####################################################################################################

# The name of the server, used for server specific permissions.
server: global

# How the plugin should store data.
storage-method: h2

# Whether LuckPerms should apply wildcard permissions.
apply-wildcards: true

# Whether LuckPerms should automatically install translation bundles.
auto-install-translations: true
`,
    ],
    ['luckperms-h2-v2.mv.db', 81 * kb],
  ],
  coreprotect: () => [
    [
      'config.yml',
      `# CoreProtect Config

# If enabled, CoreProtect will check for updates when your server starts up.
check-updates: true

# If no radius is specified in a rollback or restore, this value will be
# used as the radius. Set to "0" to disable automatically adding a radius.
default-radius: 10

# The maximum radius that can be used in a command. Set to "0" to disable.
max-radius: 100

# If enabled, items taken from containers (etc) will be included in rollbacks.
rollback-items: true

# Logs blocks placed by players.
block-place: true

# Logs blocks broken by players.
block-break: true
`,
    ],
    ['database.db', 38.4 * mb],
  ],
  bluemap: () => [
    [
      'core.conf',
      `##                          ##
##         BlueMap          ##
##       Core-Config        ##
##                          ##

# By changing accept-download below to true you agree that BlueMap downloads
# resources from Mojang's servers to render the map (see Mojang's EULA).
accept-download: true

# How many threads BlueMap uses to render the maps.
render-thread-count: 1

# Whether BlueMap loads mod resources and data packs from the server's folders.
scan-for-mod-resources: true

# Where BlueMap keeps what it needs while it runs.
data: "bluemap"
`,
    ],
    [
      'webserver.conf',
      `# The web server BlueMap uses to show the map.
enabled: true
webroot: "bluemap/web"
port: 8100
`,
    ],
  ],
  ViaVersion: () => [
    [
      'config.yml',
      `# Thanks for downloading ViaVersion
# Ensure you look through all these options
#
#----------------------------------------------------------#
#                 GLOBAL OPTIONS                           #
#----------------------------------------------------------#
#
# Should ViaVersion check for updates?
check-for-updates: true
# Send the supported versions with the Status (Ping) response packet
send-supported-versions: false
# Versions to block, like "<1.20" or "1.21.4".
block-versions: []
# The message players on a blocked version see.
block-disconnect-msg: You are using an unsupported Minecraft version!
`,
    ],
  ],
  worldedit: (at) => [
    [
      'config.yml',
      `limits:
  allow-extra-data-values: false
  max-blocks-changed:
    default: -1
    maximum: -1
  max-radius: -1
  max-super-pickaxe-size: 5
  max-brush-radius: 6
use-inventory:
  enable: false
logging:
  log-commands: false
wand-item: minecraft:wooden_axe
shell-save-type: ''
history:
  size: 15
`,
    ],
    ['schematics/castle.schem', 412 * kb + (at % kb)],
    ['schematics/spawn-hub.schem', 186 * kb],
  ],
}

/** The settings of a plugin the demo's library doesn't know. */
const genericConfig = (name: string) => `# ${name} settings
enabled: true
`

/** A mod's own settings, in config/. */
const modFiles: Record<string, [string, string][]> = {
  cobblemon: [
    [
      'main.json',
      json({ maxLevel: 100, maxFriendship: 255, announceDropItems: true, defaultDropItemMethod: 'ON_ENTITY', ambientCryTicks: 1080, defaultBoxCount: 30, saveIntervalSeconds: 30, storageFormat: 'nbt', shinyRate: 8192.0, worldSliceDiameter: 8, worldSliceHeight: 16, exportSpawnConfig: false }),
    ],
  ],
  lithium: [
    [
      'lithium.properties',
      `# This is the configuration file for Lithium.
# This file exists for debugging purposes and should not be configured otherwise.
# Before configuring anything, take a backup of the worlds that will be opened.
#
# You can find information on editing this file and all the available options here:
# https://github.com/CaffeineMC/lithium/wiki/Configuration-File
#
# By default, this file will be empty except for this notice.
`,
    ],
  ],
}

const friendsWelcome = `# Greets friends when they join.
welcome: "&aWelcome back, {player}! &7{online} friends are on."
first-join: "&6Say hi to {player}, who's new here!"
sound: ENTITY_PLAYER_LEVELUP
`

/** An out-of-memory crash, as the server writes it to crash-reports/. */
function crashReport(srv: ServerStatus, at: number, players: string[], mods: string[]): string {
  const heapMB = srv.config?.heapMB ?? 3072
  const heap = heapMB * mb
  const stamp = iso(at).slice(0, 19).replace('T', ' ')
  return `---- Minecraft Crash Report ----
// Don't be sad, have a hug! <3

Time: ${stamp}
Description: Exception in server tick loop

java.lang.OutOfMemoryError: Java heap space
\tat java.base/java.util.Arrays.copyOf(Arrays.java:3541)
\tat net.minecraft.world.level.chunk.storage.RegionFileStorage.write(RegionFileStorage.java:123)
\tat net.minecraft.server.MinecraftServer.tickServer(MinecraftServer.java:1021)
\tat net.minecraft.server.MinecraftServer.runServer(MinecraftServer.java:812)


A detailed walkthrough of the error, its code path and all known details is as follows:
---------------------------------------------------------------------------------------

-- System Details --
Details:
\tMinecraft Version: ${srv.config?.minecraftVersion ?? ''}
\tOperating System: Linux (amd64)
\tJava Version: 25, Eclipse Adoptium
\tMemory: ${Math.round(heap * 0.004)} bytes / ${heap} bytes (${heapMB} MiB) up to ${heap} bytes
\tJVM Flags: 2 total; -Xms${heapMB}M -Xmx${heapMB}M
\tMods:
${mods.map((m) => `\t\t${m}`).join('\n')}
\tPlayer Count: ${players.length} / ${srv.config?.maxPlayers ?? 10}; [${players.join(', ')}]
`
}

/** Region files named for the area around spawn, sharing bytes between them. */
function regions(tree: Tree, folder: string, bytes: number, count: number, modified: number, seed: number) {
  const side = Math.ceil(Math.sqrt(count))
  let n = 0
  for (let x = -Math.floor(side / 2); n < count; x++) {
    for (let z = -Math.floor(side / 2); z < Math.ceil(side / 2) && n < count; z++, n++) {
      const share = 0.55 + noise(seed + n) * 0.9
      tree[`${folder}/r.${x}.${z}.mca`] = { size: Math.round((bytes / count) * share), modified: modified - Math.round(noise(seed + n + 50) * 20 * minute) }
    }
  }
}

/** A world folder as the game keeps it: the level, its regions, and what it knows of each player. */
function world(tree: Tree, s: DemoState, srv: ServerStatus, level: string, modified: number, created: number) {
  const bytes = srv.worldBytes ?? 0.1 * 1024 * mb
  const paper = addonKind(srv.type) === 'plugins'
  const overworld = paper ? 0.84 : 0.8
  tree[`${level}/level.dat`] = { size: 5_212 + (srv.gamePort % 700), modified }
  tree[`${level}/level.dat_old`] = { size: 5_190 + (srv.gamePort % 700), modified: modified - 5 * minute }
  tree[`${level}/session.lock`] = { size: 3, modified: Date.parse(srv.startedAt ?? srv.createdAt) }
  regions(tree, `${level}/region`, bytes * overworld, Math.max(4, Math.round(bytes / (48 * mb))), modified, srv.gamePort)
  regions(tree, `${level}/entities`, bytes * 0.06, 4, modified, srv.gamePort + 7)
  regions(tree, `${level}/poi`, bytes * 0.03, 4, modified, srv.gamePort + 13)
  for (const name of ['raids.dat', 'random_sequences.dat', 'scoreboard.dat']) tree[`${level}/data/${name}`] = { size: 180 + (seedOf(name) % 900), modified }
  for (const p of s.roster[srv.id] ?? []) {
    const uuid = uuidOf(p.name)
    const seen = Date.parse(p.lastSeen)
    tree[`${level}/playerdata/${uuid}.dat`] = { size: 3_400 + (seedOf(p.name) % 9_000), modified: p.online ? modified : seen }
    tree[`${level}/playerdata/${uuid}.dat_old`] = { size: 3_380 + (seedOf(p.name) % 9_000), modified: (p.online ? modified : seen) - 5 * minute }
    tree[`${level}/stats/${uuid}.json`] = {
      text: `${JSON.stringify({ stats: { 'minecraft:custom': { 'minecraft:play_time': p.playtimeSeconds * 20, 'minecraft:leave_game': p.sessions, 'minecraft:jump': Math.round(p.playtimeSeconds / 9), 'minecraft:deaths': Math.round(p.playtimeSeconds / 20_000) } }, DataVersion: 4671 })}\n`,
      modified: p.online ? modified : seen,
    }
    tree[`${level}/advancements/${uuid}.json`] = {
      text: json({ 'minecraft:story/root': { criteria: { crafting_table: iso(created + day) }, done: true }, 'minecraft:story/mine_stone': { criteria: { get_stone: iso(created + day + hour) }, done: true }, DataVersion: 4671 }),
      modified: p.online ? modified : seen,
    }
  }
  for (const pack of s.packs[srv.id]?.data ?? []) tree[`${level}/datapacks/${pack.name}`] = { size: pack.size, modified: Date.parse(pack.addedAt) }
  const nether = paper ? `${level}_nether/DIM-1` : `${level}/DIM-1`
  const end = paper ? `${level}_the_end/DIM1` : `${level}/DIM1`
  regions(tree, `${nether}/region`, bytes * 0.08, 4, modified, srv.gamePort + 21)
  regions(tree, `${end}/region`, bytes * 0.02, 2, modified, srv.gamePort + 29)
  if (paper) {
    tree[`${level}/uid.dat`] = { size: 16, modified: created }
    tree[`${level}/paper-world.yml`] = { text: paperWorldYml(level, 'minecraft:overworld'), modified: created }
    for (const [dim, key] of [
      [`${level}_nether`, 'minecraft:the_nether'],
      [`${level}_the_end`, 'minecraft:the_end'],
    ] as const) {
      tree[`${dim}/level.dat`] = { size: 4_980, modified }
      tree[`${dim}/uid.dat`] = { size: 16, modified: created }
      tree[`${dim}/paper-world.yml`] = { text: paperWorldYml(dim, key), modified: created }
    }
  }
}

/** Gives each folder the time of the newest thing in it. */
function folders(tree: Tree) {
  for (const [p, f] of Object.entries(tree)) {
    for (let up = parentOf(p); up; up = parentOf(up)) {
      const folder = (tree[up] ??= { folder: true, modified: 0 })
      folder.modified = Math.max(folder.modified, f.modified)
    }
  }
}

/** A server's folder as it is when its Files tab first opens. */
export function sampleFiles(s: DemoState, srv: ServerStatus, now: number): Tree {
  const tree: Tree = {}
  const created = Date.parse(srv.createdAt)
  const online = srv.phase === 'online'
  const lastWrite = online ? now - 40_000 : Date.parse(srv.stoppedAt ?? srv.startedAt ?? srv.createdAt)
  const started = Date.parse(srv.startedAt ?? srv.stoppedAt ?? srv.createdAt)
  const level = srv.config?.levelName ?? 'world'
  const version = srv.config?.minecraftVersion ?? ''
  const paper = addonKind(srv.type) === 'plugins'
  const text = (path: string, body: string, modified: number) => (tree[path] = { text: body, modified })
  const blob = (path: string, size: number, modified: number) => (tree[path] = { size: Math.round(size), modified })

  text('server.properties', properties(s, srv, started), started)
  text('eula.txt', eulaTxt(created), created)
  const whitelist = s.whitelist[srv.id] ?? []
  const ops = s.operators[srv.id] ?? []
  text('whitelist.json', json(whitelist.map((p) => ({ uuid: uuidOf(p.name), name: p.name }))), started)
  text('ops.json', json(ops.map((p) => ({ uuid: uuidOf(p.name), name: p.name, level: p.level, bypassesPlayerLimit: false }))), started)
  text('banned-players.json', '[]\n', created)
  text('banned-ips.json', '[]\n', created)
  const known = [...new Set([...(s.roster[srv.id] ?? []).map((p) => p.name), ...whitelist.map((p) => p.name)])]
  text('usercache.json', `${JSON.stringify(known.map((name) => ({ name, uuid: uuidOf(name), expiresOn: new Date(now + 30 * day).toISOString().slice(0, 19).replace('T', ' ') + ' +0000' })))}\n`, lastWrite)

  const log = (s.logs[srv.id] ?? []).map((l) => l.text)
  text('logs/latest.log', log.length ? `${log.join('\n')}\n` : '', lastWrite)
  for (let n = 1; n <= 3; n++) {
    const at = started - n * day
    if (at > created) blob(`logs/${iso(at).slice(0, 10)}-1.log.gz`, 6 * kb + noise(at) * 30 * kb, at + 18 * hour)
  }

  if (paper) {
    const build = srv.config?.paperBuild ?? 0
    blob(`${srv.type ?? 'paper'}-${version}-${build}.jar`, 51.4 * mb, Date.parse(srv.config?.jarVerifiedAt ?? srv.createdAt))
    text('version_history.json', `${JSON.stringify({ currentVersion: `${version}-${build} (MC: ${version})` })}\n`, started)
    text('bukkit.yml', bukkitYml, created)
    text('spigot.yml', spigotYml, created)
    text('commands.yml', commandsYml, created)
    text('help.yml', helpYml, created + 2 * day)
    text('permissions.yml', '', created)
    text('config/paper-global.yml', paperGlobalYml, created)
    text('config/paper-world-defaults.yml', paperWorldDefaultsYml, created)
    blob(`cache/mojang_${version}.jar`, 57.8 * mb, created)
    blob(`versions/${version}/paper-${version}.jar`, 61.2 * mb, created)
    blob('libraries/com/google/guava/guava/33.4.8-jre/guava-33.4.8-jre.jar', 3.1 * mb, created)
    blob('libraries/net/kyori/adventure-api/4.24.0/adventure-api-4.24.0.jar', 0.6 * mb, created)
  } else {
    blob(`fabric-server-mc.${version}-loader.0.17.2-launcher.1.1.0.jar`, 184 * kb, created)
    blob(`versions/${version}/server-${version}.jar`, 57.8 * mb, created)
    blob('libraries/net/fabricmc/fabric-loader/0.17.2/fabric-loader-0.17.2.jar', 1.4 * mb, created)
  }

  const folder = paper ? 'plugins' : 'mods'
  tree[folder] = { folder: true, modified: created }
  const mods: string[] = []
  for (const inst of s.addons[srv.id]?.installed ?? []) {
    const a = library.find((x) => x.source === inst.source && x.projectId === inst.projectId)
    if (!a) continue
    blob(`${folder}/${a.jar.replace('{v}', inst.version)}`, a.size, inst.installedAt)
    mods.push(`${a.slug}: ${a.name} ${inst.version}`)
    if (a.mod) {
      for (const [name, body] of modFiles[a.slug] ?? []) text(`config/${a.folder}/${name}`, body, inst.installedAt + minute)
      continue
    }
    const files = pluginFiles[a.slug]?.(inst.installedAt) ?? [['config.yml', genericConfig(a.name)]]
    for (const [name, body] of files) {
      const path = `plugins/${a.folder}/${name}`
      if (typeof body === 'string') text(path, body, inst.installedAt + minute)
      else blob(path, body, lastWrite)
    }
  }
  for (const h of s.addons[srv.id]?.byHand ?? []) {
    blob(`${folder}/${h.fileName}`, h.size, created + 5 * day)
    if (paper) text(`plugins/${h.name}/config.yml`, h.name === 'FriendsWelcome' ? friendsWelcome : genericConfig(h.name), created + 5 * day)
  }
  if (paper && srv.slug === 'survival') blob('server-icon.png', 5_832, created + 3 * day)

  if (srv.crash) {
    const at = Date.parse(srv.crash.at)
    const players = (s.roster[srv.id] ?? []).filter((p) => Date.parse(p.lastSeen) >= at - minute).map((p) => p.name)
    const stamp = iso(at).slice(0, 19).replace('T', '_').replace(/:/g, '.')
    text(`crash-reports/crash-${stamp}-server.txt`, crashReport(srv, at, players, mods), at)
  }

  world(tree, s, srv, level, lastWrite, created)
  folders(tree)
  return tree
}
