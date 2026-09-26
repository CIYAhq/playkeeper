package modpacks

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

func cardSlugs(cs []Card) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Slug)
	}
	return out
}

func versionIDs(vs []Version) []string {
	out := []string{}
	for _, v := range vs {
		out = append(out, v.ID)
	}
	return out
}

func cardBySlug(t *testing.T, cs []Card, slug string) Card {
	t.Helper()
	for _, c := range cs {
		if c.Slug == slug {
			return c
		}
	}
	t.Fatalf("no card for %s", slug)
	return Card{}
}

func TestSearchModrinth(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	l := f.library()
	res, err := l.Search(ctx, Query{Source: addons.Modrinth})
	if err != nil {
		t.Fatal(err)
	}
	// Sodium Plus is for clients only and Aged only for Minecraft versions
	// Playkeeper does not run.
	wantList(t, "results", cardSlugs(res.Cards), "cobblemon-fabric", "vanilla-perfected", "the-pixelmon-modpack", "create_plus", "rso")
	if res.Total != 10091 || res.Offset != 0 || res.Limit != 20 {
		t.Errorf("page: %d from %d of %d", res.Limit, res.Offset, res.Total)
	}
	q := f.lastQueryAt("modrinth", "/v2/search")
	if q.Get("facets") != `[["project_type:modpack"],["categories:fabric","categories:quilt","categories:neoforge","categories:forge","categories:minecraft"],["server_side:required","server_side:optional"]]` ||
		q.Get("index") != "relevance" || q.Get("limit") != "20" || q.Has("query") {
		t.Errorf("query %v", q)
	}
	pixelmon := cardBySlug(t, res.Cards, "the-pixelmon-modpack")
	sameJSON(t, "pixelmon", pixelmon, Card{
		Card: addons.Card{
			Source: addons.Modrinth, ProjectID: "vwgtbO0y", Slug: "the-pixelmon-modpack", Name: "The Pixelmon Modpack", Author: "Pixelmon",
			Summary: pixelmon.Summary, Categories: []string{"adventure", "quests"}, License: "All Rights Reserved", Downloads: 2203252,
			IconURL: "https://cdn.modrinth.com/data/vwgtbO0y/e16493aab7b0a32a2c9920cb0d89ebe06c4b3726_96.webp",
			Updated: time.Date(2026, 9, 17, 8, 39, 6, 72455000, time.UTC), PageURL: "https://modrinth.com/modpack/the-pixelmon-modpack",
		},
		Types: []string{"neoforge", "forge"}, MinecraftVersions: []string{"1.21.1"},
	})
	rso := cardBySlug(t, res.Cards, "rso")
	wantList(t, "rso's Minecraft versions", rso.MinecraftVersions, "26.3", "26.2", "26.1.2", "26.1.1", "26.1", "1.21.11", "1.21.10",
		"1.21.9", "1.21.8", "1.21.7", "1.21.6", "1.21.5", "1.21.4", "1.21.3", "1.21.1", "1.21")
	if rso.Name != "红石生电优化【Redstone Survival Optimization】" || strings.Join(cardBySlug(t, res.Cards, "cobblemon-fabric").Categories, ",") != "adventure,lightweight,multiplayer" {
		t.Errorf("rso %q, cobblemon %q", rso.Name, cardBySlug(t, res.Cards, "cobblemon-fabric").Categories)
	}

	res, err = l.Search(ctx, Query{Source: addons.Modrinth, Text: " vanilla ", MinecraftVersion: "26.2", Sort: "downloads", Offset: 20, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	wantList(t, "results for 26.2", cardSlugs(res.Cards), "vanilla-perfected", "rso")
	q = f.lastQueryAt("modrinth", "/v2/search")
	if !strings.HasSuffix(q.Get("facets"), `,["versions:26.2"]]`) || q.Get("query") != "vanilla" || q.Get("index") != "downloads" ||
		q.Get("offset") != "20" || q.Get("limit") != "5" {
		t.Errorf("query %v", q)
	}
	res, err = l.Search(ctx, Query{Source: addons.Modrinth, Type: "quilt"})
	if err != nil || len(res.Cards) != 0 || !strings.Contains(f.lastQueryAt("modrinth", "/v2/search").Get("facets"), `["categories:quilt"]`) {
		t.Errorf("quilt: %v, %v", res, err)
	}

	asked := len(f.served("modrinth"))
	for _, q := range []Query{
		{Source: addons.Modrinth, Sort: "popular"},
		{Source: addons.Modrinth, Limit: 26},
		{Source: addons.Modrinth, Offset: -1},
		{Source: addons.Modrinth, Offset: 10001},
		{Source: addons.Modrinth, Type: "paper"},
		{Source: addons.Modrinth, MinecraftVersion: "1.20.1"},
		{Source: addons.Modrinth, MinecraftVersion: "26.2-pre1"},
		{Source: addons.Modrinth, Text: strings.Repeat("é", 101)},
		{Source: addons.Hangar},
	} {
		_, err := l.Search(ctx, q)
		if addons.KindOf(err) != addons.KindInvalid {
			t.Errorf("%+v: %v", q, err)
		}
	}
	if len(f.served("modrinth")) != asked {
		t.Error("a search Playkeeper refused reached Modrinth")
	}
	_, err = l.Search(ctx, Query{Source: addons.Modrinth, Type: "paper"})
	if e := wantKind(t, err, addons.KindInvalid); e.Msg != "Playkeeper runs packs on Fabric, Quilt, NeoForge, Forge and Vanilla servers." {
		t.Errorf("message %q", e.Msg)
	}
}

func TestSearchCurseForge(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	l := f.library()
	res, err := l.Search(ctx, Query{Source: CurseForge})
	if err != nil {
		t.Fatal(err)
	}
	// The Forge pack for Minecraft 1.20.1 and the pack for Minecraft 1.19.2
	// are left out; a pack whose files name no loader is a vanilla pack.
	wantList(t, "results", cardSlugs(res.Cards), "example-fabric-pack", "example-neoforge-pack", "example-untagged-pack")
	if res.Total != 812 {
		t.Errorf("total %d", res.Total)
	}
	q := f.lastQuery("curseforge")
	if q.Get("classId") != "4471" || q.Get("gameId") != "432" || q.Get("sortField") != "2" || q.Get("pageSize") != "20" || q.Has("modLoaderType") {
		t.Errorf("query %v", q)
	}
	fabric := cardBySlug(t, res.Cards, "example-fabric-pack")
	sameJSON(t, "fabric pack", fabric, Card{
		Card: addons.Card{
			Source: CurseForge, ProjectID: "9100001", Slug: "example-fabric-pack", Name: "Example Fabric Pack", Author: "example-author",
			Summary: fabric.Summary, Categories: []string{"Multiplayer"}, Downloads: 152340,
			IconURL: "https://media.forgecdn.net/avatars/thumbnails/900/1/256/256/logo.png",
			Updated: time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC), PageURL: "https://www.curseforge.com/minecraft/modpacks/example-fabric-pack",
		},
		Types: []string{"fabric"}, MinecraftVersions: []string{"26.3", "26.2"},
	})
	if u := cardBySlug(t, res.Cards, "example-untagged-pack"); strings.Join(u.Types, ",") != "vanilla" || strings.Join(u.MinecraftVersions, ",") != "1.21.4" {
		t.Errorf("untagged pack: %v %v", u.Types, u.MinecraftVersions)
	}

	// CurseForge only filters by loader together with a Minecraft version;
	// Playkeeper filters the rest.
	res, err = l.Search(ctx, Query{Source: CurseForge, Type: "fabric", MinecraftVersion: "26.2"})
	if q := f.lastQuery("curseforge"); err != nil || q.Get("gameVersion") != "26.2" || q.Get("modLoaderType") != "4" {
		t.Errorf("query %v, %v", q, err)
	}
	wantList(t, "Fabric 26.2 results", cardSlugs(res.Cards), "example-fabric-pack")
	res, err = l.Search(ctx, Query{Source: CurseForge, Type: "neoforge"})
	if err != nil || f.lastQuery("curseforge").Has("modLoaderType") {
		t.Fatal(err)
	}
	wantList(t, "NeoForge results", cardSlugs(res.Cards), "example-neoforge-pack")
	res, err = l.Search(ctx, Query{Source: CurseForge, Type: "forge", MinecraftVersion: "26.2"})
	if q := f.lastQuery("curseforge"); err != nil || q.Get("gameVersion") != "26.2" || q.Get("modLoaderType") != "1" {
		t.Errorf("query %v, %v", q, err)
	}
	wantList(t, "Forge 26.2 results", cardSlugs(res.Cards))
}

func TestVersions(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	l := f.library()
	vs, err := l.Versions(ctx, addons.Modrinth, "adrenaline", "")
	if err != nil {
		t.Fatal(err)
	}
	wantList(t, "Adrenaline", versionIDs(vs), "afEtFNbW", "EZaeTUP8", "mj1mTI1B", "Cm3CfkQm", "A93YaQYh", "wGteoJrN", "r5uSD3JV")
	sameJSON(t, "Adrenaline for 26.1.2", vs[2], Version{
		ID: "mj1mTI1B", Number: "26.5.0+mc26.1.2.fabric", Name: vs[2].Name, Channel: "release",
		Published: time.Date(2026, 9, 21, 21, 5, 27, 474175000, time.UTC), Size: 7038, Type: "fabric", MinecraftVersion: "26.1.2",
	})
	for _, v := range vs {
		if (v.Unsupported != nil) != (v.ID == "wGteoJrN") {
			t.Errorf("%s: unsupported %+v", v.ID, v.Unsupported)
		}
	}
	if u := vs[5]; u.MinecraftVersion != "1.20.1" || u.Unsupported.Kind != KindMinecraft ||
		u.Unsupported.Msg != "Adrenaline is for Minecraft 1.20.1, and Playkeeper runs Minecraft 1.21 and newer." {
		t.Errorf("1.20.1: %+v %+v", u, u.Unsupported)
	}

	vs, err = l.Versions(ctx, addons.Modrinth, "vanilla-perfected", "26.1.2")
	if err != nil || f.lastQuery("modrinth").Get("game_versions") != `["26.1.2"]` {
		t.Fatal(err)
	}
	wantList(t, "Vanilla Perfected for 26.1.2", versionIDs(vs), "zCNpmrT6", "jmYEuzNA", "1nywXwlh")

	vs, err = l.Versions(ctx, addons.Modrinth, "create_plus", "")
	if err != nil {
		t.Fatal(err)
	}
	wantList(t, "Create+", versionIDs(vs), "BSg2ZS8u", "3XKDXorU", "OirSzesD")
	if v := vs[2]; v.Type != "forge" || v.MinecraftVersion != "1.19.2" || v.Unsupported == nil || v.Unsupported.Kind != KindMinecraft ||
		v.Unsupported.Msg != "Create+ is for Minecraft 1.19.2, and Playkeeper runs Minecraft 1.21 and newer." {
		t.Errorf("Forge version for an old Minecraft: %+v", v)
	}
	if v := vs[0]; v.Channel != "alpha" || v.Type != "neoforge" || v.MinecraftVersion != "1.21.1" || v.Unsupported != nil {
		t.Errorf("NeoForge version: %+v", v)
	}

	// The server pack is not offered.
	vs, err = l.Versions(ctx, CurseForge, "9100001", "")
	if err != nil {
		t.Fatal(err)
	}
	wantList(t, "Example Fabric Pack", versionIDs(vs), "9200003", "9200002", "9200001")
	if v := vs[0]; v.Number != "Example Fabric Pack 2.1.0-alpha.1" || v.Name != "Example Fabric Pack 2.1.0-alpha.1.zip" || v.Channel != "alpha" ||
		v.Type != "fabric" || v.MinecraftVersion != "26.3" || v.Unsupported != nil {
		t.Errorf("CurseForge version: %+v", v)
	}

	for _, c := range []struct {
		source      addons.Source
		project, mc string
		kind        addons.Kind
	}{
		{addons.Modrinth, "adrenaline", "26.2-rc1", addons.KindInvalid},
		{addons.Modrinth, "../adrenaline", "", addons.KindInvalid},
		{addons.Modrinth, "no-such-pack", "", addons.KindNotFound},
		{addons.Hangar, "adrenaline", "", addons.KindInvalid},
		{CurseForge, "adrenaline", "", addons.KindInvalid},
	} {
		if _, err := l.Versions(ctx, c.source, c.project, c.mc); addons.KindOf(err) != c.kind {
			t.Errorf("%s %s %s: %v, want kind %q", c.source, c.project, c.mc, err, c.kind)
		}
	}
}

func TestDetail(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	l := f.library()
	d, err := l.Detail(ctx, addons.Modrinth, "adrenaline")
	if err != nil {
		t.Fatal(err)
	}
	if d.ProjectID != "BYN9yKrV" || d.Name != "Adrenaline" || d.License != "MIT" || d.PageURL != "https://modrinth.com/modpack/adrenaline" ||
		d.SourceURL != "https://github.com/skywardmc/adrenaline" || d.IssuesURL != "https://github.com/skywardmc/adrenaline/issues" ||
		d.WikiURL != "https://skywardmc.org/adrenaline" || d.Unavailable != nil || len(d.Versions) != 7 {
		t.Errorf("detail: %+v", d)
	}
	wantList(t, "types", d.Types, "fabric", "quilt")
	wantList(t, "categories", d.Categories, "lightweight", "multiplayer", "optimization")
	wantList(t, "Minecraft versions", d.MinecraftVersions, "26.3", "26.2", "26.1.2", "26.1.1", "26.1", "1.21.11", "1.21.10", "1.21.8",
		"1.21.7", "1.21.5", "1.21.4", "1.21.3", "1.21.1", "1.21")

	d, err = l.Detail(ctx, addons.Modrinth, "create_plus")
	if err != nil {
		t.Fatal(err)
	}
	wantList(t, "Create+ types", d.Types, "neoforge", "forge")
	wantList(t, "Create+ categories", d.Categories, "lightweight", "multiplayer", "technology", "adventure", "optimization")

	d, err = l.Detail(ctx, addons.Modrinth, "sodiumplus")
	if err != nil || d.Unavailable == nil || d.Unavailable.Kind != addons.KindClientOnly || d.Unavailable.Msg != "Sodium Plus is for the game client only, not for servers." {
		t.Errorf("client-only pack: %+v, %v", d, err)
	}

	d, err = l.Detail(ctx, CurseForge, "9100001")
	if err != nil || d.Name != "Example Fabric Pack" || d.SourceURL != "" || len(d.Versions) != 3 || d.Unavailable != nil {
		t.Errorf("CurseForge detail: %+v, %v", d, err)
	}
	f.cfChangeMod(9100001, func(m obj) { m["allowModDistribution"] = false })
	d, err = l.Detail(ctx, CurseForge, "9100001")
	if err != nil || d.Unavailable == nil || d.Unavailable.Kind != addons.KindExternal ||
		d.Unavailable.Msg != "Example Fabric Pack's author only allows downloading it through CurseForge's app, so Playkeeper cannot install it." {
		t.Errorf("pack its author keeps to CurseForge's app: %+v, %v", d.Unavailable, err)
	}

	_, err = l.Detail(ctx, addons.Modrinth, "no-such-pack")
	if e := wantKind(t, err, addons.KindNotFound); e.Msg != `Modrinth has no project "no-such-pack".` {
		t.Errorf("message %q", e.Msg)
	}
}

func TestLicenseAndLinks(t *testing.T) {
	for id, want := range map[string]string{
		"MIT": "MIT", "LicenseRef-All-Rights-Reserved": "All Rights Reserved", "LicenseRef-Unknown": "", "LicenseRef-unknown": "", "": "",
	} {
		if got := license(id); got != want {
			t.Errorf("license(%q) = %q, want %q", id, got, want)
		}
	}
	for s, want := range map[string]string{
		"https://github.com/skywardmc/adrenaline":         "https://github.com/skywardmc/adrenaline",
		"http://example.com":                              "",
		"javascript:alert(1)":                             "",
		"https://user:pass@example.com":                   "",
		"https:///nohost":                                 "",
		"https://example.com/" + strings.Repeat("a", 600): "",
	} {
		if got := link(s); got != want {
			t.Errorf("link(%q) = %q, want %q", s, got, want)
		}
	}
}
