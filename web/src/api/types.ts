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
  /** Set while the server's machine can't be reached: the status is the one it last sent, at this time. */
  lastKnownAt?: string
  /** Two joined machines list this server, so the dashboard sends its requests to neither. */
  disputed?: boolean
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
  params?: Record<string, string>
  operation?: Operation
}

export type LinkState = 'connected' | 'offline' | 'waiting' | 'removed'

export type LinkProblemCode = 'machine_offline' | 'machine_never_connected' | 'link_slow' | 'clock_skew' | 'version_mismatch' | 'link_unstable' | 'machine_cloned'

/** Something wrong with a machine's link; message and hint are the English text. */
export interface LinkProblem {
  code: LinkProblemCode
  params?: Record<string, string>
  message: string
  hint?: string
}

/** A joined machine's link, as the dashboard sees it. */
export interface MachineLink {
  machineId: string
  name: string
  fingerprint: string
  state: LinkState
  connectedAt?: string
  /** Its last heartbeat while connected, or when it went away. */
  lastSeen?: string
  rttMs?: number
  version?: string
  address?: string
  problems: LinkProblem[]
}

/** A machine the panel manages, with what its agent reports now. */
export interface MachineView {
  id: string
  projectId: string
  name: string
  kind: 'local' | 'remote'
  live?: Machine
  error?: ApiErrorBody
  link?: MachineLink
  /** The address a joined machine's command dialed. */
  dials?: string
  joinedAt?: string
  joinedFrom?: string
  /** Who made the code it joined with. */
  addedBy?: string
}

/** An address another machine can dial to reach this dashboard. */
export interface DialAddress {
  kind: 'name' | 'ip'
  address: string
  /** The name points at a proxy, which stops the machine checking the fingerprint. */
  proxied?: boolean
}

export type JoinCodeState = 'waiting' | 'used' | 'expired'

/** A join code without the code itself, which only its maker sees, once. */
export interface JoinCode {
  id: string
  name?: string
  dials?: string
  createdAt: string
  expiresAt: string
  createdBy?: string
  state: JoinCodeState
  machineId?: string
}

export interface JoinCommand extends JoinCode {
  code: string
  install: string
  join: string
  installLines: string[]
  joinLines: string[]
}

/** What connecting a machine needs: where it dials, the smallest machine that works and the codes. */
export interface MachineLinkInfo {
  addresses: DialAddress[]
  minimum: { cores: number; memoryGB: number; freeDiskGB: number }
  sizingUrl: string
  available: boolean
  fingerprint?: string
  /** Joining is paused for this many seconds after too many wrong codes. */
  joinPausedSeconds?: number
  /** Only for accounts that may connect machines. */
  codes?: JoinCode[]
}

export interface MachineEvent {
  at: string
  kind: string
  actor?: string
  address?: string
  code?: string
}

export type TokenRole = 'viewer' | 'moderator' | 'admin'

export interface ApiToken {
  id: string
  name: string
  role: TokenRole
  allServers: boolean
  servers: string[]
  createdAt: string
  expiresAt: string
  lastUsedAt?: string
  /** The account the token acts for; owners see every account's. */
  account: string
  mine: boolean
}

export interface NewToken {
  token: ApiToken
  /** Shown this once; the dashboard keeps only its hash. */
  secret: string
}

/** A token's calls of one tool on one server, each within 10 minutes of the one before. */
export interface AgentActivity {
  tokenId: string
  tokenName: string
  tool: string
  serverId?: string
  serverName?: string
  count: number
  at: string
}

/** What a route asks the panel's permit for. */
export type Action = 'view' | 'servers.manage' | 'machine.manage' | 'account.manage' | 'audit.view'

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
}

export interface ServerType {
  id: string
  name: string
  available: boolean
}

export interface ServerMemory {
  id: string
  name: string
  memoryMB: number
  running: boolean
}

/** One memory option, with Java's share and the players at once the sizing guide sizes it for (0 below its smallest suggestion). */
export interface MemoryBudget {
  memoryMB: number
  heapMB: number
  players: number
}

/** The sizing guide's first budget for a band of players at once; players is the top of the band. */
export interface MemorySuggestion {
  players: number
  memoryMB: number
}

export interface MemorySizing {
  workload: string
  budgets: MemoryBudget[]
  suggestions: MemorySuggestion[]
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
  /** Missing while a machine still runs an agent from before 0.4. */
  sizing?: MemorySizing
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

/** An actor that isn't an account: an AI agent's token, or root running `playkeeper mcp` for a user. */
export type ActorKind = 'token' | 'cli'

export interface Activity {
  ts: string
  serverId?: string
  kind: ActivityKind
  player?: string
  actor?: string
  actorKind?: ActorKind
  /** The token's name, or the account that ran sudo. */
  actorName?: string
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
  /** The machine whose agent recorded it. */
  machineId?: string
  actorKind?: ActorKind
  actorName?: string
}

export interface Me {
  user: { username: string; role: string }
  csrfToken: string
  expiresAt: string
  idleTimeoutSeconds: number
  version: string
}

// Follow-ups after 0.3.0.

/** A world folder a restore left next to the live one. */
export interface WorldCopy {
  name: string
  kind: 'previous' | 'failed_restore'
  createdAt: string
  sizeBytes: number
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
  problem?: string
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
