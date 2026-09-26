// Package worldimport prepares a Minecraft world someone uploads, such as a
// singleplayer save, a world downloaded from another host or a whole server
// folder, to run on one Playkeeper server.
//
// Inspect reads the uploaded archives without extracting them, refuses
// anything unsafe, and lists the Java worlds inside with what their level.dat
// says. Plan compares the chosen world with the target server (type,
// Minecraft version, level-name) and returns a Preview: what will be written,
// what is left out, the settings worth carrying over, and any warnings or
// problems. Stage writes exactly that into a new directory, in the layout the
// server type expects. The caller swaps it in, keeps a rollback archive and
// persists what it needs; this package keeps no state and touches nothing
// but the paths it is given.
package worldimport

import (
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kinds of refusals, problems, warnings and left-out files. The UI translates
// them by kind; the comment lists the parameters each one carries.
const (
	KindArchiveName      = "archive_name"        // name
	KindArchiveFormat    = "archive_format"      // name, format
	KindBedrock          = "bedrock_world"       //
	KindArchiveCorrupt   = "archive_corrupt"     // name
	KindArchiveTruncated = "archive_truncated"   // name
	KindTooManyArchives  = "too_many_archives"   // limit
	KindTooManyEntries   = "too_many_entries"    // limit
	KindUnsafePath       = "unsafe_path"         // path
	KindPathTooLong      = "path_too_long"       // path, limit
	KindLink             = "archive_link"        // path
	KindSpecialFile      = "special_file"        // path
	KindSparseFile       = "sparse_file"         // path
	KindEncrypted        = "archive_encrypted"   // name
	KindCompression      = "archive_compression" // name, method
	KindRatio            = "compression_ratio"   // name, limit
	KindDuplicate        = "duplicate_entry"     // path
	KindOverlap          = "overlapping_entries" // name
	KindArchiveChanged   = "archive_changed"     // name
	KindNoWorld          = "no_world"            //
	KindTooManyWorlds    = "too_many_worlds"     // limit
	KindChooseWorld      = "choose_world"        // count
	KindUnknownWorld     = "unknown_world"       // world
	KindTargetType       = "target_type"         // type
	KindTargetVersion    = "target_version"      // version
	KindTargetLevelName  = "target_level_name"   // levelName
	KindBlocked          = "import_blocked"      // problems
	KindFolderLink       = "world_folder_link"   // folder

	// Problems: the world can't be imported into this target.
	KindWorldNewer      = "world_newer"      // world, target
	KindWorldSeries     = "world_series"     // world, series
	KindLevelUnreadable = "level_unreadable" // detail
	KindSpigotLayout    = "spigot_layout"    //
	KindMixedLayout     = "mixed_layout"     // dimension
	KindTooLarge        = "too_large"        // bytes, limit
	KindTooManyFiles    = "too_many_files"   // files, limit
	KindFileTooLarge    = "file_too_large"   // path, bytes, limit

	// Warnings: the import works, but the owner should know.
	KindUpgrade          = "world_upgrade"         // world, target
	KindNoVersion        = "world_no_version"      //
	KindUnknownVersion   = "world_unknown_version" // dataVersion, target
	KindSnapshot         = "world_snapshot"        // world
	KindExperimental     = "experimental_features" // features
	KindModded           = "modded_world"          // brands, target
	KindCustomDimensions = "custom_dimensions"     // dimensions
	KindBukkitSplit      = "bukkit_split"          // levelName
	KindLayoutUpgrade    = "layout_upgrade"        // target
	KindMerged           = "dimensions_merged"     // folders
	KindPaperFiles       = "paper_files_moved"     // target
	KindStaleDimension   = "stale_dimension"       // dimension, kept, dropped
	KindCompanionGuessed = "companion_guessed"     // folder, dimension
	KindOrphanDimension  = "orphan_dimension"      // folder
	KindBedrockIgnored   = "bedrock_ignored"       // count
	KindLevelBackup      = "level_backup_used"     //
	KindOwnerData        = "singleplayer_owner"    // owner
	KindOfflinePlayers   = "offline_players"       // count
	KindAddonsKept       = "addons_kept"           // folder
	KindOperatorsKept    = "operators_kept"        // operators
	KindHardcore         = "hardcore"              //
	KindNoTerrain        = "no_terrain"            //

	// Left out of the staged world.
	LeftSessionLock    = "session_lock"
	LeftServerSoftware = "server_software"
	LeftLogs           = "logs"
	LeftCaches         = "caches"
	LeftServerConfig   = "server_config"
	LeftAddons         = "addons"
	LeftPlayerLists    = "player_lists"
	LeftOperators      = "operators"
	LeftOtherWorlds    = "other_worlds"
	LeftStaleCopies    = "stale_copies"
	LeftCompanionFiles = "companion_files"
	LeftSystemFiles    = "system_files"
	LeftOther          = "other"
)

// Message is one finding the UI shows: a stable Kind and its Params for
// translation, an English Text, and a Hint of what to do about it where one
// helps.
type Message struct {
	Kind   string         `json:"kind"`
	Params map[string]any `json:"params,omitempty"`
	Text   string         `json:"text"`
	Hint   string         `json:"hint,omitempty"`
}

// note builds a Message from key/value pairs of parameters.
func note(kind, hint, text string, kv ...any) Message {
	m := Message{Kind: kind, Text: text, Hint: hint}
	if len(kv) > 0 {
		m.Params = make(map[string]any, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			m.Params[kv[i].(string)] = kv[i+1]
		}
	}
	return m
}

// Error is a refusal a user may see. Msg is a full sentence and Hint says
// what to do next; Kind and Params identify it for translation.
type Error struct {
	Kind   string         `json:"kind"`
	Params map[string]any `json:"params,omitempty"`
	Msg    string         `json:"message"`
	Hint   string         `json:"hint,omitempty"`
}

func (e *Error) Error() string { return e.Msg }

func refuse(kind, hint, msg string, kv ...any) *Error {
	m := note(kind, hint, msg, kv...)
	return &Error{Kind: kind, Params: m.Params, Msg: msg, Hint: hint}
}

// clip makes a name from an upload safe to show: invalid UTF-8 and control
// or direction-changing characters become "�", and anything over 80
// characters is shortened in the middle so a hostile name can't flood the
// screen.
func clip(s string) string {
	s = strings.Map(func(r rune) rune {
		if unsafeRune(r) {
			return utf8.RuneError
		}
		return r
	}, strings.ToValidUTF8(s, string(utf8.RuneError)))
	if r := []rune(s); len(r) > 80 {
		s = string(r[:38]) + "…" + string(r[len(r)-38:])
	}
	return s
}

// quoted returns clip(s) in quotation marks, for a message.
func quoted(s string) string { return "“" + clip(s) + "”" }

// unsafeRune reports control characters and the invisible characters that
// change the direction of text, which can disguise a name.
func unsafeRune(r rune) bool {
	switch {
	case unicode.IsControl(r):
		return true
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0x061C:
		return true
	}
	return false
}

// plural returns "1 file" or "3 files".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// humanBytes formats a size the way the UI does: 1.5 GB, 820 MB.
func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return plural(int(n), "byte", "bytes")
	}
	v, i := float64(n), 0
	for v >= unit && i < 4 {
		v /= unit
		i++
	}
	s := fmt.Sprintf("%.1f", v)
	s = strings.TrimSuffix(s, ".0")
	return s + " " + []string{"", "KB", "MB", "GB", "TB"}[i]
}

// Limits bound what an import may read and write. The caller lowers
// MaxTotalBytes to the disk space it can spare. Zero fields take their
// DefaultLimits value.
type Limits struct {
	MaxEntries    int   // entries listed across all archives of an import
	MaxFiles      int   // files Stage writes
	MaxTotalBytes int64 // bytes Stage writes
	MaxFileBytes  int64 // bytes of one file Stage writes
	MaxPathLen    int   // bytes in the path of one entry
	// MaxRatio is how many times its compressed size an archive, or one
	// file over 1 MiB in it, may expand to.
	MaxRatio      int64
	MaxLevelBytes int64 // decoded size budget of one level.dat, see nbt.Limits
	MaxWorlds     int   // level.dat files an import may hold
}

// DefaultLimits suit a server with plenty of disk space; the caller lowers
// MaxTotalBytes to what it can spare.
func DefaultLimits() Limits {
	return Limits{
		MaxEntries:    500_000,
		MaxFiles:      200_000,
		MaxTotalBytes: 64 << 30,
		MaxFileBytes:  16 << 30,
		MaxPathLen:    1024,
		MaxRatio:      100,
		MaxLevelBytes: 16 << 20,
		MaxWorlds:     64,
	}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxEntries <= 0 {
		l.MaxEntries = d.MaxEntries
	}
	l.MaxEntries = min(l.MaxEntries, math.MaxInt32)
	if l.MaxFiles <= 0 {
		l.MaxFiles = d.MaxFiles
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = d.MaxTotalBytes
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = d.MaxFileBytes
	}
	if l.MaxPathLen <= 0 {
		l.MaxPathLen = d.MaxPathLen
	}
	if l.MaxRatio <= 0 {
		l.MaxRatio = d.MaxRatio
	}
	if l.MaxLevelBytes <= 0 {
		l.MaxLevelBytes = d.MaxLevelBytes
	}
	if l.MaxWorlds <= 0 {
		l.MaxWorlds = d.MaxWorlds
	}
	return l
}
