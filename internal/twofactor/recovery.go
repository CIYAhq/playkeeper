package twofactor

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// RecoveryCodeCount is how many recovery codes a new set has.
	RecoveryCodeCount = 10
	// LowRecoveryCodes is how few codes left make sign-in suggest new ones.
	LowRecoveryCodes = 3
)

// recoveryAlphabet has no 0, 1, l or o, which look alike on paper.
const recoveryAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

// recoveryLen characters of 5 bits each make 80 random bits per code.
const recoveryLen = 16

var recoveryEncoding = base32.NewEncoding(recoveryAlphabet).WithPadding(base32.NoPadding)

// RecoverySet is how a user's recovery codes are stored: the hash of each
// code, in the order they were shown, with "" for the ones used.
type RecoverySet struct {
	Hashes    []string
	CreatedAt time.Time
}

// NewRecoveryCodes makes RecoveryCodeCount codes from rand
// (crypto/rand.Reader in production). It returns the set to store and the
// codes to show the user once, as "abcd-efgh-jkmn-pqrs".
func NewRecoveryCodes(rand io.Reader, now time.Time) (RecoverySet, []string, error) {
	raw := make([]byte, RecoveryCodeCount*recoveryLen*5/8)
	if _, err := io.ReadFull(rand, raw); err != nil {
		return RecoverySet{}, nil, fmt.Errorf("could not make recovery codes: %w", err)
	}
	set := RecoverySet{Hashes: make([]string, RecoveryCodeCount), CreatedAt: now}
	codes := make([]string, RecoveryCodeCount)
	size := len(raw) / RecoveryCodeCount
	for i := range codes {
		code := recoveryEncoding.EncodeToString(raw[i*size : (i+1)*size])
		set.Hashes[i] = hashRecovery(code)
		if slices.Contains(set.Hashes[:i], set.Hashes[i]) {
			return RecoverySet{}, nil, errors.New("could not make recovery codes: the random source repeated itself")
		}
		codes[i] = code[0:4] + "-" + code[4:8] + "-" + code[8:12] + "-" + code[12:16]
	}
	return set, codes, nil
}

// Remaining is the number of unused codes.
func (s RecoverySet) Remaining() int {
	n := 0
	for _, h := range s.Hashes {
		if h != "" {
			n++
		}
	}
	return n
}

// Use checks code against the unused codes. If one matches, it returns a
// copy of the set with that code used up, and true. Case, spaces and dashes
// in code are ignored.
func (s RecoverySet) Use(code string) (RecoverySet, bool) {
	norm, ok := normalizeRecovery(code)
	if !ok {
		return s, false
	}
	return s.use(norm)
}

func (s RecoverySet) use(norm string) (RecoverySet, bool) {
	want := []byte(hashRecovery(norm))
	match := -1
	for i, h := range s.Hashes {
		if h != "" && subtle.ConstantTimeCompare([]byte(h), want) == 1 {
			match = i
		}
	}
	if match < 0 {
		return s, false
	}
	used := RecoverySet{Hashes: slices.Clone(s.Hashes), CreatedAt: s.CreatedAt}
	used.Hashes[match] = ""
	return used, true
}

// normalizeRecovery returns code in lower case without spaces and dashes,
// and whether that leaves the 16 characters of a recovery code.
func normalizeRecovery(code string) (string, bool) {
	if len(code) > 64 {
		return "", false
	}
	var b strings.Builder
	for _, r := range code {
		if 'A' <= r && r <= 'Z' {
			r += 'a' - 'A'
		}
		switch {
		case unicode.IsSpace(r) || unicode.Is(unicode.Pd, r):
		case r < utf8.RuneSelf && strings.IndexByte(recoveryAlphabet, byte(r)) >= 0:
			b.WriteRune(r)
		default:
			return "", false
		}
	}
	return b.String(), b.Len() == recoveryLen
}

// hashRecovery is the stored form of a normalised code. A fast hash is
// enough for 80 random bits, and the prefix keeps it apart from other
// SHA-256 hashes in the database. Changing it invalidates stored codes.
func hashRecovery(norm string) string {
	sum := sha256.Sum256([]byte("playkeeper recovery code v1\x00" + norm))
	return hex.EncodeToString(sum[:])
}
