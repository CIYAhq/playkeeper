package certs

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
)

func TestExplainLocalErrors(t *testing.T) {
	now := time.Date(2025, 1, 18, 20, 0, 0, 0, time.UTC)
	if Explain(nil, now) != nil {
		t.Error("Explain(nil) is not nil")
	}
	orig := newProblem(nil, CodePort80Busy, map[string]string{"port": "80"})
	if got := Explain(fmt.Errorf("issuing: %w", orig), now); got != orig {
		t.Errorf("a wrapped *Problem is not returned as it is: %+v", got)
	}
	leaf := &x509.Certificate{NotAfter: now.Add(-time.Hour)}
	cases := []struct {
		name string
		err  error
		code string
		kind string
	}{
		{"canceled", fmt.Errorf("x: %w", context.Canceled), CodeCanceled, ""},
		{"deadline", context.DeadlineExceeded, CodeCanceled, "timeout"},
		{"clock", &url500{x509.CertificateInvalidError{Cert: leaf, Reason: x509.Expired}}, CodeClockWrong, ""},
		{"unknown authority", x509.UnknownAuthorityError{Cert: leaf}, CodeCAUntrusted, ""},
		{"wrong host", x509.HostnameError{Certificate: leaf, Host: "acme-v02.api.letsencrypt.org"}, CodeCAUntrusted, ""},
		{"refused", &refusedError{"refused a redirect to https://elsewhere.example/"}, CodeCAError, ""},
		{"dns", &net.DNSError{Err: "no such host", Name: "acme-v02.api.letsencrypt.org", IsNotFound: true}, CodeCAUnreachable, ""},
		{"connect", &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, CodeCAUnreachable, ""},
		{"other", errors.New("something odd"), CodeFailed, ""},
		{"authorization without problem", &acme.AuthorizationError{URI: "https://ca/authz/1", Identifier: "mc.example.com"}, CodeCAError, ""},
		{"order without problem", &acme.OrderError{OrderURL: "https://ca/order/1", Status: "invalid"}, CodeCAError, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Explain(c.err, now)
			if p.Code != c.code || p.Params["kind"] != c.kind {
				t.Fatalf("Explain = %s/%s, want %s/%s", p.Code, p.Params["kind"], c.code, c.kind)
			}
			if p.Message == "" || p.Detail == "" {
				t.Errorf("missing message or detail: %+v", p)
			}
			if !errors.Is(p, c.err) {
				t.Errorf("the problem does not wrap the error")
			}
		})
	}
	p := explain(&net.DNSError{Err: "timeout", Name: "acme-v02.api.letsencrypt.org", IsTimeout: true}, situation{host: "acme-v02.api.letsencrypt.org"}, now)
	if p.Message != "Playkeeper could not connect to the certificate authority, acme-v02.api.letsencrypt.org." {
		t.Errorf("unreachable message = %q", p.Message)
	}
	if p := Explain(errors.New("x"), now); p.Message != "Getting the certificate failed." || p.NeedsAction {
		t.Errorf("fallback problem = %+v", p)
	}
}

// url500 wraps an error the way net/http wraps transport errors.
type url500 struct{ err error }

func (u *url500) Error() string { return "Get \"https://ca/dir\": " + u.err.Error() }
func (u *url500) Unwrap() error { return u.err }

func TestFromACMEDetails(t *testing.T) {
	now := time.Date(2025, 1, 18, 20, 0, 0, 0, time.UTC)
	e := &acme.Error{
		StatusCode:  http.StatusBadRequest,
		ProblemType: "urn:ietf:params:acme:error:malformed",
		Detail:      "Error creating new order :: 2 problems",
		Subproblems: []acme.Subproblem{{
			Type:       "urn:ietf:params:acme:error:caa",
			Detail:     "CAA record for blocked.example.com prevents issuance",
			Identifier: &acme.AuthzID{Type: "dns", Value: "blocked.example.com"},
		}},
	}
	p := fromACME(e, situation{}, now)
	if p.Code != CodeCAAForbids || p.Params["name"] != "blocked.example.com" || p.Detail != "CAA record for blocked.example.com prevents issuance" {
		t.Errorf("subproblem = %+v", p)
	}
	if !errors.Is(p, e) {
		t.Error("the problem does not wrap the ACME error")
	}

	e = &acme.Error{StatusCode: 403, ProblemType: "urn:ietf:params:acme:error:userActionRequired", Detail: "Terms of service have changed",
		Instance: "https://letsencrypt.org/agree", Header: http.Header{"Link": {`<https://acme-v02.api.letsencrypt.org/directory>;rel="index"`, `<https://letsencrypt.org/documents/LE-SA-v1.6.pdf>; rel="terms-of-service"`}}}
	if p := fromACME(e, situation{}, now); p.Code != CodeTermsNotAccepted || p.Params["url"] != "https://letsencrypt.org/documents/LE-SA-v1.6.pdf" {
		t.Errorf("terms = %+v", p)
	}
	e = &acme.Error{StatusCode: 403, ProblemType: "urn:ietf:params:acme:error:userActionRequired", Detail: "Please verify your contact details", Instance: "https://ca.example/verify"}
	if p := fromACME(e, situation{}, now); p.Code != CodeCAActionRequired || p.Params["url"] != "https://ca.example/verify" {
		t.Errorf("user action = %+v", p)
	}
	e = &acme.Error{StatusCode: 403, ProblemType: "urn:ietf:params:acme:error:userActionRequired", Detail: "Please verify", Instance: "javascript:alert(1)"}
	if p := fromACME(e, situation{}, now); p.Params["url"] != "" || strings.Contains(p.Hint, "javascript") {
		t.Errorf("an unsafe link is offered: %+v", p)
	}
	e = &acme.Error{StatusCode: 502, ProblemType: "about:blank", Detail: "Bad Gateway"}
	if p := fromACME(e, situation{}, now); p.Code != CodeCAUnavailable || !p.RetryAt.IsZero() {
		t.Errorf("gateway error = %+v", p)
	}
	e = &acme.Error{StatusCode: 429, Detail: "slow down", Header: http.Header{"Retry-After": {"120"}}}
	if p := fromACME(e, situation{}, now); p.Code != CodeRateLimited || !p.RetryAt.Equal(now.Add(2*time.Minute)) {
		t.Errorf("untyped 429 = %+v", p)
	}
	e = &acme.Error{StatusCode: 400, ProblemType: "urn:ietf:params:acme:error:badRevocationReason", Detail: "no"}
	if p := fromACME(e, situation{name: "mc.example.com"}, now); p.Code != CodeCAError || p.Params["name"] != "mc.example.com" {
		t.Errorf("unknown type = %+v", p)
	}
	e = &acme.Error{StatusCode: 403, ProblemType: "urn:ietf:params:acme:error:unauthorized",
		Detail: "Your account is temporarily prevented from requesting certificates for mc.example.com and possibly others. Please visit: https://portal.letsencrypt.org/sfe/v1/unpause?jwt=abc.def.ghi"}
	if p := fromACME(e, situation{}, now); p.Code != CodePaused || p.Params["name"] != "mc.example.com" || p.Params["url"] != "https://portal.letsencrypt.org/sfe/v1/unpause?jwt=abc.def.ghi" || strings.Contains(p.Detail, "abc.def") {
		t.Errorf("paused, whatever the type: %+v", p)
	}

	pebbleDNS01 := map[string]string{
		"No TXT records found for DNS challenge":    CodeDNS01NotVisible,
		"Correct value not found for DNS challenge": CodeDNS01RecordWrong,
		`Error retrieving TXT records for DNS challenge ("DNS lookup for \"_acme-challenge.alex.playkeeper.io.\" returned an unsuccessful response: 2")`: CodeDNSServersFailing,
	}
	for detail, code := range pebbleDNS01 {
		e = &acme.Error{StatusCode: 403, ProblemType: "urn:ietf:params:acme:error:unauthorized", Detail: detail}
		if p := fromACME(e, situation{name: "alex.playkeeper.io", challenge: "dns-01"}, now); p.Code != code {
			t.Errorf("Pebble's %q = %s, want %s", detail, p.Code, code)
		}
	}
}

func TestRateLimitUntil(t *testing.T) {
	now := time.Date(2025, 1, 18, 20, 0, 0, 0, time.UTC)
	limited := func(detail string, h http.Header) *Problem {
		return fromACME(&acme.Error{StatusCode: 429, ProblemType: "urn:ietf:params:acme:error:rateLimited", Detail: detail, Header: h}, situation{}, now)
	}
	cases := []struct {
		name   string
		detail string
		header http.Header
		want   time.Time
	}{
		{"header wins", "retry after 2025-01-18 23:00:00 UTC", http.Header{"Retry-After": {"60"}}, now.Add(time.Minute)},
		{"detail", "too many new orders (300) from this account in the last 3h0m0s, retry after 2025-01-18 21:15:30 UTC: see https://letsencrypt.org/docs/rate-limits/#new-orders-per-account", nil, time.Date(2025, 1, 18, 21, 15, 30, 0, time.UTC)},
		{"no time", "too many requests", nil, now.Add(time.Hour)},
		{"in the past", "retry after 2025-01-18 19:00:00 UTC", nil, now.Add(time.Minute)},
		{"far future", "retry after 2025-03-01 00:00:00 UTC", nil, now.Add(8 * 24 * time.Hour)},
		{"http date", "limited", http.Header{"Retry-After": {"Sat, 18 Jan 2025 22:00:00 GMT"}}, time.Date(2025, 1, 18, 22, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		p := limited(c.detail, c.header)
		if !p.RetryAt.Equal(c.want) || p.Params["until"] != c.want.Format(time.RFC3339) {
			t.Errorf("%s: RetryAt %s (until %q), want %s", c.name, p.RetryAt, p.Params["until"], c.want)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2025, 1, 18, 20, 0, 0, 0, time.UTC)
	for v, want := range map[string]time.Duration{
		"":                              0,
		"120":                           2 * time.Minute,
		"soon":                          0,
		"Sat, 18 Jan 2025 20:05:00 GMT": 5 * time.Minute,
	} {
		if got := retryAfter(v, now); got != want {
			t.Errorf("retryAfter(%q) = %s, want %s", v, got, want)
		}
	}
}

func TestTermsLinkAndSafeURL(t *testing.T) {
	h := http.Header{"Link": {`<https://ca.example/dir>;rel="index", <https://ca.example/terms.pdf> ; rel = "terms-of-service"`}}
	if got := termsLink(h); got != "https://ca.example/terms.pdf" {
		t.Errorf("termsLink = %q", got)
	}
	if got := termsLink(http.Header{"Link": {`<http://ca.example/terms>;rel="terms-of-service"`}}); got != "" {
		t.Errorf("an http link is offered: %q", got)
	}
	for raw, want := range map[string]string{
		"https://letsencrypt.org/repository/":    "https://letsencrypt.org/repository/",
		"http://letsencrypt.org/":                "",
		"https://":                               "",
		"ftp://x/":                               "",
		"https://x/" + strings.Repeat("a", 2100): "",
	} {
		if got := safeURL(raw); got != want {
			t.Errorf("safeURL(%.40q) = %q, want %q", raw, got, want)
		}
	}
}

func TestLeadingIP(t *testing.T) {
	for detail, want := range map[string]string{
		"203.0.113.10: Fetching http://mc.example.com/.well-known/acme-challenge/x: Connection refused":                              "203.0.113.10",
		"During secondary validation: 2001:db8::10: Fetching http://mc.example.com/.well-known/acme-challenge/x: Connection refused": "2001:db8::10",
		"[2001:db8::10]: Fetching http://mc.example.com/":                                                                            "2001:db8::10",
		"Fetching http://mc.example.com/: Timeout":                                                                                   "",
		"DNS problem: NXDOMAIN looking up A":                                                                                         "",
		"abc: def":                                                                                                                   "",
		"999.1.1.1: x":                                                                                                               "",
	} {
		if got := leadingIP(detail); got != want {
			t.Errorf("leadingIP(%.50q) = %q, want %q", detail, got, want)
		}
	}
}

func TestCleanDetail(t *testing.T) {
	in := "line one\n\tline\x00two \u202egnirts\u202c see https://portal.letsencrypt.org/sfe/v1/unpause?jwt=eyJhbGci.eyJzdWIi.sig&x=1"
	got := cleanDetail(in)
	want := "line one line two gnirts see https://portal.letsencrypt.org/sfe/v1/unpause?jwt=[redacted]&x=1"
	if got != want {
		t.Errorf("cleanDetail = %q\nwant %q", got, want)
	}
	long := cleanDetail(strings.Repeat("é", 500))
	if r := []rune(long); len(r) != 401 || r[400] != '…' {
		t.Errorf("long detail has %d runes", len(r))
	}
}
