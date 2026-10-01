package install

import (
	"os"
	"strings"
)

// PortsURL is the page with each provider's steps for opening ports 8443 and
// 25565 in its firewall; a provider's ID is the id of its section there.
const PortsURL = "https://playkeeper.io/ports"

// Provider is the cloud or VPS provider the machine runs at, as far as the
// machine itself tells, so the summary can link that provider's steps for
// opening its firewall. The zero Provider is one it can't tell.
type Provider struct {
	ID   string
	Name string
}

// StepsURL is where the provider's firewall steps are.
func (p Provider) StepsURL() string {
	if p.ID == "" {
		return PortsURL
	}
	return PortsURL + "#" + p.ID
}

var (
	providerAWS          = Provider{"aws", "AWS"}
	providerGoogleCloud  = Provider{"google-cloud", "Google Cloud"}
	providerAzure        = Provider{"azure", "Azure"}
	providerOracleCloud  = Provider{"oracle-cloud", "Oracle Cloud"}
	providerDigitalOcean = Provider{"digitalocean", "DigitalOcean"}
	providerHetzner      = Provider{"hetzner", "Hetzner"}
	providerVultr        = Provider{"vultr", "Vultr"}
	providerLinode       = Provider{"linode", "Linode"}
)

// Providers are the providers DetectProvider can tell, each with a section
// of PortsURL.
var Providers = []Provider{providerAWS, providerGoogleCloud, providerAzure, providerOracleCloud, providerDigitalOcean, providerHetzner, providerVultr, providerLinode}

// DetectProvider tells the provider from the cloud cloud-init found when the
// machine booted (/run/cloud-init/cloud-id, as `cloud-id` prints it) or, on
// an image without cloud-init, from the firmware's strings in
// /sys/class/dmi/id, which cloud-init's ds-identify reads too.
func DetectProvider(sys System) Provider {
	if id := readTrimmed(sys, "/run/cloud-init/cloud-id"); id != "" {
		if p, ok := providerFromCloudID(id); ok {
			return p
		}
	}
	dmi := func(name string) string { return readTrimmed(sys, "/sys/class/dmi/id/"+name) }
	switch dmi("chassis_asset_tag") {
	case "OracleCloud.com":
		return providerOracleCloud
	case "7783-7084-3265-9085-8269-3286-77":
		return providerAzure
	}
	switch vendor, uuid := dmi("sys_vendor"), strings.ToLower(dmi("product_uuid")); {
	case vendor == "Amazon EC2", strings.HasPrefix(uuid, "ec2"), strings.HasPrefix(strings.ToLower(readTrimmed(sys, "/sys/hypervisor/uuid")), "ec2"):
		return providerAWS
	case vendor == "Google", dmi("product_name") == "Google Compute Engine":
		return providerGoogleCloud
	case vendor == "DigitalOcean":
		return providerDigitalOcean
	case vendor == "Hetzner":
		return providerHetzner
	case vendor == "Vultr":
		return providerVultr
	case vendor == "Linode", vendor == "Akamai":
		return providerLinode
	}
	return Provider{}
}

// providerFromCloudID maps cloud-init's cloud id ("aws", "aws-gov", "gce",
// "azure-china", "akamai" …) to a provider.
func providerFromCloudID(id string) (Provider, bool) {
	name, _, _ := strings.Cut(strings.ToLower(id), "-")
	switch name {
	case "aws":
		return providerAWS, true
	case "gce":
		return providerGoogleCloud, true
	case "azure":
		return providerAzure, true
	case "oracle":
		return providerOracleCloud, true
	case "digitalocean":
		return providerDigitalOcean, true
	case "hetzner":
		return providerHetzner, true
	case "vultr":
		return providerVultr, true
	case "akamai", "linode":
		return providerLinode, true
	}
	return Provider{}, false
}

func readTrimmed(sys System, path string) string {
	b, err := os.ReadFile(sys.P(path))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
