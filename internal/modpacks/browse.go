package modpacks

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/modpacks/curseforge"
)

// Query is a search for packs Playkeeper can run.
type Query struct {
	Source addons.Source `json:"source"`
	Text   string        `json:"text"`
	// Type and MinecraftVersion narrow the results to packs with versions
	// for one server type or Minecraft version.
	Type             string `json:"type,omitempty"`
	MinecraftVersion string `json:"minecraftVersion,omitempty"`
	Sort             string `json:"sort"`   // relevance (default), downloads, updated or newest
	Offset           int    `json:"offset"` // at most 10000
	Limit            int    `json:"limit"`  // 1 to 25; 20 when zero
}

// Card is one pack in search results.
type Card struct {
	addons.Card
	// Types are the server types, of those Playkeeper runs, and
	// MinecraftVersions the release versions (newest first) the pack has
	// versions for, as far as its source says.
	Types             []string `json:"types"`
	MinecraftVersions []string `json:"minecraftVersions"`
	// Mods counts the projects the pack's newest version bundles, mostly
	// mods, when its source says (Modrinth lists them); zero otherwise.
	Mods int `json:"mods,omitempty"`
	// Unavailable says why Playkeeper cannot install the pack at all.
	Unavailable *addons.Notice `json:"unavailable,omitempty"`
}

// Results is one page of search results. Total counts the source's matches
// before packs Playkeeper cannot run are left out.
type Results struct {
	Cards  []Card `json:"cards"`
	Total  int    `json:"total"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// Version is one version of a pack, for the version picker.
type Version struct {
	ID        string    `json:"id"`
	Number    string    `json:"number"`
	Name      string    `json:"name"`
	Channel   string    `json:"channel"` // release, beta or alpha
	Published time.Time `json:"published"`
	Size      int64     `json:"size"` // of the pack's archive
	// Type and MinecraftVersion are what the source says the version needs,
	// empty when it does not say; the pack itself has the final say.
	Type             string `json:"type,omitempty"`
	MinecraftVersion string `json:"minecraftVersion,omitempty"`
	// Mods counts the projects the version bundles, as for Card.Mods.
	Mods int `json:"mods,omitempty"`
	// Unsupported says why Playkeeper cannot install this version.
	Unsupported *addons.Notice `json:"unsupported,omitempty"`

	bundled []string // Modrinth project ids
}

// Detail is a pack's page: its card, links and versions.
type Detail struct {
	Card
	SourceURL string    `json:"sourceUrl,omitempty"`
	IssuesURL string    `json:"issuesUrl,omitempty"`
	WikiURL   string    `json:"wikiUrl,omitempty"`
	Versions  []Version `json:"versions"`
	// Headline is the mod the pack is built around, named in the pack's
	// title ("Cobblemon" in "Cobblemon Modpack"), when its newest version
	// bundles one.
	Headline string `json:"headline,omitempty"`
}

// maxVersions bounds the versions listed for one pack; the newest come
// first.
const maxVersions = 100

// Search finds packs in one source. Packs only for the game client, only
// for Forge or only for Minecraft versions Playkeeper does not run are left
// out.
func (l *Library) Search(ctx context.Context, q Query) (*Results, error) {
	q.Text = strings.TrimSpace(q.Text)
	switch {
	case len([]rune(q.Text)) > 100:
		return nil, fail(addons.KindInvalid, kv("field", "text"), "The search text is too long.", "Use at most 100 characters.")
	case !slices.Contains([]string{"", "relevance", "downloads", "updated", "newest"}, q.Sort):
		return nil, fail(addons.KindInvalid, kv("field", "sort"), "Results can be sorted by relevance, downloads, last update or newest.", "")
	case q.Offset < 0 || q.Offset > 10000 || q.Limit < 0 || q.Limit > 25:
		return nil, fail(addons.KindInvalid, kv("field", "offset"), "That page of results is out of range.", "")
	case q.Type != "" && !slices.Contains(l.types(), q.Type):
		return nil, fail(addons.KindInvalid, kv("field", "type"),
			fmt.Sprintf("Playkeeper runs packs on %s servers.", l.typeList()), "")
	case q.MinecraftVersion != "" && l.minecraftUnsupported("", q.MinecraftVersion) != nil:
		return nil, fail(addons.KindInvalid, kv("field", "minecraftVersion", "oldest", l.minMinecraft()),
			fmt.Sprintf("Packs are offered for release versions of Minecraft from %s on.", l.minMinecraft()), "")
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	switch q.Source {
	case addons.Modrinth:
		return l.searchModrinth(ctx, q)
	case CurseForge:
		if l.CurseForge == nil {
			return nil, noCurseForge()
		}
		return l.searchCurseForge(ctx, q)
	}
	return nil, fail(addons.KindInvalid, kv("field", "source"),
		"Packs come from Modrinth or CurseForge, not \""+printable(string(q.Source))+"\".", "")
}

func (l *Library) typeList() string {
	names := make([]string, len(l.types()))
	for i, t := range l.types() {
		names[i] = typeName(t)
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// modrinthLoader is the loader tag Modrinth files a pack for a server type
// under.
func modrinthLoader(typ string) string {
	if typ == "vanilla" {
		return "minecraft"
	}
	return typ
}

func (l *Library) searchModrinth(ctx context.Context, q Query) (*Results, error) {
	types := l.types()
	if q.Type != "" {
		types = []string{q.Type}
	}
	loaders := make([]string, len(types))
	for i, t := range types {
		loaders[i] = modrinthLoader(t)
	}
	facets := [][]string{
		modrinth.Facet("project_type", "modpack"),
		modrinth.Facet("categories", loaders...),
		modrinth.Facet("server_side", "required", "optional"),
	}
	if q.MinecraftVersion != "" {
		facets = append(facets, modrinth.Facet("versions", q.MinecraftVersion))
	}
	index := q.Sort
	if index == "" {
		index = "relevance"
	}
	res, err := l.Modrinth.Search(ctx, modrinth.SearchQuery{Query: q.Text, Facets: facets, Index: index, Offset: q.Offset, Limit: q.Limit})
	if err != nil {
		return nil, Upstream(addons.Modrinth, err)
	}
	out := &Results{Cards: []Card{}, Total: res.TotalHits, Offset: q.Offset, Limit: q.Limit}
	latest := map[string]int{} // version id → card
	for _, h := range res.Hits {
		c := Card{
			Card: addons.Card{
				Source: addons.Modrinth, ProjectID: h.ProjectID, Slug: h.Slug, Name: printable(h.Title), Author: printable(h.Author),
				Summary: h.Description, Categories: categories(h.DisplayCategories, h.Categories), License: license(h.License),
				Downloads: h.Downloads, IconURL: h.IconURL, Updated: h.DateModified, PageURL: modrinthPage(h.Slug),
			},
			Types: l.loaderTypes(h.Categories), MinecraftVersions: l.releases(h.Versions),
		}
		if h.ProjectType != "modpack" || !h.RunsOnServer() || !c.fits(q) {
			continue
		}
		if validID(h.LatestVersion) {
			latest[h.LatestVersion] = len(out.Cards)
		}
		out.Cards = append(out.Cards, c)
	}
	// The mod counts are extra: without them the page still lists the packs.
	// A count is kept only when the latest version is for the Minecraft
	// version the card names, so the card and the pack's details agree.
	if len(latest) > 0 {
		if vs, err := l.Modrinth.Versions(ctx, slices.Sorted(maps.Keys(latest))); err == nil {
			for i := range vs {
				if n, ok := latest[vs[i].ID]; ok && vs[i].ProjectID == out.Cards[n].ProjectID && slices.Contains(vs[i].GameVersions, out.Cards[n].MinecraftVersions[0]) {
					out.Cards[n].Mods = len(bundled(&vs[i]))
				}
			}
		}
	}
	return out, nil
}

// bundled are the Modrinth projects a pack version includes, which Modrinth
// lists as its embedded dependencies.
func bundled(v *modrinth.Version) []string {
	var out []string
	for _, d := range v.Dependencies {
		if d.DependencyType == modrinth.Embedded && validID(d.ProjectID) && !slices.Contains(out, d.ProjectID) {
			out = append(out, d.ProjectID)
		}
	}
	return out
}

// headline finds, among the projects a pack bundles, the mod its title
// names: the longest title of at least four letters that the pack's title
// contains as a word.
func (l *Library) headline(ctx context.Context, pack string, ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	projects, err := l.Modrinth.Projects(ctx, ids[:min(len(ids), 200)])
	if err != nil {
		return ""
	}
	best := ""
	for _, p := range projects {
		name := printable(p.Title)
		if len([]rune(name)) >= 4 && len(name) > len(best) && containsWord(pack, name) {
			best = name
		}
	}
	return best
}

// containsWord reports whether s contains word, ignoring case, with no
// letter or digit right before or after it.
func containsWord(s, word string) bool {
	s, word = strings.ToLower(s), strings.ToLower(word)
	for i := 0; ; {
		j := strings.Index(s[i:], word)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(word)
		before, _ := utf8.DecodeLastRuneInString(s[:start])
		after, _ := utf8.DecodeRuneInString(s[end:])
		if (start == 0 || !unicode.IsLetter(before) && !unicode.IsDigit(before)) && (end == len(s) || !unicode.IsLetter(after) && !unicode.IsDigit(after)) {
			return true
		}
		i = start + 1
	}
}

// fits reports whether a card has something Playkeeper can run that the
// query asks for.
func (c *Card) fits(q Query) bool {
	return len(c.Types) > 0 && len(c.MinecraftVersions) > 0 &&
		(q.Type == "" || slices.Contains(c.Types, q.Type)) &&
		(q.MinecraftVersion == "" || slices.Contains(c.MinecraftVersions, q.MinecraftVersion))
}

func (l *Library) searchCurseForge(ctx context.Context, q Query) (*Results, error) {
	sort := map[string]int{"downloads": curseforge.SortTotalDownloads, "updated": curseforge.SortLastUpdated, "newest": curseforge.SortReleasedDate}[q.Sort]
	if sort == 0 {
		sort = curseforge.SortPopularity
	}
	res, err := l.CurseForge.Search(ctx, curseforge.SearchQuery{
		ClassID: curseforge.ClassModpacks, Text: q.Text, GameVersion: q.MinecraftVersion, ModLoaderType: curseForgeLoader(q.Type),
		SortField: sort, Index: q.Offset, PageSize: q.Limit,
	})
	if err != nil {
		return nil, Upstream(CurseForge, err)
	}
	out := &Results{Cards: []Card{}, Total: int(min(res.Pagination.TotalCount, curseforge.MaxResults)), Offset: q.Offset, Limit: q.Limit}
	for i := range res.Data {
		m := &res.Data[i]
		if m.ClassID != curseforge.ClassModpacks || m.GameID != curseforge.GameMinecraft {
			continue
		}
		if c := l.curseForgeCard(m); c.fits(q) {
			out.Cards = append(out.Cards, c)
		}
	}
	return out, nil
}

func curseForgeLoader(typ string) int {
	switch typ {
	case "fabric":
		return curseforge.LoaderFabric
	case "quilt":
		return curseforge.LoaderQuilt
	case "neoforge":
		return curseforge.LoaderNeoForge
	}
	return curseforge.LoaderAny
}

func (l *Library) curseForgeCard(m *curseforge.Mod) Card {
	c := Card{Card: addons.Card{
		Source: CurseForge, ProjectID: strconv.FormatInt(m.ID, 10), Slug: m.Slug, Name: printable(m.Name),
		Summary: m.Summary, Categories: []string{}, Downloads: m.DownloadCount, Updated: m.DateModified, PageURL: m.ProjectPage(),
	}}
	if len(m.Authors) > 0 {
		c.Author = printable(m.Authors[0].Name)
	}
	if m.Logo != nil {
		c.IconURL = m.Logo.ThumbnailURL
	}
	for _, cat := range m.Categories {
		if n := printable(cat.Name); n != "" && !slices.Contains(c.Categories, n) {
			c.Categories = append(c.Categories, n)
		}
	}
	var tags, versions []string
	for _, ix := range m.LatestFilesIndexes {
		// A pack without a mod loader is a vanilla pack.
		t := "vanilla"
		if ix.ModLoader != nil {
			switch *ix.ModLoader {
			case curseforge.LoaderFabric:
				t = "fabric"
			case curseforge.LoaderQuilt:
				t = "quilt"
			case curseforge.LoaderNeoForge:
				t = "neoforge"
			default:
				continue
			}
		}
		tags, versions = append(tags, t), append(versions, ix.GameVersion)
	}
	c.Types, c.MinecraftVersions = l.loaderTypes(tags), l.releases(versions)
	if m.AllowModDistribution != nil && !*m.AllowModDistribution {
		n := notice(addons.KindExternal, kv("pack", c.Name, "host", "CurseForge"),
			fmt.Sprintf("%s's author only allows downloading it through CurseForge's app, so Playkeeper cannot install it.", c.Name), "")
		c.Unavailable = &n
	}
	return c
}

// loaderTypes are the server types Playkeeper runs among a pack's loader
// tags, in the order of Library.Types.
func (l *Library) loaderTypes(tags []string) []string {
	out := []string{}
	for _, t := range l.types() {
		if slices.Contains(tags, modrinthLoader(t)) || slices.Contains(tags, t) {
			out = append(out, t)
		}
	}
	return out
}

// releases are the Minecraft release versions Playkeeper runs among vs,
// newest first.
func (l *Library) releases(vs []string) []string {
	out := []string{}
	for _, v := range vs {
		if l.minecraftUnsupported("", v) == nil && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return minecraft.CompareMinecraft(b, a) })
	return out[:min(len(out), 20)]
}

// categories drops the loader tags Modrinth mixes into categories.
func categories(display, all []string) []string {
	if len(display) == 0 {
		display = all
	}
	out := []string{}
	for _, c := range display {
		if !modrinth.IsLoaderTag(c) {
			out = append(out, printable(c))
		}
	}
	return out
}

// license shows an SPDX id as it is and a custom LicenseRef-Some-Name as
// "Some Name".
func license(id string) string {
	if rest, ok := strings.CutPrefix(id, "LicenseRef-"); ok {
		if strings.EqualFold(rest, "unknown") {
			return ""
		}
		return printable(strings.ReplaceAll(rest, "-", " "))
	}
	return printable(id)
}

func modrinthPage(slug string) string {
	return "https://modrinth.com/modpack/" + url.PathEscape(slug)
}

// link passes on an https address from a pack's page and drops anything
// else.
func link(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(s) > 512 {
		return ""
	}
	return s
}

// Versions lists a pack's versions, newest first, optionally only those for
// one Minecraft version, each with why Playkeeper cannot install it if it
// cannot.
func (l *Library) Versions(ctx context.Context, source addons.Source, project, mc string) ([]Version, error) {
	if mc != "" && !releaseVersion.MatchString(mc) {
		return nil, invalid("Minecraft version")
	}
	return l.versions(ctx, source, project, mc)
}

func (l *Library) versions(ctx context.Context, source addons.Source, project, mc string) ([]Version, error) {
	switch source {
	case addons.Modrinth:
		proj, err := l.modrinthPack(ctx, project)
		if err != nil {
			return nil, err
		}
		return l.modrinthVersions(ctx, proj, mc)
	case CurseForge:
		mod, err := l.curseForgePack(ctx, project)
		if err != nil {
			return nil, err
		}
		return l.curseForgeVersions(ctx, mod, mc)
	}
	return nil, fail(addons.KindInvalid, kv("field", "source"),
		"Packs come from Modrinth or CurseForge, not \""+printable(string(source))+"\".", "")
}

func (l *Library) modrinthVersions(ctx context.Context, proj *modrinth.Project, mc string) ([]Version, error) {
	var f modrinth.VersionFilter
	if mc != "" {
		f.GameVersions = []string{mc}
	}
	vs, err := l.Modrinth.ProjectVersions(ctx, proj.ID, f)
	if err != nil {
		return nil, Upstream(addons.Modrinth, err)
	}
	name := printable(proj.Title)
	out := []Version{}
	for i := range vs {
		v := &vs[i]
		if v.ProjectID != proj.ID || v.Status != "" && v.Status != "listed" && v.Status != "archived" {
			continue
		}
		typ, m, why := l.modrinthTarget(name, v)
		file, ok := mrpackFile(v)
		if why == nil && !ok {
			n := notice(addons.KindNoVersion, kv("pack", name, "version", printable(v.VersionNumber)),
				fmt.Sprintf("Version %s of %s has no .mrpack file.", printable(v.VersionNumber), name), "Choose another version.")
			why = &n
		}
		if m == "" && len(v.GameVersions) > 0 {
			m = v.GameVersions[0]
		}
		ids := bundled(v)
		out = append(out, Version{
			ID: v.ID, Number: printable(v.VersionNumber), Name: printable(v.Name), Channel: channel(v.VersionType),
			Published: v.DatePublished, Size: file.Size, Type: typ, MinecraftVersion: printable(m), Mods: len(ids), Unsupported: why,
			bundled: ids,
		})
	}
	return newestFirst(out), nil
}

func (l *Library) curseForgeVersions(ctx context.Context, mod *curseforge.Mod, mc string) ([]Version, error) {
	files, _, err := l.CurseForge.ModFiles(ctx, mod.ID, curseforge.FilesQuery{GameVersion: mc, PageSize: curseforge.MaxPageSize})
	if err != nil {
		return nil, Upstream(CurseForge, err)
	}
	name := printable(mod.Name)
	out := []Version{}
	for i := range files {
		f := &files[i]
		if f.ModID != mod.ID || !usableFile(f) {
			continue
		}
		typ, m, why := l.curseForgeTarget(name, f)
		if why == nil && f.DownloadURL == "" {
			n := notice(addons.KindExternal, kv("pack", name, "host", "CurseForge"),
				fmt.Sprintf("%s's author only allows downloading it through CurseForge's app, so Playkeeper cannot install it.", name), "")
			why = &n
		}
		out = append(out, Version{
			ID: strconv.FormatInt(f.ID, 10), Number: printable(f.DisplayName), Name: printable(f.FileName), Channel: cfChannel(f.ReleaseType),
			Published: f.FileDate, Size: f.FileLength, Type: typ, MinecraftVersion: m, Unsupported: why,
		})
	}
	return newestFirst(out), nil
}

// Newest is the version a pack installs when none is asked for: the release
// Playkeeper can install for the newest Minecraft version (the latest one
// published for it), else the same among betas and alphas; nil when it can
// install none. vs is newest first. Packs that keep a version per Minecraft
// version often publish for an older one last, and a pack's card names its
// newest Minecraft version.
func Newest(vs []Version) *Version {
	var rel, pre *Version
	newer := func(v, than *Version) bool {
		return than == nil || minecraft.CompareMinecraft(v.MinecraftVersion, than.MinecraftVersion) > 0
	}
	for i := range vs {
		v := &vs[i]
		switch {
		case v.Unsupported != nil:
		case v.Channel == "release":
			if newer(v, rel) {
				rel = v
			}
		case newer(v, pre):
			pre = v
		}
	}
	if rel != nil {
		return rel
	}
	return pre
}

func newestFirst(vs []Version) []Version {
	slices.SortStableFunc(vs, func(a, b Version) int { return b.Published.Compare(a.Published) })
	return vs[:min(len(vs), maxVersions)]
}

// Detail returns a pack's page with its versions.
func (l *Library) Detail(ctx context.Context, source addons.Source, project string) (*Detail, error) {
	switch source {
	case addons.Modrinth:
		proj, err := l.modrinthPack(ctx, project)
		if err != nil {
			return nil, err
		}
		vs, err := l.modrinthVersions(ctx, proj, "")
		if err != nil {
			return nil, err
		}
		d := &Detail{
			Card: Card{
				Card: addons.Card{
					Source: addons.Modrinth, ProjectID: proj.ID, Slug: proj.Slug, Name: printable(proj.Title), Summary: proj.Description,
					Categories: categories(nil, slices.Concat(proj.Categories, proj.AdditionalCategories)), License: license(proj.License.ID),
					Downloads: proj.Downloads, IconURL: proj.IconURL, Updated: proj.Updated, PageURL: modrinthPage(proj.Slug),
				},
				Types: l.loaderTypes(proj.Loaders), MinecraftVersions: l.releases(proj.GameVersions),
			},
			SourceURL: link(proj.SourceURL), IssuesURL: link(proj.IssuesURL), WikiURL: link(proj.WikiURL), Versions: vs,
		}
		if v := Newest(vs); v != nil {
			d.Mods = v.Mods
			d.Headline = l.headline(ctx, d.Name, v.bundled)
		}
		if !proj.RunsOnServer() {
			n := notice(addons.KindClientOnly, kv("pack", d.Name), fmt.Sprintf("%s is for the game client only, not for servers.", d.Name), "Choose another pack.")
			d.Unavailable = &n
		}
		return d, nil
	case CurseForge:
		mod, err := l.curseForgePack(ctx, project)
		if err != nil {
			return nil, err
		}
		vs, err := l.curseForgeVersions(ctx, mod, "")
		if err != nil {
			return nil, err
		}
		return &Detail{
			Card: l.curseForgeCard(mod), SourceURL: link(mod.Links.SourceURL), IssuesURL: link(mod.Links.IssuesURL),
			WikiURL: link(mod.Links.WikiURL), Versions: vs,
		}, nil
	}
	return nil, fail(addons.KindInvalid, kv("field", "source"),
		"Packs come from Modrinth or CurseForge, not \""+printable(string(source))+"\".", "")
}
