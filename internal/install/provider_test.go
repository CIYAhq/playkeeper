package install

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestDetectProviderFromCloudInitAndTheFirmware(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string]string
		want  Provider
	}{
		{"nothing to tell by", nil, Provider{}},
		{"cloud-init on AWS", map[string]string{"/run/cloud-init/cloud-id": "aws\n"}, providerAWS},
		{"cloud-init on AWS GovCloud", map[string]string{"/run/cloud-init/cloud-id": "aws-gov\n"}, providerAWS},
		{"cloud-init on Google Cloud", map[string]string{"/run/cloud-init/cloud-id": "gce\n"}, providerGoogleCloud},
		{"cloud-init on Azure China", map[string]string{"/run/cloud-init/cloud-id": "azure-china\n"}, providerAzure},
		{"cloud-init on Oracle Cloud", map[string]string{"/run/cloud-init/cloud-id": "oracle\n"}, providerOracleCloud},
		{"cloud-init on DigitalOcean", map[string]string{"/run/cloud-init/cloud-id": "digitalocean\n"}, providerDigitalOcean},
		{"cloud-init on Hetzner", map[string]string{"/run/cloud-init/cloud-id": "hetzner\n"}, providerHetzner},
		{"cloud-init on Vultr", map[string]string{"/run/cloud-init/cloud-id": "vultr\n"}, providerVultr},
		{"cloud-init on Linode", map[string]string{"/run/cloud-init/cloud-id": "akamai\n"}, providerLinode},
		{"cloud-init's id wins over the firmware", map[string]string{"/run/cloud-init/cloud-id": "hetzner\n", "/sys/class/dmi/id/sys_vendor": "Amazon EC2\n"}, providerHetzner},
		{"a cloud-init id it doesn't know falls back to the firmware", map[string]string{"/run/cloud-init/cloud-id": "openstack\n", "/sys/class/dmi/id/sys_vendor": "Vultr\n"}, providerVultr},
		{"cloud-init on a lab's NoCloud", map[string]string{"/run/cloud-init/cloud-id": "nocloud\n", "/sys/class/dmi/id/sys_vendor": "QEMU\n"}, Provider{}},
		{"Oracle Cloud's asset tag", map[string]string{"/sys/class/dmi/id/chassis_asset_tag": "OracleCloud.com\n"}, providerOracleCloud},
		{"Azure's asset tag", map[string]string{"/sys/class/dmi/id/chassis_asset_tag": "7783-7084-3265-9085-8269-3286-77\n", "/sys/class/dmi/id/sys_vendor": "Microsoft Corporation\n"}, providerAzure},
		{"Hyper-V outside Azure", map[string]string{"/sys/class/dmi/id/sys_vendor": "Microsoft Corporation\n", "/sys/class/dmi/id/product_name": "Virtual Machine\n"}, Provider{}},
		{"an AWS Nitro instance", map[string]string{"/sys/class/dmi/id/sys_vendor": "Amazon EC2\n"}, providerAWS},
		{"an AWS Xen instance by its product UUID", map[string]string{"/sys/class/dmi/id/sys_vendor": "Xen\n", "/sys/class/dmi/id/product_uuid": "EC2E1916-9099-7CAF-FD21-012345ABCDEF\n"}, providerAWS},
		{"an AWS Xen instance by its hypervisor UUID", map[string]string{"/sys/hypervisor/uuid": "ec2e1916-9099-7caf-fd21-012345abcdef\n"}, providerAWS},
		{"Google Cloud's product name", map[string]string{"/sys/class/dmi/id/product_name": "Google Compute Engine\n"}, providerGoogleCloud},
		{"Google Cloud's vendor", map[string]string{"/sys/class/dmi/id/sys_vendor": "Google\n"}, providerGoogleCloud},
		{"DigitalOcean's vendor", map[string]string{"/sys/class/dmi/id/sys_vendor": "DigitalOcean\n"}, providerDigitalOcean},
		{"Hetzner's vendor", map[string]string{"/sys/class/dmi/id/sys_vendor": "Hetzner\n"}, providerHetzner},
		{"Linode's vendor", map[string]string{"/sys/class/dmi/id/sys_vendor": "Linode\n"}, providerLinode},
		{"Akamai's vendor", map[string]string{"/sys/class/dmi/id/sys_vendor": "Akamai\n"}, providerLinode},
	} {
		root := t.TempDir()
		for p, body := range c.files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, p), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := DetectProvider(System{Root: root}); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

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
	if res.Provider != providerOracleCloud || res.PanelPort != 8443 || res.GamePort != 25565 {
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
