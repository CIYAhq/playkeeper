// Package packs inspects, installs and serves Minecraft data packs and
// resource packs.
//
// A data pack changes game data (recipes, loot tables, advancements, world
// generation) and is installed into a world's datapacks folder, where the
// server loads it. A resource pack changes textures, sounds and models; the
// server only tells players where to download it, so Playkeeper stores it
// and serves it over plain HTTP on the panel's port. Every pack arrives as a
// zip file, which Inspect checks before anything else reads it.
//
// The package keeps no state: callers pass the server's data directory, its
// level-name and type, and a console to send commands to.
package packs

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/gamefiles"
)

// Kind is what a pack holds.
type Kind string

const (
	// Data is a data pack: game data in data/, installed into a world.
	Data Kind = "data"
	// Resource is a resource pack: textures, sounds and models in assets/,
	// which each player downloads.
	Resource Kind = "resource"
	// Both is a zip that holds a data pack and a resource pack at once.
	Both Kind = "both"
)

func (k Kind) holds(use Kind) bool { return k == use || k == Both }

// AnyMinor is the Minor of a range's upper end declared without one: the
// range covers every minor version of that Major.
const AnyMinor = math.MaxInt32

// Format is a pack format version. Major grows with Minecraft releases;
// since 1.21.9 some releases only raise Minor.
type Format struct {
	Major int
	Minor int
}

// String writes the format as the game does: "81.0", or "94.*" for
// AnyMinor.
func (f Format) String() string {
	if f.Minor == AnyMinor {
		return strconv.Itoa(f.Major) + ".*"
	}
	return strconv.Itoa(f.Major) + "." + strconv.Itoa(f.Minor)
}

func (f Format) MarshalText() ([]byte, error) { return []byte(f.String()), nil }

func (f *Format) UnmarshalText(b []byte) error {
	major, minor, ok := strings.Cut(string(b), ".")
	ma, err := strconv.Atoi(major)
	if !ok || err != nil || ma < math.MinInt32 || ma > math.MaxInt32 {
		return fmt.Errorf("pack format %q is not MAJOR.MINOR", b)
	}
	mi := AnyMinor
	if minor != "*" {
		if mi, err = strconv.Atoi(minor); err != nil || mi < 0 {
			return fmt.Errorf("pack format %q is not MAJOR.MINOR", b)
		}
	}
	*f = Format{ma, mi}
	return nil
}

func (f Format) compare(g Format) int {
	if c := cmp.Compare(f.Major, g.Major); c != 0 {
		return c
	}
	return cmp.Compare(f.Minor, g.Minor)
}

// FormatRange is the pack formats a pack says it works with.
type FormatRange struct {
	Min Format `json:"min"`
	Max Format `json:"max"`
}

// Contains reports whether a game whose pack format is f considers the
// pack made for it. The game still loads packs outside their range, after
// warning the player.
func (r FormatRange) Contains(f Format) bool {
	return r.Min.compare(f) <= 0 && f.compare(r.Max) <= 0
}

// Info is what Inspect found in a pack.
type Info struct {
	// Kind is what the zip holds, which may be more than it was inspected
	// for: a zip with data/ and assets/ is Both.
	Kind Kind `json:"kind"`
	// Description is the pack's description as plain text, without colors
	// or formatting codes. It may hold a line break.
	Description string `json:"description"`
	// Formats is the range of pack formats the pack declares, or nil when
	// the declaration breaks the game's rules. Minecraft still loads such a
	// pack, as one made for a newer version; FormatProblem is the game's
	// explanation, in English.
	Formats       *FormatRange `json:"formats,omitempty"`
	FormatProblem string       `json:"formatProblem,omitempty"`
	// Features are the experimental features the pack needs, such as
	// "minecraft:trade_rebalance". Worlds created without them can't enable
	// the pack.
	Features []string `json:"features,omitempty"`
	// Size is the zip's size in bytes, Files the number of files in it and
	// Unpacked their size once decompressed.
	Size     int64 `json:"size"`
	Files    int   `json:"files"`
	Unpacked int64 `json:"unpacked"`
	// SHA1 and SHA256 are the zip's hashes in lowercase hex. Players' games
	// check a downloaded resource pack against SHA1.
	SHA1   string `json:"sha1"`
	SHA256 string `json:"sha256"`
}

// ResourcePackMaxBytes is the largest resource pack players' games
// download; they refuse anything bigger.
const ResourcePackMaxBytes = 262_144_000

// Limits bound the zips Inspect accepts. Zero fields take the defaults.
type Limits struct {
	// MaxBytes is the largest zip, 250 MiB by default. Resource packs are
	// never allowed past ResourcePackMaxBytes.
	MaxBytes int64
	// MaxFiles is the most entries a zip may have, 100,000 by default.
	MaxFiles int
	// MaxUnpackedBytes is the most a zip's files may add up to once
	// decompressed, 2 GiB by default.
	MaxUnpackedBytes int64
}

// DefaultLimits are the limits a zero Limits stands for.
func DefaultLimits() Limits {
	return Limits{MaxBytes: ResourcePackMaxBytes, MaxFiles: 100_000, MaxUnpackedBytes: 2 << 30}
}

func (l Limits) orDefaults() Limits {
	d := DefaultLimits()
	if l.MaxBytes <= 0 {
		l.MaxBytes = d.MaxBytes
	}
	if l.MaxFiles <= 0 {
		l.MaxFiles = d.MaxFiles
	}
	if l.MaxUnpackedBytes <= 0 {
		l.MaxUnpackedBytes = d.MaxUnpackedBytes
	}
	return l
}

// maxBytes is the largest zip accepted for use.
func (l Limits) maxBytes(use Kind) int64 {
	if use == Resource {
		return min(l.MaxBytes, ResourcePackMaxBytes)
	}
	return l.MaxBytes
}

// Error codes: stable identifiers the UI translates, with Error.Params as
// the values.
const (
	CodeTooLarge               = "too_large"
	CodeNotZip                 = "not_zip"
	CodeCorrupt                = "corrupt"
	CodeTooManyFiles           = "too_many_files"
	CodeTooMuchData            = "too_much_data"
	CodeUnsafePath             = "unsafe_path"
	CodeUnsupportedEntry       = "unsupported_entry"
	CodeEncrypted              = "encrypted"
	CodeUnsupportedCompression = "unsupported_compression"
	CodeNoMcmeta               = "no_mcmeta"
	CodeInvalidMcmeta          = "invalid_mcmeta"
	CodeNoContent              = "no_content"
	CodeWrongKind              = "wrong_kind"
	CodeInvalidName            = "invalid_name"
	CodeInvalidLevel           = "invalid_level"
	CodeAlreadyInstalled       = "already_installed"
	CodeNotFound               = "not_found"
	CodeFolderPack             = "folder_pack"
	CodeFileFailed             = "file_failed"
	CodeFileRefused            = "file_refused"
	CodeInvalidOffer           = "invalid_offer"
	CodeInvalidHost            = "invalid_host"
	CodeInvalidPrompt          = "invalid_prompt"
	CodeUnsupportedServer      = "unsupported_server"
	CodeInvalidID              = "invalid_id"
	CodeUnknownPack            = "unknown_pack"
	CodeNeedsFeatures          = "needs_features"
	CodeFeaturePack            = "feature_pack"
	CodeNotApplied             = "not_applied"
	CodeUnexpectedReply        = "unexpected_reply"
	CodeConsole                = "console_failed"
	CodeNoIcon                 = "no_icon"
)

// Error is an error the UI can show: Code is a stable identifier to
// translate, Params its values, Msg the English sentence and Hint what to
// do next. errors.Is matches Errors with the same Code, so callers can test
// against the Err* values.
type Error struct {
	Code   string
	Params map[string]any
	Msg    string
	Hint   string
	Err    error
}

func (e *Error) Error() string { return e.Msg }

func (e *Error) Unwrap() error { return e.Err }

func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// Targets for errors.Is.
var (
	ErrTooLarge         = &Error{Code: CodeTooLarge}
	ErrWrongKind        = &Error{Code: CodeWrongKind}
	ErrAlreadyInstalled = &Error{Code: CodeAlreadyInstalled}
	ErrNotFound         = &Error{Code: CodeNotFound}
	ErrInvalidID        = &Error{Code: CodeInvalidID}
	ErrUnknownPack      = &Error{Code: CodeUnknownPack}
	ErrNotApplied       = &Error{Code: CodeNotApplied}
	ErrUnexpectedReply  = &Error{Code: CodeUnexpectedReply}
	ErrNoIcon           = &Error{Code: CodeNoIcon}
)

const hintRedownload = "Download the pack again from where you got it, or ask its author for a fixed version."

// tooLarge is the error for a pack of size bytes over the limit of max
// bytes. size is -1 when it is only known to be larger.
func tooLarge(size, max int64, use Kind) *Error {
	what := "The pack"
	switch use {
	case Data:
		what = "The data pack"
	case Resource:
		what = "The resource pack"
	}
	params := map[string]any{"max": max}
	if size >= 0 {
		params["size"] = size
	}
	// Sizes just over the limit round to the same figure.
	known := size >= 0 && formatSize(size) != formatSize(max)
	var msg, hint string
	if use == Resource && max == ResourcePackMaxBytes {
		msg = fmt.Sprintf("%s is larger than %s, the most players' games download.", what, formatSize(max))
		if known {
			msg = fmt.Sprintf("%s is %s, but players' games only download resource packs of up to %s.", what, formatSize(size), formatSize(max))
		}
		hint = "Remove textures or sounds you don't need, or use a lighter version of the pack."
	} else {
		msg = fmt.Sprintf("%s is larger than %s, the most Playkeeper accepts.", what, formatSize(max))
		if known {
			msg = fmt.Sprintf("%s is %s, more than the %s Playkeeper accepts.", what, formatSize(size), formatSize(max))
		}
		hint = "Use a smaller version of the pack."
	}
	return &Error{Code: CodeTooLarge, Params: params, Msg: msg, Hint: hint}
}

// fileFailed is the error for a file operation that failed on Playkeeper's
// side rather than because of the pack. what completes "Playkeeper
// couldn't …". A file in the server's folder that Playkeeper refused, such
// as a link (see internal/gamefiles), is named with what to do about it.
func fileFailed(what string, err error) *Error {
	var ge *gamefiles.Error
	if errors.As(err, &ge) {
		params := map[string]any{"kind": string(ge.Kind)}
		for k, v := range ge.Params {
			params[k] = v
		}
		return &Error{Code: CodeFileRefused, Params: params, Msg: fmt.Sprintf("Playkeeper couldn't %s. %s", what, ge.Msg), Hint: ge.Hint, Err: ge}
	}
	return &Error{
		Code:   CodeFileFailed,
		Params: map[string]any{"detail": err.Error()},
		Msg:    fmt.Sprintf("Playkeeper couldn't %s: %v.", what, err),
		Hint:   "Check that the server's disk isn't full, then try again.",
		Err:    err,
	}
}

// shortQuote quotes a name for a message, eliding the middle of a long one.
func shortQuote(s string) string {
	return strconv.Quote(elide(s, 80))
}

// shortName is a name as an error parameter: valid UTF-8, with the middle
// of a long one elided.
func shortName(s string) string {
	return elide(strings.ToValidUTF8(s, "\uFFFD"), 120)
}

func elide(s string, n int) string {
	if r := []rune(s); len(r) > n {
		s = string(r[:n/2]) + "…" + string(r[len(r)-n/2+10:])
	}
	return s
}

// thousands writes n with commas between groups of three digits.
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// kindName is the kind as a noun for messages.
func kindName(k Kind) string {
	switch k {
	case Data:
		return "a data pack"
	case Resource:
		return "a resource pack"
	case Both:
		return "a data pack and resource pack in one"
	}
	return "a pack"
}

// formatSize writes a byte count the way file managers do, in units of
// 1,024.
func formatSize(n int64) string {
	units := []string{"bytes", "KB", "MB", "GB", "TB"}
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " bytes"
	}
	f, i := float64(n), 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if f < 10 {
		return strconv.FormatFloat(f, 'f', 1, 64) + " " + units[i]
	}
	return strconv.FormatFloat(f, 'f', 0, 64) + " " + units[i]
}

// sentence ends s with a period unless it ends with punctuation already.
func sentence(s string) string {
	if strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?") {
		return s
	}
	return s + "."
}
