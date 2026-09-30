package whop

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// signRaw signs any header and claims, as a forger might.
func signRaw(t *testing.T, key *ecdsa.PrivateKey, head, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(head)
	c, _ := json.Marshal(claims)
	signed := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	sum := sha256.Sum256([]byte(signed))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// keyServer publishes the keys it's given at the moment, as Whop's JWKS,
// and counts how often it's read.
type keyServer struct {
	mu    sync.Mutex
	keys  []byte
	reads int
	srv   *httptest.Server
}

func newKeyServer(t *testing.T, kid string, key *ecdsa.PrivateKey) *keyServer {
	t.Helper()
	ks := &keyServer{keys: UserTokenKeys(kid, &key.PublicKey)}
	ks.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ks.mu.Lock()
		defer ks.mu.Unlock()
		ks.reads++
		w.Header().Set("Content-Type", "application/json")
		w.Write(ks.keys)
	}))
	t.Cleanup(ks.srv.Close)
	return ks
}

func (ks *keyServer) publish(kid string, key *ecdsa.PrivateKey) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.keys = UserTokenKeys(kid, &key.PublicKey)
}

func (ks *keyServer) count() int {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	return ks.reads
}

// A user token is Whop's, for this app, for a user, and not expired; any
// other is refused, however close.
func TestUserTokensAreWhopsForTheAppAndUnexpired(t *testing.T) {
	key, other := newKey(t), newKey(t)
	ks := newKeyServer(t, "k1", key)
	u := &UserTokens{URL: ks.srv.URL}
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC)
	good := SignUserToken(key, "k1", "app_pk", "user_seller", now.Add(time.Hour))
	if user, err := u.Verify(ctx, good, "app_pk", now); err != nil || user != "user_seller" {
		t.Fatalf("Verify = %q, %v", user, err)
	}
	head := map[string]any{"alg": "ES256", "kid": "k1"}
	claims := func(change map[string]any) map[string]any {
		c := map[string]any{"iss": "urn:whopcom:exp-proxy", "aud": "app_pk", "sub": "user_seller", "exp": now.Add(time.Hour).Unix()}
		for k, v := range change {
			if v == nil {
				delete(c, k)
			} else {
				c[k] = v
			}
		}
		return c
	}
	parts := strings.Split(good, ".")
	tampered, _ := json.Marshal(claims(map[string]any{"sub": "user_someoneelse"}))
	for name, token := range map[string]string{
		"expired":             SignUserToken(key, "k1", "app_pk", "user_seller", now),
		"for another app":     SignUserToken(key, "k1", "app_other", "user_seller", now.Add(time.Hour)),
		"signed by another":   SignUserToken(other, "k1", "app_pk", "user_seller", now.Add(time.Hour)),
		"another issuer":      signRaw(t, key, head, claims(map[string]any{"iss": "someone"})),
		"audience list":       signRaw(t, key, head, claims(map[string]any{"aud": []string{"app_pk"}})),
		"no user":             signRaw(t, key, head, claims(map[string]any{"sub": nil})),
		"no expiry":           signRaw(t, key, head, claims(map[string]any{"exp": nil})),
		"another algorithm":   signRaw(t, key, map[string]any{"alg": "HS256", "kid": "k1"}, claims(nil)),
		"its claims changed":  parts[0] + "." + base64.RawURLEncoding.EncodeToString(tampered) + "." + parts[2],
		"not a token":         "abc",
		"none":                "",
		"a signature cut off": parts[0] + "." + parts[1] + "." + parts[2][:20],
	} {
		if user, err := u.Verify(ctx, token, "app_pk", now); err == nil {
			t.Errorf("%s: taken, for %q", name, user)
		}
	}
	if _, err := u.Verify(ctx, good, "", now); err == nil {
		t.Error("a token taken for no app")
	}
	if n := ks.count(); n > 2 {
		t.Errorf("Whop's keys were read %d times", n)
	}
}

// Whop's keys are read once and kept, read again for a token signed with a
// key they don't have (as when Whop rotates them) but never twice within
// the cooldown, and read again after twelve hours.
func TestUserTokensFollowWhopsKeysWithoutAskingAtEveryRequest(t *testing.T) {
	k1, k2 := newKey(t), newKey(t)
	ks := newKeyServer(t, "k1", k1)
	u := &UserTokens{URL: ks.srv.URL}
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC)
	for range 3 {
		if _, err := u.Verify(ctx, SignUserToken(k1, "k1", "app_pk", "user_a", now.Add(time.Hour)), "app_pk", now); err != nil {
			t.Fatal(err)
		}
	}
	if n := ks.count(); n != 1 {
		t.Fatalf("read %d times for one key", n)
	}
	ks.publish("k2", k2)
	now = now.Add(time.Minute)
	if user, err := u.Verify(ctx, SignUserToken(k2, "k2", "app_pk", "user_b", now.Add(time.Hour)), "app_pk", now); err != nil || user != "user_b" || ks.count() != 2 {
		t.Fatalf("after Whop rotated its key: %q, %v, %d reads", user, err, ks.count())
	}
	for range 5 {
		if _, err := u.Verify(ctx, SignUserToken(k2, "k9", "app_pk", "user_b", now.Add(time.Hour)), "app_pk", now.Add(time.Second)); err == nil {
			t.Fatal("a token with an unknown key was taken")
		}
	}
	if n := ks.count(); n != 2 {
		t.Fatalf("unknown keys within the cooldown read Whop's keys %d times in all", n)
	}
	if _, err := u.Verify(ctx, SignUserToken(k2, "k9", "app_pk", "user_b", now.Add(time.Hour)), "app_pk", now.Add(31*time.Second)); err == nil || ks.count() != 3 {
		t.Fatalf("an unknown key after the cooldown: %v, %d reads", err, ks.count())
	}
	if _, err := u.Verify(ctx, signRaw(t, k2, map[string]any{"alg": "ES256"},
		map[string]any{"iss": "urn:whopcom:exp-proxy", "aud": "app_pk", "sub": "user_c", "exp": now.Add(time.Hour).Unix()}), "app_pk", now.Add(time.Second)); err != nil {
		t.Fatalf("a token naming no key, signed with a known one: %v", err)
	}
	later := now.Add(13 * time.Hour)
	if _, err := u.Verify(ctx, SignUserToken(k2, "k2", "app_pk", "user_b", later.Add(time.Hour)), "app_pk", later); err != nil || ks.count() != 4 {
		t.Fatalf("after twelve hours: %v, %d reads", err, ks.count())
	}
}

// A request that's gone before Whop's keys are read, as when a seller
// leaves the page, doesn't cut the read short: the keys are kept, and the
// next request is checked with them instead of refused until the cooldown
// ends.
func TestAReadOfWhopsKeysOutlivesTheRequestThatStartedIt(t *testing.T) {
	key := newKey(t)
	ks := newKeyServer(t, "k1", key)
	u := &UserTokens{URL: ks.srv.URL}
	now := time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC)
	token := SignUserToken(key, "k1", "app_pk", "user_seller", now.Add(time.Hour))
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	u.Verify(gone, token, "app_pk", now)
	if user, err := u.Verify(context.Background(), token, "app_pk", now.Add(time.Second)); err != nil || user != "user_seller" || ks.count() != 1 {
		t.Fatalf("the next request: %q, %v, %d reads", user, err, ks.count())
	}
}

// Whop's keys are at its API's origin.
func TestJWKSURLIsAtTheAPIsOrigin(t *testing.T) {
	for api, want := range map[string]string{"": "https://api.whop.com/.well-known/jwks.json", "https://api.whop.com/api/v1": "https://api.whop.com/.well-known/jwks.json",
		"http://127.0.0.1:9000/api/v1": "http://127.0.0.1:9000/.well-known/jwks.json"} {
		if got, err := JWKSURL(api); err != nil || got != want {
			t.Errorf("JWKSURL(%q) = %q, %v", api, got, err)
		}
	}
	if _, err := JWKSURL("http://example.com/api/v1"); err == nil {
		t.Error("an http:// address elsewhere")
	}
}

// Whop's access check says what a user may do with a business: "admin" for
// anyone on its team.
func TestAccessSaysWhatAUserMayDoWithABusiness(t *testing.T) {
	c := fake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /users/user_seller/access/biz_seller":   answer(map[string]any{"has_access": true, "access_level": "admin"}),
		"GET /users/user_buyer/access/biz_seller":    answer(map[string]any{"has_access": true, "access_level": "customer"}),
		"GET /users/user_stranger/access/biz_seller": answer(map[string]any{"has_access": false, "access_level": "no_access"}),
	})
	for user, want := range map[string]string{"user_seller": "admin", "user_buyer": "customer", "user_stranger": "no_access"} {
		if got, err := c.Access(context.Background(), user, "biz_seller"); err != nil || got != want {
			t.Errorf("Access(%s) = %q, %v", user, got, err)
		}
	}
}
