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

export type LagStatus = 'smooth' | 'a_bit_behind' | 'lagging' | 'frozen' | 'unknown'

export interface Resources {
  cpuPercent?: number
  /** Ticks per second over the last minute; 20 is full speed. */
  tps?: number
  /** Milliseconds a tick took; 50 fits 20 ticks a second. */
  mspt?: number
  lag?: LagStatus
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
  /** online_copy or online_in_place (players stayed online), or stopped. */
  method?: 'online_copy' | 'online_in_place' | 'stopped'
  /** How long world saving was paused; durationMs is the whole backup. */
  savingPausedMs: number
  durationMs: number
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
  /** A backup left world saving off since then; Playkeeper keeps turning it back on. */
  savingPausedSince?: string
  /** Why the server last stopped unexpectedly or could not start. */
  crash?: Crash
}

/** Params are the numbers and names each kind's text is built from. */
export type Params = Record<string, unknown>

export interface DiagnosisEvidence {
  kind: string
  params?: Params
  text: string
}

export type ActionKind =
  | 'raise_memory'
  | 'lower_memory'
  | 'restart'
  | 'remove_addon'
  | 'update_addon'
  | 'install_addon'
  | 'remove_datapack'
  | 'restore_backup'
  | 'free_disk'
  | 'change_port'
  | 'accept_eula'
  | 'fix_permissions'
  | 'pregenerate_world'
  | 'lower_view_distance'
  | 'lower_simulation_distance'
  | 'run_profiler'
  | 'move_to_dedicated_cpu'
  | 'raise_cpu_limit'
  | 'reduce_other_load'
  | 'upgrade_host'

export interface DiagnosisAction {
  kind: ActionKind
  params?: Params
  title: string
  recommended?: boolean
}

export type LagCauseKind = 'cpu_steal' | 'cpu_limit' | 'host_cpu_busy' | 'memory_pressure' | 'chunk_generation' | 'slow_disk' | 'high_distance' | 'world_workload'

export interface LagCause {
  kind: LagCauseKind
  params?: Params
  score: number
  title: string
  explanation: string
  evidence: DiagnosisEvidence[]
  actions: DiagnosisAction[]
}

export interface Running {
  status: LagStatus
  params?: Params
  title: string
  explanation: string
  evidence: DiagnosisEvidence[]
  causes: LagCause[]
  windowMinutes: number
  at?: string
  behindSince?: string
  players?: number
}

export type MemoryVerdict = 'lower' | 'raise' | 'keep' | 'not_enough_data'
export type MemoryFit = 'too_tight' | 'little_room' | 'room_to_grow' | 'more_than_needed'

export interface MemoryDay {
  date: string
  peakMB: number
}

export interface MemoryOption {
  memoryMB: number
  heapMB: number
  fits: boolean
  fit?: MemoryFit
}

export interface MemoryAdvice {
  verdict: MemoryVerdict
  params?: Params
  title: string
  explanation: string
  evidence: DiagnosisEvidence[]
  actions: DiagnosisAction[]
  budgetMB: number
  heapMB: number
  recommendedMB?: number
  fromNextStart?: boolean
  days: MemoryDay[]
  options: MemoryOption[]
}

export interface CrashLine {
  time?: string
  level?: 'WARN' | 'ERROR' | 'FATAL'
  text: string
}

export type CrashKind =
  | 'container_memory_limit'
  | 'heap_out_of_memory'
  | 'metaspace_out_of_memory'
  | 'thread_limit'
  | 'watchdog'
  | 'port_in_use'
  | 'newer_java'
  | 'missing_dependency'
  | 'incompatible_addon'
  | 'addon_failed'
  | 'mixin_failed'
  | 'datapack_failed'
  | 'corrupt_world'
  | 'world_locked'
  | 'disk_full'
  | 'eula'
  | 'permission_denied'
  | 'killed'
  | 'refused_file'
  | 'unknown'

export interface Crash {
  at: string
  /** The server did not come up, rather than stopping while it ran. */
  start: boolean
  kind: CrashKind
  params?: Params
  certain: boolean
  title: string
  explanation: string
  evidence: DiagnosisEvidence[]
  fixes: DiagnosisAction[]
  lines: CrashLine[]
  roomMB: number
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
  tpsAvg: number | null
  msptAvg: number | null
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
