// Mirrors internal/api/types.go and the panel's own responses. Keep both in sync.

export type Phase =
  | 'not_created'
  | 'stopped'
  | 'pulling_image'
  | 'starting_container'
  | 'downloading_server'
  | 'starting'
  | 'preparing_world'
  | 'online'
  | 'stopping'
  | 'crashed'
  | 'docker_unavailable'

export interface PlayerSnapshot {
  online: number
  max: number
  names: string[]
  source: string
  at: string
}

export interface Resources {
  cpuPercent?: number
  /** Ticks per second over the last minute; 20 is full speed. */
  tps?: number
  memBytes?: number
  memLimitBytes?: number
  diskFreeBytes?: number
  diskTotalBytes?: number
  at: string
}

export type Difficulty = 'peaceful' | 'easy' | 'normal' | 'hard'
export type GameMode = 'survival' | 'creative' | 'adventure' | 'spectator'
export type LevelType = 'normal' | 'flat' | 'amplified' | 'large_biomes'
export type PlayStyle = 'friends' | 'creative' | 'hardcore' | 'solo'

export interface Gameplay {
  difficulty?: Difficulty
  pvp?: boolean
  gameMode?: GameMode
  hardcore?: boolean
  viewDistance?: number
  levelType?: LevelType
}

export interface ServerConfig {
  type?: string
  versionId: string
  minecraftVersion: string
  paperBuild: number
  jarSha256?: string
  memoryMB: number
  heapMB: number
  levelName: string
  jarVerifiedAt?: string
  motd: string
  maxPlayers: number
  whitelist: boolean
  eulaAcceptedAt: string
  eulaAcceptedBy: string
  createdAt: string
  image: string
  playStyle?: PlayStyle | ''
  gameplay?: Gameplay
  iconUpdatedAt?: string
  /** The exact software of a type other than Paper. */
  software?: SoftwarePin
  /** The pack the server was created from. */
  modpack?: ServerModpack
  /** The template the server was created from. */
  template?: ServerTemplate
}

export interface Operation {
  id: string
  serverId?: string
  kind: string
  status: 'running' | 'succeeded' | 'failed'
  phase: string
  actor: string
  startedAt: string
  finishedAt?: string
  error?: string
  hint?: string
  detail?: Record<string, unknown>
}

export interface Backup {
  id: string
  serverId: string
  kind: 'manual' | 'rollback'
  createdAt: string
  fileName: string
  sizeBytes: number
  sha256: string
  location: string
  verified?: boolean
  verifiedAt?: string
  verifyError?: string
  downtimeMs: number
  minecraftVersion: string
  levelName: string
  fileCount: number
  createdBy: string
  downloadedAt?: string
  note?: string
}

export interface FirstSteps {
  invited?: string
  friendJoined?: string
  friendJoinedAt?: string
  backedUp: boolean
  downloaded: boolean
}

export interface ServerStatus {
  id: string
  name: string
  slug: string
  game: string
  type: string
  createdAt: string
  machineId?: string
  exists: boolean
  desired: 'running' | 'stopped'
  phase: Phase
  phaseDetail?: string
  reachable: boolean
  reachableAt?: string
  startedAt?: string
  stoppedAt?: string
  exitCode?: number
  players?: PlayerSnapshot
  config?: ServerConfig
  gameplay: Gameplay
  gamePort: number
  lastError?: string
  lastErrorHint?: string
  offlineModeTest: boolean
  operation?: Operation
  lastOperation?: Operation
  crashCount: number
  resources?: Resources
  lastBackup?: Backup
  /** The world's size on disk, measured every few minutes. */
  worldBytes?: number
  pendingRestart: boolean
  collectingSince?: string
  firstSteps: FirstSteps
  /** The file that stopped the last start, while the server stays stopped. */
  refusal?: FileRefusal
  /** Set when the server's software no longer matches what Playkeeper installed. */
  softwareChanged?: SoftwareChange
}

/** A file in the server's folder that Playkeeper would not follow or change. */
export interface FileRefusal {
  code: 'link' | 'special_file' | 'not_a_file' | 'not_a_folder' | 'too_large' | 'too_many_entries' | 'changed' | 'bad_name'
  params: { path: string; type?: string; limit?: string }
  message: string
  hint?: string
}

export interface PreflightCheck {
  id: string
  label: string
  status: 'pass' | 'warn' | 'fail' | 'info'
  detail: string
  fix?: string
}

export interface Preflight {
  ok: boolean
  checks: PreflightCheck[]
}

export interface Machine {
  hostname: string
  os: string
  arch: string
  cpus: number
  cpuPercent?: number
  memoryTotalMB: number
  systemReserveMB: number
  serversMemoryMB: number
  memoryFreeMB: number
  diskFreeBytes?: number
  diskTotalBytes?: number
  diskWarning?: PreflightCheck
  docker: boolean
  dockerVersion?: string
  agentVersion: string
  defaultGamePort: number
  offlineModeTest: boolean
  operation?: Operation
  updateAvailable?: string
  updateInstalling?: string
  servers: number
}

export interface ApiErrorBody {
  error: string
  code: string
  hint?: string
  operation?: Operation
}

/** A machine the panel manages, with what its agent reports now. */
export interface MachineView {
  id: string
  projectId: string
  name: string
  kind: 'local' | 'remote'
  live?: Machine
  error?: ApiErrorBody
}

export interface Project {
  id: string
  name: string
  role: string
}

export interface UpdateResult {
  from: string
  to: string
  outcome: 'updated' | 'rolled_back' | 'refused' | 'failed'
  error?: string
  finishedAt: string
}

export interface UpdateInfo {
  current: string
  supported: boolean
  reason?: string
  latest?: string
  available: boolean
  notes?: string
  releaseDate?: string
  checkedAt?: string
  checkError?: string
  installing?: string
  lastResult?: UpdateResult
}

export interface CatalogEntry {
  id: string
  label: string
  minecraftVersion: string
  paperBuild: number
  jarSha256: string
  java: number
  recommended: boolean
  notes: string
  channel: string
  experimental: boolean
  supported: boolean
  /** When Mojang released the Minecraft version. */
  releasedAt?: string
  software?: SoftwarePin
  build?: string
}

export interface ServerType {
  id: string
  name: string
  available: boolean
  check?: SoftwareCheck
}

export interface ServerMemory {
  id: string
  name: string
  memoryMB: number
  running: boolean
}

export interface Catalog {
  type: string
  types: ServerType[]
  versions: CatalogEntry[]
  versionsError?: string
  versionsCheckedAt?: string
  memoryOptionsMB: number[]
  recommendedMemoryMB: number
  hostMemoryMB: number
  maxMemoryMB: number
  systemReserveMB: number
  memoryFreeMB: number
  servers: ServerMemory[]
  suggestedPort?: number
  image: string
  /** The newest Minecraft release Mojang lists, whether or not the type offers it yet. */
  latestRelease?: string
}

export interface LogLine {
  seq: number
  ts: string
  text: string
}

export interface LogsResponse {
  epoch: string
  lines: LogLine[]
  next: number
  truncated: boolean
}

export interface WhitelistEntry {
  name: string
  uuid?: string
}

export interface OperatorEntry {
  name: string
  uuid?: string
  level: number
}

export type BucketState = 'online' | 'offline' | 'no_data' | 'not_collected'

export interface MetricsBucket {
  start: string
  playersMax: number | null
  cpuAvg: number | null
  memAvg: number | null
  coverage: number
  state: BucketState
}

export interface Gap {
  from: string
  to: string
  kind: 'collector_down' | 'server_offline'
}

export interface MetricsResponse {
  from: string
  to: string
  bucketSeconds: number
  sampleIntervalSeconds: number
  buckets: MetricsBucket[]
  gaps: Gap[]
  collectingSince?: string
  source: string
}

export interface Session {
  id: number
  player: string
  uuid?: string
  start: string
  end?: string
  endReason?: string
  startUncertain: boolean
  endUncertain: boolean
  durationSeconds: number
  source: string
}

export interface SessionsResponse {
  from: string
  to: string
  sessions: Session[]
}

export interface DailyActivity {
  date: string
  uniquePlayers: number
  sessions: number
  playtimeSeconds: number
  playtimeLowerBound: boolean
  playtimeUpperBound: boolean
  coverage: number
}

export interface PlayerStat {
  name: string
  uuid?: string
  lastSeen: string
  online: boolean
  sessions: number
  playtimeSeconds: number
  /** A session's start or end wasn't seen, so the playtime is an estimate. */
  playtimeUncertain?: boolean
}

export interface PlayersSummary {
  tz: string
  days: DailyActivity[]
  players: PlayerStat[]
  observedSessions: number
  uncertainSessions: number
  collectingSince?: string
  retentionDays: number
}

export type ActivityKind =
  | 'joined'
  | 'crashed'
  | 'created'
  | 'restored'
  | 'version'
  | 'stopped_outside'
  | 'allowlisted'
  | 'unlisted'
  | 'operator'
  | 'deoperator'
  | 'kicked'
  | 'backup'
  | 'downloaded'
  | 'started'
  | 'stopped'
  | 'restarted'
  | 'settings'

export interface Activity {
  ts: string
  serverId?: string
  kind: ActivityKind
  player?: string
  actor?: string
  detail?: string
}

export interface ManifestSummary {
  createdAt: string
  playkeeperVersion: string
  minecraftVersion: string
  paperBuild: number
  versionId: string
  levelName: string
  fileCount: number
  totalBytes: number
  sourceInstall: string
  settings: Record<string, string>
  type?: string
  build?: string
}

export interface RestorePreview {
  id: string
  serverId?: string
  source: string
  receivedAt: string
  sizeBytes: number
  sha256: string
  manifest?: ManifestSummary
  compatible: boolean
  problems: string[]
  warnings: string[]
  currentWorld: { exists: boolean; levelName?: string; sizeBytes: number }
  willCreateRollback: boolean
  needsEula: boolean
  memoryMB: number
  confirmPhrase: string
  steps: string[]
  notRestored: string[]
}

export interface AuditEntry {
  id: number
  serverId?: string
  ts: string
  actor: string
  action: string
  target?: string
  result: string
  detail?: string
  source: 'panel' | 'agent'
}

export interface Me {
  user: { username: string; role: string }
  csrfToken: string
  expiresAt: string
  idleTimeoutSeconds: number
  version: string
}

// Wave 1: plugins and mods, map pre-generation, resource and data packs.

export type AddonSource = 'modrinth' | 'hangar'

export interface AddonTarget {
  kind: 'plugin' | 'mod'
  folder: string
  sources: AddonSource[]
  categories: string[]
  minecraftVersion: string
}

/** A message from the add-on library: kind and params pick the wording, message is the English text. */
export interface AddonNotice {
  kind: string
  params?: Record<string, string>
  message: string
  hint?: string
  url?: string
}

export interface AddonVersion {
  versionId: string
  versionNumber: string
  channel: string
  published: string
  fileName?: string
  size?: number
  externalUrl?: string
}

export interface Addon {
  source: AddonSource
  projectId: string
  slug: string
  name: string
  summary?: string
  iconUrl?: string
  versionId: string
  versionNumber: string
  channel: string
  published: string
  fileName: string
  size: number
  dependencyOf?: string
  installedAt: string
}

export interface AddonKey {
  source: AddonSource
  projectId: string
}

export interface AddonFile {
  fileName: string
  size: number
  status: 'managed' | 'modified' | 'identified' | 'unknown'
  addon?: Addon
  name?: string
  version?: string
  pending?: boolean
}

export interface Addons {
  target: AddonTarget
  files: AddonFile[]
  missing: Addon[]
  warnings: AddonNotice[]
  restartNeeded: boolean
}

export interface AddonUpdate {
  source: AddonSource
  projectId: string
  latest?: AddonVersion
  available: boolean
  notice?: AddonNotice
}

export interface AddonChecks {
  updates: AddonUpdate[]
  identified: AddonFile[]
  checkedAt: string
}

export interface AddonCard {
  source: AddonSource
  projectId: string
  slug: string
  name: string
  author?: string
  summary: string
  categories: string[]
  license?: string
  downloads: number
  iconUrl?: string
  updated: string
  pageUrl: string
  installed: boolean
}

export interface AddonBrowse {
  cards: AddonCard[]
  more: boolean
  unanswered: AddonNotice[]
}

export interface AddonStep {
  action: 'install' | 'update'
  source: AddonSource
  projectId: string
  name: string
  versionNumber: string
  channel: string
  fileName: string
  size: number
  neededBy?: string
  was?: string
}

export interface AddonPlan {
  steps: AddonStep[]
  manual: AddonNotice[]
  blockers: AddonNotice[]
  warnings: AddonNotice[]
  ready: boolean
  fingerprint: string
}

export interface AddonDetails {
  card: AddonCard
  latest?: AddonVersion
  notes?: string
  notice?: AddonNotice
  installed?: Addon
  changed?: boolean
  missing?: boolean
  updateAvailable?: boolean
  plan?: AddonPlan
  planError?: AddonNotice
}

/** One file of an add-on install or update, from the operation's detail. */
export interface AddonProgress {
  name: string
  versionNumber: string
  was?: string
  neededBy?: string
  size: number
  received: number
  state: 'waiting' | 'downloading' | 'verified' | 'failed'
}

export interface AddonRemovePreview {
  addon: Addon
  neededBy: string[]
  orphans: Addon[]
  configFolder?: string
  changed: boolean
  missing: boolean
}

export interface AddonRemoval {
  removed: string[]
  warnings: AddonNotice[]
}

export type PregenPresetId = 'small' | 'medium' | 'large' | 'huge'

export interface PregenPreset {
  id: PregenPresetId
  radius: number
  chunks: number
  seconds: number
  diskBytes: number
  fits: boolean
}

export interface Pregen {
  state: 'idle' | 'starting' | 'running' | 'paused' | 'finished'
  step?: 'installing' | 'restarting' | 'starting_server' | 'starting_task'
  world: string
  preset?: PregenPresetId
  radius?: number
  chunks: number
  total: number
  percent: number
  rate?: number
  etaSeconds: number
  elapsedSeconds?: number
  pausedBy?: 'user' | 'players' | 'server'
  pausedFor?: string
  pauseForPlayers: boolean
  startedAt?: string
  finishedAt?: string
  diskBytes?: number
  installed: boolean
  presets: PregenPreset[]
  diskFreeBytes?: number
  error?: string
}

export interface ResourcePackOffer {
  sha1: string
  fileName: string
  size: number
  description?: string
  icon?: boolean
  addedAt: string
  url: string
  required: boolean
  prompt?: string
}

export interface ResourcePack {
  offer?: ResourcePackOffer
  pending: boolean
}

export interface DataPack {
  name: string
  description?: string
  icon?: boolean
  size: number
  enabled?: boolean
  folder?: boolean
  addedAt: string
}

export interface DataPacks {
  packs: DataPack[]
  live: boolean
  added?: string
  notEnabled?: boolean
  problem?: string
}

// Wave 4: every server type.

/** How a type's downloads are verified. */
export type SoftwareCheck = 'full' | 'weak_hash' | 'recorded_outputs'

export interface SoftwarePin {
  type: string
  minecraftVersion: string
  purpurBuild?: number
  fabricLoader?: string
  quiltLoader?: string
  neoforgeVersion?: string
}

export interface SoftwareBuild {
  version: string
  channel: string
  recommended: boolean
}

export interface SoftwareBuilds {
  type: string
  minecraftVersion: string
  builds: SoftwareBuild[]
  checkedAt: string
}

export interface SoftwareChange {
  file: string
  algorithm: string
  recorded: string
  found?: string
  installedAt?: string
  changedAt?: string
  detectedAt: string
  software: string
}

// Wave 4: modpacks.

export type ModpackSource = 'modrinth' | 'curseforge'

export interface ModpackCard {
  source: ModpackSource
  projectId: string
  slug: string
  name: string
  author?: string
  summary: string
  downloads: number
  iconUrl?: string
  updated: string
  pageUrl: string
  types: string[]
  minecraftVersions: string[]
  /** The mods the newest version bundles, and the memory suggested for them; 0 when unknown. */
  mods?: number
  memoryMB?: number
  unavailable?: AddonNotice
}

export interface ModpackResults {
  cards: ModpackCard[]
  total: number
  offset: number
  limit: number
  sources: ModpackSource[]
}

export interface ModpackVersion {
  id: string
  number: string
  name?: string
  channel: string
  published: string
  size: number
  type?: string
  minecraftVersion?: string
  mods?: number
  unsupported?: AddonNotice
}

export interface ModpackDetail extends ModpackCard {
  sourceUrl?: string
  issuesUrl?: string
  wikiUrl?: string
  headline?: string
  versions: ModpackVersion[]
  newest?: string
}

export interface ModpackPreview {
  type: string
  minecraftVersion: string
  loaderVersion?: string
  /** Set when the pack's Minecraft version runs on an older Java than the newest versions. */
  java?: number
  files: number
  downloadSize: number
  ready: boolean
  blockers: AddonNotice[]
  warnings: AddonNotice[]
  manual: AddonNotice[]
}

export interface ModpackRef {
  source: ModpackSource
  projectId: string
  versionId: string
}

// Wave 4: sharing a modded server's pack with friends.

/** Text from the share: key and params pick the wording, text is the English. */
export interface ShareText {
  key: string
  params?: Record<string, string>
  text: string
}

export type ShareNeed = 'required' | 'optional' | 'server_only' | 'unknown'

export interface SharePack {
  name: string
  version: string
  source: string
  page?: string
  need: ShareNeed
  label: ShareText
}

export interface ShareMod {
  name: string
  version?: string
  path: string
  from: 'user' | 'pack'
  source?: string
  project?: string
  dependencyOf?: string
  page?: string
  onServer: boolean
  need: ShareNeed
  label: ShareText
  inFile: boolean
  byHand?: boolean
}

/** A mod friends get by hand, because the file can't link it. */
export interface ShareYourself {
  name: string
  path: string
  page?: string
  need: ShareNeed
  reason: ShareText
}

/** What friends get from a server, server-only mods included. */
export interface FriendsShare {
  server: string
  type: string
  minecraftVersion: string
  loaderVersion: string
  pack?: SharePack
  notice: ShareText
  mods: ShareMod[]
  yourself?: ShareYourself[]
}

/** A server's friends' pack; token is set while the page is shared. */
export interface PackShare {
  public: boolean
  token?: string
  file: string
  size: number
  loaderName: string
  share: FriendsShare
}

export interface PackPageMod {
  name: string
  version?: string
  from: 'user' | 'pack'
  page?: string
  need: ShareNeed
  label: ShareText
  inFile: boolean
  neededBy?: string
}

export interface PackLauncher {
  id: string
  name: string
  site: string
  steps: ShareText[]
}

/** The public /packs/<token> page: only what friends get. */
export interface PackPage {
  server: string
  minecraftVersion: string
  loader: string
  loaderName: string
  loaderVersion: string
  pack?: SharePack
  notice: ShareText
  steps: ShareText[]
  launchers: PackLauncher[]
  mods: PackPageMod[]
  yourself?: ShareYourself[]
  download: { url: string; name: string; size: number; type: string }
  address?: string
  hasIcon: boolean
}

export interface ServerModpack {
  source: ModpackSource
  projectId: string
  versionId: string
  name: string
  versionNumber: string
  pageUrl?: string
  iconUrl?: string
  mods?: number
  pending?: boolean
}

// Wave 4: templates.

export interface ServerTemplate {
  name: string
  /** Set until the template's add-ons are on the server; the next start installs the rest. */
  pending?: boolean
}

export interface TemplateSettings {
  difficulty?: Difficulty
  pvp?: boolean
  gameMode?: GameMode
  hardcore?: boolean
  viewDistance?: number
  levelType?: LevelType
  maxPlayers?: number
  motd?: string
  playStyle?: PlayStyle
  memoryMB?: number
}

export interface TemplateAddon {
  source: string
  name: string
  /** The pinned version; missing means the newest that fits. */
  versionNumber?: string
}

export interface TemplateContents {
  name: string
  type: string
  minecraftVersion: string
  build?: string
  settings: TemplateSettings
  addons: TemplateAddon[]
  modpack?: TemplateAddon
  resourcePacks: number
  dataPacks: number
  /** The packs' names, the resource pack first. */
  packs: string[]
}

export interface TemplateExport {
  fileName: string
  /** The template file's text. */
  file: string
  /** Empty when the template is too large for a link. */
  link: string
  /** Set when some chats would cut the link. */
  linkLong?: boolean
  contents: TemplateContents
  /** What the template carries with every part included. */
  available: TemplateContents
  /** The server's packs, counting those that can't travel. */
  packsHere: number
  leftOut: AddonNotice[]
  notes: AddonNotice[]
}

export interface TemplatePlan {
  contents: TemplateContents
  /** What the new server runs, which can differ from what the template names. */
  type: string
  versionId?: string
  minecraftVersion?: string
  build?: string
  experimental?: boolean
  memoryMB: number
  skipped: AddonNotice[]
  warnings: AddonNotice[]
  blockers: AddonNotice[]
  ready: boolean
  fingerprint: string
}
