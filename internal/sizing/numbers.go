package sizing

import "github.com/CIYAhq/playkeeper/internal/minecraft"

const (
	// systemMB is the part of a VPS's memory the guide leaves to everything
	// but Minecraft: what Linux keeps for itself, minecraft.HostReserveMB for
	// the operating system, Docker and Playkeeper, and room for the page
	// cache and backups.
	systemMB = 2048

	// reportedPercent is the share of a VPS's memory that Linux reports
	// (MemTotal), which Playkeeper's memory options are based on. The kernel
	// keeps kernelPercent for itself (a 3 GB KVM guest reports 2971 MB, 96.7%,
	// and bigger machines a larger share); the rest is room for providers
	// that hold back a little more.
	reportedPercent = 95
	kernelPercent   = 97
)

// crashKernelMB is the memory Ubuntu 24.10 and later, so 26.04 LTS, set
// aside for crash dumps: its installer turns them on for machines with at
// least 6 GB and 4 CPU threads, with the reservation below, which Linux
// then doesn't report
// (https://ubuntu.com/server/docs/how-to/software/kernel-crash-dump/).
// Cloud images, Debian and Ubuntu 24.04 and older set none aside unless
// asked to.
func crashKernelMB(memoryGB int) int {
	switch {
	case memoryGB < 6:
		return 0
	case memoryGB < 32:
		return 512
	case memoryGB < 64:
		return 1024
	case memoryGB < 128:
		return 2048
	}
	return 4096
}

// memorySizesGB, coreSizes and diskSizesGB are common VPS sizes, smallest
// first. Ubuntu suggests at least 25 GB of disk for a server that does more
// than boot (https://ubuntu.com/server/docs/reference/installation/system-requirements/);
// Debian needs less.
var (
	memorySizesGB = []int{3, 4, 6, 8, 12, 16, 24, 32, 48, 64}
	coreSizes     = []int{2, 4, 6, 8, 12, 16}
	diskSizesGB   = []int{40, 80, 160, 240, 320, 480, 640}
)

const (
	// systemDiskGB is Ubuntu's own 5 GB (system requirements, above; Debian
	// takes less) plus Docker, Playkeeper's Minecraft image, logs and
	// updates.
	systemDiskGB = 10
	// backupCopies is a week of daily backups.
	backupCopies = 7
	// backupShare is a backup's size next to the world it copies. Region
	// files already compress every chunk (region-file-compression,
	// https://minecraft.wiki/w/Server.properties), so gzip mostly removes the
	// padding around chunks.
	backupShare = 0.9
)

const (
	// Each player needs well under 1 Mbit/s: about 0.1 Mbit/s while playing
	// (40 players sent 4 Mbit/s in all) and 0.6 to 1 Mbit/s while sprinting
	// through new chunks
	// (https://www.spigotmc.org/threads/how-much-bandwidth-does-a-player-use.53415/page-3).
	playerAverageKbit = 100
	playerPeakMbit    = 1
)

// manyPlugins and modpackMods are rough lines, not from a source, where a
// few plugins become lots and a handful of mods becomes a big modpack. Most
// modpacks ask for at least 4 to 6 GB
// (https://docs.feed-the-beast.com/docs/support/Guides/Server/), more than
// AddOns starts with.
const (
	manyPlugins = 20
	modpackMods = 50
)

// profile is the guide's numbers for one workload, by band.
type profile struct {
	// budgetsMB are memory budgets; Java's heap is minecraft.HeapMB of each.
	budgetsMB [len(bands)]int
	// cores: Minecraft runs each world on one thread, so one fast core
	// matters most; since 1.14 it uses other cores for other work, "typically
	// three cores are used at most" (https://minecraft.wiki/w/Server/Requirements).
	// Paper generates new land on one thread below 4 cores and on half of
	// them from 4 up (chunk-system.worker-threads,
	// https://docs.papermc.io/paper/reference/global-configuration), so
	// groups that explore get 4 or more.
	cores [len(bands)]int
	// filesGB is the server's own files, which backups leave out: server
	// jars, libraries and mods, downloaded again on restore.
	filesGB float64
	// worldGBPerPlayer is how much world and add-on data each player adds
	// over months of play. Generated chunks take about 9.8 KiB each in
	// Minecraft 26.1 region files (measured by Playkeeper), so 0.4 GB is some
	// 43,000 chunks: an area about 3,300 blocks across for each player.
	worldGBPerPlayer float64
}

func profileFor(w Workload) (profile, error) {
	switch w {
	case Vanilla:
		return profile{
			// The Minecraft Wiki suggests giving the server 1 GB for up to 4
			// players, 2 GB for 5 to 10 and 4 GB for more
			// (https://minecraft.wiki/w/Server/Requirements), and on a
			// dedicated Linux server 5 GB for 15 to 20 and 6 GB for more
			// (https://minecraft.wiki/w/Server/Requirements/Dedicated). Each
			// budget covers that; Java's heap alone (1.5, 3, 4.5 and 6 GB)
			// covers the first page's.
			budgetsMB:        [...]int{2048, 4096, 6144, 8192},
			cores:            [...]int{2, 2, 4, 4},
			filesGB:          0.5,
			worldGBPerPlayer: 0.4,
		}, nil
	case AddOns:
		return profile{
			// Plugins and mods bring memory and threads of their own (map
			// renderers, block loggers, machines), so each band gets the
			// budget Vanilla gives the next, and extra cores sooner. From 11
			// players Java gets the 6 GB or more PaperMC recommends "no
			// matter how few players" (https://docs.papermc.io/paper/aikars-flags).
			// Their data (maps, logs) grows with the world.
			budgetsMB:        [...]int{4096, 6144, 8192, 12288},
			cores:            [...]int{2, 4, 4, 6},
			filesGB:          1,
			worldGBPerPlayer: 0.6,
		}, nil
	case Modpack:
		return profile{
			// All the Mods asks for a heap of at least 6 GB, 12 GB or more
			// for multiple players, and at most 16 GB
			// (https://allthemods.github.io/alltheguides/help/server/,
			// https://allthemods.github.io/alltheguides/help/server/amp/).
			// Java gets 6 GB for up to 4 friends and 12 GB from 5, from the
			// largest budget Playkeeper offers. Modded land takes far more
			// work and space per chunk, and packs add dimensions, so cores
			// start at 4.
			budgetsMB:        [...]int{8192, 16384, 16384, 16384},
			cores:            [...]int{4, 4, 6, 8},
			filesGB:          3,
			worldGBPerPlayer: 0.8,
		}, nil
	}
	return profile{}, unknownWorkload(w)
}

// worldGB is the world of a band's biggest group after months of play.
func (p profile) worldGB(b Band) float64 { return p.worldGBPerPlayer * float64(b.Max) }

// serverDiskGB is one server's disk: its files, its world and a week of
// backups.
func (p profile) serverDiskGB(b Band) float64 {
	return p.filesGB + p.worldGB(b)*(1+backupCopies*backupShare)
}

// smallServerMB is the budget of a small second server.
func smallServerMB() int {
	p, _ := profileFor(Vanilla)
	return p.budgetsMB[0]
}

// ReportedMemoryMB is the least memory Linux reports, as the guide assumes,
// on a VPS sold with memoryGB, whichever supported system it runs: with
// Ubuntu's crash dump memory set aside, too.
func ReportedMemoryMB(memoryGB int) int {
	mb := memoryGB * 1024
	return min(mb*reportedPercent/100, mb*kernelPercent/100-crashKernelMB(memoryGB))
}

// fits reports whether a VPS sold with memoryGB holds servers whose budgets
// add up to budgetMB: systemMB is left over, and Playkeeper offers them on
// the memory Linux reports (minecraft.MemoryOptions keeps HostReserveMB).
func fits(memoryGB, budgetMB int) bool {
	return memoryGB*1024-systemMB >= budgetMB &&
		ReportedMemoryMB(memoryGB)-minecraft.HostReserveMB >= budgetMB
}

// memoryGBFor is the smallest common VPS memory that fits budgetMB.
func memoryGBFor(budgetMB int) int {
	gb := 0
	for _, gb = range memorySizesGB {
		if fits(gb, budgetMB) {
			return gb
		}
	}
	for !fits(gb, budgetMB) {
		gb += 32
	}
	return gb
}

// roundUp is the smallest size of at least need, going on past the last
// size in steps.
func roundUp(need float64, sizes []int, step int) int {
	s := 0
	for _, s = range sizes {
		if float64(s) >= need {
			return s
		}
	}
	for float64(s) < need {
		s += step
	}
	return s
}
