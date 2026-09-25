package addons

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/addons/hangar"
)

func sourcedNames(cards []Card) string {
	var out []string
	for _, c := range cards {
		out = append(out, c.Name+"@"+c.Source.Name())
	}
	return strings.Join(out, ", ")
}

func TestBrowseMergesSourcesWithoutDuplicates(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")

	res, err := l.Browse(context.Background(), srv, BrowseQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want := "VeinMiner@Modrinth, Simple Voice Chat@Modrinth, Chunky@Modrinth, JourneyMap@Modrinth, WorldEdit@Modrinth, " +
		"ViaBackwards@Hangar, ViaVersion@Hangar, ViaRewind@Hangar, Geyser@Hangar"
	if got := sourcedNames(res.Cards); got != want || !res.More || len(res.Unanswered) != 0 {
		t.Errorf("cards %s, more %v, unanswered %+v", got, res.More, res.Unanswered)
	}
	m, h := f.sentTo("modrinth", "/v2/search"), f.sentTo("hangar", "/api/v1/projects")
	if len(m) != 1 || m[0].query.Get("index") != "downloads" || m[0].query.Get("limit") != "12" || strings.Contains(m[0].query.Get("facets"), "categories:worldgen") {
		t.Errorf("Modrinth got %+v", m)
	}
	if len(h) != 1 || h[0].query.Get("sort") != "-downloads" || h[0].query.Get("limit") != "12" || h[0].query.Has("category") {
		t.Errorf("Hangar got %+v", h)
	}

	res, err = l.Browse(context.Background(), srv, BrowseQuery{Text: "via", Sort: "relevance", Page: 1})
	if err != nil {
		t.Fatal(err)
	}
	want = "VeinMiner@Modrinth, ViaBackwards@Hangar, Simple Voice Chat@Modrinth, ViaVersion@Hangar, Chunky@Modrinth, " +
		"ViaRewind@Hangar, JourneyMap@Modrinth, WorldEdit@Modrinth, Geyser@Hangar"
	if got := sourcedNames(res.Cards); got != want {
		t.Errorf("relevance takes turns: %s", got)
	}
	if m := f.sentTo("modrinth", "/v2/search"); m[1].query.Get("offset") != "12" || m[1].query.Get("query") != "via" {
		t.Errorf("page 2 asked Modrinth for %v", m[1].query)
	}
}

func TestBrowseCategoriesMapToEachSource(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")

	if _, err := l.Browse(context.Background(), srv, BrowseQuery{Category: "world"}); err != nil {
		t.Fatal(err)
	}
	m, h := f.sentTo("modrinth", "/v2/search"), f.sentTo("hangar", "/api/v1/projects")
	if !strings.Contains(m[0].query.Get("facets"), `["categories:worldgen"]`) || h[0].query.Get("category") != "world_management" {
		t.Errorf("Modrinth got %v, Hangar got %v", m[0].query, h[0].query)
	}

	res, err := l.Browse(context.Background(), srv, BrowseQuery{Category: "protection"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.sentTo("modrinth", "/v2/search")) != 1 || f.sentTo("hangar", "/api/v1/projects")[1].query.Get("category") != "protection" || len(res.Cards) != 5 {
		t.Errorf("a category only Hangar has must not search Modrinth: %s", sourcedNames(res.Cards))
	}

	if cats := Categories(Target{HangarPlatform: ""}); slices.Contains(cats, "protection") || !slices.Contains(cats, "optimization") {
		t.Errorf("mod servers list %v", cats)
	}
	for _, q := range []BrowseQuery{{Category: "cats"}, {Sort: "newest"}, {Page: -1}} {
		_, err := l.Browse(context.Background(), srv, q)
		wantKind(t, err, KindInvalid)
	}
}

func TestBrowseShowsWhatAnswered(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	f.setBusy(true)

	res, err := l.Browse(context.Background(), srv, BrowseQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Cards) != 5 || len(res.Unanswered) != 1 || res.Unanswered[0].Kind != KindRateLimited {
		t.Errorf("cards %s, unanswered %+v", sourcedNames(res.Cards), res.Unanswered)
	}

	l.Hangar = hangar.New(fetch.Options{BaseURL: "https://127.0.0.1:1/api/v1", HTTP: f.client, Now: l.Now})
	_, err = l.Browse(context.Background(), srv, BrowseQuery{})
	wantKind(t, err, KindRateLimited)
}

func TestInstalledCardMatchesAcrossSources(t *testing.T) {
	rec := Installed{Source: Hangar, ProjectID: "31", Name: "ViaVersion"}
	for _, c := range []Card{{Source: Hangar, ProjectID: "31", Name: "Renamed"}, {Source: Modrinth, ProjectID: "P1OZGk5p", Name: "Via-Version"}} {
		if !InstalledCard(c, []Installed{rec}) {
			t.Errorf("%+v is installed", c)
		}
	}
	if InstalledCard(Card{Source: Modrinth, ProjectID: "x", Name: ""}, []Installed{{Source: Hangar, ProjectID: "y"}}) {
		t.Error("nameless cards match only by id")
	}
}

func TestInstallReportsProgress(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	var events []Progress
	res, err := l.Install(context.Background(), srv, nil, InstallRequest{Source: Modrinth, Project: "viarewind", OnProgress: func(p Progress) { events = append(events, p) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].Step != -1 || events[0].Plan == nil || len(events[0].Plan.Steps) != 3 {
		t.Fatalf("the first event must carry the plan: %+v", events)
	}
	plan := events[0].Plan
	step := -1
	for _, e := range events[1:] {
		if e.Plan != plan || e.Step < step || e.Step > step+1 {
			t.Fatalf("events out of order: %+v", events)
		}
		if e.Step == step+1 {
			if step >= 0 && !events[slices.Index(events, e)-1].Verified {
				t.Fatalf("step %d moved on unverified", step)
			}
			step = e.Step
		}
		if e.Verified && e.Received != plan.Steps[e.Step].Size {
			t.Errorf("step %d verified at %d of %d bytes", e.Step, e.Received, plan.Steps[e.Step].Size)
		}
	}
	if last := events[len(events)-1]; !last.Verified || last.Step != 2 || len(res.Installed) != 3 {
		t.Errorf("last event %+v", last)
	}

	f2 := newFakes(t)
	l2 := f2.library()
	srv2 := newServer(t, "paper", "26.2")
	bad := f2.mfile("FaishMnD")
	f2.hook(bad.path, serveBytes(append([]byte("x"), bad.data[1:]...)))
	events = nil
	_, err = l2.Install(context.Background(), srv2, nil, InstallRequest{Source: Modrinth, Project: "viaversion", OnProgress: func(p Progress) { events = append(events, p) }})
	wantKind(t, err, KindHashMismatch)
	if last := events[len(events)-1]; last.Verified || last.Step != 0 {
		t.Errorf("a failed download must not look verified: %+v", last)
	}
}
