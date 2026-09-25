// Package names is the protocol and client for free yourname.playkeeper.io
// addresses. An install proves who it is with its own Ed25519 key: every
// change is a request signed with that key, and the names service
// (cmd/playkeeper-names) points the name's DNS records at the address the
// request came from. The package holds no server code; the service imports
// it for the shared rules and the signature check.
package names

import (
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultServiceURL is the names service the Playkeeper project runs.
	DefaultServiceURL = "https://names.playkeeper.io"
	// DefaultBase is the domain free names live under.
	DefaultBase = "playkeeper.io"
)

// Length limits of names and server labels.
const (
	NameMinLen   = 3
	NameMaxLen   = 32
	ServerMaxLen = 32
)

// States of a claimed name.
const (
	// StateActive: the name's records point at the install.
	StateActive = "active"
	// StateLapsed: the install stopped refreshing, so the records were
	// removed; the name is still the install's until it is freed.
	StateLapsed = "lapsed"
	// StateReleased: the install gave the name up; it is held from others
	// for a while so nobody can take over its players at once.
	StateReleased = "released"
)

// DNS states in answers: whether Cloudflare already has the change.
const (
	DNSOK      = "ok"
	DNSPending = "pending"
)

// Problems with a name or server label, in Error.Params["problem"].
const (
	ProblemTooShort     = "too_short"
	ProblemTooLong      = "too_long"
	ProblemCharacters   = "characters"
	ProblemHyphenEdge   = "hyphen_edge"
	ProblemDoubleHyphen = "double_hyphen"
)

// CheckName reports whether s is a valid name: 3 to 32 lowercase letters,
// digits and hyphens, not starting or ending with a hyphen and without two
// hyphens in a row (which also rules out internationalised "xn--" names).
func CheckName(s string) error {
	return checkLabel(s, NameMinLen, NameMaxLen, CodeInvalidName, "A name")
}

// CheckServerLabel reports whether s can name a server under a name
// ("survival" in survival.alice.playkeeper.io): the rules of CheckName, from
// one character on.
func CheckServerLabel(s string) error {
	return checkLabel(s, 1, ServerMaxLen, CodeInvalidServer, "A server label")
}

func checkLabel(s string, minLen, maxLen int, code, what string) error {
	var problem, msg string
	switch {
	case len(s) < minLen:
		problem, msg = ProblemTooShort, fmt.Sprintf("%s needs at least %d characters.", what, minLen)
	case len(s) > maxLen:
		problem, msg = ProblemTooLong, fmt.Sprintf("%s can have at most %d characters.", what, maxLen)
	case strings.Trim(s, "abcdefghijklmnopqrstuvwxyz0123456789-") != "":
		problem, msg = ProblemCharacters, what+" can only use lowercase letters a to z, digits and hyphens."
	case s[0] == '-' || s[len(s)-1] == '-':
		problem, msg = ProblemHyphenEdge, what+" cannot start or end with a hyphen."
	case strings.Contains(s, "--"):
		problem, msg = ProblemDoubleHyphen, what+" cannot have two hyphens in a row."
	default:
		return nil
	}
	return &Error{Status: 400, Code: code, Message: msg, Params: map[string]any{"problem": problem, "min": minLen, "max": maxLen}}
}

// NormalizeName turns what someone typed into a name candidate: it trims
// spaces, lowercases, and drops a trailing ".playkeeper.io" (or other base)
// so pasting the whole address works. The result still needs CheckName.
func NormalizeName(input, base string) string {
	s := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(input)), ".")
	return strings.TrimSuffix(s, "."+strings.ToLower(base))
}

// Address is the host name of name under base: alice.playkeeper.io.
func Address(name, base string) string { return name + "." + base }

// ServerAddress is the address players type for a server under a name; an
// empty label is the name itself.
func ServerAddress(label, name, base string) string {
	if label == "" {
		return Address(name, base)
	}
	return label + "." + Address(name, base)
}

// ChallengeFQDN is the ACME DNS-01 record name for a name's certificate.
func ChallengeFQDN(name, base string) string { return "_acme-challenge." + Address(name, base) }

// Availability answers whether a name can be claimed.
type Availability struct {
	Name      string `json:"name"`
	Address   string `json:"address,omitempty"`
	Available bool   `json:"available"`
	// Code says why not: CodeInvalidName, CodeNameReserved, CodeNameTaken
	// or CodeNameHeld.
	Code    string         `json:"code,omitempty"`
	Message string         `json:"message"`
	Params  map[string]any `json:"params,omitempty"`
}

// Name is a claimed name as the service keeps it.
type Name struct {
	Name        string    `json:"name"`
	Address     string    `json:"address"`
	State       string    `json:"state"`
	IPv4        string    `json:"ipv4,omitempty"`
	IPv6        string    `json:"ipv6,omitempty"`
	ClaimedAt   time.Time `json:"claimedAt"`
	RefreshedAt time.Time `json:"refreshedAt"`
	// RefreshBy is when the records are removed unless the install
	// refreshes first (zero once they are removed).
	RefreshBy time.Time `json:"refreshBy,omitzero"`
	// FreedAt is when the name goes back to everyone: for an active or
	// lapsed name if it is not refreshed before, for a released one when
	// its hold ends.
	FreedAt time.Time `json:"freedAt"`
	Servers []Server  `json:"servers"`
	DNS     string    `json:"dns"`
}

// Server is a Minecraft server with its own address under a name, reached
// through an SRV record that points at the name and the server's port.
type Server struct {
	// Label is the part before the name ("survival" in
	// survival.alice.playkeeper.io), or "" for the name itself.
	Label   string `json:"label"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	DNS     string `json:"dns"`
}

// Challenge is an ACME DNS-01 TXT record the service set for a name.
type Challenge struct {
	FQDN      string    `json:"fqdn"`
	Value     string    `json:"value"`
	ExpiresAt time.Time `json:"expiresAt"`
	DNS       string    `json:"dns"`
}

// IPInfo is the address the service sees a request come from.
type IPInfo struct {
	IP string `json:"ip"`
	// Family is "ipv4" or "ipv6".
	Family string `json:"family"`
	// Public is false for addresses a name cannot point at: private,
	// shared, reserved and documentation ranges, and Cloudflare's proxies.
	Public bool `json:"public"`
}

// RefreshRequest is the body of an address refresh.
type RefreshRequest struct {
	// ClearOther removes the record of the other IP version, because the
	// install has no working address of that version any more.
	ClearOther bool `json:"clearOther,omitempty"`
}

// ServerRequest is the body of a server address request.
type ServerRequest struct {
	Port int `json:"port"`
}

// NameList is the answer to listing a key's names.
type NameList struct {
	Names []Name `json:"names"`
}
