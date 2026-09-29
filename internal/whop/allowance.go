package whop

import (
	"math"
	"strconv"
	"strings"
)

// Metadata keys Sell on Whop reads and writes on Whop.
const (
	// MetaServers and MetaMemoryGB on a plan say what a buyer of it may
	// create: how many servers, and how much memory between them in GB,
	// whole or half, such as "1" and "4". The blueprint's plans carry them.
	MetaServers  = "playkeeper_servers"
	MetaMemoryGB = "playkeeper_memory_gb"
	// MetaDashboard on a product is the address of the Playkeeper that
	// sells it. The store takes orders only once its products have one.
	MetaDashboard = "playkeeper_dashboard"
)

// PlanAllowance reads what a plan's metadata lets a buyer create: servers,
// and memory in MB. ok is false when the plan says nothing, or something
// that isn't a count of servers and a size in whole or half GB. The
// dashboard checks the bounds.
func PlanAllowance(m Metadata) (servers, memoryMB int, ok bool) {
	sv, gb := strings.TrimSpace(m[MetaServers]), strings.TrimSpace(m[MetaMemoryGB])
	if sv == "" || gb == "" {
		return 0, 0, false
	}
	n, err := strconv.Atoi(sv)
	if err != nil || n < 1 {
		return 0, 0, false
	}
	g, err := strconv.ParseFloat(gb, 64)
	if err != nil || math.IsNaN(g) || math.IsInf(g, 0) || g <= 0 || g > 1024 || g*2 != math.Trunc(g*2) {
		return 0, 0, false
	}
	return n, int(g * 1024), true
}

// WithDashboard returns meta with the dashboard's address set, or removed
// when addr is empty, and whether that changed anything. The other keys
// stay, since Whop replaces a product's metadata as a whole.
func WithDashboard(meta Metadata, addr string) (Metadata, bool) {
	out := make(Metadata, len(meta)+1)
	for k, v := range meta {
		out[k] = v
	}
	if addr == "" {
		_, had := out[MetaDashboard]
		delete(out, MetaDashboard)
		return out, had
	}
	changed := out[MetaDashboard] != addr
	out[MetaDashboard] = addr
	return out, changed
}
