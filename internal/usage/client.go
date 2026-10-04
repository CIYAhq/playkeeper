package usage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Paths the service takes reports at.
const (
	PathInstall   = "/v1/install"
	PathHeartbeat = "/v1/heartbeat"
)

// MaxBody bounds a report, and what the service reads of one.
const MaxBody = 4 << 10

// CheckURL accepts an https:// address without a path, and plain http://
// only for this machine (a test's recorder).
func CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Host == "" {
		return nil, errors.New("the stats service location is not a URL")
	}
	if u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("the stats service location %s must be just a scheme and host", u.Redacted())
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
	return nil, fmt.Errorf("the stats service location %s must be an https:// address", u.Redacted())
}

// NewID makes a random install ID: 128 bits from the system's random
// source, unrelated to anything else on the machine.
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Client sends reports to the stats service. Only what Check accepts is
// sent, and nothing but the report: no cookies, no redirects followed.
type Client struct {
	// URL is DefaultURL when empty.
	URL string
	// HTTP is a client with a 10-second timeout when nil.
	HTTP *http.Client
	// Version goes into the User-Agent.
	Version string
}

// SendInstall reports an installer event.
func (c Client) SendInstall(ctx context.Context, e Install) error {
	if err := e.Check(); err != nil {
		return err
	}
	return c.post(ctx, PathInstall, e)
}

// SendHeartbeat reports a running install.
func (c Client) SendHeartbeat(ctx context.Context, h Heartbeat) error {
	if err := h.Check(); err != nil {
		return err
	}
	return c.post(ctx, PathHeartbeat, h)
}

// reCode is the form of the service's codes, like "rate_limited".
var reCode = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

// ServiceError is an answer the service refused a request with.
type ServiceError struct {
	Host   string
	Status int
	// Code is the service's own, like "rate_limited".
	Code string
	// RetryAfter is how long the service asked to wait, when it said.
	RetryAfter time.Duration
}

func (e *ServiceError) Error() string { return fmt.Sprintf("%s answered HTTP %d", e.Host, e.Status) }

func (c Client) post(ctx context.Context, p string, v any) error {
	return c.do(ctx, p, v, nil, 10*time.Second)
}

// do posts v to path p and reads the answer into out, unless out is nil. A
// client of its own waits up to timeout.
func (c Client) do(ctx context.Context, p string, v, out any, timeout time.Duration) error {
	raw := c.URL
	if raw == "" {
		raw = DefaultURL
	}
	u, err := CheckURL(raw)
	if err != nil {
		return err
	}
	u.Path = path.Join("/", p)
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "playkeeper/"+c.Version)
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	// A copy, so the caller's client keeps following redirects elsewhere.
	nc := *hc
	nc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	nc.Jar = nil
	resp, err := nc.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", u.Host, err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, MaxBody))
	if resp.StatusCode/100 != 2 {
		e := &ServiceError{Host: u.Host, Status: resp.StatusCode}
		var refusal struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(answer, &refusal) == nil && reCode.MatchString(refusal.Code) {
			e.Code = refusal.Code
		}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			e.RetryAfter = time.Duration(s) * time.Second
		}
		return e
	}
	if out != nil {
		if err := json.Unmarshal(answer, out); err != nil {
			return fmt.Errorf("%s answered something that isn't JSON", u.Host)
		}
	}
	return nil
}

// TestFromEnv reports whether PLAYKEEPER_USAGE_TEST marks the install as a
// test.
func TestFromEnv(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv(EnvTest))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// ActionsJob reports whether one of the command lines, as /proc lists them,
// is a GitHub Actions job's worker: it runs only while a job does, on
// GitHub's runners and on self-hosted ones.
func ActionsJob(cmdlines []string) bool {
	for _, c := range cmdlines {
		exe, _, _ := strings.Cut(c, " ")
		if path.Base(exe) == "Runner.Worker" {
			return true
		}
	}
	return false
}
