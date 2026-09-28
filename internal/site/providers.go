package site

import (
	"fmt"
	"strings"
)

// Provider is a VPS provider the site suggests, with the plans it sells as
// the provider names them. Its cards show plan names and "See today's
// price", never prices; only the cost guide shows what a plan cost, dated
// with checkedProviders.
type Provider struct {
	Name string
	// Regions says where it has machines, in a few words.
	Regions string
	// URL is the page listing its plans.
	URL string
	// Referral says whether URL earns Playkeeper a commission. The
	// disclosure under the providers follows it.
	Referral bool
	Plans    []Plan
}

// Plan is one size a provider sells.
type Plan struct {
	Name     string
	CPUs     int
	MemoryGB int
	// USD is what the plan cost a month on checkedProviders, in US dollars
	// before tax. Renews is the monthly price after a first term paid ahead,
	// as Hostinger's two years are, or 0 when USD is the price every month.
	USD, Renews float64
}

// Fit is the smallest plan a provider sells with at least the memory and
// cores asked for; a plan without a name when it sells none that big.
func (p Provider) Fit(memoryGB, cores int) Plan {
	for _, pl := range p.Plans {
		if pl.MemoryGB >= memoryGB && pl.CPUs >= cores {
			return pl
		}
	}
	return Plan{}
}

// providers were picked for fast cores and many regions (docs/marketing,
// plan.md). Their plans, regions and prices were checked on each provider's
// own pricing page, or Vultr's public API, on checkedProviders: Hostinger's
// KVM plans, DigitalOcean's Basic and General Purpose Droplets, Vultr's Cloud
// Compute.
var providers = []Provider{
	{
		Name:    "Hostinger",
		Regions: "Regions in the Americas, Europe and Asia",
		URL:     "https://www.hostinger.com/vps-hosting",
		Plans: []Plan{
			{"KVM 1", 1, 4, 6.49, 11.99}, {"KVM 2", 2, 8, 8.99, 14.99}, {"KVM 4", 4, 16, 12.99, 28.99}, {"KVM 8", 8, 32, 25.99, 49.99},
		},
	},
	{
		Name:    "DigitalOcean",
		Regions: "Regions on four continents",
		URL:     "https://www.digitalocean.com/pricing/droplets",
		Plans: []Plan{
			{"Basic", 2, 4, 24, 0}, {"Basic", 4, 8, 48, 0}, {"Basic", 8, 16, 96, 0}, {"General Purpose", 8, 32, 252, 0},
		},
	},
	{
		Name:    "Vultr",
		Regions: "Regions on six continents",
		URL:     "https://www.vultr.com/pricing/",
		Plans: []Plan{
			{"Cloud Compute", 2, 4, 20, 0}, {"Cloud Compute", 4, 8, 40, 0}, {"Cloud Compute", 6, 16, 80, 0}, {"Cloud Compute", 8, 32, 160, 0},
		},
	},
}

// checkedProviders is the day the plans above were last checked.
const checkedProviders = "2026-09-28"

// anyReferral reports whether any provider link earns a commission.
func anyReferral() bool {
	for _, p := range providers {
		if p.Referral {
			return true
		}
	}
	return false
}

// usd writes a price as the cost guide shows it: "$20", "$8.99".
func usd(v float64) string {
	return "$" + strings.TrimSuffix(fmt.Sprintf("%.2f", v), ".00")
}

// PlanAt is a plan and the provider that sells it.
type PlanAt struct {
	Provider string
	Plan
}

// cheapestFit is the cheapest plan, at any of the providers, with at least
// the memory and cores asked for; one without a name when none is that big.
func cheapestFit(memoryGB, cores int) PlanAt {
	var best PlanAt
	for _, p := range providers {
		if pl := p.Fit(memoryGB, cores); pl.Name != "" && (best.Name == "" || pl.USD < best.USD) {
			best = PlanAt{Provider: p.Name, Plan: pl}
		}
	}
	return best
}
