package minecraft

import (
	"fmt"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// Image is the Minecraft runtime container, pinned by digest. The image
// downloads Paper (and Paper downloads Mojang's server) from upstream at first
// start, only after the user has accepted the Minecraft EULA in Playkeeper.
const (
	Image    = "docker.io/itzg/minecraft-server@sha256:e8640538dac5d54c2838d57fa9641e735ad0cf2b71fb0e8a68da3b542a315749"
	ImageTag = "itzg/minecraft-server:2026.9.1-java25"
)

// versions lists the only server builds Playkeeper offers. Each is pinned to a
// Paper build and the SHA-256 PaperMC's Fill v3 API publishes for its jar, so a
// restore on another host runs byte-identical server software.
var versions = []api.CatalogEntry{
	{
		ID:               "paper-26.1.2",
		Label:            "Paper 26.1.2",
		MinecraftVersion: "26.1.2",
		PaperBuild:       74,
		JarSHA256:        "1d70b1dab9cf4a6de615209a536f3a45a2186240253c428213ce2188ab95e5f7",
		Java:             25,
		Recommended:      true,
		Notes:            "Recommended. Java Edition 26.1.x clients can join.",
	},
	{
		ID:               "paper-1.21.11",
		Label:            "Paper 1.21.11",
		MinecraftVersion: "1.21.11",
		PaperBuild:       132,
		JarSHA256:        "5ffef465eeeb5f2a3c23a24419d97c51afd7dbb4923ff42df9a3f58bba1ccfba",
		Java:             21,
		Notes:            "For friends or plugins still on 1.21.11.",
	},
}

func Versions() []api.CatalogEntry {
	out := make([]api.CatalogEntry, len(versions))
	copy(out, versions)
	return out
}

func LookupVersion(id string) (api.CatalogEntry, error) {
	for _, v := range versions {
		if v.ID == id {
			return v, nil
		}
	}
	return api.CatalogEntry{}, fmt.Errorf("unknown server version %q", id)
}

// LookupMinecraft finds the catalog entry that runs the given game version.
func LookupMinecraft(mcVersion string) (api.CatalogEntry, bool) {
	for _, v := range versions {
		if v.MinecraftVersion == mcVersion {
			return v, true
		}
	}
	return api.CatalogEntry{}, false
}

// HostReserveMB is memory kept free for the OS, Docker and Playkeeper.
const HostReserveMB = 768

// budgets are the memory budgets (container limit, MB) Playkeeper offers.
var budgets = []int{1536, 2048, 3072, 4096, 6144, 8192, 12288, 16384}

// MemoryOptions returns the budgets that fit a host with hostMB of RAM, the
// suggested default and the largest allowed budget.
func MemoryOptions(hostMB int) (options []int, recommended, max int) {
	max = hostMB - HostReserveMB
	for _, mb := range budgets {
		if mb <= max {
			options = append(options, mb)
		}
	}
	if len(options) == 0 {
		return nil, 0, max
	}
	recommended = options[0]
	for _, mb := range options {
		if float64(mb) <= float64(hostMB)*0.6 {
			recommended = mb
		}
	}
	return options, recommended, max
}

// ValidBudget reports whether mb is an offered budget that fits the host.
func ValidBudget(mb, hostMB int) bool {
	opts, _, _ := MemoryOptions(hostMB)
	for _, o := range opts {
		if o == mb {
			return true
		}
	}
	return false
}

// HeapMB is the Java heap (-Xms/-Xmx) for a memory budget. The container
// limit equals the budget; the JVM needs non-heap memory inside it.
func HeapMB(budgetMB int) int {
	overhead := budgetMB / 4
	if overhead < 512 {
		overhead = 512
	}
	return budgetMB - overhead
}
