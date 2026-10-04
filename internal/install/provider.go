package install

import "github.com/CIYAhq/playkeeper/internal/platform"

// PortsURL is the page with each provider's steps for opening ports 8443 and
// 25565 in its firewall (platform.PortsURL).
const PortsURL = platform.PortsURL

// Provider is the cloud or VPS provider the machine runs at, so the summary
// can link that provider's steps for opening its firewall (platform.Provider).
type Provider = platform.Provider

// Providers are the providers DetectProvider can tell.
var Providers = platform.Providers

// DetectProvider tells the provider of the machine sys describes.
func DetectProvider(sys System) Provider { return platform.DetectProvider(sys.Root) }
