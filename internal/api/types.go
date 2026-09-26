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
	// JoinAddress is the server's address under the machine's name, such as
	// survival.alex.playkeeper.io, once its DNS records work; empty until
	// then, when players use the IP address and port.
	JoinAddress string `json:"joinAddress,omitempty"`
	// WorldBytes is the world's size on disk (all its dimensions), measured
	// every few minutes.
	WorldBytes      *int64     `json:"worldBytes,omitempty"`
	PendingRestart  bool       `json:"pendingRestart"`
	CollectingSince *time.Time `json:"collectingSince,omitempty"`
	FirstSteps      FirstSteps `json:"firstSteps"`
	// Refusal is the file that stopped the server's last start, while the
	// server stays stopped.
	Refusal *FileRefusal `json:"refusal,omitempty"`
}

// FileRefusal is a file in the server's folder that Playkeeper would not
// follow or change. Code is stable ("link", "special_file", "not_a_file",
// …) for the dashboard to translate with Params, which always has "path"
// and, for some codes, "type" or "limit". Message and Hint say the same in
// English.
type FileRefusal struct {
	Code    string            `json:"code"`
	Params  map[string]string `json:"params"`
	Message string            `json:"message"`
	Hint    string            `json:"hint,omitempty"`
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
	// Params carries the values a translated message needs, such as
	// retryAfterSeconds.
	Params map[string]any `json:"params,omitempty"`
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
	// CodeNamesUnreachable: the free address service could not be reached.
	CodeNamesUnreachable = "names_unreachable"
	// CodeRetryLater: the certificate authority refuses attempts until
	// params.retryAt.
	CodeRetryLater  = "retry_later"
	CodeIconInvalid = "icon_invalid"
)

// Kinds of machine address.
const (
	AddressNone       = ""
	AddressPlaykeeper = "playkeeper"
	AddressOwn        = "own"
)

// Address is how people reach a machine by name instead of its IP address:
// a free playkeeper.io address or the admin's own domain. Each server's join
// address and the dashboard's certificate follow from it.
type Address struct {
	// Kind is AddressNone, AddressPlaykeeper or AddressOwn.
	Kind string `json:"kind"`
	// Host is the machine's name, such as alex.playkeeper.io or
	// play.example.com.
	Host  string     `json:"host,omitempty"`
	Since *time.Time `json:"since,omitempty"`
	// IP is the machine's public address as far as Playkeeper can tell (the
	// one the dashboard was opened with, or a network interface's). It keeps
	// working next to any name.
	IP        string `json:"ip,omitempty"`
	PanelPort int    `json:"panelPort"`
	// Base is the domain free addresses live under.
	Base string `json:"base"`
	// Servers are the servers' join addresses, in display order.
	Servers []JoinAddress `json:"servers"`
	// Free is the free address as the names service last described it.
	Free *FreeAddress `json:"free,omitempty"`
	// Records are the DNS records the own domain needs; Check is the last
	// look at them.
	Records []DNSRecord   `json:"records,omitempty"`
	Check   *AddressCheck `json:"check,omitempty"`
	// Certificate is the dashboard's certificate for Host.
	Certificate *CertificateStatus `json:"certificate,omitempty"`
	Names       NamesService       `json:"names"`
	// TermsAccepted is when an admin accepted Let's Encrypt's terms.
	TermsAccepted *time.Time `json:"termsAccepted,omitempty"`
	// Operation is the address's work in progress: publishing a free
	// address or getting a certificate.
	Operation *Operation `json:"operation,omitempty"`
}

// JoinAddress is what players type to join one server.
type JoinAddress struct {
	ServerID string `json:"serverId"`
	Name     string `json:"name"`
	Port     int    `json:"port"`
	// Label is the server's part of its address: "survival" in
	// survival.alex.playkeeper.io.
	Label string `json:"label"`
	// Address is the friendly address, empty without a machine name. Direct
	// is the IP address with the port, which always works.
	Address string `json:"address,omitempty"`
	Direct  string `json:"direct,omitempty"`
	// Published: Address works (a free address's records are published, or
	// the last check found an own domain's).
	Published bool `json:"published"`
}

// FreeAddress is a free playkeeper.io address at the names service.
type FreeAddress struct {
	Name string `json:"name"`
	// State is "active", "lapsed" (its records were removed, for the
	// reason in LapseReason) or "released".
	State string `json:"state"`
	// LapseReason is "not_refreshed" (the machine did not refresh the name
	// for a month) or "no_answer" (the names service could not reach the
	// dashboard on port 8443 for a week).
	LapseReason string `json:"lapseReason,omitempty"`
	// ServersWait is why the servers have no address under the name yet:
	// "server_address_not_yet" until ServersFrom, a few days after the
	// claim, or "not_answering" until the names service has reached the
	// dashboard on port 8443. Players join at the name with the server's
	// port meanwhile.
	ServersWait string     `json:"serversWait,omitempty"`
	ServersFrom *time.Time `json:"serversFrom,omitempty"`
	// DNS is "ok" once the name's own records are published, else
	// "pending".
	DNS         string    `json:"dns"`
	IPv4        string    `json:"ipv4,omitempty"`
	IPv6        string    `json:"ipv6,omitempty"`
	ClaimedAt   time.Time `json:"claimedAt"`
	RefreshedAt time.Time `json:"refreshedAt"`
	// StoppedAt is when a lapsed name stopped pointing at the machine.
	StoppedAt *time.Time `json:"stoppedAt,omitempty"`
	// CheckedAt is when the names service last answered about it.
	CheckedAt time.Time `json:"checkedAt"`
	// HoldDays is how long a released name is held from others.
	HoldDays int `json:"holdDays"`
}

// NamesService is the service behind free addresses.
type NamesService struct {
	URL string `json:"url"`
	// Unreachable: the last request could not reach it; Error says how.
	Unreachable bool       `json:"unreachable,omitempty"`
	Error       string     `json:"error,omitempty"`
	CheckedAt   *time.Time `json:"checkedAt,omitempty"`
}

// NameAvailability says whether a free address can be claimed.
type NameAvailability struct {
	Name      string `json:"name"`
	Address   string `json:"address,omitempty"`
	Available bool   `json:"available"`
	// Code and Params say why not: invalid_name, name_reserved, name_taken
	// or name_held (params.until is a Unix time).
	Code    string         `json:"code,omitempty"`
	Message string         `json:"message,omitempty"`
	Params  map[string]any `json:"params,omitempty"`
	// Suggestions are similar names that are free right now.
	Suggestions []string `json:"suggestions,omitempty"`
}

// DNSRecord is a record to create where the own domain's DNS is managed.
type DNSRecord struct {
	// ServerID is the server an SRV record is for.
	ServerID string    `json:"serverId,omitempty"`
	Type     string    `json:"type"`
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	TTL      int       `json:"ttl"`
	SRV      *SRVParts `json:"srv,omitempty"`
}

// SRVParts are an SRV record's fields, for DNS providers that ask for them
// one by one.
type SRVParts struct {
	Service  string `json:"service"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
	Port     int    `json:"port"`
	Target   string `json:"target"`
}

// Note is something to show about a name or a certificate. Code, with
// Params, is what the UI translates; Message and Hint are the English text.
type Note struct {
	Code    string            `json:"code"`
	Params  map[string]string `json:"params,omitempty"`
	Message string            `json:"message"`
	Hint    string            `json:"hint,omitempty"`
}

// AddressCheck is a look at the own domain's records, as players and Let's
// Encrypt see them.
type AddressCheck struct {
	At      time.Time     `json:"at"`
	Name    NameCheck     `json:"name"`
	Records []RecordCheck `json:"records,omitempty"`
	// Ready: the name points here and every SRV record is right.
	Ready bool `json:"ready"`
}

// NameCheck is where the own domain points, compared with this machine.
type NameCheck struct {
	Note
	Name    string       `json:"name"`
	OK      bool         `json:"ok"`
	Records []AddrRecord `json:"records,omitempty"`
}

// AddrRecord is an A or AAAA record found for a name.
type AddrRecord struct {
	Type string `json:"type"`
	Addr string `json:"addr"`
	// Here: the address is this machine's.
	Here bool   `json:"here"`
	Kind string `json:"kind,omitempty"`
}

// RecordCheck is the state of one SRV record.
type RecordCheck struct {
	Note
	Record DNSRecord `json:"record"`
	OK     bool      `json:"ok"`
	Found  []string  `json:"found,omitempty"`
}

// CertificateStatus is the dashboard's certificate for the machine's name,
// and how getting or renewing it went.
type CertificateStatus struct {
	Names []string `json:"names"`
	// Challenge is how Let's Encrypt checks the name: "http-01" (port 80)
	// or "dns-01" (a record the names service publishes).
	Challenge   string              `json:"challenge"`
	NotBefore   *time.Time          `json:"notBefore,omitempty"`
	NotAfter    *time.Time          `json:"notAfter,omitempty"`
	RenewAt     *time.Time          `json:"renewAt,omitempty"`
	Issuer      string              `json:"issuer,omitempty"`
	LastAttempt *time.Time          `json:"lastAttempt,omitempty"`
	NextAttempt *time.Time          `json:"nextAttempt,omitempty"`
	Failures    int                 `json:"failures,omitempty"`
	Problem     *CertificateProblem `json:"problem,omitempty"`
}

// CertificateProblem is why the last attempt failed.
type CertificateProblem struct {
	Note
	// RetryAt is the earliest time another attempt can succeed.
	RetryAt     *time.Time `json:"retryAt,omitempty"`
	NeedsAction bool       `json:"needsAction,omitempty"`
	// Detail is the certificate authority's or the system's own words.
	Detail string `json:"detail,omitempty"`
}

// AddressPlan is what a domain would need, before it is saved.
type AddressPlan struct {
	Domain  string        `json:"domain"`
	Records []DNSRecord   `json:"records"`
	Servers []JoinAddress `json:"servers"`
}

// Address requests. PanelHost is the host the dashboard was opened with;
// the panel adds it, as it adds the actor.
type AddressClaimRequest struct {
	Name        string `json:"name"`
	AcceptTerms bool   `json:"acceptTerms"`
	PanelHost   string `json:"panelHost"`
	Actor       string `json:"actor"`
}

type AddressCheckRequest struct {
	Domain      string `json:"domain"`
	AcceptTerms bool   `json:"acceptTerms"`
	PanelHost   string `json:"panelHost"`
	Actor       string `json:"actor"`
}

type AddressActionRequest struct {
	AcceptTerms bool   `json:"acceptTerms"`
	PanelHost   string `json:"panelHost"`
	Actor       string `json:"actor"`
}
