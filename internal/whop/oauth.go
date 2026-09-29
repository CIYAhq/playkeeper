package whop

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Sign in with Whop is OAuth 2.1 with PKCE, as Whop documents it
// (docs.whop.com/developer/guides/oauth). The dashboard only learns who
// someone is on Whop: their user id and username. It keeps none of Whop's
// tokens.

// SignInScope is what the dashboard asks Whop for: who they are and their
// username, and not their email.
const SignInScope = "openid profile"

// OAuthURL is where Whop's OAuth answers for an API location: /oauth at its
// origin, such as https://api.whop.com/oauth.
func OAuthURL(apiURL string) (string, error) {
	base, err := CheckAPIURL(apiURL)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	return u.Scheme + "://" + u.Host + "/oauth", nil
}

var reClientID = regexp.MustCompile(`^app_[A-Za-z0-9]{1,60}$`)

// ValidClientID reports whether id could be a Whop app's id, app_….
func ValidClientID(id string) bool { return reClientID.MatchString(id) }

// NewVerifier makes a PKCE code verifier: 43 random URL-safe characters.
func NewVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Challenge is a verifier's S256 code challenge.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// OAuth is sign-in through one Whop app.
type OAuth struct {
	// URL is OAuthURL's answer.
	URL string
	// ClientID is the app's id; ClientSecret, which Whop's PKCE flow
	// doesn't need, is sent along when there is one.
	ClientID, ClientSecret string
	// RedirectURI is where Whop sends people back, exactly as the app
	// lists it.
	RedirectURI string
	UserAgent   string
	// HTTP defaults to a client that never follows redirects.
	HTTP *http.Client
}

// AuthorizeURL is where a browser goes to sign in with Whop.
func (o OAuth) AuthorizeURL(state, nonce, verifier string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {o.ClientID},
		"redirect_uri":          {o.RedirectURI},
		"scope":                 {SignInScope},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {Challenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	return o.URL + "/authorize?" + q.Encode()
}

// Tokens are what Whop gives for an authorization code.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

// OAuthError is Whop refusing an OAuth request, such as invalid_grant for
// a code that expired.
type OAuthError struct {
	Status      int
	Code        string
	Description string
}

func (e *OAuthError) Error() string {
	msg := fmt.Sprintf("Whop answered %d", e.Status)
	if e.Code != "" {
		msg += ": " + e.Code
	}
	if e.Description != "" {
		msg += " (" + e.Description + ")"
	}
	return msg
}

// Exchange trades an authorization code, with its verifier, for tokens.
func (o OAuth) Exchange(ctx context.Context, code, verifier string) (Tokens, error) {
	body := map[string]string{"grant_type": "authorization_code", "code": code, "redirect_uri": o.RedirectURI, "client_id": o.ClientID, "code_verifier": verifier}
	if o.ClientSecret != "" {
		body["client_secret"] = o.ClientSecret
	}
	var t Tokens
	if err := o.send(ctx, http.MethodPost, "/token", body, "", &t, code, verifier); err != nil {
		return Tokens{}, err
	}
	if t.AccessToken == "" {
		return Tokens{}, errors.New("Whop's answer had no access token")
	}
	return t, nil
}

// UserInfo is who signed in: their Whop user id, username and name.
type UserInfo struct {
	Subject  string `json:"sub"`
	Username string `json:"preferred_username"`
	Name     string `json:"name"`
}

// UserInfo reads who an access token belongs to.
func (o OAuth) UserInfo(ctx context.Context, accessToken string) (UserInfo, error) {
	var u UserInfo
	err := o.send(ctx, http.MethodGet, "/userinfo", nil, accessToken, &u, accessToken)
	return u, err
}

// Revoke ends a refresh token, which the dashboard never uses.
func (o OAuth) Revoke(ctx context.Context, token string) error {
	body := map[string]string{"token": token, "client_id": o.ClientID}
	if o.ClientSecret != "" {
		body["client_secret"] = o.ClientSecret
	}
	return o.send(ctx, http.MethodPost, "/revoke", body, "", nil, token)
}

// send makes one OAuth request and decodes a 2xx answer into out. hidden
// are values kept out of any error.
func (o OAuth) send(ctx context.Context, method, path string, body any, bearer string, out any, hidden ...string) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(o.URL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if o.UserAgent != "" {
		req.Header.Set("User-Agent", o.UserAgent)
	}
	hc := o.HTTP
	if hc == nil {
		hc = noRedirects
	}
	res, err := hc.Do(req)
	if err != nil {
		for _, h := range append(hidden, o.ClientSecret) {
			err = scrub(err, h)
		}
		return fmt.Errorf("couldn't reach Whop: %w", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil {
		return fmt.Errorf("couldn't read Whop's answer: %w", err)
	}
	if len(b) > maxResponse {
		return errors.New("Whop's answer was too large")
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(b, &e)
		return &OAuthError{Status: res.StatusCode, Code: clip(e.Error, 64), Description: clip(e.Description, 300)}
	}
	if out == nil || len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("Whop's answer didn't read as expected: %w", err)
	}
	return nil
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}
