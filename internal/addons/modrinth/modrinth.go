// Package modrinth is a client for the Modrinth API v2
// (https://docs.modrinth.com/api/): project search with facets, projects,
// versions with their files and dependencies, and identifying files by hash.
// It knows nothing about servers; internal/addons decides what fits a server
// and installs it, and modpacks can use the same client.
package modrinth

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/version"
)

const (
	DefaultBaseURL = "https://api.modrinth.com/v2"
	// CDNHost serves version files and project icons.
	CDNHost = "cdn.modrinth.com"
)

// UserAgent is the User-Agent Modrinth asks API clients to send: project,
// version and a contact URL.
func UserAgent(v string) string {
	return "CIYAhq/playkeeper/" + v + " (https://github.com/CIYAhq/playkeeper)"
}

// Client talks to Modrinth. Share one per machine: the rate limit (300
// requests a minute) counts per IP address.
type Client struct{ api *fetch.API }

// New returns a client; BaseURL defaults to DefaultBaseURL and UserAgent to
// UserAgent(version.Version).
func New(o fetch.Options) *Client {
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	if o.UserAgent == "" {
		o.UserAgent = UserAgent(version.Version)
	}
	return &Client{api: fetch.New("Modrinth", o)}
}

// SearchQuery is a project search. Facets are ANDed lists of ORed
// "key:value" filters, e.g. [["categories:paper","categories:spigot"],["versions:26.2"]].
type SearchQuery struct {
	Query  string
	Facets [][]string
	Index  string // relevance (default), downloads, follows, newest or updated
	Offset int
	Limit  int // 1 to 100; Modrinth's default is 10
}

// Facet builds one ORed facet group.
func Facet(key string, values ...string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = key + ":" + v
	}
	return out
}

type SearchResult struct {
	Hits      []SearchHit `json:"hits"`
	Offset    int         `json:"offset"`
	Limit     int         `json:"limit"`
	TotalHits int         `json:"total_hits"`
}

type SearchHit struct {
	ProjectID         string    `json:"project_id"`
	ProjectType       string    `json:"project_type"`
	Slug              string    `json:"slug"`
	Author            string    `json:"author"`
	Title             string    `json:"title"`
	Description       string    `json:"description"`
	Categories        []string  `json:"categories"`
	DisplayCategories []string  `json:"display_categories"`
	Versions          []string  `json:"versions"`
	Downloads         int64     `json:"downloads"`
	Follows           int64     `json:"follows"`
	IconURL           string    `json:"icon_url"`
	DateCreated       time.Time `json:"date_created"`
	DateModified      time.Time `json:"date_modified"`
	LatestVersion     string    `json:"latest_version"`
	License           string    `json:"license"`
	ClientSide        string    `json:"client_side"`
	ServerSide        string    `json:"server_side"`
	Environment       []string  `json:"environment"`
}

// RunsOnServer reports whether the project can run on a dedicated server.
func (h *SearchHit) RunsOnServer() bool { return RunsOnServer(h.ServerSide, h.Environment...) }

type Project struct {
	ID                   string    `json:"id"`
	Slug                 string    `json:"slug"`
	ProjectType          string    `json:"project_type"`
	Title                string    `json:"title"`
	Description          string    `json:"description"`
	Categories           []string  `json:"categories"`
	AdditionalCategories []string  `json:"additional_categories"`
	Loaders              []string  `json:"loaders"`
	GameVersions         []string  `json:"game_versions"`
	ClientSide           string    `json:"client_side"`
	ServerSide           string    `json:"server_side"`
	Environment          []string  `json:"environment"`
	Status               string    `json:"status"`
	Downloads            int64     `json:"downloads"`
	Followers            int64     `json:"followers"`
	IconURL              string    `json:"icon_url"`
	License              License   `json:"license"`
	Published            time.Time `json:"published"`
	Updated              time.Time `json:"updated"`
	Versions             []string  `json:"versions"`
	SourceURL            string    `json:"source_url"`
	IssuesURL            string    `json:"issues_url"`
	WikiURL              string    `json:"wiki_url"`
}

// RunsOnServer reports whether the project can run on a dedicated server.
func (p *Project) RunsOnServer() bool { return RunsOnServer(p.ServerSide, p.Environment...) }

type License struct {
	ID   string `json:"id"` // SPDX identifier, or LicenseRef-… for custom licences
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Version struct {
	ID            string       `json:"id"`
	ProjectID     string       `json:"project_id"`
	Name          string       `json:"name"`
	VersionNumber string       `json:"version_number"`
	VersionType   string       `json:"version_type"` // release, beta or alpha
	Status        string       `json:"status"`
	Featured      bool         `json:"featured"`
	GameVersions  []string     `json:"game_versions"`
	Loaders       []string     `json:"loaders"`
	Environment   string       `json:"environment"`
	DatePublished time.Time    `json:"date_published"`
	Downloads     int64        `json:"downloads"`
	Files         []File       `json:"files"`
	Dependencies  []Dependency `json:"dependencies"`
	// Changelog is the author's Markdown notes for the version.
	Changelog string `json:"changelog"`
}

// RunsOnServer reports whether this version can run on a dedicated server.
func (v *Version) RunsOnServer() bool { return RunsOnServer("", v.Environment) }

// PrimaryFile is the file a launcher installs: the one marked primary, else
// the first plain file (not sources, javadoc, signatures or resource packs).
func (v *Version) PrimaryFile() (File, bool) {
	for _, f := range v.Files {
		if f.Primary && f.FileType == "" {
			return f, true
		}
	}
	for _, f := range v.Files {
		if f.FileType == "" {
			return f, true
		}
	}
	return File{}, false
}

type File struct {
	Hashes   Hashes `json:"hashes"`
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Primary  bool   `json:"primary"`
	Size     int64  `json:"size"`
	FileType string `json:"file_type"` // empty for the add-on itself; sources-jar, dev-jar, … otherwise
}

type Hashes struct {
	SHA512 string `json:"sha512"`
	SHA1   string `json:"sha1"`
}

// Dependency types.
const (
	Required     = "required"
	Optional     = "optional"
	Incompatible = "incompatible"
	Embedded     = "embedded"
)

// Dependency points at a project, a specific version, or (for files not on
// Modrinth) only a file name.
type Dependency struct {
	VersionID      string `json:"version_id"`
	ProjectID      string `json:"project_id"`
	FileName       string `json:"file_name"`
	DependencyType string `json:"dependency_type"`
}

// RunsOnServer interprets Modrinth's side information. server_side is the
// older field ("unsupported" means client only); environment replaced it in
// 2025 and is a list on projects (one entry per version) and a single value
// on versions. Unknown values count as able to run on a server.
func RunsOnServer(serverSide string, environments ...string) bool {
	if serverSide == "unsupported" {
		return false
	}
	known := false
	for _, e := range environments {
		switch e {
		case "":
		case "client_only", "singleplayer_only":
			known = true
		default:
			return true
		}
	}
	return !known
}

// IsLoaderTag reports whether a category tag names a loader or platform
// (Modrinth lists them among a project's categories).
func IsLoaderTag(tag string) bool {
	switch tag {
	case "babric", "bta-babric", "bukkit", "bungeecord", "canvas", "datapack", "fabric", "folia", "forge",
		"geyser", "iris", "java-agent", "legacy-fabric", "liteloader", "minecraft", "modloader", "neoforge",
		"nilloader", "optifine", "ornithe", "paper", "purpur", "quilt", "rift", "spigot", "sponge", "vanilla",
		"velocity", "waterfall":
		return true
	}
	return false
}

// Search runs a project search.
func (c *Client) Search(ctx context.Context, q SearchQuery) (*SearchResult, error) {
	v := url.Values{}
	if q.Query != "" {
		v.Set("query", q.Query)
	}
	if len(q.Facets) > 0 {
		v.Set("facets", jsonList(q.Facets))
	}
	if q.Index != "" {
		v.Set("index", q.Index)
	}
	if q.Offset > 0 {
		v.Set("offset", strconv.Itoa(q.Offset))
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	var r SearchResult
	if err := c.api.Get(ctx, "/search", v, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Project returns a project by id or slug. A missing project is an error
// matching fetch.ErrNotFound.
func (c *Client) Project(ctx context.Context, idOrSlug string) (*Project, error) {
	var p Project
	if err := c.api.Get(ctx, "/project/"+url.PathEscape(idOrSlug), nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Projects returns the projects with these ids or slugs; unknown ones are
// left out.
func (c *Client) Projects(ctx context.Context, ids []string) ([]Project, error) {
	var out []Project
	for chunk := range slices.Chunk(ids, 50) {
		var ps []Project
		if err := c.api.Get(ctx, "/projects", url.Values{"ids": {jsonList(chunk)}}, &ps); err != nil {
			return nil, err
		}
		out = append(out, ps...)
	}
	return out, nil
}

// VersionFilter narrows versions to loaders and game versions (either one
// matching is enough within each list).
type VersionFilter struct {
	Loaders      []string
	GameVersions []string
}

// ProjectVersions lists a project's versions, newest first, without
// changelogs.
func (c *Client) ProjectVersions(ctx context.Context, idOrSlug string, f VersionFilter) ([]Version, error) {
	v := url.Values{"include_changelog": {"false"}}
	if len(f.Loaders) > 0 {
		v.Set("loaders", jsonList(f.Loaders))
	}
	if len(f.GameVersions) > 0 {
		v.Set("game_versions", jsonList(f.GameVersions))
	}
	var vs []Version
	if err := c.api.Get(ctx, "/project/"+url.PathEscape(idOrSlug)+"/version", v, &vs); err != nil {
		return nil, err
	}
	return vs, nil
}

// Version returns one version by id.
func (c *Client) Version(ctx context.Context, id string) (*Version, error) {
	var v Version
	if err := c.api.Get(ctx, "/version/"+url.PathEscape(id), nil, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// Versions returns the versions with these ids; unknown ones are left out.
func (c *Client) Versions(ctx context.Context, ids []string) ([]Version, error) {
	var out []Version
	for chunk := range slices.Chunk(ids, 50) {
		var vs []Version
		if err := c.api.Get(ctx, "/versions", url.Values{"ids": {jsonList(chunk)}}, &vs); err != nil {
			return nil, err
		}
		out = append(out, vs...)
	}
	return out, nil
}

type hashList struct {
	Hashes       []string `json:"hashes"`
	Algorithm    string   `json:"algorithm"`
	Loaders      []string `json:"loaders,omitempty"`
	GameVersions []string `json:"game_versions,omitempty"`
	VersionTypes []string `json:"version_types,omitempty"`
}

// VersionsFromHashes identifies files by their sha1 or sha512 hash. The map
// holds only the hashes Modrinth knows.
func (c *Client) VersionsFromHashes(ctx context.Context, algo string, hashes []string) (map[string]Version, error) {
	out := map[string]Version{}
	if len(hashes) == 0 {
		return out, nil
	}
	err := c.api.Post(ctx, "/version_files", hashList{Hashes: hashes, Algorithm: algo}, &out)
	return out, err
}

// LatestVersionsFromHashes returns, for each known file hash, the newest
// version of the same project for the loaders and game versions in f,
// limited to versionTypes (release, beta, alpha) when given.
func (c *Client) LatestVersionsFromHashes(ctx context.Context, algo string, hashes []string, f VersionFilter, versionTypes []string) (map[string]Version, error) {
	out := map[string]Version{}
	if len(hashes) == 0 {
		return out, nil
	}
	body := hashList{Hashes: hashes, Algorithm: algo, Loaders: f.Loaders, GameVersions: f.GameVersions, VersionTypes: versionTypes}
	err := c.api.Post(ctx, "/version_files/update", body, &out)
	return out, err
}

func jsonList(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
