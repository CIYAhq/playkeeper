package invites

import (
	"math"
	"strings"
	"testing"
)

func TestNewCode(t *testing.T) {
	if bits := CodeLen * math.Log2(float64(len(codeAlphabet))); bits < 128 {
		t.Fatalf("a code carries %.1f bits, want at least 128", bits)
	}
	const n = 2000
	seen := map[string]bool{}
	counts := map[byte]int{}
	var positions [CodeLen]map[byte]bool
	for i := range positions {
		positions[i] = map[byte]bool{}
	}
	for range n {
		code := NewCode()
		if len(code) != CodeLen || !WellFormed(code) {
			t.Fatalf("code %q is not %d letters and digits", code, CodeLen)
		}
		if seen[code] {
			t.Fatal("a code repeated")
		}
		seen[code] = true
		for i := 0; i < len(code); i++ {
			counts[code[i]]++
			positions[i][code[i]] = true
		}
	}
	// Each position draws 2000 times from 62 characters; one is missed with
	// probability about 1e-14.
	for i, chars := range positions {
		if len(chars) != len(codeAlphabet) {
			t.Errorf("position %d used %d of the %d characters", i, len(chars), len(codeAlphabet))
		}
	}
	// Taking every byte modulo 62 would make the first eight characters a
	// quarter more likely than the others.
	first, rest := 0, 0
	for i := 0; i < len(codeAlphabet); i++ {
		if i < 8 {
			first += counts[codeAlphabet[i]]
		} else {
			rest += counts[codeAlphabet[i]]
		}
	}
	if ratio := (float64(first) / 8) / (float64(rest) / 54); ratio < 0.9 || ratio > 1.1 {
		t.Errorf("the first eight characters come up %.2f times as often as the others", ratio)
	}
}

func TestHashCode(t *testing.T) {
	if got := HashCode("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("HashCode(abc) = %s, want the SHA-256 in hex", got)
	}
	code := NewCode()
	if h := HashCode(code); len(h) != 64 || h != strings.ToLower(h) || strings.Contains(h, code) {
		t.Errorf("HashCode = %q", h)
	}
}

func TestWellFormed(t *testing.T) {
	code := NewCode()
	if !WellFormed(code) || !WellFormed(strings.Repeat("Z", CodeLen)) || !WellFormed(strings.Repeat("0", CodeLen)) {
		t.Error("a well-formed code was refused")
	}
	for _, s := range []string{
		"", code[:CodeLen-1], code + "a",
		code[:CodeLen-1] + "-", code[:CodeLen-1] + "_", code[:CodeLen-1] + "+", code[:CodeLen-1] + "/",
		code[:CodeLen-1] + " ", code[:CodeLen-1] + "%", code[:CodeLen-2] + "é",
	} {
		if WellFormed(s) {
			t.Errorf("WellFormed(%q) = true", s)
		}
	}
}

func TestLinkPath(t *testing.T) {
	code := NewCode()
	if got := LinkPath(code); got != "/join/"+code {
		t.Errorf("LinkPath = %q", got)
	}
	if got, ok := CodeFromPath(LinkPath(code)); !ok || got != code {
		t.Errorf("CodeFromPath(LinkPath(code)) = %q, %v", got, ok)
	}
	for _, p := range []string{
		"", "/join", "/join/", "/join/" + code[:CodeLen-1], "/join/" + code + "/", "/join/" + code + "x",
		"/join//" + code, "join/" + code, "/JOIN/" + code, "/accept/" + code, "/join/" + strings.Repeat("-", CodeLen),
	} {
		if got, ok := CodeFromPath(p); ok || got != "" {
			t.Errorf("CodeFromPath(%q) = %q, %v", p, got, ok)
		}
	}
}

func TestRedactPath(t *testing.T) {
	code := NewCode()
	for p, want := range map[string]string{
		"/join/" + code:             "/join/[code]",
		"/join/" + code + "/more":   "/join/[code]",
		"/join/" + code[:CodeLen-3]: "/join/[code]",
		"/JOIN/" + code:             "/join/[code]",
		"//join/" + code:            "/join/[code]",
		"/assets/../join/" + code:   "/join/[code]",
		"/join/../join/" + code:     "/join/[code]",
		"/join":                     "/join",
		"/join/":                    "/join/",
		"/api/public/join/preview":  "/api/public/join/preview",
		"/api/servers/k3q9zt7mwa":   "/api/servers/k3q9zt7mwa",
		"/players":                  "/players",
		"/":                         "/",
		"":                          "",
	} {
		if got := RedactPath(p); got != want {
			t.Errorf("RedactPath(%q) = %q, want %q", p, got, want)
		}
	}
}
