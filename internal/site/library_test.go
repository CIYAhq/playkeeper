package site

import (
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// Every library page says what its template sets up as the release creates
// it: the server and its build, Java, the memory and Java's share of it, and
// each plugin with the version and licence that installed, and when a server
// was created and started from it. Its Docker command sets up the same
// server by hand, it opens its template, the hub and the sitemap list it, and
// every page under /templates has its facts.
func TestLibraryPagesFollowTheirTemplates(t *testing.T) {
	o := build(t, Default)
	built := pages(o)
	cards, err := loadTemplateCards(os.DirFS("../.."), "site/data/templates")
	if err != nil {
		t.Fatal(err)
	}
	library, err := loadLibrary(os.DirFS("../.."), "site/data/library", cards)
	if err != nil {
		t.Fatal(err)
	}
	if len(library) == 0 {
		t.Fatal("site/data/library has no templates")
	}
	hub, sitemap := built["/templates"], string(o.Files["sitemap.xml"])
	for id, l := range library {
		page, ok := built[l.Path()]
		if !ok {
			t.Errorf("%s has no page at %s", id, l.Path())
			continue
		}
		tpl := l.T()
		java := itoa(minecraft.JavaFor(tpl.Server.MinecraftVersion))
		budget := tpl.Settings.MemoryMB
		heap := minecraft.HeapFor(budget, tpl.Server.Type, 0)
		facts := between(page, `<caption class="visually-hidden">`, `</table>`)
		want := []string{
			l.TypeName() + " " + tpl.Server.MinecraftVersion + ", build " + l.Build + "</span>",
			`<th scope="row">Java</th><td>` + java + "</td>",
			`<th scope="row">Memory</th><td>` + gigabytes(budget) + ", " + gigabytes(heap) + " of it for Java</td>",
			`<th scope="row">Checked</th><td>` + Day(l.Checked) + ": created and started on Playkeeper " + l.Release + ",",
		}
		for _, p := range l.Plugins {
			want = append(want, `<a href="https://modrinth.com/plugin/`+p.Slug+`">`+p.Name+`</a> `+p.Version+" · "+licenceName(p.Licence)+"</td>")
		}
		for _, w := range want {
			if !strings.Contains(facts, w) {
				t.Errorf("%s's facts don't say %q", l.Path(), w)
			}
		}
		if !strings.Contains(page, `href="`+l.Card().Link+`"`) {
			t.Errorf("%s doesn't open its template, %s", l.Path(), l.Template)
		}
		docker := between(page, `<pre id="code-docker"`, `</pre>`)
		for _, w := range []string{
			"--memory " + gbFlag(budget) + " --memory-swap " + gbFlag(budget),
			"-e TYPE=" + strings.ToUpper(tpl.Server.Type) + " -e VERSION=" + tpl.Server.MinecraftVersion + " -e MEMORY=" + gbFlag(heap) + " ",
			"-e MODRINTH_PROJECTS=" + l.Projects() + " ",
			"itzg/minecraft-server:java" + java,
		} {
			if !strings.Contains(docker, w) {
				t.Errorf("%s's Docker command doesn't say %q: %s", l.Path(), w, docker)
			}
		}
		if strings.Contains(docker, "-e HARDCORE=true") != l.Hardcore() {
			t.Errorf("%s's Docker command and its template disagree on hardcore: %s", l.Path(), docker)
		}
		if strings.Contains(docker, "-p 24454:24454/udp") != l.Has("simple-voice-chat") {
			t.Errorf("%s's Docker command opens voice chat's port only with Simple Voice Chat: %s", l.Path(), docker)
		}
		if !strings.Contains(hub, `href="`+l.Path()+`"`) {
			t.Errorf("the hub doesn't list %s", l.Path())
		}
		if !strings.Contains(sitemap, "<loc>"+Default.BaseURL+l.Path()+"</loc>") {
			t.Errorf("the sitemap doesn't list %s", l.Path())
		}
	}
	for p := range built {
		if id, ok := strings.CutPrefix(p, "/templates/"); ok && library[id] == nil {
			t.Errorf("%s has no facts in site/data/library/%s.json", p, id)
		}
	}
}

// A library page that doesn't describe exactly what its template installs,
// or doesn't say how it was checked, stops the build.
func TestLibraryFactsTheTemplateCantBackStopTheBuild(t *testing.T) {
	cards, err := loadTemplateCards(os.DirFS("../.."), "site/data/templates")
	if err != nil {
		t.Fatal(err)
	}
	good := func() *LibraryPage {
		return &LibraryPage{ID: "towny", Template: "towny", Checked: "2026-09-28", Release: "0.4.2", Build: "129", DoneSeconds: 15.4, Plugins: []LibraryPlugin{
			{Name: "LuckPerms", Slug: "luckperms", Version: "5.5.71", Licence: "MIT", Downloads: 1},
			{Name: "Towny", Slug: "towny", Version: "0.103.2.0", Licence: "LicenseRef-CC-BY-NC-ND-3.0", Downloads: 1},
			{Name: "Chunky", Slug: "chunky", Version: "1.5.3", Licence: "GPL-3.0-only", Downloads: 1},
		}}
	}
	if err := good().check(cards); err != nil {
		t.Fatalf("a page that describes its template: %v", err)
	}
	sponge := *cards["towny"]
	tpl := *sponge.Template
	tpl.Server.Type = "sponge"
	sponge.Template = &tpl
	withSponge := maps.Clone(cards)
	withSponge["towny"] = &sponge
	if err := good().check(withSponge); err == nil {
		t.Error("a template of a type the release can't create passes")
	}
	for name, edit := range map[string]func(*LibraryPage){
		"a template that isn't there":        func(l *LibraryPage) { l.Template = "nope" },
		"a modpack's template":               func(l *LibraryPage) { l.Template = "cobblemon" },
		"a plugin the template doesn't have": func(l *LibraryPage) { l.Plugins[1].Slug = "townyadvanced" },
		"a plugin under another name":        func(l *LibraryPage) { l.Plugins[1].Name = "TownyAdvanced" },
		"a plugin left out":                  func(l *LibraryPage) { l.Plugins = l.Plugins[:2] },
		"plugins in another order":           func(l *LibraryPage) { l.Plugins[0], l.Plugins[1] = l.Plugins[1], l.Plugins[0] },
		"a plugin without its version":       func(l *LibraryPage) { l.Plugins[0].Version = "" },
		"a plugin without its licence":       func(l *LibraryPage) { l.Plugins[0].Licence = "" },
		"a plugin without its downloads":     func(l *LibraryPage) { l.Plugins[0].Downloads = 0 },
		"no release":                         func(l *LibraryPage) { l.Release = "0.4" },
		"no build":                           func(l *LibraryPage) { l.Build = "" },
		"no start time":                      func(l *LibraryPage) { l.DoneSeconds = 0 },
		"no day it was checked":              func(l *LibraryPage) { l.Checked = "28 Sep 2026" },
	} {
		l := good()
		edit(l)
		if err := l.check(cards); err == nil {
			t.Errorf("%s passes", name)
		}
	}
}
