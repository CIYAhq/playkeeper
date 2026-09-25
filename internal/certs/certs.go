// Package certs gives a Playkeeper machine a friendly address with a real
// certificate: it checks that a name such as mc.example.com points at this
// machine (with Minecraft SRV records for servers on other ports), gets a
// certificate for it from Let's Encrypt over ACME with an HTTP-01 or DNS-01
// check, decides when to renew, and serves the certificate to the panel by
// SNI, falling back to the self-signed certificate for access by IP address.
//
// The package keeps no state of its own and writes no database: callers pass
// in directories, HTTP clients and clocks, and store the returned structs.
// Everything a user may see comes as a Note or Problem with a stable code.
package certs

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"slices"
	"strings"
)

// Owner is the numeric user and group that saved files are given to.
type Owner struct {
	UID int
	GID int
}

// maxNames bounds how many names one certificate covers.
const maxNames = 10

// reservedTLDs only work inside private networks, so no public CA issues
// certificates for them.
var reservedTLDs = map[string]bool{
	"arpa": true, "corp": true, "example": true, "home": true, "internal": true,
	"intranet": true, "invalid": true, "lan": true, "local": true,
	"localdomain": true, "localhost": true, "onion": true, "private": true,
	"test": true,
}

// NormalizeName checks that name is a DNS name a public certificate
// authority can issue a certificate for, and returns it in lower case
// without a trailing dot. Its error is a *Problem with code invalid_name.
func NormalizeName(name string) (string, error) {
	n := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	bad := func(kind string) (string, error) {
		return "", newProblem(nil, CodeInvalidName, map[string]string{"kind": kind, "name": displayName(name)})
	}
	switch {
	case n == "":
		return bad("empty")
	case strings.ContainsAny(n, "/:@ \t"):
		if _, err := netip.ParseAddr(strings.Trim(n, "[]")); err == nil {
			return bad("ip")
		}
		return bad("not_a_name")
	case len(n) > 253:
		return bad("too_long")
	case strings.Contains(n, "*"):
		return bad("wildcard")
	}
	if _, err := netip.ParseAddr(n); err == nil {
		return bad("ip")
	}
	for _, r := range n {
		if r > 0x7e {
			return bad("not_ascii")
		}
	}
	labels := strings.Split(n, ".")
	if len(labels) < 2 {
		return bad("single_label")
	}
	for _, l := range labels {
		if !validLabel(l) {
			return bad("bad_label")
		}
	}
	tld := labels[len(labels)-1]
	if strings.Trim(tld, "0123456789") == "" {
		return bad("numeric_tld")
	}
	if reservedTLDs[tld] {
		return "", newProblem(nil, CodeInvalidName, map[string]string{"kind": "reserved_tld", "name": displayName(name), "tld": tld})
	}
	return n, nil
}

// validLabel reports whether l is a letters-digits-hyphens label of 1 to 63
// characters that neither starts nor ends with a hyphen.
func validLabel(l string) bool {
	if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for i := 0; i < len(l); i++ {
		c := l[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// normalizeNames normalizes names and drops duplicates, keeping their order.
func normalizeNames(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, newProblem(nil, CodeInvalidName, map[string]string{"kind": "empty"})
	}
	var out []string
	for _, raw := range names {
		n, err := NormalizeName(raw)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	if len(out) > maxNames {
		return nil, newProblem(nil, CodeInvalidName, map[string]string{"kind": "too_many"})
	}
	return out, nil
}

// displayName shortens user input for use in a message.
func displayName(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}

// fingerprint is the SHA-256 of a DER certificate in the panel's format:
// upper-case hex bytes separated by colons.
func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	var b strings.Builder
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}
