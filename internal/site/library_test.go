package site

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/templates/checks"
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
	checked, err := checks.Read(os.DirFS("../.."), "site/data/checks")
	if err != nil {
		t.Fatal(err)
	}
	library, err := loadLibrary(os.DirFS("../.."), "site/data/library", cards, checked)
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

// No page links a template the release people install can't open yet: its
// cards stay out, or give way to the stand-in a page names, and the pack
// pages offer New server › A modpack, until that release is out.
func TestNoPageOpensAHeldTemplate(t *testing.T) {
	built := pages(build(t, Default))
	cards, err := loadTemplateCards(os.DirFS("../.."), "site/data/templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cards {
		if !c.Held() {
			continue
		}
		for p, html := range built {
			if strings.Contains(html, c.Link) {
				t.Errorf("%s opens the %s template, which opens only from Playkeeper %s", p, c.ID, c.OpensFrom)
			}
		}
	}
}

// A held template says which release opens it, so it's clear when it can
// come back.
func TestAHeldTemplateSaysWhichReleaseOpensIt(t *testing.T) {
	dir := t.TempDir()
	tpl, err := os.ReadFile("../../site/data/templates/creative.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "creative.json"), tpl, 0o644); err != nil {
		t.Fatal(err)
	}
	for opens, ok := range map[string]bool{"": true, "0.4.4": true, "soon": false, "v0.4.4": false} {
		cards := `{"creative": {"art": "app/pixel-art/play-creative.svg", "opensFrom": "` + opens + `"}}`
		if err := os.WriteFile(filepath.Join(dir, "cards.json"), []byte(cards), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := loadTemplateCards(os.DirFS(dir), ".")
		switch {
		case ok && err != nil:
			t.Errorf("opensFrom %q: %v", opens, err)
		case !ok && err == nil:
			t.Errorf("opensFrom %q passes", opens)
		case ok && got["creative"].Held() != (opens != ""):
			t.Errorf("opensFrom %q: held is %v", opens, got["creative"].Held())
		}
	}
}

func TestVersionNameDropsWhatTheSourceAdds(t *testing.T) {
	for in, want := range map[string]string{
		"bukkit-2.6.24": "2.6.24", "v5.5.71-bukkit": "5.5.71", "mc26.2-0.25.3-fabric": "0.25.3", "fabric-2.6.24+26.2": "2.6.24",
		"0.161.0+26.2": "0.161.0", "1.5.3": "1.5.3", "0.103.2.0": "0.103.2.0", "5.12.1-SNAPSHOT+1069": "5.12.1-SNAPSHOT+1069",
		"2.11.3-b1247": "2.11.3-b1247", "v": "v", "version": "version",
	} {
		if got := versionName(in); got != want {
			t.Errorf("versionName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A library page takes its facts from its template's check, and a template
// without a passing check has no page.
func TestLibraryPagesNeedAPassingCheck(t *testing.T) {
	passing := &checks.Check{Status: checks.Passing, Checked: "2026-09-28", Release: "0.4.2", Build: "129", DoneSeconds: 12.5,
		Addons: []checks.Addon{{Name: "Chunky", Source: "modrinth", Slug: "chunky", Version: "1.5.3", Licence: "GPL-3.0-only", Downloads: 9}}}
	l := &LibraryPage{ID: "smp", Template: "survival-with-friends"}
	if err := l.fill(passing); err != nil {
		t.Fatal(err)
	}
	if l.Release != "0.4.2" || l.DoneSeconds != 12.5 || len(l.Plugins) != 1 || l.Plugins[0].Version != "1.5.3" || l.Plugins[0].Downloads != 9 {
		t.Errorf("filled %+v", l)
	}
	if err := (&LibraryPage{Template: "x"}).fill(nil); err == nil {
		t.Error("a page without a check filled")
	}
	failed := &checks.Check{Status: checks.Failing, Failure: "LifeStealZ failed to enable", Checked: "2026-09-29", Release: "0.4.2"}
	if err := (&LibraryPage{Template: "x"}).fill(failed); err == nil || !strings.Contains(err.Error(), "LifeStealZ failed to enable") {
		t.Errorf("a failing check filled a page: %v", err)
	}
}

// A template that failed its last check is held: every card block leaves it
// out, as it does a template whose release isn't out.
func TestAFailingCheckHoldsItsTemplate(t *testing.T) {
	cards, err := loadTemplateCards(os.DirFS("../.."), "site/data/templates")
	if err != nil {
		t.Fatal(err)
	}
	if cards["creative"].Held() {
		t.Fatal("creative is held before its check fails")
	}
	failing(cards, map[string]*checks.Check{
		"creative": {Status: checks.Failing, Failure: "WorldEdit failed to enable", Checked: "2026-09-29", Release: "0.4.2"},
		"towny":    {Status: checks.Passing, Checked: "2026-09-29", Release: "0.4.2", DoneSeconds: 15},
		"gone":     {Status: checks.Failing, Failure: "no card", Checked: "2026-09-29", Release: "0.4.2"},
	})
	if !cards["creative"].Held() || cards["towny"].Held() {
		t.Errorf("creative held %v, towny held %v", cards["creative"].Held(), cards["towny"].Held())
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
