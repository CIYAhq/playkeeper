package names

import "github.com/CIYAhq/playkeeper/internal/certs"

// Certificates for free addresses answer their DNS-01 challenges through this
// client.
var _ certs.DNSChallenger = (*Client)(nil)
