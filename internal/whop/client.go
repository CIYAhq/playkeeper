// Package whop talks to Whop for Sell on Whop, which sells a machine's
// servers through a store on Whop: the seller's account, the plans the store
// sells and what each lets a buyer create. Requests go to Whop's API alone,
// with the seller's account API key, which never appears in logs or errors.
package whop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultAPIURL is Whop's API. Tests and local runs point the panel at
// another (config.Config.WhopAPIURL).
const DefaultAPIURL = "https://api.whop.com/api/v1"

// SandboxAPIURL is Whop's sandbox, where nothing is charged, for
// `playkeeper dev`.
const SandboxAPIURL = "https://sandbox-api.whop.com/api/v1"

// CheckAPIURL accepts an https:// address, and plain http:// only for this
// machine (tests and local runs), as config.Config.WhopAPIURL. Empty means
// DefaultAPIURL.
func CheckAPIURL(raw string) (string, error) {
	if raw == "" {
		return DefaultAPIURL, nil
	}
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("the Whop API location is not a plain URL")
	}
	switch u.Scheme {
	case "https":
		return u.String(), nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return u.String(), nil
		}
	}
	return "", fmt.Errorf("the Whop API location %s must be an https:// address", u.Redacted())
}

// APIVersion pins the shapes of Whop's requests and answers
// (Api-Version-Date), so an API change can't change them under the panel.
const APIVersion = "2026-09-29"

// maxResponse bounds what the client reads from one answer.
const maxResponse = 1 << 20

// maxPages bounds a list the client follows page by page, at 100 a page.
const maxPages = 20

// Client talks to Whop for one seller's account. It is safe for concurrent
// use as long as its fields aren't changed meanwhile.
type Client struct {
	// APIURL defaults to DefaultAPIURL.
	APIURL string
	// Key is the seller's account API key.
	Key string
	// UserAgent names the caller, such as "Playkeeper/0.4.6".
	UserAgent string
	// HTTP defaults to a client that never follows redirects, so the key
	// is only ever sent to APIURL.
	HTTP *http.Client
}

// Error is Whop's refusal of a request: the HTTP status and Whop's own
// words, which may be shown to the seller.
type Error struct {
	Status  int
	Type    string
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Whop answered %d", e.Status)
	}
	return fmt.Sprintf("Whop answered %d: %s", e.Status, e.Message)
}

// KeyRefused reports whether Whop refused the key itself: it's wrong,
// revoked or expired.
func KeyRefused(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusUnauthorized
}

// NotFound reports whether Whop said the thing asked for doesn't exist, or
// isn't the key's to see.
func NotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// ValidKey reports whether k could be an API key: 16 to 256 printable ASCII
// characters without spaces. Whop decides whether it works.
func ValidKey(k string) bool {
	if len(k) < 16 || len(k) > 256 {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] <= ' ' || k[i] > '~' {
			return false
		}
	}
	return true
}

// Ending is the last four characters of a key, which the dashboard shows to
// tell keys apart.
func Ending(k string) string {
	if len(k) <= 4 {
		return ""
	}
	return k[len(k)-4:]
}

var noRedirects = &http.Client{
	Timeout:       20 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (c *Client) base() string {
	if c.APIURL != "" {
		return strings.TrimRight(c.APIURL, "/")
	}
	return DefaultAPIURL
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return noRedirects
}

// do sends one request to path (with query q) and decodes a 2xx answer into
// out, when out isn't nil. Any other answer is an *Error.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	u := c.base() + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Api-Version-Date", APIVersion)
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach Whop: %w", scrub(err, c.Key))
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
		return apiError(res.StatusCode, b)
	}
	if out == nil || len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("Whop's answer didn't read as expected: %w", err)
	}
	return nil
}

// apiError reads Whop's error body, {"error": {"type", "message"}}.
func apiError(status int, b []byte) *Error {
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &body)
	msg := strings.TrimSpace(body.Error.Message)
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return &Error{Status: status, Type: body.Error.Type, Message: msg}
}

// scrub keeps the key out of an error that quotes the request.
func scrub(err error, key string) error {
	if key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "[key]"))
}

// page is one page of a list answer.
type page[T any] struct {
	Data     []T `json:"data"`
	PageInfo struct {
		EndCursor   *string `json:"end_cursor"`
		HasNextPage bool    `json:"has_next_page"`
	} `json:"page_info"`
}

// list follows a list's pages, 100 at a time, up to maxPages of them.
func list[T any](ctx context.Context, c *Client, path string, q url.Values) ([]T, error) {
	var all []T
	q.Set("first", strconv.Itoa(100))
	for range maxPages {
		var p page[T]
		if err := c.do(ctx, http.MethodGet, path, q, nil, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Data...)
		if !p.PageInfo.HasNextPage || p.PageInfo.EndCursor == nil || *p.PageInfo.EndCursor == "" {
			return all, nil
		}
		q.Set("after", *p.PageInfo.EndCursor)
	}
	return nil, fmt.Errorf("Whop's list at %s is longer than %d pages", path, maxPages)
}
