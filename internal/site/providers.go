package site

import (
	"fmt"
	"slices"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/platform"
	"github.com/CIYAhq/playkeeper/internal/sizing"
)

// Provider is a VPS provider the site suggests, with the plans it sells as
// the provider names them. Its cards on /sizing and the Aternos page show
// plan names and "See today's price", never prices; the cost guide and the
// provider's own guide show what a plan cost, dated with checkedProviders.
type Provider struct {
	Name string
	// Guide is the address of its setup guide.
	Guide string
	// Regions says where it has machines, in a few words.
	Regions string
	// URL is the page listing its plans.
	URL string
	// Partner is Playkeeper's link to the provider through its affiliate or
	// referral program, which earns a commission, and Program says which of
	// the two it is. Pages link URL while Partner is empty, and say who pays
	// them beside every partner link. A partner link on a host other than
	// the provider's own also needs that host in recordEvents in
	// test/e2e/ui/site.spec.ts, or the analytics test follows it off the
	// machine.
	Partner, Program string
	// OfferUSD and OfferDays are the credit a new account gets through
	// Partner, as the program's promotion sets it, and how long it lasts;
	// OfferTerms are its conditions. Pages show all three, and only beside
	// the partner link.
	OfferUSD   float64
	OfferDays  int
	OfferTerms string
	// TermMonths is how many months a first price is paid upfront for, when
	// the provider sells by the term rather than by the month.
	TermMonths int
	// Plans are the sizes the site suggests, cheapest first, so Fit's first
	// match is the cheapest that fits.
	Plans []Plan
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
	// DiskGB is its NVMe disk.
	DiskGB int
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

// PlanFit is one of a provider's plans with the groups the sizing guide
// sends to it.
type PlanFit struct {
	Plan
	// For are the friends at once, and what they run, that it's the
	// cheapest fit for, like "1–10 friends on Vanilla or Paper".
	For []string
}

// Fits are the provider's plans, each with the groups it's the cheapest fit
// for.
func (p Provider) Fits() ([]PlanFit, error) {
	out := make([]PlanFit, len(p.Plans))
	for i, pl := range p.Plans {
		out[i].Plan = pl
	}
	for _, w := range sizing.Workloads() {
		from, to := map[int]int{}, map[int]int{}
		var phrase string
		for _, b := range sizing.Bands() {
			r, err := sizing.Recommend(w, b.Max)
			if err != nil {
				return nil, err
			}
			phrase = sizingPhrase(r)
			i := slices.Index(p.Plans, p.Fit(r.MemoryGB, r.Cores))
			if i < 0 {
				continue
			}
			if _, ok := from[i]; !ok {
				from[i] = b.Min
			}
			to[i] = b.Max
		}
		for i := range p.Plans {
			if lo, ok := from[i]; ok {
				out[i].For = append(out[i].For, fmt.Sprintf("%d–%d friends %s", lo, to[i], phrase))
			}
		}
	}
	return out, nil
}

// LinkNote says, beside a button to the provider's partner link, what it earns.
func (p Provider) LinkNote() string {
	kind := "Affiliate"
	if p.Program == "referral" {
		kind = "Referral"
	}
	return kind + " link: Playkeeper earns a commission, at no extra cost to you."
}

// Offered is the new-account offer pages show, like "$300 of credit for 30
// days": the provider's, while its partner link carries it, or "".
func (p Provider) Offered() string {
	if p.Partner == "" || p.OfferUSD <= 0 {
		return ""
	}
	return fmt.Sprintf("%s of credit for %d days", usd(p.OfferUSD), p.OfferDays)
}

// CoversMonth reports whether the offer pays for a month of the plan, so a
// page may call that month free.
func (p Provider) CoversMonth(pl Plan) bool {
	return p.Offered() != "" && p.OfferDays >= 30 && p.OfferUSD >= pl.USD
}

// Link is where the provider's links go: the partner link when there is one.
func (p Provider) Link() string {
	if p.Partner != "" {
		return p.Partner
	}
	return p.URL
}

// Rel is a link to the provider's rel: a partner link is sponsored.
func (p Provider) Rel() string {
	if p.Partner != "" {
		return "sponsored noopener"
	}
	return "noopener"
}

// Disclosure says, at the top of a page with the provider's partner link,
// that Playkeeper earns a commission from it.
func (p Provider) Disclosure() string {
	who := "a " + p.Name + " affiliate"
	if p.Program == "referral" {
		who = "in " + p.Name + "'s referral program"
	}
	return "Playkeeper is " + who + ": we earn a commission when you buy through the links on this page, at no extra cost to you. It doesn't change what we recommend."
}

// Upfront is what the plan costs for the provider's first term, when it
// sells by the term.
func (p Provider) Upfront(pl Plan) float64 { return pl.USD * float64(p.TermMonths) }

// providers were picked on merit for Minecraft (docs/marketing, plan.md):
// fast cores, NVMe disks, enough memory for the sizing guide's answers and
// regions near your friends. Each gets its line that suits Minecraft best:
// Hostinger's KVM plans (KVM 1 has one core, below the two Playkeeper is
// tested on), DigitalOcean's Basic Droplets with Premium AMD cores, Vultr's
// High Performance Cloud Compute. Their plans, regions and prices were
// checked on each provider's own pricing page, or Vultr's public API, on
// checkedProviders.
var providers = []Provider{
	{
		Name:       "Hostinger",
		Guide:      "/guides/hostinger-minecraft-server",
		Regions:    "Regions in the Americas, Europe and Asia",
		URL:        "https://www.hostinger.com/vps-hosting",
		Program:    "affiliate",
		TermMonths: 24,
		Plans: []Plan{
			{"KVM 2", 2, 8, 8.99, 14.99, 100}, {"KVM 4", 4, 16, 12.99, 28.99, 200}, {"KVM 8", 8, 32, 25.99, 49.99, 400},
		},
	},
	{
		Name:    "DigitalOcean",
		Guide:   "/guides/digitalocean-minecraft-server",
		Regions: "Regions on four continents",
		URL:     "https://www.digitalocean.com/pricing/droplets",
		Program: "affiliate",
		Plans: []Plan{
			{"Premium AMD", 2, 4, 28, 0, 80}, {"Premium AMD", 2, 8, 42, 0, 100}, {"Premium AMD", 4, 8, 56, 0, 160},
			{"Premium AMD", 4, 16, 84, 0, 200}, {"Premium AMD", 8, 16, 112, 0, 320}, {"Premium AMD", 8, 32, 168, 0, 400},
		},
	},
	{
		Name:       "Vultr",
		Guide:      "/guides/vultr-minecraft-server",
		Regions:    "Regions on six continents",
		URL:        "https://www.vultr.com/pricing/",
		Partner:    "https://www.vultr.com/?ref=9925287-9J",
		Program:    "referral",
		OfferUSD:   300,
		OfferDays:  30,
		OfferTerms: "A card or PayPal is needed, and unused credit expires.",
		Plans: []Plan{
			{"High Performance", 2, 4, 24, 0, 100}, {"High Performance", 4, 8, 48, 0, 180}, {"High Performance", 4, 12, 72, 0, 260},
			{"High Performance", 8, 16, 96, 0, 350}, {"High Performance", 12, 24, 144, 0, 500},
		},
	},
}

// checkedProviders is the day the plans above were last checked.
const checkedProviders = "2026-09-28"

// provider is the provider with the given name.
func provider(name string) (*Provider, error) {
	for i := range providers {
		if providers[i].Name == name {
			return &providers[i], nil
		}
	}
	return nil, fmt.Errorf("no provider %q in providers", name)
}

// partnerNote says which of the providers' links below it earn a commission,
// as referral links when every one is, affiliate links otherwise; empty when
// none does.
func partnerNote() string {
	var names []string
	kind := "referral"
	for _, p := range providers {
		if p.Partner != "" {
			names = append(names, p.Name)
			if p.Program != "referral" {
				kind = "affiliate"
			}
		}
	}
	const rest = ", at no extra cost to you. It never changes which providers we list or how we order them."
	switch len(names) {
	case 0:
		return ""
	case 1:
		return "The " + names[0] + " link below is " + article(kind) + " " + kind + " link: Playkeeper earns a commission when you buy through it" + rest
	case len(providers):
		return "The links below are " + kind + " links: Playkeeper earns a commission when you buy through them" + rest
	}
	return "The " + andList(names) + " links below are " + kind + " links: Playkeeper earns a commission when you buy through them" + rest
}

// article is "an" before a vowel and "a" otherwise, as in "an affiliate link".
func article(word string) string {
	if strings.ContainsRune("aeiou", rune(word[0])) {
		return "an"
	}
	return "a"
}

// systemRanges names the systems Playkeeper runs on by the releases it's
// tested on, for picking one at a provider: "Ubuntu 20.04–26.04, Debian
// 12–13, the RHEL family 9+ or Amazon Linux 2023".
func systemRanges() string {
	distros := platform.Distros()
	var parts []string
	for _, g := range platform.Groups() {
		if len(g.Members) > 0 {
			parts = append(parts, g.Name+" "+g.Version+"+")
			continue
		}
		i := slices.IndexFunc(distros, func(d platform.Distro) bool { return d.Name == g.Name })
		if i < 0 {
			continue
		}
		part := g.Name + " " + distros[i].Oldest().Version
		if newest := distros[i].Newest().Version; newest != distros[i].Oldest().Version {
			part += "–" + newest
		}
		parts = append(parts, part)
	}
	return joinList(parts, "or")
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
