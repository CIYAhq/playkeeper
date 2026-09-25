package sizing

import (
	"fmt"
	"math"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// Label is the workload as the guide's question offers it.
func (w Workload) Label() string {
	switch w {
	case Vanilla:
		return "Vanilla or Paper"
	case AddOns:
		return "Lots of plugins or a few mods"
	case Modpack:
		return "A big modpack"
	}
	return ""
}

// Description explains the workload under its label.
func (w Workload) Description() string {
	switch w {
	case Vanilla:
		return "The normal game, or Paper with a few plugins"
	case AddOns:
		return "Paper with many plugins, or Fabric or NeoForge"
	case Modpack:
		return "Hundreds of mods, like All the Mods"
	}
	return ""
}

func (w Workload) phrase() string {
	switch w {
	case Vanilla:
		return "on Vanilla or Paper"
	case AddOns:
		return "using lots of plugins or a few mods"
	case Modpack:
		return "on a big modpack"
	}
	return ""
}

// Label is the topic as the guide's answer names it.
func (t Topic) Label() string {
	switch t {
	case Memory:
		return "Memory"
	case CPU:
		return "CPU"
	case Disk:
		return "Disk"
	case Network:
		return "Network"
	}
	return ""
}

// Label is the band as the guide shows it, such as "5–10".
func (b Band) Label() string { return fmt.Sprintf("%d–%d", b.Min, b.Max) }

// Key names the band in addresses and element IDs, such as "5-10".
func (b Band) Key() string { return fmt.Sprintf("%d-%d", b.Min, b.Max) }

func title(m Machine) string { return fmt.Sprintf("A VPS with %d GB of memory", m.MemoryGB) }

func summary(w Workload, b Band, secondServer bool) string {
	room := "backups"
	if secondServer {
		room = "backups and a second small server"
	}
	return fmt.Sprintf("For %s friends %s, with room for %s.", b.Label(), w.phrase(), room)
}

func memoryReason(m Machine, budgetMB int) Reason {
	r := Reason{
		Topic:  Memory,
		Value:  fmt.Sprintf("%d GB", m.MemoryGB),
		Code:   "memory_split",
		Text:   fmt.Sprintf("%s for the world, %s for Ubuntu, Docker and Playkeeper", gbText(budgetMB), gbText(systemMB)),
		Params: map[string]int{"memoryGB": m.MemoryGB, "budgetMB": budgetMB, "heapMB": minecraft.HeapMB(budgetMB), "systemMB": systemMB},
	}
	if spareMB := m.MemoryGB*1024 - budgetMB - systemMB; spareMB >= 1024 {
		r.Code = "memory_spare"
		r.Params["spareMB"] = spareMB
		r.Text += ", and " + gbText(spareMB) + " to spare"
	}
	r.Text += "."
	return r
}

func cpuReason(w Workload, cores int) Reason {
	r := Reason{Topic: CPU, Value: fmt.Sprintf("%d fast cores", cores), Params: map[string]int{"cores": cores}}
	switch {
	case w == Modpack:
		r.Code, r.Text = "cpu_modpack", "Mods keep extra cores busy, but the world still runs on one, so fast beats many. Prefer dedicated (not shared) cores."
	case cores <= MinCores:
		r.Code, r.Text = "cpu_one_core", "Minecraft runs the world on one core, so fast beats many."
	case w == AddOns:
		r.Code, r.Text = "cpu_add_ons", "The world runs on one core, so fast beats many; plugins and mods keep the others busy."
	default:
		r.Code, r.Text = "cpu_explore", "The world runs on one core, so fast beats many; the others load new land as friends explore."
	}
	return r
}

func diskReason(m Machine, worldGB float64) Reason {
	return Reason{
		Topic:  Disk,
		Value:  fmt.Sprintf("%d GB SSD", m.DiskGB),
		Code:   "disk_week",
		Text:   "Worlds, a week of backups and room to explore.",
		Params: map[string]int{"diskGB": m.DiskGB, "worldMB": int(math.Round(worldGB * 1024)), "backups": backupCopies},
	}
}

func networkReason(b Band) Reason {
	mbPerHour := playerAverageKbit * 3600 / 8 / 1000
	return Reason{
		Topic:  Network,
		Value:  "Any VPS will do",
		Code:   "network_any",
		Text:   fmt.Sprintf("Each player uses well under %d Mbit/s.", playerPeakMbit),
		Params: map[string]int{"players": b.Max, "peakMbit": b.Max * playerPeakMbit, "mbPerHourPerPlayer": mbPerHour},
	}
}

// gbText writes a size in MB as GB, such as "4 GB" or "1.5 GB".
func gbText(mb int) string {
	if mb%1024 == 0 {
		return fmt.Sprintf("%d GB", mb/1024)
	}
	return fmt.Sprintf("%.1f GB", float64(mb)/1024)
}
