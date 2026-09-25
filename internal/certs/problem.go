package certs

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/acme"
)

// Note is something to show the admin. Code is stable and, with Params, is
// what the UI translates; Params["kind"], when set, picks a variant of the
// message. Message and Hint are the English text.
type Note struct {
	Code    string            `json:"code"`
	Params  map[string]string `json:"params,omitempty"`
	Message string            `json:"message"`
	Hint    string            `json:"hint,omitempty"`
}

// Problem is a failure explained for the admin. Operations in this package
// return their errors as a *Problem.
type Problem struct {
	Note
	// RetryAt, when set, is the earliest time another attempt can succeed:
	// before it the certificate authority refuses again (a rate limit or
	// maintenance), and attempts may count against its limits.
	RetryAt time.Time `json:"retryAt,omitzero"`
	// NeedsAction means the admin has to change something, such as a DNS
	// record or a firewall rule, before trying again can help.
	NeedsAction bool `json:"needsAction,omitempty"`
	// Detail is the underlying error in the certificate authority's or the
	// system's own words, cleaned up for a "details" section and logs.
	Detail string `json:"detail,omitempty"`

	err error
}

func (p *Problem) Error() string { return p.Message }

func (p *Problem) Unwrap() error { return p.err }

// Codes of Notes and Problems.
const (
	CodeNameOK           = "name_ok"
	CodeNameMissing      = "name_missing"
	CodeNameElsewhere    = "name_elsewhere"
	CodeNameMixed        = "name_mixed"
	CodeNameProxied      = "name_proxied"
	CodeNamePrivate      = "name_private"
	CodeNameUnverified   = "name_unverified"
	CodeNameLookupFailed = "name_lookup_failed"

	CodeSRVOK           = "srv_ok"
	CodeSRVMissing      = "srv_missing"
	CodeSRVWrong        = "srv_wrong"
	CodeSRVConflict     = "srv_conflict"
	CodeSRVLookupFailed = "srv_lookup_failed"

	CodeInvalidName  = "invalid_name"
	CodeInvalidEmail = "invalid_email"
	CodeInvalidPlan  = "invalid_plan"
	CodeConfig       = "config"

	CodeRateLimited       = "rate_limited"
	CodePaused            = "paused"
	CodeTermsNotAccepted  = "terms_not_accepted"
	CodeCAActionRequired  = "ca_action_required"
	CodeCANeedsAccount    = "ca_needs_account"
	CodeAccountDisabled   = "account_disabled"
	CodeAccountKeyDamaged = "account_key_damaged"
	CodeNameRefused       = "name_refused"
	CodeEmailRefused      = "email_refused"

	CodePort80Unreachable   = "port80_unreachable"
	CodeWrongAnswer         = "challenge_wrong_answer"
	CodeDNSNoRecord         = "dns_no_record"
	CodeDNSServersFailing   = "dns_servers_failing"
	CodeCAAForbids          = "caa_forbids"
	CodeValidationTimeout   = "validation_timeout"
	CodeChallengeNotOffered = "challenge_not_offered"
	CodeDNS01PublishFailed  = "dns01_publish_failed"
	CodeDNS01NotVisible     = "dns01_not_visible"
	CodeDNS01RecordWrong    = "dns01_record_wrong"

	CodePort80Busy     = "port80_busy"
	CodePort80Denied   = "port80_denied"
	CodePort80Failed   = "port80_failed"
	CodeSaveFailed     = "save_failed"
	CodeBadCertificate = "bad_certificate"
	CodeCanceled       = "canceled"

	CodeCAUnreachable = "ca_unreachable"
	CodeClockWrong    = "clock_wrong"
	CodeCAUntrusted   = "ca_untrusted"
	CodeCAUnavailable = "ca_unavailable"
	CodeCAError       = "ca_error"
	CodeFailed        = "failed"
)

type text struct {
	msg, hint string
	action    bool
}

// catalog is the English text of every code, and of code.kind variants.
var catalog = map[string]text{
	CodeNameOK:      {msg: "{name} points at this server."},
	CodeNameMissing: {msg: "{name} has no DNS record yet.", hint: "At your DNS provider, add an A record for {name} with the value {ipv4}. New records can take a few minutes to appear.", action: true},
	CodeNameElsewhere: {msg: "{name} points at {found}, which is not this server.",
		hint: "At your DNS provider, change the A record for {name} to {ipv4} and remove records that point elsewhere. Changes can take a while to appear, up to the record's TTL.", action: true},
	CodeNameMixed: {msg: "{name} points at several addresses, and {elsewhere} is not this server.",
		hint: "Remove the records for {name} that point at {elsewhere}, or players and Let's Encrypt may reach the wrong machine.", action: true},
	CodeNameMixed + ".stale_aaaa": {msg: "{name} has an IPv6 (AAAA) record, {elsewhere}, that is not this server.",
		hint: "Delete that AAAA record, or change it to {ipv6}. Let's Encrypt tries IPv6 first, so a wrong AAAA record makes its check fail.", action: true},
	CodeNameProxied: {msg: "{name} goes through Cloudflare's proxy, so Minecraft players cannot connect through it.",
		hint: "In Cloudflare's DNS settings, set the record for {name} to \"DNS only\" (the grey cloud).", action: true},
	CodeNamePrivate: {msg: "{name} points at {found}, a private address that only works inside a local network.",
		hint: "Use this server's public IP address, {ipv4}, in the A record.", action: true},
	CodeNameUnverified: {msg: "{name} points at {found}, but Playkeeper could not find this server's public address to compare.",
		hint: "Make sure {found} is this server's public IP address, as shown by your hosting provider."},
	CodeNameLookupFailed: {msg: "Playkeeper could not look up {name} in DNS.",
		hint: "Try again in a minute. If it keeps failing, check the domain's settings at your DNS provider."},

	CodeSRVOK:      {msg: "The SRV record for {host} sends players to port {port}."},
	CodeSRVMissing: {msg: "The SRV record for {host} does not exist yet.", hint: "Add it at your DNS provider with the values shown. New records can take a few minutes to appear.", action: true},
	CodeSRVWrong: {msg: "The SRV record for {host} sends players to {found} instead of {target} port {port}.",
		hint: "Change it to the values shown, and delete any other SRV records for {host}.", action: true},
	CodeSRVConflict: {msg: "An SRV record for {host} sends players to {found} instead of this server's port {port}.",
		hint: "Delete the SRV record {record} at your DNS provider.", action: true},
	CodeSRVLookupFailed: {msg: "Playkeeper could not look up the SRV record for {host}.", hint: "Try again in a minute."},

	CodeInvalidName:                   {msg: "{name} is not a domain name Let's Encrypt can issue a certificate for.", hint: "Enter a name such as mc.example.com.", action: true},
	CodeInvalidName + ".empty":        {msg: "Enter a domain name, such as mc.example.com.", action: true},
	CodeInvalidName + ".not_a_name":   {msg: "{name} is not just a domain name.", hint: "Enter only the name, such as mc.example.com, without https://, a port or a path.", action: true},
	CodeInvalidName + ".too_long":     {msg: "That name is too long for a domain name.", hint: "Domain names have at most 253 characters.", action: true},
	CodeInvalidName + ".wildcard":     {msg: "Wildcard names such as {name} are not supported.", hint: "Enter the exact name players and the dashboard will use, such as mc.example.com.", action: true},
	CodeInvalidName + ".ip":           {msg: "{name} is an IP address, not a domain name.", hint: "Point a domain name such as mc.example.com at this address and enter that name instead.", action: true},
	CodeInvalidName + ".not_ascii":    {msg: "{name} contains characters outside plain ASCII.", hint: "Enter the name's xn-- form (punycode), which your domain registrar shows.", action: true},
	CodeInvalidName + ".single_label": {msg: "{name} is not a full domain name.", hint: "Enter a name with a dot, such as mc.example.com.", action: true},
	CodeInvalidName + ".bad_label": {msg: "{name} is not a valid domain name.",
		hint: "Each part between the dots may only use letters, digits and hyphens, must not start or end with a hyphen, and has at most 63 characters.", action: true},
	CodeInvalidName + ".numeric_tld":    {msg: "{name} ends in a number, so it is not a domain name.", hint: "Enter a name such as mc.example.com.", action: true},
	CodeInvalidName + ".reserved_tld":   {msg: "{name} ends in {tld}, which only works inside private networks.", hint: "Let's Encrypt only issues certificates for public domain names. Use a name you registered, such as mc.example.com.", action: true},
	CodeInvalidName + ".too_many":       {msg: "One certificate can cover at most 10 names.", action: true},
	CodeInvalidEmail:                    {msg: "{email} is not a valid email address.", hint: "Enter an address such as you@example.com, or leave it empty: it is optional.", action: true},
	CodeInvalidPlan:                     {msg: "The join addresses are not set up correctly.", action: true},
	CodeInvalidPlan + ".duplicate_host": {msg: "Two servers use the join address {host}.", hint: "Give every server its own join address.", action: true},
	CodeInvalidPlan + ".duplicate_port": {msg: "Two servers use port {port}.", hint: "Every server on this machine needs its own port.", action: true},
	CodeInvalidPlan + ".bad_port":       {msg: "{port} is not a valid port.", hint: "Ports are numbers from 1 to 65535.", action: true},
	CodeInvalidPlan + ".too_many":       {msg: "There are too many servers for one address plan.", action: true},
	CodeInvalidPlan + ".bad_address":    {msg: "The address for the A or AAAA record is not a valid IPv4 or IPv6 address.", action: true},
	CodeConfig:                          {msg: "Playkeeper's certificate settings are incomplete.", hint: "Report this, with the details.", action: true},
	CodeConfig + ".directory_url":       {msg: "The certificate authority address is not a valid https:// URL.", hint: "Remove the setting to use Let's Encrypt, or enter its https:// directory URL.", action: true},
	CodeConfig + ".no_account_key":      {msg: "No file for the Let's Encrypt account key is set.", hint: "Report this, with the details.", action: true},
	CodeConfig + ".no_challenge":        {msg: "No way to prove control of the name (HTTP-01 or DNS-01) is set up.", hint: "Report this, with the details.", action: true},
	CodeConfig + ".no_dir":              {msg: "No folder to save the certificate in is set.", hint: "Report this, with the details.", action: true},

	CodeRateLimited: {msg: "Let's Encrypt is limiting requests from this server for now.", hint: "Playkeeper can try again after {until}."},
	CodeRateLimited + ".duplicate_certificate": {msg: "Let's Encrypt has already issued the most certificates it allows for exactly these names this week.",
		hint: "Playkeeper can try again after {until}. A certificate you already have keeps working until it expires."},
	CodeRateLimited + ".certificates_per_domain": {msg: "Let's Encrypt has already issued the most certificates it allows for {domain} this week.",
		hint: "Playkeeper can try again after {until}."},
	CodeRateLimited + ".failed_validations": {msg: "Let's Encrypt saw too many failed checks for {name} in the last hour.",
		hint: "Fix the problem the last check reported, then try again after {until}.", action: true},
	CodeRateLimited + ".new_orders":   {msg: "Playkeeper asked Let's Encrypt for too many certificates in the last few hours.", hint: "Playkeeper can try again after {until}."},
	CodeRateLimited + ".new_accounts": {msg: "Too many Let's Encrypt accounts were created from this server's IP address recently.", hint: "Playkeeper can try again after {until}."},
	CodePaused: {msg: "Let's Encrypt paused certificates for {name} from this Playkeeper after many failed attempts.",
		hint: "Fix the problem that made the checks fail, then open Let's Encrypt's unpause page and try again.", action: true},
	CodeTermsNotAccepted: {msg: "Let's Encrypt's terms of service have to be accepted before it issues certificates.",
		hint: "Read the terms at {url} and accept them in Playkeeper, then try again.", action: true},
	CodeCAActionRequired: {msg: "Let's Encrypt needs something from you before it continues.", hint: "Open {url} for instructions, then try again.", action: true},
	CodeCANeedsAccount: {msg: "This certificate authority needs an account set up on its website first, which Playkeeper does not support.",
		hint: "Use Let's Encrypt, the default.", action: true},
	CodeAccountDisabled: {msg: "The Let's Encrypt account Playkeeper uses is no longer valid.",
		hint: "Reset the Let's Encrypt account in Playkeeper and try again; a new account is created.", action: true},
	CodeAccountKeyDamaged: {msg: "The file with Playkeeper's Let's Encrypt account key is damaged.",
		hint: "Reset the Let's Encrypt account in Playkeeper and try again; a new account is created.", action: true},
	CodeNameRefused: {msg: "Let's Encrypt will not issue certificates for {name}.",
		hint: "Let's Encrypt refuses some names by policy, such as names on its blocklist or without a public domain ending. Use a different name.", action: true},
	CodeEmailRefused: {msg: "Let's Encrypt did not accept the email address.",
		hint: "Use a working address at a real domain, or leave the email empty: it is optional.", action: true},

	CodePort80Unreachable: {msg: "Let's Encrypt could not connect to {name} on port 80.",
		hint: "Allow incoming TCP port 80 in this server's firewall and in your hosting provider's firewall, and check that {name} points at this server. Playkeeper only opens port 80 for the few seconds of the check.", action: true},
	CodeWrongAnswer: {msg: "Something other than Playkeeper answered Let's Encrypt on port 80 for {name}.",
		hint: "Check that {name} points at this server and not at a proxy or another host, and that no other web server handles port 80 here.", action: true},
	CodeDNSNoRecord: {msg: "Let's Encrypt found no DNS record for {name}.",
		hint: "At your DNS provider, add an A record for {name} pointing at this server, wait a few minutes, then try again.", action: true},
	CodeDNSServersFailing: {msg: "Let's Encrypt could not get an answer from the DNS servers for {name}.",
		hint: "This is usually a problem at your DNS provider, such as broken DNSSEC settings. Check the domain's DNS settings, or wait and try again."},
	CodeCAAForbids: {msg: "A CAA record on your domain does not allow Let's Encrypt to issue certificates for {name}.",
		hint: "At your DNS provider, add a CAA record that allows letsencrypt.org, or delete the CAA records, then try again.", action: true},
	CodeValidationTimeout:   {msg: "Let's Encrypt did not finish checking {name} in time.", hint: "Try again in a few minutes."},
	CodeChallengeNotOffered: {msg: "Let's Encrypt did not offer the {challenge} check for {name}.", hint: "Try again later. If it keeps happening, report the details."},
	CodeDNS01PublishFailed:  {msg: "Playkeeper could not create the DNS record {fqdn} that Let's Encrypt checks.", hint: "Try again in a few minutes."},
	CodeDNS01NotVisible:     {msg: "The DNS record {fqdn} that Let's Encrypt checks did not appear in time.", hint: "DNS changes can take a few minutes to spread. Try again shortly."},
	CodeDNS01RecordWrong: {msg: "Let's Encrypt found a different value in the DNS record {fqdn}.",
		hint: "Wait a few minutes for old values to expire and try again. If you added _acme-challenge records by hand, delete them."},

	CodePort80Busy: {msg: "Another program on this server is already using port {port}, so Playkeeper cannot answer Let's Encrypt's check.",
		hint: "Stop the other web server, such as nginx, Apache or Caddy, and try again.", action: true},
	CodePort80Denied: {msg: "Playkeeper is not allowed to open port {port} on this server.",
		hint: "Update Playkeeper: its agent service needs permission to open port 80 (the CAP_NET_BIND_SERVICE capability).", action: true},
	CodePort80Failed:                {msg: "Playkeeper could not open port {port} for Let's Encrypt's check.", hint: "Try again. If it keeps failing, report the details."},
	CodeSaveFailed:                  {msg: "Playkeeper got the certificate but could not save it.", hint: "Check the free disk space, then try again.", action: true},
	CodeSaveFailed + ".account_key": {msg: "Playkeeper could not save its Let's Encrypt account key.", hint: "Check the free disk space, then try again.", action: true},
	CodeBadCertificate:              {msg: "Let's Encrypt sent a certificate Playkeeper cannot use.", hint: "Try again later. If it keeps happening, report the details."},
	CodeCanceled:                    {msg: "Getting the certificate was stopped before it finished.", hint: "Try again."},
	CodeCanceled + ".timeout":       {msg: "Getting the certificate took too long and was stopped.", hint: "Try again in a few minutes."},
	CodeCAUnreachable:               {msg: "Playkeeper could not connect to the certificate authority, {server}.", hint: "Check this server's internet connection and that outgoing HTTPS (port 443) is allowed, then try again."},
	CodeClockWrong:                  {msg: "This server's clock is wrong, so secure connections to Let's Encrypt fail.", hint: "Turn on time synchronisation, for example with timedatectl set-ntp true, and try again.", action: true},
	CodeCAUntrusted:                 {msg: "Playkeeper could not verify that it is talking to Let's Encrypt.", hint: "Something between this server and the internet, such as a proxy or firewall, may be intercepting secure connections. Check the network, then try again.", action: true},
	CodeCAUnavailable:               {msg: "Let's Encrypt is having problems or is down for maintenance.", hint: "Playkeeper tries again later by itself; nothing is needed from you."},
	CodeCAError:                     {msg: "Let's Encrypt refused the request.", hint: "Try again later. If it keeps failing, report the details."},
	CodeFailed:                      {msg: "Getting the certificate failed.", hint: "Try again later. If it keeps failing, report the details."},
}

// fallbackText fills placeholders whose parameter is unknown.
var fallbackText = map[string]string{
	"name":      "this name",
	"host":      "this address",
	"server":    "Let's Encrypt",
	"ipv4":      "this server's public IPv4 address",
	"ipv6":      "this server's IPv6 address",
	"found":     "another address",
	"elsewhere": "another address",
	"fqdn":      "_acme-challenge",
	"until":     "a while",
	"url":       "the certificate authority's website",
	"domain":    "this domain",
	"port":      "80",
	"target":    "this server",
	"record":    "for this name",
	"challenge": "requested",
	"tld":       "a private ending",
	"email":     "That",
}

var rePlaceholder = regexp.MustCompile(`\{([a-z0-9]+)\}`)

// newProblem builds a Problem from the catalog. Empty params are dropped.
func newProblem(err error, code string, params map[string]string) *Problem {
	n, action := note(code, params)
	p := &Problem{Note: n, NeedsAction: action, err: err}
	if err != nil {
		p.Detail = cleanDetail(err.Error())
	}
	return p
}

func note(code string, params map[string]string) (Note, bool) {
	for k, v := range params {
		if v == "" {
			delete(params, k)
		}
	}
	t := catalog[code]
	if v, ok := catalog[code+"."+params["kind"]]; ok && params["kind"] != "" {
		t = v
	}
	if len(params) == 0 {
		params = nil
	}
	return Note{Code: code, Params: params, Message: fill(t.msg, params), Hint: fill(t.hint, params)}, t.action
}

func fill(s string, params map[string]string) string {
	return rePlaceholder.ReplaceAllStringFunc(s, func(m string) string {
		key := m[1 : len(m)-1]
		v, ok := params[key]
		if !ok {
			return fallbackText[key]
		}
		if key == "until" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				return t.UTC().Format("2006-01-02 15:04 UTC")
			}
		}
		return v
	})
}

// situation is what the caller knows about the step that failed.
type situation struct {
	challenge string // "http-01", "dns-01" or ""
	name      string // the name being checked
	host      string // the certificate authority's host
}

// Explain turns an error from this package, the ACME client or the network
// into a Problem. A *Problem is returned as it is; nil stays nil.
func Explain(err error, now time.Time) *Problem {
	return explain(err, situation{}, now)
}

func explain(err error, s situation, now time.Time) *Problem {
	if err == nil {
		return nil
	}
	var p *Problem
	if errors.As(err, &p) {
		return p
	}
	if errors.Is(err, context.Canceled) {
		return newProblem(err, CodeCanceled, nil)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return newProblem(err, CodeCanceled, map[string]string{"kind": "timeout"})
	}
	var authzErr *acme.AuthorizationError
	if errors.As(err, &authzErr) {
		if s.name == "" {
			s.name = authzErr.Identifier
		}
		for _, e := range authzErr.Errors {
			var ae *acme.Error
			if errors.As(e, &ae) {
				return fromACME(ae, s, now)
			}
		}
		return newProblem(err, CodeCAError, map[string]string{"name": s.name})
	}
	var orderErr *acme.OrderError
	if errors.As(err, &orderErr) {
		if orderErr.Problem != nil {
			return fromACME(orderErr.Problem, s, now)
		}
		return newProblem(err, CodeCAError, nil)
	}
	var ae *acme.Error
	if errors.As(err, &ae) {
		return fromACME(ae, s, now)
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) && invalid.Reason == x509.Expired {
		return newProblem(err, CodeClockWrong, nil)
	}
	var unknownAuthority x509.UnknownAuthorityError
	var wrongHost x509.HostnameError
	if errors.As(err, &unknownAuthority) || errors.As(err, &wrongHost) || errors.As(err, &invalid) {
		return newProblem(err, CodeCAUntrusted, nil)
	}
	var refused *refusedError
	if errors.As(err, &refused) {
		return newProblem(err, CodeCAError, nil)
	}
	var dnsErr *net.DNSError
	var opErr *net.OpError
	var netErr net.Error
	if errors.As(err, &dnsErr) || errors.As(err, &opErr) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return newProblem(err, CodeCAUnreachable, map[string]string{"server": s.host})
	}
	return newProblem(err, CodeFailed, nil)
}

var (
	reRetryAfter = regexp.MustCompile(`retry after (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) UTC`)
	reLeadingIP  = regexp.MustCompile(`^(?:During secondary validation: )?\[?([0-9A-Fa-f:.]+)\]?: `)
	reQuoted     = regexp.MustCompile(`"([^"\s]+)"`)
	reURL        = regexp.MustCompile(`https://[^\s"'<>]+`)
	reJWT        = regexp.MustCompile(`(jwt=)[^\s&"'<>]+`)
	rePausedName = regexp.MustCompile(`certificates for ([^\s,"]+) and possibly others`)
)

// fromACME explains a problem document from the certificate authority. The
// texts it recognises are Let's Encrypt's (Boulder) and Pebble's.
func fromACME(e *acme.Error, s situation, now time.Time) *Problem {
	typ, detail := e.ProblemType, e.Detail
	kind := problemKind(typ)
	if len(e.Subproblems) > 0 {
		sp := e.Subproblems[0]
		if s.name == "" && sp.Identifier != nil {
			s.name = sp.Identifier.Value
		}
		if (kind == "compound" || kind == "malformed") && sp.Type != "" {
			typ, detail = sp.Type, sp.Detail
			kind = problemKind(typ)
		}
	}
	lower := strings.ToLower(detail)
	named := func(code string, extra ...string) *Problem {
		params := map[string]string{"name": s.name}
		for i := 0; i+1 < len(extra); i += 2 {
			params[extra[i]] = extra[i+1]
		}
		return newProblem(e, code, params)
	}
	txt := s.challenge == "dns-01" || (s.challenge == "" && (strings.Contains(detail, "TXT") || strings.Contains(lower, "_acme-challenge.")))
	fqdn := ""
	if s.name != "" {
		fqdn = "_acme-challenge." + s.name
	}
	if strings.Contains(lower, "unpause") {
		p := pausedProblem(e, detail, s)
		p.Detail = cleanDetail(detail)
		return p
	}
	var p *Problem
	switch kind {
	case "ratelimited":
		p = rateLimitProblem(e, detail, s, now)
	case "dns":
		switch {
		case txt && (strings.Contains(detail, "NXDOMAIN") || strings.Contains(lower, "no txt record")):
			p = named(CodeDNS01NotVisible, "fqdn", fqdn)
		case containsAny(lower, "servfail", "timed out", "timeout", "refused", "networking error"):
			p = named(CodeDNSServersFailing)
		case strings.Contains(detail, "NXDOMAIN") || strings.Contains(lower, "no valid ip addresses") || strings.Contains(lower, "no valid a records"):
			p = named(CodeDNSNoRecord)
		case txt:
			p = named(CodeDNS01NotVisible, "fqdn", fqdn)
		default:
			p = named(CodeDNSServersFailing)
		}
	case "connection":
		if s.challenge == "dns-01" {
			p = named(CodeDNSServersFailing)
		} else {
			p = named(CodePort80Unreachable, "ip", leadingIP(detail))
		}
	case "unauthorized", "incorrectresponse":
		switch {
		case txt:
			switch {
			case strings.Contains(lower, "no txt record"):
				p = named(CodeDNS01NotVisible, "fqdn", fqdn)
			case strings.Contains(lower, "error retrieving txt"):
				p = named(CodeDNSServersFailing)
			default:
				p = named(CodeDNS01RecordWrong, "fqdn", fqdn)
			}
		case s.challenge == "http-01" || containsAny(lower, "acme-challenge", "invalid response", "key authorization", "status code"):
			p = named(CodeWrongAnswer, "ip", leadingIP(detail))
		case strings.Contains(lower, "account"):
			p = named(CodeAccountDisabled)
		default:
			p = named(CodeCAError)
		}
	case "tls":
		p = named(CodeWrongAnswer, "ip", leadingIP(detail))
	case "caa":
		p = named(CodeCAAForbids)
	case "rejectedidentifier", "unsupportedidentifier":
		if s.name == "" {
			if m := reQuoted.FindStringSubmatch(detail); m != nil {
				s.name = m[1]
			}
		}
		p = named(CodeNameRefused)
	case "invalidcontact", "unsupportedcontact":
		p = newProblem(e, CodeEmailRefused, nil)
	case "useractionrequired", "agreementrequired":
		terms := termsLink(e.Header)
		switch {
		case terms != "" || kind == "agreementrequired" || strings.Contains(lower, "terms"):
			if terms == "" {
				terms = safeURL(e.Instance)
			}
			p = newProblem(e, CodeTermsNotAccepted, map[string]string{"url": terms})
		default:
			p = newProblem(e, CodeCAActionRequired, map[string]string{"url": safeURL(e.Instance)})
		}
	case "externalaccountrequired":
		p = newProblem(e, CodeCANeedsAccount, nil)
	case "accountdoesnotexist":
		p = newProblem(e, CodeAccountDisabled, nil)
	case "serverinternal", "badnonce":
		p = unavailableProblem(e, now)
	default:
		switch {
		case e.StatusCode == http.StatusTooManyRequests:
			p = rateLimitProblem(e, detail, s, now)
		case e.StatusCode >= 500:
			p = unavailableProblem(e, now)
		default:
			p = named(CodeCAError)
		}
	}
	p.Detail = cleanDetail(detail)
	return p
}

func problemKind(typ string) string {
	typ = strings.ToLower(typ)
	return typ[strings.LastIndex(typ, ":")+1:]
}

// pausedProblem explains that Let's Encrypt paused the account for a name
// after too many failures; its message links to an unpause page. The link
// carries a token for the account: it stays in Params for the admin's
// button, cleanDetail redacts it from Detail, and Params must not be logged.
func pausedProblem(e *acme.Error, detail string, s situation) *Problem {
	link := ""
	for _, u := range reURL.FindAllString(detail, -1) {
		if strings.Contains(u, "unpause") {
			link = safeURL(strings.TrimRight(u, ".,;)"))
			break
		}
	}
	name := s.name
	if m := rePausedName.FindStringSubmatch(detail); name == "" && m != nil {
		name, _ = NormalizeName(m[1])
	}
	return newProblem(e, CodePaused, map[string]string{"name": name, "url": link})
}

func rateLimitProblem(e *acme.Error, detail string, s situation, now time.Time) *Problem {
	lower := strings.ToLower(detail)
	until := now.Add(time.Hour)
	if d := retryAfter(e.Header.Get("Retry-After"), now); d > 0 {
		until = now.Add(d)
	} else if m := reRetryAfter.FindStringSubmatch(detail); m != nil {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1], time.UTC); err == nil {
			until = t
		}
	}
	until = clampTime(until, now.Add(time.Minute), now.Add(8*24*time.Hour))
	params := map[string]string{"name": s.name, "until": until.UTC().Format(time.RFC3339)}
	quoted := ""
	if m := reQuoted.FindStringSubmatch(detail); m != nil {
		quoted = m[1]
	}
	switch {
	case containsAny(lower, "exact-set", "exact set", "duplicate certificate"):
		params["kind"] = "duplicate_certificate"
	case containsAny(lower, "per-registered-domain", "registered domain", "certificates already issued for", "already issued for \""):
		params["kind"] = "certificates_per_domain"
		params["domain"] = quoted
	case containsAny(lower, "failed authorizations", "authorization-failures", "failed validation"):
		params["kind"] = "failed_validations"
		if params["name"] == "" {
			params["name"] = quoted
		}
	case containsAny(lower, "new orders", "new-orders"):
		params["kind"] = "new_orders"
	case containsAny(lower, "registrations", "new-accounts"):
		params["kind"] = "new_accounts"
	}
	p := newProblem(e, CodeRateLimited, params)
	p.RetryAt = until
	return p
}

func unavailableProblem(e *acme.Error, now time.Time) *Problem {
	p := newProblem(e, CodeCAUnavailable, nil)
	if e.Header != nil {
		if d := retryAfter(e.Header.Get("Retry-After"), now); d > 0 {
			p.RetryAt = now.Add(min(d, 24*time.Hour))
		}
	}
	return p
}

// retryAfter reads a Retry-After header: seconds or an HTTP date.
func retryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return t.Sub(now)
	}
	return 0
}

// termsLink finds the terms-of-service URL in Link headers.
func termsLink(h http.Header) string {
	for _, v := range h.Values("Link") {
		for _, part := range strings.Split(v, ",") {
			target, params, ok := strings.Cut(part, ";")
			if !ok || !strings.Contains(strings.ReplaceAll(params, " ", ""), `rel="terms-of-service"`) {
				continue
			}
			return safeURL(strings.Trim(strings.TrimSpace(target), "<>"))
		}
	}
	return ""
}

// safeURL keeps only https URLs, which are safe to show as links.
func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || len(raw) > 2048 {
		return ""
	}
	return u.String()
}

// leadingIP is the address Let's Encrypt connected to, from the start of its
// message ("203.0.113.10: Fetching http://…").
func leadingIP(detail string) string {
	m := reLeadingIP.FindStringSubmatch(detail)
	if m == nil {
		return ""
	}
	a, err := netip.ParseAddr(m[1])
	if err != nil {
		return ""
	}
	return a.String()
}

// cleanDetail makes an outside error message safe to show: no control or
// formatting characters, no unpause tokens, at most 400 characters.
func cleanDetail(s string) string {
	s = reJWT.ReplaceAllString(s, "${1}[redacted]")
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.In(r, unicode.Cc, unicode.Cf) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if r := []rune(out); len(r) > 400 {
		out = string(r[:400]) + "…"
	}
	return out
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func clampTime(t, lo, hi time.Time) time.Time {
	if t.Before(lo) {
		return lo
	}
	if t.After(hi) {
		return hi
	}
	return t
}
