// Package api defines the JSON contract between the agent, the panel and the
// browser UI. web/src/api/types.ts mirrors these shapes; keep them in sync.
package api

import (
	"encoding/json"
	"time"
)

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
	// Refusal is the file that stopped the server's last start, while the
	// server stays stopped.
	Refusal *FileRefusal `json:"refusal,omitempty"`
	// SoftwareChanged is set when the server's software no longer matches
	// what Playkeeper installed, so it was not started.
	SoftwareChanged *SoftwareChange `json:"softwareChanged,omitempty"`
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
	// ResourcePack is the resource pack offered to players who join. An
	// offer with an empty SHA1 clears the pack from server.properties at the
	// next start; nil leaves server.properties alone.
	ResourcePack *ResourcePackOffer `json:"resourcePack,omitempty"`
	// Software pins the exact software of a type other than Paper: the
	// Minecraft version and the type's build. JarVerifiedAt is when its
	// install was last verified.
	Software *SoftwarePin `json:"software,omitempty"`
	// Modpack is the pack the server was created from (wave 4).
	Modpack *ServerModpack `json:"modpack,omitempty"`
	// Template is the template the server was created from (wave 4).
	Template *ServerTemplate `json:"template,omitempty"`
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
	// Build is the build of a type's software to pin (a Purpur build, a
	// Fabric or Quilt loader, a NeoForge version); empty takes the one the
	// catalog recommends. Paper and Vanilla have none.
	Build string `json:"build,omitempty"`
	// Modpack creates the server from a pack (wave 4), which decides the
	// type, versions and build: Type, VersionID and Build stay empty.
	Modpack *ModpackRef `json:"modpack,omitempty"`
	// Template creates the server from a template planned on this machine
	// (wave 4), which decides the type, version, build and settings: Type,
	// VersionID, Build, Modpack, PlayStyle, Gameplay, MOTD and MaxPlayers
	// stay empty.
	Template *TemplateRef `json:"template,omitempty"`
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
	// ReleasedAt is when Mojang released the Minecraft version.
	ReleasedAt *time.Time `json:"releasedAt,omitempty"`
	// Software is the pin for a type other than Paper, with the build the
	// catalog recommends; Build names that build.
	Software *SoftwarePin `json:"software,omitempty"`
	Build    string       `json:"build,omitempty"`
}

// ServerType is one kind of server software and whether it can be chosen yet.
type ServerType struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Available bool   `json:"available"`
	// Check is how the type's downloads are verified: full, weak_hash
	// (Purpur's MD5) or recorded_outputs (NeoForge's installer builds files
	// nobody publishes a hash for).
	Check string `json:"check,omitempty"`
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

	// LatestRelease is the newest Minecraft release Mojang lists, whether
	// or not the type offers it yet.
	LatestRelease string `json:"latestRelease,omitempty"`
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
	// Type and Build are the backup's server software when it is not Paper.
	Type  string `json:"type,omitempty"`
	Build string `json:"build,omitempty"`
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

// Add-ons are a server's plugins (Paper) or mods, installed from Modrinth
// and Hangar.

// AddonTarget is what a server's add-ons are and where they come from.
type AddonTarget struct {
	// Kind is plugin or mod; Folder is the folder they load from.
	Kind       string   `json:"kind"`
	Folder     string   `json:"folder"`
	Sources    []string `json:"sources"`
	Categories []string `json:"categories"`
	// MinecraftVersion is the version add-ons must fit.
	MinecraftVersion string `json:"minecraftVersion"`
}

// AddonNotice is a message from the add-on library: Kind and Params choose
// the translation, Message and Hint are the English text.
type AddonNotice struct {
	Kind    string            `json:"kind"`
	Params  map[string]string `json:"params,omitempty"`
	Message string            `json:"message"`
	Hint    string            `json:"hint,omitempty"`
	// URL is the page for a step Playkeeper can't do itself.
	URL string `json:"url,omitempty"`
}

// AddonVersion is one version of an add-on.
type AddonVersion struct {
	VersionID     string    `json:"versionId"`
	VersionNumber string    `json:"versionNumber"`
	Channel       string    `json:"channel"` // release | beta | alpha
	Published     time.Time `json:"published"`
	FileName      string    `json:"fileName,omitempty"`
	Size          int64     `json:"size,omitempty"`
	// ExternalURL is set when the version is only offered on another site.
	ExternalURL string `json:"externalUrl,omitempty"`
}

// Addon is an add-on Playkeeper installed on a server.
type Addon struct {
	Source        string    `json:"source"` // modrinth | hangar
	ProjectID     string    `json:"projectId"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	Summary       string    `json:"summary,omitempty"`
	IconURL       string    `json:"iconUrl,omitempty"`
	VersionID     string    `json:"versionId"`
	VersionNumber string    `json:"versionNumber"`
	Channel       string    `json:"channel"`
	Published     time.Time `json:"published"`
	FileName      string    `json:"fileName"`
	Size          int64     `json:"size"`
	// DependencyOf is the project id (same source) of the add-on this one
	// was installed for.
	DependencyOf string    `json:"dependencyOf,omitempty"`
	InstalledAt  time.Time `json:"installedAt"`
}

// AddonKey names an installed add-on.
type AddonKey struct {
	Source    string `json:"source"`
	ProjectID string `json:"projectId"`
}

// AddonFile is one jar in a server's add-on folder.
type AddonFile struct {
	FileName string `json:"fileName"`
	Size     int64  `json:"size"`
	// Status is managed (Playkeeper installed it, unchanged since),
	// modified (changed since), identified (added by hand, and Modrinth
	// knows it), unknown (added by hand) or pack (the server's modpack put
	// it there, AddonFromPack).
	Status string `json:"status"`
	// Addon is the record of a managed or modified file, or what Modrinth
	// knows an identified file as.
	Addon *Addon `json:"addon,omitempty"`
	// Name and Version are what the jar says about itself.
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	// Pending is set when the running server started before the file was
	// put in place, so it loads at the next restart.
	Pending bool `json:"pending,omitempty"`
}

// AddonFromPack is the status of a file the server's modpack put in the
// add-on folder: the pack keeps it, not the add-on library.
const AddonFromPack = "pack"

// Addons is what is in a server's add-on folder.
type Addons struct {
	Target AddonTarget `json:"target"`
	// Modpack is the pack the server runs, once its files are in place.
	Modpack *ServerModpack `json:"modpack,omitempty"`
	Files   []AddonFile    `json:"files"`
	// Missing are add-ons Playkeeper installed whose file is gone.
	Missing  []Addon       `json:"missing"`
	Warnings []AddonNotice `json:"warnings"`
	// RestartNeeded is set while the running server hasn't loaded the
	// latest change to its add-ons.
	RestartNeeded bool `json:"restartNeeded"`
}

// AddonUpdate says whether a newer version of an installed add-on fits the
// server.
type AddonUpdate struct {
	Source    string        `json:"source"`
	ProjectID string        `json:"projectId"`
	Latest    *AddonVersion `json:"latest,omitempty"`
	Available bool          `json:"available"`
	Notice    *AddonNotice  `json:"notice,omitempty"`
}

// AddonChecks is what the sources say about a server's add-ons: newer
// versions, and the files added by hand that Modrinth recognizes.
type AddonChecks struct {
	Updates    []AddonUpdate `json:"updates"`
	Identified []AddonFile   `json:"identified"`
	CheckedAt  time.Time     `json:"checkedAt"`
}

// AddonCard is an add-on in the library.
type AddonCard struct {
	Source     string    `json:"source"`
	ProjectID  string    `json:"projectId"`
	Slug       string    `json:"slug"`
	Name       string    `json:"name"`
	Author     string    `json:"author,omitempty"`
	Summary    string    `json:"summary"`
	Categories []string  `json:"categories"`
	License    string    `json:"license,omitempty"`
	Downloads  int64     `json:"downloads"`
	IconURL    string    `json:"iconUrl,omitempty"`
	Updated    time.Time `json:"updated"`
	PageURL    string    `json:"pageUrl"`
	// Installed is set when the server has it.
	Installed bool `json:"installed"`
}

// AddonBrowse is one page of the library for a server.
type AddonBrowse struct {
	Cards []AddonCard `json:"cards"`
	More  bool        `json:"more"`
	// Unanswered explains sources that failed while others answered.
	Unanswered []AddonNotice `json:"unanswered"`
}

// AddonStep is one file an install or update downloads.
type AddonStep struct {
	Action        string `json:"action"` // install | update
	Source        string `json:"source"`
	ProjectID     string `json:"projectId"`
	Name          string `json:"name"`
	VersionNumber string `json:"versionNumber"`
	Channel       string `json:"channel"`
	FileName      string `json:"fileName"`
	Size          int64  `json:"size"`
	// NeededBy is the add-on a dependency is installed for.
	NeededBy string `json:"neededBy,omitempty"`
	// Was is the version an update replaces.
	Was string `json:"was,omitempty"`
}

// AddonPlan is what an install or update would do. Ready is set when
// nothing blocks it; Fingerprint confirms it.
type AddonPlan struct {
	Steps []AddonStep `json:"steps"`
	// Manual are steps Playkeeper can't do, such as a download only on the
	// author's site.
	Manual      []AddonNotice `json:"manual"`
	Blockers    []AddonNotice `json:"blockers"`
	Warnings    []AddonNotice `json:"warnings"`
	Ready       bool          `json:"ready"`
	Fingerprint string        `json:"fingerprint"`
}

// AddonDetails is one add-on, for its detail sheet.
type AddonDetails struct {
	Card AddonCard `json:"card"`
	// Latest is the newest version that runs on the server, and Notes the
	// author's notes for it; Notice explains a missing one.
	Latest *AddonVersion `json:"latest,omitempty"`
	Notes  string        `json:"notes,omitempty"`
	Notice *AddonNotice  `json:"notice,omitempty"`
	// Installed is the add-on's record when Playkeeper installed it;
	// Changed is set when its file changed since, and UpdateAvailable when
	// Latest is newer.
	Installed       *Addon `json:"installed,omitempty"`
	Changed         bool   `json:"changed,omitempty"`
	Missing         bool   `json:"missing,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable,omitempty"`
	// Plan is what installing it would do, when it isn't installed;
	// PlanError says why it can't be installed.
	Plan      *AddonPlan   `json:"plan,omitempty"`
	PlanError *AddonNotice `json:"planError,omitempty"`
}

// AddonProgress is one file of an add-on install or update: the "files" of
// the operation's detail.
type AddonProgress struct {
	Name          string `json:"name"`
	VersionNumber string `json:"versionNumber"`
	Was           string `json:"was,omitempty"`
	NeededBy      string `json:"neededBy,omitempty"`
	Size          int64  `json:"size"`
	Received      int64  `json:"received"`
	// State is waiting, downloading, verified or failed.
	State string `json:"state"`
}

type AddonInstallRequest struct {
	Source    string `json:"source"`
	ProjectID string `json:"projectId"`
	// Fingerprint is the plan the user confirmed, AddonDetails.Plan; the
	// install is refused without it, or when the plan has changed since.
	Fingerprint string `json:"fingerprint"`
	Actor       string `json:"actor"`
}

// AddonUpdatePlanRequest asks what an update would do, for the user to
// confirm before sending the AddonUpdateRequest with the same add-ons and
// Changed.
type AddonUpdatePlanRequest struct {
	// Addons are the add-ons to update; empty means every one with an
	// update.
	Addons []AddonKey `json:"addons,omitempty"`
	// Changed replaces files that changed since they were installed.
	Changed bool   `json:"changed,omitempty"`
	Actor   string `json:"actor"`
}

type AddonUpdateRequest struct {
	Addons  []AddonKey `json:"addons,omitempty"`
	Changed bool       `json:"changed,omitempty"`
	// Fingerprint is the plan the user confirmed; the update is refused
	// without it, or when the plan has changed since.
	Fingerprint string `json:"fingerprint"`
	Actor       string `json:"actor"`
}

// AddonRemovePreview is what removing an add-on would involve.
type AddonRemovePreview struct {
	Addon Addon `json:"addon"`
	// NeededBy names the installed add-ons that need it.
	NeededBy []string `json:"neededBy"`
	// Orphans are dependencies nothing else would need any more.
	Orphans []Addon `json:"orphans"`
	// ConfigFolder is the plugin's settings folder, when it has one.
	ConfigFolder string `json:"configFolder,omitempty"`
	Changed      bool   `json:"changed"`
	Missing      bool   `json:"missing"`
}

type AddonRemoveRequest struct {
	Source    string `json:"source"`
	ProjectID string `json:"projectId"`
	// KeepConfig keeps the plugin's settings folder.
	KeepConfig bool `json:"keepConfig"`
	// Force removes it although installed add-ons need it.
	Force bool `json:"force,omitempty"`
	// Changed deletes the file although it changed since it was installed.
	Changed bool `json:"changed,omitempty"`
	// Orphans are dependencies to remove with it.
	Orphans []AddonKey `json:"orphans,omitempty"`
	Actor   string     `json:"actor"`
}

// AddonRemoval is what a removal did.
type AddonRemoval struct {
	Removed  []string      `json:"removed"`
	Warnings []AddonNotice `json:"warnings"`
}

// AddonAdoptRequest lets Playkeeper manage a file added by hand that
// Modrinth recognizes.
type AddonAdoptRequest struct {
	FileName string `json:"fileName"`
	Actor    string `json:"actor"`
}

// AddonForgetRequest drops the record of an add-on whose file is gone.
type AddonForgetRequest struct {
	Source    string `json:"source"`
	ProjectID string `json:"projectId"`
	Actor     string `json:"actor"`
}

// Pre-generating a server's map with Chunky.

// PregenPreset is a ready-made area around spawn and what it's expected to
// take on this machine.
type PregenPreset struct {
	ID        string `json:"id"` // small | medium | large | huge
	Radius    int    `json:"radius"`
	Chunks    int64  `json:"chunks"`
	Seconds   int64  `json:"seconds"`
	DiskBytes int64  `json:"diskBytes"`
	// Fits is false when the disk has no room for it.
	Fits bool `json:"fits"`
}

// Pregen is where pre-generating a server's map stands.
type Pregen struct {
	// State is idle (also after a cancel), starting (Playkeeper is
	// installing Chunky or restarting the server for it), running, paused
	// or finished.
	State string `json:"state"`
	// Step is where starting stands: installing (Chunky), restarting (the
	// server, to load it), starting_server or starting_task.
	Step   string `json:"step,omitempty"`
	World  string `json:"world"`
	Preset string `json:"preset,omitempty"`
	Radius int    `json:"radius,omitempty"`
	// Chunks of Total are done; ETASeconds is -1 while unknown.
	Chunks         int64   `json:"chunks"`
	Total          int64   `json:"total"`
	Percent        float64 `json:"percent"`
	Rate           float64 `json:"rate,omitempty"`
	ETASeconds     int64   `json:"etaSeconds"`
	ElapsedSeconds int64   `json:"elapsedSeconds,omitempty"`
	// PausedBy says why a paused task waits: "user" (until someone
	// resumes it), "players" (until the server has been empty a while;
	// PausedFor is one of them) or "server" (until the server starts).
	PausedBy        string     `json:"pausedBy,omitempty"`
	PausedFor       string     `json:"pausedFor,omitempty"`
	PauseForPlayers bool       `json:"pauseForPlayers"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	// DiskBytes is how much the world grew, once finished.
	DiskBytes *int64 `json:"diskBytes,omitempty"`
	// Installed is set when Chunky is in the plugins folder.
	Installed     bool           `json:"installed"`
	Presets       []PregenPreset `json:"presets"`
	DiskFreeBytes *int64         `json:"diskFreeBytes,omitempty"`
	// Error is why the last start failed.
	Error string `json:"error,omitempty"`
}

type PregenStartRequest struct {
	Preset          string `json:"preset"`
	PauseForPlayers bool   `json:"pauseForPlayers"`
	Actor           string `json:"actor"`
}

// Resource and data packs.

// ResourcePackOffer is the resource pack a server offers players when they
// join, from the panel's public /resource-packs/ route.
type ResourcePackOffer struct {
	SHA1        string `json:"sha1"`
	FileName    string `json:"fileName"`
	Size        int64  `json:"size"`
	Description string `json:"description,omitempty"`
	// Icon is set when the pack has a pack.png to show.
	Icon     bool      `json:"icon,omitempty"`
	AddedAt  time.Time `json:"addedAt"`
	URL      string    `json:"url"`
	Required bool      `json:"required"`
	Prompt   string    `json:"prompt,omitempty"`
}

// ResourcePack is a server's resource pack.
type ResourcePack struct {
	Offer *ResourcePackOffer `json:"offer,omitempty"`
	// Pending is set while the running server offers something else: a
	// restart applies the change.
	Pending bool `json:"pending"`
}

type ResourcePackSettingsRequest struct {
	Required bool   `json:"required"`
	Prompt   string `json:"prompt"`
	Actor    string `json:"actor"`
}

// ActiveResourcePacks are the SHA-1 hashes of the packs servers offer,
// which the panel serves to players without sign-in.
type ActiveResourcePacks struct {
	SHA1 []string `json:"sha1"`
}

// DataPack is a data pack in a server's world.
type DataPack struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Icon is set when the pack has a pack.png to show.
	Icon bool  `json:"icon,omitempty"`
	Size int64 `json:"size"`
	// Enabled is known only while the server is online.
	Enabled *bool `json:"enabled,omitempty"`
	// Folder packs are listed but left alone.
	Folder  bool      `json:"folder,omitempty"`
	AddedAt time.Time `json:"addedAt"`
}

// DataPacks are the data packs of a server's world.
type DataPacks struct {
	Packs []DataPack `json:"packs"`
	// Live is set while the server is online and can switch packs.
	Live bool `json:"live"`
	// Added names the pack an upload added or replaced.
	Added string `json:"added,omitempty"`
	// NotEnabled is set when that pack could not be switched on, and
	// Problem says why.
	NotEnabled bool   `json:"notEnabled,omitempty"`
	Problem    string `json:"problem,omitempty"`
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
	CodeIconInvalid       = "icon_invalid"
)

// Wave 4: every server type.

// CodeUpstream is an error from a download site Playkeeper reads (Mojang,
// Fabric, Modrinth and the others): unreachable, or answering with
// something Playkeeper refuses to use.
const CodeUpstream = "upstream_unavailable"

// SoftwarePin is the exact software of a server type other than Paper. It
// mirrors software.Pin field for field; only its own type's build is set.
type SoftwarePin struct {
	Type             string `json:"type"`
	MinecraftVersion string `json:"minecraftVersion"`
	PurpurBuild      int    `json:"purpurBuild,omitempty"`
	FabricLoader     string `json:"fabricLoader,omitempty"`
	QuiltLoader      string `json:"quiltLoader,omitempty"`
	NeoForgeVersion  string `json:"neoforgeVersion,omitempty"`
}

// SoftwareBuild is one build of a type's software for a Minecraft version:
// a Purpur build, a Fabric or Quilt loader, or a NeoForge version.
type SoftwareBuild struct {
	Version     string `json:"version"`
	Channel     string `json:"channel"`
	Recommended bool   `json:"recommended"`
}

// SoftwareBuilds lists a type's builds for one Minecraft version, newest
// first.
type SoftwareBuilds struct {
	Type             string          `json:"type"`
	MinecraftVersion string          `json:"minecraftVersion"`
	Builds           []SoftwareBuild `json:"builds"`
	CheckedAt        time.Time       `json:"checkedAt"`
}

// SoftwareChange says which of a server's software files no longer matches
// what Playkeeper installed and recorded. Hashes are lowercase hex; Found is
// empty when the file is missing.
type SoftwareChange struct {
	File        string     `json:"file"`
	Algorithm   string     `json:"algorithm"`
	Recorded    string     `json:"recorded"`
	Found       string     `json:"found,omitempty"`
	InstalledAt *time.Time `json:"installedAt,omitempty"`
	ChangedAt   *time.Time `json:"changedAt,omitempty"`
	DetectedAt  time.Time  `json:"detectedAt"`
	// Software is what a reinstall downloads again: "Paper 26.1.2 build 41".
	Software string `json:"software"`
}

// Wave 4: modpacks.

// ModpackCard is a pack in the create flow's list.
type ModpackCard struct {
	Source            string    `json:"source"`
	ProjectID         string    `json:"projectId"`
	Slug              string    `json:"slug"`
	Name              string    `json:"name"`
	Author            string    `json:"author,omitempty"`
	Summary           string    `json:"summary"`
	Downloads         int64     `json:"downloads"`
	IconURL           string    `json:"iconUrl,omitempty"`
	Updated           time.Time `json:"updated"`
	PageURL           string    `json:"pageUrl"`
	Types             []string  `json:"types"`
	MinecraftVersions []string  `json:"minecraftVersions"`
	// Mods counts the mods the pack's newest version bundles, and MemoryMB
	// is the memory Playkeeper suggests for that many; both are 0 when the
	// source doesn't say.
	Mods     int `json:"mods,omitempty"`
	MemoryMB int `json:"memoryMB,omitempty"`
	// Unavailable says why Playkeeper can't install the pack at all.
	Unavailable *AddonNotice `json:"unavailable,omitempty"`
}

// ModpackResults is one page of packs from one source.
type ModpackResults struct {
	Cards  []ModpackCard `json:"cards"`
	Total  int           `json:"total"`
	Offset int           `json:"offset"`
	Limit  int           `json:"limit"`
	// Sources are where packs can come from on this machine: modrinth, and
	// curseforge once there's a key.
	Sources []string `json:"sources"`
}

// ModpackVersion is one version of a pack.
type ModpackVersion struct {
	ID               string       `json:"id"`
	Number           string       `json:"number"`
	Name             string       `json:"name,omitempty"`
	Channel          string       `json:"channel"`
	Published        time.Time    `json:"published"`
	Size             int64        `json:"size"`
	Type             string       `json:"type,omitempty"`
	MinecraftVersion string       `json:"minecraftVersion,omitempty"`
	Mods             int          `json:"mods,omitempty"`
	Unsupported      *AddonNotice `json:"unsupported,omitempty"`
}

// ModpackDetail is a pack's details sheet.
type ModpackDetail struct {
	ModpackCard
	SourceURL string `json:"sourceUrl,omitempty"`
	IssuesURL string `json:"issuesUrl,omitempty"`
	WikiURL   string `json:"wikiUrl,omitempty"`
	// Headline is the mod the pack is built around ("Cobblemon"), when its
	// title names one it bundles.
	Headline string           `json:"headline,omitempty"`
	Versions []ModpackVersion `json:"versions"`
	// Newest is the id of the version a new server gets; empty when
	// Playkeeper can install none.
	Newest string `json:"newest,omitempty"`
}

// ModpackPreview is what a pack version needs and puts on a new server, read
// from the pack itself.
type ModpackPreview struct {
	Type             string `json:"type"`
	MinecraftVersion string `json:"minecraftVersion"`
	// LoaderVersion is the Fabric Loader, Quilt Loader or NeoForge version;
	// empty for vanilla packs.
	LoaderVersion string `json:"loaderVersion,omitempty"`
	// Java is set when the pack's Minecraft version runs on an older Java
	// than the newest versions, as its loader and mods need: the server
	// gets the runtime image with that Java.
	Java int `json:"java,omitempty"`
	// Files counts the files the pack puts on the server; DownloadSize is
	// what downloading them takes.
	Files        int           `json:"files"`
	DownloadSize int64         `json:"downloadSize"`
	Ready        bool          `json:"ready"`
	Blockers     []AddonNotice `json:"blockers"`
	Warnings     []AddonNotice `json:"warnings"`
	Manual       []AddonNotice `json:"manual"`
}

// ModpackRef names a pack version to create a server from.
type ModpackRef struct {
	Source    string `json:"source"`
	ProjectID string `json:"projectId"`
	VersionID string `json:"versionId"`
}

// ServerModpack is the pack a server runs.
type ServerModpack struct {
	Source        string `json:"source"`
	ProjectID     string `json:"projectId"`
	VersionID     string `json:"versionId"`
	Name          string `json:"name"`
	VersionNumber string `json:"versionNumber"`
	PageURL       string `json:"pageUrl,omitempty"`
	IconURL       string `json:"iconUrl,omitempty"`
	// Mods counts the pack's mods on the server.
	Mods int `json:"mods,omitempty"`
	// Pending is set until the pack's files are in place; the next start
	// puts them there.
	Pending bool `json:"pending,omitempty"`
}

// Wave 4: templates.

// TemplateRef names a template planned on this machine by the fingerprint
// of the plan the user confirmed (TemplatePlan.Fingerprint).
type TemplateRef struct {
	Fingerprint string `json:"fingerprint"`
}

// ServerTemplate is the template a server was created from.
type ServerTemplate struct {
	Name string `json:"name"`
	// Pending is set until the template's add-ons are on the server; the
	// next start installs the rest.
	Pending bool `json:"pending,omitempty"`
	// Skipped are the template's add-ons and data packs that could not be
	// installed, each with params["name"]; POST …/template/retry tries them
	// again.
	Skipped []AddonNotice `json:"skipped,omitempty"`
}

// TemplateSettings are the settings a template carries.
type TemplateSettings struct {
	Difficulty   string `json:"difficulty,omitempty"`
	PVP          *bool  `json:"pvp,omitempty"`
	GameMode     string `json:"gameMode,omitempty"`
	Hardcore     *bool  `json:"hardcore,omitempty"`
	ViewDistance int    `json:"viewDistance,omitempty"`
	LevelType    string `json:"levelType,omitempty"`
	MaxPlayers   int    `json:"maxPlayers,omitempty"`
	MOTD         string `json:"motd,omitempty"`
	PlayStyle    string `json:"playStyle,omitempty"`
	MemoryMB     int    `json:"memoryMB,omitempty"`
}

// TemplateAddon is a plugin, mod or modpack a template names.
type TemplateAddon struct {
	Source string `json:"source"`
	Name   string `json:"name"`
	// VersionNumber is the pinned version; empty means the newest that fits.
	VersionNumber string `json:"versionNumber,omitempty"`
}

// TemplateContents is what a template carries.
type TemplateContents struct {
	Name             string           `json:"name"`
	Type             string           `json:"type"`
	MinecraftVersion string           `json:"minecraftVersion"`
	Build            string           `json:"build,omitempty"`
	Settings         TemplateSettings `json:"settings"`
	Addons           []TemplateAddon  `json:"addons"`
	Modpack          *TemplateAddon   `json:"modpack,omitempty"`
	ResourcePacks    int              `json:"resourcePacks"`
	DataPacks        int              `json:"dataPacks"`
	// Packs names the packs, the resource pack first.
	Packs []string `json:"packs"`
}

// TemplateExport is a server's setup as a template, with the export
// dialog's choices applied.
type TemplateExport struct {
	FileName string `json:"fileName"`
	// File is the template file's text.
	File string `json:"file"`
	// Link is empty when the template is too large for one; LinkLong is set
	// when it's long enough for some chats to cut it.
	Link     string           `json:"link"`
	LinkLong bool             `json:"linkLong,omitempty"`
	Contents TemplateContents `json:"contents"`
	// Available is what the template carries with every part included, so
	// the dialog's rows keep their details while a part is left out.
	Available TemplateContents `json:"available"`
	// PacksHere counts the server's packs, with those that can't travel.
	PacksHere int           `json:"packsHere"`
	LeftOut   []AddonNotice `json:"leftOut"`
	Notes     []AddonNotice `json:"notes"`
}

// TemplatePlan is what a template would create on this machine.
type TemplatePlan struct {
	Contents TemplateContents `json:"contents"`
	// Type, VersionID, MinecraftVersion and Build are what the new server
	// runs, which can differ from what the template names.
	Type             string        `json:"type"`
	VersionID        string        `json:"versionId,omitempty"`
	MinecraftVersion string        `json:"minecraftVersion,omitempty"`
	Build            string        `json:"build,omitempty"`
	Experimental     bool          `json:"experimental,omitempty"`
	MemoryMB         int           `json:"memoryMB"`
	Skipped          []AddonNotice `json:"skipped"`
	Warnings         []AddonNotice `json:"warnings"`
	Blockers         []AddonNotice `json:"blockers"`
	Ready            bool          `json:"ready"`
	Fingerprint      string        `json:"fingerprint"`
}

// Wave 4: sharing a modded server's pack with friends.

// PackShare is a modded server's friends' share, for the Mods tab and the
// share sheet. Share is internal/modpacks/share's Share without its index.
type PackShare struct {
	// Public is whether the friends' page answers, and Token its link's
	// token while it does.
	Public     bool            `json:"public"`
	Token      string          `json:"token,omitempty"`
	File       string          `json:"file"`
	Size       int64           `json:"size"`
	LoaderName string          `json:"loaderName"`
	Share      json.RawMessage `json:"share"`
}

// PackShareRequest turns a server's friends' page on or off. Turning it on
// makes a new link; turning it off forgets the old one.
type PackShareRequest struct {
	Public bool   `json:"public"`
	Actor  string `json:"actor"`
}

// PackLink is what the agent tells the panel about a link that opens a
// friends' page: the server and its share. Share is the whole
// internal/modpacks/share Share, server-only mods included, so the panel
// never sends it on.
type PackLink struct {
	Server   string          `json:"server"`
	Slug     string          `json:"slug"`
	GamePort int             `json:"gamePort"`
	HasIcon  bool            `json:"hasIcon"`
	Share    json.RawMessage `json:"share"`
}
