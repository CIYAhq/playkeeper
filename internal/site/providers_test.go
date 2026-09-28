package site

import (
	"fmt"
	"html/template"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/sizing"
)

// Every size the sizing guide answers with fits a plan at each provider,
// disk included; each plan is the cheapest fit for some group; and plans run
// cheapest first, so Fit's first match is the cheapest.
func TestProvidersFitEverySize(t *testing.T) {
	for _, p := range providers {
		for i, pl := range p.Plans {
			if i > 0 && pl.USD < p.Plans[i-1].USD {
				t.Errorf("%s: %s with %d GB costs less than the plan before it", p.Name, pl.Name, pl.MemoryGB)
			}
			if pl.DiskGB <= 0 {
				t.Errorf("%s: %s with %d GB has no disk size", p.Name, pl.Name, pl.MemoryGB)
			}
		}
		for _, r := range sizing.Table() {
			pl := p.Fit(r.MemoryGB, r.Cores)
			switch {
			case pl.Name == "":
				t.Errorf("%s sells nothing for %s friends %s (%d GB, %d cores)", p.Name, r.Band.Label(), r.Workload, r.MemoryGB, r.Cores)
			case pl.DiskGB < r.DiskGB:
				t.Errorf("%s: %s for %s friends %s has %d GB of disk, want %d", p.Name, pl.Name, r.Band.Label(), r.Workload, pl.DiskGB, r.DiskGB)
			}
		}
		fits, err := p.Fits()
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fits {
			if len(f.For) == 0 {
				t.Errorf("%s: the sizing guide picks %s with %d GB for nobody", p.Name, f.Name, f.MemoryGB)
			}
		}
		if p.Partner != "" {
			if u, err := url.Parse(p.Partner); err != nil || u.Scheme != "https" || u.Host == "" {
				t.Errorf("%s's partner link %q isn't an https address", p.Name, p.Partner)
			}
			if p.Program != "affiliate" && p.Program != "referral" {
				t.Errorf("%s's program is %q, want affiliate or referral", p.Name, p.Program)
			}
		}
	}
	hostinger, err := provider("Hostinger")
	if err != nil {
		t.Fatal(err)
	}
	fits, err := hostinger.Fits()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"11–40 friends on Vanilla or Paper", "5–20 friends using lots of plugins or a few mods", "1–4 friends on a big modpack"}; !slices.Equal(fits[1].For, want) {
		t.Errorf("KVM 4 is the pick for %q, want %q", fits[1].For, want)
	}
	if got := usd(hostinger.Upfront(hostinger.Plans[0])); got != "$215.76" {
		t.Errorf("KVM 2's two years upfront come to %s, want $215.76", got)
	}
}

var reAnchor = regexp.MustCompile(`<a\s[^>]*>`)

// A partner link is sponsored, and its page says who pays for it before the
// first one: the note above the provider cards, or a guide's line at its top.
// A provider without one gets its plain link and no claim of a commission.
// That holds with every program on, with some, with one, and with none; the
// note calls Vultr's alone a referral link.
func TestPartnerLinksAreDisclosedFirst(t *testing.T) {
	saved := slices.Clone(providers)
	t.Cleanup(func() { providers = saved })
	for _, c := range []struct {
		partners []string
		note     string
	}{
		{[]string{"Hostinger", "DigitalOcean", "Vultr"}, "The links below are affiliate links: Playkeeper earns a commission when you buy through them,"},
		{[]string{"Hostinger", "Vultr"}, "The Hostinger and Vultr links below are affiliate links: Playkeeper earns a commission when you buy through them,"},
		{[]string{"Hostinger"}, "The Hostinger link below is an affiliate link: Playkeeper earns a commission when you buy through it,"},
		{[]string{"Vultr"}, "The Vultr link below is a referral link: Playkeeper earns a commission when you buy through it,"},
		{nil, ""},
	} {
		partners := c.partners
		providers = slices.Clone(saved)
		for i, p := range providers {
			switch {
			case !slices.Contains(partners, p.Name):
				providers[i].Partner = ""
			case p.Partner == "":
				providers[i].Partner = p.URL + "?partner=test"
			}
		}
		built := pages(build(t, Default))
		note := template.HTMLEscapeString(partnerNote())
		if !strings.HasPrefix(partnerNote(), c.note) || (c.note == "") != (partnerNote() == "") {
			t.Errorf("with %v on, the note is %q, want it to start %q", partners, partnerNote(), c.note)
		}
		if has := strings.Contains(built["/sizing"], "No partner links: we earn nothing from these."); has != (len(partners) == 0) {
			t.Errorf("with %v on, /sizing says it earns nothing: %v", partners, has)
		}
		for path, html := range built {
			for _, p := range providers {
				plain, partner := `href="`+template.HTMLEscapeString(p.URL)+`"`, ""
				if p.Partner != "" {
					partner = `href="` + template.HTMLEscapeString(p.Partner) + `"`
				}
				for _, a := range reAnchor.FindAllString(html, -1) {
					switch {
					case partner != "" && strings.Contains(a, partner) && !strings.Contains(a, ` rel="sponsored noopener"`):
						t.Errorf("%v on: %s links %s's partner link without rel=sponsored: %s", partners, path, p.Name, a)
					case strings.Contains(a, plain) && !strings.Contains(a, ` rel="noopener"`):
						t.Errorf("%v on: %s links %s's plain link as sponsored: %s", partners, path, p.Name, a)
					}
				}
				disclosure := template.HTMLEscapeString(p.Disclosure())
				if partner == "" {
					if strings.Contains(html, disclosure) {
						t.Errorf("%v on: %s says Playkeeper earns from %s, which has no partner link", partners, path, p.Name)
					}
					continue
				}
				at := strings.Index(html, partner)
				if at < 0 {
					continue
				}
				said := func(s string) bool { i := strings.Index(html, s); return s != "" && i >= 0 && i < at }
				if !said(note) && !said(disclosure) {
					t.Errorf("%v on: %s links %s's partner link before saying it earns a commission", partners, path, p.Name)
				}
			}
		}
		for _, p := range providers {
			guide := built[p.Guide]
			if has := strings.Contains(guide, template.HTMLEscapeString(p.Disclosure())); has != (p.Partner != "") {
				t.Errorf("%v on: %s's guide says it earns a commission: %v", partners, p.Name, has)
			}
		}
	}
}

// Each provider's guide is linked from its card, lists its plans with their
// prices and the groups the sizing guide sends to each, offers the plan for
// the sizing guide's first answer, and names the systems Playkeeper runs on
// as it does.
func TestProviderGuidesFollowTheSizingGuide(t *testing.T) {
	built := pages(build(t, Default))
	first, err := sizing.Recommend(sizingWorkload, sizingPlayers)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range providers {
		guide, ok := built[p.Guide]
		if !ok {
			t.Errorf("%s has no guide at %s", p.Name, p.Guide)
			continue
		}
		for _, from := range []string{"/sizing", "/alternatives/aternos"} {
			if !strings.Contains(built[from], `<a class="link-arrow" href="`+p.Guide+`">Setup guide`) {
				t.Errorf("%s's card on %s doesn't link its guide", p.Name, from)
			}
		}
		fits, err := p.Fits()
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fits {
			row := fmt.Sprintf(`<th scope="row">%d vCPU · %d GB<span class="plan-sub">%s · %d GB NVMe</span><span class="plan-price">%s a month`, f.CPUs, f.MemoryGB, f.Name, f.DiskGB, usd(f.USD))
			if f.Renews != 0 {
				row += ", then " + usd(f.Renews)
			}
			if !strings.Contains(guide, row+"</span></th><td>"+strings.Join(f.For, "<br>")+"</td>") {
				t.Errorf("%s's guide has no row for %s with %d GB at %s for %q", p.Name, f.Name, f.MemoryGB, usd(f.USD), f.For)
			}
		}
		pl := p.Fit(first.MemoryGB, first.Cores)
		cta := fmt.Sprintf(`data-plan>%s · %d vCPU · %d GB</p>`, pl.Name, pl.CPUs, pl.MemoryGB)
		link := `href="` + template.HTMLEscapeString(p.Link()) + `" rel="` + p.Rel() + `">Get a ` + p.Name + ` server`
		if !strings.Contains(guide, cta) || !strings.Contains(guide, link) {
			t.Errorf("%s's guide doesn't offer %s with %d GB through its link", p.Name, pl.Name, pl.MemoryGB)
		}
		// html/template writes + as &#43; in text.
		if !strings.Contains(guide, "Playkeeper also runs on "+strings.ReplaceAll(systemRanges(), "+", "&#43;")+", on x86_64 or 64-bit ARM.") {
			t.Errorf("%s's guide doesn't name the systems Playkeeper runs on", p.Name)
		}
	}
	if got, want := systemRanges(), "Ubuntu 20.04–26.04, Debian 12–13, the RHEL family 9+ or Amazon Linux 2023"; got != want {
		t.Errorf("the provider guides name the systems as %q, want %q", got, want)
	}
}

// A new account's credit shows with its conditions wherever its partner link
// does, on the provider's card and in its guide, and nowhere once the link
// is gone. A guide calls a month free only when the credit pays for it.
func TestOffersShowWithTheirTermsBesideTheirLink(t *testing.T) {
	saved := slices.Clone(providers)
	t.Cleanup(func() { providers = saved })
	vultr, err := provider("Vultr")
	if err != nil {
		t.Fatal(err)
	}
	if got := vultr.Offered(); got != "$300 of credit for 30 days" || vultr.OfferTerms == "" {
		t.Fatalf("Vultr's offer is %q with terms %q", got, vultr.OfferTerms)
	}
	offer := template.HTMLEscapeString("New accounts get " + vultr.Offered() + ". " + vultr.OfferTerms)
	built := pages(build(t, Default))
	for path, want := range map[string]int{"/sizing": 1, "/alternatives/aternos": 1, vultr.Guide: 1} {
		if got := strings.Count(strings.ReplaceAll(built[path], "</strong>", ""), offer); got != want {
			t.Errorf("%s shows Vultr's offer with its terms %d times, want %d", path, got, want)
		}
	}
	guide := built[vultr.Guide]
	for _, want := range []string{"so the first month is free. " + vultr.OfferTerms, "enough for that plan's first month. " + vultr.OfferTerms} {
		if !strings.Contains(guide, template.HTMLEscapeString(vultr.Offered())+" through our link, "+want) {
			t.Errorf("Vultr's guide doesn't say %q", want)
		}
	}
	for path, html := range built {
		if path != "/sizing" && path != "/alternatives/aternos" && path != vultr.Guide && strings.Contains(html, "of credit for 30 days") {
			t.Errorf("%s shows Vultr's offer away from its link", path)
		}
	}
	if pl := (Plan{USD: vultr.OfferUSD + 1}); vultr.CoversMonth(pl) {
		t.Error("an offer covers a month of a plan that costs more than it")
	}
	for i := range providers {
		if providers[i].Name == "Vultr" {
			providers[i].Partner = ""
		}
	}
	for path, html := range pages(build(t, Default)) {
		if strings.Contains(html, "of credit for 30 days") {
			t.Errorf("without Vultr's partner link, %s still shows its offer", path)
		}
	}
}
