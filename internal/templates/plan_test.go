package templates

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

// paperCatalog is what the create-server flow offers on a machine with
// 8 GB of memory and no other servers: the registry's types, of which only
// Paper can be created yet, with the builds PaperMC listed on 25 September
// 2026.
func paperCatalog() Catalog {
	return Catalog{
		Types: []CatalogType{
			{ID: "paper", Name: "Paper", Available: true, Versions: []CatalogVersion{
				paperVersion("26.3", 41, true), paperVersion("26.2", 129, false), paperVersion("26.1.2", 74, false), paperVersion("1.21.11", 132, false),
			}},
			{ID: "vanilla", Name: "Vanilla"},
			{ID: "purpur", Name: "Purpur"},
			{ID: "fabric", Name: "Fabric"},
			{ID: "quilt", Name: "Quilt"},
			{ID: "neoforge", Name: "NeoForge"},
		},
		MemoryOptionsMB:     []int{1536, 2048, 3072, 4096, 6144},
		RecommendedMemoryMB: 4096,
	}
}

func paperVersion(mc string, build int, experimental bool) CatalogVersion {
	return CatalogVersion{ID: "paper-" + mc, MinecraftVersion: mc, Build: paperBuild(build), Experimental: experimental}
}

func paperBuild(n int) map[string]string { return map[string]string{"paperBuild": strconv.Itoa(n)} }

// withType makes a type available with the given Minecraft versions, adding
// it to the catalog when a later registry would have it.
func withType(c Catalog, id string, mcs ...string) Catalog {
	var vs []CatalogVersion
	for _, mc := range mcs {
		vs = append(vs, CatalogVersion{ID: id + "-" + mc, MinecraftVersion: mc})
	}
	c.Types = slices.Clone(c.Types)
	i := slices.IndexFunc(c.Types, func(ct CatalogType) bool { return ct.ID == id })
	if i < 0 {
		c.Types = append(c.Types, CatalogType{ID: id, Name: strings.ToUpper(id[:1]) + id[1:]})
		i = len(c.Types) - 1
	}
	c.Types[i].Available, c.Types[i].Versions = true, vs
	return c
}

func planOf(t *testing.T, tp *Template, c Catalog) *Plan {
	t.Helper()
	p, err := PlanImport(tp, c)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// paperTemplate is the Paper fixture without its packs, whose warnings
// every plan of it has.
func paperTemplate(t *testing.T) *Template {
	t.Helper()
	tp := fixture(t, "paper-server.json")
	tp.Packs = nil
	return tp
}

func TestPlanPaperTemplate(t *testing.T) {
	tp := fixture(t, "paper-server.json")
	p := planOf(t, tp, paperCatalog())
	if p.Name != tp.Name || p.Description != tp.Description || p.Type != (TypeChoice{ID: "paper", Name: "Paper"}) {
		t.Errorf("got %q, %q and %+v", p.Name, p.Description, p.Type)
	}
	if want := (&VersionChoice{ID: "paper-26.2", MinecraftVersion: "26.2", Build: paperBuild(129)}); !reflect.DeepEqual(p.Version, want) {
		t.Errorf("got version %+v, want %+v", p.Version, want)
	}
	st := tp.Settings
	st.MemoryMB = 0
	if !reflect.DeepEqual(p.Settings, st) || p.MemoryMB != 4096 {
		t.Errorf("got settings %+v and %d MB", p.Settings, p.MemoryMB)
	}

	want := []addons.InstallRequest{
		{Source: addons.Modrinth, Project: "fALzjamp", VersionID: "MdY6JATr"},
		{Source: addons.Hangar, Project: "31", VersionID: "30566", AllowPrerelease: true},
		{Source: addons.Hangar, Project: "12", VersionID: "30818", AllowPrerelease: true},
	}
	var got []addons.InstallRequest
	for i, a := range p.Addons {
		got = append(got, a.Request)
		if a.Unpinned || a.Addon.Pin == nil || *a.Addon.Pin != *tp.Addons[i].Pin {
			t.Errorf("add-on %d: got %+v", i, a)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("got requests %+v, want %+v", got, want)
	}
	if p.Addons[0].PageURL != "https://modrinth.com/project/fALzjamp" || p.Addons[1].PageURL != "" {
		t.Errorf("got pages %q and %q", p.Addons[0].PageURL, p.Addons[1].PageURL)
	}
	for i, pk := range p.Packs {
		if pk.Pack != tp.Packs[i] || pk.Host != "cdn.modrinth.com" {
			t.Errorf("got pack %+v", pk)
		}
	}

	if wantKinds(t, "warnings", p.Warnings, KindResourcePack, KindDataPacks) {
		if w := p.Warnings[0]; w.Msg != "The resource pack Fresh Animations comes from cdn.modrinth.com, a website Playkeeper cannot vouch for." {
			t.Errorf("got %q", w.Msg)
		}
		if w := p.Warnings[1]; w.Msg != "The template adds 1 data pack from cdn.modrinth.com. Data packs change how the game plays and can run commands in the world." {
			t.Errorf("got %q", w.Msg)
		}
	}
	wantKinds(t, "skipped", p.Skipped)
	wantKinds(t, "blockers", p.Blockers)
	if !p.Ready || len(p.Fingerprint) != 32 {
		t.Errorf("got ready %v, fingerprint %q", p.Ready, p.Fingerprint)
	}
	if err := p.Confirm(p.Fingerprint); err != nil {
		t.Error(err)
	}
	js, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(js), "null") {
		t.Errorf("the plan's JSON has nulls: %s", js)
	}
}

func TestPlanVersions(t *testing.T) {
	substituted := []Kind{KindVersionSubstituted, KindAddonsUnpinned}
	cases := []struct {
		name, minecraft string
		build           map[string]string
		id, requested   string
		requestedBuild  map[string]string
		warnings        []Kind
	}{
		{name: "same version and build", minecraft: "26.2", build: paperBuild(129), id: "paper-26.2"},
		{name: "same version, another build", minecraft: "26.2", build: paperBuild(120), id: "paper-26.2", requestedBuild: paperBuild(120)},
		{name: "same version, no build", minecraft: "26.2", id: "paper-26.2"},
		{name: "older release of a line this Playkeeper has", minecraft: "1.21.4", build: paperBuild(232), id: "paper-1.21.11", requested: "1.21.4", warnings: substituted},
		{name: "release candidate", minecraft: "1.21.5-rc1", id: "paper-1.21.11", requested: "1.21.5-rc1", warnings: substituted},
		{name: "newer than every version here", minecraft: "26.4", id: "paper-26.2", requested: "26.4", warnings: substituted},
		{name: "older than every version here", minecraft: "1.20.4", build: paperBuild(499), id: "paper-1.21.11", requested: "1.20.4", warnings: substituted},
		{name: "snapshot of the experimental version", minecraft: "26.3-snapshot-2", id: "paper-26.2", requested: "26.3-snapshot-2", warnings: substituted},
		{name: "old-style snapshot", minecraft: "25w14a", id: "paper-26.2", requested: "25w14a", warnings: substituted},
		{name: "experimental version", minecraft: "26.3", build: paperBuild(41), id: "paper-26.3", warnings: []Kind{KindVersionExperimental}},
		{name: "experimental version, another build", minecraft: "26.3", build: paperBuild(40), id: "paper-26.3", requestedBuild: paperBuild(40),
			warnings: []Kind{KindVersionExperimental}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tp := paperTemplate(t)
			tp.Server.MinecraftVersion, tp.Server.Build = c.minecraft, c.build
			p := planOf(t, tp, paperCatalog())
			v := p.Version
			if v == nil || v.ID != c.id || v.Requested != c.requested || !maps.Equal(v.RequestedBuild, c.requestedBuild) || v.Experimental != (c.id == "paper-26.3") {
				t.Fatalf("got version %+v", v)
			}
			warned := wantKinds(t, "warnings", p.Warnings, c.warnings...)
			if !p.Ready || len(p.Addons) != 3 {
				t.Fatalf("got ready %v with %d add-ons", p.Ready, len(p.Addons))
			}
			unpinned := c.requested != ""
			for _, a := range p.Addons {
				want := addons.InstallRequest{Source: a.Source, Project: a.Project, AllowPrerelease: a.Pin.Channel == "beta"}
				if !unpinned {
					want.VersionID = a.Pin.VersionID
				}
				if a.Unpinned != unpinned || a.Request != want {
					t.Errorf("%s: got unpinned %v and %+v, want %+v", a.Name, a.Unpinned, a.Request, want)
				}
			}
			if unpinned && warned {
				w := p.Warnings[1]
				msg := fmt.Sprintf("The template's add-on versions are for Minecraft %s, so Playkeeper installs the newest version of each that fits %s.", c.minecraft, v.MinecraftVersion)
				if w.Msg != msg || w.Params["count"] != "3" {
					t.Errorf("got %+v", w)
				}
			}
		})
	}
}

func TestNearest(t *testing.T) {
	versions := func(ss ...string) []CatalogVersion {
		var vs []CatalogVersion
		for _, s := range ss {
			mc, experimental := strings.CutSuffix(s, "*")
			vs = append(vs, CatalogVersion{ID: mc, MinecraftVersion: mc, Experimental: experimental})
		}
		return vs
	}
	catalog := versions("26.2*", "26.1", "1.21.5", "1.21.3", "1.20.6")
	for want, got := range map[string]string{
		"1.21.4":      "1.21.5", // newer releases of the same line first
		"1.21.9":      "1.21.5", // then older ones of it
		"1.21":        "1.21.3",
		"1.20.6-pre1": "1.20.6",
		"26.1.1":      "26.1",
		"1.19.4":      "1.20.6", // then the nearest newer line
		"1.8.9":       "1.20.6",
		"27.1":        "26.1", // then the nearest older one, stable versions only
		"26.3":        "26.1",
		"25w14a":      "26.1", // no order: the newest stable version
	} {
		v, same := nearest(catalog, want, nil)
		if v.ID != got || same {
			t.Errorf("%s: got %s (same %v), want %s", want, v.ID, same, got)
		}
	}
	if v, same := nearest(catalog, "26.2", nil); v.ID != "26.2" || !same {
		t.Errorf("26.2: got %s (same %v), want the experimental 26.2 itself", v.ID, same)
	}
	if v, _ := nearest(versions("26.3*", "26.2*"), "1.21.4", nil); v.ID != "26.2" {
		t.Errorf("with experimental versions only, got %s, want the nearest of them", v.ID)
	}
	if v, _ := nearest(versions("latest"), "26.2", nil); v.ID != "latest" {
		t.Errorf("with versions that have no order, got %s, want the first", v.ID)
	}

	builds := []CatalogVersion{
		{ID: "paper-26.2-130", MinecraftVersion: "26.2", Build: paperBuild(130), Experimental: true},
		{ID: "paper-26.2-129", MinecraftVersion: "26.2", Build: paperBuild(129)},
		{ID: "paper-26.2-128", MinecraftVersion: "26.2", Build: paperBuild(128)},
	}
	for build, want := range map[int]string{128: "paper-26.2-128", 130: "paper-26.2-130", 1: "paper-26.2-129"} {
		if v, same := nearest(builds, "26.2", paperBuild(build)); v.ID != want || !same {
			t.Errorf("build %d: got %s (same %v), want %s", build, v.ID, same, want)
		}
	}
}

func TestPlanTypes(t *testing.T) {
	withoutModpack := func(t *testing.T) *Template {
		tp := fixture(t, "fabric-modpack.json")
		tp.Modpack = nil
		return tp
	}
	ofType := func(typ string, build map[string]string, keepAddons bool) func(*testing.T) *Template {
		return func(t *testing.T) *Template {
			tp := paperTemplate(t)
			tp.Server.Type, tp.Server.Build = typ, build
			if !keepAddons {
				tp.Addons = nil
			}
			return tp
		}
	}
	unlisted := paperCatalog()
	unlisted.Types[0].Versions, unlisted.Types[0].VersionsError = nil, "PaperMC's API did not answer."

	cases := []struct {
		name     string
		template func(*testing.T) *Template
		catalog  Catalog
		typ      TypeChoice
		version  string // empty when none can be chosen
		warnings []Kind
		skipped  []Kind
		blockers []Kind
		addons   int
		msg      string // of the first warning, skipped part or blocker
	}{
		{name: "type this Playkeeper does not know", template: ofType("folia", nil, true), catalog: paperCatalog(),
			typ: TypeChoice{ID: "folia", Name: "folia"}, blockers: []Kind{KindTypeUnknown}, addons: 3,
			msg: `This Playkeeper does not know the server type "folia" that the template uses.`},
		{name: "type this Playkeeper cannot create yet", template: withoutModpack, catalog: paperCatalog(),
			typ: TypeChoice{ID: "fabric", Name: "Fabric"}, blockers: []Kind{KindTypeUnavailable}, addons: 1,
			msg: "This Playkeeper cannot create Fabric servers yet."},
		{name: "Purpur runs as Paper", template: ofType("purpur", map[string]string{"purpurBuild": "2491"}, true), catalog: paperCatalog(),
			typ: TypeChoice{ID: "paper", Name: "Paper", Requested: "purpur"}, version: "paper-26.2", warnings: []Kind{KindTypeSubstituted}, addons: 3,
			msg: "This Playkeeper cannot create Purpur servers yet, so the new server runs Paper. Purpur is built on Paper, so Paper loads the same plugins."},
		{name: "Vanilla runs as Paper", template: ofType("vanilla", nil, false), catalog: paperCatalog(),
			typ: TypeChoice{ID: "paper", Name: "Paper", Requested: "vanilla"}, version: "paper-26.2", warnings: []Kind{KindTypeSubstituted},
			msg: "This Playkeeper cannot create Vanilla servers yet, so the new server runs Paper. Paper runs the same game."},
		{name: "Fabric runs as Quilt", template: withoutModpack, catalog: withType(paperCatalog(), "quilt", "1.21.4", "1.21.1"),
			typ: TypeChoice{ID: "quilt", Name: "Quilt", Requested: "fabric"}, version: "quilt-1.21.1", warnings: []Kind{KindTypeSubstituted}, addons: 1},
		{name: "a modpack keeps its type", template: func(t *testing.T) *Template { return fixture(t, "fabric-modpack.json") },
			catalog: withType(paperCatalog(), "quilt", "1.21.1"), typ: TypeChoice{ID: "fabric", Name: "Fabric"}, blockers: []Kind{KindTypeUnavailable}, addons: 1,
			msg: "The template's modpack Adrenaserver needs a Fabric server, which this Playkeeper cannot create yet."},
		{name: "a modpack keeps its Minecraft version", template: func(t *testing.T) *Template { return fixture(t, "fabric-modpack.json") },
			catalog: withType(paperCatalog(), "fabric", "1.21.4", "1.21.8"), typ: TypeChoice{ID: "fabric", Name: "Fabric"},
			blockers: []Kind{KindModpackUnavailable}, addons: 1,
			msg: "The modpack Adrenaserver is made for Minecraft 1.21.1, which this Playkeeper cannot create for Fabric servers."},
		{name: "modpack at its version", template: func(t *testing.T) *Template { return fixture(t, "fabric-modpack.json") },
			catalog: withType(paperCatalog(), "fabric", "1.21.8", "1.21.1"), typ: TypeChoice{ID: "fabric", Name: "Fabric"}, version: "fabric-1.21.1", addons: 1},
		{name: "versions that could not be listed", template: paperTemplate, catalog: unlisted,
			typ: TypeChoice{ID: "paper", Name: "Paper"}, blockers: []Kind{KindVersionsUnavailable}, addons: 3,
			msg: "Playkeeper could not list the versions of Paper: PaperMC's API did not answer."},
		{name: "no versions", template: paperTemplate, catalog: noVersions(),
			typ: TypeChoice{ID: "paper", Name: "Paper"}, blockers: []Kind{KindVersionsUnavailable}, addons: 3,
			msg: "Playkeeper could not list the versions of Paper."},
		{name: "a type the add-on library does not know", template: ofType("folia", nil, true), catalog: withType(paperCatalog(), "folia", "26.2"),
			typ: TypeChoice{ID: "folia", Name: "Folia"}, version: "folia-26.2", skipped: []Kind{KindAddonUnsupported},
			msg: "The template's add-ons are skipped: Playkeeper cannot install add-ons on Folia servers yet."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := planOf(t, c.template(t), c.catalog)
			if p.Type != c.typ {
				t.Errorf("got type %+v, want %+v", p.Type, c.typ)
			}
			if c.version == "" && p.Version != nil || c.version != "" && (p.Version == nil || p.Version.ID != c.version || p.Version.RequestedBuild != nil) {
				t.Errorf("got version %+v, want %q", p.Version, c.version)
			}
			wantKinds(t, "warnings", p.Warnings, c.warnings...)
			wantKinds(t, "skipped", p.Skipped, c.skipped...)
			wantKinds(t, "blockers", p.Blockers, c.blockers...)
			if len(p.Addons) != c.addons {
				t.Errorf("got %d add-ons, want %d", len(p.Addons), c.addons)
			}
			if first := slices.Concat(p.Blockers, p.Skipped, p.Warnings); c.msg != "" && (len(first) == 0 || first[0].Msg != c.msg) {
				t.Errorf("got %+v, want the message %q", first, c.msg)
			}
			if p.Ready != (len(c.blockers) == 0) {
				t.Errorf("got ready %v", p.Ready)
			}
			if len(c.blockers) > 0 {
				refused(t, p.Confirm(p.Fingerprint), c.blockers[0])
			} else if err := p.Confirm(p.Fingerprint); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestPlanModpack(t *testing.T) {
	tp := fixture(t, "fabric-modpack.json")
	p := planOf(t, tp, withType(paperCatalog(), "fabric", "1.21.1"))
	if !reflect.DeepEqual(p.Modpack, tp.Modpack) || p.MemoryMB != 6144 {
		t.Errorf("got %+v and %d MB", p.Modpack, p.MemoryMB)
	}
	if a := p.Addons[0]; a.Request != (addons.InstallRequest{Source: addons.Modrinth, Project: "fALzjamp"}) || !a.Latest || a.Unpinned {
		t.Errorf("got %+v", a)
	}
}

func TestPlanMemory(t *testing.T) {
	eightGB := []int{1536, 2048, 3072, 4096, 6144}
	cases := []struct {
		name        string
		suggested   int
		options     []int
		recommended int
		want        int
		warning     string
		blocked     bool
	}{
		{name: "the template's suggestion", suggested: 4096, options: eightGB, recommended: 4096, want: 4096},
		{name: "between two budgets", suggested: 3000, options: eightGB, recommended: 4096, want: 3072},
		{name: "below every budget", suggested: 1024, options: eightGB, recommended: 4096, want: 1536},
		{name: "no suggestion", options: eightGB, recommended: 4096, want: 4096},
		{name: "no suggestion or recommendation", options: eightGB, want: 1536},
		{name: "more than this machine has", suggested: 8192, options: eightGB, recommended: 4096, want: 6144,
			warning: "The template suggests 8 GB of memory, but this machine can give a new server at most 6 GB."},
		{name: "more than a small machine has", suggested: 2560, options: []int{1536, 2048}, recommended: 1536, want: 2048,
			warning: "The template suggests 2.5 GB of memory, but this machine can give a new server at most 2 GB."},
		{name: "options out of order", suggested: 1800, options: []int{0, 2048, 1536, 2048}, recommended: 2048, want: 2048},
		{name: "no memory left", suggested: 4096, blocked: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tp := paperTemplate(t)
			tp.Settings.MemoryMB = c.suggested
			cat := paperCatalog()
			cat.MemoryOptionsMB, cat.RecommendedMemoryMB = c.options, c.recommended
			p := planOf(t, tp, cat)
			if p.MemoryMB != c.want || p.Settings.MemoryMB != 0 {
				t.Errorf("got %d MB (settings %d), want %d", p.MemoryMB, p.Settings.MemoryMB, c.want)
			}
			switch {
			case c.warning != "":
				if wantKinds(t, "warnings", p.Warnings, KindMemoryReduced) && p.Warnings[0].Msg != c.warning {
					t.Errorf("got %q", p.Warnings[0].Msg)
				}
			case c.blocked:
				wantKinds(t, "blockers", p.Blockers, KindNoMemory)
				refused(t, p.Confirm(p.Fingerprint), KindNoMemory)
			default:
				wantKinds(t, "warnings", p.Warnings)
				wantKinds(t, "blockers", p.Blockers)
			}
		})
	}
}

func TestPlanFingerprint(t *testing.T) {
	p := planOf(t, fixture(t, "paper-server.json"), paperCatalog())
	if again := planOf(t, fixture(t, "paper-server.json"), paperCatalog()); again.Fingerprint != p.Fingerprint {
		t.Fatal("planning the same template twice gave two fingerprints")
	}

	// The name and memory are the user's to change in the create-server
	// flow, so they do not change what was confirmed.
	tp := fixture(t, "paper-server.json")
	tp.Name, tp.Settings.MemoryMB = "Our server", 2048
	small := paperCatalog()
	small.MemoryOptionsMB, small.RecommendedMemoryMB = []int{1536, 2048}, 1536
	if err := planOf(t, tp, small).Confirm(p.Fingerprint); err != nil {
		t.Errorf("with another name and memory: %v", err)
	}

	newBuild := paperCatalog()
	newBuild.Types[0].Versions[1].Build = paperBuild(130)
	altered := fixture(t, "paper-server.json")
	altered.Addons[0].Pin.VersionID = "vbGiEu4k"
	gone := fixture(t, "paper-server.json")
	gone.Packs = gone.Packs[:1]
	for name, q := range map[string]*Plan{
		"a new build":      planOf(t, fixture(t, "paper-server.json"), newBuild),
		"another version":  planOf(t, altered, paperCatalog()),
		"one pack fewer":   planOf(t, gone, paperCatalog()),
		"Paper not listed": planOf(t, fixture(t, "paper-server.json"), noVersions()),
	} {
		t.Run(name, func(t *testing.T) {
			if q.Fingerprint == p.Fingerprint {
				t.Fatal("the fingerprint did not change")
			}
			err := q.Confirm(p.Fingerprint)
			if len(q.Blockers) > 0 {
				refused(t, err, q.Blockers[0].Kind)
				return
			}
			refused(t, err, addons.KindPlanChanged)
		})
	}
}

func noVersions() Catalog {
	c := paperCatalog()
	c.Types[0].Versions = nil
	return c
}

func TestPlanPacks(t *testing.T) {
	tp := fixture(t, "paper-server.json")
	tp.Packs = append(tp.Packs,
		Pack{Kind: DataPack, Name: "Incendium", URL: "https://github.com/Stardust-Labs-MC/Incendium/releases/download/v5.4.4/Incendium_1.21.x_v5.4.4.zip",
			SHA256: digest("sha256", "incendium")},
		Pack{Kind: DataPack, Name: "Nullscape", URL: "https://cdn.modrinth.com/data/LPjGiSO4/versions/9Tds4xyZ/Nullscape_1.21.x_v1.2.10.zip",
			SHA1: digest("sha1", "nullscape")},
	)
	p := planOf(t, tp, paperCatalog())
	var hosts []string
	for _, pk := range p.Packs {
		hosts = append(hosts, pk.Host)
	}
	if want := []string{"cdn.modrinth.com", "cdn.modrinth.com", "github.com", "cdn.modrinth.com"}; !slices.Equal(hosts, want) {
		t.Errorf("got hosts %v, want %v", hosts, want)
	}
	if got := joinNames([]string{"a.example", "b.example", "c.example"}); got != "a.example, b.example and c.example" {
		t.Errorf("got %q", got)
	}
	if !wantKinds(t, "warnings", p.Warnings, KindResourcePack, KindDataPacks) {
		return
	}
	if w := p.Warnings[1]; w.Params["count"] != "3" || w.Params["hosts"] != "cdn.modrinth.com, github.com" ||
		w.Msg != "The template adds 3 data packs from cdn.modrinth.com and github.com. Data packs change how the game plays and can run commands in the world." {
		t.Errorf("got %+v", w)
	}
}

func TestPlanRefusesInvalidTemplates(t *testing.T) {
	_, err := PlanImport(nil, paperCatalog())
	refused(t, err, KindNotTemplate)

	tp := paperTemplate(t)
	tp.Settings.MOTD = "§cRed"
	_, err = PlanImport(tp, paperCatalog())
	refused(t, err, KindInvalid)

	tp = paperTemplate(t)
	tp.Addons[1].DependencyOf = "31"
	_, err = PlanImport(tp, paperCatalog())
	refused(t, err, KindInvalid)

	tp = paperTemplate(t)
	tp.Format = Format + 1
	_, err = PlanImport(tp, paperCatalog())
	refused(t, err, KindNewer)
}
