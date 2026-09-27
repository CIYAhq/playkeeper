// Package curseforge is a client for the CurseForge API
// (https://docs.curseforge.com/rest-api/) as far as modpacks need it:
// searching projects, reading projects and files with their hashes and
// download addresses, and the manifest.json inside a modpack's zip. It also
// decides which API key to use.
//
// A file whose author does not allow downloads outside CurseForge's own app
// has no download address. This package never tries to get one another way;
// callers list such files for the user to fetch by hand.
package curseforge

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
)

const (
	DefaultBaseURL = "https://api.curseforge.com"
	// GameMinecraft is Minecraft's game id.
	GameMinecraft = 432
	// MaxPageSize is the largest page the search allows; index + page size
	// may not pass MaxResults.
	MaxPageSize = 50
	MaxResults  = 10000
)

// Class ids of Minecraft's sections.
const (
	ClassBukkitPlugins = 5
	ClassMods          = 6
	ClassResourcePacks = 12
	ClassWorlds        = 17
	ClassModpacks      = 4471
	ClassCustomization = 4546
	ClassShaders       = 6552
	ClassDataPacks     = 6945
)

// ModLoaderType values.
const (
	LoaderAny        = 0
	LoaderForge      = 1
	LoaderCauldron   = 2
	LoaderLiteLoader = 3
	LoaderFabric     = 4
	LoaderQuilt      = 5
	LoaderNeoForge   = 6
)

// HashAlgo values.
const (
	HashSHA1 = 1
	HashMD5  = 2
)

// FileReleaseType values.
const (
	Release = 1
	Beta    = 2
	Alpha   = 3
)

// FileStatus values a caller acts on.
const (
	StatusApproved        = 4
	StatusRejected        = 5
	StatusMalwareDetected = 6
	StatusDeleted         = 7
)

// Sort fields for search.
const (
	SortFeatured       = 1
	SortPopularity     = 2
	SortLastUpdated    = 3
	SortName           = 4
	SortTotalDownloads = 6
	SortReleasedDate   = 11
)

// Client talks to CurseForge with one API key.
type Client struct{ api *fetch.API }

// New returns a client that sends key with every request. BaseURL defaults
// to DefaultBaseURL.
func New(key Key, o fetch.Options) *Client {
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	o.Header = o.Header.Clone()
	if o.Header == nil {
		o.Header = make(map[string][]string)
	}
	o.Header.Set("x-api-key", key.value)
	return &Client{api: fetch.New("CurseForge", o)}
}

type Pagination struct {
	Index       int   `json:"index"`
	PageSize    int   `json:"pageSize"`
	ResultCount int   `json:"resultCount"`
	TotalCount  int64 `json:"totalCount"`
}

type Links struct {
	WebsiteURL string `json:"websiteUrl"`
	WikiURL    string `json:"wikiUrl"`
	IssuesURL  string `json:"issuesUrl"`
	SourceURL  string `json:"sourceUrl"`
}

type Author struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Asset struct {
	ID           int64  `json:"id"`
	ModID        int64  `json:"modId"`
	Title        string `json:"title"`
	ThumbnailURL string `json:"thumbnailUrl"`
	URL          string `json:"url"`
}

type Category struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	ClassID int    `json:"classId"`
}

// FileIndex is a short entry for a project's newest file per game version
// and loader. ModLoader is nil when CurseForge doesn't say.
type FileIndex struct {
	GameVersion string `json:"gameVersion"`
	FileID      int64  `json:"fileId"`
	Filename    string `json:"filename"`
	ReleaseType int    `json:"releaseType"`
	ModLoader   *int   `json:"modLoader"`
}

// Mod is a project: a mod, a modpack, a resource pack and so on.
type Mod struct {
	ID                   int64       `json:"id"`
	GameID               int         `json:"gameId"`
	Name                 string      `json:"name"`
	Slug                 string      `json:"slug"`
	Summary              string      `json:"summary"`
	Links                Links       `json:"links"`
	Status               int         `json:"status"`
	DownloadCount        int64       `json:"downloadCount"`
	ClassID              int         `json:"classId"`
	Categories           []Category  `json:"categories"`
	Authors              []Author    `json:"authors"`
	Logo                 *Asset      `json:"logo"`
	MainFileID           int64       `json:"mainFileId"`
	LatestFilesIndexes   []FileIndex `json:"latestFilesIndexes"`
	DateModified         time.Time   `json:"dateModified"`
	DateReleased         time.Time   `json:"dateReleased"`
	AllowModDistribution *bool       `json:"allowModDistribution"`
	IsAvailable          bool        `json:"isAvailable"`
}

type FileHash struct {
	Value string `json:"value"`
	Algo  int    `json:"algo"`
}

type FileDependency struct {
	ModID        int64 `json:"modId"`
	RelationType int   `json:"relationType"`
}

// File is one uploaded file of a project. DownloadURL is empty when the
// author does not allow downloads outside CurseForge's app.
type File struct {
	ID               int64            `json:"id"`
	GameID           int              `json:"gameId"`
	ModID            int64            `json:"modId"`
	IsAvailable      bool             `json:"isAvailable"`
	DisplayName      string           `json:"displayName"`
	FileName         string           `json:"fileName"`
	ReleaseType      int              `json:"releaseType"`
	FileStatus       int              `json:"fileStatus"`
	Hashes           []FileHash       `json:"hashes"`
	FileDate         time.Time        `json:"fileDate"`
	FileLength       int64            `json:"fileLength"`
	DownloadURL      string           `json:"downloadUrl"`
	GameVersions     []string         `json:"gameVersions"`
	Dependencies     []FileDependency `json:"dependencies"`
	IsServerPack     bool             `json:"isServerPack"`
	ServerPackFileID *int64           `json:"serverPackFileId"`
}

// SHA1 is the file's sha1 hash in lower case, or "".
func (f *File) SHA1() string {
	for _, h := range f.Hashes {
		if h.Algo == HashSHA1 && len(h.Value) == 40 {
			return strings.ToLower(h.Value)
		}
	}
	return ""
}

// ClientOnly reports whether CurseForge tags the file for the game client
// and not for servers.
func (f *File) ClientOnly() bool {
	return slices.Equal(f.Sides(), []string{"client"})
}

// Sides lists the sides CurseForge tags the file for, "client" then
// "server". Authors may leave both out.
func (f *File) Sides() []string {
	var out []string
	for _, side := range []string{"client", "server"} {
		if slices.ContainsFunc(f.GameVersions, func(v string) bool { return strings.EqualFold(v, side) }) {
			out = append(out, side)
		}
	}
	return out
}

// SearchQuery is a project search in one class.
type SearchQuery struct {
	ClassID     int
	Text        string
	GameVersion string
	// ModLoaderType narrows results to one loader. CurseForge only applies it
	// together with GameVersion.
	ModLoaderType int
	SortField     int
	Index         int
	PageSize      int
}

type SearchResult struct {
	Data       []Mod      `json:"data"`
	Pagination Pagination `json:"pagination"`
}

// Search finds Minecraft projects, sorted descending by q.SortField.
func (c *Client) Search(ctx context.Context, q SearchQuery) (*SearchResult, error) {
	v := url.Values{"gameId": {strconv.Itoa(GameMinecraft)}, "sortOrder": {"desc"}}
	if q.ClassID != 0 {
		v.Set("classId", strconv.Itoa(q.ClassID))
	}
	if q.Text != "" {
		v.Set("searchFilter", q.Text)
	}
	if q.GameVersion != "" {
		v.Set("gameVersion", q.GameVersion)
		if q.ModLoaderType != LoaderAny {
			v.Set("modLoaderType", strconv.Itoa(q.ModLoaderType))
		}
	}
	if q.SortField != 0 {
		v.Set("sortField", strconv.Itoa(q.SortField))
	}
	if q.Index > 0 {
		v.Set("index", strconv.Itoa(q.Index))
	}
	if q.PageSize > 0 {
		v.Set("pageSize", strconv.Itoa(min(q.PageSize, MaxPageSize)))
	}
	var r SearchResult
	if err := c.api.Get(ctx, "/v1/mods/search", v, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Mod returns one project. A missing one is an error matching
// fetch.ErrNotFound.
func (c *Client) Mod(ctx context.Context, id int64) (*Mod, error) {
	var r struct {
		Data Mod `json:"data"`
	}
	if err := c.api.Get(ctx, "/v1/mods/"+strconv.FormatInt(id, 10), nil, &r); err != nil {
		return nil, err
	}
	return &r.Data, nil
}

// Mods returns the projects with these ids; unknown ones are left out.
func (c *Client) Mods(ctx context.Context, ids []int64) ([]Mod, error) {
	var out []Mod
	for chunk := range slices.Chunk(ids, MaxPageSize) {
		var r struct {
			Data []Mod `json:"data"`
		}
		body := struct {
			ModIDs       []int64 `json:"modIds"`
			FilterPcOnly bool    `json:"filterPcOnly"`
		}{chunk, true}
		if err := c.api.Post(ctx, "/v1/mods", body, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Data...)
	}
	return out, nil
}

// FilesQuery narrows a project's files.
type FilesQuery struct {
	GameVersion   string
	ModLoaderType int
	Index         int
	PageSize      int
}

// ModFiles lists a project's files, newest first.
func (c *Client) ModFiles(ctx context.Context, id int64, q FilesQuery) ([]File, Pagination, error) {
	v := url.Values{}
	if q.GameVersion != "" {
		v.Set("gameVersion", q.GameVersion)
	}
	if q.ModLoaderType != LoaderAny {
		v.Set("modLoaderType", strconv.Itoa(q.ModLoaderType))
	}
	if q.Index > 0 {
		v.Set("index", strconv.Itoa(q.Index))
	}
	if q.PageSize > 0 {
		v.Set("pageSize", strconv.Itoa(min(q.PageSize, MaxPageSize)))
	}
	var r struct {
		Data       []File     `json:"data"`
		Pagination Pagination `json:"pagination"`
	}
	if err := c.api.Get(ctx, "/v1/mods/"+strconv.FormatInt(id, 10)+"/files", v, &r); err != nil {
		return nil, Pagination{}, err
	}
	return r.Data, r.Pagination, nil
}

// File returns one file of a project.
func (c *Client) File(ctx context.Context, modID, fileID int64) (*File, error) {
	var r struct {
		Data File `json:"data"`
	}
	if err := c.api.Get(ctx, "/v1/mods/"+strconv.FormatInt(modID, 10)+"/files/"+strconv.FormatInt(fileID, 10), nil, &r); err != nil {
		return nil, err
	}
	return &r.Data, nil
}

// Files returns the files with these ids; unknown ones are left out.
func (c *Client) Files(ctx context.Context, ids []int64) ([]File, error) {
	var out []File
	for chunk := range slices.Chunk(ids, MaxPageSize) {
		var r struct {
			Data []File `json:"data"`
		}
		body := struct {
			FileIDs []int64 `json:"fileIds"`
		}{chunk}
		if err := c.api.Post(ctx, "/v1/mods/files", body, &r); err != nil {
			return nil, err
		}
		out = append(out, r.Data...)
	}
	return out, nil
}

// ProjectPage is the page for a project, for links in messages.
func (m *Mod) ProjectPage() string {
	if u, err := url.Parse(m.Links.WebsiteURL); err == nil && u.Scheme == "https" && u.User == nil && u.Port() == "" &&
		(u.Hostname() == "curseforge.com" || strings.HasSuffix(u.Hostname(), ".curseforge.com")) {
		return m.Links.WebsiteURL
	}
	return "https://www.curseforge.com/projects/" + strconv.FormatInt(m.ID, 10)
}

// FilePage is the page where a person can download one file of the
// project by hand.
func (m *Mod) FilePage(fileID int64) string {
	p := m.ProjectPage()
	if strings.Contains(p, "/projects/") {
		return p
	}
	return strings.TrimRight(p, "/") + "/files/" + strconv.FormatInt(fileID, 10)
}
