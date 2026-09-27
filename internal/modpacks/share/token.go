package share

import "io"

// TokenLen is the length of a public page's link token: 22 letters and
// digits, the invite codes' alphabet, which is about 131 random bits.
const TokenLen = 22

const tokenAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// NewToken makes the link token of a server's public page from rand, a
// source of random bytes such as crypto/rand.Reader. A byte of 248 or more
// is skipped, as 248 is the largest multiple of 62 below 256, so each letter
// and digit is equally likely.
func NewToken(rand io.Reader) (string, error) {
	out := make([]byte, 0, TokenLen)
	buf := make([]byte, 32)
	for len(out) < TokenLen {
		if _, err := io.ReadFull(rand, buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b < 248 && len(out) < TokenLen {
				out = append(out, tokenAlphabet[b%62])
			}
		}
	}
	return string(out), nil
}

// ValidToken reports whether s has the shape of a link token. It says
// nothing about whether any server has it.
func ValidToken(s string) bool {
	if len(s) != TokenLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
