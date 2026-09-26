package share

import (
	"bytes"
	"crypto/rand"
	"errors"
	"math"
	"strings"
	"testing"
	"testing/iotest"
)

func TestNewTokenHasTheInviteCodesShape(t *testing.T) {
	seen := map[string]bool{}
	for range 2000 {
		tok, err := NewToken(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if len(tok) != TokenLen || !ValidToken(tok) {
			t.Fatalf("%q is not a token", tok)
		}
		if seen[tok] {
			t.Fatalf("%q came twice", tok)
		}
		seen[tok] = true
	}
	if bits := TokenLen * math.Log2(float64(len(tokenAlphabet))); bits < 128 {
		t.Errorf("a token holds %.1f random bits", bits)
	}
}

// Every letter and digit comes up about as often, in every position: a
// chi-squared test with 61 degrees of freedom, whose 99.99th percentile is
// about 110.
func TestNewTokenIsUniform(t *testing.T) {
	const n = 20000
	var counts [TokenLen][62]int
	for range n {
		tok, err := NewToken(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		for i := range TokenLen {
			counts[i][strings.IndexByte(tokenAlphabet, tok[i])]++
		}
	}
	want := float64(n) / 62
	for i := range TokenLen {
		chi := 0.0
		for _, c := range counts[i] {
			chi += (float64(c) - want) * (float64(c) - want) / want
		}
		if chi > 110 {
			t.Errorf("position %d: chi-squared %.1f", i, chi)
		}
	}
}

func TestNewTokenSkipsBiasedBytes(t *testing.T) {
	// 248 to 255 would favour the first eight letters, so they are skipped.
	src := append(bytes.Repeat([]byte{255, 248}, 16), make([]byte, 32)...)
	for i := range 32 {
		src[32+i] = []byte{1, 63, 125, 187}[i%4]
	}
	tok, err := NewToken(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if tok != strings.Repeat("B", TokenLen) {
		t.Errorf("got %q", tok)
	}
	if _, err := NewToken(iotest.ErrReader(errors.New("no randomness"))); err == nil {
		t.Error("a failing source made a token")
	}
}

func TestValidToken(t *testing.T) {
	for _, s := range []string{"", "short", strings.Repeat("a", TokenLen-1), strings.Repeat("a", TokenLen+1), strings.Repeat("a", TokenLen-1) + "-",
		strings.Repeat("a", TokenLen-1) + "/", "../" + strings.Repeat("a", TokenLen-3), strings.Repeat("a", TokenLen-1) + "é"[:1], "cobblemon"} {
		if ValidToken(s) {
			t.Errorf("%q passed", s)
		}
	}
	if !ValidToken(tok) || !ValidToken(strings.Repeat("Z9", TokenLen/2)) {
		t.Error("a token failed")
	}
}
