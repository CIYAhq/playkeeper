// Package diagnose explains how a Minecraft server is doing in plain
// language: why it lags, why it stopped and whether its memory budget fits.
//
// Everything here is analysis over inputs the agent collects for one server:
// RCON replies, console lines, the JVM's GC log, /proc/stat, crash reports,
// container state and files in the server's data directory. Nothing in this
// package talks to Docker, RCON or the network.
//
// Every result carries a stable Kind with Params for the UI to translate, and
// an English Title and Explanation for now. Findings only state what their
// Evidence shows; where the evidence points at a likely cause, the wording
// says so instead of presenting it as certain.
package diagnose

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Evidence is one observation a finding rests on: a measurement or a line
// the server wrote.
type Evidence struct {
	Kind   EvidenceKind   `json:"kind"`
	Params map[string]any `json:"params,omitempty"`
	Text   string         `json:"text"`
}

// EvidenceKind identifies an Evidence item for translation.
type EvidenceKind string

const (
	EvidenceTickRate           EvidenceKind = "tick_rate"           // tps, target
	EvidenceTickTime           EvidenceKind = "tick_time"           // mspt, target_ms
	EvidenceOverloads          EvidenceKind = "overloads"           // count, max_behind_ms, minutes
	EvidencePlayers            EvidenceKind = "players_online"      // count
	EvidenceCPUSteal           EvidenceKind = "cpu_steal"           // percent
	EvidenceServerCPU          EvidenceKind = "server_cpu"          // percent (100 = one core), limit_cores
	EvidenceHostCPU            EvidenceKind = "host_cpu"            // busy_percent, others_percent
	EvidenceIOWait             EvidenceKind = "io_wait"             // percent
	EvidenceHeapAfterGC        EvidenceKind = "heap_after_gc"       // used_mb, heap_mb, percent
	EvidenceFullGC             EvidenceKind = "full_gc"             // count, minutes
	EvidenceEvacuationFailure  EvidenceKind = "evacuation_failure"  // count
	EvidenceGCPauses           EvidenceKind = "gc_pauses"           // percent, longest_ms
	EvidenceNewChunks          EvidenceKind = "new_chunks"          // count, minutes
	EvidenceViewDistance       EvidenceKind = "view_distance"       // value, default
	EvidenceSimulationDistance EvidenceKind = "simulation_distance" // value, default
	EvidenceLogLine            EvidenceKind = "log_line"            // line
	EvidenceCrashReport        EvidenceKind = "crash_report_line"   // file, line
	EvidenceExitCode           EvidenceKind = "exit_code"           // code
	EvidenceOOMKilled          EvidenceKind = "oom_killed"          // limit_mb
	EvidenceMemoryRoom         EvidenceKind = "memory_room"         // budget_mb, room_mb
	EvidenceFreeDisk           EvidenceKind = "free_disk"           // free_mb
	EvidenceJavaVersion        EvidenceKind = "java_version"        // required, available, jar
	EvidenceAddonFile          EvidenceKind = "addon_file"          // addon, jar
	EvidenceDockerError        EvidenceKind = "docker_error"        // message
	EvidenceHeapNeeded         EvidenceKind = "heap_needed"         // peak_mb, heap_mb, days
	EvidenceGCSamples          EvidenceKind = "gc_samples"          // windows, days
)

// Action is something the user can do about a finding. Params name the
// target, e.g. the jar to remove or the memory budget to switch to.
type Action struct {
	Kind        ActionKind     `json:"kind"`
	Params      map[string]any `json:"params,omitempty"`
	Title       string         `json:"title"`
	Recommended bool           `json:"recommended,omitempty"`
}

// ActionKind identifies an Action for translation and for the UI to wire the
// right button.
type ActionKind string

const (
	ActionRaiseMemory     ActionKind = "raise_memory"              // from_mb, to_mb
	ActionLowerMemory     ActionKind = "lower_memory"              // from_mb, to_mb
	ActionRestart         ActionKind = "restart"                   //
	ActionRemoveAddon     ActionKind = "remove_addon"              // jar
	ActionUpdateAddon     ActionKind = "update_addon"              // jar
	ActionInstallAddon    ActionKind = "install_addon"             // name
	ActionRemoveDatapack  ActionKind = "remove_datapack"           // pack: its name in the world's datapacks folder
	ActionRestoreBackup   ActionKind = "restore_backup"            //
	ActionFreeDisk        ActionKind = "free_disk"                 // free_mb
	ActionChangePort      ActionKind = "change_port"               // port
	ActionAcceptEULA      ActionKind = "accept_eula"               //
	ActionFixPermissions  ActionKind = "fix_permissions"           //
	ActionPregenerate     ActionKind = "pregenerate_world"         //
	ActionLowerView       ActionKind = "lower_view_distance"       // from, to
	ActionLowerSimulation ActionKind = "lower_simulation_distance" // from, to
	ActionRunProfiler     ActionKind = "run_profiler"              //
	ActionDedicatedCPU    ActionKind = "move_to_dedicated_cpu"     //
	ActionRaiseCPULimit   ActionKind = "raise_cpu_limit"           // cores
	ActionReduceOtherLoad ActionKind = "reduce_other_load"         //
	ActionUpgradeHost     ActionKind = "upgrade_host"              // resource: cpu, memory or disk
)

// ConsoleLine is a cleaned console line (minecraft.CleanLine) with the time
// Docker recorded for it.
type ConsoleLine struct {
	At   time.Time
	Text string
}

// logPrefix matches the log4j prefix of Paper ("[12:00:00 WARN]: "), vanilla
// and Fabric ("[12:00:00] [Server thread/WARN]: ") and NeoForge, which adds
// the logger ("[12:00:00] [main/ERROR] [ne.ne.fm.ModLoader/LOADING]: ").
var logPrefix = regexp.MustCompile(`^\[\d{2}:\d{2}:\d{2}(?: (INFO|WARN|ERROR|FATAL))?\](?: \[[^\]]{1,64}/(INFO|WARN|ERROR|FATAL)\])?(?: \[[^\]]{1,120}\])?: `)

// logLine is a console line split into its level and message. Lines without
// a log prefix are continuation lines (stack traces, multi-line messages) or
// come from the JVM and the image's scripts.
type logLine struct {
	level    string
	msg      string
	prefixed bool
}

func splitLog(line string) logLine {
	m := logPrefix.FindStringSubmatchIndex(line)
	if m == nil {
		return logLine{msg: line}
	}
	level := ""
	for _, g := range []int{2, 4} {
		if m[g] >= 0 {
			level = line[m[g]:m[g+1]]
		}
	}
	return logLine{level: level, msg: line[m[1]:], prefixed: true}
}

// trusted reports whether players cannot have written the line. Chat, /say
// and /me are logged at INFO behind a prefix, and chat cannot contain line
// breaks, so warnings, errors and continuation lines are the server's own.
func (l logLine) trusted() bool {
	return !l.prefixed || l.level == "WARN" || l.level == "ERROR" || l.level == "FATAL"
}

// sizeText renders megabytes the way the UI shows memory: "900 MB", "2 GB",
// "1.5 GB".
func sizeText(mb int) string {
	if mb < 1024 {
		return fmt.Sprintf("%d MB", mb)
	}
	return trimZero(float64(mb)/1024) + " GB"
}

// trimZero formats with one decimal and drops a trailing ".0".
func trimZero(v float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// minutesText renders a window length: "10 minutes", "1 minute", "90 seconds".
func minutesText(d time.Duration) string {
	if d < 2*time.Minute {
		s := int(d.Round(time.Second) / time.Second)
		return fmt.Sprintf("%d %s", s, plural(s, "second", "seconds"))
	}
	m := int(d.Round(time.Minute) / time.Minute)
	return fmt.Sprintf("%d %s", m, plural(m, "minute", "minutes"))
}

func logEvidence(line string) Evidence {
	return Evidence{Kind: EvidenceLogLine, Params: map[string]any{"line": line}, Text: line}
}
