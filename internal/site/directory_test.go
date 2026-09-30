package site

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/templates"
	"github.com/CIYAhq/playkeeper/internal/templates/checks"
)

// dataOverlay is the repository with the directory's data replaced by
// over's, folders and all, so a folder lists over's files alone.
type dataOverlay struct {
	base fs.FS
	over fstest.MapFS
}

func (o dataOverlay) Open(name string) (fs.File, error) {
	if name == taxonomyFile {
		return o.over.Open(name)
	}
	for _, dir := range dataDirs {
		if name == dir || strings.HasPrefix(name, dir+"/") {
			return o.over.Open(name)
		}
	}
	return o.base.Open(name)
}

// dataDirs are the folders the directory is made from, and taxonomyFile the
// file beside them, which dataOverlay replaces.
var dataDirs = []string{"site/data/templates", "site/data/library", "site/data/checks"}

const taxonomyFile = "site/data/taxonomy.json"

// cardOf is the card on a page whose name links path, or "".
func cardOf(page, path string) string {
	for _, c := range strings.Split(page, `<article class="dcard">`)[1:] {
		c, _, _ = strings.Cut(c, "</article>")
		if strings.Contains(c, `class="dcard-link" href="`+path+`"`) {
			return c
		}
	}
	return ""
}

// ownPages are the site's own pages under /templates, like the AI build
// battle's, which are none of the directory's, by address.
func ownPages(t *testing.T) map[string]bool {
	t.Helper()
	ps, err := loadPages(os.DirFS("../.."), "site/pages")
	if err != nil {
		t.Fatal(err)
	}
	own := map[string]bool{}
	for _, p := range ps {
		if ownUnderTemplates(p) {
			own[p.Path] = true
		}
	}
	return own
}

// indexOf reads js/templates-index.js back.
func indexOf(t *testing.T, o *Output) (idx struct {
	Arts      []indexImage           `json:"arts"`
	Icons     []string               `json:"icons"`
	Loaders   map[string]indexLoader `json:"loaders"`
	TagSearch map[string]string      `json:"tagSearch"`
	Templates []indexTemplate        `json:"templates"`
}) {
	t.Helper()
	for name, b := range o.Files {
		if strings.HasPrefix(name, "assets/js/templates-index.") {
			js, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "window.playkeeperTemplates = ")
			if !ok || !strings.HasSuffix(js, ";") {
				t.Fatalf("%s isn't window.playkeeperTemplates = {…};", name)
			}
			if err := json.Unmarshal([]byte(strings.TrimSuffix(js, ";")), &idx); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return idx
		}
	}
	t.Fatal("no js/templates-index.js")
	return idx
}

// The directory lists every template the release people install can open
// and a server has been created and started from, as it says of each, and
// nothing else: each has a card on /templates that opens it, a page with
// its facts under its first category, a /t/<id> that sends the browser on to
// the share page with it, and an entry in the index the directory's script
// filters. Each category's page lists its templates. The site's own pages
// under /templates, like the AI build battle's, are none of these.
func TestTheDirectoryListsWhatTheReleaseOpensAndWasChecked(t *testing.T) {
	o := build(t, Default)
	built := pages(o)
	own := ownPages(t)
	sitemap := string(o.Files["sitemap.xml"])
	root := os.DirFS("../..")
	cards, err := loadTemplateCards(root, "site/data/templates")
	if err != nil {
		t.Fatal(err)
	}
	checked, err := checks.Read(root, "site/data/checks")
	if err != nil {
		t.Fatal(err)
	}
	failing(cards, checked)
	crossplays(cards, checked)
	packs, err := loadModpacks(root, "site/data/modpacks", cards)
	if err != nil {
		t.Fatal(err)
	}
	d, err := loadDirectory(root, "site/data/taxonomy.json", cards, checked, packs)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Templates) < 2 {
		t.Fatalf("the directory lists %d templates", len(d.Templates))
	}
	hub := built["/templates"]
	// The search box works only where js/templates.js does: with search,
	// filters and sorting, from minFiltered templates.
	if strings.Contains(hub, `data-dir-q`) != (len(d.Templates) >= minFiltered) || strings.Contains(hub, `data-directory`) != (len(d.Templates) >= minFiltered) {
		t.Errorf("/templates has a search box %v and the directory's script %v, with %d templates", strings.Contains(hub, `data-dir-q`), strings.Contains(hub, `data-directory`), len(d.Templates))
	}
	idx := indexOf(t, o)
	if len(idx.Templates) != len(d.Templates) {
		t.Errorf("the index has %d templates, and the directory lists %d", len(idx.Templates), len(d.Templates))
	}
	for i, c := range d.Templates {
		if i < PerPage && (!strings.Contains(hub, `href="`+c.Path()+`"`) || !strings.Contains(hub, `href="`+c.Link+`"`)) {
			t.Errorf("/templates has no card for %s linking %s and opening it", c.ID, c.Path())
		}
		if i < PerPage && strings.Contains(cardOf(hub, c.Path()), `class="dcard-badge"`) != c.Crossplay {
			t.Errorf("/templates marks %s Crossplay %v, and its check says %v", c.ID, !c.Crossplay, c.Crossplay)
		}
		page, ok := built[c.Path()]
		if !ok {
			t.Errorf("%s has no page at %s", c.ID, c.Path())
			continue
		}
		for _, want := range []string{
			`href="` + c.Link + `" data-template-open="` + c.ID + `"`,
			`<a href="` + c.Primary().Path() + `">`,
			`<meta name="robots" content="noindex">`,
			c.Loader().Name + " " + c.Version(),
		} {
			if !strings.Contains(page, want) {
				t.Errorf("%s doesn't say %s", c.Path(), want)
			}
		}
		if k := c.Check(); k != nil && !strings.Contains(page, "Created and started on Playkeeper "+k.Release+" on "+Day(k.Checked)) {
			t.Errorf("%s doesn't say when and on which release it was checked", c.Path())
		}
		if strings.Contains(sitemap, "<loc>"+Default.BaseURL+c.Path()+"</loc>") {
			t.Errorf("the sitemap lists %s, a template's page without notes of its own", c.Path())
		}
		open := built[c.OpenPath()]
		if !strings.Contains(open, `<meta http-equiv="refresh" content="0; url=`+c.Link+`">`) || !strings.Contains(open, `<meta name="robots" content="noindex">`) {
			t.Errorf("%s doesn't send the browser on to %s, or search engines index it", c.OpenPath(), c.Link)
		}
		if !strings.Contains(open, `href="/assets/css/templates.`) {
			t.Errorf("%s doesn't load templates.css, which lays out what it shows while it sends the browser on", c.OpenPath())
		}
		if i < len(idx.Templates) {
			e := idx.Templates[i]
			if e.ID != c.ID || e.Page != c.Path() || e.Open != c.OpenPath() || e.MemoryMB != c.MemoryMB || e.Loader != c.Template.Server.Type || e.Art >= len(idx.Arts) {
				t.Errorf("the index's template %d is %+v, want %s, the most popular first", i, e, c.ID)
			}
		}
	}
	unlisted := 0
	for _, c := range cards {
		why := ""
		switch {
		case c.Failing:
			why = "failed its last check"
		case c.Held():
			why = "opens only from Playkeeper " + c.OpensFrom
		case c.Check() == nil:
			why = "has no passing check in site/data/checks"
		default:
			continue
		}
		unlisted++
		for p := range built {
			if (strings.HasPrefix(p, "/templates/") || strings.HasPrefix(p, "/t/")) && strings.HasSuffix(p, "/"+c.ID) && !own[p] {
				t.Errorf("%s is a page for %s, which %s", p, c.ID, why)
			}
		}
		if strings.Contains(hub, `href="`+c.Link+`"`) {
			t.Errorf("/templates opens %s, which %s", c.ID, why)
		}
		if slices.ContainsFunc(idx.Templates, func(e indexTemplate) bool { return e.ID == c.ID }) {
			t.Errorf("the index has %s, which %s", c.ID, why)
		}
	}
	if unlisted == 0 {
		t.Log("every template in site/data/templates is listed; TestTheDirectoryScalesToHundredsOfTemplates checks that unchecked ones stay out")
	}
	for _, cat := range d.Categories {
		page, ok := built[cat.Path()]
		if !ok {
			t.Errorf("category %s has no page at %s", cat.ID, cat.Path())
			continue
		}
		for _, c := range cat.Templates {
			if !strings.Contains(page, `href="`+c.Path()+`"`) {
				t.Errorf("%s doesn't list %s", cat.Path(), c.ID)
			}
		}
		_, err := os.Stat("../../site/pages" + cat.Path() + ".html")
		guide := err == nil
		indexed := !strings.Contains(page, `<meta name="robots" content="noindex">`)
		if want := guide || len(cat.Templates) >= minIndexedCategory; indexed != want {
			t.Errorf("%s: indexed %v, want %v with %d templates and a guide %v", cat.Path(), indexed, want, len(cat.Templates), guide)
		}
		if guide && !strings.Contains(page, `<article class="article">`) {
			t.Errorf("%s doesn't show its guide", cat.Path())
		}
		if indexed && !strings.Contains(hub, `href="`+cat.Path()+`"`) {
			t.Errorf("/templates doesn't link %s", cat.Path())
		}
	}
	for p := range built {
		rest, ok := strings.CutPrefix(p, "/templates/")
		if !ok {
			continue
		}
		parts := strings.Split(rest, "/")
		switch {
		case len(parts) == 2 && parts[0] == "page", len(parts) == 3 && parts[1] == "page":
		case len(parts) == 1 && slices.ContainsFunc(d.Categories, func(c *Category) bool { return c.ID == parts[0] }):
		case len(parts) == 2 && slices.ContainsFunc(d.Templates, func(c *TemplateCard) bool { return c.Path() == p }):
		case own[p]:
		default:
			t.Errorf("%s is no page of the directory: its pages, a category or a template", p)
		}
	}
}

// A page of the site's own can go under /templates, like the AI build
// battle's, as none of the directory's pages: one word under it, and neither
// page, which the directory's pages use, nor a category's address, listed or
// not. A page there with a directory's layout is still one of its pages.
func TestOwnPagesUnderTemplates(t *testing.T) {
	if !ownPages(t)["/templates/ai-build-battle"] {
		t.Fatal("the AI build battle's page isn't a page of the site's own under /templates")
	}
	built := pages(build(t, Default))
	b, err := os.ReadFile("../../" + taxonomyFile)
	if err != nil {
		t.Fatal(err)
	}
	var tax map[string]any
	if err := json.Unmarshal(b, &tax); err != nil {
		t.Fatal(err)
	}
	categories := tax["categories"].(map[string]any)
	// A listed category without a guide of its own, whose page the directory
	// makes; and battles, a category with no template at all.
	listed := ""
	for _, id := range slices.Sorted(maps.Keys(categories)) {
		if _, err := os.Stat("../../site/pages/templates/" + id + ".html"); err != nil && built["/templates/"+id] != "" {
			listed = id
			break
		}
	}
	if listed == "" {
		t.Fatal("every listed category has a guide; this test needs one without")
	}
	categories["battles"] = categories["smp"]
	withBattles, _ := json.Marshal(tax)
	for name, c := range map[string]struct {
		edits map[string]func(string) string
		says  string
	}{
		"page":                        {map[string]func(string) string{battlePage: set("path", "/templates/page")}, "one word under it"},
		"two words under /templates":  {map[string]func(string) string{battlePage: set("path", "/templates/smp/battle")}, "one word under it"},
		"a listed category's address": {map[string]func(string) string{battlePage: set("path", "/templates/"+listed)}, "category's page, with layout: category"},
		"an unlisted category's address": {map[string]func(string) string{
			battlePage:   set("path", "/templates/battles"),
			taxonomyFile: func(string) string { return string(withBattles) },
		}, "lists no template yet"},
		"a directory's layout": {map[string]func(string) string{battlePage: set("layout", "category")}, "no category with a listed template"},
	} {
		if _, err := buildEdited(t, c.edits); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("a page of the site's own under /templates at %s: %v, want an error that says %q", name, err, c.says)
		}
	}
	if _, err := buildEdited(t, map[string]func(string) string{taxonomyFile: func(string) string { return string(withBattles) }}); err != nil {
		t.Errorf("with battles, a category with no template, the site doesn't build: %v", err)
	}
}

// What cards.json and taxonomy.json say that the directory can't list stops
// the build.
func TestTaxonomyMistakesStopTheBuild(t *testing.T) {
	root := os.DirFS("../..")
	taxonomy, err := os.ReadFile("../../site/data/taxonomy.json")
	if err != nil {
		t.Fatal(err)
	}
	load := func(edit func(tax map[string]any, cards map[string]*TemplateCard)) error {
		cards, err := loadTemplateCards(root, "site/data/templates")
		if err != nil {
			t.Fatal(err)
		}
		var tax map[string]any
		if err := json.Unmarshal(taxonomy, &tax); err != nil {
			t.Fatal(err)
		}
		edit(tax, cards)
		b, _ := json.Marshal(tax)
		_, err = loadDirectory(fstest.MapFS{"taxonomy.json": {Data: b}}, "taxonomy.json", cards, nil, nil)
		return err
	}
	if err := load(func(map[string]any, map[string]*TemplateCard) {}); err != nil {
		t.Fatalf("the repository's taxonomy: %v", err)
	}
	categories := func(tax map[string]any) map[string]any { return tax["categories"].(map[string]any) }
	for name, edit := range map[string]func(map[string]any, map[string]*TemplateCard){
		"a template in no category":   func(_ map[string]any, c map[string]*TemplateCard) { c["towny"].categoryIDs = nil },
		"a category that isn't there": func(_ map[string]any, c map[string]*TemplateCard) { c["towny"].categoryIDs = []string{"nations"} },
		"a category twice": func(_ map[string]any, c map[string]*TemplateCard) {
			c["towny"].categoryIDs = []string{"towny", "towny"}
		},
		"a tag that isn't there":  func(_ map[string]any, c map[string]*TemplateCard) { c["towny"].tagIDs = []string{"economy"} },
		"no day it was added":     func(_ map[string]any, c map[string]*TemplateCard) { c["towny"].Added = "" },
		"a day that isn't one":    func(_ map[string]any, c map[string]*TemplateCard) { c["towny"].Added = "28 Sep 2026" },
		"a popularity below zero": func(_ map[string]any, c map[string]*TemplateCard) { c["towny"].Popularity = -1 },
		"a category called page": func(tax map[string]any, _ map[string]*TemplateCard) {
			categories(tax)["page"] = categories(tax)["towny"]
		},
		"a category that isn't a slug": func(tax map[string]any, _ map[string]*TemplateCard) {
			categories(tax)["Towny Plus"] = categories(tax)["towny"]
		},
		"a category without an intro": func(tax map[string]any, _ map[string]*TemplateCard) {
			categories(tax)["towny"] = map[string]any{"name": "Towny"}
		},
		"an add-on's tag that isn't there": func(tax map[string]any, _ map[string]*TemplateCard) {
			tax["addonTags"].(map[string]any)["towny"] = []string{"nations"}
		},
		"crossplay from cards.json, not a check": func(_ map[string]any, c map[string]*TemplateCard) {
			c["towny"].tagIDs = []string{"crossplay"}
		},
		"crossplay from an add-on, not a check": func(tax map[string]any, _ map[string]*TemplateCard) {
			tax["addonTags"].(map[string]any)["towny"] = []string{"crossplay"}
		},
		"a setting the directory doesn't know": func(tax map[string]any, _ map[string]*TemplateCard) {
			tax["sorts"] = []string{"popular"}
		},
	} {
		if err := load(edit); err == nil {
			t.Errorf("%s passes", name)
		}
	}

	// A category with a listed template needs its own page or a description
	// for search engines: Creative, whose template is checked, has no page.
	data := dataFS(t)
	var tax map[string]any
	if err := json.Unmarshal(taxonomy, &tax); err != nil {
		t.Fatal(err)
	}
	delete(categories(tax)["creative"].(map[string]any), "description")
	noDesc := maps.Clone(data)
	b, _ := json.Marshal(tax)
	noDesc["site/data/taxonomy.json"] = &fstest.MapFile{Data: b}
	if _, err := Build(Options{Root: dataOverlay{root, noDesc}, Settings: Default, Now: time.Now()}); err == nil || !strings.Contains(err.Error(), "category creative has no page") {
		t.Errorf("a category with neither a page nor a description builds: %v", err)
	}
}

// Crossplay comes from templates' checks alone. A template whose last check
// passed with crossplay on has the Crossplay feature, a mark on its card
// instead of a tag, and a line on its page, and search finds it by what
// people call it. Until one has, nothing mentions it.
func TestCrossplayComesFromChecks(t *testing.T) {
	withCrossplay := func(on func(id string) bool) (map[string]string, []indexTemplate, map[string]string) {
		t.Helper()
		data := dataFS(t)
		for name, f := range data {
			id, ok := strings.CutPrefix(name, "site/data/checks/")
			if !ok {
				continue
			}
			var k map[string]any
			dec := json.NewDecoder(strings.NewReader(string(f.Data)))
			dec.UseNumber()
			if err := dec.Decode(&k); err != nil {
				t.Fatal(err)
			}
			delete(k, "crossplay")
			if on(strings.TrimSuffix(id, ".json")) {
				k["crossplay"] = true
			}
			b, err := json.Marshal(k)
			if err != nil {
				t.Fatal(err)
			}
			data[name] = &fstest.MapFile{Data: b}
		}
		o, err := Build(Options{Root: dataOverlay{os.DirFS("../.."), data}, Settings: Default, Now: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		idx := indexOf(t, o)
		return pages(o), idx.Templates, idx.TagSearch
	}
	const faq = "Templates marked Crossplay"

	built, idx, _ := withCrossplay(func(string) bool { return false })
	if len(idx) < minFiltered {
		t.Fatalf("the directory lists %d templates, too few for its filters", len(idx))
	}
	hub := built["/templates"]
	for _, s := range []string{`class="dcard-badge">`, `value="crossplay"`, faq} {
		if strings.Contains(hub, s) {
			t.Errorf("/templates has %s, with no template checked with crossplay", s)
		}
	}
	for _, e := range idx {
		if slices.Contains(e.Tags, crossplayTag) || strings.Contains(built[e.Page], "crossplay on") {
			t.Errorf("%s has crossplay, and its check doesn't", e.ID)
		}
	}

	without := idx[0].ID
	built, idx, search := withCrossplay(func(id string) bool { return id != without })
	hub = built["/templates"]
	if !strings.Contains(hub, `name="tag" value="crossplay"`) || !strings.Contains(hub, faq) {
		t.Error("/templates offers no Crossplay feature, or doesn't say what the mark means")
	}
	if !strings.Contains(search[crossplayTag], "Bedrock") || !strings.Contains(search[crossplayTag], "GeyserMC") {
		t.Errorf("searching Bedrock or GeyserMC doesn't find crossplay's templates: %q", search[crossplayTag])
	}
	for i, e := range idx {
		want := e.ID != without
		if slices.Contains(e.Tags, crossplayTag) != want {
			t.Errorf("the index gives %s crossplay %v, want %v", e.ID, !want, want)
		}
		page := built[e.Page]
		if strings.Contains(page, "<dt>Bedrock</dt><dd>Yes, with crossplay on</dd>") != want || strings.Contains(page, "Crossplay turned on too") != want {
			t.Errorf("%s says it has crossplay %v, want %v", e.Page, !want, want)
		}
		if i >= PerPage {
			continue
		}
		card := cardOf(hub, e.Page)
		if strings.Contains(card, `<p class="dcard-badge">Crossplay</p>`) != want {
			t.Errorf("/templates marks %s Crossplay %v, want %v", e.ID, !want, want)
		}
		if strings.Contains(card, "<li>Crossplay</li>") {
			t.Errorf("/templates lists Crossplay among %s's tags, as well as marking it", e.ID)
		}
	}
}

// dataFS is the folders the directory is made from, as they are.
func dataFS(t testing.TB) fstest.MapFS {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../..", taxonomyFile))
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{taxonomyFile: {Data: b}}
	for _, dir := range dataDirs {
		entries, err := os.ReadDir(filepath.Join("../..", dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join("../..", dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out[path.Join(dir, e.Name())] = &fstest.MapFile{Data: b}
		}
	}
	return out
}

// Each filter counts the templates it would show, and offers only what's
// there: the memory steps that split the list, the versions newest first,
// the loaders in the logo row's order.
func TestFiltersCountWhatTheyShow(t *testing.T) {
	cat := func(id string) *Category { return &Category{ID: id, Name: strings.ToUpper(id[:1]) + id[1:]} }
	smp, towny := cat("smp"), cat("towny")
	pvp, voice := &Tag{ID: "pvp", Name: "PvP"}, &Tag{ID: "voice", Name: "Voice"}
	card := func(typ, version string, mb int, cats []*Category, tags []*Tag) *TemplateCard {
		c := &TemplateCard{MemoryMB: mb, Categories: cats, Tags: tags, Template: &templates.Template{}}
		c.Template.Server.Type, c.Template.Server.MinecraftVersion = typ, version
		if typ != "vanilla" {
			c.Template.Addons = []templates.Addon{{Name: "Chunky", Slug: "chunky"}}
		}
		return c
	}
	list := []*TemplateCard{
		card("paper", "26.2", 4096, []*Category{smp}, []*Tag{pvp}),
		card("paper", "1.21.1", 3072, []*Category{smp, towny}, []*Tag{pvp, voice}),
		card("fabric", "26.2", 8192, []*Category{towny}, nil),
		card("vanilla", "1.20.1", 2048, []*Category{smp}, nil),
	}
	got := map[string]Facet{}
	for _, f := range facets(list, nil) {
		got[f.Key] = f
	}
	opts := func(key string) string {
		var out []string
		for _, o := range got[key].Options {
			out = append(out, o.Label+"="+strconv.Itoa(o.Count))
		}
		return strings.Join(out, " ")
	}
	for key, want := range map[string]string{
		"mode":    "Smp=3 Towny=2",
		"type":    "Plugins=2 Mods=1 Vanilla=1",
		"loader":  "Paper=2 Fabric=1 Vanilla=1",
		"version": "26.2=2 1.21.1=1 1.20.1=1",
		"memory":  "Any=4 Up to 3 GB=2 Up to 4 GB=3",
		"tag":     "PvP=2 Voice=1",
	} {
		if opts(key) != want {
			t.Errorf("%s: %s, want %s", key, opts(key), want)
		}
	}
	var inSMP []*TemplateCard
	for _, c := range list {
		if slices.Contains(c.Categories, smp) {
			inSMP = append(inSMP, c)
		}
	}
	for _, f := range facets(inSMP, smp) {
		if f.Key == "mode" && (len(f.Options) != 1 || f.Options[0].Value != "towny" || f.Options[0].Count != 1) {
			t.Errorf("the smp category's page offers game modes %+v, want Towny, in one of its templates", f.Options)
		}
	}
}

// A meta description is 100 to 160 characters: a short one gets more, and a
// long one is cut at a word.
func TestMetaDescriptionsFit(t *testing.T) {
	more := "Open it in your own Playkeeper dashboard in one click; nothing installs until you confirm."
	for _, text := range []string{
		"Paper for Towny.",
		"Paper for a lifesteal SMP: kill a player to take a heart, die and lose one. With LifeStealZ, pre-generation and block logging.",
		strings.Repeat("A very long description of a template that goes on ", 6),
	} {
		got := metaDescription(text, more)
		if n := utf8.RuneCountInString(got); n < 100 || n > 160 {
			t.Errorf("%q: %d characters, want 100 to 160", got, n)
		}
		if utf8.RuneCountInString(text) > 160 && !strings.HasSuffix(got, "…") {
			t.Errorf("%q was cut without an ellipsis", got)
		}
	}
}

// synthetic is the directory's data folders with n more templates, made
// from the real templates' add-ons and their checks, across the real
// categories and a dozen more: the directory at a size it hasn't reached
// yet. Every tenth has no check, like a template the library hasn't started
// yet, and every 25th failed its check; the directory leaves both out.
func synthetic(t testing.TB, n int) fstest.MapFS {
	t.Helper()
	out := dataFS(t)
	type addon struct {
		Source  string          `json:"source"`
		Project string          `json:"project"`
		Slug    string          `json:"slug"`
		Name    string          `json:"name"`
		Pin     json.RawMessage `json:"pin,omitempty"`
		Latest  bool            `json:"latest,omitempty"`
	}
	var pool []addon
	facts := map[string]checks.Addon{}
	for name, f := range out {
		switch {
		case strings.HasPrefix(name, "site/data/templates/") && !strings.HasSuffix(name, "cards.json"):
			var tpl struct{ Addons []addon }
			if err := json.Unmarshal(f.Data, &tpl); err != nil {
				t.Fatal(err)
			}
			for _, a := range tpl.Addons {
				// Playkeeper's own plugins run only on the types their
				// registry lists, which a made-up template's may not be.
				if a.Source == "playkeeper" {
					continue
				}
				if !slices.ContainsFunc(pool, func(b addon) bool { return b.Slug == a.Slug }) {
					pool = append(pool, a)
				}
			}
		case strings.HasPrefix(name, "site/data/checks/"):
			var k checks.Check
			if err := json.Unmarshal(f.Data, &k); err != nil {
				t.Fatal(err)
			}
			for _, a := range k.Addons {
				facts[a.Slug] = a
			}
		}
	}
	slices.SortFunc(pool, func(a, b addon) int { return strings.Compare(a.Slug, b.Slug) })
	var tax struct {
		Categories map[string]map[string]string `json:"categories"`
		Tags       map[string]map[string]string `json:"tags"`
		AddonTags  map[string][]string          `json:"addonTags"`
	}
	if err := json.Unmarshal(out["site/data/taxonomy.json"].Data, &tax); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"Factions", "Prison", "KitPvP", "Survival Games", "Parkour", "Earth", "RPG", "Economy", "Anarchy", "Minigames", "Build Battle", "Manhunt"} {
		id := strings.ReplaceAll(strings.ToLower(m), " ", "-")
		tax.Categories[id] = map[string]string{
			"name":        m,
			"intro":       m + " servers, made up for this test: players meet, build and compete in their own way on each of them.",
			"description": m + " server templates for Playkeeper, made up for this test, each opened in your own dashboard in one click, and nothing installs until you confirm.",
		}
	}
	var cats []string
	for id := range tax.Categories {
		cats = append(cats, id)
	}
	slices.Sort(cats)
	var cards map[string]map[string]any
	if err := json.Unmarshal(out["site/data/templates/cards.json"].Data, &cards); err != nil {
		t.Fatal(err)
	}
	arts := []string{"world-normal", "world-amplified", "world-big-biomes", "world-flat", "play-with-friends", "play-creative", "play-hardcore", "play-just-me"}
	variants := []string{"Classic", "Plus", "Hard", "Voice", "Lite", "Extreme", "Friends", "Claims", "Economy", "Hardcore", "Vanilla+", "Weekend", "Season 2", "Duos", "Big World"}
	types := []struct{ id, version string }{{"paper", "26.2"}, {"purpur", "26.2"}, {"paper", "26.1.2"}, {"fabric", "26.2"}, {"paper", "1.21.10"}, {"neoforge", "1.21.1"}, {"vanilla", "26.2"}, {"quilt", "1.21.1"}}
	memory := []int{2048, 3072, 4096, 4096, 6144, 8192, 12288}
	for i := range n {
		cat := cats[i%len(cats)]
		name := tax.Categories[cat]["name"] + " " + variants[(i/len(cats))%len(variants)]
		if (i/len(cats))/len(variants) > 0 {
			name += " " + strconv.Itoa((i/len(cats))/len(variants)+1)
		}
		id := "made-up-" + strconv.Itoa(i+1)
		typ := types[(i*7)%len(types)]
		var addons []addon
		if typ.id != "vanilla" {
			for k := range 1 + i%3 {
				a := pool[(i*3+k*5)%len(pool)]
				if !slices.ContainsFunc(addons, func(b addon) bool { return b.Slug == a.Slug }) {
					addons = append(addons, a)
				}
			}
		}
		var names []string
		for _, a := range addons {
			names = append(names, a.Name)
		}
		desc := fmt.Sprintf("%s for a %s server your friends can join any time.", strings.ToUpper(typ.id[:1])+typ.id[1:], strings.ToLower(tax.Categories[cat]["name"]))
		if len(names) > 0 {
			desc += " With " + andList(names) + "."
		}
		tpl := map[string]any{
			"playkeeperTemplate": 1, "name": name, "description": desc, "game": "minecraft-java",
			"server":   map[string]any{"type": typ.id, "minecraftVersion": typ.version},
			"settings": map[string]any{"difficulty": "normal", "pvp": true, "gameMode": "survival", "maxPlayers": 20, "playStyle": "friends", "memoryMB": memory[i%len(memory)]},
		}
		if len(addons) > 0 {
			tpl["addons"] = addons
		}
		b, err := json.Marshal(tpl)
		if err != nil {
			t.Fatal(err)
		}
		out["site/data/templates/"+id+".json"] = &fstest.MapFile{Data: b}
		second := cats[(i*5+3)%len(cats)]
		entry := map[string]any{"art": "app/pixel-art/" + arts[i%len(arts)] + ".svg", "categories": []string{cat}, "added": time.Date(2026, 9, 1+i%28, 0, 0, 0, 0, time.UTC).Format(time.DateOnly), "popularity": (i * 7919) % 5000}
		if second != cat {
			entry["categories"] = []string{cat, second}
		}
		if i%3 == 0 {
			entry["tags"] = []string{"pvp"}
		}
		cards[id] = entry
		if i%10 == 9 || !hasType(typ.id) {
			continue
		}
		check := checks.Check{Status: checks.Passing, Checked: "2026-09-28", Release: "0.4.4", Build: "129", DoneSeconds: 10 + float64(i%9)}
		check.Crossplay = (typ.id == "paper" || typ.id == "purpur") && i%2 == 0
		if i%25 == 24 {
			check = checks.Check{Status: checks.Failing, Failure: "a plugin failed to enable", Checked: "2026-09-28", Release: "0.4.2"}
		}
		for _, a := range addons {
			if check.Status == checks.Passing {
				check.Addons = append(check.Addons, facts[a.Slug])
			}
		}
		if b, err = json.Marshal(check); err != nil {
			t.Fatal(err)
		}
		out["site/data/checks/"+id+".json"] = &fstest.MapFile{Data: b}
	}
	for name, v := range map[string]any{"site/data/templates/cards.json": cards, "site/data/taxonomy.json": tax} {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		out[name] = &fstest.MapFile{Data: b}
	}
	return out
}

// The directory stays quick with hundreds of templates: pages of 24 cards,
// a page for each, an index a few hundred kilobytes long, and every template
// reachable from /templates by following links, without scripts. The made-up
// templates without a passing check stay out.
func TestTheDirectoryScalesToHundredsOfTemplates(t *testing.T) {
	const n = 600
	data := synthetic(t, n)
	passing := func(id string) bool {
		f := data["site/data/checks/"+id+".json"]
		return f != nil && strings.Contains(string(f.Data), `"status":"passing"`)
	}
	checked := 0
	for name := range data {
		if id, ok := strings.CutPrefix(name, "site/data/templates/"); ok && strings.HasPrefix(id, "made-up-") && passing(strings.TrimSuffix(id, ".json")) {
			checked++
		}
	}
	start := time.Now()
	o, err := Build(Options{Root: dataOverlay{os.DirFS("../.."), data}, Settings: Default, Now: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("built %d files with %d more templates, %d of them checked, in %s", len(o.Files), n, checked, time.Since(start).Round(time.Millisecond))
	built := pages(o)
	idx := indexOf(t, o)
	total := len(idx.Templates)
	made := 0
	for _, e := range idx.Templates {
		if strings.HasPrefix(e.ID, "made-up-") {
			made++
			if !passing(e.ID) {
				t.Errorf("the directory lists %s, which has no passing check", e.ID)
			}
		}
	}
	if made != checked || total < 500 {
		t.Fatalf("the index lists %d templates, %d of them made up, want the %d made-up ones with a check and at least 500 in all", total, made, checked)
	}
	if last := pagesFor(total); built[fmt.Sprintf("/templates/page/%d", last)] == "" || built[fmt.Sprintf("/templates/page/%d", last+1)] != "" {
		t.Errorf("/templates has no page %d, or has one past it", last)
	}
	for p, limit := range map[string]int{"/templates": 160_000, "/templates/page/2": 150_000} {
		if size := len(built[p]); size > limit {
			t.Errorf("%s is %d bytes, over %d", p, size, limit)
		}
		if cards := strings.Count(built[p], `class="dcard-open" href="/t#`); cards != PerPage {
			t.Errorf("%s shows %d cards, want %d", p, cards, PerPage)
		}
	}
	for name, b := range o.Files {
		if strings.HasPrefix(name, "assets/js/templates-index.") && len(b)/total > 700 {
			t.Errorf("the index takes %d bytes a template (%d in all), over 700", len(b)/total, len(b))
		}
	}
	seen := map[string]bool{"/templates": true}
	queue := []string{"/templates"}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, m := range reHref.FindAllStringSubmatch(built[p], -1) {
			addr, _, _ := strings.Cut(m[1], "#")
			if strings.HasPrefix(addr, "/templates") && !seen[addr] && built[addr] != "" {
				seen[addr] = true
				queue = append(queue, addr)
			}
		}
	}
	for _, e := range idx.Templates {
		if !seen[e.Page] {
			t.Errorf("%s can't be reached from /templates by its links", e.Page)
		}
	}
}

// PK_DIRECTORY_PREVIEW=<dir> writes the made-up directory's
// site/data/templates and site/data/library to dir, to look at in a browser:
// copy the repository with them in place and run go run ./cmd/site -root
// <copy> -serve 127.0.0.1:8080.
func TestWriteDirectoryPreview(t *testing.T) {
	dir := os.Getenv("PK_DIRECTORY_PREVIEW")
	if dir == "" {
		t.Skip("PK_DIRECTORY_PREVIEW isn't set")
	}
	for name, f := range synthetic(t, 600) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A directory with fewer templates than minFiltered shows them as a plain
// grid, with no search box: js/templates.js runs only where there are
// filters too. Every guide needs a listed template, so rather than hold some
// of today's, the test raises the bar past them.
func TestASmallDirectoryHasNoSearchItCantRun(t *testing.T) {
	saved := minFiltered
	t.Cleanup(func() { minFiltered = saved })
	minFiltered = 100
	hub := pages(build(t, Default))["/templates"]
	if n := strings.Count(hub, `class="dcard-open" href="/t#`); n == 0 || n >= minFiltered {
		t.Fatalf("/templates shows %d cards, want fewer than %d", n, minFiltered)
	}
	if strings.Contains(hub, `data-dir-q`) || strings.Contains(hub, `data-directory`) {
		t.Error("/templates has a search box or the directory's script with fewer templates than it filters")
	}
}
