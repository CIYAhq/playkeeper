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
  /** The friendly join address, set once it works. */
  joinAddress?: string
  /** The file that stopped the last start, while the server stays stopped. */
  refusal?: FileRefusal
  /** A backup left world saving off since then; Playkeeper keeps turning it back on. */
  savingPausedSince?: string
  /** Why the server last stopped unexpectedly or could not start. */
  crash?: Crash
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
}

export interface ApiErrorBody {
  error: string
  code: string
  hint?: string
  operation?: Operation
  /** Values a translated message needs, such as retryAfterSeconds. */
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
