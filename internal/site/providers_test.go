package site

import (
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
// first one: the note above the provider cards.
// A provider without one gets its plain link and no claim of a commission.
// That holds with every program on, with one, and with none.
func TestPartnerLinksAreDisclosedFirst(t *testing.T) {
	saved := slices.Clone(providers)
	t.Cleanup(func() { providers = saved })
	all := []string{"Hostinger", "DigitalOcean", "Vultr"}
	for _, partners := range [][]string{all, {"Vultr"}, nil} {
		providers = slices.Clone(saved)
		for i := range providers {
			if !slices.Contains(partners, providers[i].Name) {
				providers[i].Partner = ""
			}
		}
		built := pages(build(t, Default))
		note := template.HTMLEscapeString(partnerNote())
		switch len(partners) {
		case len(all):
			if !strings.HasPrefix(partnerNote(), "The links below are affiliate links:") {
				t.Errorf("with every program on, the note is %q", partnerNote())
			}
		case 1:
			if !strings.HasPrefix(partnerNote(), "The Vultr link below is an affiliate link:") {
				t.Errorf("with Vultr's program alone, the note is %q", partnerNote())
			}
		case 0:
			if partnerNote() != "" || !strings.Contains(built["/sizing"], "No partner links: we earn nothing from these.") {
				t.Errorf("with no program on, /sizing still claims a commission: %q", partnerNote())
			}
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
	}
}
