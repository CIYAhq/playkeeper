// Package hetzner reads Hetzner Cloud's stock of the machines the owner
// watches, so they hear when one can be bought and added to the dashboard,
// and the servers in the owner's project, so a machine that joins from one
// of them is known to be theirs. It only reads (GET /server_types and GET
// /servers), with a read-only API token from the owner's project, which
// never appears in logs or errors.
package hetzner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultAPIURL is Hetzner Cloud's API. Tests point the panel at another
// (config.Config.HetznerAPIURL).
const DefaultAPIURL = "https://api.hetzner.cloud/v1"

// consoleCreate is where Hetzner's console creates a server.
const consoleCreate = "https://console.hetzner.com/create/server"

// CheckAPIURL accepts an https:// address, and plain http:// only for this
// machine (tests and local runs), as config.Config.HetznerAPIURL. Empty
// means DefaultAPIURL.
func CheckAPIURL(raw string) (string, error) {
	if raw == "" {
		return DefaultAPIURL, nil
	}
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("the Hetzner API location is not a plain URL")
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
	return "", fmt.Errorf("the Hetzner API location %s must be an https:// address", u.Redacted())
}

// maxResponse bounds what the client reads from one answer. One server
// type with its locations is a few kilobytes.
const maxResponse = 256 << 10

var reToken = regexp.MustCompile(`^[A-Za-z0-9]{64}$`)

// ValidToken reports whether t has the shape of a Hetzner Cloud API token:
// 64 letters and digits. Hetzner decides whether it works.
func ValidToken(t string) bool { return reToken.MatchString(t) }

// Ending is the last four characters of a token, which the dashboard shows
// to tell tokens apart.
func Ending(t string) string {
	if len(t) <= 4 {
		return ""
	}
	return t[len(t)-4:]
}

var reName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidName reports whether s can be a server type's or a location's name
// as Hetzner writes them, such as "cx53" or "fsn1".
func ValidName(s string) bool { return reName.MatchString(s) }

// cities are the places of Hetzner's locations.
var cities = map[string]string{
	"fsn1": "Falkenstein",
	"nbg1": "Nuremberg",
	"hel1": "Helsinki",
	"ash":  "Ashburn",
	"hil":  "Hillsboro",
	"sin":  "Singapore",
}

// City is where location is, such as "Falkenstein" for "fsn1", or the
// location's own name in capitals for one this Playkeeper doesn't know.
func City(location string) string {
	if c, ok := cities[location]; ok {
		return c
	}
	return strings.ToUpper(location)
}

// BuyURL is Hetzner's console with a server of this type at this location
// picked, and a public IPv4 address, which Minecraft players need. It is ""
// unless both are valid names.
func BuyURL(serverType, location string) string {
	if !ValidName(serverType) || !ValidName(location) {
		return ""
	}
	q := url.Values{"type": {serverType}, "location": {location}, "useIPv4": {"true"}}
	return consoleCreate + "?" + q.Encode()
}

// Client reads stock for one project's token. It is safe for concurrent use
// as long as its fields aren't changed meanwhile.
type Client struct {
	// APIURL defaults to DefaultAPIURL.
	APIURL string
	// Token is a read-only API token of the owner's project.
	Token string
	// UserAgent names the caller, such as "Playkeeper/0.4.7".
	UserAgent string
	// HTTP defaults to a client that never follows redirects, so the token
	// is only ever sent to APIURL.
	HTTP *http.Client
}

// Error is Hetzner's refusal of a request: the HTTP status and Hetzner's
// own code and words, which may be shown to the owner.
type Error struct {
	Status  int
	Code    string
	Message string
	// RetryAfter is when Hetzner's rate limit lets the token ask again, for
	// a 429; zero if Hetzner didn't say.
	RetryAfter time.Time
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Hetzner answered %d", e.Status)
	}
	return fmt.Sprintf("Hetzner answered %d: %s", e.Status, e.Message)
}

// TokenRefused reports whether Hetzner refused the token itself: it's
// wrong, or was deleted.
func TokenRefused(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusUnauthorized
}

// RateLimited reports whether Hetzner asked the token to slow down, and
// until when if it said.
func RateLimited(err error) (bool, time.Time) {
	var e *Error
	if errors.As(err, &e) && e.Status == http.StatusTooManyRequests {
		return true, e.RetryAfter
	}
	return false, time.Time{}
}

// ErrNoSuchType is the answer for a server type Hetzner doesn't sell.
var ErrNoSuchType = errors.New("Hetzner sells no server type by that name")

// Stock is a server type and whether it can be created now in each place
// Hetzner offers it.
type Stock struct {
	// Type is the server type's name, such as "cx53", and Description
	// Hetzner's, such as "CX53".
	Type        string
	Description string
	Cores       int
	MemoryGB    float64
	DiskGB      int
	Locations   []Location
}

// Location is one place a server type is offered. Available is Hetzner's
// own hint, "only an indicator whether resources are currently available
// and no guarantee": creating the server is what really tells.
type Location struct {
	Name      string
	Available bool
}

// Stock reads serverType's stock: GET /server_types?name=serverType.
func (c *Client) Stock(ctx context.Context, serverType string) (Stock, error) {
	if !ValidName(serverType) {
		return Stock{}, ErrNoSuchType
	}
	var body struct {
		ServerTypes []struct {
			Name        string  `json:"name"`
			Description string  `json:"description"`
			Cores       int     `json:"cores"`
			Memory      float64 `json:"memory"`
			Disk        int     `json:"disk"`
			Locations   []struct {
				Name      string `json:"name"`
				Available bool   `json:"available"`
			} `json:"locations"`
		} `json:"server_types"`
	}
	if err := c.get(ctx, "/server_types", url.Values{"name": {serverType}}, &body); err != nil {
		return Stock{}, err
	}
	for _, t := range body.ServerTypes {
		if t.Name != serverType {
			continue
		}
		s := Stock{Type: t.Name, Description: t.Description, Cores: t.Cores, MemoryGB: t.Memory, DiskGB: t.Disk}
		seen := map[string]bool{}
		for _, l := range t.Locations {
			if !ValidName(l.Name) || seen[l.Name] {
				continue
			}
			seen[l.Name] = true
			s.Locations = append(s.Locations, Location{Name: l.Name, Available: l.Available})
		}
		return s, nil
	}
	return Stock{}, ErrNoSuchType
}

// Server is one server in the token's project, and its public addresses.
type Server struct {
	ID   int64
	Name string
	// IPv4 is its public IPv4 address, if it has one, and IPv6 its public
	// IPv6 network, such as 2a01:4f8:c17:1234::/64, which is its alone.
	IPv4 netip.Addr
	IPv6 netip.Prefix
}

// Has reports whether addr is one of s's public addresses.
func (s Server) Has(addr netip.Addr) bool {
	addr = addr.Unmap()
	return s.IPv4.IsValid() && s.IPv4 == addr || s.IPv6.IsValid() && s.IPv6.Contains(addr)
}

// maxServerPages bounds how many pages of serversPerPage Servers reads,
// far more servers than a Playkeeper fleet has.
const (
	serversPerPage = 25
	maxServerPages = 40
)

// Servers lists the project's servers: GET /servers, page by page.
func (c *Client) Servers(ctx context.Context) ([]Server, error) {
	var out []Server
	for page := 1; ; {
		var body struct {
			Servers []struct {
				ID        int64  `json:"id"`
				Name      string `json:"name"`
				PublicNet struct {
					IPv4 *struct {
						IP string `json:"ip"`
					} `json:"ipv4"`
					IPv6 *struct {
						IP string `json:"ip"`
					} `json:"ipv6"`
				} `json:"public_net"`
			} `json:"servers"`
			Meta struct {
				Pagination struct {
					NextPage *int `json:"next_page"`
				} `json:"pagination"`
			} `json:"meta"`
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(serversPerPage)}}
		if err := c.get(ctx, "/servers", q, &body); err != nil {
			return nil, err
		}
		for _, s := range body.Servers {
			v := Server{ID: s.ID, Name: s.Name}
			if n := s.PublicNet.IPv4; n != nil {
				v.IPv4, _ = netip.ParseAddr(n.IP)
			}
			if n := s.PublicNet.IPv6; n != nil {
				v.IPv6, _ = netip.ParsePrefix(n.IP)
			}
			out = append(out, v)
		}
		next := body.Meta.Pagination.NextPage
		switch {
		case next == nil || *next <= page:
			return out, nil
		case *next > maxServerPages:
			return nil, errors.New("the Hetzner project has more servers than Playkeeper reads")
		}
		page = *next
	}
}

var noRedirects = &http.Client{
	Timeout:       20 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// get sends one GET to path (with query q) and decodes a 2xx answer into
// out. Any other answer is an *Error.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	base := DefaultAPIURL
	if c.APIURL != "" {
		base = strings.TrimRight(c.APIURL, "/")
	}
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	hc := c.HTTP
	if hc == nil {
		hc = noRedirects
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach Hetzner: %w", scrub(err, c.Token))
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil {
		return fmt.Errorf("couldn't read Hetzner's answer: %w", scrub(err, c.Token))
	}
	if len(b) > maxResponse {
		return errors.New("Hetzner's answer was too large")
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return apiError(res, b)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("Hetzner's answer didn't read as expected: %w", err)
	}
	return nil
}

// apiError reads Hetzner's error body, {"error": {"code", "message"}}, and
// for a 429 its RateLimit-Reset header, the Unix time the limit resets.
func apiError(res *http.Response, b []byte) *Error {
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &body)
	msg := strings.TrimSpace(body.Error.Message)
	if len(msg) > 300 {
		msg = msg[:300]
	}
	e := &Error{Status: res.StatusCode, Code: body.Error.Code, Message: msg}
	if res.StatusCode == http.StatusTooManyRequests {
		var reset int64
		if _, err := fmt.Sscan(res.Header.Get("RateLimit-Reset"), &reset); err == nil && reset > 0 {
			e.RetryAfter = time.Unix(reset, 0)
		}
	}
	return e
}

// scrub keeps the token out of an error that quotes the request.
func scrub(err error, token string) error {
	if token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "[token]"))
}
