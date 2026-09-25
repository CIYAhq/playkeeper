package software

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Releases the fakes can add: one that needs a newer Java than the image
// has, and one Mojang does not list at all.
const (
	needsJava27 = "26.4"
	notOnMojang = "26.5"
)

var allTypes = []string{Vanilla, Purpur, Fabric, Quilt, NeoForge}

// catalogFake serves Mojang and one type's upstream from the fixtures. With
// reverse, every list comes in the opposite order. With newer, the type also
// lists needsJava27 and notOnMojang, and Mojang lists needsJava27 as a
// release that needs Java 27.
func catalogFake(t *testing.T, typeID string, reverse, newer bool) *fakeNet {
	t.Helper()
	f := newFakeNet(t)
	files := mojangFiles(t)
	var extra []mojangEntry
	if newer {
		files[needsJava27] = mojangVersionFile(t, f, needsJava27, 27, []byte("server "+needsJava27))
		extra = append(extra, mojangEntry{ID: needsJava27, Type: "release", URL: "https://piston-meta.mojang.com/v1/packages/" + needsJava27 + ".json"})
	}
	list := func(b []byte, path ...string) []byte {
		if !reverse {
			return b
		}
		return reverseList(t, b, path...)
	}
	f.serve(mojangManifestURL, list(serveMojang(t, f, files, extra...), "versions"))
	switch typeID {
	case Purpur:
		project := readFixture(t, "purpur/project.json")
		if newer {
			project = editJSON(t, project, func(v any) any {
				obj(v)["versions"] = append(obj(v)["versions"].([]any), needsJava27, notOnMojang)
				return v
			})
			for _, mc := range []string{needsJava27, notOnMojang} {
				f.serve(purpurAPI+"/"+mc, editJSON(t, readFixture(t, "purpur/26.2.json"), func(v any) any {
					obj(v)["version"] = mc
					return v
				}))
			}
		}
		f.serve(purpurAPI, list(project, "versions"))
		for _, mc := range []string{"26.3", "26.2", "26.1.2", "1.21.11"} {
			f.serve(purpurAPI+"/"+mc, list(readFixture(t, "purpur/"+mc+".json"), "builds", "all"))
		}
	case Fabric, Quilt:
		base, dir := fabricMeta, "fabric"
		if typeID == Quilt {
			base, dir = quiltMeta, "quilt"
		}
		games := readFixture(t, dir+"/game.json")
		if newer {
			games = editJSON(t, games, func(v any) any {
				return append([]any{
					map[string]any{"version": notOnMojang, "stable": true},
					map[string]any{"version": needsJava27, "stable": true},
				}, v.([]any)...)
			})
		}
		f.serve(base+"/versions/game", list(games))
		f.serve(base+"/versions/loader", list(readFixture(t, dir+"/loader.json")))
	case NeoForge:
		meta := readFixture(t, "neoforge/maven-metadata.xml")
		if newer {
			meta = []byte(strings.Replace(string(meta), "<versions>", "<versions>\n      <version>26.4.0.1</version>\n      <version>26.5.0.1</version>", 1))
		}
		if reverse {
			meta = reverseXMLVersions(meta)
		}
		f.serve(neoforgeMaven+"/maven-metadata.xml", meta)
	}
	return f
}

func pinDetail(p Pin) string {
	switch {
	case p.PurpurBuild != 0:
		return fmt.Sprintf(" build %d", p.PurpurBuild)
	case p.FabricLoader != "":
		return " loader " + p.FabricLoader
	case p.QuiltLoader != "":
		return " loader " + p.QuiltLoader
	case p.NeoForgeVersion != "":
		return " neoforge " + p.NeoForgeVersion
	}
	return ""
}

func describeReleases(rs []Release) []string {
	var out []string
	for _, r := range rs {
		s := r.MinecraftVersion + " " + string(r.Channel)
		if r.Recommended {
			s += " recommended"
		}
		out = append(out, s+pinDetail(r.Pin))
	}
	return out
}

func TestCatalog(t *testing.T) {
	want := map[string][]string{
		Vanilla:  {"26.3 stable recommended", "26.2 stable", "26.1.2 stable", "1.21.11 stable"},
		Purpur:   {"26.3 experimental build 2641", "26.2 stable recommended build 2633", "26.1.2 stable build 2592", "1.21.11 stable build 2568"},
		Fabric:   {"26.3 stable recommended loader 0.19.5", "26.2 stable loader 0.19.5", "26.1.2 stable loader 0.19.5", "1.21.11 stable loader 0.19.5"},
		Quilt:    {"26.3 stable recommended loader 0.30.1", "26.2 stable loader 0.30.1", "26.1.2 stable loader 0.30.1", "1.21.11 stable loader 0.30.1"},
		NeoForge: {"26.3 beta neoforge 26.3.0.16-beta", "26.2 stable recommended neoforge 26.2.0.88", "26.1.2 stable neoforge 26.1.2.109", "1.21.11 stable neoforge 21.11.45"},
	}
	variants := []struct {
		name           string
		reverse, newer bool
	}{
		{"as listed", false, false},
		{"lists reversed", true, false},
		{"newer releases it cannot run", false, true},
	}
	for _, typeID := range allTypes {
		for _, v := range variants {
			t.Run(typeID+"/"+v.name, func(t *testing.T) {
				f := catalogFake(t, typeID, v.reverse, v.newer)
				got, err := f.sources().Catalog(context.Background(), typeID)
				if err != nil {
					t.Fatal(err)
				}
				if d := describeReleases(got); !slices.Equal(d, want[typeID]) {
					t.Fatalf("got  %q\nwant %q", d, want[typeID])
				}
				recommended := 0
				for _, r := range got {
					java := 25
					if strings.HasPrefix(r.MinecraftVersion, "1.") {
						java = 21
					}
					if r.ID != typeID+"-"+r.MinecraftVersion || r.Type != typeID || r.Label != typeName(typeID)+" "+r.MinecraftVersion || r.Java != java {
						t.Errorf("release %q has id %q, type %q, label %q, Java %d", r.MinecraftVersion, r.ID, r.Type, r.Label, r.Java)
					}
					if r.Experimental != (r.Channel != Stable) || r.Pin.Type != typeID || r.Pin.MinecraftVersion != r.MinecraftVersion {
						t.Errorf("release %q: experimental %v, channel %s, pin %+v", r.MinecraftVersion, r.Experimental, r.Channel, r.Pin)
					}
					if err := r.Pin.Validate(); err != nil {
						t.Errorf("release %q: its pin is not valid: %v", r.MinecraftVersion, err)
					}
					if r.Recommended {
						recommended++
					}
				}
				if recommended != 1 {
					t.Errorf("%d releases are recommended, want 1", recommended)
				}
				if v.newer && typeID == Purpur {
					for _, mc := range []string{needsJava27, notOnMojang} {
						if n := f.hitCount(purpurAPI + "/" + mc); n != 0 {
							t.Errorf("Purpur's builds for %s were loaded %d times, want never", mc, n)
						}
					}
				}
			})
		}
	}
}

func TestCatalogSkipsExperimentalOlderThanStable(t *testing.T) {
	f := catalogFake(t, Purpur, false, false)
	f.serve(purpurAPI+"/26.1.2", editJSON(t, readFixture(t, "purpur/26.1.2.json"), func(v any) any {
		for _, b := range obj(v, "builds")["all"].([]any) {
			b.(map[string]any)["metadata"] = map[string]any{"type": "experimental"}
		}
		return v
	}))
	got, err := f.sources().Catalog(context.Background(), Purpur)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"26.3 experimental build 2641", "26.2 stable recommended build 2633", "1.21.11 stable build 2568"}
	if d := describeReleases(got); !slices.Equal(d, want) {
		t.Fatalf("got  %q\nwant %q", d, want)
	}
}

func TestCatalogNotes(t *testing.T) {
	f := catalogFake(t, NeoForge, false, false)
	got, err := f.sources().Catalog(context.Background(), NeoForge)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		note Note
		text string
	}{
		"26.3":    {NoteExperimental, "Experimental: NeoForge only has beta builds for Minecraft 26.3 so far."},
		"26.2":    {NoteRecommended, "Recommended: the newest stable NeoForge release. Java Edition 26.2 clients can join."},
		"26.1.2":  {NoteStable, "Stable. Java Edition 26.1.2 clients can join."},
		"1.21.11": {NoteStable, "Stable. Java Edition 1.21.11 clients can join."},
	}
	for _, r := range got {
		w := want[r.MinecraftVersion]
		if r.Note != w.note || !strings.HasPrefix(r.Notes, w.text) {
			t.Errorf("%s: got note %s %q, want %s %q", r.MinecraftVersion, r.Note, r.Notes, w.note, w.text)
		}
	}
}

func TestCatalogWithNothingToOffer(t *testing.T) {
	f := newFakeNet(t)
	serveMojang(t, f, mojangFiles(t))
	f.serve(purpurAPI, []byte(`{"project":"purpur","versions":["1.20.4","1.20.6","26.4-snapshot-1"]}`))
	_, err := f.sources().Catalog(context.Background(), Purpur)
	if e := wantKind(t, err, KindNoVersions); e.Msg != "Purpur lists no version Playkeeper can run." {
		t.Errorf("got %q", e.Msg)
	}
}

func TestCatalogSkipsVersionsWithoutBuilds(t *testing.T) {
	f := catalogFake(t, Purpur, false, false)
	f.status(purpurAPI+"/26.2", 404)
	f.serve(purpurAPI+"/26.1.2", editJSON(t, readFixture(t, "purpur/26.1.2.json"), func(v any) any {
		for _, b := range obj(v, "builds")["all"].([]any) {
			b.(map[string]any)["result"] = "FAILURE"
		}
		return v
	}))
	got, err := f.sources().Catalog(context.Background(), Purpur)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"26.3 experimental build 2641", "1.21.11 stable recommended build 2568"}
	if d := describeReleases(got); !slices.Equal(d, want) {
		t.Fatalf("got  %q\nwant %q", d, want)
	}
}

func TestCatalogUpstreamErrors(t *testing.T) {
	const mojang262 = "https://piston-meta.mojang.com/v1/packages/33c420747ce582e48dff1d8c5d8e67e5bb6257c9/26.2.json"
	tests := []struct {
		name   string
		typeID string
		change func(f *fakeNet)
		kind   Kind
		msg    string
	}{
		{"Purpur limits a build list", Purpur, func(f *fakeNet) { f.status(purpurAPI+"/26.2", 429) }, KindRateLimited,
			"Purpur is limiting requests from this host, so Playkeeper could not load its build list for Minecraft 26.2 (HTTP 429)."},
		{"Purpur fails a build list", Purpur, func(f *fakeNet) { f.status(purpurAPI+"/26.2", 503) }, KindUpstreamStatus,
			"Purpur answered HTTP 503 when Playkeeper asked for its build list for Minecraft 26.2."},
		{"Mojang fails a version file", Vanilla, func(f *fakeNet) { f.status(mojang262, 503) }, KindUpstreamStatus,
			"Mojang answered HTTP 503 when Playkeeper asked for the version file of Minecraft 26.2."},
		{"Fabric sends a broken game list", Fabric, func(f *fakeNet) { f.serve(fabricMeta+"/versions/game", []byte("<html>")) }, KindMalformed,
			"Fabric sent its list of Minecraft versions in a form Playkeeper could not read"},
		{"Quilt limits its loader list", Quilt, func(f *fakeNet) { f.status(quiltMeta+"/versions/loader", 429) }, KindRateLimited,
			"Quilt is limiting requests from this host, so Playkeeper could not load its list of loader versions (HTTP 429)."},
		{"NeoForge sends a broken version list", NeoForge, func(f *fakeNet) { f.serve(neoforgeMaven+"/maven-metadata.xml", []byte("{}")) }, KindMalformed,
			"NeoForge sent its version list in a form Playkeeper could not read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := catalogFake(t, tt.typeID, false, false)
			tt.change(f)
			_, err := f.sources().Catalog(context.Background(), tt.typeID)
			if e := wantKind(t, err, tt.kind); !strings.HasPrefix(e.Msg, tt.msg) {
				t.Errorf("got %q, want it to start with %q", e.Msg, tt.msg)
			}
		})
	}
}

func TestCatalogOfUnsupportedType(t *testing.T) {
	for _, typeID := range []string{"paper", "forge", "", "../vanilla"} {
		_, err := Sources{}.Catalog(context.Background(), typeID)
		wantKind(t, err, KindUnsupported)
	}
}

func describeBuilds(bs []Build) []string {
	var out []string
	for _, b := range bs {
		s := b.Version + " " + string(b.Channel)
		if b.Recommended {
			s += " recommended"
		}
		out = append(out, s)
	}
	return out
}

func TestBuilds(t *testing.T) {
	purpur262 := []string{"2633 stable recommended", "2632 stable", "2631 stable", "2630 stable", "2629 stable", "2628 stable", "2627 stable", "2622 stable", "2621 stable"}
	purpur2612 := []string{"2592 stable recommended", "2591 stable", "2590 stable", "2589 stable", "2588 stable", "2587 stable", "2586 stable", "2585 stable",
		"2584 stable", "2583 stable", "2582 experimental", "2581 experimental", "2580 experimental"}
	tests := []struct {
		typeID, mc string
		want       []string
	}{
		{Vanilla, "26.2", nil},
		{Purpur, "26.2", purpur262},
		{Purpur, "26.1.2", purpur2612},
		{Purpur, "26.3", []string{"2641 experimental", "2640 experimental", "2639 experimental", "2638 experimental", "2637 experimental", "2636 experimental",
			"2635 experimental", "2634 experimental"}},
		{Fabric, "26.3", []string{"0.19.5 stable recommended", "0.19.4 stable", "0.19.3 stable", "0.19.2 stable", "0.19.1 stable", "0.19.0 stable"}},
		{Quilt, "1.21.8", []string{"0.31.0-beta.4 beta", "0.31.0-beta.3 beta", "0.31.0-beta.2 beta", "0.31.0-beta.1 beta", "0.30.2-beta.1 beta",
			"0.30.1 stable recommended", "0.30.1-beta.4 beta", "0.30.1-beta.3 beta", "0.30.1-beta.2 beta", "0.30.1-beta.1 beta", "0.30.0 stable", "0.30.0-beta.8 beta"}},
		{NeoForge, "26.2", []string{"26.2.0.88 stable recommended", "26.2.0.87 stable", "26.2.0.86 stable", "26.2.0.0-beta beta"}},
		{NeoForge, "26.3", []string{"26.3.0.16-beta beta", "26.3.0.14-beta beta", "26.3.0.0-beta beta"}},
		{NeoForge, "1.21.1", []string{"21.1.251 stable recommended", "21.1.250 stable", "21.1.1 stable"}},
		{NeoForge, "1.21", []string{"21.0.167 stable recommended", "21.0.166 stable", "21.0.0-beta beta"}},
	}
	for _, tt := range tests {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%s/reversed=%v", tt.typeID, tt.mc, reverse), func(t *testing.T) {
				f := catalogFake(t, tt.typeID, reverse, false)
				got, err := f.sources().Builds(context.Background(), tt.typeID, tt.mc)
				if err != nil {
					t.Fatal(err)
				}
				if d := describeBuilds(got); !slices.Equal(d, tt.want) {
					t.Fatalf("got  %q\nwant %q", d, tt.want)
				}
				for _, b := range got {
					if err := b.Pin.Validate(); err != nil || b.Pin.MinecraftVersion != tt.mc || !strings.HasSuffix(pinDetail(b.Pin), " "+b.Version) {
						t.Errorf("build %s has pin %+v (%v)", b.Version, b.Pin, err)
					}
				}
			})
		}
	}
}

func TestBuildsErrors(t *testing.T) {
	tests := []struct {
		typeID, mc string
		kind       Kind
		msg        string
	}{
		{Fabric, "1.21.12", KindNotFound, "Fabric does not list Minecraft 1.21.12 as a stable version."},
		{Quilt, "26.3-rc-3", KindUnsupported, `Playkeeper runs Minecraft releases from 1.21 on, not "26.3-rc-3".`},
		{Purpur, "1.20.4", KindUnsupported, `Playkeeper runs Minecraft releases from 1.21 on, not "1.20.4".`},
		{Purpur, "1.21.9", KindNotFound, "Purpur does not have its build list for Minecraft 1.21.9."},
		{NeoForge, "1.21.8", KindNoVersions, "NeoForge has no build for Minecraft 1.21.8 that Playkeeper can install."},
		{"forge", "1.21.1", KindUnsupported, `Playkeeper does not install "forge" servers this way.`},
		{"paper", "26.2", KindUnsupported, `Playkeeper does not install "paper" servers this way.`},
	}
	for _, tt := range tests {
		t.Run(tt.typeID+"/"+tt.mc, func(t *testing.T) {
			f := catalogFake(t, tt.typeID, false, false)
			_, err := f.sources().Builds(context.Background(), tt.typeID, tt.mc)
			if e := wantKind(t, err, tt.kind); e.Msg != tt.msg {
				t.Errorf("got %q, want %q", e.Msg, tt.msg)
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"0.30.1", "0.30.1-beta.4", 1},
		{"0.31.0-beta.4", "0.31.0-beta.3", 1},
		{"0.31.0-beta.10", "0.31.0-beta.9", 1},
		{"0.30.2-beta.1", "0.30.1", 1},
		{"1.0.0-pre.1", "1.0.0-beta.9", 1},
		{"1.0.0-rc.1", "1.0.0-pre.2", 1},
		{"0.19.10", "0.19.9", 1},
		{"26.2.0.88", "26.2.0.9", 1},
		{"26.2.0.0-beta", "26.2.0.0", -1},
		{"26.3.0.0-beta", "26.2.0.88", 1},
		{"21.1.251", "21.1.251", 0},
		{"1.0", "1.0.0", -1},
	}
	for _, tt := range tests {
		if got := compareVersions(tt.a, tt.b); got != tt.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := compareVersions(tt.b, tt.a); got != -tt.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}

func TestNeoForgeMinecraft(t *testing.T) {
	tests := []struct {
		v, mc    string
		beta, ok bool
	}{
		{"21.1.251", "1.21.1", false, true},
		{"21.0.167", "1.21", false, true},
		{"21.0.0-beta", "1.21", true, true},
		{"21.11.45", "1.21.11", false, true},
		{"26.2.0.88", "26.2", false, true},
		{"26.1.2.109", "26.1.2", false, true},
		{"26.3.0.16-beta", "26.3", true, true},
		{"26.1.0.0-alpha.1+snapshot-1", "", false, false},
		{"0.25w14craftmine.3-beta", "", false, false},
		{"25.1.0.1", "", false, false},
		{"22.1.1", "", false, false},
		{"021.1.1", "", false, false},
		{"21.1.251/../x", "", false, false},
		{"21.1.251\n", "", false, false},
	}
	for _, tt := range tests {
		mc, beta, ok := neoforgeMinecraft(tt.v)
		if mc != tt.mc || beta != tt.beta || ok != tt.ok {
			t.Errorf("neoforgeMinecraft(%q) = %q, %v, %v; want %q, %v, %v", tt.v, mc, beta, ok, tt.mc, tt.beta, tt.ok)
		}
	}
}

func TestOfferedFamily(t *testing.T) {
	for v, want := range map[string]bool{
		"1.21": true, "1.21.11": true, "26.1.2": true, "26.3": true,
		"1.20.6": false, "26.3-rc-3": false, "26.4-snapshot-1": false, "24w14potato": false,
		"": false, "1": false, "1.21.1.1.1": false, "../1.21": false, "1.21 ": false,
	} {
		if got := offeredFamily(v); got != want {
			t.Errorf("offeredFamily(%q) = %v, want %v", v, got, want)
		}
	}
}
