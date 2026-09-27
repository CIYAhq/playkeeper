package names

import (
	"crypto/ed25519"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

var testSeed = []byte("playkeeper-names-test-seed-32byt")

func signed(t *testing.T, method, target, body string, at time.Time) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, "https://names.playkeeper.io"+target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	SignRequest(req, ed25519.NewKeyFromSeed(testSeed), DefaultBase, []byte(body), at)
	return req
}

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestSignedRequestsVerify(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	req := signed(t, "POST", "/v1/names/alice/address", `{"clearOther":true}`, now)
	s, err := VerifyRequest(req.Header, "POST", "/v1/names/alice/address", []byte(`{"clearOther":true}`), DefaultBase, now.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Key.Equal(ed25519.NewKeyFromSeed(testSeed).Public()) || !s.Time.Equal(now) || len(s.Nonce) < 22 {
		t.Errorf("unexpected signer %+v", s)
	}
	if s.KeyID() != KeyID(s.Key) || len(s.KeyID()) != 16 {
		t.Errorf("key id %q", s.KeyID())
	}
	other := signed(t, "POST", "/v1/names/alice/address", `{"clearOther":true}`, now)
	if other.Header.Get(HeaderNonce) == req.Header.Get(HeaderNonce) {
		t.Error("two requests got the same nonce")
	}
}

func TestTamperedRequestsAreRefused(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	body := `{"port":25566}`
	for _, tc := range []struct {
		name                    string
		change                  func(h http.Header)
		method, uri, body, base string
		want                    string
	}{
		{name: "other body", body: `{"port":25567}`, want: CodeBadSignature},
		{name: "other path", uri: "/v1/names/bob/servers/survival", want: CodeBadSignature},
		{name: "other method", method: "DELETE", want: CodeBadSignature},
		{name: "other service", base: "example.com", want: CodeBadSignature},
		{name: "other timestamp", change: func(h http.Header) { h.Set(HeaderTimestamp, "1790337601") }, want: CodeBadSignature},
		{name: "other nonce", change: func(h http.Header) { h.Set(HeaderNonce, strings.Repeat("A", 22)) }, want: CodeBadSignature},
		{name: "other key", change: func(h http.Header) {
			h.Set(HeaderKey, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
		}, want: CodeBadSignature},
		{name: "short key", change: func(h http.Header) { h.Set(HeaderKey, "AAAA") }, want: CodeBadSignature},
		{name: "garbage signature", change: func(h http.Header) { h.Set(HeaderSignature, "not base64!") }, want: CodeBadSignature},
		{name: "bad nonce", change: func(h http.Header) { h.Set(HeaderNonce, "short") }, want: CodeBadSignature},
		{name: "bad timestamp", change: func(h http.Header) { h.Set(HeaderTimestamp, "noon") }, want: CodeBadSignature},
		{name: "no signature", change: func(h http.Header) { h.Del(HeaderSignature) }, want: CodeUnsigned},
		{name: "no key", change: func(h http.Header) { h.Del(HeaderKey) }, want: CodeUnsigned},
	} {
		req := signed(t, "PUT", "/v1/names/alice/servers/survival", body, now)
		if tc.change != nil {
			tc.change(req.Header)
		}
		method, uri, b, base := "PUT", "/v1/names/alice/servers/survival", body, DefaultBase
		if tc.method != "" {
			method = tc.method
		}
		if tc.uri != "" {
			uri = tc.uri
		}
		if tc.body != "" {
			b = tc.body
		}
		if tc.base != "" {
			base = tc.base
		}
		_, err := VerifyRequest(req.Header, method, uri, []byte(b), base, now)
		if code(err) != tc.want {
			t.Errorf("%s: got %v, want %s", tc.name, err, tc.want)
		}
	}
}

func TestClockSkewBeyondFiveMinutesIsRefusedWithTheServiceTime(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	for _, off := range []time.Duration{-5 * time.Minute, 5 * time.Minute, 0} {
		req := signed(t, "GET", "/v1/names", "", now.Add(off))
		if _, err := VerifyRequest(req.Header, "GET", "/v1/names", nil, DefaultBase, now); err != nil {
			t.Errorf("skew %v must be accepted: %v", off, err)
		}
	}
	for _, off := range []time.Duration{-5*time.Minute - time.Second, 6 * time.Minute, 24 * time.Hour} {
		req := signed(t, "GET", "/v1/names", "", now.Add(off))
		_, err := VerifyRequest(req.Header, "GET", "/v1/names", nil, DefaultBase, now)
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeClockSkew || e.Params["serverTime"] != now.Unix() || e.Hint == "" {
			t.Errorf("skew %v: got %#v, want %s with the service time and a hint", off, err, CodeClockSkew)
		}
	}
}
