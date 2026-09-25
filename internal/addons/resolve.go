package addons

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/addons/hangar"
	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
)

// VersionInfo is one version of an add-on that fits the server.
type VersionInfo struct {
	VersionID     string    `json:"versionId"`
	VersionNumber string    `json:"versionNumber"`
	Channel       string    `json:"channel"` // release, beta or alpha
	Published     time.Time `json:"published"`
	FileName      string    `json:"fileName,omitempty"`
	Size          int64     `json:"size,omitempty"`
	// ExternalURL is set when the file is only offered on another site, so
	// Playkeeper cannot install it.
	ExternalURL string `json:"externalUrl,omitempty"`
}

// project is what resolving needs to know about a project.
type project struct {
	Source                           Source
	ID, Slug, Name, Summary, IconURL string
	clientOnly                       bool
}

// candidate is one version of a project that fits the server: a file to
// install, or only an external link.
type candidate struct {
	project
	VersionID string
	Number    string
	Channel   string
	Published time.Time
	FileName  string
	URL       string
	HashAlgo  string
	Hash      string
	Size      int64
	External  string
	deps      []dep
	notes     string // the author's Markdown notes for the version
}

func (c candidate) key() Key { return Key{c.Source, c.ProjectID()} }

// ProjectID is the candidate's project id.
func (c candidate) ProjectID() string { return c.project.ID }

func (c candidate) info() VersionInfo {
	return VersionInfo{VersionID: c.VersionID, VersionNumber: c.Number, Channel: c.Channel, Published: c.Published, FileName: c.FileName, Size: c.Size, ExternalURL: c.External}
}

type dep struct {
	typ       string // modrinth.Required, Optional or Incompatible
	projectID string
	versionID string // Modrinth: a pinned version
	name      string // Hangar: the plugin's name. Modrinth: a file that is not on Modrinth
	external  string // Hangar: where to get a plugin that is not on Hangar
}

const release = "release"

// Versions lists the versions of a project that fit the server, newest
// first. Pre-releases are included; Channel marks them.
func (l *Library) Versions(ctx context.Context, srv Server, src Source, ref string) ([]VersionInfo, error) {
	t, err := l.check(srv, src, ref, "")
	if err != nil {
		return nil, err
	}
	p, err := l.project(ctx, src, ref)
	if err != nil {
		return nil, err
	}
	if p.clientOnly {
		return nil, clientOnly(p)
	}
	cands, err := l.candidates(ctx, t, srv.MinecraftVersion, p)
	if err != nil {
		return nil, err
	}
	out := []VersionInfo{}
	for _, c := range cands {
		out = append(out, c.info())
	}
	return out, nil
}

// check validates a request against the server and returns its target.
func (l *Library) check(srv Server, src Source, ref, versionID string) (Target, error) {
	t, err := TargetFor(srv.Type)
	if err != nil {
		return t, err
	}
	if err := t.supports(src); err != nil {
		return t, err
	}
	if err := checkMinecraft(srv); err != nil {
		return t, err
	}
	if !validRef(ref) {
		return t, fail(KindInvalid, kv("field", "project"), "That is not a valid project name or id.", "Pick the add-on from the search results.")
	}
	if versionID != "" && !validRef(versionID) {
		return t, fail(KindInvalid, kv("field", "versionId"), "That is not a valid version id.", "Pick the version from the list.")
	}
	return t, nil
}

func validRef(s string) bool {
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

func (l *Library) project(ctx context.Context, src Source, ref string) (*project, error) {
	p, _, err := l.projectCard(ctx, Target{}, src, ref)
	return p, err
}

// projectCard is project with the card the library shows for it; t only
// shapes the card's page address.
func (l *Library) projectCard(ctx context.Context, t Target, src Source, ref string) (*project, *Card, error) {
	switch src {
	case Modrinth:
		p, err := l.Modrinth.Project(ctx, ref)
		if err != nil {
			return nil, nil, lookupError(src, ref, err)
		}
		// Modrinth's v2 API calls plugins "mod" too.
		if p.ProjectType != "" && p.ProjectType != "mod" && p.ProjectType != "plugin" {
			what := map[string]string{"modpack": "modpack", "resourcepack": "resource pack", "shader": "shader pack", "datapack": "data pack"}[p.ProjectType]
			if what == "" {
				what = "different kind of project"
			}
			return nil, nil, fail(KindNotAddon, kv("name", p.Title, "type", p.ProjectType),
				fmt.Sprintf("%s is a %s, not a plugin or mod.", p.Title, what), "Playkeeper's library installs plugins and mods only.")
		}
		card := &Card{
			Source: Modrinth, ProjectID: p.ID, Slug: p.Slug, Name: p.Title, Summary: p.Description,
			Categories: categories(p.Categories), License: modrinthLicense(p.License.ID), Downloads: p.Downloads,
			IconURL: p.IconURL, Updated: p.Updated, PageURL: modrinthPage(t, p.Slug),
		}
		return modrinthProject(p), card, nil
	case Hangar:
		p, err := l.Hangar.Project(ctx, ref)
		if err != nil {
			return nil, nil, lookupError(src, ref, err)
		}
		card := hangarCard(p)
		return &project{Source: Hangar, ID: strconv.FormatInt(p.ID, 10), Slug: p.Namespace.Slug, Name: p.Name, Summary: p.Description, IconURL: p.AvatarURL}, &card, nil
	}
	return nil, nil, fail(KindInvalid, kv("field", "source"), "Add-ons come from Modrinth or Hangar.", "")
}

func modrinthProject(p *modrinth.Project) *project {
	return &project{Source: Modrinth, ID: p.ID, Slug: p.Slug, Name: p.Title, Summary: p.Description, IconURL: p.IconURL, clientOnly: !p.RunsOnServer()}
}

func lookupError(src Source, ref string, err error) error {
	if errors.Is(err, fetch.ErrNotFound) {
		return &Error{Notice: notice(KindNotFound, kv("source", src.Name(), "project", printable(ref)),
			fmt.Sprintf("%s has no project \"%s\".", src.Name(), printable(ref)), "Check the name, or search for the add-on instead."), Err: err}
	}
	return upstream(src, err)
}

func clientOnly(p *project) *Error {
	return fail(KindClientOnly, kv("name", p.Name),
		p.Name+" only runs in the game client, not on a server.", "Players install it in their own game instead.")
}

// candidates lists the versions of p that fit the target and Minecraft
// version, newest first.
func (l *Library) candidates(ctx context.Context, t Target, mc string, p *project) ([]candidate, error) {
	var out []candidate
	switch p.Source {
	case Modrinth:
		vs, err := l.Modrinth.ProjectVersions(ctx, p.ID, modrinth.VersionFilter{Loaders: t.Loaders, GameVersions: []string{mc}})
		if err != nil {
			return nil, lookupError(Modrinth, p.ID, err)
		}
		for i := range vs {
			if c, ok := modrinthCandidate(p, &vs[i], t, mc); ok {
				out = append(out, c)
			}
		}
	case Hangar:
		vl, err := l.Hangar.Versions(ctx, p.ID, hangar.VersionFilter{Platform: t.HangarPlatform, PlatformVersion: mc, Limit: hangar.MaxLimit})
		if err != nil {
			return nil, lookupError(Hangar, p.ID, err)
		}
		for i := range vl.Result {
			if c, ok := hangarCandidate(p, &vl.Result[i], t, mc); ok {
				out = append(out, c)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b candidate) int { return b.Published.Compare(a.Published) })
	return out, nil
}

// exact fetches one version of p by id, for versions beyond the first page.
func (l *Library) exact(ctx context.Context, t Target, mc string, p *project, versionID string) (candidate, bool, error) {
	switch p.Source {
	case Modrinth:
		v, err := l.Modrinth.Version(ctx, versionID)
		if errors.Is(err, fetch.ErrNotFound) {
			return candidate{}, false, nil
		}
		if err != nil {
			return candidate{}, false, upstream(Modrinth, err)
		}
		if v.ProjectID != p.ID {
			return candidate{}, false, nil
		}
		c, ok := modrinthCandidate(p, v, t, mc)
		return c, ok, nil
	case Hangar:
		id, err := strconv.ParseInt(versionID, 10, 64)
		if err != nil {
			return candidate{}, false, nil
		}
		v, err := l.Hangar.Version(ctx, id)
		if errors.Is(err, fetch.ErrNotFound) {
			return candidate{}, false, nil
		}
		if err != nil {
			return candidate{}, false, upstream(Hangar, err)
		}
		if strconv.FormatInt(v.ProjectID, 10) != p.ID {
			return candidate{}, false, nil
		}
		c, ok := hangarCandidate(p, v, t, mc)
		return c, ok, nil
	}
	return candidate{}, false, nil
}

// choose picks the version of p to install: versionID when given, else the
// newest release, else (when allowed) the newest pre-release.
func (l *Library) choose(ctx context.Context, srv Server, t Target, p *project, versionID string, allowPre bool) (candidate, error) {
	cands, err := l.candidates(ctx, t, srv.MinecraftVersion, p)
	if err != nil {
		return candidate{}, err
	}
	if versionID != "" {
		for _, c := range cands {
			if c.VersionID == versionID {
				return c, nil
			}
		}
		c, ok, err := l.exact(ctx, t, srv.MinecraftVersion, p, versionID)
		if err != nil {
			return candidate{}, err
		}
		if !ok {
			return candidate{}, fail(KindNoVersion, kv("name", p.Name, "version", versionID, "minecraft", srv.MinecraftVersion, "type", t.Type),
				fmt.Sprintf("That version of %s does not fit this server (%s, Minecraft %s).", p.Name, t.Name(), srv.MinecraftVersion),
				"Choose a version from the list.")
		}
		return c, nil
	}
	c, e := pick(p, cands, allowPre, srv, t)
	if e != nil {
		return candidate{}, e
	}
	return c, nil
}

func pick(p *project, cands []candidate, allowPre bool, srv Server, t Target) (candidate, *Error) {
	for _, c := range cands {
		if c.Channel == release {
			return c, nil
		}
	}
	if len(cands) == 0 {
		return candidate{}, fail(KindNoVersion, kv("name", p.Name, "minecraft", srv.MinecraftVersion, "type", t.Type),
			fmt.Sprintf("%s has no version for %s servers on Minecraft %s.", p.Name, t.Name(), srv.MinecraftVersion),
			"Check the add-on's page for supported versions, or look for an alternative.")
	}
	if allowPre {
		return cands[0], nil
	}
	c := cands[0]
	return candidate{}, fail(KindOnlyPrerelease, kv("name", p.Name, "version", c.Number, "channel", c.Channel, "minecraft", srv.MinecraftVersion),
		fmt.Sprintf("%s has only pre-release versions for Minecraft %s (newest: %s, a %s).", p.Name, srv.MinecraftVersion, c.Number, c.Channel),
		"Pre-releases can be unstable. Allow pre-releases to install it anyway.")
}

func modrinthCandidate(p *project, v *modrinth.Version, t Target, mc string) (candidate, bool) {
	if v.Status != "" && v.Status != "listed" || !overlaps(v.Loaders, t.Loaders) || !slices.Contains(v.GameVersions, mc) || !v.RunsOnServer() {
		return candidate{}, false
	}
	f, ok := v.PrimaryFile()
	if !ok {
		return candidate{}, false
	}
	c := candidate{project: *p, VersionID: v.ID, Number: v.VersionNumber, Channel: modrinthChannel(v.VersionType), Published: v.DatePublished,
		FileName: f.Filename, URL: f.URL, HashAlgo: "sha512", Hash: f.Hashes.SHA512, Size: f.Size, notes: v.Changelog}
	for _, d := range v.Dependencies {
		switch d.DependencyType {
		case modrinth.Required, modrinth.Optional, modrinth.Incompatible:
			c.deps = append(c.deps, dep{typ: d.DependencyType, projectID: d.ProjectID, versionID: d.VersionID, name: d.FileName})
		}
	}
	return c, true
}

// modrinthChannel maps Modrinth's version type; anything unknown counts as
// a pre-release.
func modrinthChannel(versionType string) string {
	switch versionType {
	case release, "alpha":
		return versionType
	}
	return "beta"
}

func hangarCandidate(p *project, v *hangar.Version, t Target, mc string) (candidate, bool) {
	d, ok := v.Downloads[t.HangarPlatform]
	if !ok || v.Visibility != "" && v.Visibility != "public" || !slices.Contains(v.PlatformDependencies[t.HangarPlatform], mc) {
		return candidate{}, false
	}
	ch := release
	switch {
	case v.Channel.Stable():
	case strings.Contains(strings.ToLower(v.Channel.Name), "alpha"):
		ch = "alpha"
	default:
		ch = "beta"
	}
	c := candidate{project: *p, VersionID: strconv.FormatInt(v.ID, 10), Number: v.Name, Channel: ch, Published: v.CreatedAt, notes: v.Description}
	switch {
	case d.FileInfo != nil && d.DownloadURL != "":
		c.FileName, c.URL, c.HashAlgo, c.Hash, c.Size = d.FileInfo.Name, d.DownloadURL, "sha256", d.FileInfo.SHA256Hash, d.FileInfo.SizeBytes
	case d.ExternalURL != "":
		c.External = d.ExternalURL
	default:
		return candidate{}, false
	}
	for _, pd := range v.PluginDependencies[t.HangarPlatform] {
		typ := modrinth.Optional
		if pd.Required {
			typ = modrinth.Required
		}
		dd := dep{typ: typ, name: pd.Name, external: pd.ExternalURL}
		if pd.ProjectID != nil {
			dd.projectID = strconv.FormatInt(*pd.ProjectID, 10)
		}
		c.deps = append(c.deps, dd)
	}
	return c, true
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}
