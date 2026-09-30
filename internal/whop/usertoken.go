package whop

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Whop's proxy, which serves an app's pages inside whop.com, adds a
// short-lived token saying who's looking to each request for the app's own
// origin: the page, and the calls it makes to relative URLs. It's an ES256
// JWT with Whop as its issuer, the app as its audience and the user as its
// subject, signed with one of the keys Whop publishes at JWKSURL.

// UserTokenHeader carries the token.
const UserTokenHeader = "x-whop-user-token"

const (
	userTokenIssuer = "urn:whopcom:exp-proxy"
	// keysKept is how long Whop's keys are used before they're read again,
	// and keysCooldown how soon they may be read again for a token signed
	// with a key they don't have, as when Whop rotates them.
	keysKept     = 12 * time.Hour
	keysCooldown = 30 * time.Second
	// keysReadFor bounds a read of Whop's keys, which goes on when the
	// request that started it gives up.
	keysReadFor  = 10 * time.Second
	maxUserToken = 8 << 10
)

// ErrUserToken refuses a token that isn't Whop's for the app, or has
// expired.
var ErrUserToken = errors.New("the request doesn't carry Whop's token for this app")

// JWKSURL is where Whop publishes the keys it signs user tokens with, for an
// API location: /.well-known/jwks.json at its origin.
func JWKSURL(apiURL string) (string, error) {
	base, err := CheckAPIURL(apiURL)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	return u.Scheme + "://" + u.Host + "/.well-known/jwks.json", nil
}

// UserTokens checks Whop's user tokens with the keys Whop publishes. It is
// safe for concurrent use.
type UserTokens struct {
	// URL is JWKSURL's answer, and HTTP the client it's read with.
	URL  string
	HTTP *http.Client

	mu        sync.Mutex
	keys      map[string]*ecdsa.PublicKey
	fetchedAt time.Time
	triedAt   time.Time
}

// Verify checks that token was signed by Whop for app and hasn't expired at
// now, and returns the user it's for (user_…).
func (u *UserTokens) Verify(ctx context.Context, token, app string, now time.Time) (string, error) {
	if token == "" || len(token) > maxUserToken {
		return "", ErrUserToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", ErrUserToken
	}
	var head struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	var claims struct {
		Iss string          `json:"iss"`
		Sub string          `json:"sub"`
		Aud json.RawMessage `json:"aud"`
		Exp *float64        `json:"exp"`
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 || decodeSegment(parts[0], &head) != nil || decodeSegment(parts[1], &claims) != nil || head.Alg != "ES256" {
		return "", ErrUserToken
	}
	var aud string
	if json.Unmarshal(claims.Aud, &aud) != nil || aud != app || app == "" || claims.Iss != userTokenIssuer || claims.Sub == "" ||
		claims.Exp == nil || !now.Before(time.Unix(int64(*claims.Exp), 0)) {
		return "", ErrUserToken
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	for _, fresh := range []bool{false, true} {
		keys, err := u.keysFor(ctx, head.Kid, fresh, now)
		if err != nil {
			return "", err
		}
		for _, k := range keys {
			if ecdsa.Verify(k, sum[:], r, s) {
				return claims.Sub, nil
			}
		}
	}
	return "", ErrUserToken
}

// keysFor are the keys a token with kid may be signed with: that key, or
// every key for a token that names none. Whop's keys are read again after
// keysKept, and, when fresh, for a kid they don't have, but never twice
// within keysCooldown, so a Whop that doesn't answer or a flood of unknown
// kids isn't asked at every request.
func (u *UserTokens) keysFor(ctx context.Context, kid string, fresh bool, now time.Time) ([]*ecdsa.PublicKey, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	_, known := u.keys[kid]
	due := u.keys == nil || now.Sub(u.fetchedAt) >= keysKept || (fresh && (kid == "" || !known))
	if due && (u.triedAt.IsZero() || now.Sub(u.triedAt) >= keysCooldown) {
		u.triedAt = now
		// A read cut short by its caller would start the cooldown with no
		// keys, refusing every token until it ends, so it outlives the caller.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), keysReadFor)
		keys, err := u.fetch(rctx)
		cancel()
		if err == nil {
			u.keys, u.fetchedAt = keys, now
		} else if u.keys == nil {
			return nil, err
		}
	}
	if u.keys == nil {
		return nil, errors.New("Whop's signing keys couldn't be read, so no token can be checked yet")
	}
	if kid != "" {
		if k := u.keys[kid]; k != nil {
			return []*ecdsa.PublicKey{k}, nil
		}
		return nil, nil
	}
	all := make([]*ecdsa.PublicKey, 0, len(u.keys))
	for _, k := range u.keys {
		all = append(all, k)
	}
	return all, nil
}

func (u *UserTokens) fetch(ctx context.Context) (map[string]*ecdsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	c := u.HTTP
	if c == nil {
		c = noRedirects
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("couldn't read Whop's signing keys: %w", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil || len(b) > maxResponse || res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("couldn't read Whop's signing keys (HTTP %d)", res.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty, Crv, Kid, X, Y, Alg string
		} `json:"keys"`
	}
	if err := json.Unmarshal(b, &set); err != nil {
		return nil, fmt.Errorf("Whop's signing keys didn't read as expected: %w", err)
	}
	keys := map[string]*ecdsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" || (k.Alg != "" && k.Alg != "ES256") {
			continue
		}
		if pub := p256Key(k.X, k.Y); pub != nil {
			keys[k.Kid] = pub
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("Whop published no key its tokens can be checked with")
	}
	return keys, nil
}

// p256Key is the P-256 public key at x, y, nil when that isn't a point on
// the curve.
func p256Key(x, y string) *ecdsa.PublicKey {
	xb, err1 := base64.RawURLEncoding.DecodeString(x)
	yb, err2 := base64.RawURLEncoding.DecodeString(y)
	if err1 != nil || err2 != nil || len(xb) != 32 || len(yb) != 32 {
		return nil
	}
	if _, err := ecdh.P256().NewPublicKey(append(append([]byte{4}, xb...), yb...)); err != nil {
		return nil
	}
	return &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(xb), Y: new(big.Int).SetBytes(yb)}
}

func decodeSegment(s string, into any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

// Access is what a user may do with a resource, such as a business: "admin"
// for anyone on its team, whatever their role, "customer" for a buyer, and
// "no_access".
func (c *Client) Access(ctx context.Context, userID, resourceID string) (string, error) {
	var a struct {
		HasAccess bool   `json:"has_access"`
		Level     string `json:"access_level"`
	}
	if err := c.do(ctx, http.MethodGet, "/users/"+url.PathEscape(userID)+"/access/"+url.PathEscape(resourceID), nil, nil, &a); err != nil {
		return "", err
	}
	if !a.HasAccess || a.Level == "" {
		return "no_access", nil
	}
	return a.Level, nil
}

// SignUserToken signs a user token as Whop's proxy does, for tests and
// fakes.
func SignUserToken(key *ecdsa.PrivateKey, kid, app, user string, exp time.Time) string {
	head, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": kid, "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iss": userTokenIssuer, "aud": app, "sub": user, "exp": exp.Unix(), "iat": exp.Add(-time.Hour).Unix()})
	signed := base64.RawURLEncoding.EncodeToString(head) + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signed))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		panic(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// UserTokenKeys is key as Whop publishes it at JWKSURL, for tests and fakes.
func UserTokenKeys(kid string, key *ecdsa.PublicKey) []byte {
	x, y := make([]byte, 32), make([]byte, 32)
	key.X.FillBytes(x)
	key.Y.FillBytes(y)
	b, _ := json.Marshal(map[string]any{"keys": []map[string]string{{"kty": "EC", "use": "sig", "crv": "P-256", "kid": kid, "alg": "ES256",
		"x": base64.RawURLEncoding.EncodeToString(x), "y": base64.RawURLEncoding.EncodeToString(y)}}})
	return b
}
