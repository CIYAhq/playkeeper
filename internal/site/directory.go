package site

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/templates"
)

// The template directory at /templates: every template the release people
// install can open, as cards to search, filter and sort. Without scripts it
// is pages of cards, a page per category and a page per template, all
// linked; with them, js/templates.js searches and filters every template at
// once, from the index the build writes (js/templates-index.js). What a
// template is listed under comes from cards.json and taxonomy.json in
// site/data/templates.

// PerPage is how many cards a page of the directory or of a category shows.
const PerPage = 24

// minIndexedCategory is how many templates a category needs before search
// engines index its page, when it has no page of its own in site/pages.
const minIndexedCategory = 3

var reDirSlug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Taxonomy is site/data/templates/taxonomy.json: the categories templates
// are listed under, the tags that say what they add, and the tags each
// add-on brings, so a template with CoreProtect is tagged without saying so.
type Taxonomy struct {
	Categories map[string]*Category `json:"categories"`
	Tags       map[string]*Tag      `json:"tags"`
	AddonTags  map[string][]string  `json:"addonTags"`
}

// Category is a game mode or theme templates are listed under, with its own
// page at /templates/<ID>.
type Category struct {
	ID   string `json:"-"`
	Name string `json:"name"`
	// Intro opens its page; Description is its page's meta description when
	// site/pages has no page for it, whose own settings say it otherwise.
	Intro       string `json:"intro"`
	Description string `json:"description,omitempty"`
	// Art is the scene its page shows, the first template's when empty.
	Art string `json:"art,omitempty"`
	// Templates are its listed templates, the most popular first.
	Templates []*TemplateCard `json:"-"`
	// own is its page in site/pages, when it has one.
	own *Page
}

// Path is the category's page.
func (c *Category) Path() string { return "/templates/" + c.ID }

// Guide is the category's page in site/pages, a guide to it under its
// templates, or nil when it has none.
func (c *Category) Guide() *Page { return c.own }

// Indexed reports whether search engines index the category's page: it has
// a page of its own, or enough templates to be worth one.
func (c *Category) Indexed() bool { return c.own != nil || len(c.Templates) >= minIndexedCategory }

// Scene is the category's picture.
func (c *Category) Scene() string {
	if c.Art != "" || len(c.Templates) == 0 {
		return c.Art
	}
	return c.Templates[0].Art
}

// Tag is something templates add, like block logging.
type Tag struct {
	ID   string `json:"-"`
	Name string `json:"name"`
}

// Directory is every template the directory lists, and its categories.
type Directory struct {
	// Templates are the templates the release people install opens, the
	// most popular first; held ones (TemplateCard.Held) stay out.
	Templates []*TemplateCard
	// Categories are those with a listed template, by name.
	Categories []*Category
	tax        *Taxonomy
}

func checkSlug(kind, id string) error {
	if !reDirSlug.MatchString(id) || id == "page" {
		return fmt.Errorf("%s %q isn't a slug like lifesteal-smp, or is page, which the directory's pages use", kind, id)
	}
	return nil
}

// loadDirectory reads taxonomy.json and lists the templates by it: each
// template's categories and tags must be in it, and a template needs a
// category and the day it was added.
func loadDirectory(src fs.FS, file string, cards map[string]*TemplateCard, library map[string]*LibraryPage, packs map[string]*Modpack) (*Directory, error) {
	b, err := fs.ReadFile(src, file)
	if err != nil {
		return nil, err
	}
	tax := &Taxonomy{}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(tax); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	for id, c := range tax.Categories {
		if err := checkSlug("category", id); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if c.Name == "" || c.Intro == "" {
			return nil, fmt.Errorf("%s: category %s needs a name and an intro", file, id)
		}
		c.ID, c.Templates = id, nil
	}
	for id, t := range tax.Tags {
		if err := checkSlug("tag", id); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if t.Name == "" {
			return nil, fmt.Errorf("%s: tag %s needs a name", file, id)
		}
		t.ID = id
	}
	for addon, tags := range tax.AddonTags {
		for _, t := range tags {
			if tax.Tags[t] == nil {
				return nil, fmt.Errorf("%s: add-on %s brings tag %q, which isn't in tags", file, addon, t)
			}
		}
	}
	checks := map[string]*LibraryPage{}
	for _, l := range library {
		checks[l.Template] = l
	}
	packOf := map[string]*Modpack{}
	for _, m := range packs {
		if m.Template != "" {
			packOf[m.Template] = m
		}
	}
	d := &Directory{tax: tax}
	ids := make([]string, 0, len(cards))
	for id := range cards {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		c := cards[id]
		if err := checkSlug("template", id); err != nil {
			return nil, fmt.Errorf("cards.json: %w", err)
		}
		if len(c.categoryIDs) == 0 {
			return nil, fmt.Errorf("cards.json: %s has no categories; its first is the one its page is under", id)
		}
		c.Categories, c.Tags = nil, nil
		for _, cid := range c.categoryIDs {
			cat := tax.Categories[cid]
			if cat == nil {
				return nil, fmt.Errorf("cards.json: %s is in category %q, which isn't in %s", id, cid, file)
			}
			if slices.Contains(c.Categories, cat) {
				return nil, fmt.Errorf("cards.json: %s lists category %s twice", id, cid)
			}
			c.Categories = append(c.Categories, cat)
		}
		if _, err := time.Parse(time.DateOnly, c.Added); err != nil {
			return nil, fmt.Errorf("cards.json: %s needs added, the day it was listed, YYYY-MM-DD", id)
		}
		if c.Popularity < 0 {
			return nil, fmt.Errorf("cards.json: %s has a popularity below zero", id)
		}
		var tagErr error
		addTag := func(tid string) {
			t := tax.Tags[tid]
			switch {
			case t == nil && tagErr == nil:
				tagErr = fmt.Errorf("cards.json: %s has tag %q, which isn't in %s", id, tid, file)
			case t != nil && !slices.Contains(c.Tags, t):
				c.Tags = append(c.Tags, t)
			}
		}
		for _, tid := range c.tagIDs {
			addTag(tid)
		}
		for _, a := range c.Template.Addons {
			for _, tid := range tax.AddonTags[a.Slug] {
				addTag(tid)
			}
		}
		if tagErr != nil {
			return nil, tagErr
		}
		c.check, c.pack = checks[id], packOf[id]
		if c.Held() {
			continue
		}
		d.Templates = append(d.Templates, c)
		for _, cat := range c.Categories {
			cat.Templates = append(cat.Templates, c)
		}
	}
	sortCards(d.Templates)
	for _, cat := range tax.Categories {
		sortCards(cat.Templates)
		if len(cat.Templates) > 0 {
			d.Categories = append(d.Categories, cat)
		}
	}
	slices.SortFunc(d.Categories, func(a, b *Category) int { return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID)) })
	return d, nil
}

// sortCards puts the most popular first, then by name.
func sortCards(cs []*TemplateCard) {
	slices.SortStableFunc(cs, func(a, b *TemplateCard) int {
		return cmp.Or(cmp.Compare(b.Popularity, a.Popularity), strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
}

// Popular are the categories with the most popular templates, most first:
// the directory's shortcuts.
func (d *Directory) Popular(n int) []*Category {
	out := slices.Clone(d.Categories)
	sum := func(c *Category) int {
		t := 0
		for _, x := range c.Templates {
			t += x.Popularity
		}
		return t
	}
	slices.SortStableFunc(out, func(a, b *Category) int { return cmp.Compare(sum(b), sum(a)) })
	return out[:min(n, len(out))]
}

// Related are up to n other listed templates to look at next: those that
// share its first category, then its others, then the most popular.
func (d *Directory) Related(c *TemplateCard, n int) []*TemplateCard {
	var out []*TemplateCard
	add := func(x *TemplateCard) {
		if x != c && len(out) < n && !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	for _, cat := range c.Categories {
		for _, x := range cat.Templates {
			add(x)
		}
	}
	for _, x := range d.Templates {
		add(x)
	}
	return out
}

// Path is the template's page, under its first category.
func (c *TemplateCard) Path() string {
	if len(c.Categories) == 0 {
		return ""
	}
	return "/templates/" + c.Categories[0].ID + "/" + c.ID
}

// OpenPath is /t/<id>, which sends the visitor on to the share page with the
// template: for links made where the template itself isn't at hand, like
// the cards js/templates.js draws.
func (c *TemplateCard) OpenPath() string { return "/t/" + c.ID }

// Primary is the template's first category.
func (c *TemplateCard) Primary() *Category { return c.Categories[0] }

// Description is what the template says it is.
func (c *TemplateCard) Description() string { return c.Template.Description }

var kindNames = map[string]string{"plugins": "Plugins", "mods": "Mods", "modpack": "Modpack", "vanilla": "Vanilla"}

// kindOrder is the order of the Type filter.
var kindOrder = []string{"plugins", "mods", "modpack", "vanilla"}

// Kind is what the template adds to the game: a modpack, mods, plugins or
// nothing (vanilla).
func (c *TemplateCard) Kind() string {
	switch {
	case c.Template.Modpack != nil:
		return "modpack"
	case len(c.Template.Addons) == 0:
		return "vanilla"
	case minecraft.ModLoader(c.Template.Server.Type):
		return "mods"
	}
	return "plugins"
}

// KindName is Kind as the Type filter says it.
func (c *TemplateCard) KindName() string { return kindNames[c.Kind()] }

// Loader is the template's server type, with its logo.
func (c *TemplateCard) Loader() ServerType {
	for _, t := range serverTypes(rowOrder) {
		if t.ID == c.Template.Server.Type {
			return t
		}
	}
	return ServerType{ID: c.Template.Server.Type, Name: c.Template.Server.Type}
}

// Version is the Minecraft version it runs.
func (c *TemplateCard) Version() string { return c.Template.Server.MinecraftVersion }

// Memory is the memory it gives the server, like "4 GB".
func (c *TemplateCard) Memory() string { return gigabytes(c.MemoryMB) }

// Check is what happened when a server was created and started from it, or
// nil when site/data/library doesn't say.
func (c *TemplateCard) Check() *LibraryPage { return c.check }

// PackPage is its modpack's page under /modpacks, or nil.
func (c *TemplateCard) PackPage() *Modpack { return c.pack }

// ModCount is how many mods its modpack has, 0 when it has none or
// cards.json doesn't say.
func (c *TemplateCard) ModCount() int { return c.modpackMods }

// ModpackName is its modpack's name, or "".
func (c *TemplateCard) ModpackName() string { return c.modpackTitle }

// AddonNames are its add-ons' names, in its order.
func (c *TemplateCard) AddonNames() []string {
	var out []string
	for _, a := range c.Template.Addons {
		out = append(out, a.Name)
	}
	return out
}

// CardTags are the tags a card shows: up to three.
func (c *TemplateCard) CardTags() []*Tag { return c.Tags[:min(3, len(c.Tags))] }

// DirView is what a page of the directory shows, for its layout.
type DirView struct {
	// Kind is directory, category, template or open.
	Kind     string
	Category *Category
	Template *TemplateCard
	// List is what the page's cards come from: every listed template, or
	// the category's. Page is which of Pages this is, from 1.
	List        []*TemplateCard
	Page, Pages int
}

// Cards are the cards this page shows.
func (v *DirView) Cards() []*TemplateCard {
	from := (v.Page - 1) * PerPage
	return v.List[min(from, len(v.List)):min(from+PerPage, len(v.List))]
}

// Base is the listing's first page.
func (v *DirView) Base() string {
	if v.Category != nil {
		return v.Category.Path()
	}
	return "/templates"
}

// PagePath is the address of the listing's page n.
func (v *DirView) PagePath(n int) string {
	if n <= 1 {
		return v.Base()
	}
	return v.Base() + "/page/" + strconv.Itoa(n)
}

// PageLink is one item of the pagination: a page, or a gap when Number is 0.
type PageLink struct {
	Number  int
	Path    string
	Current bool
}

// PageLinks are the pagination's numbers: the first, the last, and those
// next to this one, with gaps between.
func (v *DirView) PageLinks() []PageLink {
	var out []PageLink
	for n := 1; n <= v.Pages; n++ {
		if n == 1 || n == v.Pages || (n >= v.Page-1 && n <= v.Page+1) {
			out = append(out, PageLink{Number: n, Path: v.PagePath(n), Current: n == v.Page})
		} else if len(out) > 0 && out[len(out)-1].Number != 0 {
			out = append(out, PageLink{})
		}
	}
	return out
}

func pagesFor(n int) int { return max(1, (n+PerPage-1)/PerPage) }

// Facet is one of the directory's filters, as the page draws it before
// js/templates.js counts again.
type Facet struct {
	// Key names it in the address: /templates?loader=paper.
	Key, Label string
	// Radio: one option at a time, the first being "any".
	Radio   bool
	Options []FacetOption
	// Shown is how many options show before "Show all".
	Shown int
}

// FacetOption is one choice of a filter, with how many templates have it.
type FacetOption struct {
	Value, Label string
	Count        int
	Logo         string
	Pixel        bool
}

// memorySteps are the Memory filter's choices: templates that give the
// server at most this much.
var memorySteps = []int{3072, 4096, 6144, 8192, 12288, 16384, 24576, 32768}

// facets are the filters for a list of templates, leaving out the category
// a category page is about.
func facets(list []*TemplateCard, except *Category) []Facet {
	var out []Facet
	counts := func(key func(*TemplateCard) []string) map[string]int {
		m := map[string]int{}
		for _, c := range list {
			for _, k := range key(c) {
				m[k]++
			}
		}
		return m
	}
	byCount := func(opts []FacetOption) {
		slices.SortStableFunc(opts, func(a, b FacetOption) int {
			return cmp.Or(cmp.Compare(b.Count, a.Count), strings.Compare(a.Label, b.Label))
		})
	}

	names := map[string]string{}
	modes := counts(func(c *TemplateCard) []string {
		var ids []string
		for _, cat := range c.Categories {
			if cat != except {
				ids = append(ids, cat.ID)
				names[cat.ID] = cat.Name
			}
		}
		return ids
	})
	mode := Facet{Key: "mode", Label: "Game mode", Shown: 8}
	for id, n := range modes {
		mode.Options = append(mode.Options, FacetOption{Value: id, Label: names[id], Count: n})
	}
	byCount(mode.Options)
	out = append(out, mode)

	kinds := counts(func(c *TemplateCard) []string { return []string{c.Kind()} })
	kind := Facet{Key: "type", Label: "Type", Shown: 4}
	for _, k := range kindOrder {
		if kinds[k] > 0 {
			kind.Options = append(kind.Options, FacetOption{Value: k, Label: kindNames[k], Count: kinds[k]})
		}
	}
	out = append(out, kind)

	loaders := counts(func(c *TemplateCard) []string { return []string{c.Template.Server.Type} })
	loader := Facet{Key: "loader", Label: "Loader", Shown: 7}
	for _, t := range serverTypes(rowOrder) {
		if loaders[t.ID] > 0 {
			loader.Options = append(loader.Options, FacetOption{Value: t.ID, Label: t.Name, Count: loaders[t.ID], Logo: t.Logo, Pixel: t.Pixel})
		}
	}
	out = append(out, loader)

	versions := counts(func(c *TemplateCard) []string { return []string{c.Version()} })
	version := Facet{Key: "version", Label: "Minecraft version", Shown: 6}
	for v, n := range versions {
		version.Options = append(version.Options, FacetOption{Value: v, Label: v, Count: n})
	}
	slices.SortFunc(version.Options, func(a, b FacetOption) int { return minecraft.CompareMinecraft(b.Value, a.Value) })
	out = append(out, version)

	memory := Facet{Key: "memory", Label: "Memory", Radio: true, Shown: len(memorySteps) + 1}
	memory.Options = append(memory.Options, FacetOption{Label: "Any", Count: len(list)})
	last := 0
	for _, step := range memorySteps {
		n := 0
		for _, c := range list {
			if c.MemoryMB <= step {
				n++
			}
		}
		if n > last && n < len(list) {
			memory.Options = append(memory.Options, FacetOption{Value: strconv.Itoa(step / 1024), Label: "Up to " + gigabytes(step), Count: n})
		}
		last = max(last, n)
	}
	out = append(out, memory)

	tagNames := map[string]string{}
	tags := counts(func(c *TemplateCard) []string {
		var ids []string
		for _, t := range c.Tags {
			ids = append(ids, t.ID)
			tagNames[t.ID] = t.Name
		}
		return ids
	})
	feature := Facet{Key: "tag", Label: "Features", Shown: 6}
	for id, n := range tags {
		feature.Options = append(feature.Options, FacetOption{Value: id, Label: tagNames[id], Count: n})
	}
	byCount(feature.Options)
	out = append(out, feature)

	return slices.DeleteFunc(out, func(f Facet) bool { return len(f.Options) == 0 || (f.Radio && len(f.Options) == 1) })
}

// Facets are the filters of the page's list.
func (v *DirView) Facets() []Facet { return facets(v.List, v.Category) }

// minFiltered is how many templates a list needs before its page has
// search, filters and sorting: a handful are simpler to look through.
const minFiltered = 7

// Filters reports whether the page offers search, filters and sorting.
func (v *DirView) Filters() bool { return len(v.List) >= minFiltered }

// addDirectory adds the directory's pages to the site: /templates's pages
// after the first, a page for each category and each template, unless
// site/pages has its own at that address, and /t/<id> for each template.
func (s *Site) addDirectory() error {
	d := s.dir
	own := map[string]*Page{}
	for _, p := range s.pages {
		if p.Path == "/templates" || strings.HasPrefix(p.Path, "/templates/") {
			own[p.Path] = p
		}
	}
	hub := own["/templates"]
	if hub == nil {
		return fmt.Errorf("site/pages has no page at /templates, the directory")
	}
	if hub.Layout != "directory" {
		return fmt.Errorf("/templates is the directory, with layout: directory")
	}
	delete(own, "/templates")
	pages := pagesFor(len(d.Templates))
	s.directoryPage(hub, &DirView{Kind: "directory", List: d.Templates, Page: 1, Pages: pages})
	for n := 2; n <= pages; n++ {
		p := &Page{
			Path:        fmt.Sprintf("/templates/page/%d", n),
			Title:       fmt.Sprintf("Minecraft server templates, page %d of %d", n, pages),
			Description: fmt.Sprintf("Page %d of %d of Minecraft server templates for Playkeeper, each created and started on a real server before it was listed.", n, pages),
			Label:       hub.Label, H1: hub.H1, Card: hub.Card, Kind: hub.Kind, OG: hub.OG,
			Layout: "directory", NoIndex: true,
		}
		s.directoryPage(p, &DirView{Kind: "directory", List: d.Templates, Page: n, Pages: pages})
		s.pages = append(s.pages, p)
	}
	for _, c := range d.Categories {
		p := own[c.Path()]
		delete(own, c.Path())
		if p != nil {
			if p.Layout != "category" {
				return fmt.Errorf("%s is the %s category's page, with layout: category", p.Path, c.ID)
			}
			c.own = p
		} else {
			if n := utf8.RuneCountInString(c.Description); n < 100 || n > 160 {
				return fmt.Errorf("category %s has no page in site/pages, so it needs a description of 100 to 160 characters, not %d", c.ID, n)
			}
			p = &Page{
				Path:        c.Path(),
				Title:       c.Name + " server templates for Minecraft",
				Description: c.Description,
				Label:       c.Name + " server templates", H1: c.Name + " server templates",
				Kind: "Templates", OG: "templates", Layout: "category",
			}
			p.Card = p.Label
			s.pages = append(s.pages, p)
		}
		p.NoIndex = p.NoIndex || !c.Indexed()
		pages := pagesFor(len(c.Templates))
		s.directoryPage(p, &DirView{Kind: "category", Category: c, List: c.Templates, Page: 1, Pages: pages})
		for n := 2; n <= pages; n++ {
			q := &Page{
				Path:        fmt.Sprintf("%s/page/%d", c.Path(), n),
				Title:       fmt.Sprintf("%s server templates, page %d of %d", c.Name, n, pages),
				Description: fmt.Sprintf("Page %d of %d of %s server templates for Playkeeper, each created and started on a real server before it was listed.", n, pages, c.Name),
				Label:       p.Label, H1: p.H1, Card: p.Card, Kind: p.Kind, OG: p.OG,
				Layout: "category", NoIndex: true,
			}
			s.directoryPage(q, &DirView{Kind: "category", Category: c, List: c.Templates, Page: n, Pages: pages})
			s.pages = append(s.pages, q)
		}
	}
	for _, t := range d.Templates {
		p := own[t.Path()]
		delete(own, t.Path())
		if p != nil {
			if p.Layout != "template" {
				return fmt.Errorf("%s is the %s template's page, with layout: template", p.Path, t.ID)
			}
		} else {
			p = &Page{
				Path:        t.Path(),
				Title:       t.Name + ": a Minecraft server template",
				Description: metaDescription(t.Description(), "Open it in your own Playkeeper dashboard in one click; nothing installs until you confirm."),
				Label:       t.Name + " server template", H1: t.Name,
				Kind: "Template", OG: "templates", Layout: "template", NoIndex: true,
			}
			p.Card = p.Label
			s.pages = append(s.pages, p)
		}
		s.directoryPage(p, &DirView{Kind: "template", Category: t.Primary(), Template: t, List: t.Primary().Templates, Page: 1, Pages: 1})
		open := &Page{
			Path:        t.OpenPath(),
			Title:       "Open " + t.Name + " in your dashboard",
			Description: "Opens the " + t.Name + " server template on Playkeeper's share page, which sends it to your own dashboard. Nothing installs until you confirm there.",
			Label:       t.Name, H1: "Opening " + t.Name,
			OG: "t", Layout: "open", Closing: "none", NoIndex: true, Refresh: t.Link,
		}
		open.Card = open.Label
		open.dir = &DirView{Kind: "open", Category: t.Primary(), Template: t}
		s.pages = append(s.pages, open)
	}
	for p := range own {
		return fmt.Errorf("site/pages has a page at %s, which is no category or listed template of the directory", p)
	}
	return nil
}

// directoryPage makes p one of the directory's pages: its section, styles
// and scripts.
func (s *Site) directoryPage(p *Page, v *DirView) {
	p.dir = v
	p.Section = "templates"
	for _, css := range []string{"css/templates.css"} {
		if !slices.Contains(p.Styles, css) {
			p.Styles = append(p.Styles, css)
		}
	}
	if !slices.Contains(p.Scripts, "js/templates.js") {
		p.Scripts = append(p.Scripts, "js/templates.js")
	}
	p.NoScript = "css/templates-nojs.css"
}

// shortCount writes a count the way add-on sites round them: "18M", "2.8M",
// "417k".
func shortCount(n int) string {
	switch f := float64(n); {
	case f >= 10e6:
		return strconv.FormatFloat(f/1e6, 'f', 0, 64) + "M"
	case f >= 1e6:
		return strings.TrimSuffix(strconv.FormatFloat(f/1e6, 'f', 1, 64), ".0") + "M"
	case f >= 1e3:
		return strconv.FormatFloat(f/1e3, 'f', 0, 64) + "k"
	}
	return itoa(n)
}

// addonPage is an add-on's page at its source, or "" for a source without
// pages by slug.
func addonPage(a templates.Addon) string {
	if a.Source == addons.Modrinth {
		return "https://modrinth.com/project/" + firstOf(a.Slug, a.Project)
	}
	return ""
}

// metaDescription fits text into a meta description of 100 to 160
// characters: more is added when it's short, and it's cut at a word when
// it's long.
func metaDescription(text, more string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) < 100 {
		text = strings.TrimSpace(text + " " + more)
	}
	if utf8.RuneCountInString(text) <= 160 {
		return text
	}
	r := []rune(text)[:159]
	cut := strings.LastIndex(string(r), " ")
	if cut < 100 {
		cut = len(string(r))
	}
	return strings.TrimRight(string(r)[:cut], " ,.;:") + "…"
}

// indexTemplate is one template in js/templates-index.js, which the
// directory's script searches, filters and draws cards from.
type indexTemplate struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Desc       string   `json:"desc"`
	Page       string   `json:"page"`
	Open       string   `json:"open"`
	Art        int      `json:"art"`
	Categories []string `json:"cats"`
	Tags       []string `json:"tags"`
	Loader     string   `json:"loader"`
	Version    string   `json:"version"`
	MemoryMB   int      `json:"mb"`
	Kind       string   `json:"kind"`
	Popularity int      `json:"pop"`
	Added      string   `json:"added"`
	Addons     []string `json:"addons"`
}

type indexImage struct {
	Src string `json:"src"`
	W   int    `json:"w"`
	H   int    `json:"h"`
}

type indexLoader struct {
	Name  string `json:"name"`
	Logo  string `json:"logo"`
	Pixel bool   `json:"pixel,omitempty"`
}

// directoryIndex is js/templates-index.js: every listed template, the most
// popular first, with the names and pictures its cards show.
func (s *Site) directoryIndex() ([]byte, error) {
	idx := struct {
		Arts       []indexImage           `json:"arts"`
		Loaders    map[string]indexLoader `json:"loaders"`
		Categories map[string]string      `json:"cats"`
		Tags       map[string]string      `json:"tags"`
		Kinds      map[string]string      `json:"kinds"`
		Templates  []indexTemplate        `json:"templates"`
	}{Loaders: map[string]indexLoader{}, Categories: map[string]string{}, Tags: map[string]string{}, Kinds: kindNames}
	arts := map[string]int{}
	for _, t := range s.dir.Templates {
		a, ok := s.assets[t.Art]
		if !ok {
			return nil, fmt.Errorf("cards.json: %s's art %q isn't an asset", t.ID, t.Art)
		}
		i, ok := arts[t.Art]
		if !ok {
			i = len(idx.Arts)
			arts[t.Art] = i
			idx.Arts = append(idx.Arts, indexImage{Src: a.URL, W: a.Width, H: a.Height})
		}
		l := t.Loader()
		if _, ok := idx.Loaders[l.ID]; !ok {
			logo := ""
			if x, ok := s.assets[l.Logo]; ok {
				logo = x.URL
			}
			idx.Loaders[l.ID] = indexLoader{Name: l.Name, Logo: logo, Pixel: l.Pixel}
		}
		e := indexTemplate{ID: t.ID, Name: t.Name, Desc: t.Description(), Page: t.Path(), Open: t.OpenPath(), Art: i,
			Loader: l.ID, Version: t.Version(), MemoryMB: t.MemoryMB, Kind: t.Kind(), Popularity: t.Popularity, Added: t.Added,
			Categories: []string{}, Tags: []string{}, Addons: t.AddonNames()}
		if e.Addons == nil {
			e.Addons = []string{}
		}
		if t.ModpackName() != "" {
			e.Addons = append(e.Addons, t.ModpackName())
		}
		for _, c := range t.Categories {
			e.Categories = append(e.Categories, c.ID)
			idx.Categories[c.ID] = c.Name
		}
		for _, g := range t.Tags {
			e.Tags = append(e.Tags, g.ID)
			idx.Tags[g.ID] = g.Name
		}
		idx.Templates = append(idx.Templates, e)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(idx); err != nil {
		return nil, err
	}
	return append(append([]byte("window.playkeeperTemplates = "), bytes.TrimSpace(b.Bytes())...), ";\n"...), nil
}
