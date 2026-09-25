package names

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// maxResponse bounds what the client reads from the service.
const maxResponse = 64 << 10

// Family is the IP version a request goes over.
type Family int

// IP versions for Client.IP; AnyFamily lets the network choose.
const (
	AnyFamily Family = 0
	IPv4      Family = 4
	IPv6      Family = 6
)

// reChallengeValue is an ACME DNS-01 value: the unpadded base64url SHA-256
// of the key authorization.
var reChallengeValue = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// CheckServiceURL accepts https:// addresses without a path, and plain
// http:// only for this machine (tests and local runs).
func CheckServiceURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Host == "" {
		return nil, errors.New("the names service location is not a URL")
	}
	if u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("the names service location %s must be just a scheme and host", u.Redacted())
	}
	switch u.Scheme {
	case "https":
		return u, nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return u, nil
		}
	}
	return nil, fmt.Errorf("the names service location %s must be an https:// address", u.Redacted())
}

// Client talks to the names service for one install. It is safe for
// concurrent use as long as its fields are not changed meanwhile.
type Client struct {
	// ServiceURL defaults to DefaultServiceURL and Base to DefaultBase.
	ServiceURL string
	Base       string
	// Key is the install's key (see LoadOrCreateKey).
	Key ed25519.PrivateKey
	// Name is the install's claimed name. Refresh, Release, the server
	// methods, SetTXT and ClearTXT act on it; set it after Claim.
	Name string
	// HTTP carries requests that may use either IP version; HTTP4 and
	// HTTP6 carry address refreshes over one version only. Nil means
	// built-in clients that never use a proxy (the service has to see this
	// machine's own address) and never follow redirects.
	HTTP, HTTP4, HTTP6 *http.Client
	Now                func() time.Time
}

func (c *Client) base() string {
	if c.Base != "" {
		return c.Base
	}
	return DefaultBase
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) client(f Family) *http.Client {
	switch {
	case f == IPv4 && c.HTTP4 != nil:
		return c.HTTP4
	case f == IPv6 && c.HTTP6 != nil:
		return c.HTTP6
	case f == AnyFamily && c.HTTP != nil:
		return c.HTTP
	case f == IPv4:
		return directClient("tcp4")
	case f == IPv6:
		return directClient("tcp6")
	}
	return directClient("tcp")
}

func directClient(network string) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return d.DialContext(ctx, network, addr)
			},
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			DisableKeepAlives:     true,
		},
	}
}

func (c *Client) do(ctx context.Context, f Family, method, path string, in, out any, signed bool) error {
	service := c.ServiceURL
	if service == "" {
		service = DefaultServiceURL
	}
	u, err := CheckServiceURL(service)
	if err != nil {
		return err
	}
	var body []byte
	if in != nil {
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String()+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "playkeeper (https://github.com/CIYAhq/playkeeper)")
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if signed {
		if len(c.Key) != ed25519.PrivateKeySize {
			return errors.New("the names client has no key")
		}
		SignRequest(req, c.Key, c.base(), body, c.now())
	}
	resp, err := c.client(f).Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the names service at %s: %w", u.Redacted(), err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return fmt.Errorf("reading the names service's answer: %w", err)
	}
	if len(b) > maxResponse {
		return errors.New("the names service's answer is larger than expected")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return responseError(resp, b)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("unexpected answer from the names service: %w", err)
	}
	return nil
}

func responseError(resp *http.Response, b []byte) error {
	e := &Error{Status: resp.StatusCode}
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s <= 2*86400 {
		e.RetryAfter = time.Duration(s) * time.Second
	}
	var body ErrorBody
	if json.Unmarshal(b, &body) == nil && body.Code != "" && body.Error != "" {
		e.Code, e.Message, e.Hint, e.Params = body.Code, body.Error, body.Hint, body.Params
		return e
	}
	e.Code = CodeUnavailable
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		e.Message = fmt.Sprintf("The names service answered with a redirect (HTTP %d), which Playkeeper does not follow.", resp.StatusCode)
		return e
	}
	e.Message = fmt.Sprintf("The names service is not available right now (HTTP %d).", resp.StatusCode)
	e.Hint = "Addresses that already work keep working; try again later."
	return e
}

func (c *Client) namePath(suffix string) (string, error) {
	if c.Name == "" {
		return "", errorf(0, CodeNoName, "Claim one first.", "This install has no %s address yet.", c.base())
	}
	if err := CheckName(c.Name); err != nil {
		return "", err
	}
	return "/v1/names/" + c.Name + suffix, nil
}

// Available reports whether name can be claimed. An invalid name is
// answered here, without asking the service.
func (c *Client) Available(ctx context.Context, name string) (Availability, error) {
	if err := CheckName(name); err != nil {
		e := err.(*Error)
		return Availability{Name: name, Code: e.Code, Message: e.Message, Params: e.Params}, nil
	}
	var a Availability
	err := c.do(ctx, AnyFamily, http.MethodGet, "/v1/names/"+name, nil, &a, false)
	return a, err
}

// Claim takes name for this install's key and points it at the address the
// request comes from. Claiming a name the key already holds is harmless.
// Call Refresh right after, so that both IPv4 and IPv6 are set.
func (c *Client) Claim(ctx context.Context, name string) (Name, error) {
	if err := CheckName(name); err != nil {
		return Name{}, err
	}
	var n Name
	err := c.do(ctx, AnyFamily, http.MethodPut, "/v1/names/"+name, nil, &n, true)
	return n, err
}

// Refresh keeps the name alive and points it at this machine's current
// addresses: it sends one request over IPv4 and one over IPv6, and the
// service sets the A and AAAA records from where they came from. When one
// IP version cannot reach the service at all (no route, no address), the
// record of that version is removed so players are not sent to an address
// the machine no longer has. It fails only if neither version got through.
func (c *Client) Refresh(ctx context.Context) (Name, error) {
	path, err := c.namePath("/address")
	if err != nil {
		return Name{}, err
	}
	send := func(f Family, clearOther bool) (Name, error) {
		var n Name
		err := c.do(ctx, f, http.MethodPost, path, RefreshRequest{ClearOther: clearOther}, &n, true)
		return n, err
	}
	n4, err4 := send(IPv4, false)
	n6, err6 := send(IPv6, false)
	switch {
	case err4 == nil && err6 == nil:
		return n6, nil
	case err4 == nil:
		if familyUnavailable(err6) && n4.IPv6 != "" {
			return send(IPv4, true)
		}
		return n4, nil
	case err6 == nil:
		if familyUnavailable(err4) && n6.IPv4 != "" {
			return send(IPv6, true)
		}
		return n6, nil
	case familyUnavailable(err4):
		return Name{}, err6
	}
	return Name{}, err4
}

// familyUnavailable reports whether a request failed because this machine
// has no way to reach the service over that IP version, as opposed to a
// timeout or an answer, which say nothing about the address.
func familyUnavailable(err error) bool {
	var addrErr *net.AddrError
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &addrErr):
		return true
	case errors.As(err, &dnsErr):
		return dnsErr.IsNotFound
	}
	return errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.ECONNREFUSED)
}

// Release gives the name up: its records are removed and it is held from
// other installs for a while, during which this key can claim it again.
func (c *Client) Release(ctx context.Context) (Name, error) {
	path, err := c.namePath("")
	if err != nil {
		return Name{}, err
	}
	var n Name
	err = c.do(ctx, AnyFamily, http.MethodDelete, path, nil, &n, true)
	return n, err
}

// Names lists the names this key holds, including lapsed and released
// ones, so a restored install can find its address again.
func (c *Client) Names(ctx context.Context) ([]Name, error) {
	var l NameList
	err := c.do(ctx, AnyFamily, http.MethodGet, "/v1/names", nil, &l, true)
	return l.Names, err
}

func serverPath(label string) (string, error) {
	if label == "" {
		return "/servers/@", nil
	}
	if err := CheckServerLabel(label); err != nil {
		return "", err
	}
	return "/servers/" + label, nil
}

// SetServer gives a Minecraft server its own address under the name
// (label "survival" gives survival.alice.playkeeper.io; "" the name
// itself) with an SRV record pointing at the name and port.
func (c *Client) SetServer(ctx context.Context, label string, port int) (Server, error) {
	sp, err := serverPath(label)
	if err != nil {
		return Server{}, err
	}
	if port < 1 || port > 65535 {
		return Server{}, errorf(400, CodeInvalidPort, "", "The port must be between 1 and 65535.")
	}
	path, err := c.namePath(sp)
	if err != nil {
		return Server{}, err
	}
	var s Server
	err = c.do(ctx, AnyFamily, http.MethodPut, path, ServerRequest{Port: port}, &s, true)
	return s, err
}

// RemoveServer removes a server's SRV record.
func (c *Client) RemoveServer(ctx context.Context, label string) error {
	sp, err := serverPath(label)
	if err != nil {
		return err
	}
	path, err := c.namePath(sp)
	if err != nil {
		return err
	}
	return c.do(ctx, AnyFamily, http.MethodDelete, path, nil, nil, true)
}

func (c *Client) challengePath(fqdn, value string) (string, error) {
	if c.Name == "" {
		return c.namePath("")
	}
	want := ChallengeFQDN(c.Name, c.base())
	if got := strings.TrimSuffix(strings.ToLower(fqdn), "."); got != want {
		return "", errorf(400, CodeInvalidChallenge, "", "%s is not this install's challenge record (%s).", fqdn, want)
	}
	if !reChallengeValue.MatchString(value) {
		return "", errorf(400, CodeInvalidChallenge, "", "An ACME DNS-01 challenge value is 43 characters of base64url.")
	}
	return c.namePath("/acme-challenge/" + value)
}

// SetTXT publishes an ACME DNS-01 challenge: a TXT record at fqdn, which
// must be _acme-challenge.<name>.<base>. It returns once Cloudflare has the
// record; the service removes it after an hour if ClearTXT is not called.
func (c *Client) SetTXT(ctx context.Context, fqdn, value string) error {
	path, err := c.challengePath(fqdn, value)
	if err != nil {
		return err
	}
	var ch Challenge
	if err := c.do(ctx, AnyFamily, http.MethodPut, path, nil, &ch, true); err != nil {
		return err
	}
	if ch.DNS != DNSOK {
		return errorf(503, CodeDNSPending, "Try again in a few minutes.", "Cloudflare has not published the challenge record yet.")
	}
	return nil
}

// ClearTXT removes a challenge SetTXT published.
func (c *Client) ClearTXT(ctx context.Context, fqdn, value string) error {
	path, err := c.challengePath(fqdn, value)
	if err != nil {
		return err
	}
	return c.do(ctx, AnyFamily, http.MethodDelete, path, nil, nil, true)
}

// IP asks which address the service sees this machine's requests come from,
// over one IP version or either. It needs no key.
func (c *Client) IP(ctx context.Context, f Family) (IPInfo, error) {
	var info IPInfo
	err := c.do(ctx, f, http.MethodGet, "/v1/ip", nil, &info, false)
	return info, err
}
