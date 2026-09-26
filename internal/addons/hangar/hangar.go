// Package hangar is a client for PaperMC's plugin repository Hangar
// (https://hangar.papermc.io/api-docs): project search by platform and
// Minecraft version, projects, and versions with their per-platform
// downloads and plugin dependencies. It knows nothing about servers;
// internal/addons decides what fits a server and installs it.
package hangar

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/version"
)

const (
	DefaultBaseURL = "https://hangar.papermc.io/api/v1"
	// CDNHost serves version files and project avatars.
	CDNHost = "hangarcdn.papermc.io"
	// SiteURL is where project pages live, as SiteURL/<owner>/<slug>.
	SiteURL = "https://hangar.papermc.io"
	// Paper is the platform Paper and its forks (Purpur, Folia) load.
	Paper = "PAPER"
	// MaxLimit is the largest page Hangar serves.
	MaxLimit = 25
)

// Client talks to Hangar. Share one per machine: its rate limits count per
// IP address.
type Client struct{ api *fetch.API }

// New returns a client; BaseURL defaults to DefaultBaseURL and UserAgent to
// the one Playkeeper sends Modrinth.
func New(o fetch.Options) *Client {
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	if o.UserAgent == "" {
		o.UserAgent = "CIYAhq/playkeeper/" + version.Version + " (https://github.com/CIYAhq/playkeeper)"
	}
	return &Client{api: fetch.New("Hangar", o)}
}

// SearchQuery is a project search. Sort is one of views, downloads, newest,
// stars, updated, recent_downloads, recent_views or slug; a leading "-"
// sorts descending.
type SearchQuery struct {
	Query    string
	Platform string
	Version  string // a platform version, e.g. a Minecraft version for PAPER
	Category string // e.g. admin_tools or world_management
	Sort     string
	Offset   int
	Limit    int // 1 to MaxLimit
}

type Pagination struct {
	Count  int `json:"count"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

type SearchResult struct {
	Pagination Pagination `json:"pagination"`
	Result     []Project  `json:"result"`
}

type Project struct {
	ID                 int64               `json:"id"`
	Name               string              `json:"name"`
	Namespace          Namespace           `json:"namespace"`
	Stats              Stats               `json:"stats"`
	Category           string              `json:"category"`
	Description        string              `json:"description"`
	CreatedAt          time.Time           `json:"createdAt"`
	LastUpdated        time.Time           `json:"lastUpdated"`
	Visibility         string              `json:"visibility"`
	Settings           Settings            `json:"settings"`
	SupportedPlatforms map[string][]string `json:"supportedPlatforms"`
	AvatarURL          string              `json:"avatarUrl"`
}

// PageURL is the project's page on Hangar.
func (p *Project) PageURL() string {
	return SiteURL + "/" + url.PathEscape(p.Namespace.Owner) + "/" + url.PathEscape(p.Namespace.Slug)
}

type Namespace struct {
	Owner string `json:"owner"`
	Slug  string `json:"slug"`
}

type Stats struct {
	Views           int64 `json:"views"`
	Downloads       int64 `json:"downloads"`
	RecentViews     int64 `json:"recentViews"`
	RecentDownloads int64 `json:"recentDownloads"`
	Stars           int64 `json:"stars"`
	Watchers        int64 `json:"watchers"`
}

type Settings struct {
	Tags     []string `json:"tags"`
	License  License  `json:"license"`
	Keywords []string `json:"keywords"`
}

// License: Type is one of Hangar's choices (MIT, Apache 2.0, GPL, LGPL,
// AGPL, Unspecified) or Other, in which case Name holds the author's text.
type License struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Type string `json:"type"`
}

type Version struct {
	ID                   int64                         `json:"id"`
	ProjectID            int64                         `json:"projectId"`
	Name                 string                        `json:"name"`
	CreatedAt            time.Time                     `json:"createdAt"`
	Visibility           string                        `json:"visibility"`
	Author               string                        `json:"author"`
	ReviewState          string                        `json:"reviewState"`
	Channel              Channel                       `json:"channel"`
	Downloads            map[string]Download           `json:"downloads"`
	PluginDependencies   map[string][]PluginDependency `json:"pluginDependencies"`
	PlatformDependencies map[string][]string           `json:"platformDependencies"`
	// Description is the author's Markdown notes for the version.
	Description string `json:"description"`
}

// Channel groups versions; projects start with a "Release" channel and add
// others such as "Snapshot" or "Beta" for unstable builds.
type Channel struct {
	Name  string   `json:"name"`
	Flags []string `json:"flags"`
}

// Stable reports whether versions in the channel are releases.
func (c Channel) Stable() bool {
	switch strings.ToLower(c.Name) {
	case "release", "releases", "stable":
		return true
	}
	return false
}

// Download is either a file hosted on Hangar (FileInfo and DownloadURL) or
// only a link to another site (ExternalURL).
type Download struct {
	FileInfo    *FileInfo `json:"fileInfo"`
	ExternalURL string    `json:"externalUrl"`
	DownloadURL string    `json:"downloadUrl"`
}

type FileInfo struct {
	Name       string `json:"name"`
	SizeBytes  int64  `json:"sizeBytes"`
	SHA256Hash string `json:"sha256Hash"`
}

// PluginDependency names another Hangar project (ProjectID) or, for plugins
// not on Hangar, a page to get it from (ExternalURL).
type PluginDependency struct {
	Name        string `json:"name"`
	ProjectID   *int64 `json:"projectId"`
	Required    bool   `json:"required"`
	ExternalURL string `json:"externalUrl"`
	Platform    string `json:"platform"`
}

type VersionList struct {
	Pagination Pagination `json:"pagination"`
	Result     []Version  `json:"result"`
}

// VersionFilter narrows a project's versions.
type VersionFilter struct {
	Platform        string
	PlatformVersion string
	Channel         string
	Offset          int
	Limit           int // 1 to MaxLimit
}

// Search runs a project search.
func (c *Client) Search(ctx context.Context, q SearchQuery) (*SearchResult, error) {
	v := url.Values{}
	set(v, "query", q.Query)
	set(v, "platform", q.Platform)
	set(v, "version", q.Version)
	set(v, "category", q.Category)
	set(v, "sort", q.Sort)
	page(v, q.Offset, q.Limit)
	var r SearchResult
	if err := c.api.Get(ctx, "/projects", v, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Project returns a project by slug or numeric id. A missing project is an
// error matching fetch.ErrNotFound.
func (c *Client) Project(ctx context.Context, slugOrID string) (*Project, error) {
	var p Project
	if err := c.api.Get(ctx, "/projects/"+url.PathEscape(slugOrID), nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Versions lists a project's versions, newest first.
func (c *Client) Versions(ctx context.Context, slugOrID string, f VersionFilter) (*VersionList, error) {
	v := url.Values{}
	set(v, "platform", f.Platform)
	set(v, "platformVersion", f.PlatformVersion)
	set(v, "channel", f.Channel)
	page(v, f.Offset, f.Limit)
	var r VersionList
	if err := c.api.Get(ctx, "/projects/"+url.PathEscape(slugOrID)+"/versions", v, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Version returns one version by its numeric id.
func (c *Client) Version(ctx context.Context, id int64) (*Version, error) {
	var v Version
	if err := c.api.Get(ctx, "/versions/"+strconv.FormatInt(id, 10), nil, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func set(v url.Values, k, s string) {
	if s != "" {
		v.Set(k, s)
	}
}

func page(v url.Values, offset, limit int) {
	if offset > 0 {
		v.Set("offset", strconv.Itoa(offset))
	}
	if limit > 0 {
		v.Set("limit", strconv.Itoa(min(limit, MaxLimit)))
	}
}
