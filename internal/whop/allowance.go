package whop

import (
	"maps"
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
	// MetaDiskGB on a plan, optional, is the disk its buyer's servers may
	// use between them in whole GB; without it the hosting core gives the
	// default for the memory.
	MetaDiskGB = "playkeeper_disk_gb"
	// MetaDashboard on a product is the address of the Playkeeper that
	// sells it, and MetaBusiness the Whop business it marked the product
	// in. The store takes orders only once its products have both, for its
	// own business: a copy of a product in another business, as deploying
	// a blueprint makes, doesn't count.
	MetaDashboard = "playkeeper_dashboard"
	MetaBusiness  = "playkeeper_business"
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

// PlanDiskGB reads a plan's disk in whole GB from its metadata, 0 when it
// says nothing or something that isn't a size up to 100 TB.
func PlanDiskGB(m Metadata) int {
	n, err := strconv.Atoi(strings.TrimSpace(m[MetaDiskGB]))
	if err != nil || n < 1 || n > 100_000 {
		return 0
	}
	return n
}

// Seller is the address of the dashboard that sells a product of business,
// "" when none does. A marking without a business, as dashboards wrote
// before MetaBusiness, counts as the product's own.
func Seller(meta Metadata, business string) string {
	addr := strings.TrimSpace(meta[MetaDashboard])
	if b := strings.TrimSpace(meta[MetaBusiness]); addr == "" || b != "" && b != business {
		return ""
	}
	return addr
}

// WithSeller returns meta with the selling dashboard's address and business
// set, or both removed when addr is empty, and whether that changed
// anything. The other keys stay, since Whop replaces a product's metadata as
// a whole.
func WithSeller(meta Metadata, addr, business string) (Metadata, bool) {
	out := make(Metadata, len(meta)+2)
	maps.Copy(out, meta)
	if addr == "" {
		_, hadAddr := out[MetaDashboard]
		_, hadBusiness := out[MetaBusiness]
		delete(out, MetaDashboard)
		delete(out, MetaBusiness)
		return out, hadAddr || hadBusiness
	}
	changed := out[MetaDashboard] != addr || out[MetaBusiness] != business
	out[MetaDashboard], out[MetaBusiness] = addr, business
	return out, changed
}
