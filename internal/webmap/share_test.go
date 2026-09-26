package webmap

import (
	"math"
	"strings"
	"testing"
)

// A link token must be unguessable: at least 128 random bits, with every
// character equally likely.
func TestShareTokensAreUnguessable(t *testing.T) {
	if bits := float64(ShareTokenLen) * math.Log2(float64(len(shareAlphabet))); bits < 128 {
		t.Fatalf("a token carries %.1f random bits", bits)
	}
	const n = 20000
	seen := make(map[string]bool, n)
	counts := map[byte]int{}
	for i := 0; i < n; i++ {
		tok := NewShareToken()
		if !ValidShareToken(tok) || seen[tok] {
			t.Fatalf("token %d: %q (valid %v, repeated %v)", i, tok, ValidShareToken(tok), seen[tok])
		}
		seen[tok] = true
		for j := 0; j < len(tok); j++ {
			counts[tok[j]]++
		}
	}
	// About 7100 of each character, give or take 84: a tenth either way is
	// more than eight times that, while a byte taken modulo 62 without
	// rejection makes A to H about 20% more common.
	want := float64(n*ShareTokenLen) / float64(len(shareAlphabet))
	for i := 0; i < len(shareAlphabet); i++ {
		c := shareAlphabet[i]
		if got := float64(counts[c]); math.Abs(got-want) > want/10 {
			t.Errorf("%q appears %.0f times in %d tokens, want about %.0f", c, got, n, want)
		}
	}
	if len(counts) != len(shareAlphabet) {
		t.Errorf("tokens use %d different characters", len(counts))
	}
}

func TestShareTokenShape(t *testing.T) {
	good := "Ab3dEf6hIj9lMn2pQr5tUv"
	if !ValidShareToken(good) {
		t.Fatalf("%q refused", good)
	}
	for _, s := range []string{
		"", "survival", "my-server", "survival-k3v9x2q7",
		good[:21], good + "w", strings.Repeat("a", 64),
		"Ab3dEf6hIj9lMn2pQr5tU-", "Ab3dEf6hIj9lMn2pQr5tU_", "Ab3dEf6hIj9lMn2pQr5tU/", "Ab3dEf6hIj9lMn2pQr5tU%",
		"Ab3dEf6hIj9lMn2pQr5tU ", "Ab3dEf6hIj9lMn2pQr5t\u00e9",
	} {
		if ValidShareToken(s) {
			t.Errorf("%q accepted", s)
		}
	}
	if !ShareTokenMatches(good, good) || ShareTokenMatches(good, good[:21]+"w") || ShareTokenMatches("", "") || ShareTokenMatches("short", "short") {
		t.Fatal("ShareTokenMatches")
	}
}
