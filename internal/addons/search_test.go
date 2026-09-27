package addons

import (
	"context"
	"strings"
	"testing"
	"time"
)

func cardNames(r *Results) string {
	var names []string
	for _, c := range r.Cards {
		names = append(names, c.Name)
	}
	return strings.Join(names, ", ")
}

func TestSearchModrinthForPaper(t *testing.T) {
	f := newFakes(t)
	res, err := f.library().Search(context.Background(), newServer(t, "paper", "26.2"), Query{Source: Modrinth, Text: "  chunky ", Sort: "downloads", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	reqs := f.sentTo("modrinth", "/v2/search")
	if len(reqs) != 1 {
		t.Fatalf("%d search requests, want 1", len(reqs))
	}
	q := reqs[0].query
	facets := `[["categories:paper","categories:spigot","categories:bukkit"],["versions:26.2"],["project_type:plugin"],["server_side:required","server_side:optional"]]`
	if q.Get("facets") != facets || q.Get("query") != "chunky" || q.Get("index") != "downloads" || q.Get("limit") != "5" || q.Has("offset") {
		t.Errorf("search sent %v", q)
	}
	if reqs[0].userAgent != wantUserAgent {
		t.Errorf("User-Agent %q, want %q", reqs[0].userAgent, wantUserAgent)
	}

	if res.Total != 4196 || res.Offset != 0 || res.Limit != 5 {
		t.Errorf("total %d, offset %d, limit %d", res.Total, res.Offset, res.Limit)
	}
	if got, want := cardNames(res), "VeinMiner, Simple Voice Chat, Chunky, JourneyMap, WorldEdit"; got != want {
		t.Fatalf("cards %s, want %s", got, want)
	}
	sameJSON(t, "Chunky's card", res.Cards[2], Card{
		Source: Modrinth, ProjectID: "fALzjamp", Slug: "chunky", Name: "Chunky", Author: "pop4959",
		Summary:    "Pre-generates chunks, quickly and efficiently",
		Categories: []string{"optimization", "utility", "worldgen"},
		License:    "GPL-3.0-only", Downloads: 18217036,
		IconURL: "https://cdn.modrinth.com/data/fALzjamp/e1954413665e57b7bae1feef44eda530270c7d47_96.webp",
		Updated: time.Date(2026, 7, 23, 4, 22, 27, 486544000, time.UTC),
		PageURL: "https://modrinth.com/plugin/chunky",
	})
	if l := res.Cards[1].License; l != "All Rights Reserved" {
		t.Errorf("Simple Voice Chat's licence %q", l)
	}
}

// The Fabric fixture has client-only mods (Sodium, Iris, Entity Culling);
// servers never see them.
func TestSearchModrinthLeavesOutClientOnlyMods(t *testing.T) {
	f := newFakes(t)
	res, err := f.library().Search(context.Background(), newServer(t, "fabric", "26.2"), Query{Source: Modrinth})
	if err != nil {
		t.Fatal(err)
	}
	q := f.sentTo("modrinth", "/v2/search")[0].query
	facets := `[["categories:fabric"],["versions:26.2"],["project_type:mod"],["server_side:required","server_side:optional"]]`
	if q.Get("facets") != facets || q.Get("index") != "relevance" || q.Get("limit") != "20" || q.Has("query") {
		t.Errorf("search sent %v", q)
	}
	if got, want := cardNames(res), "Fabric API, Cloth Config API"; got != want {
		t.Errorf("cards %s, want %s", got, want)
	}
	if res.Total != 12006 {
		t.Errorf("total %d", res.Total)
	}
	sameJSON(t, "Fabric API's card", res.Cards[0], Card{
		Source: Modrinth, ProjectID: "P7dR8mSH", Slug: "fabric-api", Name: "Fabric API", Author: "modmuss50",
		Summary:    "Lightweight and modular API providing common hooks and intercompatibility measures utilized by mods using the Fabric toolchain.",
		Categories: []string{"library"}, License: "Apache-2.0", Downloads: 260574358,
		IconURL: "https://cdn.modrinth.com/data/P7dR8mSH/icon.png",
		Updated: time.Date(2026, 9, 22, 20, 19, 44, 977899000, time.UTC),
		PageURL: "https://modrinth.com/mod/fabric-api",
	})
}

func TestSearchHangar(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "purpur", "26.2")
	res, err := l.Search(context.Background(), srv, Query{Source: Hangar})
	if err != nil {
		t.Fatal(err)
	}
	q := f.sentTo("hangar", "/api/v1/projects")[0].query
	if q.Get("platform") != "PAPER" || q.Get("version") != "26.2" || q.Get("sort") != "-downloads" || q.Get("limit") != "20" || q.Has("query") || q.Has("offset") {
		t.Errorf("search sent %v", q)
	}
	if got, want := cardNames(res), "ViaBackwards, ViaVersion, ViaRewind, WorldEdit, Geyser"; got != want {
		t.Fatalf("cards %s, want %s", got, want)
	}
	if res.Total != 891 {
		t.Errorf("total %d", res.Total)
	}
	sameJSON(t, "ViaBackwards' card", res.Cards[0], Card{
		Source: Hangar, ProjectID: "12", Slug: "ViaBackwards", Name: "ViaBackwards", Author: "ViaVersion",
		Summary:    "Allow Java Edition clients with older versions to connect to your Minecraft server",
		Categories: []string{"misc"}, License: "GPL", Downloads: 742460,
		IconURL: "https://hangarcdn.papermc.io/avatars/project/12.webp?v=1",
		Updated: time.Date(2026, 9, 25, 13, 3, 10, 227632000, time.UTC),
		PageURL: "https://hangar.papermc.io/ViaVersion/ViaBackwards",
	})
	if l := res.Cards[3].License; l != "GPL" {
		t.Errorf("WorldEdit's licence %q", l)
	}

	if _, err := l.Search(context.Background(), srv, Query{Source: Hangar, Text: "via", Offset: 20}); err != nil {
		t.Fatal(err)
	}
	q = f.sentTo("hangar", "/api/v1/projects")[1].query
	if q.Get("query") != "via" || q.Get("offset") != "20" || q.Has("sort") {
		t.Errorf("text search sent %v", q)
	}
}

func TestSearchRefusals(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	for _, tc := range []struct {
		name    string
		typ, mc string
		q       Query
		kind    Kind
	}{
		{"vanilla", "vanilla", "26.2", Query{Source: Modrinth}, KindNoAddons},
		{"unknown server type", "folia", "26.2", Query{Source: Modrinth}, KindUnknownServerType},
		{"Hangar for mods", "fabric", "26.2", Query{Source: Hangar}, KindSourceUnsupported},
		{"Hangar for Forge mods", "forge", "26.2", Query{Source: Hangar}, KindSourceUnsupported},
		{"unknown source", "paper", "26.2", Query{Source: "curseforge"}, KindInvalid},
		{"no Minecraft version", "paper", "", Query{Source: Modrinth}, KindInvalid},
		{"odd Minecraft version", "paper", `26.2"],["x`, Query{Source: Modrinth}, KindInvalid},
		{"long text", "paper", "26.2", Query{Source: Modrinth, Text: strings.Repeat("a", 101)}, KindInvalid},
		{"unknown sort", "paper", "26.2", Query{Source: Modrinth, Sort: "stars"}, KindInvalid},
		{"page too large", "paper", "26.2", Query{Source: Modrinth, Limit: 26}, KindInvalid},
		{"negative offset", "paper", "26.2", Query{Source: Modrinth, Offset: -1}, KindInvalid},
	} {
		_, err := l.Search(context.Background(), newServer(t, tc.typ, tc.mc), tc.q)
		if KindOf(err) != tc.kind {
			t.Errorf("%s: %v (kind %q), want kind %q", tc.name, err, KindOf(err), tc.kind)
		}
	}
	if n := len(f.sent("modrinth")) + len(f.sent("hangar")); n != 0 {
		t.Errorf("refused searches sent %d requests", n)
	}
}

// Modrinth's 429 becomes a notice with the wait, later searches wait
// without asking again, and Hangar is not affected.
func TestSearchWhenModrinthAsksToSlowDown(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	f.setBusy(true)
	_, err := l.Search(context.Background(), srv, Query{Source: Modrinth})
	e := wantKind(t, err, KindRateLimited)
	if e.Msg != "Modrinth asked Playkeeper to slow down." || e.Hint != "Try again in 42 seconds." || e.Params["seconds"] != "42" || e.Params["source"] != "Modrinth" {
		t.Errorf("notice %+v", e.Notice)
	}

	f.setBusy(false)
	_, err = l.Search(context.Background(), srv, Query{Source: Modrinth})
	if e := wantKind(t, err, KindRateLimited); e.Params["seconds"] != "42" {
		t.Errorf("second notice %+v", e.Notice)
	}
	if n := len(f.sent("modrinth")); n != 1 {
		t.Errorf("Modrinth got %d requests, want 1", n)
	}
	if _, err := l.Search(context.Background(), srv, Query{Source: Hangar}); err != nil {
		t.Errorf("Hangar: %v", err)
	}
}
