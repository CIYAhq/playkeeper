package certs

import (
	"crypto/sha256"
	"regexp"
	"strings"
	"testing"
)

func TestNormalizeName(t *testing.T) {
	ok := []struct{ in, want string }{
		{"mc.example.com", "mc.example.com"},
		{"  MC.Example.COM.  ", "mc.example.com"},
		{"alex.playkeeper.io", "alex.playkeeper.io"},
		{"a-b.c-d.co.uk", "a-b.c-d.co.uk"},
		{"xn--bcher-kva.de", "xn--bcher-kva.de"},
		{"1.2.3.example.io", "1.2.3.example.io"},
		{strings.Repeat("a", 63) + ".com", strings.Repeat("a", 63) + ".com"},
	}
	for _, c := range ok {
		got, err := NormalizeName(c.in)
		if err != nil || got != c.want {
			t.Errorf("NormalizeName(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}

	bad := []struct{ in, kind string }{
		{"", "empty"},
		{"   ", "empty"},
		{"https://mc.example.com", "not_a_name"},
		{"mc.example.com:25565", "not_a_name"},
		{"mc.example.com/panel", "not_a_name"},
		{"admin@example.com", "not_a_name"},
		{"mc example.com", "not_a_name"},
		{"[2001:db8::1]", "ip"},
		{"2001:db8::1", "ip"},
		{"203.0.113.10", "ip"},
		{strings.Repeat("a.", 127) + "com", "too_long"},
		{"*.example.com", "wildcard"},
		{"bücher.de", "not_ascii"},
		{"localhost", "single_label"},
		{"minecraft", "single_label"},
		{"-mc.example.com", "bad_label"},
		{"mc-.example.com", "bad_label"},
		{"mc..example.com", "bad_label"},
		{"mc_1.example.com", "bad_label"},
		{strings.Repeat("a", 64) + ".com", "bad_label"},
		{"mc.example.123", "numeric_tld"},
		{"mc.example.test", "reserved_tld"},
		{"nas.local", "reserved_tld"},
		{"xn--bcher-kva.example", "reserved_tld"},
		{"router.lan", "reserved_tld"},
		{"panel.internal", "reserved_tld"},
	}
	for _, c := range bad {
		_, err := NormalizeName(c.in)
		if err == nil {
			t.Errorf("NormalizeName(%q) accepted it, want kind %s", c.in, c.kind)
			continue
		}
		p := problemOf(t, err)
		if p.Code != CodeInvalidName || p.Params["kind"] != c.kind {
			t.Errorf("NormalizeName(%q) = %s/%s, want %s/%s", c.in, p.Code, p.Params["kind"], CodeInvalidName, c.kind)
		}
		if !p.NeedsAction || p.Message == "" || strings.Contains(p.Message, "{") {
			t.Errorf("NormalizeName(%q): unhelpful problem %+v", c.in, p)
		}
	}

	_, err := NormalizeName("mc.example.test")
	if p := problemOf(t, err); p.Params["tld"] != ".test" || p.Message != "mc.example.test ends in .test, which only works inside private networks." {
		t.Errorf("reserved TLD problem = %+v", p)
	}
	_, err = NormalizeName(strings.Repeat("x", 300) + ":1")
	if p := problemOf(t, err); len([]rune(p.Params["name"])) > 81 {
		t.Errorf("long input is shown in full: %d runes", len([]rune(p.Params["name"])))
	}
}

func TestNormalizeNames(t *testing.T) {
	got, err := normalizeNames([]string{"MC.example.com", "www.example.com", "mc.example.com."})
	if err != nil || strings.Join(got, ",") != "mc.example.com,www.example.com" {
		t.Fatalf("normalizeNames = %v, %v", got, err)
	}
	if _, err := normalizeNames(nil); err == nil {
		t.Error("no names accepted")
	}
	many := make([]string, 11)
	for i := range many {
		many[i] = strings.Repeat("a", i+1) + ".example.com"
	}
	_, err = normalizeNames(many)
	wantProblem(t, err, CodeInvalidName, "too_many")
	_, err = normalizeNames([]string{"mc.example.com", "*.example.com"})
	wantProblem(t, err, CodeInvalidName, "wildcard")
}

func TestFingerprint(t *testing.T) {
	der := []byte("certificate")
	fp := fingerprint(der)
	if !regexp.MustCompile(`^([0-9A-F]{2}:){31}[0-9A-F]{2}$`).MatchString(fp) {
		t.Fatalf("fingerprint %q is not in the panel's format", fp)
	}
	sum := sha256.Sum256(der)
	if !strings.HasPrefix(fp, strings.ToUpper(strings.Join([]string{hex2(sum[0]), hex2(sum[1])}, ":"))) {
		t.Errorf("fingerprint %q does not start with the digest's bytes", fp)
	}
}

func hex2(b byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[b>>4], digits[b&15]})
}

// allCodes lists every code constant; the catalog must explain each.
var allCodes = []string{
	CodeNameOK, CodeNameMissing, CodeNameElsewhere, CodeNameMixed, CodeNameProxied, CodeNamePrivate,
	CodeNameUnverified, CodeNameLookupFailed,
	CodeSRVOK, CodeSRVMissing, CodeSRVWrong, CodeSRVConflict, CodeSRVLookupFailed,
	CodeInvalidName, CodeInvalidEmail, CodeInvalidPlan, CodeConfig,
	CodeRateLimited, CodePaused, CodeTermsNotAccepted, CodeCAActionRequired, CodeCANeedsAccount,
	CodeAccountDisabled, CodeAccountKeyDamaged, CodeNameRefused, CodeEmailRefused,
	CodePort80Unreachable, CodeWrongAnswer, CodeDNSNoRecord, CodeDNSServersFailing, CodeCAAForbids,
	CodeValidationTimeout, CodeIssuanceTimeout, CodeChallengeNotOffered, CodeDNS01PublishFailed, CodeDNS01NotVisible,
	CodeDNS01RecordWrong, CodeCertificateLimit,
	CodePort80Busy, CodePort80Denied, CodePort80Failed, CodeSaveFailed, CodeBadCertificate, CodeCanceled,
	CodeCAUnreachable, CodeClockWrong, CodeCAUntrusted, CodeCAUnavailable, CodeCAError, CodeFailed,
}

func TestCatalog(t *testing.T) {
	codes := map[string]bool{}
	for _, c := range allCodes {
		codes[c] = true
		if _, ok := catalog[c]; !ok {
			t.Errorf("code %s has no text", c)
		}
	}
	for key, txt := range catalog {
		base, kind, _ := strings.Cut(key, ".")
		if !codes[base] {
			t.Errorf("catalog key %s is not a known code", key)
		}
		if txt.msg == "" {
			t.Errorf("%s has no message", key)
		}
		for _, s := range []string{txt.msg, txt.hint} {
			for _, m := range rePlaceholder.FindAllStringSubmatch(s, -1) {
				if _, ok := fallbackText[m[1]]; !ok {
					t.Errorf("%s uses {%s}, which has no fallback text", key, m[1])
				}
			}
			if strings.Count(s, "{") != len(rePlaceholder.FindAllString(s, -1)) {
				t.Errorf("%s has a malformed placeholder: %q", key, s)
			}
		}
		params := map[string]string{}
		if kind != "" {
			params["kind"] = kind
		}
		n, _ := note(base, params)
		for _, s := range []string{n.Message, n.Hint} {
			if strings.ContainsAny(s, "{}") || strings.Contains(s, "  ") || strings.Contains(s, " .") || strings.Contains(s, " ,") {
				t.Errorf("%s without params reads badly: %q", key, s)
			}
		}
		if n.Message != fill(txt.msg, nil) {
			t.Errorf("%s: note picked %q, want the variant's text", key, n.Message)
		}
	}
}

func TestNoteParams(t *testing.T) {
	n, action := note(CodeNameElsewhere, map[string]string{"name": "mc.example.com", "found": "198.51.100.7", "ipv4": "203.0.113.10", "empty": ""})
	if !action || n.Message != "mc.example.com points at 198.51.100.7, which is not this server." || !strings.Contains(n.Hint, "to 203.0.113.10") {
		t.Errorf("note = %+v", n)
	}
	if _, ok := n.Params["empty"]; ok {
		t.Error("empty params are kept")
	}
	n, _ = note(CodeInvalidName, map[string]string{"kind": "no_such_variant", "name": "x"})
	if n.Message != fill(catalog[CodeInvalidName].msg, map[string]string{"name": "x"}) {
		t.Errorf("unknown kind: %q", n.Message)
	}
	n, _ = note(CodeRateLimited, map[string]string{"until": "2025-01-18T21:41:42Z"})
	if !strings.Contains(n.Hint, "after 2025-01-18 21:41 UTC") {
		t.Errorf("until is not formatted: %q", n.Hint)
	}
	if n, _ := note(CodeNameOK, nil); n.Params != nil || n.Message != "this name points at this server." {
		t.Errorf("note without params = %+v", n)
	}
}
