package addons

import (
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons/hangar"
	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
)

// Query is a search in one source for add-ons that fit a server.
type Query struct {
	Source Source `json:"source"`
	Text   string `json:"text"`
	Sort   string `json:"sort"`   // relevance (default), downloads, updated or newest
	Offset int    `json:"offset"` // at most 10000
	Limit  int    `json:"limit"`  // 1 to 25; 20 when zero
}

// Card is one add-on in search results, with what the UI shows on its card.
type Card struct {
	Source     Source   `json:"source"`
	ProjectID  string   `json:"projectId"`
	Slug       string   `json:"slug"`
	Name       string   `json:"name"`
	Author     string   `json:"author,omitempty"`
	Summary    string   `json:"summary"`
	Categories []string `json:"categories"`
	License    string   `json:"license,omitempty"`
	Downloads  int64    `json:"downloads"`
	// IconURL is on the source's CDN. The panel serves it through FetchIcon;
	// the browser never loads it directly.
	IconURL string    `json:"iconUrl,omitempty"`
	Updated time.Time `json:"updated"`
	PageURL string    `json:"pageUrl"`
}

// Results is one page of search results. Total counts the source's matches
// before client-only projects are left out.
type Results struct {
	Cards  []Card `json:"cards"`
	Total  int    `json:"total"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// Search finds add-ons in one source that fit the server's type and
// Minecraft version. Modrinth projects that only run in the game client are
// left out.
func (l *Library) Search(ctx context.Context, srv Server, q Query) (*Results, error) {
	t, err := TargetFor(srv.Type)
	if err != nil {
		return nil, err
	}
	if err := t.supports(q.Source); err != nil {
		return nil, err
	}
	if err := checkMinecraft(srv); err != nil {
		return nil, err
	}
	q.Text = strings.TrimSpace(q.Text)
	switch {
	case len([]rune(q.Text)) > 100:
		return nil, fail(KindInvalid, kv("field", "text"), "The search text is too long.", "Use at most 100 characters.")
	case !slices.Contains([]string{"", "relevance", "downloads", "updated", "newest"}, q.Sort):
		return nil, fail(KindInvalid, kv("field", "sort"), "Results can be sorted by relevance, downloads, last update or newest.", "")
	case q.Offset < 0 || q.Offset > 10000 || q.Limit < 0 || q.Limit > hangar.MaxLimit:
		return nil, fail(KindInvalid, kv("field", "offset"), "That page of results is out of range.", "")
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Source == Hangar {
		return l.searchHangar(ctx, srv, t, q)
	}
	return l.searchModrinth(ctx, srv, t, q)
}

func (l *Library) searchModrinth(ctx context.Context, srv Server, t Target, q Query) (*Results, error) {
	index := q.Sort
	if index == "" {
		index = "relevance"
	}
	res, err := l.Modrinth.Search(ctx, modrinth.SearchQuery{
		Query: q.Text,
		Facets: [][]string{
			modrinth.Facet("categories", t.Loaders...),
			modrinth.Facet("versions", srv.MinecraftVersion),
			modrinth.Facet("project_type", t.Kind),
			modrinth.Facet("server_side", "required", "optional"),
		},
		Index:  index,
		Offset: q.Offset,
		Limit:  q.Limit,
	})
	if err != nil {
		return nil, upstream(Modrinth, err)
	}
	out := &Results{Cards: []Card{}, Total: res.TotalHits, Offset: q.Offset, Limit: q.Limit}
	for _, h := range res.Hits {
		if !h.RunsOnServer() || !slices.Contains(h.Versions, srv.MinecraftVersion) {
			continue
		}
		cats := h.DisplayCategories
		if len(cats) == 0 {
			cats = h.Categories
		}
		out.Cards = append(out.Cards, Card{
			Source:     Modrinth,
			ProjectID:  h.ProjectID,
			Slug:       h.Slug,
			Name:       h.Title,
			Author:     h.Author,
			Summary:    h.Description,
			Categories: categories(cats),
			License:    modrinthLicense(h.License),
			Downloads:  h.Downloads,
			IconURL:    h.IconURL,
			Updated:    h.DateModified,
			PageURL:    modrinthPage(t, h.Slug),
		})
	}
	return out, nil
}

func (l *Library) searchHangar(ctx context.Context, srv Server, t Target, q Query) (*Results, error) {
	sort := map[string]string{"downloads": "-downloads", "updated": "-updated", "newest": "-newest"}[q.Sort]
	if sort == "" && q.Text == "" {
		sort = "-downloads"
	}
	res, err := l.Hangar.Search(ctx, hangar.SearchQuery{Query: q.Text, Platform: t.HangarPlatform, Version: srv.MinecraftVersion, Sort: sort, Offset: q.Offset, Limit: q.Limit})
	if err != nil {
		return nil, upstream(Hangar, err)
	}
	out := &Results{Cards: []Card{}, Total: res.Pagination.Count, Offset: q.Offset, Limit: q.Limit}
	for _, p := range res.Result {
		if p.Visibility != "" && p.Visibility != "public" {
			continue
		}
		out.Cards = append(out.Cards, hangarCard(&p))
	}
	return out, nil
}

func hangarCard(p *hangar.Project) Card {
	var cats []string
	if p.Category != "" {
		cats = []string{p.Category}
	}
	return Card{
		Source:     Hangar,
		ProjectID:  strconv.FormatInt(p.ID, 10),
		Slug:       p.Namespace.Slug,
		Name:       p.Name,
		Author:     p.Namespace.Owner,
		Summary:    p.Description,
		Categories: cats,
		License:    hangarLicense(p.Settings.License),
		Downloads:  p.Stats.Downloads,
		IconURL:    p.AvatarURL,
		Updated:    p.LastUpdated,
		PageURL:    p.PageURL(),
	}
}

// categories drops the loader tags Modrinth mixes into categories.
func categories(tags []string) []string {
	out := []string{}
	for _, c := range tags {
		if !modrinth.IsLoaderTag(c) {
			out = append(out, c)
		}
	}
	return out
}

// modrinthLicense shows an SPDX id as is and a custom LicenseRef-Some-Name
// as "Some Name".
func modrinthLicense(id string) string {
	if rest, ok := strings.CutPrefix(id, "LicenseRef-"); ok {
		if strings.EqualFold(rest, "unknown") {
			return ""
		}
		return strings.ReplaceAll(rest, "-", " ")
	}
	return id
}

func hangarLicense(l hangar.License) string {
	switch l.Type {
	case "", "Unspecified":
		return ""
	case "Other":
		return l.Name
	}
	return l.Type
}

func modrinthPage(t Target, slug string) string {
	return "https://modrinth.com/" + t.Kind + "/" + url.PathEscape(slug)
}

func checkMinecraft(srv Server) error {
	v := srv.MinecraftVersion
	ok := v != "" && len(v) <= 32
	for _, r := range v {
		ok = ok && (r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' || r == '_' || r == '+')
	}
	if !ok {
		return fail(KindInvalid, kv("field", "minecraftVersion"), "The server has no valid Minecraft version set.", "Choose a version in the server's settings.")
	}
	return nil
}
