package invites

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"path"
	"strings"
)

// CodeLen is the length of a link code. Each character is one of 62
// letters and digits, so a code carries about 131 random bits.
const CodeLen = 22

const codeAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// JoinPath is where invite links point: JoinPath + "/" + code on the
// panel's address, for both kinds of invite. The page asks the panel which
// kind it is.
const JoinPath = "/join"

// NewCode returns a fresh link code.
func NewCode() string {
	code := make([]byte, 0, CodeLen)
	var buf [32]byte
	for len(code) < CodeLen {
		_, _ = rand.Read(buf[:])
		for _, b := range buf {
			// 248 is the largest multiple of 62 below 256: taking larger
			// bytes modulo 62 would favour the first eight characters.
			if b < 248 && len(code) < CodeLen {
				code = append(code, codeAlphabet[b%62])
			}
		}
	}
	return string(code)
}

// HashCode is what is stored for a code, and what invites are looked up
// by: its SHA-256 in lower-case hex.
func HashCode(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

// WellFormed reports whether s has the shape of a code, so the panel can
// refuse anything else without a database lookup.
func WellFormed(s string) bool {
	if len(s) != CodeLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(codeAlphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

// codeMatches compares the code's digest with the stored one in constant
// time. Both are 32 bytes whatever the code, so timing says nothing about
// how much of a code was right.
func codeMatches(storedHash, code string) bool {
	want, err := hex.DecodeString(storedHash)
	if err != nil || len(want) != sha256.Size {
		return false
	}
	got := sha256.Sum256([]byte(code))
	return subtle.ConstantTimeCompare(got[:], want) == 1
}

// LinkPath is the path of an invite's link; the UI puts the panel's
// address in front.
func LinkPath(code string) string { return JoinPath + "/" + code }

// CodeFromPath returns the code in a link path, "/join/<code>".
func CodeFromPath(p string) (string, bool) {
	code, ok := strings.CutPrefix(p, JoinPath+"/")
	if !ok || !WellFormed(code) {
		return "", false
	}
	return code, true
}

// RedactPath returns p with everything after "/join/" hidden, for request
// logs. It hides mistyped and malformed codes too, since they may hold most
// of a real one.
func RedactPath(p string) string {
	prefix := JoinPath + "/"
	if c := path.Clean("/" + p); len(c) > len(prefix) && strings.EqualFold(c[:len(prefix)], prefix) {
		return prefix + "[code]"
	}
	return p
}
