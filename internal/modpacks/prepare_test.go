package modpacks

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

func TestInstallReportsProgress(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "", "")
	ref := Ref{Source: addons.Modrinth, Project: "adrenaline"}
	p := mustPlan(t, l, srv, InstallRequest{Ref: ref})
	var downloads int
	for _, c := range p.Changes {
		if c.Origin == Download {
			downloads++
		}
	}
	var heard []Progress
	if _, err := l.Install(context.Background(), srv, nil, InstallRequest{Ref: ref, OnProgress: func(pr Progress) { heard = append(heard, pr) }}); err != nil {
		t.Fatal(err)
	}
	if downloads == 0 || len(heard) != downloads+1 {
		t.Fatalf("heard %d times for %d downloads: %v", len(heard), downloads, heard)
	}
	for i, pr := range heard {
		if pr != (Progress{Done: i, Total: downloads}) {
			t.Fatalf("progress %d = %+v, want %d of %d", i, pr, i, downloads)
		}
	}
}

func TestPreparedInstallDownloadsThePackOnce(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "", "")
	ref := Ref{Source: addons.Modrinth, Project: "adrenaline"}
	prep, err := l.PrepareInstall(context.Background(), srv, nil, InstallRequest{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	defer prep.Close()
	if prep.Plan.Requirements != (Requirements{Type: "fabric", MinecraftVersion: "26.2", LoaderVersion: "0.19.5"}) || !prep.Plan.Ready {
		t.Fatalf("plan: %+v", prep.Plan)
	}
	// The server software goes in between: start files and libraries,
	// which a pack never writes.
	for _, p := range []string{"fabric-server-launch.jar", "libraries/net/fabricmc/loader.jar"} {
		full := filepath.Join(srv.Dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("software"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	var last Progress
	prep.req.OnProgress = func(p Progress) { last = p }
	res, err := prep.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Record.Pack.VersionID != "EZaeTUP8" || len(res.Record.Files) == 0 || last.Total == 0 || last.Done != last.Total {
		t.Fatalf("record %+v, last progress %+v", res.Record.Pack, last)
	}
	archive := f.parse(str(f.mrpackOf("EZaeTUP8")["url"])).Path
	if n := count(f.served("cdn"), "GET "+archive); n != 1 {
		t.Errorf("the pack's archive was downloaded %d times, want once", n)
	}
	for _, file := range res.Record.Files {
		if _, err := os.Stat(filepath.Join(srv.Dir, filepath.FromSlash(file.Path))); err != nil {
			t.Errorf("recorded %s is not on the server: %v", file.Path, err)
		}
	}
}

func TestPreparedInstallRefusesAChangedFolder(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "", "")
	prep, err := l.PrepareInstall(context.Background(), srv, nil, InstallRequest{Ref: Ref{Source: addons.Modrinth, Project: "adrenaline"}})
	if err != nil {
		t.Fatal(err)
	}
	defer prep.Close()
	var mod string
	for _, c := range prep.Plan.Changes {
		if strings.HasPrefix(c.Path, "mods/") {
			mod = c.Path
			break
		}
	}
	full := filepath.Join(srv.Dir, filepath.FromSlash(mod))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("someone else's mod"), 0o640); err != nil {
		t.Fatal(err)
	}
	_, err = prep.Apply(context.Background())
	wantKind(t, err, addons.KindPlanChanged)
	entries, _ := os.ReadDir(filepath.Dir(full))
	if len(entries) != 1 {
		t.Errorf("the mods folder has %d files after a refused install, want only the user's", len(entries))
	}
}

func TestSearchCountsBundledMods(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	f.editVersion("BSg2ZS8u", func(v obj) {
		v["dependencies"] = []any{
			obj{"project_id": "AAAA0001", "dependency_type": "embedded"},
			obj{"project_id": "AAAA0002", "dependency_type": "embedded"},
			obj{"project_id": "AAAA0002", "dependency_type": "embedded"},
			obj{"project_id": "AAAA0003", "dependency_type": "required"},
			obj{"project_id": "../../x", "dependency_type": "embedded"},
		}
	})
	res, err := l.Search(context.Background(), Query{Source: addons.Modrinth})
	if err != nil {
		t.Fatal(err)
	}
	mods := map[string]int{}
	for _, c := range res.Cards {
		mods[c.Slug] = c.Mods
	}
	if mods["create_plus"] != 2 || mods["vanilla-perfected"] != 0 {
		t.Errorf("mod counts %v, want 2 for create_plus and none listed for vanilla-perfected", mods)
	}
	if q := f.lastQuery("modrinth"); !slices.Contains(jsonStrings(q.Get("ids")), "BSg2ZS8u") {
		t.Errorf("the counts were not asked for in one call of the newest versions: %v", q)
	}

	// A latest version for an older Minecraft version than the card names
	// isn't what a server from the card gets, so its count isn't shown.
	f.editVersion("BSg2ZS8u", func(v obj) { v["game_versions"] = []any{"1.20.1"} })
	older, err := l.Search(context.Background(), Query{Source: addons.Modrinth})
	if err != nil {
		t.Fatal(err)
	}
	if c := cardBySlug(t, older.Cards, "create_plus"); c.Mods != 0 || c.MinecraftVersions[0] == "1.20.1" {
		t.Errorf("create_plus shows %d mods for Minecraft %s", c.Mods, c.MinecraftVersions[0])
	}

	// Without the counts, the page still lists the packs.
	f.hook("modrinth /v2/versions", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	again, err := l.Search(context.Background(), Query{Source: addons.Modrinth})
	if err != nil || len(again.Cards) != len(res.Cards) {
		t.Fatalf("search without counts: %v, %d cards", err, len(again.Cards))
	}
}

func TestDetailNamesTheHeadlineMod(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	f.setProject(obj{"id": "CRTE0001", "slug": "create", "title": "Create", "project_type": "mod"})
	f.setProject(obj{"id": "FAPI0001", "slug": "fabric-api", "title": "Fabric API", "project_type": "mod"})
	f.setProject(obj{"id": "CRTA0001", "slug": "create-addition", "title": "Create Crafts & Additions", "project_type": "mod"})
	for _, id := range f.order {
		if str(f.versions[id]["project_id"]) == "t1tOiUHZ" {
			f.editVersion(id, func(v obj) {
				v["dependencies"] = []any{
					obj{"project_id": "FAPI0001", "dependency_type": "embedded"},
					obj{"project_id": "CRTE0001", "dependency_type": "embedded"},
					obj{"project_id": "CRTA0001", "dependency_type": "embedded"},
				}
			})
		}
	}
	d, err := l.Detail(context.Background(), addons.Modrinth, "create_plus")
	if err != nil {
		t.Fatal(err)
	}
	if d.Headline != "Create" || d.Mods != 3 {
		t.Errorf("headline %q with %d mods, want Create with 3", d.Headline, d.Mods)
	}
	v := Newest(d.Versions)
	if v == nil || v.Unsupported != nil || v.Mods != 3 {
		t.Errorf("newest version %+v", v)
	}
}

func TestNewestPrefersReleases(t *testing.T) {
	no := &addons.Notice{Kind: KindForge}
	vs := []Version{{ID: "a", Channel: "release", Unsupported: no}, {ID: "b", Channel: "beta"}, {ID: "c", Channel: "release"}, {ID: "d", Channel: "release"}}
	if v := Newest(vs); v == nil || v.ID != "c" {
		t.Errorf("newest = %+v, want c", v)
	}
	if v := Newest(vs[:2]); v == nil || v.ID != "b" {
		t.Errorf("newest without a release = %+v, want b", v)
	}
	if v := Newest(vs[:1]); v != nil {
		t.Errorf("newest of unsupported versions = %+v, want none", v)
	}
}

// Smooth Server published 1.2 for 26.1 minutes after 1.1 for 26.2; its card
// names 26.2, so a new server gets 1.1.
func TestNewestTakesTheNewestMinecraftVersion(t *testing.T) {
	vs := []Version{
		{ID: "1.2", Channel: "release", MinecraftVersion: "26.1"},
		{ID: "1.1", Channel: "release", MinecraftVersion: "26.2"},
		{ID: "1.0", Channel: "release", MinecraftVersion: "26.2"},
		{ID: "0.9", Channel: "release", MinecraftVersion: "1.21.11"},
	}
	if v := Newest(vs); v == nil || v.ID != "1.1" {
		t.Errorf("newest = %+v, want 1.1, the latest for 26.2", v)
	}
	betas := []Version{{ID: "b2", Channel: "beta", MinecraftVersion: "1.21.4"}, {ID: "b1", Channel: "beta", MinecraftVersion: "1.21.11"}}
	if v := Newest(betas); v == nil || v.ID != "b1" {
		t.Errorf("newest beta = %+v, want b1", v)
	}
	if v := Newest(append(betas, Version{ID: "r", Channel: "release", MinecraftVersion: "1.20.1"})); v == nil || v.ID != "r" {
		t.Errorf("a release still wins over newer betas: %+v", v)
	}
}

func TestContainsWord(t *testing.T) {
	for _, c := range []struct {
		s, word string
		want    bool
	}{
		{"Cobblemon Modpack", "Cobblemon", true},
		{"Create+", "create", true},
		{"All the Mods 10", "All the Mods", true},
		{"Recreate", "create", false},
		{"Creates", "create", false},
		{"Create2 Pack", "Create", false},
		{"Better Create", "Create", true},
		{"Creator create", "Create", true},
	} {
		if got := containsWord(c.s, c.word); got != c.want {
			t.Errorf("containsWord(%q, %q) = %v, want %v", c.s, c.word, got, c.want)
		}
	}
}

func count(ss []string, s string) int {
	n := 0
	for _, x := range ss {
		if x == s {
			n++
		}
	}
	return n
}
