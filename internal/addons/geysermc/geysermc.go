// Package geysermc is a client for GeyserMC's download API
// (https://download.geysermc.org/docs): the newest build of one of its
// projects, with each platform's file name and SHA-256. Hangar lists Geyser
// and Floodgate with a link to that API instead of a file of their own;
// internal/addons follows the link for GeyserMC's projects and nothing else.
package geysermc

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/version"
)

const (
	DefaultBaseURL = "https://download.geysermc.org/v2"
	// Host serves the API and the files.
	Host = "download.geysermc.org"
)

// Projects are GeyserMC's projects whose Hangar links the library follows.
var Projects = []string{"geyser", "floodgate"}

// Client talks to GeyserMC's download API.
type Client struct {
	api       *fetch.API
	http      *http.Client
	base      string
	userAgent string
}

// New returns a client; BaseURL defaults to DefaultBaseURL and UserAgent to
// the one Playkeeper sends Modrinth and Hangar.
func New(o fetch.Options) *Client {
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	if o.UserAgent == "" {
		o.UserAgent = "CIYAhq/playkeeper/" + version.Version + " (https://github.com/CIYAhq/playkeeper)"
	}
	c := &Client{api: fetch.New("GeyserMC", o), base: strings.TrimRight(o.BaseURL, "/"), userAgent: o.UserAgent}
	var hosts fetch.Hosts
	if u, err := url.Parse(c.base); err == nil && u.Host != "" {
		hosts = fetch.Hosts{u.Host}
	}
	c.http = fetch.Guard(o.HTTP, hosts)
	return c
}

// Build is one build of a project. GeyserMC numbers builds across a
// project's versions and publishes every build on its "default" channel.
type Build struct {
	Project   string              `json:"project_id"`
	Version   string              `json:"version"`
	Build     int                 `json:"build"`
	Time      time.Time           `json:"time"`
	Channel   string              `json:"channel"`
	Changes   []Change            `json:"changes"`
	Downloads map[string]Download `json:"downloads"`
}

// Name is the build as GeyserMC names it in its plugins' own version
// strings, such as "2.2.5-b141".
func (b *Build) Name() string { return b.Version + "-b" + strconv.Itoa(b.Build) }

type Change struct {
	Summary string `json:"summary"`
}

// Download is one platform's file of a build.
type Download struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// Latest returns the newest build of project. The API answers with a
// redirect to the build, on its own host.
func (c *Client) Latest(ctx context.Context, project string) (*Build, error) {
	var b Build
	if err := c.api.Get(ctx, "/projects/"+url.PathEscape(project)+"/versions/latest/builds/latest", nil, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// FileURL is where the build's file for platform downloads from, pinned to
// the build, so a newer build published meanwhile can't take its place.
func (c *Client) FileURL(b *Build, platform string) string {
	return c.base + "/projects/" + url.PathEscape(b.Project) + "/versions/" + url.PathEscape(b.Version) +
		"/builds/" + strconv.Itoa(b.Build) + "/downloads/" + url.PathEscape(platform)
}

// FileSize asks for the size of the file at rawURL, one of FileURL's, which
// the API doesn't list.
func (c *Client) FileSize(ctx context.Context, rawURL string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		var re *fetch.RedirectError
		if errors.As(err, &re) {
			return 0, re
		}
		return 0, &fetch.NetError{Service: "GeyserMC", Err: err}
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, &fetch.StatusError{Service: "GeyserMC", Status: resp.StatusCode, Path: req.URL.Path}
	}
	if resp.ContentLength <= 0 {
		return 0, &fetch.StatusError{Service: "GeyserMC", Status: resp.StatusCode, Path: req.URL.Path, Detail: "the answer names no file size"}
	}
	return resp.ContentLength, nil
}

// ParseLatestLink reads a link to the newest build of a project's file on
// a platform, as Hangar lists them:
// https://download.geysermc.org/v2/projects/<project>/versions/latest/builds/latest/downloads/<platform>.
func ParseLatestLink(raw string) (project, platform string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 9 || parts[0] != "v2" || parts[1] != "projects" || parts[3] != "versions" || parts[4] != "latest" ||
		parts[5] != "builds" || parts[6] != "latest" || parts[7] != "downloads" || !plain(parts[2]) || !plain(parts[8]) {
		return "", "", false
	}
	return parts[2], parts[8], true
}

func plain(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
