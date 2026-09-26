// Package modpacks installs whole modpacks onto a server: Modrinth packs
// (.mrpack) and, when Playkeeper has a CurseForge API key, CurseForge packs.
// It finds packs Playkeeper can run, works out from a pack's dependencies
// which server type and Minecraft version it needs, and plans every file it
// would add, replace or remove before anything is written.
//
// Files are downloaded only from the hosts each format allows and checked
// against the pack's hashes before they reach the server's folder. Paths
// that are absolute or climb out of the folder refuse the whole pack; files
// for the game client, the world, the server's own settings and its start
// files are left out. Every file the pack puts on the server is recorded, so
// an update or removal touches only those files and never the world or the
// user's own files.
//
// The package keeps no state. Callers store the Record an install returns
// (one pack per server) and pass it back to update or remove the pack.
package modpacks

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
	"github.com/CIYAhq/playkeeper/internal/modpacks/curseforge"
	"github.com/CIYAhq/playkeeper/internal/modpacks/mrpack"
	"github.com/CIYAhq/playkeeper/internal/version"
)

// CurseForge is the source of CurseForge packs; Modrinth packs use
// addons.Modrinth.
const CurseForge addons.Source = "curseforge"

func sourceName(s addons.Source) string {
	if s == CurseForge {
		return "CurseForge"
	}
	return s.Name()
}

// Kinds this package adds to the add-on library's.
const (
	KindUnknownLoader   addons.Kind = "unknown_loader"
	KindTypeUnavailable addons.Kind = "server_type_unavailable"
	KindMinecraft       addons.Kind = "minecraft_unsupported"
	KindDowngrade       addons.Kind = "minecraft_downgrade"
	KindTypeChange      addons.Kind = "server_type_change"
	KindBadPack         addons.Kind = "bad_pack"
	KindUnsafePath      addons.Kind = "unsafe_path"
	KindTooManyFiles    addons.Kind = "too_many_files"
	KindNotModpack      addons.Kind = "not_a_modpack"
	KindNoCurseForge    addons.Kind = "curseforge_unavailable"
	KindKeyRefused      addons.Kind = "curseforge_key_refused"
	KindPackInstalled   addons.Kind = "pack_installed"
	KindClientContent   addons.Kind = "client_content"
	KindProtected       addons.Kind = "protected_path"
	KindWorld           addons.Kind = "world_files"
	KindProperties      addons.Kind = "server_properties"
	KindUnverifiedEnv   addons.Kind = "environment_unknown"
	KindExtraMods       addons.Kind = "extra_mods"
	KindInTheWay        addons.Kind = "in_the_way"
	KindUnavailable     addons.Kind = "file_unavailable"
	KindMalware         addons.Kind = "malware"
	KindOtherContent    addons.Kind = "other_content"
	KindOptionalOff     addons.Kind = "optional_off"
	KindUserRemoved     addons.Kind = "user_removed"
	KindModdedWorld     addons.Kind = "modded_world"
)

// Server is the server a pack goes on.
type Server struct {
	// Dir must exist. Type and MinecraftVersion may be empty for a server
	// that is being created from the pack.
	addons.Server
	// World is the world folder's name (level-name); "world" when empty.
	World string
}

func (s Server) world() string {
	if s.World == "" {
		return "world"
	}
	return s.World
}

// Requirements are what a pack needs from the server. The caller sets the
// server's type and versions to these before starting it.
type Requirements struct {
	Type             string `json:"type"` // fabric, quilt, neoforge or vanilla
	MinecraftVersion string `json:"minecraftVersion"`
	// LoaderVersion is the version of Fabric Loader, Quilt Loader or
	// NeoForge; empty for vanilla.
	LoaderVersion string `json:"loaderVersion,omitempty"`
}

// Ref names a pack and, optionally, one of its versions.
type Ref struct {
	Source addons.Source `json:"source"` // modrinth or curseforge
	// Project is a Modrinth project id or slug, or a CurseForge project id.
	Project string `json:"project"`
	// Version is a Modrinth version id or a CurseForge file id; empty means
	// the newest release Playkeeper can run.
	Version string `json:"version,omitempty"`
}

// Record is what Playkeeper stores for the pack on a server, and all an
// update or removal goes by.
type Record struct {
	// Pack is the installed version. Its file and hash are the pack's own
	// archive; the files it put on the server are in Files.
	Pack         addons.Installed `json:"pack"`
	Requirements Requirements     `json:"requirements"`
	Files        []File           `json:"files"`
	// Excluded are pack files left off the server: optional files the user
	// turned off and pack files the user deleted. Updates leave them off.
	Excluded []Excluded `json:"excluded,omitempty"`
	// Client lists the mods, resource packs and shader packs the pack gives
	// players, as its index or manifest describes them, for sharing the
	// pack with friends. Nothing in it is read from the server's folder.
	Client []ClientFile `json:"client,omitempty"`
}

// ClientFile is a mod, resource pack or shader pack for players' games
// (see PlayerContent). For a Modrinth pack these are the files whose env
// does not rule out the client; for a CurseForge pack, every one it lists,
// since only CurseForge's tags say which side a file is for.
type ClientFile struct {
	// Path is relative to the game's folder, with forward slashes.
	Path string `json:"path"`
	// SHA1 and SHA512 are lower-case hex; either may be empty (CurseForge
	// lists only SHA-1, and archive files too large to read have neither).
	SHA1   string `json:"sha1,omitempty"`
	SHA512 string `json:"sha512,omitempty"`
	Size   int64  `json:"size"`
	Origin Origin `json:"origin"`
	// Env is what a Modrinth pack's index says about the file, with values
	// outside the format as mrpack.Unknown; nil means both sides need it.
	Env *mrpack.Env `json:"env,omitempty"`
	// Downloads are the index's addresses on the hosts packs may use.
	Downloads []string `json:"downloads,omitempty"`
	// Project is the file's project on the pack's source, when known.
	Project string `json:"project,omitempty"`

	// The rest describes files of CurseForge packs: the file's id, the
	// project's name, the file's display name, the page to download it by
	// hand, whether the pack requires it, and the sides CurseForge tags it
	// for ("client", "server").
	FileID   string   `json:"fileId,omitempty"`
	Name     string   `json:"name,omitempty"`
	Version  string   `json:"version,omitempty"`
	Page     string   `json:"page,omitempty"`
	Required bool     `json:"required,omitempty"`
	Sides    []string `json:"sides,omitempty"`
}

// PlayerContent reports whether a path is a mod, resource pack or shader
// pack in a game's folder: a .jar directly in mods, or a .zip directly in
// resourcepacks or shaderpacks. Only these are shared with friends.
func PlayerContent(p string) bool {
	dir, name, ok := strings.Cut(p, "/")
	if !ok || name == "" || strings.Contains(name, "/") || name[0] == '.' || mrpack.CheckPath(p) != nil {
		return false
	}
	switch dir {
	case "mods":
		return strings.HasSuffix(name, ".jar") && len(name) > len(".jar")
	case "resourcepacks", "shaderpacks":
		return strings.HasSuffix(name, ".zip") && len(name) > len(".zip")
	}
	return false
}

// File is one file the pack put on the server.
type File struct {
	// Path is relative to the server's folder, with forward slashes.
	Path     string `json:"path"`
	HashAlgo string `json:"hashAlgo"` // sha512, or sha1 for CurseForge downloads
	Hash     string `json:"hash"`
	Size     int64  `json:"size"`
	Origin   Origin `json:"origin"`
	// Project is the file's project on the pack's source, when known.
	Project  string `json:"project,omitempty"`
	Optional bool   `json:"optional,omitempty"`
	// Preexisting is set when the file was on the server before the pack
	// (the same file, or the user's own version kept at their request).
	// Updates and removal never delete it.
	Preexisting bool `json:"preexisting,omitempty"`
}

// Origin is where a pack file comes from.
type Origin string

const (
	// Download files are fetched from the pack's file hosts.
	Download Origin = "download"
	// Override files are copied out of the pack's archive.
	Override Origin = "override"
)

// Excluded is a pack file the user does not want.
type Excluded struct {
	Path    string `json:"path"`
	Project string `json:"project,omitempty"`
}

// Owns reports whether path is one of the pack's files on the server, for
// labelling files in the add-on folder and the file manager.
func (r *Record) Owns(path string) bool {
	return slices.ContainsFunc(r.Files, func(f File) bool { return f.Path == path })
}

// Limits bound what a pack may make Playkeeper download and write.
type Limits struct {
	Pack      int64 // the pack's archive
	Index     int64 // modrinth.index.json or manifest.json
	Files     int   // files the pack puts on the server
	File      int64 // each of them
	Downloads int64 // the downloads together
	Unpacked  int64 // the files copied out of the archive together
	Entries   int   // entries in the archive
}

// DefaultLimits returns the limits used for every limit left at zero.
func DefaultLimits() Limits {
	return Limits{
		Pack: 1 << 30, Index: 16 << 20, Files: 5000, File: 256 << 20,
		Downloads: 4 << 30, Unpacked: 2 << 30, Entries: 20000,
	}
}

// DefaultMinMinecraft is the oldest Minecraft version packs are offered for.
const DefaultMinMinecraft = "1.21"

// Library runs modpack operations. One Library serves every server on a
// machine and holds no per-server state.
type Library struct {
	Modrinth *modrinth.Client
	// CurseForge is nil when Playkeeper has no CurseForge API key, and then
	// CurseForge is not offered.
	CurseForge *curseforge.Client
	// HTTP downloads packs and their files. Redirects are checked against
	// the host lists below regardless of its own policy.
	HTTP      *http.Client
	UserAgent string
	Now       func() time.Time
	// TempDir holds downloads until they are verified. It must not be
	// writable by the game; empty means the system temporary directory.
	TempDir string
	// PackHosts are the hosts a Modrinth pack's files may be listed on, and
	// PackRedirects the further hosts those may redirect to. They default
	// to the hosts Modrinth accepts in packs and GitHub's download hosts.
	PackHosts, PackRedirects fetch.Hosts
	// ModrinthFiles are the hosts Modrinth serves pack archives from and
	// CurseForgeFiles the hosts of CurseForge's files.
	ModrinthFiles, CurseForgeFiles fetch.Hosts
	// Types are the server types Playkeeper can run packs on: fabric,
	// quilt, neoforge, forge and vanilla when empty.
	Types []string
	// MinMinecraft is the oldest Minecraft version packs may need;
	// DefaultMinMinecraft when empty.
	MinMinecraft string
	Limits       Limits
}

// New returns a Library that reaches Modrinth, and CurseForge when key is
// usable, through hc.
func New(hc *http.Client, key curseforge.Key) *Library {
	o := fetch.Options{HTTP: hc, MaxWait: 10 * time.Second}
	l := &Library{Modrinth: modrinth.New(o), HTTP: hc}
	if key.Usable() {
		o.UserAgent = modrinth.UserAgent(version.Version)
		l.CurseForge = curseforge.New(key, o)
	}
	return l
}

// CheckKey asks CurseForge for one modpack with key, which it answers only
// for a key it accepts. A refused key is an error of kind KindKeyRefused;
// other failures are Upstream errors.
func CheckKey(ctx context.Context, hc *http.Client, key curseforge.Key) error {
	o := fetch.Options{HTTP: hc, MaxWait: 10 * time.Second, UserAgent: modrinth.UserAgent(version.Version)}
	_, err := curseforge.New(key, o).Search(ctx, curseforge.SearchQuery{ClassID: curseforge.ClassModpacks, PageSize: 1})
	return Upstream(CurseForge, err)
}

// Sources lists where packs can come from on this Playkeeper.
func (l *Library) Sources() []addons.Source {
	if l.CurseForge != nil {
		return []addons.Source{addons.Modrinth, CurseForge}
	}
	return []addons.Source{addons.Modrinth}
}

func (l *Library) now() time.Time {
	if l.Now != nil {
		return l.Now().UTC()
	}
	return time.Now().UTC()
}

func (l *Library) userAgent() string {
	if l.UserAgent != "" {
		return l.UserAgent
	}
	return modrinth.UserAgent(version.Version)
}

func (l *Library) packHosts() fetch.Hosts {
	if l.PackHosts != nil {
		return l.PackHosts
	}
	return mrpack.Hosts()
}

func (l *Library) packRedirects() fetch.Hosts {
	if l.PackRedirects != nil {
		return l.PackRedirects
	}
	return fetch.Hosts{"objects.githubusercontent.com", "release-assets.githubusercontent.com"}
}

func (l *Library) modrinthFiles() fetch.Hosts {
	if l.ModrinthFiles != nil {
		return l.ModrinthFiles
	}
	return fetch.Hosts{modrinth.CDNHost}
}

func (l *Library) curseForgeFiles() fetch.Hosts {
	if l.CurseForgeFiles != nil {
		return l.CurseForgeFiles
	}
	return fetch.Hosts{"edge.forgecdn.net", "mediafilez.forgecdn.net"}
}

func (l *Library) types() []string {
	if len(l.Types) > 0 {
		return l.Types
	}
	return []string{"fabric", "quilt", "neoforge", "forge", "vanilla"}
}

func (l *Library) minMinecraft() string {
	if l.MinMinecraft != "" {
		return l.MinMinecraft
	}
	return DefaultMinMinecraft
}

func (l *Library) limits() Limits {
	lim, d := l.Limits, DefaultLimits()
	pick := func(v, def int64) int64 {
		if v > 0 {
			return v
		}
		return def
	}
	lim.Pack, lim.Index, lim.File = pick(lim.Pack, d.Pack), pick(lim.Index, d.Index), pick(lim.File, d.File)
	lim.Downloads, lim.Unpacked = pick(lim.Downloads, d.Downloads), pick(lim.Unpacked, d.Unpacked)
	if lim.Files <= 0 {
		lim.Files = d.Files
	}
	if lim.Entries <= 0 {
		lim.Entries = d.Entries
	}
	return lim
}

func notice(k addons.Kind, params map[string]string, msg, hint string) addons.Notice {
	return addons.Notice{Kind: k, Params: params, Msg: msg, Hint: hint}
}

func fail(k addons.Kind, params map[string]string, msg, hint string) *addons.Error {
	return &addons.Error{Notice: notice(k, params, msg, hint)}
}

func kv(pairs ...string) map[string]string {
	m := make(map[string]string, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return m
}

// printable makes text from a pack safe to show: no control or format
// characters, no line breaks, at most 80 characters.
func printable(s string) string {
	s = strings.ToValidUTF8(s, "?")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return '?'
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80]) + "…"
	}
	return s
}

// validID accepts a project, version or file id or slug from a request.
func validID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return s != "." && s != ".."
}
