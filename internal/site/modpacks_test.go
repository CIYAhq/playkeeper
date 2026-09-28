package site

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// Every pack page says what the release would do with its pack: the Java its
// Minecraft version runs on and the memory New server suggests, worked out
// from the pack's facts, and when they were checked. Every page under
// /modpacks has its pack's facts, and the hub lists each page.
func TestModpackPagesFollowTheProduct(t *testing.T) {
	built := pages(build(t, Default))
	cards, err := loadTemplateCards(os.DirFS("../.."), "site/data/templates")
	if err != nil {
		t.Fatal(err)
	}
	packs, err := loadModpacks(os.DirFS("../.."), "site/data/modpacks", cards)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) == 0 {
		t.Fatal("site/data/modpacks has no packs")
	}
	hub := built["/modpacks"]
	for id, m := range packs {
		page, ok := built[m.Path()]
		if !ok {
			t.Errorf("%s has no page at %s", id, m.Path())
			continue
		}
		facts := between(page, `<caption class="visually-hidden">`, `</table>`)
		for _, want := range []string{
			"<th scope=\"row\">Java</th><td>" + itoa(minecraft.JavaFor(m.Minecraft)) + "</td>",
			"<th scope=\"row\">Memory</th><td>" + gigabytes(minecraft.PackNeedMB(m.Type, m.Mods, m.HeapMB)) + ", as Playkeeper suggests it</td>",
			"<th scope=\"row\">Minecraft</th><td>" + m.Minecraft + "</td>",
			m.TypeName() + " " + m.Loader + "</span>",
		} {
			if !strings.Contains(facts, want) {
				t.Errorf("%s's facts don't say %q", m.Path(), want)
			}
		}
		if !strings.Contains(facts, `<th scope="row">Checked</th><td>`+Day(m.Checked)+", on "+m.SourceName()) {
			t.Errorf("%s doesn't say when its facts were checked", m.Path())
		}
		if row := between(hub, `<a href="`+m.Path()+`">`+m.Name+`</a> `+m.Version+`</th>`, "</tr>"); !strings.Contains(row, `<td data-col="Memory">`+m.Memory()+`</td>`) {
			t.Errorf("the hub doesn't list %s with the memory it needs: %q", m.Path(), row)
		}
		if m.Template != "" && !strings.Contains(page, `href="`+cards[m.Template].Link+`"`) {
			t.Errorf("%s doesn't open its template, %s", m.Path(), m.Template)
		}
		// The image's MEMORY is Java's heap, so the Docker command gives
		// Java what Playkeeper would, inside a container of the whole budget.
		if docker := between(page, `<pre id="code-docker"`, `</pre>`); docker != "" {
			budget := minecraft.PackNeedMB(m.Type, m.Mods, m.HeapMB)
			heap := minecraft.HeapFor(budget, m.Type, m.Mods)
			for _, want := range []string{"--memory " + gbFlag(budget) + " --memory-swap " + gbFlag(budget), "-e MEMORY=" + gbFlag(heap) + " "} {
				if !strings.Contains(docker, want) {
					t.Errorf("%s's Docker command doesn't say %q: %s", m.Path(), want, docker)
				}
			}
		}
	}
	for p := range built {
		if id, ok := strings.CutSuffix(strings.TrimPrefix(p, "/modpacks/"), "-server"); ok && strings.HasPrefix(p, "/modpacks/") && packs[id] == nil {
			t.Errorf("%s has no pack in site/data/modpacks/%s.json", p, id)
		}
	}
}

// A pack the release couldn't install as its page says stops the build.
func TestModpackFactsTheReleaseCantBackStopTheBuild(t *testing.T) {
	cards, err := loadTemplateCards(os.DirFS("../.."), "site/data/templates")
	if err != nil {
		t.Fatal(err)
	}
	good := func() *Modpack {
		return &Modpack{Name: "Cobblemon Official Modpack", Source: "modrinth", Project: "5FFgwNNP", Page: "https://modrinth.com/modpack/cobblemon-fabric",
			Author: "Cobbled Studios", Downloads: 1, Version: "1.8.1", Released: "2026-09-13", Type: "fabric", Loader: "0.19.5",
			Minecraft: "1.21.1", Mods: 32, DownloadMB: 183, Template: "cobblemon", Checked: "2026-09-28"}
	}
	if err := good().check(cards); err != nil {
		t.Fatalf("a pack the release installs: %v", err)
	}
	for name, edit := range map[string]func(*Modpack){
		"Minecraft older than the packs it offers":  func(m *Modpack) { m.Minecraft = "1.20.1" },
		"a type it doesn't run":                     func(m *Modpack) { m.Type = "sponge" },
		"a plugin server":                           func(m *Modpack) { m.Type = "paper" },
		"another source":                            func(m *Modpack) { m.Source = "technic" },
		"a template of another version":             func(m *Modpack) { m.Version = "1.8.0" },
		"a template with less memory than it needs": func(m *Modpack) { m.Mods = 400 },
		"a template that isn't there":               func(m *Modpack) { m.Template = "nope" },
		"no day it was checked":                     func(m *Modpack) { m.Checked = "" },
		"mods not counted":                          func(m *Modpack) { m.Mods = 0 },
	} {
		m := good()
		edit(m)
		if err := m.check(cards); err == nil {
			t.Errorf("%s passes", name)
		}
	}
}

var (
	reArticle  = regexp.MustCompile(`(?s)<article class="article">(.*?)</article>`)
	reSentence = regexp.MustCompile(`[^.!?]+[.!?]`)
)

// Google treats pages made at scale from one template with little of their
// own as spam, so most of each pack page's and library template page's
// sentences are its own: said on no other page under the same hub.
func TestModpackAndTemplatePagesAreMostlyTheirOwn(t *testing.T) {
	built := pages(build(t, Default))
	for _, hub := range []string{"/modpacks/", "/templates/"} {
		t.Run(strings.Trim(hub, "/"), func(t *testing.T) {
			sentences := map[string][]string{}
			seen := map[string]int{}
			for p, html := range built {
				if !strings.HasPrefix(p, hub) {
					continue
				}
				m := reArticle.FindStringSubmatch(html)
				if m == nil {
					t.Fatalf("%s has no article", p)
				}
				own := map[string]bool{}
				for _, s := range reSentence.FindAllString(plainText(m[1]), -1) {
					s = strings.Join(strings.Fields(s), " ")
					if len(strings.Fields(s)) < 4 || own[s] {
						continue
					}
					own[s] = true
					sentences[p] = append(sentences[p], s)
					seen[s]++
				}
			}
			if len(sentences) < 2 {
				t.Fatalf("%d pages under %s; this test compares them", len(sentences), hub)
			}
			for p, ss := range sentences {
				unique := 0
				for _, s := range ss {
					if seen[s] == 1 {
						unique++
					}
				}
				share := float64(unique) / float64(len(ss))
				t.Logf("%s: %d of its %d sentences are its own (%.0f%%)", p, unique, len(ss), share*100)
				if share < 0.6 {
					t.Errorf("%s: %d of its %d sentences are its own (%.0f%%), want at least 60%%", p, unique, len(ss), share*100)
				}
			}
		})
	}
}
