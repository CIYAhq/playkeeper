package addons

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
)

// category is one browse category and what each source calls it; an empty
// name means the source has no such category.
type category struct {
	id, modrinth, hangar string
}

// categoryList is the order the UI lists categories in.
var categoryList = []category{
	{"admin", "management", "admin_tools"},
	{"chat", "social", "chat"},
	{"economy", "economy", "economy"},
	{"gameplay", "game-mechanics", "gameplay"},
	{"minigames", "minigame", "games"},
	{"world", "worldgen", "world_management"},
	{"protection", "", "protection"},
	{"optimization", "optimization", ""},
	{"utility", "utility", "misc"},
	{"library", "library", "dev_tools"},
	{"adventure", "adventure", ""},
	{"technology", "technology", ""},
	{"magic", "magic", ""},
	{"storage", "storage", ""},
	{"mobs", "mobs", ""},
}

func findCategory(id string) (category, bool) {
	i := slices.IndexFunc(categoryList, func(c category) bool { return c.id == id })
	if i < 0 {
		return category{}, false
	}
	return categoryList[i], true
}

func (c category) in(s Source) string {
	if s == Hangar {
		return c.hangar
	}
	return c.modrinth
}

// Categories lists the browse categories some source has add-ons in for
// the target.
func Categories(t Target) []string {
	out := []string{}
	for _, c := range categoryList {
		if slices.ContainsFunc(t.Sources(), func(s Source) bool { return c.in(s) != "" }) {
			out = append(out, c.id)
		}
	}
	return out
}

// BrowseQuery is a search across every source with add-ons for the server.
type BrowseQuery struct {
	Text string `json:"text"`
	// Category is one of Categories; empty means every category.
	Category string `json:"category"`
	// Sort is downloads (the default), relevance or updated.
	Sort string `json:"sort"`
	Page int    `json:"page"`
}

// BrowseResults is one page from each source, merged.
type BrowseResults struct {
	Cards []Card `json:"cards"`
	// More is set when some source has further pages.
	More bool `json:"more"`
	// Unanswered explains sources that failed while others answered.
	Unanswered []Notice `json:"unanswered"`
}

// browsePage is how many cards Browse asks each source for.
const browsePage = 12

// Browse searches every source with add-ons for the server at once and
// merges the pages: an add-on listed on both appears once, from the listing
// with more downloads. It fails only when no source answered.
func (l *Library) Browse(ctx context.Context, srv Server, q BrowseQuery) (*BrowseResults, error) {
	t, err := TargetFor(srv.Type)
	if err != nil {
		return nil, err
	}
	sort := q.Sort
	switch sort {
	case "", "downloads":
		sort = "downloads"
	case "relevance", "updated":
	default:
		return nil, fail(KindInvalid, kv("field", "sort"), "Results can be sorted by downloads, relevance or last update.", "")
	}
	if q.Page < 0 || q.Page*browsePage > 10000 {
		return nil, fail(KindInvalid, kv("field", "page"), "That page of results is out of range.", "")
	}
	if q.Category != "" {
		if _, ok := findCategory(q.Category); !ok {
			return nil, fail(KindInvalid, kv("field", "category"), "That is not one of the library's categories.", "")
		}
	}
	srcs := t.Sources()
	pages := make([]*Results, len(srcs))
	errs := make([]error, len(srcs))
	var wg sync.WaitGroup
	for i, s := range srcs {
		wg.Go(func() {
			pages[i], errs[i] = l.Search(ctx, srv, Query{Source: s, Text: q.Text, Category: q.Category, Sort: sort, Offset: q.Page * browsePage, Limit: browsePage})
		})
	}
	wg.Wait()

	out := &BrowseResults{Cards: []Card{}, Unanswered: []Notice{}}
	var lists [][]Card
	for i, res := range pages {
		if errs[i] != nil {
			var e *Error
			if !errors.As(errs[i], &e) || e.Kind == KindInvalid {
				return nil, errs[i]
			}
			out.Unanswered = append(out.Unanswered, e.Notice)
			continue
		}
		lists = append(lists, res.Cards)
		out.More = out.More || res.Offset+res.Limit < res.Total
	}
	if len(lists) == 0 {
		return nil, errs[0]
	}
	out.Cards = mergeCards(lists, sort)
	return out, nil
}

// mergeCards combines each source's page. Relevance takes the sources'
// results in turns; the other orders sort the combined list.
func mergeCards(lists [][]Card, sort string) []Card {
	var all []Card
	for i := 0; ; i++ {
		added := false
		for _, l := range lists {
			if i < len(l) {
				all, added = append(all, l[i]), true
			}
		}
		if !added {
			break
		}
	}
	best := map[string]int{} // normalized name → index in all of the listing kept
	for i, c := range all {
		n := norm(c.Name)
		if j, ok := best[n]; !ok || c.Downloads > all[j].Downloads {
			best[n] = i
		}
	}
	out := []Card{}
	for i, c := range all {
		if best[norm(c.Name)] == i {
			out = append(out, c)
		}
	}
	switch sort {
	case "downloads":
		slices.SortStableFunc(out, func(a, b Card) int { return cmp.Compare(b.Downloads, a.Downloads) })
	case "updated":
		slices.SortStableFunc(out, func(a, b Card) int { return b.Updated.Compare(a.Updated) })
	}
	return out
}

// sameAddon reports whether a card and an installed add-on are the same
// project, from the same source or listed on both under one name.
func sameAddon(c Card, rec Installed) bool {
	n := norm(c.Name)
	return c.Source == rec.Source && c.ProjectID == rec.ProjectID || n != "" && n == norm(rec.Name)
}

// InstalledCard reports whether the card is an add-on in installed.
func InstalledCard(c Card, installed []Installed) bool {
	return slices.ContainsFunc(installed, func(rec Installed) bool { return sameAddon(c, rec) })
}
