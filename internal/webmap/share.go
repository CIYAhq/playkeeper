package webmap

import (
	"crypto/rand"
	"crypto/subtle"
	"strings"
)

// ShareTokenLen is the length of a shared map's link token. Each character
// is one of 62 letters and digits, so a token carries about 131 random
// bits. Invite codes have the same shape, so the two kinds of link look
// alike.
const ShareTokenLen = 22

const shareAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// NewShareToken returns a fresh link token for a shared map.
func NewShareToken() string {
	token := make([]byte, 0, ShareTokenLen)
	var buf [32]byte
	for len(token) < ShareTokenLen {
		_, _ = rand.Read(buf[:])
		for _, b := range buf {
			// 248 is the largest multiple of 62 below 256: taking larger
			// bytes modulo 62 would favour the first eight characters.
			if b < 248 && len(token) < ShareTokenLen {
				token = append(token, shareAlphabet[b%62])
			}
		}
	}
	return string(token)
}

// ValidShareToken reports whether s has the shape of a link token, so
// anything else can be refused without a lookup.
func ValidShareToken(s string) bool {
	if len(s) != ShareTokenLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(shareAlphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

// ShareTokenMatches compares a stored token with one from a link in
// constant time, so how long a lookup takes says nothing about how much of
// a guess was right.
func ShareTokenMatches(stored, token string) bool {
	return len(stored) == ShareTokenLen && subtle.ConstantTimeCompare([]byte(stored), []byte(token)) == 1
}
