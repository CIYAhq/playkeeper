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
  /** The exact software of a type other than Paper. */
  software?: SoftwarePin
  /** The pack the server was created from. */
  modpack?: ServerModpack
  /** The template the server was created from. */
  template?: ServerTemplate
  /** The UDP port voice chat has on this server. */
  voiceChatPort?: number
}

export interface Operation {
  id: string
  serverId?: string
  kind: string
  status: 'running' | 'succeeded' | 'failed' | 'cancelled'
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
  /** The friendly join address, set once it works. */
  joinAddress?: string
  /** The file that stopped the last start, while the server stays stopped. */
  refusal?: FileRefusal
  /** A backup left world saving off since then; Playkeeper keeps turning it back on. */
  savingPausedSince?: string
  /** Why the server last stopped unexpectedly or could not start. */
  crash?: Crash
  /** Set when the server's software no longer matches what Playkeeper installed. */
  softwareChanged?: SoftwareChange
  // Wave 7: sleep when nobody's playing.
  sleep?: SleepStatus
}

/** A file in the server's folder that Playkeeper would not follow or change. */
export interface FileRefusal {
  code: 'link' | 'special_file' | 'not_a_file' | 'not_a_folder' | 'too_large' | 'too_many_entries' | 'changed' | 'bad_name'
  params: { path: string; type?: string; limit?: string }
  message: string
  hint?: string
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
  /** Wave 7: the part of serversMemoryMB that sleeping servers gave back for now. */
  sleepingMemoryMB?: number
}

export interface ApiErrorBody {
  error: string
  code: string
  hint?: string
  operation?: Operation
  /** Values a translated message needs, such as retryAfterSeconds. */
  params?: Record<string, unknown>
  // Wave 7: the form field at fault and a stable reason code, with its values in params.
  field?: string
  reason?: string
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
  // Wave 5: someone joined the team with a team invite; detail is their role.
  | 'team_joined'
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

/** Served before sign-in, so only what is public anyway. */
export interface SetupStatus {
  needsSetup: boolean
  machine?: string
  version: string
}

export interface Me {
  user: { username: string; role: string }
  csrfToken: string
  expiresAt: string
  idleTimeoutSeconds: number
  version: string
  passwordChangedAt?: string
  /** What to tell the user once, right after the second sign-in step. */
  notices?: SignInNotice[]
}

// Two-factor sign-in (internal/twofactor and internal/panel/twofactor.go).

export type SecondFactorMethod = 'app_code' | 'recovery_code'

/** What the sign-in page needs for the second step. */
export interface Challenge {
  methods: SecondFactorMethod[]
  appCodesLockedUntil?: string
  appCodesBlocked: boolean
}

/** The password was right and the second step is next. */
export interface SecondFactorNeeded {
  secondFactor: Challenge
  user: { username: string }
  expiresAt: string
}

export type LoginAnswer = Me | SecondFactorNeeded

export interface TwoFactorStatus {
  state: 'off' | 'pending' | 'on'
  setupExpiresAt?: string
  confirmedAt?: string
  lastUsedAt?: string
  recoveryCodesLeft: number
  recoveryCodesMadeAt?: string
  appCodesLockedUntil?: string
  appCodesBlocked: boolean
}

export interface TwoFactorSetup {
  qrCodeSvg: string
  manualKey: string
  uri: string
  issuer: string
  account: string
  expiresAt: string
}

export type SignInNoticeKind = 'failed_attempts' | 'recovery_code_used' | 'recovery_codes_low' | 'no_recovery_codes'

export interface SignInNotice {
  kind: SignInNoticeKind
  count: number
  text: string
}

// The machine's address (internal/api/types.go, Address).

export type AddressKind = '' | 'playkeeper' | 'own'

/** What players type to join one server. */
export interface JoinAddress {
  serverId: string
  name: string
  port: number
  /** The server's part of the address: "survival" in survival.alex.playkeeper.io. */
  label: string
  address?: string
  /** The IP address with the port, which always works. */
  direct?: string
  published: boolean
}

export interface FreeAddress {
  name: string
  state: 'active' | 'lapsed' | 'released'
  /** Why a lapsed name stopped: not refreshed for a month, or the dashboard not answering on port 8443 for a week. */
  lapseReason?: 'not_refreshed' | 'no_answer'
  /** Why the servers have no address under the name yet; players join at the name with the port meanwhile. */
  serversWait?: 'server_address_not_yet' | 'not_answering'
  serversFrom?: string
  dns: 'ok' | 'pending'
  ipv4?: string
  ipv6?: string
  claimedAt: string
  refreshedAt: string
  stoppedAt?: string
  checkedAt: string
  holdDays: number
}

export interface NamesService {
  url: string
  unreachable?: boolean
  error?: string
  checkedAt?: string
}

export interface NameAvailability {
  name: string
  address?: string
  available: boolean
  /** invalid_name, name_reserved, name_taken or name_held (params.until is a Unix time). */
  code?: string
  message?: string
  params?: Record<string, unknown>
  suggestions?: string[]
}

export interface SRVParts {
  service: string
  protocol: string
  host: string
  priority: number
  weight: number
  port: number
  target: string
}

export interface DNSRecord {
  serverId?: string
  type: string
  name: string
  value: string
  ttl: number
  srv?: SRVParts
}

/** Code and params are what the page translates; message and hint are the backend's English. */
export interface Note {
  code: string
  params?: Record<string, string>
  message: string
  hint?: string
}

export interface AddrRecord {
  type: string
  addr: string
  here: boolean
  kind?: string
}

export interface NameCheck extends Note {
  name: string
  ok: boolean
  records?: AddrRecord[]
}

export interface RecordCheck extends Note {
  record: DNSRecord
  ok: boolean
  found?: string[]
}

export interface AddressCheck {
  at: string
  name: NameCheck
  records?: RecordCheck[]
  ready: boolean
}

export interface CertificateProblem extends Note {
  retryAt?: string
  needsAction?: boolean
  detail?: string
}

export interface CertificateStatus {
  names: string[]
  challenge: string
  notBefore?: string
  notAfter?: string
  renewAt?: string
  issuer?: string
  lastAttempt?: string
  nextAttempt?: string
  failures?: number
  problem?: CertificateProblem
}

export interface Address {
  kind: AddressKind
  host?: string
  since?: string
  ip?: string
  panelPort: number
  base: string
  servers: JoinAddress[] | null
  free?: FreeAddress
  records?: DNSRecord[] | null
  check?: AddressCheck
  certificate?: CertificateStatus
  names: NamesService
  termsAccepted?: string
  operation?: Operation
}

/** What a domain would need, before it is saved. */
export interface AddressPlan {
  domain: string
  records: DNSRecord[] | null
  servers: JoinAddress[] | null
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
  /** The part of Playkeeper that installed it and alone removes it: the Map, for squaremap. */
  usedBy?: 'map'
}

export interface AddonKey {
  source: AddonSource
  projectId: string
}

export interface AddonFile {
  fileName: string
  size: number
  /** pack: the server's modpack put it there and keeps it. */
  status: 'managed' | 'modified' | 'identified' | 'unknown' | 'pack'
  addon?: Addon
  name?: string
  version?: string
  pending?: boolean
}

export interface Addons {
  target: AddonTarget
  /** The pack the server runs, once its files are in place. */
  modpack?: ServerModpack
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
  /** Ports the add-on needs of its own, with the numbers Playkeeper would open. */
  ports?: AddonPort[]
}

/** A port an add-on listens on, such as voice chat's: opened when it's installed, closed when it's removed. */
export interface AddonPort {
  protocol: 'udp' | 'tcp'
  port: number
}

/** The add-ons Playkeeper picked by hand that fit the server's type and version. */
export interface CuratedAddons {
  picks: CuratedAddon[]
}

export interface CuratedAddon {
  /** Names the pick across releases: voice-chat, rollback, pregenerate… */
  id: string
  card: AddonCard
  permission?: string
  ports?: AddonPort[]
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

/** Settings › Add-on sources. Modrinth and Hangar are built in and always on. */
export interface AddonSources {
  curseforge: CurseForgeSource
}

export interface CurseForgeSource {
  /** Where the key in use comes from: the release's own (build), the owner's (file), an empty key file (disabled), or none. */
  key: 'none' | 'build' | 'file' | 'disabled'
  /** The last four characters of the owner's key. */
  ending?: string
  /** Why the owner's key file can't be used. */
  problem?: string
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
  /** The template's add-ons and data packs that couldn't be installed, each with params.name; Try again retries them. */
  skipped?: AddonNotice[]
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
  /** The day the template was made (YYYY-MM-DD); older templates don't say. */
  created?: string
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

// Wave 5: invite links, the team, Discord and player profiles.

export type Action =
  | 'view'
  | 'account.manage'
  | 'servers.run'
  | 'servers.console'
  | 'players.manage'
  | 'backups.make'
  | 'backups.restore'
  | 'servers.manage'
  | 'servers.create'
  | 'team.manage'
  | 'machine.manage'
  | 'audit.view'
  | 'backups.copies.manage'
  | 'backups.recovery_key'
  | 'backups.recover'

export type ProjectRole = 'admin' | 'moderator' | 'viewer'

/** Which servers an account or invite covers: all of them, or these. */
export interface Scope {
  all?: boolean
  servers?: string[]
}

/** What the signed-in account may do; the panel checks every request anyway. */
export interface Access {
  projectId?: string
  /** The team's name; empty while it has the default one. */
  team?: string
  role: ProjectRole
  servers: Scope
  twoFactor: boolean
  /** An admin whose Admin rights wait for two-factor sign-in. */
  needsTwoFactor?: boolean
  /** An admin with two-factor on, waiting for the owner or an admin to confirm them. */
  awaitingConfirmation?: boolean
  can: Action[]
}

export interface Me {
  access: Access
}

/** A sentence the UI shows; text is the backend's English. */
export interface Phrase {
  key: string
  params?: Record<string, string>
  text: string
  at?: string
}

export interface WhitelistEntry {
  /** How they got in, when an invite link let them in. */
  joined?: Phrase
}

export type InviteStatus = 'active' | 'used_up' | 'expired' | 'revoked'
export type Expiry = '1d' | '7d' | '30d' | 'until_turned_off'
export type Approval = 'right_away' | 'after_yes'

export interface Invite {
  id: string
  kind: 'player' | 'member'
  projectId: string
  serverId?: string
  role?: ProjectRole
  servers?: Scope
  approval?: Approval
  label?: string
  createdBy: number
  createdAt: string
  expiresAt?: string
  /** 0 for a friend link with no limit. */
  maxUses: number
  uses: number
  revokedAt?: string
  status: InviteStatus
  usesLeft?: number
  /** /join/<code>; only for links whose code is still known. */
  path?: string
}

/** Where invite links start: base is https://host:port; friendly is false for a bare address. */
export interface LinkBase {
  base: string
  friendly: boolean
}

export interface InvitesResponse {
  invites: Invite[]
  expiries: Expiry[]
  link: LinkBase
}

export interface NewInvite {
  label: string
  expiry: Expiry
  maxUses: number
  unlimited: boolean
  approval: Approval
}

export interface JoinRequest {
  id: string
  inviteId: string
  serverId: string
  playerName: string
  playerUuid: string
  state: 'pending' | 'approved' | 'declined'
  createdAt: string
  decidedAt?: string
  decidedBy?: number
}

export interface JoinRequestView {
  request: JoinRequest
  notice: { title: Phrase; detail: Phrase }
}

export interface PlayerDay {
  date: string
  playtimeSeconds: number
}

export interface PlayerProfile {
  name: string
  uuid?: string
  online: boolean
  onlineSince?: string
  allowlisted: boolean
  operator: boolean
  /** On the server's ban list. */
  banned?: boolean
  firstSeen?: string
  sessions: number
  playtimeSeconds: number
  longestSeconds: number
  playtimeUncertain?: boolean
  tz: string
  /** The last 14 days, oldest first. */
  days: PlayerDay[]
  mostly?: 'morning' | 'afternoon' | 'evening' | 'night'
  /** Newest first. */
  recent: Session[]
  joined?: Phrase
}

export interface TeamMember {
  id: number
  username: string
  owner: boolean
  you: boolean
  role: ProjectRole
  servers: Scope
  twoFactor: boolean
  addedAt: string
  canEdit: boolean
  /** An admin with two-factor on whose Admin rights wait for confirmation. */
  waiting?: boolean
  canConfirm?: boolean
}

export interface TeamInvite extends Invite {
  canEdit: boolean
}

export interface TeamResponse {
  projectId: string
  project: string
  members: TeamMember[]
  invites: TeamInvite[]
  grantableRoles: ProjectRole[]
  servers: { id: string; name: string }[]
}

export interface Grant {
  role: ProjectRole
  servers: Scope
  label?: string
}

/** A new team invite: its link is link.base + path, shown only now. */
export interface CreatedTeamInvite {
  invite: Invite
  path: string
  link: LinkBase
}

export type DiscordKind =
  | 'crash'
  | 'recovered'
  | 'low_disk'
  | 'backup_failed'
  | 'backup_succeeded'
  | 'update_available'
  | 'started'
  | 'stopped'
  | 'player_joined'
  | 'player_left'
  | 'join_requested'

export interface DiscordDelivery {
  sent?: string
  failed?: string
  code?: string
  msg?: string
  hint?: string
  retryAfterSeconds?: number
  stopped?: boolean
}

export interface DiscordSettings {
  connected: boolean
  webhookName?: string
  connectedAt?: string
  alerts: string[]
  liveStatus: boolean
  delivery: DiscordDelivery
  kinds: string[]
}

export interface PlayerPreview {
  kind: 'player'
  inviter: string
  server: string
  version?: string
  online: boolean
  playing: number
  approval: Approval
}

/** A condition the new account has, such as turning on two-factor sign-in. */
export interface Requirement {
  code: string
  text: string
  hint: string
}

export interface MemberPreview {
  kind: 'member'
  inviter: string
  role: ProjectRole
  servers: Scope
  expiresAt: string
  requires?: Requirement[]
  team?: string
  serverNames: string[]
}

export type JoinPreview = PlayerPreview | MemberPreview

export interface Candidate {
  name: string
  uuid: string
  /** A data: URL of their face. */
  face?: string
}

export interface JoinInfo {
  player: string
  server: string
  address: string
  version?: string
  /** An invite that needs a yes: they're waiting for it. */
  waiting?: boolean
  steps: Phrase[]
}

export interface AcceptResponse extends Me {
  requires?: Requirement[]
}

// Wave 6: each server's live map (MapInfo, and internal/webmap's worlds and
// players), and starting a server from a world (WorldImport and its preview).

export type MapState = 'unsupported' | 'not_installed' | 'server_stopped' | 'needs_restart' | 'not_answering' | 'drawing' | 'ready'

export interface MapProgress {
  done: number
  total: number
  percent: number
  secondsLeft?: number
}

export interface MapInfo {
  supported: boolean
  enabled: boolean
  /** The map was on, but the files it installed are gone; turning it on installs them again. */
  missing?: boolean
  state: MapState
  params?: Record<string, string>
  message: string
  hint?: string
  areas: number
  bytes: number
  lastDrawn?: string
  progress?: MapProgress
  plugin: string
  pluginVersion?: string
  estimatedMinutes: number
  estimatedMegabytes: number
  public: boolean
  publicPlayers: boolean
  /** The shared map under the machine's name; empty until the name points at the machine and has a certificate. */
  link?: string
  /** /map/<link token>, new each time sharing is switched on; empty while the map isn't shared. */
  path: string
  restartWhenEmpty: boolean
  checkedAt: string
}

export type MapDimension = 'overworld' | 'nether' | 'end' | 'custom'

export interface MapWorld {
  name: string
  dimension: MapDimension
  label: string
  spawn: { x: number; z: number }
  /** Tiles exist for 0 to max; at max one pixel is one block. */
  zoom: { max: number; default: number; extra: number }
  refreshSeconds: number
}

export interface MapWorlds {
  worlds: MapWorld[]
  tileSize: number
}

export type MapPlaceKind = 'near_spawn' | 'exploring' | 'nether' | 'end' | 'other_world'

export interface MapPlayer {
  name: string
  uuid: string
  world: string
  dimension: MapDimension
  x: number
  z: number
  place?: { kind: MapPlaceKind; params?: Record<string, string>; text: string }
}

export interface MapPlayers {
  players: MapPlayer[]
  updatedAt: string
}

/** What the shared map page may know about a server. */
export interface PublicMap {
  name: string
  players: boolean
}

export interface ImportMessage {
  kind: string
  params?: Record<string, unknown>
  text: string
  hint?: string
}

export interface ImportLevel {
  name: string
  version?: string
  dataVersion?: number
  snapshot?: boolean
  gameMode?: string
  hardcore: boolean
  difficulty?: string
  dataPacks?: string[]
  seed?: string
  spawn?: { x: number; z: number }
}

export interface ImportWorld {
  id: string
  archive: string
  path: string
  level?: ImportLevel
  levelError?: string
  origin: 'singleplayer' | 'server' | 'unknown'
  software?: string
  default?: boolean
  dimensions: string[]
  players: number
  sizeBytes: number
  files: number
}

export interface WorldImportFile {
  index: number
  name: string
  size: number
  received: number
  sha256?: string
}

export interface WorldImport {
  id: string
  serverId?: string
  createdAt: string
  files: WorldImportFile[]
  limitBytes: number
  inspection?: { archives: { name: string; format: string; bytes: number; entries: number }[]; worlds: ImportWorld[]; warnings?: ImportMessage[] }
}

export interface ImportPreview {
  world: ImportWorld
  target: { type: string; minecraftVersion: string; levelName: string }
  version?: { compat: 'same' | 'upgrade' | 'newer' | 'unknown'; world?: string; target: string; problem?: ImportMessage; warnings?: ImportMessage[] }
  folders: string[]
  fileCount: number
  sizeBytes: number
  dimensions: { id: string; folder: string; files: number; bytes: number }[]
  dataPacks?: string[]
  players: number
  settings?: { key: string; value: string; source: string }[]
  leftOut?: { kind: string; files: number; bytes: number; examples?: string[]; text: string }[]
  warnings?: ImportMessage[]
  problems?: ImportMessage[]
}

export interface WorldImportVersion extends CatalogEntry {
  /** The world's own version, so the world isn't upgraded. */
  keep: boolean
}

export interface WorldImportPreview {
  id: string
  serverId?: string
  preview: ImportPreview
  /** A new server's choices, recommended first. */
  versions?: WorldImportVersion[]
  versionId: string
  keepsOriginal: boolean
  memoryMB?: number
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
  /** The backup's SHA-256, as recorded when it was copied. */
  sha256?: string
  /** Why the last check found the copy missing or damaged. */
  checkError?: string
  /** Who removed the backup from this machine, once only the copy is left, if known. */
  removed?: 'rules' | 'person'
  /** The person's name, when a person removed it. */
  removedBy?: string
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
  backupCreatedAt?: string
}

export interface OffsiteView {
  enabled: boolean
  configured: boolean
  type: '' | 's3' | 'sftp'
  place: string
  s3?: OffsiteS3
  sftp?: OffsiteSFTP
  sshKey?: { publicKey: string; authorizedKey: string; fingerprint: string }
  key?: {
    recipient: string
    createdAt: string
    oldKeys: number
    savedAt?: string
    fileName: string
    /** The folder a file downloaded now names, if any. */
    folder?: string
    /** Copies went to another folder since the file was downloaded. */
    stale?: boolean
    /** The folder the downloaded file names, when stale. */
    savedFolder?: string
  }
  lastCopy?: OffsiteCopy
  /** lastCopy is the first copy made to this place. */
  firstCopy?: boolean
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
