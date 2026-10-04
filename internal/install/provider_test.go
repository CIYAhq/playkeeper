package install

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestTheInstallTellsTheSummaryItsProvider(t *testing.T) {
	h := newFakeHost(t)
	if err := os.MkdirAll(filepath.Join(h.root, "/run/cloud-init"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.root, "/run/cloud-init/cloud-id"), []byte("oracle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := opts("")
	o.Yes = true
	res, err := Run(context.Background(), h.system(t), o, "test")
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != (Provider{ID: "oracle-cloud", Name: "Oracle Cloud"}) || res.PanelPort != 8443 || res.GamePort != 25565 {
		t.Errorf("result: provider %+v, ports %d and %d", res.Provider, res.PanelPort, res.GamePort)
	}
	if got := res.Provider.StepsURL(); got != "https://playkeeper.io/ports#oracle-cloud" {
		t.Errorf("steps at %s", got)
	}
	if got := (Provider{}).StepsURL(); got != "https://playkeeper.io/ports" {
		t.Errorf("an unknown provider's steps at %s", got)
	}
}

// Every provider the installer names has its section on playkeeper.io/ports,
// once that page is on the branch.
func TestEveryProviderHasItsStepsOnTheSite(t *testing.T) {
	page, err := os.ReadFile("../../site/pages/ports.html")
	if os.IsNotExist(err) {
		t.Skip("site/pages/ports.html isn't on this branch yet")
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range Providers {
		if !regexp.MustCompile(`<h2 id="` + regexp.QuoteMeta(p.ID) + `">[^<]*` + regexp.QuoteMeta(p.Name)).Match(page) {
			t.Errorf("site/pages/ports.html has no section %q for %s", p.ID, p.Name)
		}
	}
	if !strings.Contains(string(page), "path: /ports\n") {
		t.Error("the page isn't at /ports, where PortsURL points")
	}
}

func TestOnlyAJoinedMachinesCheckMentionsTheProvidersFirewall(t *testing.T) {
	h := newFakeHost(t)
	if c := check(Preflight(context.Background(), h.system(t), opts("")), "firewall"); c == nil || strings.Contains(c.Detail, "cloud firewall") {
		t.Errorf("a machine with a dashboard: %+v", c)
	}
	o := opts("")
	o.Join = "203.0.113.5:8443"
	if c := check(Preflight(context.Background(), h.system(t), o), "firewall"); c == nil || !strings.Contains(c.Detail, "If your provider has a cloud firewall, allow 25565/tcp there too.") {
		t.Errorf("a joined machine, which prints no setup link: %+v", c)
	}
}
