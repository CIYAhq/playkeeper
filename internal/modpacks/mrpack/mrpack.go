// Package mrpack reads Modrinth's modpack format, the .mrpack file
// (https://support.modrinth.com/en/articles/8802351-modrinth-modpack-format-mrpack):
// the modrinth.index.json index with its files, hashes, download addresses
// and environments, the loader a pack depends on, and the rules for paths
// inside a pack. Opening the zip and placing files is up to the caller.
package mrpack

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// IndexName is the index at the root of the zip.
	IndexName = "modrinth.index.json"
	// FormatVersion is the only format version there is.
	FormatVersion = 1
	// Overrides are copied into the instance folder; ServerOverrides are
	// layered on top of them on a server, ClientOverrides only on a client.
	Overrides       = "overrides"
	ServerOverrides = "server-overrides"
	ClientOverrides = "client-overrides"
)

// Dependency ids the format defines. It warns that more may be added.
const (
	Minecraft    = "minecraft"
	Forge        = "forge"
	NeoForge     = "neoforge"
	FabricLoader = "fabric-loader"
	QuiltLoader  = "quilt-loader"
)

// Hosts are the download hosts Modrinth accepts in the packs it publishes.
func Hosts() []string {
	return []string{"cdn.modrinth.com", "github.com", "raw.githubusercontent.com", "gitlab.com"}
}

// Index is modrinth.index.json.
type Index struct {
	FormatVersion int               `json:"formatVersion"`
	Game          string            `json:"game"`
	VersionID     string            `json:"versionId"`
	Name          string            `json:"name"`
	Summary       string            `json:"summary,omitempty"`
	Files         []File            `json:"files"`
	Dependencies  map[string]string `json:"dependencies"`
}

// File is a file to download into the instance folder.
type File struct {
	Path      string   `json:"path"`
	Hashes    Hashes   `json:"hashes"`
	Env       *Env     `json:"env,omitempty"`
	Downloads []string `json:"downloads"`
	FileSize  int64    `json:"fileSize"`
}

// Hashes are hex digests; the format requires both.
type Hashes struct {
	SHA1   string `json:"sha1"`
	SHA512 string `json:"sha512"`
}

// Env says where a file belongs.
type Env struct {
	Client Support `json:"client"`
	Server Support `json:"server"`
}

// Support is required, optional or unsupported.
type Support string

const (
	Required    Support = "required"
	Optional    Support = "optional"
	Unsupported Support = "unsupported"
	// Unknown is not in the format, but published packs use it for files
	// whose authors never said.
	Unknown Support = "unknown"
)

// Valid reports whether s is one of the three values the format allows.
func (s Support) Valid() bool { return s == Required || s == Optional || s == Unsupported }

// Server is how the file relates to a dedicated server. A file without env
// is required everywhere. Values outside the format ("unknown" occurs in
// published packs) are returned as they are.
func (f *File) Server() Support {
	if f.Env == nil {
		return Required
	}
	return f.Env.Server
}

// Client is how the file relates to the game client, read like Server.
func (f *File) Client() Support {
	if f.Env == nil {
		return Required
	}
	return f.Env.Client
}

// FormatError is an index that breaks the format.
type FormatError struct {
	Path   string // the file entry at fault, if any
	Reason string
}

func (e *FormatError) Error() string {
	if e.Path == "" {
		return "the pack's index " + e.Reason
	}
	return fmt.Sprintf("the pack's entry for %q %s", e.Path, e.Reason)
}

// PathError is a path that could land outside the server's folder or is
// otherwise unsafe to create.
type PathError struct {
	Path   string
	Reason string
}

func (e *PathError) Error() string { return fmt.Sprintf("the path %q %s", e.Path, e.Reason) }

// Parse decodes an index and checks it against the format: version,
// game, required fields, safe paths, both hashes, HTTPS download addresses
// and dependency versions. Download hosts are the caller's to check.
func Parse(b []byte) (*Index, error) {
	if !utf8.Valid(b) {
		return nil, &FormatError{Reason: "is not UTF-8 text"}
	}
	var ix Index
	if err := json.Unmarshal(b, &ix); err != nil {
		return nil, &FormatError{Reason: "is not valid JSON (" + err.Error() + ")"}
	}
	switch {
	case ix.FormatVersion != FormatVersion:
		return nil, &FormatError{Reason: fmt.Sprintf("uses format version %d; Playkeeper reads version %d", ix.FormatVersion, FormatVersion)}
	case ix.Game != "minecraft":
		return nil, &FormatError{Reason: fmt.Sprintf("is for the game %q, not Minecraft", ix.Game)}
	case strings.TrimSpace(ix.VersionID) == "":
		return nil, &FormatError{Reason: "has no versionId"}
	case strings.TrimSpace(ix.Name) == "":
		return nil, &FormatError{Reason: "has no name"}
	}
	if err := checkDependencies(ix.Dependencies); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(ix.Files))
	for i := range ix.Files {
		f := &ix.Files[i]
		if err := checkFile(f); err != nil {
			return nil, err
		}
		if seen[f.Path] {
			return nil, &FormatError{Path: f.Path, Reason: "appears more than once"}
		}
		seen[f.Path] = true
	}
	return &ix, nil
}

func checkFile(f *File) error {
	if err := CheckPath(f.Path); err != nil {
		return err
	}
	var err error
	if f.Hashes.SHA1, err = hexHash(f.Hashes.SHA1, 20); err != nil {
		return &FormatError{Path: f.Path, Reason: "has no valid sha1 hash"}
	}
	if f.Hashes.SHA512, err = hexHash(f.Hashes.SHA512, 64); err != nil {
		return &FormatError{Path: f.Path, Reason: "has no valid sha512 hash"}
	}
	if f.FileSize < 0 {
		return &FormatError{Path: f.Path, Reason: "has a negative fileSize"}
	}
	if len(f.Downloads) == 0 {
		return &FormatError{Path: f.Path, Reason: "has no download address"}
	}
	for _, raw := range f.Downloads {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || strings.ContainsAny(raw, " \t\r\n") {
			return &FormatError{Path: f.Path, Reason: "has a download address that is not a valid HTTPS URL"}
		}
	}
	return nil
}

func hexHash(s string, size int) (string, error) {
	s = strings.ToLower(s)
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != size {
		return "", fmt.Errorf("not a %d-byte hex hash", size)
	}
	return s, nil
}

func checkDependencies(deps map[string]string) error {
	if deps[Minecraft] == "" {
		return &FormatError{Reason: "does not say which Minecraft version it is for"}
	}
	for id, v := range deps {
		if !ValidToken(id) || !ValidToken(v) {
			return &FormatError{Reason: fmt.Sprintf("has a malformed dependency %q: %q", printable(id), printable(v))}
		}
	}
	return nil
}

// ValidToken accepts loader ids and version strings such as "fabric-loader",
// "0.20.0-beta.4" or "1.20.1-47.1.99". They end up in the server's settings,
// so nothing else is let through.
func ValidToken(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' || r == '_' || r == '+') {
			return false
		}
	}
	return true
}

// Loader returns the loader the pack depends on besides Minecraft and its
// version, or "" for a vanilla pack. An id the format doesn't define yet is
// returned too, for the caller to refuse by name.
func (ix *Index) Loader() (id, version string, err error) {
	var ids []string
	for k := range ix.Dependencies {
		if k != Minecraft {
			ids = append(ids, k)
		}
	}
	slices.Sort(ids)
	switch len(ids) {
	case 0:
		return "", "", nil
	case 1:
		return ids[0], ix.Dependencies[ids[0]], nil
	}
	return "", "", &FormatError{Reason: "depends on more than one mod loader (" + strings.Join(ids, ", ") + ")"}
}

// CheckPath checks a path from a pack, either a file entry's path or a file
// inside an overrides folder: relative, forward slashes only, no empty, "."
// or ".." parts, no drive letters or colons, no control, line-break or
// invisible formatting characters, and bounded in length.
func CheckPath(p string) error {
	fail := func(reason string) error { return &PathError{Path: printable(p), Reason: reason} }
	switch {
	case p == "":
		return fail("is empty")
	case len(p) > 1024:
		return fail("is longer than 1024 bytes")
	case !utf8.ValidString(p):
		return fail("is not valid text")
	case strings.HasPrefix(p, "/"):
		return fail("is absolute")
	case strings.ContainsRune(p, '\\'):
		return fail("uses backslashes")
	case strings.ContainsRune(p, ':'):
		return fail("contains a colon or drive letter")
	}
	for _, r := range p {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) || r == utf8.RuneError {
			return fail("contains control or invisible characters")
		}
	}
	for _, seg := range strings.Split(p, "/") {
		switch {
		case seg == "":
			return fail("has an empty part")
		case seg == "." || seg == "..":
			return fail("contains \"" + seg + "\"")
		case len(seg) > 255:
			return fail("has a part longer than 255 bytes")
		}
	}
	if path.Clean(p) != p {
		return fail("is not in its simplest form")
	}
	return nil
}

// printable makes a string from a pack safe to show in a message.
func printable(s string) string {
	s = strings.ToValidUTF8(s, "?")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return '?'
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120]) + "…"
	}
	return s
}
