package names

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Headers of a signed request.
const (
	HeaderKey       = "Playkeeper-Key"
	HeaderTimestamp = "Playkeeper-Timestamp"
	HeaderNonce     = "Playkeeper-Nonce"
	HeaderSignature = "Playkeeper-Signature"
)

// MaxSkew is how far a signed request's timestamp may be from the service's
// clock. The service remembers nonces for this long, so a request cannot be
// replayed within the window either.
const MaxSkew = 5 * time.Minute

const signingContext = "playkeeper-names-v1"

var reNonce = regexp.MustCompile(`^[A-Za-z0-9_-]{22,64}$`)

// signingMessage binds a signature to the service's base domain, the
// method, the path and query, the time, a one-time nonce and the body.
func signingMessage(base, method, requestURI string, ts int64, nonce string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(strings.Join([]string{
		signingContext, base, method, requestURI, strconv.FormatInt(ts, 10), nonce, hex.EncodeToString(sum[:]),
	}, "\n"))
}

// SignRequest adds the signature headers to req. body must be exactly what
// req sends.
func SignRequest(req *http.Request, key ed25519.PrivateKey, base string, body []byte, now time.Time) {
	var n [16]byte
	_, _ = rand.Read(n[:])
	nonce := base64.RawURLEncoding.EncodeToString(n[:])
	ts := now.Unix()
	sig := ed25519.Sign(key, signingMessage(base, req.Method, req.URL.RequestURI(), ts, nonce, body))
	req.Header.Set(HeaderKey, base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)))
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderSignature, base64.StdEncoding.EncodeToString(sig))
}

// Signer is who signed a verified request, and when.
type Signer struct {
	Key   ed25519.PublicKey
	Nonce string
	Time  time.Time
}

// KeyID is the signer's key id, for logs.
func (s Signer) KeyID() string { return KeyID(s.Key) }

// EncodedKey is the signer's public key in base64, as it is stored.
func (s Signer) EncodedKey() string { return base64.StdEncoding.EncodeToString(s.Key) }

// VerifyRequest checks the signature headers of a request whose body was
// already read, for a service under base whose clock says now. It does not
// know which nonces were used before: the caller must refuse a nonce it saw
// from the same key within MaxSkew.
func VerifyRequest(h http.Header, method, requestURI string, body []byte, base string, now time.Time) (Signer, error) {
	keyB64, tsText, nonce, sigB64 := h.Get(HeaderKey), h.Get(HeaderTimestamp), h.Get(HeaderNonce), h.Get(HeaderSignature)
	if keyB64 == "" || tsText == "" || nonce == "" || sigB64 == "" {
		return Signer{}, errorf(401, CodeUnsigned, "", "This request is not signed with a Playkeeper key.")
	}
	bad := func(what string) error {
		return errorf(401, CodeBadSignature, "", "The request's %s is not valid.", what)
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return Signer{}, bad("key")
	}
	ts, err := strconv.ParseInt(tsText, 10, 64)
	if err != nil || len(tsText) > 12 {
		return Signer{}, bad("timestamp")
	}
	if !reNonce.MatchString(nonce) {
		return Signer{}, bad("nonce")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return Signer{}, bad("signature")
	}
	at := time.Unix(ts, 0)
	if skew := at.Sub(now); skew > MaxSkew || skew < -MaxSkew {
		return Signer{}, &Error{
			Status: 401, Code: CodeClockSkew,
			Message: "This machine's clock is off by more than 5 minutes, so the names service refused the request.",
			Hint:    "Turn on automatic time (on Ubuntu: sudo timedatectl set-ntp true) and try again.",
			Params:  map[string]any{"serverTime": now.Unix(), "skewSeconds": int64(skew / time.Second)},
		}
	}
	if !ed25519.Verify(ed25519.PublicKey(key), signingMessage(base, method, requestURI, ts, nonce, body), sig) {
		return Signer{}, bad("signature")
	}
	return Signer{Key: ed25519.PublicKey(key), Nonce: nonce, Time: at}, nil
}
