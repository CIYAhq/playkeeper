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
  // Wave 7: stopped because nobody played; the first join wakes it.
  | 'asleep'

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
  kind: 'manual' | 'scheduled' | 'rollback'
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
  desired: 'running' | 'stopped' | 'sleeping'
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
  // Wave 7: sleep when nobody's playing.
  sleep?: SleepStatus
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
  /** Wave 7: the part of serversMemoryMB that sleeping servers gave back for now. */
  sleepingMemoryMB?: number
}

export interface ApiErrorBody {
  error: string
  code: string
  hint?: string
  operation?: Operation
  // Wave 7: the form field at fault, a stable reason code and its values.
  field?: string
  reason?: string
  params?: Record<string, unknown>
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
  // Wave 7
  | 'fell_asleep'
  | 'woke_up'

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

// --- Wave 7 (0.4.0): schedules, sleep, backup rules, copies somewhere else, disk space ---

export interface SleepStatus {
  enabled: boolean
  idleMinutes: number
  asleepSince?: string
  listening: boolean
  sleepAt?: string
}

export interface SleepView extends SleepStatus {
  defaultIdleMinutes: number
  minIdleMinutes: number
  maxIdleMinutes: number
  today: { count: number; seconds: number }
}

export type ScheduleKind = 'restart' | 'backup' | 'announcement' | 'command'
export type TimingKind = 'daily' | 'weekly' | 'interval' | 'once' | 'cron'
export type Weekday = 'mon' | 'tue' | 'wed' | 'thu' | 'fri' | 'sat' | 'sun'

export interface ScheduleTiming {
  kind: TimingKind
  timeZone: string
  at?: string
  days?: Weekday[]
  everyHours?: number
  date?: string
  cron?: string
  graceMinutes?: number
}

export interface SchedulePayload {
  warnSeconds?: number[]
  message?: string
  ifEmpty?: '' | 'skip' | 'now'
  command?: string
  note?: string
  skipIfPlaying?: boolean
  onlyIfPlayed?: boolean
}

export type RunResult = 'running' | 'succeeded' | 'failed' | 'skipped' | 'missed'

export interface ScheduleLastRun {
  due: string
  started?: string
  finished?: string
  result: RunResult
  reason?: string
  detail?: string
  operationId?: string
  players?: number
  retryAt?: string
}

export interface Schedule {
  id: string
  serverId: string
  name?: string
  kind: ScheduleKind
  timing: ScheduleTiming
  payload: SchedulePayload
  enabled: boolean
  createdAt: string
  updatedAt: string
  createdBy: string
  updatedBy: string
  lastRun?: ScheduleLastRun
  nextRun?: string
  summary: string
}

export interface SchedulesResponse {
  schedules: Schedule[]
  current?: { scheduleId: string; kind: ScheduleKind; due: string; restartAt?: string }
}

export interface SchedulePreview {
  valid: boolean
  nextRuns: string[]
  summary?: string
  error?: ApiErrorBody
}

export interface ScheduleRun {
  scheduleId: string
  kind: ScheduleKind
  due: string
  startedAt?: string
  finishedAt?: string
  result: RunResult
  reason?: string
  detail?: string
  operationId?: string
  players?: number
  backup?: { id: string; sizeBytes: number; verified: boolean; downtimeMs: number }
}

export interface RetentionRules {
  keepAll?: boolean
  hours?: number
  last?: number
  daily?: number
  weekly?: number
  monthly?: number
}

export interface RetentionSettings {
  onHost: RetentionRules
  offSite: RetentionRules
  includeManual?: boolean
  deleteOnlyCopies?: boolean
}

/** A plain English sentence with a stable code and its values. */
export interface RetentionText {
  code: string
  params?: Record<string, string>
  text: string
}

export type RetentionRule = 'keep_all' | 'hours' | 'last' | 'daily' | 'weekly' | 'monthly' | 'manual'

export interface RetentionEstimate {
  where: 'on-host' | 'off-site'
  rows: { rule: RetentionRule; n: number; count: number; upTo: boolean; text: RetentionText }[]
  /** The most backups kept at once; -1 when every backup is kept. */
  count: number
  bytes: number
  summary: RetentionText
}

export interface AutomaticBackups {
  enabled: boolean
  everyHours: number
  onlyIfPlayed: boolean
  scheduleId?: string
  nextRun?: string
}

export interface BackupRulesView {
  automatic: AutomaticBackups
  rules: RetentionSettings
  custom: boolean
  describe: RetentionText[]
  onHost: RetentionEstimate
  offSite: RetentionEstimate
  limits: Record<'hours' | 'last' | 'daily' | 'weekly' | 'monthly', number>
}

export interface OffsiteProvider {
  id: string
  name: string
  endpoint: string
  region: string
  pathStyle: boolean
  hint: string
}

export interface OffsiteS3 {
  provider: string
  endpoint: string
  region: string
  bucket: string
  prefix: string
  accessKeyId: string
  pathStyle: boolean
  secretKeySet?: boolean
}

export interface OffsiteSFTP {
  host: string
  port: number
  user: string
  folder: string
  hostKey?: string
  auth?: 'key' | 'password'
  passwordSet?: boolean
  hostKeyType?: string
  hostKeyFingerprint?: string
}

export interface OffsiteCopy {
  backupId: string
  kind: string
  createdAt: string
  fileName: string
  name: string
  sizeBytes: number
  copySizeBytes: number
  minecraftVersion: string
  levelName: string
  copiedAt: string
  checked: string
  onHost: boolean
}

export interface OffsitePending {
  backupId: string
  fileName: string
  uploading: boolean
  sent: number
  total: number
  bytesPerSec?: number
  attempts: number
  nextAttempt?: string
  error?: string
  hint?: string
  errorKind?: string
  params?: Record<string, string>
}

export interface OffsiteView {
  enabled: boolean
  configured: boolean
  type: '' | 's3' | 'sftp'
  place: string
  s3?: OffsiteS3
  sftp?: OffsiteSFTP
  sshKey?: { publicKey: string; authorizedKey: string; fingerprint: string }
  key?: { recipient: string; createdAt: string; oldKeys: number; savedAt?: string; fileName: string }
  lastCopy?: OffsiteCopy
  copies: number
  copiesBytes: number
  pending?: OffsitePending
  queued: number
  providers: OffsiteProvider[]
}

export interface OffsiteCheck {
  step: string
  ok: boolean
  msg: string
  hint?: string
  kind?: string
  params?: Record<string, string>
}

export interface OffsiteTestResult {
  ok: boolean
  checks: OffsiteCheck[]
  skew: number
  hostKey?: { key: string; type: string; fingerprint: string }
  warning?: string
}

export interface OffsiteNewKey {
  rotation: { recipient: string; oldRecipient: string; oldKeys: number; code: string; msg: string; hint: string }
  offsite: OffsiteView
}

/** A server's copies, as a new machine finds them with its recovery key file. */
export interface RecoverView {
  server: string
  keys: number
  madeAt?: string
  place: string
  copies: { name: string; sizeBytes: number; createdAt: string }[]
}

export type DiskGroup = 'backups' | 'worlds' | 'server_files' | 'logs' | 'other' | 'free'
export type DiskWayID = 'old_backups' | 'old_logs' | 'old_crash_reports' | 'unused_software' | 'downloads' | 'set_aside' | 'unfinished'

export interface DiskCandidate {
  id: string
  serverId?: string
  kind: string
  reason: string
  risk: string
  path: string
  backupId?: string
  bytes: number
  files: number
  modifiedAt: string
  params?: Record<string, string>
  text: string
}

export interface DiskWay {
  id: DiskWayID
  action: 'review' | 'delete' | 'clear'
  bytes: number
  candidateIds: string[]
  serverIds?: string[]
  everyServer?: boolean
  versions?: { software: string; version: string; build?: string }[]
  params?: Record<string, string>
  title: string
  text: string
}

export interface DiskUsage {
  bytes: number
  files: number
}

export interface DiskServer {
  id: string
  name: string
  total: DiskUsage
  kinds: (DiskUsage & { kind: string })[]
  groups: { group: DiskGroup; bytes: number }[]
}

export interface DiskReport {
  scannedAt: string
  disk: { dir: string; total: number; free: number; used: number; bar: { group: DiskGroup; bytes: number }[] } | null
  servers: DiskServer[]
  machine: (DiskUsage & { kind: string })[]
  total: DiskUsage
  candidates: DiskCandidate[]
  ways: DiskWay[]
  freeable: number
  truncated: boolean
  problems?: { code: string; path: string; text: string }[]
  moreProblems?: number
}
