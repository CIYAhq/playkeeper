package names

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testNonce = "Zm9yLXRoZS1saXZlbmVzcy1jaGVjay0zMi1ieXRlcw"

func TestAliveSignaturesAreBoundToBaseNameNonceAndKey(t *testing.T) {
	key := ed25519.NewKeyFromSeed(testSeed)
	pub := key.Public().(ed25519.PublicKey)
	sig := SignAlive(key, DefaultBase, "alice", testNonce)
	if !VerifyAlive(pub, DefaultBase, "alice", testNonce, sig) {
		t.Fatal("a valid answer does not verify")
	}
	other := ed25519.NewKeyFromSeed([]byte("another-install-seed-of-32-bytes"))
	for _, tc := range []struct {
		name                     string
		pub                      ed25519.PublicKey
		base, alive, nonce, sign string
	}{
		{"another base", pub, "example.com", "alice", testNonce, sig},
		{"another name", pub, DefaultBase, "bob", testNonce, sig},
		{"another nonce", pub, DefaultBase, "alice", testNonce[1:], sig},
		{"another key", other.Public().(ed25519.PublicKey), DefaultBase, "alice", testNonce, sig},
		{"no key", nil, DefaultBase, "alice", testNonce, sig},
		{"a signature that is not base64", pub, DefaultBase, "alice", testNonce, "not base64!"},
		{"a cut signature", pub, DefaultBase, "alice", testNonce, sig[:40]},
	} {
		if VerifyAlive(tc.pub, tc.base, tc.alive, tc.nonce, tc.sign) {
			t.Errorf("%s: the answer verifies", tc.name)
		}
	}
}

func TestAliveAnswersCanNeverPassAsSignedRequests(t *testing.T) {
	msg := aliveMessage(DefaultBase, "alice", testNonce)
	if bytes.HasPrefix(msg, []byte(signingContext+"\n")) {
		t.Fatalf("an answer's message starts like a request's: %q", msg)
	}
	req := signed(t, http.MethodGet, AlivePath+testNonce, "", testNow)
	if VerifyAlive(ed25519.NewKeyFromSeed(testSeed).Public().(ed25519.PublicKey), DefaultBase, "alice", req.Header.Get(HeaderNonce), req.Header.Get(HeaderSignature)) {
		t.Error("a request's signature verifies as an answer")
	}
}

func TestAliveHandlerAnswersOnlyForTheNamesItHolds(t *testing.T) {
	key := ed25519.NewKeyFromSeed(testSeed)
	h := AliveHandler(DefaultBase, func(name string) ed25519.PrivateKey {
		if name == "alice" {
			return key
		}
		return nil
	})
	mux := http.NewServeMux()
	mux.Handle(AlivePattern, h)
	get := func(handler http.Handler, host, path string) (*httptest.ResponseRecorder, []byte) {
		req := httptest.NewRequest(http.MethodGet, "https://"+host+path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		b, _ := io.ReadAll(rec.Body)
		return rec, b
	}
	for _, tc := range []struct {
		name    string
		handler http.Handler
		host    string
	}{
		{"on the mux", mux, "alice.playkeeper.io:8443"},
		{"mounted without the pattern", h, "alice.playkeeper.io:8443"},
		{"a host in capitals with a dot", mux, "ALICE.Playkeeper.IO."},
	} {
		rec, b := get(tc.handler, tc.host, AlivePath+testNonce)
		var a Alive
		if rec.Code != http.StatusOK || json.Unmarshal(b, &a) != nil || a.Name != "alice" || !VerifyAlive(key.Public().(ed25519.PublicKey), DefaultBase, "alice", testNonce, a.Signature) {
			t.Errorf("%s: HTTP %d %s", tc.name, rec.Code, b)
		}
		if rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: headers %v", tc.name, rec.Header())
		}
	}
	for _, tc := range []struct {
		name, host, path, code string
		status                 int
	}{
		{"a name it does not hold", "bob.playkeeper.io:8443", AlivePath + testNonce, CodeNoName, http.StatusNotFound},
		{"another base domain", "alice.example.com:8443", AlivePath + testNonce, CodeNoName, http.StatusNotFound},
		{"an address as the host", "5.75.160.99:8443", AlivePath + testNonce, CodeNoName, http.StatusNotFound},
		{"an invalid name", "-alice.playkeeper.io", AlivePath + testNonce, CodeNoName, http.StatusNotFound},
		{"a short nonce", "alice.playkeeper.io:8443", AlivePath + "abc", CodeInvalidRequest, http.StatusBadRequest},
		{"a nonce with other characters", "alice.playkeeper.io:8443", AlivePath + strings.Repeat("a.", 16), CodeInvalidRequest, http.StatusBadRequest},
	} {
		rec, b := get(mux, tc.host, tc.path)
		var body ErrorBody
		if rec.Code != tc.status || json.Unmarshal(b, &body) != nil || body.Code != tc.code || strings.Contains(string(b), "signature") {
			t.Errorf("%s: HTTP %d %s", tc.name, rec.Code, b)
		}
	}
}
