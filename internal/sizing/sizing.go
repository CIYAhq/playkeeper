// Package sizing recommends the size of VPS to rent for Minecraft: memory,
// CPU cores, disk and network for how many friends play at once and what
// the server runs. It is the one source of truth for the sizing guide on
// playkeeper.io (cmd/sizing-guide writes the page from it), for the memory
// a new server is offered first, and for the smallest machine the installer
// accepts without a warning.
//
// Memory comes from Playkeeper's own budgets (internal/minecraft); the other
// numbers come from public guidance, cited where each is set. Every size is
// a starting point: Settings later suggests memory from real use.
package sizing

import (
	"fmt"
	"slices"
)

// The smallest machine Playkeeper installs on without a warning. The
// installer checks memory and free disk (internal/install); it is tested on
// 2 cores but does not count them.
const (
	MinCores      = 2
	MinMemoryGB   = 3
	MinFreeDiskGB = 5
)

// MaxPlayers is the most players at once the guide sizes for. Bigger groups
// get its biggest size as a start.
const MaxPlayers = 40

// Workload is what a server runs.
type Workload string

const (
	// Vanilla is the normal game, or Paper with a few plugins.
	Vanilla Workload = "vanilla"
	// AddOns is Paper with lots of plugins, or Fabric, Quilt or NeoForge
	// with a handful of mods.
	AddOns Workload = "add-ons"
	// Modpack is a big modpack: hundreds of mods, like All the Mods.
	Modpack Workload = "modpack"
)

// Workloads lists the workloads from lightest to heaviest.
func Workloads() []Workload { return []Workload{Vanilla, AddOns, Modpack} }

// ParseWorkload reads a workload from its name, such as "vanilla".
func ParseWorkload(name string) (Workload, error) {
	w := Workload(name)
	if _, err := profileFor(w); err != nil {
		return "", err
	}
	return w, nil
}

// WorkloadFor is the workload of a server with the given numbers of mods and
// plugins, for when they are known: any mods or 20 plugins make it AddOns,
// 50 mods a Modpack.
func WorkloadFor(mods, plugins int) Workload {
	switch {
	case mods >= modpackMods:
		return Modpack
	case mods > 0 || plugins >= manyPlugins:
		return AddOns
	}
	return Vanilla
}

// Band is a range of players online at the same time.
type Band struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

var bands = [...]Band{{1, 4}, {5, 10}, {11, 20}, {21, MaxPlayers}}

// Bands lists the guide's bands of players, smallest first.
func Bands() []Band { return slices.Clone(bands[:]) }

// bandIndex is the band for players at once. Fewer than one counts as one,
// more than MaxPlayers as MaxPlayers.
func bandIndex(players int) int {
	for i, b := range bands {
		if players <= b.Max {
			return i
		}
	}
	return len(bands) - 1
}

// Machine is a size of VPS. MemoryGB is the memory it is sold with; Linux
// reports a little less (ReportedMemoryMB).
type Machine struct {
	MemoryGB int `json:"memoryGB"`
	Cores    int `json:"cores"`
	DiskGB   int `json:"diskGB"`
}

// Recommendation is the guide's answer for a band of players and a workload.
type Recommendation struct {
	Band     Band     `json:"band"`
	Workload Workload `json:"workload"`
	Machine
	// BudgetMB is the memory budget to give the server (its container
	// limit): the "for the world" part of MemoryGB.
	BudgetMB int `json:"budgetMB"`
	// SecondServer reports whether the machine also has room for a small
	// second server (Vanilla for up to 4 players).
	SecondServer bool `json:"secondServer"`
	// Title and Summary head the answer on the guide's page.
	Title   string `json:"title"`
	Summary string `json:"summary"`
	// Reasons explain memory, CPU, disk and network, in that order.
	Reasons []Reason `json:"reasons"`
}

// Topic is the part of a machine a reason is about.
type Topic string

const (
	Memory  Topic = "memory"
	CPU     Topic = "cpu"
	Disk    Topic = "disk"
	Network Topic = "network"
)

// Reason explains one part of a recommendation. Value and Text are the
// guide's English; Code and Params are stable, for a UI that words them its
// own way (sizes in Params are in MB or GB as their names say).
type Reason struct {
	Topic  Topic          `json:"topic"`
	Value  string         `json:"value"`
	Text   string         `json:"text"`
	Code   string         `json:"code"`
	Params map[string]int `json:"params"`
}

// Error is a question the guide can't answer. Kind and Params are stable;
// Error() is a sentence with a hint.
type Error struct {
	Kind   string            `json:"kind"`
	Params map[string]string `json:"params,omitempty"`
	msg    string
}

func (e *Error) Error() string { return e.msg }

// Error kinds.
const (
	// KindUnknownWorkload: Params["workload"] is the name that was given.
	KindUnknownWorkload = "unknown_workload"
	// KindNoServers: ForServers was given an empty list.
	KindNoServers = "no_servers"
)

func unknownWorkload(w Workload) *Error {
	name := string(w)
	if r := []rune(name); len(r) > 40 {
		name = string(r[:40]) + "…"
	}
	msg := fmt.Sprintf("The sizing guide has no sizes for %q.", name)
	if name == "" {
		msg = "The sizing guide needs to know what the server runs."
	}
	return &Error{Kind: KindUnknownWorkload, Params: map[string]string{"workload": name},
		msg: fmt.Sprintf("%s Choose %q, %q or %q.", msg, Vanilla, AddOns, Modpack)}
}

// Recommend is the guide's answer for players at once running w. Players
// outside 1 to MaxPlayers count as the nearest of the two; Band says which
// band answered.
func Recommend(w Workload, players int) (Recommendation, error) {
	p, err := profileFor(w)
	if err != nil {
		return Recommendation{}, err
	}
	return recommend(w, p, bandIndex(players)), nil
}

// Table is the guide's answer for every band and workload, band by band,
// each band's workloads from lightest to heaviest.
func Table() []Recommendation {
	var t []Recommendation
	for i := range bands {
		for _, w := range Workloads() {
			p, _ := profileFor(w)
			t = append(t, recommend(w, p, i))
		}
	}
	return t
}

// SuggestMemory is the memory budget, in MB, to offer first for a new server
// running w for players at once. It is the budget the guide's table sizes
// the machine for, so a machine of the recommended size always offers it.
// On a smaller machine, offer the largest budget below it that fits.
func SuggestMemory(w Workload, players int) (int, error) {
	p, err := profileFor(w)
	if err != nil {
		return 0, err
	}
	return p.budgetsMB[bandIndex(players)], nil
}

// PlayersFor is how many players at once the guide sizes a memory budget
// for: the top of the biggest band whose suggestion the budget covers, or 0
// when it is below the suggestion for the smallest band of w.
func PlayersFor(w Workload, budgetMB int) (int, error) {
	p, err := profileFor(w)
	if err != nil {
		return 0, err
	}
	n := 0
	for i, b := range bands {
		if p.budgetsMB[i] <= budgetMB {
			n = b.Max
		}
	}
	return n, nil
}

// Server is one of the servers planned for a machine.
type Server struct {
	Workload Workload `json:"workload"`
	Players  int      `json:"players"`
}

// ForServers is the machine for several servers. Every server on the machine
// counts, running or not: Playkeeper keeps a stopped server's memory so it
// can always start. They add up:
//
//   - memory: every server's budget, plus the 2 GB left for Ubuntu, Docker
//     and Playkeeper once;
//   - cores: the busiest server's, plus one for each other server, which
//     runs its world on a core of its own;
//   - disk: every server's files, worlds and backups, plus the system once.
//
// Network needs no more than for one server: any VPS carries it.
func ForServers(servers []Server) (Machine, error) {
	if len(servers) == 0 {
		return Machine{}, &Error{Kind: KindNoServers, msg: "There are no servers to size. Add at least one, with its players and what it runs."}
	}
	var budgetMB, busiest int
	diskGB := float64(systemDiskGB)
	for _, s := range servers {
		p, err := profileFor(s.Workload)
		if err != nil {
			return Machine{}, err
		}
		i := bandIndex(s.Players)
		budgetMB += p.budgetsMB[i]
		busiest = max(busiest, p.cores[i])
		diskGB += p.serverDiskGB(bands[i])
	}
	return Machine{
		MemoryGB: memoryGBFor(budgetMB),
		Cores:    roundUp(float64(busiest+len(servers)-1), coreSizes, 4),
		DiskGB:   roundUp(diskGB, diskSizesGB, 160),
	}, nil
}

func recommend(w Workload, p profile, i int) Recommendation {
	band := bands[i]
	budget := p.budgetsMB[i]
	m := Machine{
		MemoryGB: memoryGBFor(budget),
		Cores:    p.cores[i],
		DiskGB:   roundUp(systemDiskGB+p.serverDiskGB(band), diskSizesGB, 160),
	}
	r := Recommendation{
		Band:         band,
		Workload:     w,
		Machine:      m,
		BudgetMB:     budget,
		SecondServer: fits(m.MemoryGB, budget+smallServerMB()),
	}
	r.Title = title(m)
	r.Summary = summary(w, band, r.SecondServer)
	r.Reasons = []Reason{
		memoryReason(m, budget),
		cpuReason(w, m.Cores),
		diskReason(m, p.worldGB(band)),
		networkReason(band),
	}
	return r
}
