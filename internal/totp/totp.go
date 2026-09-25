// Package totp implements time-based one-time passwords (RFC 6238) with the
// parameters every authenticator app supports: HMAC-SHA1, six digits and a
// 30-second time step. It makes secrets, formats them for a QR code and for
// typing, and checks codes with replay protection. It keeps no state: the
// caller stores the secret and the time step of the last accepted code.
package totp

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// Digits is the length of a code.
	Digits = 6
	// Period is how long one code is valid.
	Period = 30 * time.Second
	// Window is how many steps before and after the current one are
	// accepted, for phones whose clock is a little off.
	Window = 1
	// SecretSize is the length of new secrets in bytes: 160 bits, as RFC 4226
	// recommends.
	SecretSize = 20
)

var (
	ErrMalformed = errors.New("A code from an authenticator app is 6 digits.")
	ErrWrong     = errors.New("The code is not correct.")
	ErrReused    = errors.New("The code has already been used.")
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// Secret is a shared TOTP secret. It prints as "[secret]" with every fmt verb
// and as {} in JSON, so it cannot end up in a log or a response by accident;
// Base32 and Grouped show it on purpose.
type Secret struct{ key []byte }

// NewSecret reads a new secret from rand (crypto/rand.Reader in production).
func NewSecret(rand io.Reader) (Secret, error) {
	key := make([]byte, SecretSize)
	if _, err := io.ReadFull(rand, key); err != nil {
		return Secret{}, fmt.Errorf("could not make a two-factor secret: %w", err)
	}
	return Secret{key}, nil
}

// ParseSecret reads a secret written by Base32 or Grouped. Case, spaces and
// padding are ignored.
func ParseSecret(s string) (Secret, error) {
	clean := strings.Map(func(r rune) rune {
		if r == ' ' || r == '=' {
			return -1
		}
		return unicode.ToUpper(r)
	}, s)
	key, err := b32.DecodeString(clean)
	// RFC 4226 requires at least 128 bits.
	if err != nil || len(key) < 16 || len(key) > 64 {
		return Secret{}, errors.New("The stored two-factor secret is damaged.")
	}
	return Secret{key}, nil
}

// Base32 is the secret as unpadded upper-case base32 (RFC 4648), the form
// otpauth:// URIs use and the one to store.
func (s Secret) Base32() string { return b32.EncodeToString(s.key) }

// Grouped is Base32 in groups of four, for typing into an app that cannot
// scan the QR code. Authenticator apps ignore the spaces.
func (s Secret) Grouped() string {
	b := s.Base32()
	var out strings.Builder
	for i := 0; i < len(b); i += 4 {
		if i > 0 {
			out.WriteByte(' ')
		}
		out.WriteString(b[i:min(i+4, len(b))])
	}
	return out.String()
}

// IsZero reports whether s holds no secret.
func (s Secret) IsZero() bool { return len(s.key) == 0 }

func (Secret) String() string   { return "[secret]" }
func (Secret) GoString() string { return "totp.Secret{[secret]}" }

// Format stops verbs such as %d, which bypass String, from printing the key.
func (s Secret) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		io.WriteString(f, s.GoString())
		return
	}
	io.WriteString(f, s.String())
}

// Step is the RFC 6238 time step t falls in: whole periods since 1970.
func Step(t time.Time) int64 {
	u, p := t.Unix(), int64(Period/time.Second)
	if u < 0 {
		return (u - p + 1) / p
	}
	return u / p
}

// Code is the code for time t.
func Code(s Secret, t time.Time) string { return hotp(s.key, uint64(Step(t)), Digits) }

// Verify checks code against the steps from Window before to Window after
// t's and returns the step it matched. Steps at or before lastStep are
// refused with ErrReused, so a code works once and never after a newer one:
// store the returned step and pass it as lastStep next time (0 for a new
// secret). Spaces and dashes in code are ignored.
func Verify(s Secret, code string, t time.Time, lastStep int64) (int64, error) {
	digits, ok := Normalize(code)
	if !ok {
		return 0, ErrMalformed
	}
	if s.IsZero() {
		return 0, ErrWrong
	}
	now := Step(t)
	matched, reused := int64(-1), false
	for st := now - Window; st <= now+Window; st++ {
		if st < 0 || subtle.ConstantTimeCompare([]byte(hotp(s.key, uint64(st), Digits)), []byte(digits)) != 1 {
			continue
		}
		if st > lastStep {
			matched = st
		} else {
			reused = true
		}
	}
	switch {
	case matched >= 0:
		return matched, nil
	case reused:
		return 0, ErrReused
	}
	return 0, ErrWrong
}

// Normalize returns code without spaces and dashes, and whether that leaves
// the six digits of a code.
func Normalize(code string) (string, bool) {
	if len(code) > 32 {
		return "", false
	}
	var b strings.Builder
	for _, r := range code {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case unicode.IsSpace(r) || r == '-':
		default:
			return "", false
		}
	}
	return b.String(), b.Len() == Digits
}

// hotp is RFC 4226's HOTP. HMAC-SHA1 is what RFC 6238 and every
// authenticator app use; HMAC does not rely on SHA-1's collision resistance.
func hotp(key []byte, counter uint64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[off:]) & 0x7fffffff
	mod := uint32(1)
	for range digits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, n%mod)
}

// KeyURI is the otpauth:// URI an authenticator app reads from a QR code, in
// Google Authenticator's Key URI format, which the other apps follow: the
// label "issuer:account", the secret, and the issuer again for apps that only
// read the parameter. The algorithm, digits and period are the format's
// defaults, so they are left out, which keeps the QR code small. The format
// allows no colon in the issuer or the account name.
func KeyURI(s Secret, issuer, account string) (string, error) {
	if s.IsZero() {
		return "", errors.New("There is no two-factor secret to put in the link.")
	}
	for _, f := range []struct{ what, v string }{{"issuer", issuer}, {"account name", account}} {
		if err := checkLabel(f.what, f.v); err != nil {
			return "", err
		}
	}
	return "otpauth://totp/" + escape(issuer) + ":" + escape(account) + "?secret=" + s.Base32() + "&issuer=" + escape(issuer), nil
}

func checkLabel(what, v string) error {
	switch {
	case v == "":
		return fmt.Errorf("The %s for the authenticator app is empty.", what)
	case len(v) > 128:
		return fmt.Errorf("The %s for the authenticator app is longer than 128 bytes.", what)
	case !utf8.ValidString(v):
		return fmt.Errorf("The %s for the authenticator app is not valid UTF-8.", what)
	case strings.ContainsRune(v, ':'):
		return fmt.Errorf("The %s for the authenticator app cannot contain a colon.", what)
	case strings.ContainsFunc(v, unicode.IsControl):
		return fmt.Errorf("The %s for the authenticator app cannot contain control characters.", what)
	}
	return nil
}

// escape percent-encodes everything but RFC 3986's unreserved characters, so
// no app has to guess whether "+" means a space.
func escape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}
