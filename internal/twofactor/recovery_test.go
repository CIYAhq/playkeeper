package twofactor

import (
	"bytes"
	"encoding/base32"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 25, 12, 0, 7, 0, time.UTC)

// newRand is a repeatable random source for tests.
func newRand(seed byte) *rand.ChaCha8 { return rand.NewChaCha8([32]byte{seed}) }

func newSet(t *testing.T) (RecoverySet, []string) {
	t.Helper()
	set, codes, err := NewRecoveryCodes(newRand(1), t0)
	if err != nil {
		t.Fatal(err)
	}
	return set, codes
}

func TestRecoveryCodesAreEightyRandomBitsEach(t *testing.T) {
	raw := make([]byte, 100)
	newRand(1).Read(raw)
	set, codes, err := NewRecoveryCodes(bytes.NewReader(raw), t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != RecoveryCodeCount || len(set.Hashes) != RecoveryCodeCount || !set.CreatedAt.Equal(t0) {
		t.Fatalf("got %d codes, %d hashes, made at %v", len(codes), len(set.Hashes), set.CreatedAt)
	}
	shape := regexp.MustCompile(`^[a-km-np-z2-9]{4}(-[a-km-np-z2-9]{4}){3}$`)
	decode := base32.NewEncoding("abcdefghijkmnpqrstuvwxyz23456789").WithPadding(base32.NoPadding)
	for i, code := range codes {
		plain := strings.ReplaceAll(code, "-", "")
		if !shape.MatchString(code) {
			t.Errorf("code %q is not four groups of four unambiguous characters", code)
		}
		if b, err := decode.DecodeString(plain); err != nil || !bytes.Equal(b, raw[i*10:(i+1)*10]) {
			t.Errorf("code %d does not carry its own 10 random bytes: %x %v", i, b, err)
		}
		if set.Hashes[i] != hashRecovery(plain) || strings.Contains(set.Hashes[i], plain) {
			t.Errorf("hash %d = %s", i, set.Hashes[i])
		}
	}
	if _, _, err := NewRecoveryCodes(bytes.NewReader(raw[:99]), t0); err == nil {
		t.Error("a short read from the random source must fail")
	}
	if _, _, err := NewRecoveryCodes(bytes.NewReader(make([]byte, 100)), t0); err == nil {
		t.Error("a random source that repeats itself must not make two equal codes")
	}
}

// Stored hashes have to keep matching their codes after an upgrade.
func TestRecoveryHashFormatIsStable(t *testing.T) {
	const want = "39582837cf528c8a04558fa45e6744957d59d609323f7fae7a3d8f7fe7f81aec"
	if got := hashRecovery("abcdefghijkmnpqr"); got != want {
		t.Fatalf("hashRecovery = %s, want %s", got, want)
	}
}

func TestRecoveryCodesWorkOnce(t *testing.T) {
	set, codes := newSet(t)
	for i, code := range codes {
		next, ok := set.Use(code)
		if !ok {
			t.Fatalf("code %d refused", i)
		}
		if set.Remaining() != RecoveryCodeCount-i {
			t.Fatalf("Use changed the set it was called on")
		}
		if next.Remaining() != RecoveryCodeCount-i-1 || next.Hashes[i] != "" || !next.CreatedAt.Equal(t0) {
			t.Fatalf("after code %d: %d left, hash %q", i, next.Remaining(), next.Hashes[i])
		}
		if _, again := next.Use(code); again {
			t.Fatalf("code %d worked twice", i)
		}
		set = next
	}
	if _, ok := (RecoverySet{}).Use(codes[0]); ok {
		t.Fatal("an empty set accepted a code")
	}
}

func TestRecoveryCodeInputIsNormalised(t *testing.T) {
	set, codes := newSet(t)
	code := codes[0]
	plain := strings.ReplaceAll(code, "-", "")
	for _, in := range []string{
		code, plain, strings.ToUpper(code),
		" " + strings.ReplaceAll(code, "-", " ") + "\n",
		plain[:8] + " - " + plain[8:],
		strings.ReplaceAll(code, "-", "\u2013"),
		strings.ReplaceAll(code, "-", "\u00a0"),
	} {
		if _, ok := set.Use(in); !ok {
			t.Errorf("Use(%q) refused the code", in)
		}
	}
	last := plain[len(plain)-1]
	other := byte('a')
	if last == 'a' {
		other = 'b'
	}
	if _, ok := set.Use(plain[:len(plain)-1] + string(other)); ok {
		t.Error("a code one character off must be refused")
	}
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"abcd-efgh-ijkm-npqr", true},
		{"ABCD EFGH IJKM NPQR", true},
		{"abcd\u2014efgh\u2010ijkm\tnpqr", true},
		{"abcd-efgh-ijkm-npq", false},
		{"abcd-efgh-ijkm-npqrs", false},
		{"abcd-efgh-ijkm-npq0", false},
		{"abcd-efgh-ijkm-npq1", false},
		{"abcd-efgh-ijkm-npql", false},
		{"abcd-efgh-ijkm-npqo", false},
		{"\u212abcd-efgh-ijkm-npqr", false}, // Kelvin sign, which lower-cases to k
		{"\uff41bcd-efgh-ijkm-npqr", false}, // full-width a
		{strings.Repeat(" ", 60) + "abcdefghijkmnpqr", false},
		{"123456", false},
		{"", false},
	} {
		if got, ok := normalizeRecovery(tc.in); ok != tc.ok || ok && got != "abcdefghijkmnpqr" {
			t.Errorf("normalizeRecovery(%q) = %q, %v; want ok=%v", tc.in, got, ok, tc.ok)
		}
	}
}
