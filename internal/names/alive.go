package names

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"strings"
)

// Liveness checks: every few hours the names service asks each name's
// address to prove that a Playkeeper dashboard holding the name's key runs
// there, and a name whose address does not answer for a week lapses. The
// service sends
//
//	GET https://<address>:8443/.well-known/playkeeper-names/<nonce>
//	Host: <name>.<base>:8443
//
// to the name's IPv4 address, then its IPv6 address, and the dashboard
// answers 200 with the JSON of Alive. The service does not check the
// certificate: the signature over its fresh nonce is the proof.
const (
	AlivePort = 8443
	AlivePath = "/.well-known/playkeeper-names/"
	// AlivePattern is AliveHandler's route for http.ServeMux.
	AlivePattern = "GET " + AlivePath + "{nonce}"
)

const aliveContext = "playkeeper-names-alive-v1"

// Alive answers a liveness check: the name, and the Ed25519 signature by
// the name's key of "playkeeper-names-alive-v1\n<base>\n<name>\n<nonce>" in
// standard base64 (see SignAlive).
type Alive struct {
	Name      string `json:"name"`
	Signature string `json:"signature"`
}

// aliveMessage's first line differs from signed requests' (see
// signingMessage), so an answer can never pass as a request.
func aliveMessage(base, name, nonce string) []byte {
	return []byte(aliveContext + "\n" + base + "\n" + name + "\n" + nonce)
}

// SignAlive is the signature of an answer to the liveness check with nonce
// for name under base.
func SignAlive(key ed25519.PrivateKey, base, name, nonce string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, aliveMessage(base, name, nonce)))
}

// VerifyAlive checks the signature of an answer to the liveness check with
// nonce for name against the name's key.
func VerifyAlive(key ed25519.PublicKey, base, name, nonce, sig string) bool {
	b, err := base64.StdEncoding.DecodeString(sig)
	return err == nil && len(key) == ed25519.PublicKeySize && len(b) == ed25519.SignatureSize &&
		ed25519.Verify(key, aliveMessage(base, name, nonce), b)
}

// AliveHandler answers the names service's liveness checks. Mount it at
// AlivePattern on the dashboard's HTTPS listener on AlivePort, reachable
// from the internet without signing in. The name comes from the Host
// header; keyFor returns the install's names key if the install holds that
// name, or nil.
func AliveHandler(base string, keyFor func(name string) ed25519.PrivateKey) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := r.PathValue("nonce")
		if nonce == "" {
			nonce = strings.TrimPrefix(r.URL.Path, AlivePath)
		}
		if !reNonce.MatchString(nonce) {
			writeAlive(w, http.StatusBadRequest, ErrorBody{Code: CodeInvalidRequest, Error: "The liveness check's nonce is not valid."})
			return
		}
		name := hostName(r.Host, base)
		var key ed25519.PrivateKey
		if name != "" {
			key = keyFor(name)
		}
		if len(key) != ed25519.PrivateKeySize {
			writeAlive(w, http.StatusNotFound, ErrorBody{Code: CodeNoName, Error: "This dashboard does not hold that name."})
			return
		}
		writeAlive(w, http.StatusOK, Alive{Name: name, Signature: SignAlive(key, base, name, nonce)})
	})
}

// hostName is the name a Host header asks about under base, or "".
func hostName(host, base string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	name, ok := strings.CutSuffix(strings.TrimSuffix(strings.ToLower(host), "."), "."+strings.ToLower(base))
	if !ok || CheckName(name) != nil {
		return ""
	}
	return name
}

func writeAlive(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
