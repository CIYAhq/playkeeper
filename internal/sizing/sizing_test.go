package sizing_test

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/sizing"
)

func name(r sizing.Recommendation) string { return r.Band.Label() + " " + string(r.Workload) }

func TestBandsCoverEveryGroupUpToTheMost(t *testing.T) {
	next := 1
	for _, b := range sizing.Bands() {
		if b.Min != next || b.Max < b.Min {
			t.Fatalf("band %s: want a band starting at %d", b.Label(), next)
		}
		next = b.Max + 1
	}
	if next-1 != sizing.MaxPlayers {
		t.Errorf("the bands end at %d, want %d", next-1, sizing.MaxPlayers)
	}
}

func TestTableHasEveryCombinationOnce(t *testing.T) {
	bands, workloads := sizing.Bands(), sizing.Workloads()
	table := sizing.Table()
	if len(table) != len(bands)*len(workloads) {
		t.Fatalf("the table has %d answers, want %d", len(table), len(bands)*len(workloads))
	}
	topics := []sizing.Topic{sizing.Memory, sizing.CPU, sizing.Disk, sizing.Network}
	for i, r := range table {
		b, w := bands[i/len(workloads)], workloads[i%len(workloads)]
		if r.Band != b || r.Workload != w {
			t.Fatalf("answer %d is for %s, want %s %s", i, name(r), b.Label(), w)
		}
		for _, players := range []int{b.Min, (b.Min + b.Max) / 2, b.Max} {
			if got, err := sizing.Recommend(w, players); err != nil || !reflect.DeepEqual(got, r) {
				t.Errorf("Recommend(%s, %d) = %+v, %v; want the table's answer for %s", w, players, got, err, name(r))
			}
		}
		if r.Title == "" || w.Label() == "" || w.Description() == "" || !strings.HasSuffix(r.Summary, ".") {
			t.Errorf("%s: missing title, labels or summary: %+v", name(r), r)
		}
		if len(r.Reasons) != len(topics) {
			t.Fatalf("%s: %d reasons, want %d", name(r), len(r.Reasons), len(topics))
		}
		for j, reason := range r.Reasons {
			if reason.Topic != topics[j] || reason.Topic.Label() == "" || reason.Value == "" || reason.Code == "" || len(reason.Params) == 0 || !strings.HasSuffix(reason.Text, ".") {
				t.Errorf("%s: reason %d = %+v, want a complete %s reason", name(r), j, reason, topics[j])
			}
		}
	}
}

func TestTableSizes(t *testing.T) {
	want := map[sizing.Workload][]sizing.Machine{
		sizing.Vanilla: {{MemoryGB: 4, Cores: 2, DiskGB: 40}, {MemoryGB: 6, Cores: 2, DiskGB: 40}, {MemoryGB: 8, Cores: 4, DiskGB: 80}, {MemoryGB: 12, Cores: 4, DiskGB: 160}},
		sizing.AddOns:  {{MemoryGB: 6, Cores: 2, DiskGB: 40}, {MemoryGB: 8, Cores: 4, DiskGB: 80}, {MemoryGB: 12, Cores: 4, DiskGB: 160}, {MemoryGB: 16, Cores: 6, DiskGB: 240}},
		sizing.Modpack: {{MemoryGB: 12, Cores: 4, DiskGB: 40}, {MemoryGB: 24, Cores: 4, DiskGB: 80}, {MemoryGB: 24, Cores: 6, DiskGB: 160}, {MemoryGB: 24, Cores: 8, DiskGB: 320}},
	}
	for _, w := range sizing.Workloads() {
		for i, b := range sizing.Bands() {
			r, err := sizing.Recommend(w, b.Max)
			if err != nil || r.Machine != want[w][i] {
				t.Errorf("%s %s: %+v, %v; want %+v", b.Label(), w, r.Machine, err, want[w][i])
			}
		}
	}
}

// shrinks names what a recommends less of than b, if anything.
func shrinks(a, b sizing.Recommendation) string {
	switch {
	case a.MemoryGB < b.MemoryGB:
		return "memory"
	case a.BudgetMB < b.BudgetMB:
		return "memory budget"
	case a.Cores < b.Cores:
		return "cores"
	case a.DiskGB < b.DiskGB:
		return "disk"
	case b.SecondServer && !a.SecondServer && a.MemoryGB == b.MemoryGB:
		return "room for a second server"
	}
	return ""
}

func TestMoreNeverRecommendsLess(t *testing.T) {
	workloads := sizing.Workloads()
	for wi, w := range workloads {
		prev, err := sizing.Recommend(w, 1)
		if err != nil {
			t.Fatal(err)
		}
		for players := 1; players <= sizing.MaxPlayers+5; players++ {
			r, err := sizing.Recommend(w, players)
			if err != nil {
				t.Fatal(err)
			}
			if what := shrinks(r, prev); what != "" {
				t.Errorf("%s: %d players get less %s than %d", w, players, what, players-1)
			}
			if wi > 0 {
				lighter, _ := sizing.Recommend(workloads[wi-1], players)
				if what := shrinks(r, lighter); what != "" {
					t.Errorf("%d players: %s gets less %s than %s", players, w, what, workloads[wi-1])
				}
			}
			prev = r
		}
	}
}

func TestBudgetsArePlaykeepersOwn(t *testing.T) {
	offered, _, _ := minecraft.MemoryOptions(1 << 30)
	for _, r := range sizing.Table() {
		if !slices.Contains(offered, r.BudgetMB) {
			t.Errorf("%s: %d MB is not a budget Playkeeper offers (%v)", name(r), r.BudgetMB, offered)
		}
		if reported := sizing.ReportedMemoryMB(r.MemoryGB); !minecraft.ValidBudget(r.BudgetMB, reported) {
			t.Errorf("%s: Playkeeper wouldn't offer %d MB on a %d GB VPS (%d MB reported, %d MB kept back)",
				name(r), r.BudgetMB, r.MemoryGB, reported, minecraft.HostReserveMB)
		}
		p := r.Reasons[0].Params
		if p["budgetMB"] != r.BudgetMB || p["heapMB"] != minecraft.HeapMB(r.BudgetMB) || p["memoryGB"] != r.MemoryGB {
			t.Errorf("%s: memory reason params %v don't match the budget %d MB", name(r), p, r.BudgetMB)
		}
		spare := r.MemoryGB*1024 - r.BudgetMB - p["systemMB"]
		switch {
		case spare < 0:
			t.Errorf("%s: %d MB for the world and %d MB for the system don't fit %d GB", name(r), r.BudgetMB, p["systemMB"], r.MemoryGB)
		case spare >= 1024 && p["spareMB"] != spare:
			t.Errorf("%s: %d MB spare, but the reason says %d", name(r), spare, p["spareMB"])
		case spare < 1024 && strings.Contains(r.Reasons[0].Text, "spare"):
			t.Errorf("%s: only %d MB spare, but the reason says %q", name(r), spare, r.Reasons[0].Text)
		}
	}
}

func TestMemoryFollowsPublicGuidance(t *testing.T) {
	budget := func(w sizing.Workload, players int) int {
		mb, err := sizing.SuggestMemory(w, players)
		if err != nil {
			t.Fatal(err)
		}
		return mb
	}
	for players := 1; players <= sizing.MaxPlayers; players++ {
		// Minecraft Wiki, Server/Requirements and Server/Requirements/Dedicated.
		wiki, dedicated := 1024, 0
		switch {
		case players > 20:
			wiki, dedicated = 4096, 6144
		case players >= 15:
			wiki, dedicated = 4096, 5120
		case players > 10:
			wiki = 4096
		case players > 4:
			wiki = 2048
		}
		if mb := budget(sizing.Vanilla, players); minecraft.HeapMB(mb) < wiki || mb < dedicated {
			t.Errorf("%d players on Vanilla: %d MB, want a heap of %d MB and a budget of %d MB at least", players, mb, wiki, dedicated)
		}
		// PaperMC: at least 6 GB "no matter how few players", from 11 here.
		if heap := minecraft.HeapMB(budget(sizing.AddOns, players)); players > 10 && heap < 6144 {
			t.Errorf("%d players with add-ons: a %d MB heap, want 6144 MB at least", players, heap)
		}
		// All the Mods: at least 6 GB, 12 GB or more for multiple players, at most 16 GB.
		atm := 6144
		if players > 4 {
			atm = 12000
		}
		if heap := minecraft.HeapMB(budget(sizing.Modpack, players)); heap < atm || heap > 16384 {
			t.Errorf("%d players on a modpack: a %d MB heap, want %d to 16384 MB", players, heap, atm)
		}
	}
}

type answer struct{ value, text string }

func checkAnswer(t *testing.T, r sizing.Recommendation, title, summary string, want []answer) {
	t.Helper()
	if r.Title != title || r.Summary != summary {
		t.Errorf("%s: %q / %q, want %q / %q", name(r), r.Title, r.Summary, title, summary)
	}
	for i, w := range want {
		if got := (answer{r.Reasons[i].Value, r.Reasons[i].Text}); got != w {
			t.Errorf("%s: %s reason %q, want %q", name(r), r.Reasons[i].Topic, got, w)
		}
	}
}

func TestDefaultAnswerMatchesTheDesign(t *testing.T) {
	r, err := sizing.Recommend(sizing.Vanilla, 5)
	if err != nil {
		t.Fatal(err)
	}
	checkAnswer(t, r, "A VPS with 6 GB of memory", "For 5–10 friends on Vanilla or Paper, with room for backups.", []answer{
		{"6 GB", "4 GB for the world, 2 GB for Ubuntu, Docker and Playkeeper."},
		{"2 fast cores", "Minecraft runs the world on one core, so fast beats many."},
		{"40 GB SSD", "Worlds, a week of backups and room to explore."},
		{"Any VPS will do", "Each player uses well under 1 Mbit/s."},
	})
	r, err = sizing.Recommend(sizing.Modpack, 8)
	if err != nil {
		t.Fatal(err)
	}
	checkAnswer(t, r, "A VPS with 24 GB of memory", "For 5–10 friends on a big modpack, with room for backups and a second small server.", []answer{
		{"24 GB", "16 GB for the world, 2 GB for Ubuntu, Docker and Playkeeper, and 6 GB to spare."},
		{"4 fast cores", "Mods keep extra cores busy, but the world still runs on one, so fast beats many. Prefer dedicated (not shared) cores."},
		{"80 GB SSD", "Worlds, a week of backups and room to explore."},
	})
}

func TestSuggestMemoryIsTheTablesBudget(t *testing.T) {
	for _, w := range sizing.Workloads() {
		for players := -1; players <= sizing.MaxPlayers+10; players++ {
			mb, err := sizing.SuggestMemory(w, players)
			r, _ := sizing.Recommend(w, players)
			if err != nil || mb != r.BudgetMB {
				t.Errorf("SuggestMemory(%s, %d) = %d, %v; want %d", w, players, mb, err, r.BudgetMB)
			}
		}
	}
}

func TestPlayersForUndoesSuggestMemory(t *testing.T) {
	offered, _, _ := minecraft.MemoryOptions(1 << 30)
	for _, w := range sizing.Workloads() {
		for players := 1; players <= sizing.MaxPlayers; players++ {
			mb, _ := sizing.SuggestMemory(w, players)
			if n, err := sizing.PlayersFor(w, mb); err != nil || n < players {
				t.Errorf("%s: %d MB is suggested for %d players, but PlayersFor says %d (%v)", w, mb, players, n, err)
			}
		}
		smallest, _ := sizing.SuggestMemory(w, 1)
		for _, mb := range offered {
			n, err := sizing.PlayersFor(w, mb)
			switch {
			case err != nil:
				t.Errorf("PlayersFor(%s, %d): %v", w, mb, err)
			case n == 0 && mb >= smallest:
				t.Errorf("PlayersFor(%s, %d) = 0, but %d MB is suggested for 1 player", w, mb, smallest)
			case n > 0:
				if s, _ := sizing.SuggestMemory(w, n); s > mb {
					t.Errorf("PlayersFor(%s, %d) = %d, but %d players are suggested %d MB", w, mb, n, n, s)
				}
			}
		}
	}
	for _, c := range []struct {
		w          sizing.Workload
		mb, player int
	}{
		{sizing.Vanilla, 1536, 0}, {sizing.Vanilla, 2048, 4}, {sizing.Vanilla, 3072, 4}, {sizing.Vanilla, 4096, 10}, {sizing.Vanilla, 8192, 40},
		{sizing.AddOns, 3072, 0}, {sizing.AddOns, 12288, 40}, {sizing.Modpack, 12288, 4}, {sizing.Modpack, 16384, 40},
	} {
		if n, _ := sizing.PlayersFor(c.w, c.mb); n != c.player {
			t.Errorf("PlayersFor(%s, %d) = %d, want %d", c.w, c.mb, n, c.player)
		}
	}
}

func TestSeveralServersAddUp(t *testing.T) {
	for _, r := range sizing.Table() {
		m, err := sizing.ForServers([]sizing.Server{{Workload: r.Workload, Players: r.Band.Max}})
		if err != nil || m != r.Machine {
			t.Errorf("one %s server: %+v, %v; want %+v", name(r), m, err, r.Machine)
		}
		with, err := sizing.ForServers([]sizing.Server{{Workload: r.Workload, Players: r.Band.Max}, {Workload: sizing.Vanilla, Players: 1}})
		if err != nil {
			t.Fatal(err)
		}
		if r.SecondServer != (with.MemoryGB == r.MemoryGB) {
			t.Errorf("%s: SecondServer is %v, but a second small server needs %d GB", name(r), r.SecondServer, with.MemoryGB)
		}
		if with.Cores <= r.Cores || with.DiskGB < r.DiskGB {
			t.Errorf("%s: with a second server %+v, want more cores and at least the disk of %+v", name(r), with, r.Machine)
		}
	}
	m, err := sizing.ForServers([]sizing.Server{{Workload: sizing.Vanilla, Players: 8}, {Workload: sizing.Modpack, Players: 3}})
	if want := (sizing.Machine{MemoryGB: 16, Cores: 6, DiskGB: 80}); err != nil || m != want {
		t.Errorf("Paper for 8 and a modpack for 3: %+v, %v; want %+v", m, err, want)
	}
	var servers []sizing.Server
	var budgetMB int
	for i := range 12 {
		s := sizing.Server{Workload: sizing.Workloads()[i%3], Players: 1 + i*7}
		mb, _ := sizing.SuggestMemory(s.Workload, s.Players)
		prev, _ := sizing.ForServers(servers)
		servers, budgetMB = append(servers, s), budgetMB+mb
		m, err := sizing.ForServers(servers)
		if err != nil {
			t.Fatal(err)
		}
		if m.MemoryGB < prev.MemoryGB || m.Cores < prev.Cores || m.DiskGB < prev.DiskGB {
			t.Errorf("%d servers get less than %d: %+v < %+v", len(servers), len(servers)-1, m, prev)
		}
		if sizing.ReportedMemoryMB(m.MemoryGB)-minecraft.HostReserveMB < budgetMB || m.MemoryGB*1024-2048 < budgetMB {
			t.Errorf("%d servers: %d GB doesn't hold their %d MB and the system", len(servers), m.MemoryGB, budgetMB)
		}
	}
}

func TestErrorsHaveAKindAndAHint(t *testing.T) {
	var e *sizing.Error
	_, err := sizing.Recommend("bedrock", 5)
	if !errors.As(err, &e) || e.Kind != sizing.KindUnknownWorkload || e.Params["workload"] != "bedrock" ||
		!strings.Contains(err.Error(), `Choose "vanilla", "add-ons" or "modpack".`) {
		t.Errorf("unknown workload: %#v", err)
	}
	long := strings.Repeat("é", 100)
	if _, err := sizing.ParseWorkload(long); !errors.As(err, &e) || e.Params["workload"] != strings.Repeat("é", 40)+"…" {
		t.Errorf("long workload name: %v", err)
	}
	for _, servers := range [][]sizing.Server{nil, {{Workload: "", Players: 3}}} {
		_, err := sizing.ForServers(servers)
		want := sizing.KindNoServers
		if len(servers) > 0 {
			want = sizing.KindUnknownWorkload
		}
		if !errors.As(err, &e) || e.Kind != want || !strings.HasSuffix(err.Error(), ".") {
			t.Errorf("ForServers(%v): %v, want %s", servers, err, want)
		}
	}
	for _, f := range []func(sizing.Workload) error{
		func(w sizing.Workload) error { _, err := sizing.SuggestMemory(w, 1); return err },
		func(w sizing.Workload) error { _, err := sizing.PlayersFor(w, 4096); return err },
	} {
		if err := f("Vanilla"); !errors.As(err, &e) || e.Kind != sizing.KindUnknownWorkload {
			t.Errorf("a workload's name is case-sensitive: %v", err)
		}
	}
}

func TestWorkloads(t *testing.T) {
	for _, w := range sizing.Workloads() {
		if got, err := sizing.ParseWorkload(string(w)); err != nil || got != w {
			t.Errorf("ParseWorkload(%q) = %q, %v", w, got, err)
		}
	}
	for _, c := range []struct {
		mods, plugins int
		want          sizing.Workload
	}{
		{0, 0, sizing.Vanilla}, {0, 19, sizing.Vanilla}, {0, 20, sizing.AddOns}, {1, 0, sizing.AddOns},
		{49, 40, sizing.AddOns}, {50, 0, sizing.Modpack}, {300, 2, sizing.Modpack},
	} {
		if got := sizing.WorkloadFor(c.mods, c.plugins); got != c.want {
			t.Errorf("WorkloadFor(%d mods, %d plugins) = %s, want %s", c.mods, c.plugins, got, c.want)
		}
	}
	if b := sizing.Bands()[1]; b.Label() != "5–10" || b.Key() != "5-10" {
		t.Errorf("band labels: %q, %q", b.Label(), b.Key())
	}
}

func ExampleForServers() {
	m, _ := sizing.ForServers([]sizing.Server{
		{Workload: sizing.Vanilla, Players: 8},
		{Workload: sizing.Modpack, Players: 3},
	})
	fmt.Printf("%d GB, %d cores, %d GB SSD\n", m.MemoryGB, m.Cores, m.DiskGB)
	// Output: 16 GB, 6 cores, 80 GB SSD
}
