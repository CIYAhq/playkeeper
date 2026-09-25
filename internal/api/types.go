// Package api defines the JSON contract between the agent, the panel and the
// browser UI. web/src/api/types.ts mirrors these shapes; keep them in sync.
package api

import "time"

// Phase is the user-visible lifecycle phase of a Minecraft server.
type Phase string

const (
	PhaseNotCreated        Phase = "not_created"
	PhaseStopped           Phase = "stopped"
	PhasePulling           Phase = "pulling_image"
	PhaseStartingContainer Phase = "starting_container"
	PhaseDownloading       Phase = "downloading_server"
	PhaseStarting          Phase = "starting"
	PhasePreparingWorld    Phase = "preparing_world"
	PhaseOnline            Phase = "online"
	PhaseStopping          Phase = "stopping"
	PhaseCrashed           Phase = "crashed"
	PhaseDockerUnavailable Phase = "docker_unavailable"
)

const (
	DesiredRunning = "running"
	DesiredStopped = "stopped"
)

// Games and server types. A server's game decides its tabs and settings; its
// type decides which software runs it (see internal/minecraft/types.go).
const (
	GameMinecraftJava = "minecraft-java"
	TypePaper         = "paper"
)

// ServerStatus is one server's identity, desired and observed state.
type ServerStatus struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Slug            string          `json:"slug"`
	Game            string          `json:"game"`
	Type            string          `json:"type"`
	CreatedAt       time.Time       `json:"createdAt"`
	Exists          bool            `json:"exists"`
	Desired         string          `json:"desired"`
	Phase           Phase           `json:"phase"`
	PhaseDetail     string          `json:"phaseDetail,omitempty"`
	Reachable       bool            `json:"reachable"`
	ReachableAt     *time.Time      `json:"reachableAt,omitempty"`
	StartedAt       *time.Time      `json:"startedAt,omitempty"`
	StoppedAt       *time.Time      `json:"stoppedAt,omitempty"`
	ExitCode        *int            `json:"exitCode,omitempty"`
	Players         *PlayerSnapshot `json:"players,omitempty"`
	Config          *ServerConfig   `json:"config,omitempty"`
	Gameplay        Gameplay        `json:"gameplay"`
	GamePort        int             `json:"gamePort"`
	LastError       string          `json:"lastError,omitempty"`
	LastErrorHint   string          `json:"lastErrorHint,omitempty"`
	OfflineModeTest bool            `json:"offlineModeTest"`
	Operation       *Operation      `json:"operation,omitempty"`
	LastOperation   *Operation      `json:"lastOperation,omitempty"`
	CrashCount      int             `json:"crashCount"`
	Resources       *Resources      `json:"resources,omitempty"`
	LastBackup      *Backup         `json:"lastBackup,omitempty"`
	// WorldBytes is the world's size on disk (all its dimensions), measured
	// every few minutes.
	WorldBytes      *int64     `json:"worldBytes,omitempty"`
	PendingRestart  bool       `json:"pendingRestart"`
	CollectingSince *time.Time `json:"collectingSince,omitempty"`
	FirstSteps      FirstSteps `json:"firstSteps"`
}

// FirstSteps is what the "Get started" checklist ticks off for a server.
type FirstSteps struct {
	// Invited is a name on the allowlist, if anyone is on it.
	Invited string `json:"invited,omitempty"`
	// FriendJoined is the first player seen joining, and when.
	FriendJoined   string     `json:"friendJoined,omitempty"`
	FriendJoinedAt *time.Time `json:"friendJoinedAt,omitempty"`
	BackedUp       bool       `json:"backedUp"`
	Downloaded     bool       `json:"downloaded"`
}

// Gameplay holds the plain-language game settings. Empty fields leave the
// server's own value (server.properties) alone; in a status they are filled
// with the value in effect.
type Gameplay struct {
	Difficulty   string `json:"difficulty,omitempty"` // peaceful | easy | normal | hard
	PVP          *bool  `json:"pvp,omitempty"`
	GameMode     string `json:"gameMode,omitempty"` // survival | creative | adventure | spectator
	Hardcore     *bool  `json:"hardcore,omitempty"`
	ViewDistance int    `json:"viewDistance,omitempty"` // chunks
	LevelType    string `json:"levelType,omitempty"`    // normal | flat | amplified | large_biomes; chosen at creation
}

// Machine is the computer an agent runs on and what its servers use of it.
type Machine struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	CPUs     int    `json:"cpus"`
	// CPUPercent is the whole machine's CPU use (100% = every core busy).
	CPUPercent    *float64 `json:"cpuPercent,omitempty"`
	MemoryTotalMB int      `json:"memoryTotalMB"`
	// SystemReserveMB is kept for the system and Playkeeper; ServersMemoryMB
	// is reserved by the servers' budgets, running or not.
	SystemReserveMB int             `json:"systemReserveMB"`
	ServersMemoryMB int             `json:"serversMemoryMB"`
	MemoryFreeMB    int             `json:"memoryFreeMB"`
	DiskFreeBytes   *int64          `json:"diskFreeBytes,omitempty"`
	DiskTotalBytes  *int64          `json:"diskTotalBytes,omitempty"`
	DiskWarning     *PreflightCheck `json:"diskWarning,omitempty"`
	Docker          bool            `json:"docker"`
	DockerVersion   string          `json:"dockerVersion,omitempty"`
	AgentVersion    string          `json:"agentVersion"`
	DefaultGamePort int             `json:"defaultGamePort"`
	OfflineModeTest bool            `json:"offlineModeTest"`
	// Operation is a machine-wide operation in progress (a Playkeeper update).
	Operation *Operation `json:"operation,omitempty"`
	// UpdateAvailable is a newer, signed Playkeeper release, if the last
	// check found one; UpdateInstalling the release being installed.
	UpdateAvailable  string `json:"updateAvailable,omitempty"`
	UpdateInstalling string `json:"updateInstalling,omitempty"`
	Servers          int    `json:"servers"`
}

// UpdateInfo is what Playkeeper knows about its own updates.
type UpdateInfo struct {
	Current string `json:"current"`
	// Supported is false when this build cannot install updates; Reason says why.
	Supported   bool          `json:"supported"`
	Reason      string        `json:"reason,omitempty"`
	Latest      string        `json:"latest,omitempty"`
	Available   bool          `json:"available"`
	Notes       string        `json:"notes,omitempty"`
	ReleaseDate string        `json:"releaseDate,omitempty"`
	CheckedAt   *time.Time    `json:"checkedAt,omitempty"`
	CheckError  string        `json:"checkError,omitempty"`
	Installing  string        `json:"installing,omitempty"`
	LastResult  *UpdateResult `json:"lastResult,omitempty"`
}

// UpdateResult is how the last update ended: updated, rolled_back (the new
// version was unhealthy and the previous one is running again), refused
// (nothing changed) or failed (the host needs a manual fix).
type UpdateResult struct {
	From       string    `json:"from"`
	To         string    `json:"to"`
	Outcome    string    `json:"outcome"`
	Error      string    `json:"error,omitempty"`
	FinishedAt time.Time `json:"finishedAt"`
}

type UpdateCheckRequest struct {
	Actor string `json:"actor"`
}

type UpdateApplyRequest struct {
	// Version is the release the admin chose; it must still be the latest.
	Version string `json:"version"`
	Actor   string `json:"actor"`
}

// VersionChangeRequest moves the server to another Paper build. Only newer
// versions are allowed; experimental ones need AcceptExperimental.
type VersionChangeRequest struct {
	VersionID          string `json:"versionId"`
	AcceptExperimental bool   `json:"acceptExperimental"`
	// WarnPlayers says in chat that the server updates in a minute, and
	// waits that minute, when players are online.
	WarnPlayers bool   `json:"warnPlayers,omitempty"`
	Actor       string `json:"actor"`
}

type PlayerSnapshot struct {
	Online int       `json:"online"`
	Max    int       `json:"max"`
	Names  []string  `json:"names"`
	Source string    `json:"source"`
	At     time.Time `json:"at"`
}

type Resources struct {
	CPUPercent *float64 `json:"cpuPercent,omitempty"`
	// TPS is the server's ticks per second over the last minute (20 is full
	// speed), from Paper's tps command.
	TPS            *float64  `json:"tps,omitempty"`
	MemBytes       *int64    `json:"memBytes,omitempty"`
	MemLimitBytes  *int64    `json:"memLimitBytes,omitempty"`
	DiskFreeBytes  *int64    `json:"diskFreeBytes,omitempty"`
	DiskTotalBytes *int64    `json:"diskTotalBytes,omitempty"`
	At             time.Time `json:"at"`
}

type ServerConfig struct {
	// Type is the server software (empty means paper, for servers made
	// before 0.3.0). PaperBuild is Paper's build of MinecraftVersion.
	Type             string `json:"type,omitempty"`
	VersionID        string `json:"versionId"`
	MinecraftVersion string `json:"minecraftVersion"`
	PaperBuild       int    `json:"paperBuild"`
	// JarSHA256 is the checksum PaperMC publishes for this build; the jar is
	// verified against it before every start. Servers created by 0.1.0 have
	// none and use the checksum pinned in that release.
	JarSHA256 string `json:"jarSha256,omitempty"`
	// MemoryMB is the memory budget: the container's hard memory limit.
	MemoryMB int `json:"memoryMB"`
	// HeapMB is the Java heap (-Xms/-Xmx) derived from MemoryMB.
	HeapMB         int        `json:"heapMB"`
	LevelName      string     `json:"levelName"`
	JarVerifiedAt  *time.Time `json:"jarVerifiedAt,omitempty"`
	MOTD           string     `json:"motd"`
	MaxPlayers     int        `json:"maxPlayers"`
	Whitelist      bool       `json:"whitelist"`
	EULAAcceptedAt time.Time  `json:"eulaAcceptedAt"`
	EULAAcceptedBy string     `json:"eulaAcceptedBy"`
	CreatedAt      time.Time  `json:"createdAt"`
	Image          string     `json:"image"`
	// PlayStyle is the preset the server was created with (friends,
	// creative, hardcore or solo), shown next to its name.
	PlayStyle string `json:"playStyle,omitempty"`
	// Gameplay holds the settings chosen in Playkeeper; unset ones stay as
	// the server has them.
	Gameplay Gameplay `json:"gameplay,omitzero"`
	// IconUpdatedAt is when the server-list icon was last changed.
	IconUpdatedAt *time.Time `json:"iconUpdatedAt,omitempty"`
}

type CreateServerRequest struct {
	// Name is what Playkeeper calls the server; empty picks "My server".
	Name       string `json:"name,omitempty"`
	Type       string `json:"type,omitempty"`
	AcceptEULA bool   `json:"acceptEula"`
	VersionID  string `json:"versionId"`
	// AcceptExperimental confirms an experimental (alpha or beta) version.
	AcceptExperimental bool      `json:"acceptExperimental,omitempty"`
	MemoryMB           int       `json:"memoryMB"`
	MOTD               string    `json:"motd"`
	MaxPlayers         int       `json:"maxPlayers"`
	PlayStyle          string    `json:"playStyle,omitempty"`
	Gameplay           *Gameplay `json:"gameplay,omitempty"`
	Actor              string    `json:"actor"`
}

type SettingsRequest struct {
	Name       *string   `json:"name,omitempty"`
	MemoryMB   *int      `json:"memoryMB,omitempty"`
	MOTD       *string   `json:"motd,omitempty"`
	MaxPlayers *int      `json:"maxPlayers,omitempty"`
	Gameplay   *Gameplay `json:"gameplay,omitempty"`
	// Restart restarts a running server right away so the changes apply.
	Restart bool   `json:"restart,omitempty"`
	Actor   string `json:"actor"`
}

// SettingsResponse is the server after a settings change, and the restart
// that applies it if one was asked for.
type SettingsResponse struct {
	Server    ServerStatus `json:"server"`
	Operation *Operation   `json:"operation,omitempty"`
}

// DeleteServerRequest deletes a server's container, world and the backups
// on this machine; Confirm must be the server's name.
type DeleteServerRequest struct {
	Confirm string `json:"confirm"`
	Actor   string `json:"actor"`
}

type OperatorEntry struct {
	Name  string `json:"name"`
	UUID  string `json:"uuid,omitempty"`
	Level int    `json:"level"`
}

type PlayerRequest struct {
	Name  string `json:"name"`
	Actor string `json:"actor"`
}

// Activity is one line of a server's (or every server's) recent activity,
// from the event log and the audit log.
type Activity struct {
	TS       time.Time `json:"ts"`
	ServerID string    `json:"serverId,omitempty"`
	Kind     string    `json:"kind"`
	Player   string    `json:"player,omitempty"`
	Actor    string    `json:"actor,omitempty"`
	Detail   string    `json:"detail,omitempty"`
}

type ActionRequest struct {
	Actor string `json:"actor"`
}

type Operation struct {
	ID string `json:"id"`
	// ServerID is the server the operation works on; empty for a
	// machine-wide operation such as a Playkeeper update.
	ServerID   string         `json:"serverId,omitempty"`
	Kind       string         `json:"kind"`
	Status     string         `json:"status"`
	Phase      string         `json:"phase"`
	Actor      string         `json:"actor"`
	StartedAt  time.Time      `json:"startedAt"`
	FinishedAt *time.Time     `json:"finishedAt,omitempty"`
	Error      string         `json:"error,omitempty"`
	Hint       string         `json:"hint,omitempty"`
	Detail     map[string]any `json:"detail,omitempty"`
}

const (
	OpRunning   = "running"
	OpSucceeded = "succeeded"
	OpFailed    = "failed"
)

type CatalogEntry struct {
	ID               string `json:"id"`
	Label            string `json:"label"`
	MinecraftVersion string `json:"minecraftVersion"`
	PaperBuild       int    `json:"paperBuild"`
	JarSHA256        string `json:"jarSha256"`
	Java             int    `json:"java"`
	Recommended      bool   `json:"recommended"`
	Notes            string `json:"notes"`
	// Channel is PaperMC's build channel: STABLE, BETA or ALPHA. Experimental
	// is set for versions without a stable build yet.
	Channel      string `json:"channel"`
	Experimental bool   `json:"experimental"`
	// Supported is false for versions PaperMC no longer updates.
	Supported bool `json:"supported"`
}

// ServerType is one kind of server software and whether it can be chosen yet.
type ServerType struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
}

// ServerMemory is one server's share of the machine's memory.
type ServerMemory struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MemoryMB int    `json:"memoryMB"`
	Running  bool   `json:"running"`
}

// Catalog is what a new server (or a server's settings) can choose from.
// Memory options leave room for the system and every other server.
type Catalog struct {
	Type     string         `json:"type"`
	Types    []ServerType   `json:"types"`
	Versions []CatalogEntry `json:"versions"`
	// VersionsError says why the version list could not be loaded from PaperMC.
	VersionsError string `json:"versionsError,omitempty"`
	// VersionsCheckedAt is when the version list was fetched from PaperMC.
	VersionsCheckedAt   *time.Time     `json:"versionsCheckedAt,omitempty"`
	MemoryOptionsMB     []int          `json:"memoryOptionsMB"`
	RecommendedMemoryMB int            `json:"recommendedMemoryMB"`
	HostMemoryMB        int            `json:"hostMemoryMB"`
	MaxMemoryMB         int            `json:"maxMemoryMB"`
	SystemReserveMB     int            `json:"systemReserveMB"`
	MemoryFreeMB        int            `json:"memoryFreeMB"`
	Servers             []ServerMemory `json:"servers"`
	SuggestedPort       int            `json:"suggestedPort,omitempty"`
	Image               string         `json:"image"`
}

type PreflightCheck struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"` // pass | warn | fail
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

type Preflight struct {
	OK     bool             `json:"ok"`
	Checks []PreflightCheck `json:"checks"`
}

type LogLine struct {
	Seq  int64     `json:"seq"`
	TS   time.Time `json:"ts"`
	Text string    `json:"text"`
}

type LogsResponse struct {
	Epoch string    `json:"epoch"`
	Lines []LogLine `json:"lines"`
	Next  int64     `json:"next"`
	// Truncated is true when lines older than the requested cursor were dropped
	// from the bounded buffer.
	Truncated bool `json:"truncated"`
}

type CommandRequest struct {
	Command string `json:"command"`
	Actor   string `json:"actor"`
}

type CommandResponse struct {
	Output string `json:"output"`
}

type WhitelistEntry struct {
	Name string `json:"name"`
	UUID string `json:"uuid,omitempty"`
}

type WhitelistRequest struct {
	Name  string `json:"name"`
	Actor string `json:"actor"`
}

type MetricsBucket struct {
	Start      time.Time `json:"start"`
	PlayersMax *int      `json:"playersMax"`
	CPUAvg     *float64  `json:"cpuAvg"`
	MemAvg     *int64    `json:"memAvg"`
	// Coverage is the fraction of expected samples actually collected.
	Coverage float64 `json:"coverage"`
	// State is online, offline (server not running), or no_data (collector gap).
	State string `json:"state"`
}

type Gap struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	Kind string    `json:"kind"` // collector_down | server_offline
}

type MetricsResponse struct {
	From                  time.Time       `json:"from"`
	To                    time.Time       `json:"to"`
	BucketSeconds         int             `json:"bucketSeconds"`
	SampleIntervalSeconds int             `json:"sampleIntervalSeconds"`
	Buckets               []MetricsBucket `json:"buckets"`
	Gaps                  []Gap           `json:"gaps"`
	CollectingSince       *time.Time      `json:"collectingSince,omitempty"`
	Source                string          `json:"source"`
}

type Session struct {
	ID              int64      `json:"id"`
	Player          string     `json:"player"`
	UUID            string     `json:"uuid,omitempty"`
	Start           time.Time  `json:"start"`
	End             *time.Time `json:"end,omitempty"`
	EndReason       string     `json:"endReason,omitempty"`
	StartUncertain  bool       `json:"startUncertain"`
	EndUncertain    bool       `json:"endUncertain"`
	DurationSeconds int64      `json:"durationSeconds"`
	Source          string     `json:"source"`
}

type SessionsResponse struct {
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Sessions []Session `json:"sessions"`
}

// DailyActivity totals a day's observed sessions. The playtime is a lower
// bound when a session's start was not seen, an upper bound when a session
// ended in a crash, and an estimate with both.
type DailyActivity struct {
	Date               string  `json:"date"`
	UniquePlayers      int     `json:"uniquePlayers"`
	Sessions           int     `json:"sessions"`
	PlaytimeSeconds    int64   `json:"playtimeSeconds"`
	PlaytimeLowerBound bool    `json:"playtimeLowerBound"`
	PlaytimeUpperBound bool    `json:"playtimeUpperBound"`
	Coverage           float64 `json:"coverage"`
}

type PlayerStat struct {
	Name            string    `json:"name"`
	UUID            string    `json:"uuid,omitempty"`
	LastSeen        time.Time `json:"lastSeen"`
	Online          bool      `json:"online"`
	Sessions        int       `json:"sessions"`
	PlaytimeSeconds int64     `json:"playtimeSeconds"`
	// PlaytimeUncertain is set when a session's start or end wasn't seen
	// (a crash, or the agent was down), so the playtime is an estimate.
	PlaytimeUncertain bool `json:"playtimeUncertain,omitempty"`
}

type PlayersSummary struct {
	TZ                string          `json:"tz"`
	Days              []DailyActivity `json:"days"`
	Players           []PlayerStat    `json:"players"`
	ObservedSessions  int             `json:"observedSessions"`
	UncertainSessions int             `json:"uncertainSessions"`
	CollectingSince   *time.Time      `json:"collectingSince,omitempty"`
	RetentionDays     int             `json:"retentionDays"`
}

type Event struct {
	ID     int64     `json:"id"`
	TS     time.Time `json:"ts"`
	Kind   string    `json:"kind"`
	Player string    `json:"player,omitempty"`
	Source string    `json:"source"`
	Detail string    `json:"detail,omitempty"`
}

type Backup struct {
	ID               string     `json:"id"`
	ServerID         string     `json:"serverId"`
	Kind             string     `json:"kind"` // manual | rollback
	CreatedAt        time.Time  `json:"createdAt"`
	FileName         string     `json:"fileName"`
	SizeBytes        int64      `json:"sizeBytes"`
	SHA256           string     `json:"sha256"`
	Location         string     `json:"location"` // on-host
	Verified         *bool      `json:"verified,omitempty"`
	VerifiedAt       *time.Time `json:"verifiedAt,omitempty"`
	VerifyError      string     `json:"verifyError,omitempty"`
	DowntimeMs       int64      `json:"downtimeMs"`
	MinecraftVersion string     `json:"minecraftVersion"`
	LevelName        string     `json:"levelName"`
	FileCount        int        `json:"fileCount"`
	CreatedBy        string     `json:"createdBy"`
	DownloadedAt     *time.Time `json:"downloadedAt,omitempty"`
	Note             string     `json:"note,omitempty"`
}

type BackupRequest struct {
	Actor string `json:"actor"`
	Note  string `json:"note,omitempty"`
}

type ManifestSummary struct {
	CreatedAt         time.Time         `json:"createdAt"`
	PlaykeeperVersion string            `json:"playkeeperVersion"`
	MinecraftVersion  string            `json:"minecraftVersion"`
	PaperBuild        int               `json:"paperBuild"`
	VersionID         string            `json:"versionId"`
	LevelName         string            `json:"levelName"`
	FileCount         int               `json:"fileCount"`
	TotalBytes        int64             `json:"totalBytes"`
	SourceInstall     string            `json:"sourceInstall"`
	Settings          map[string]string `json:"settings"`
}

type CurrentWorld struct {
	Exists    bool   `json:"exists"`
	LevelName string `json:"levelName,omitempty"`
	SizeBytes int64  `json:"sizeBytes"`
}

type RestorePreview struct {
	ID string `json:"id"`
	// ServerID is the server whose world is replaced; empty when the
	// restore creates a new server.
	ServerID           string           `json:"serverId,omitempty"`
	Source             string           `json:"source"` // upload | backup:<id>
	ReceivedAt         time.Time        `json:"receivedAt"`
	SizeBytes          int64            `json:"sizeBytes"`
	SHA256             string           `json:"sha256"`
	Manifest           *ManifestSummary `json:"manifest,omitempty"`
	Compatible         bool             `json:"compatible"`
	Problems           []string         `json:"problems"`
	Warnings           []string         `json:"warnings"`
	CurrentWorld       CurrentWorld     `json:"currentWorld"`
	WillCreateRollback bool             `json:"willCreateRollback"`
	NeedsEULA          bool             `json:"needsEula"`
	MemoryMB           int              `json:"memoryMB"`
	ConfirmPhrase      string           `json:"confirmPhrase"`
	Steps              []string         `json:"steps"`
	NotRestored        []string         `json:"notRestored"`
}

type RestoreApplyRequest struct {
	Confirm string `json:"confirm"`
	// AcceptEULA is required when the restore creates a new server.
	AcceptEULA bool `json:"acceptEula"`
	// MemoryMB overrides the backup's memory budget (must fit this host).
	MemoryMB int `json:"memoryMB,omitempty"`
	// Name names the new server a restore creates.
	Name  string `json:"name,omitempty"`
	Actor string `json:"actor"`
}

type AuditEntry struct {
	ID       int64     `json:"id"`
	ServerID string    `json:"serverId,omitempty"`
	TS       time.Time `json:"ts"`
	Actor    string    `json:"actor"`
	Action   string    `json:"action"`
	Target   string    `json:"target,omitempty"`
	Result   string    `json:"result"`
	Detail   string    `json:"detail,omitempty"`
}

type Health struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
	Docker  bool   `json:"docker"`
}

// Error is the body of every non-2xx response from the agent and panel.
type Error struct {
	Error     string     `json:"error"`
	Code      string     `json:"code"`
	Hint      string     `json:"hint,omitempty"`
	Operation *Operation `json:"operation,omitempty"`
}

const (
	CodeInvalid           = "invalid_request"
	CodeEULARequired      = "eula_required"
	CodeBusy              = "busy"
	CodeNotFound          = "not_found"
	CodeConflict          = "conflict"
	CodeNotCreated        = "not_created"
	CodeDockerUnavailable = "docker_unavailable"
	CodeForbidden         = "forbidden"
	CodeUnauthorized      = "unauthorized"
	CodeRateLimited       = "rate_limited"
	CodeInternal          = "internal"
	CodeAgentUnavailable  = "agent_unavailable"
	CodeInsufficientSpace = "insufficient_space"
)
