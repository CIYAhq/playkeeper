// Package mojang looks Minecraft: Java Edition accounts up by name with
// Mojang's public profile API, so Playkeeper can tell a real account from a
// typo before it changes a server's whitelist.
//
// Answers are cached for a few minutes, and requests to Mojang are budgeted:
// Mojang limits each IP address, and the Minecraft servers on the same
// machine look names up from that address too.
package mojang

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// DefaultBaseURL is Mojang's profile service, where Minecraft servers
// themselves look names up since 1.20.4. api.mojang.com answers the same
// lookup path and is accepted too.
const DefaultBaseURL = "https://api.minecraftservices.com"

const (
	lookupPath     = "/minecraft/profile/lookup/name/"
	maxAnswerBytes = 16 << 10
	userAgent      = "playkeeper (https://github.com/CIYAhq/playkeeper)"
)

// Defaults for Options.
const (
	// DefaultFoundTTL matches the max-age Mojang sends with a profile.
	DefaultFoundTTL    = 5 * time.Minute
	DefaultNotFoundTTL = time.Minute
	// DefaultPerMinute keeps Playkeeper well inside Mojang's limit of about
	// 200 requests in 2 minutes per address, which it shares with the
	// Minecraft servers on the machine.
	DefaultPerMinute  = 30
	DefaultMaxEntries = 1000
	// defaultBackoff is how long to wait after a 429 without Retry-After.
	defaultBackoff = time.Minute
	maxBackoff     = 10 * time.Minute
)

// A lookup that finds no usable account fails with an *Error wrapping one of
// these, so callers can use errors.Is.
var (
	ErrInvalidName = errors.New("Minecraft usernames are 3–16 letters, numbers or underscores.")
	ErrNotFound    = errors.New("No Minecraft: Java Edition account has that name.")
	ErrRateLimited = errors.New("Mojang's account service asked Playkeeper to slow down.")
	ErrUnavailable = errors.New("Mojang's account service could not be reached or gave an unexpected answer.")
)

// Error is a failed lookup. Err is one of the Err values; RetryAfter is set
// when rate limited; Detail says what happened, for logs.
type Error struct {
	Err        error
	RetryAfter time.Duration
	Detail     string
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return e.Err.Error()
	}
	return strings.TrimSuffix(e.Err.Error(), ".") + " (" + e.Detail + ")."
}

func (e *Error) Unwrap() error { return e.Err }

// Profile is a Minecraft: Java Edition account.
type Profile struct {
	// ID is the account's UUID, lower case with hyphens, as whitelist.json
	// stores it.
	ID string `json:"id"`
	// Name has the capitalisation the account chose.
	Name string `json:"name"`
	// Demo is set when the account does not own Java Edition, so it cannot
	// play on servers.
	Demo bool `json:"demo,omitempty"`
	// Legacy is set for an old account that was never migrated and can no
	// longer sign in.
	Legacy bool `json:"legacy,omitempty"`
}

// Options configure a Client. Zero fields take the defaults.
type Options struct {
	// BaseURL is the profile service: https://api.minecraftservices.com or
	// https://api.mojang.com. Tests may use an https:// loopback address.
	BaseURL string
	// HTTP is copied; the copy never follows redirects.
	HTTP *http.Client
	Now  func() time.Time
	// FoundTTL and NotFoundTTL are how long answers are reused.
	FoundTTL    time.Duration
	NotFoundTTL time.Duration
	// PerMinute is how many requests the client sends to Mojang per minute.
	PerMinute int
	// MaxEntries bounds the cache.
	MaxEntries int
}

// Client looks accounts up and caches the answers. It is safe for
// concurrent use.
type Client struct {
	base        string
	http        *http.Client
	now         func() time.Time
	foundTTL    time.Duration
	notFoundTTL time.Duration
	perMinute   float64
	maxEntries  int

	mu           sync.Mutex
	cache        map[string]entry
	tokens       float64
	refilled     time.Time
	blockedUntil time.Time
}

type entry struct {
	profile Profile
	found   bool
	expires time.Time
}

// NewClient checks the options and returns a client.
func NewClient(o Options) (*Client, error) {
	raw := o.BaseURL
	if raw == "" {
		raw = DefaultBaseURL
	}
	base, err := checkBaseURL(raw)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{}
	if o.HTTP != nil {
		copied := *o.HTTP
		hc = &copied
	}
	if hc.Timeout == 0 {
		hc.Timeout = 10 * time.Second
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c := &Client{
		base: base, http: hc, now: o.Now,
		foundTTL: o.FoundTTL, notFoundTTL: o.NotFoundTTL,
		perMinute: float64(o.PerMinute), maxEntries: o.MaxEntries,
		cache: map[string]entry{},
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.foundTTL <= 0 {
		c.foundTTL = DefaultFoundTTL
	}
	if c.notFoundTTL <= 0 {
		c.notFoundTTL = DefaultNotFoundTTL
	}
	if c.perMinute <= 0 {
		c.perMinute = DefaultPerMinute
	}
	if c.maxEntries <= 0 {
		c.maxEntries = DefaultMaxEntries
	}
	c.tokens = c.perMinute
	return c, nil
}

// checkBaseURL accepts https:// addresses of Mojang's two API hosts, and
// https:// loopback addresses for tests, without a path.
func checkBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("the Mojang profile service must be an https:// address without a path, not %q", raw)
	}
	host := u.Hostname()
	if ip, err := netip.ParseAddr(host); err == nil && ip.IsLoopback() {
		return "https://" + u.Host, nil
	}
	if (host == "api.minecraftservices.com" || host == "api.mojang.com") && u.Port() == "" {
		return "https://" + host, nil
	}
	return "", fmt.Errorf("%s is not one of Mojang's profile services (api.minecraftservices.com or api.mojang.com)", u.Host)
}

// Lookup finds the account called name, in any capitalisation. A name that
// is not a valid Java Edition username is refused without asking Mojang.
func (c *Client) Lookup(ctx context.Context, name string) (Profile, error) {
	if !minecraft.ValidPlayerName(name) {
		return Profile{}, &Error{Err: ErrInvalidName}
	}
	key := strings.ToLower(name)
	if p, err, ok := c.cached(key); ok {
		return p, err
	}
	if err := c.take(); err != nil {
		return Profile{}, err
	}
	p, err := c.fetch(ctx, name)
	c.remember(key, p, err)
	return p, err
}

func (c *Client) cached(key string) (Profile, error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[key]
	if !ok {
		return Profile{}, nil, false
	}
	if !c.now().Before(e.expires) {
		delete(c.cache, key)
		return Profile{}, nil, false
	}
	if !e.found {
		return Profile{}, &Error{Err: ErrNotFound, Detail: "answer from a few moments ago"}, true
	}
	return e.profile, nil, true
}

// take spends one request of the budget, or says how long to wait: after
// Mojang answered 429, or when this client has sent its share this minute.
func (c *Client) take() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if now.Before(c.blockedUntil) {
		return &Error{Err: ErrRateLimited, RetryAfter: c.blockedUntil.Sub(now), Detail: "waiting as Mojang asked"}
	}
	if !c.refilled.IsZero() {
		c.tokens = min(c.perMinute, c.tokens+max(0, now.Sub(c.refilled).Minutes())*c.perMinute)
	}
	c.refilled = now
	if c.tokens < 1 {
		wait := time.Duration((1 - c.tokens) / c.perMinute * float64(time.Minute))
		return &Error{Err: ErrRateLimited, RetryAfter: wait, Detail: fmt.Sprintf("Playkeeper sends at most %d lookups a minute", int(c.perMinute))}
	}
	c.tokens--
	return nil
}

// remember caches found accounts and names Mojang does not know. Other
// failures are not cached.
func (c *Client) remember(key string, p Profile, err error) {
	var e entry
	switch {
	case err == nil:
		e = entry{profile: p, found: true, expires: c.now().Add(c.foundTTL)}
	case errors.Is(err, ErrNotFound):
		e = entry{expires: c.now().Add(c.notFoundTTL)}
	default:
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.cache[key]; !ok && len(c.cache) >= c.maxEntries {
		c.evict()
	}
	c.cache[key] = e
}

// evict drops expired entries, and if none were, the one expiring first.
func (c *Client) evict() {
	now := c.now()
	oldest, first := "", time.Time{}
	for k, e := range c.cache {
		if !now.Before(e.expires) {
			delete(c.cache, k)
			continue
		}
		if oldest == "" || e.expires.Before(first) {
			oldest, first = k, e.expires
		}
	}
	if len(c.cache) >= c.maxEntries {
		delete(c.cache, oldest)
	}
}

func (c *Client) block(wait time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if until := c.now().Add(wait); until.After(c.blockedUntil) {
		c.blockedUntil = until
	}
}

func (c *Client) fetch(ctx context.Context, name string) (Profile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+lookupPath+url.PathEscape(name), nil)
	if err != nil {
		return Profile{}, &Error{Err: ErrUnavailable, Detail: err.Error()}
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Profile{}, &Error{Err: ErrUnavailable, Detail: err.Error()}
	}
	defer resp.Body.Close()
	switch code := resp.StatusCode; {
	case code == http.StatusOK:
		return decode(resp, name)
	case code == http.StatusNoContent:
		return Profile{}, &Error{Err: ErrNotFound}
	case code == http.StatusNotFound:
		return Profile{}, notFound(resp)
	case code == http.StatusTooManyRequests:
		wait := retryAfter(resp.Header.Get("Retry-After"), c.now())
		c.block(wait)
		return Profile{}, &Error{Err: ErrRateLimited, RetryAfter: wait, Detail: "HTTP 429"}
	case code >= 300 && code < 400:
		return Profile{}, &Error{Err: ErrUnavailable, Detail: fmt.Sprintf("refused a redirect (HTTP %d)", code)}
	default:
		return Profile{}, &Error{Err: ErrUnavailable, Detail: fmt.Sprintf("HTTP %d", code)}
	}
}

// read returns the body, refusing anything over maxAnswerBytes.
func read(resp *http.Response) ([]byte, error) {
	if resp.ContentLength > maxAnswerBytes {
		return nil, fmt.Errorf("an answer of %d bytes, more than the %d a profile can be", resp.ContentLength, maxAnswerBytes)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading the answer failed: %v", err)
	}
	if len(b) > maxAnswerBytes {
		return nil, fmt.Errorf("an answer larger than the %d bytes a profile can be", maxAnswerBytes)
	}
	return b, nil
}

// notFound tells "no account has this name" (a 404 whose body only has path
// and errorMessage) from "this address does not exist" (a 404 that also has
// an error field), so a moved endpoint is not taken for an unknown player.
func notFound(resp *http.Response) error {
	b, err := read(resp)
	if err != nil {
		return &Error{Err: ErrUnavailable, Detail: err.Error()}
	}
	var body struct {
		Error string `json:"error"`
	}
	if len(strings.TrimSpace(string(b))) > 0 {
		if json.Unmarshal(b, &body) != nil {
			return &Error{Err: ErrUnavailable, Detail: "HTTP 404 with an answer that is not JSON"}
		}
	}
	if body.Error != "" {
		return &Error{Err: ErrUnavailable, Detail: "HTTP 404 for the lookup address itself"}
	}
	return &Error{Err: ErrNotFound}
}

func decode(resp *http.Response, name string) (Profile, error) {
	b, err := read(resp)
	if err != nil {
		return Profile{}, &Error{Err: ErrUnavailable, Detail: err.Error()}
	}
	var a struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Demo   bool   `json:"demo"`
		Legacy bool   `json:"legacy"`
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return Profile{}, &Error{Err: ErrUnavailable, Detail: "the answer is not a profile"}
	}
	id, ok := NormalizeUUID(a.ID)
	if !ok || !minecraft.ValidPlayerName(a.Name) || !strings.EqualFold(a.Name, name) {
		return Profile{}, &Error{Err: ErrUnavailable, Detail: "the answer is not a profile for " + name}
	}
	return Profile{ID: id, Name: a.Name, Demo: a.Demo, Legacy: a.Legacy}, nil
}

// NormalizeUUID accepts a UUID with or without hyphens, in either case, and
// returns it lower case with hyphens.
func NormalizeUUID(s string) (string, bool) {
	if len(s) == 36 {
		if s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
			return "", false
		}
		s = s[:8] + s[9:13] + s[14:18] + s[19:23] + s[24:]
	}
	if len(s) != 32 {
		return "", false
	}
	s = strings.ToLower(s)
	for i := 0; i < len(s); i++ {
		if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f') {
			return "", false
		}
	}
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], true
}

// retryAfter reads a Retry-After header (seconds or an HTTP date), bounded
// to between a second and ten minutes.
func retryAfter(h string, now time.Time) time.Duration {
	var d time.Duration
	if n, err := strconv.Atoi(strings.TrimSpace(h)); err == nil {
		if n > int(maxBackoff/time.Second) {
			return maxBackoff
		}
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(h); err == nil {
		d = t.Sub(now)
	}
	switch {
	case d <= 0:
		return defaultBackoff
	case d < time.Second:
		return time.Second
	case d > maxBackoff:
		return maxBackoff
	}
	return d
}
