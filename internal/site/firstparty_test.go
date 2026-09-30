package site

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons/firstparty"
	"github.com/CIYAhq/playkeeper/internal/templates/checks"
)

// withFirstParty is the directory's data with AI Build Battle's template,
// whose one plugin is Playkeeper's own, and that plugin added to Bedwars,
// whose guide lists it beside Modrinth's. Both passed their checks, which
// give the plugin its registry's licence and no downloads.
func withFirstParty(t *testing.T, fp *firstparty.Plugin, jar firstparty.Jar) fstest.MapFS {
	t.Helper()
	data := dataFS(t)
	put := func(name string, v any) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		data[name] = &fstest.MapFile{Data: b}
	}
	read := func(name string, v any) {
		t.Helper()
		if err := json.Unmarshal(data[name].Data, v); err != nil {
			t.Fatal(err)
		}
	}
	addon := map[string]any{"source": "playkeeper", "project": fp.ID, "slug": fp.Slug, "name": fp.Name,
		"pin": map[string]string{"versionId": jar.Version, "versionNumber": jar.Version, "channel": "release", "hashAlgo": "sha256", "hash": jar.SHA256}}
	fact := checks.Addon{Name: fp.Name, Source: "playkeeper", Slug: fp.Slug, Version: jar.Version, Licence: fp.License}

	put("site/data/templates/ai-build-battle.json", map[string]any{
		"playkeeperTemplate": 1, "name": "AI Build Battle", "game": "minecraft-java",
		"description": "Paper for building contests: type /aibuild and an AI model builds it in front of you, block by block.",
		"server":      map[string]any{"type": "paper", "minecraftVersion": "1.21.10"},
		"settings":    map[string]any{"difficulty": "normal", "pvp": false, "gameMode": "creative", "maxPlayers": 12, "playStyle": "friends", "memoryMB": 4096},
		"addons":      []any{addon},
	})
	put("site/data/checks/ai-build-battle.json", checks.Check{Status: checks.Passing, Checked: "2026-09-30", Release: "0.4.4", Build: "129", DoneSeconds: 9.5, Addons: []checks.Addon{fact}})
	var cards map[string]map[string]any
	read("site/data/templates/cards.json", &cards)
	cards["ai-build-battle"] = map[string]any{"art": "app/pixel-art/play-creative.svg", "categories": []string{"creative"}, "tags": []string{"minigame"}, "added": "2026-09-30", "popularity": 100000}
	put("site/data/templates/cards.json", cards)

	var bedwars map[string]any
	read("site/data/templates/bedwars.json", &bedwars)
	bedwars["addons"] = append(bedwars["addons"].([]any), addon)
	put("site/data/templates/bedwars.json", bedwars)
	var bedwarsCheck checks.Check
	read("site/data/checks/bedwars.json", &bedwarsCheck)
	bedwarsCheck.Addons = append(bedwarsCheck.Addons, fact)
	put("site/data/checks/bedwars.json", bedwarsCheck)
	return data
}

// One of Playkeeper's own plugins is listed with its registry's licence and
// "Ships with Playkeeper" where others show their downloads, links to no
// registry, and shows its initial: the build asks no icon fetcher about it,
// nor counts it among the add-ons left without an icon. A guide's Docker
// command leaves it out of MODRINTH_PROJECTS and says so.
func TestPlaykeepersOwnPluginsAreListedWithoutARegistry(t *testing.T) {
	fp := firstparty.Lookup("ai-build-battle")
	jar, err := fp.Jar()
	if err != nil {
		t.Fatal(err)
	}
	root := dataOverlay{os.DirFS("../.."), withFirstParty(t, fp, jar)}
	var asked []Project
	fetch := func(_ context.Context, ps []Project) map[Project][]byte {
		asked = append(asked, ps...)
		out := map[Project][]byte{}
		for _, p := range ps {
			out[p] = picture(t, "png", 64, 64)
		}
		return out
	}
	o, err := Build(Options{Root: root, Settings: Default, Now: time.Now(), Icons: fetch})
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) == 0 {
		t.Fatal("no icons were asked for")
	}
	for _, p := range asked {
		if p.Source == "playkeeper" || p.ID == fp.ID {
			t.Errorf("the icon fetcher was asked about %s", p.key())
		}
	}
	if len(o.NoIcons) > 0 {
		t.Errorf("%v kept their initial, with every icon fetched", o.NoIcons)
	}

	built := pages(o)
	idx := indexOf(t, o)
	byID := map[string]indexTemplate{}
	for _, e := range idx.Templates {
		byID[e.ID] = e
	}
	own, bedwars := byID["ai-build-battle"], byID["bedwars"]
	if !slices.Equal(own.Addons, []string{fp.Name}) || !slices.Equal(own.Icons, []int{-1}) {
		t.Errorf("the index lists %v with icons %v for AI Build Battle's template", own.Addons, own.Icons)
	}
	if len(bedwars.Icons) != 4 || bedwars.Icons[3] != -1 || slices.Contains(bedwars.Icons[:3], -1) {
		t.Errorf("the index gives Bedwars' add-ons %v the icons %v", bedwars.Addons, bedwars.Icons)
	}
	if card := cardOf(built["/templates"], own.Page); !strings.Contains(card, `<span class="ai ai-initial" aria-hidden="true">A</span><span>`+fp.Name+`</span>`) {
		t.Errorf("/templates doesn't show AI Build Battle's initial on its card: %s", card)
	}

	for _, e := range []indexTemplate{own, bedwars} {
		installs := between(built[e.Page], `id="installs"`, `</section>`)
		var item string
		for _, li := range strings.Split(installs, `<li class="addon">`)[1:] {
			if strings.Contains(li, fp.Name) {
				item = li
			}
		}
		for _, want := range []string{
			`<span class="addon-mark" aria-hidden="true">A</span>`,
			`<span class="addon-name">` + fp.Name + `</span><span class="addon-version">` + versionName(jar.Version) + `</span>`,
			`<span>Ships with Playkeeper</span><span>` + licenceName(fp.License) + `</span>`,
		} {
			if !strings.Contains(item, want) {
				t.Errorf("%s lists AI Build Battle without %s: %s", e.Page, want, item)
			}
		}
		if strings.Contains(installs, "modrinth.com/project/"+fp.Slug) || strings.Contains(installs, "/assets/icons/playkeeper-") {
			t.Errorf("%s links AI Build Battle to Modrinth or shows it an icon: %s", e.Page, installs)
		}
		if others := len(e.Addons) - 1; strings.Count(installs, " downloads</span>") != others || strings.Count(installs, `href="https://modrinth.com/project/`) != others {
			t.Errorf("%s doesn't show its %d other add-ons' downloads and pages: %s", e.Page, others, installs)
		}
	}

	libraryPagesFollow(t, root, o)
	docker := between(built["/templates/bedwars"], `<pre id="code-docker"`, `</pre>`)
	if !strings.Contains(docker, "-e MODRINTH_PROJECTS=worldedit:") || strings.Contains(docker, fp.Slug) {
		t.Errorf("the Bedwars guide's Docker command asks Modrinth for AI Build Battle, or for nothing: %s", docker)
	}
	cards, err := loadTemplateCards(root, "site/data/templates")
	if err != nil {
		t.Fatal(err)
	}
	if p := (&LibraryPage{card: cards["ai-build-battle"]}).Projects(); p != "" {
		t.Errorf("a guide to AI Build Battle's template would ask Modrinth for %q", p)
	}
	if _, err := DashboardLibrary(root, Default); err != nil {
		t.Errorf("the template list releases carry: %v", err)
	}
}
