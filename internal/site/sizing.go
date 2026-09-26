package site

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/CIYAhq/playkeeper/internal/sizing"
)

// The sizing guide's calculator, on /sizing and the landing page, comes
// from internal/sizing: the answer shown first, the table that works without
// JavaScript, and sizing-data.js, the answers the script shows for each
// choice, with the providers' plans that fit each.

// The answer the calculator shows first, as in the design.
const (
	sizingPlayers  = 5
	sizingWorkload = sizing.Vanilla
)

// SizingGuide is what the calculator's templates show.
type SizingGuide struct {
	Bands, Workloads []SizingChoice
	RunLabel         string
	Answer           SizingAnswer
	// First is the answer shown first, for the page's own words: its
	// friends at once, what they run and the memory the world gets.
	FirstBand, FirstRun string
	FirstWorldGB        int
	Rows                []SizingRow
	SystemGB            int
	// Example works "several servers add up" through for two servers.
	Example string
	Min     sizing.Machine
	Most    int
	// Modpacks is the most and least memory a big modpack wants, and
	// ModpackWorldGB what the smallest of those gives the world.
	ModpackMinGB, ModpackMaxGB, ModpackWorldGB int
	// Modded is the memory table for mods, by friends at once: a few mods
	// and a big modpack.
	Modded []SizingModdedRow
}

// SizingChoice is one of the calculator's options.
type SizingChoice struct {
	Key, Label, Description string
	Checked                 bool
}

// SizingAnswer is one recommendation as the page words it.
type SizingAnswer struct {
	Title    string         `json:"title"`
	Short    string         `json:"short"`
	Summary  string         `json:"summary"`
	Specs    string         `json:"specs"`
	Announce string         `json:"announce"`
	Reasons  []SizingReason `json:"reasons"`
	// For the providers' plans: what fits this answer.
	Fit      string `json:"fit"`
	MemoryGB int    `json:"memoryGB"`
	Cores    int    `json:"cores"`
}

// SizingReason is one line of an answer: memory, CPU, disk or network.
type SizingReason struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Text  string `json:"text"`
}

// SizingRow is a row of the table of every size.
type SizingRow struct {
	Band  string
	Cells []SizingCell
}

// SizingCell is one size in the table.
type SizingCell struct {
	ID, Memory, Cores, Disk string
}

// SizingModdedRow is a row of the memory table for mods.
type SizingModdedRow struct {
	Band            string
	AddOns, Modpack sizing.Recommendation
}

func sizingAnswer(r sizing.Recommendation) SizingAnswer {
	a := SizingAnswer{
		Title:    r.Title,
		Short:    fmt.Sprintf("%d GB of memory", r.MemoryGB),
		Summary:  r.Summary,
		Specs:    r.Reasons[1].Value + " · " + r.Reasons[2].Value,
		Fit:      fmt.Sprintf("Fits your answer, %s friends %s: %d GB of memory, %d fast cores", r.Band.Label(), sizingPhrase(r), r.MemoryGB, r.Cores),
		MemoryGB: r.MemoryGB,
		Cores:    r.Cores,
	}
	a.Announce = fmt.Sprintf("%s, %s and %s.", r.Title, r.Reasons[1].Value, r.Reasons[2].Value)
	for _, x := range r.Reasons {
		a.Reasons = append(a.Reasons, SizingReason{Label: x.Topic.Label(), Value: x.Value, Text: x.Text})
	}
	return a
}

// sizingPhrase is what the friends run, as the answer's summary words it in a
// sentence ("on a big modpack"), or its label when the summary doesn't.
func sizingPhrase(r sizing.Recommendation) string {
	if rest, ok := strings.CutPrefix(r.Summary, "For "+r.Band.Label()+" friends "); ok {
		if phrase, _, ok := strings.Cut(rest, ", with room"); ok {
			return phrase
		}
	}
	return "on " + r.Workload.Label()
}

// buildSizing works out the calculator and the answers for its script.
func buildSizing() (SizingGuide, map[string]map[string]SizingAnswer, error) {
	first, err := sizing.Recommend(sizingWorkload, sizingPlayers)
	if err != nil {
		return SizingGuide{}, nil, err
	}
	g := SizingGuide{
		RunLabel:     sizingWorkload.Label(),
		Answer:       sizingAnswer(first),
		FirstBand:    first.Band.Label(),
		FirstRun:     first.Workload.Label(),
		FirstWorldGB: first.BudgetMB / 1024,
		SystemGB:     first.Reasons[0].Params["systemMB"] / 1024,
		Min:          sizing.Machine{MemoryGB: sizing.MinMemoryGB, Cores: sizing.MinCores, DiskGB: sizing.MinFreeDiskGB},
		Most:         sizing.MaxPlayers,
		ModpackMinGB: 1 << 30,
	}
	for _, b := range sizing.Bands() {
		g.Bands = append(g.Bands, SizingChoice{Key: b.Key(), Label: b.Label(), Checked: b == first.Band})
	}
	for _, w := range sizing.Workloads() {
		g.Workloads = append(g.Workloads, SizingChoice{Key: string(w), Label: w.Label(), Description: w.Description(), Checked: w == first.Workload})
	}
	answers := map[string]map[string]SizingAnswer{}
	modded := map[string]*SizingModdedRow{}
	for _, r := range sizing.Table() {
		band := r.Band.Key()
		if answers[band] == nil {
			answers[band] = map[string]SizingAnswer{}
			g.Rows = append(g.Rows, SizingRow{Band: r.Band.Label()})
			g.Modded = append(g.Modded, SizingModdedRow{Band: r.Band.Label()})
			modded[band] = &g.Modded[len(g.Modded)-1]
		}
		answers[band][string(r.Workload)] = sizingAnswer(r)
		last := &g.Rows[len(g.Rows)-1]
		last.Cells = append(last.Cells, SizingCell{
			ID:     "size-" + band + "-" + string(r.Workload),
			Memory: fmt.Sprintf("%d GB", r.MemoryGB),
			Cores:  fmt.Sprintf("%d cores", r.Cores),
			Disk:   fmt.Sprintf("%d GB disk", r.DiskGB),
		})
		switch r.Workload {
		case sizing.AddOns:
			modded[band].AddOns = r
		case sizing.Modpack:
			modded[band].Modpack = r
			g.ModpackMinGB = min(g.ModpackMinGB, r.MemoryGB)
			g.ModpackMaxGB = max(g.ModpackMaxGB, r.MemoryGB)
		case sizing.Vanilla:
		default:
			return SizingGuide{}, nil, fmt.Errorf("the sizing guide has no column for %q", r.Workload)
		}
	}
	g.ModpackWorldGB = g.Modded[0].Modpack.BudgetMB / 1024
	g.Example, err = sizingExample(g.SystemGB)
	return g, answers, err
}

// sizingExample works "several servers add up" through for two servers.
func sizingExample(systemGB int) (string, error) {
	planned := []struct {
		what   string
		server sizing.Server
	}{
		{"Paper", sizing.Server{Workload: sizing.Vanilla, Players: 10}},
		{"a big modpack", sizing.Server{Workload: sizing.Modpack, Players: 4}},
	}
	var servers []sizing.Server
	var parts, sum []string
	total := systemGB
	for _, p := range planned {
		r, err := sizing.Recommend(p.server.Workload, p.server.Players)
		if err != nil {
			return "", err
		}
		servers = append(servers, p.server)
		parts = append(parts, fmt.Sprintf("%s for %s friends (%d GB for the world)", p.what, r.Band.Label(), r.BudgetMB/1024))
		sum = append(sum, fmt.Sprint(r.BudgetMB/1024))
		total += r.BudgetMB / 1024
	}
	m, err := sizing.ForServers(servers)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("For example, %s: %s + %d = %d GB, so a VPS with %d GB of memory, %d fast cores and %d GB SSD.",
		strings.Join(parts, " and "), strings.Join(sum, " + "), systemGB, total, m.MemoryGB, m.Cores, m.DiskGB), nil
}

// sizingPlan is a provider's plans, for the script to pick the one that fits
// an answer, as Provider.Fit does.
type sizingPlan struct {
	Name     string `json:"name"`
	CPUs     int    `json:"cpus"`
	MemoryGB int    `json:"memoryGB"`
}

// sizingData is sizing-data.js: every answer, by friends at once and what
// they run, and each provider's plans.
func sizingData(answers map[string]map[string]SizingAnswer) ([]byte, error) {
	plans := map[string][]sizingPlan{}
	for _, p := range providers {
		for _, pl := range p.Plans {
			plans[p.Name] = append(plans[p.Name], sizingPlan{Name: pl.Name, CPUs: pl.CPUs, MemoryGB: pl.MemoryGB})
		}
	}
	js, err := json.Marshal(map[string]any{"answers": answers, "plans": plans})
	if err != nil {
		return nil, err
	}
	return []byte("// The sizing guide's answers, by friends at once and what they run, from internal/sizing.\n" +
		"window.playkeeperSizing = " + asciiJSON(string(js)) + ";\n"), nil
}

// asciiJSON escapes everything past ASCII in JSON, so the script reads the
// same whatever character set it is served with.
func asciiJSON(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x80 {
			b.WriteRune(r)
			continue
		}
		for _, u := range utf16.Encode([]rune{r}) {
			fmt.Fprintf(&b, `\u%04x`, u)
		}
	}
	return b.String()
}
