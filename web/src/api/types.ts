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

export interface ApiErrorBody {
  params?: Record<string, string>
}

/** A sentence the UI shows; text is the backend's English. */
export interface Note {
  key: string
  params?: Record<string, string>
  text: string
  at?: string
}

export interface WhitelistEntry {
  /** How they got in, when an invite link let them in. */
  joined?: Note
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
  notice: { title: Note; detail: Note }
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
  joined?: Note
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
  steps: Note[]
}

export interface AcceptResponse extends Me {
  requires?: Requirement[]
}
