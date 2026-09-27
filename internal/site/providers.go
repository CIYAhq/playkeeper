package site

// Provider is a VPS provider the site suggests, with the plans it sells as
// the provider names them. Pages show plan names and "See today's price",
// never prices.
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
// plan.md). Their plans and regions were checked on each provider's own
// pricing page, or Vultr's public API, on checkedProviders: Hostinger's KVM
// plans, DigitalOcean's Basic and General Purpose Droplets, Vultr's Cloud
// Compute.
var providers = []Provider{
	{
		Name:    "Hostinger",
		Regions: "Regions in the Americas, Europe and Asia",
		URL:     "https://www.hostinger.com/vps-hosting",
		Plans: []Plan{
			{"KVM 1", 1, 4}, {"KVM 2", 2, 8}, {"KVM 4", 4, 16}, {"KVM 8", 8, 32},
		},
	},
	{
		Name:    "DigitalOcean",
		Regions: "Regions on four continents",
		URL:     "https://www.digitalocean.com/pricing/droplets",
		Plans: []Plan{
			{"Basic", 2, 4}, {"Basic", 4, 8}, {"Basic", 8, 16}, {"General Purpose", 8, 32},
		},
	},
	{
		Name:    "Vultr",
		Regions: "Regions on six continents",
		URL:     "https://www.vultr.com/pricing/",
		Plans: []Plan{
			{"Cloud Compute", 2, 4}, {"Cloud Compute", 4, 8}, {"Cloud Compute", 6, 16}, {"Cloud Compute", 8, 32},
		},
	},
}

// checkedProviders is the day the plans above were last checked.
const checkedProviders = "2026-09-26"

// anyReferral reports whether any provider link earns a commission.
func anyReferral() bool {
	for _, p := range providers {
		if p.Referral {
			return true
		}
	}
	return false
}
