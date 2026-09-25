// Package addons is Playkeeper's plugin and mod library. For one server (its
// type and Minecraft version) it searches Modrinth and Hangar for add-ons
// that fit, plans an install with the dependencies it needs, downloads files
// only from the sources' own hosts, verifies them against the published
// hashes before they reach the server's folder, and installs, updates, scans
// and removes them in plugins/ or mods/.
//
// The package keeps no state. Callers pass the server and the add-ons
// already installed on it, and store the Installed records they get back:
// the agent's addons table, keyed by (server_id, source, project).
package addons

import (
	"net/http"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/addons/hangar"
	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
	"github.com/CIYAhq/playkeeper/internal/version"
)

// Source is where an add-on comes from.
type Source string

const (
	Modrinth Source = "modrinth"
	Hangar   Source = "hangar"
)

// Name is the source's name for messages.
func (s Source) Name() string {
	switch s {
	case Modrinth:
		return "Modrinth"
	case Hangar:
		return "Hangar"
	}
	return string(s)
}

// Server is the one server an operation acts on.
type Server struct {
	// Dir is the server's data directory; add-ons go into its plugins/ or
	// mods/ folder. The game may write here, so every file operation stays
	// inside it (os.Root) and never follows links out.
	Dir              string
	Type             string // paper, purpur, fabric, quilt, neoforge or vanilla
	MinecraftVersion string
	// Owner, when set, owns the files and folders the library creates (the
	// agent runs as root, the game as an unprivileged user).
	Owner *Owner
}

type Owner struct{ UID, GID int }

// Key identifies an add-on on a server: the addons table's key without the
// server id.
type Key struct {
	Source    Source `json:"source"`
	ProjectID string `json:"projectId"`
}

// Installed is an add-on Playkeeper put into a server's folder: one row of
// the addons table. ProjectID and VersionID are the source's ids (Hangar's
// numeric ids as text).
type Installed struct {
	Source        Source    `json:"source"`
	ProjectID     string    `json:"projectId"`
	Slug          string    `json:"slug"`
	Name          string    `json:"name"`
	IconURL       string    `json:"iconUrl,omitempty"`
	VersionID     string    `json:"versionId"`
	VersionNumber string    `json:"versionNumber"`
	Channel       string    `json:"channel"` // release, beta or alpha
	Published     time.Time `json:"published"`
	FileName      string    `json:"fileName"` // inside the target folder
	HashAlgo      string    `json:"hashAlgo"` // sha512 for Modrinth, sha256 for Hangar
	Hash          string    `json:"hash"`
	Size          int64     `json:"size"`
	// DependencyOf is the project id (same source) of the add-on this one
	// was installed for; empty when the user chose it.
	DependencyOf string `json:"dependencyOf,omitempty"`
	// Requires lists the project ids (same source) this version needs, so
	// removing one of them can be refused without asking the source.
	Requires    []string  `json:"requires,omitempty"`
	InstalledAt time.Time `json:"installedAt"`
}

func (i Installed) Key() Key { return Key{i.Source, i.ProjectID} }

const (
	DefaultMaxFileSize = 256 << 20
	DefaultMaxIconSize = 1 << 20
)

// Library runs add-on operations. One Library serves every server on a
// machine and holds no per-server state.
type Library struct {
	Modrinth *modrinth.Client
	Hangar   *hangar.Client
	// HTTP downloads files and icons. Redirects are checked against the
	// allowlists below regardless of its own policy.
	HTTP      *http.Client
	UserAgent string
	Now       func() time.Time
	// TempDir holds downloads until they are verified. It must not be
	// writable by the game; empty means the system temporary directory.
	TempDir string
	// ModrinthFiles and HangarFiles are the hosts files may come from, and
	// IconHosts those icons may; each defaults to the sources' own CDN.
	ModrinthFiles, HangarFiles, IconHosts fetch.Hosts
	// MaxFileSize bounds each add-on file and MaxIconSize each icon.
	MaxFileSize, MaxIconSize int64
}

// New returns a Library that reaches Modrinth and Hangar through hc and
// waits up to 10 seconds when either asks it to slow down.
func New(hc *http.Client) *Library {
	o := fetch.Options{HTTP: hc, MaxWait: 10 * time.Second}
	return &Library{Modrinth: modrinth.New(o), Hangar: hangar.New(o), HTTP: hc}
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

func (l *Library) fileHosts(s Source) fetch.Hosts {
	switch {
	case s == Modrinth && l.ModrinthFiles != nil:
		return l.ModrinthFiles
	case s == Modrinth:
		return fetch.Hosts{modrinth.CDNHost}
	case s == Hangar && l.HangarFiles != nil:
		return l.HangarFiles
	case s == Hangar:
		return fetch.Hosts{hangar.CDNHost}
	}
	return nil
}

func (l *Library) iconHosts() fetch.Hosts {
	if l.IconHosts != nil {
		return l.IconHosts
	}
	return fetch.Hosts{modrinth.CDNHost, hangar.CDNHost}
}

func (l *Library) maxFileSize() int64 {
	if l.MaxFileSize > 0 {
		return l.MaxFileSize
	}
	return DefaultMaxFileSize
}

func (l *Library) maxIconSize() int64 {
	if l.MaxIconSize > 0 {
		return l.MaxIconSize
	}
	return DefaultMaxIconSize
}
