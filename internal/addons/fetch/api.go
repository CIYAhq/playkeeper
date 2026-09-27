package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// DefaultMaxBody bounds a JSON answer.
const DefaultMaxBody = 16 << 20

// Options configure an API client.
type Options struct {
	BaseURL   string
	UserAgent string
	HTTP      *http.Client
	Now       func() time.Time
	// MaxWait is the longest the client pauses for a rate limit before it
	// returns a *RateLimitError instead. Zero means never wait.
	MaxWait time.Duration
	// MaxBody bounds each answer; DefaultMaxBody when zero.
	MaxBody int64
	// Sleep replaces the pause, for tests.
	Sleep func(ctx context.Context, d time.Duration) error
}

// API is a JSON API on one HTTPS host. It is safe for concurrent use; share
// one per service, because rate limits count per IP address.
type API struct {
	service string
	base    string
	o       Options
	http    *http.Client

	mu    sync.Mutex
	until time.Time
}

// New returns a client for the JSON API at o.BaseURL, named service in
// messages. Redirects must stay on the API's own host.
func New(service string, o Options) *API {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = sleep
	}
	if o.MaxBody <= 0 {
		o.MaxBody = DefaultMaxBody
	}
	a := &API{service: service, base: strings.TrimRight(o.BaseURL, "/"), o: o}
	var hosts Hosts
	if u, err := url.Parse(a.base); err == nil && u.Host != "" {
		hosts = Hosts{u.Host}
	}
	a.http = Guard(o.HTTP, hosts)
	return a
}

// Service is the name used in messages.
func (a *API) Service() string { return a.service }

// Get fetches path with query q and decodes the JSON answer into v.
func (a *API) Get(ctx context.Context, path string, q url.Values, v any) error {
	return a.do(ctx, http.MethodGet, path, q, nil, v)
}

// Post sends body as JSON to path and decodes the JSON answer into v.
func (a *API) Post(ctx context.Context, path string, body, v any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return a.do(ctx, http.MethodPost, path, nil, b, v)
}

func (a *API) do(ctx context.Context, method, path string, q url.Values, body []byte, v any) error {
	u, err := url.Parse(a.base + path)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return &HostError{Host: a.service, Reason: "has no valid HTTPS address configured"}
	}
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	for attempt := 0; ; attempt++ {
		if err := a.wait(ctx); err != nil {
			return err
		}
		var rd io.Reader = http.NoBody
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", a.o.UserAgent)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := a.http.Do(req)
		if err != nil {
			return netError(a.service, err)
		}
		a.observe(resp.Header)
		if resp.StatusCode == http.StatusTooManyRequests {
			d := retryAfter(resp.Header, a.o.Now())
			drain(resp)
			a.pause(d)
			if attempt == 0 && d <= a.o.MaxWait {
				continue
			}
			return &RateLimitError{Service: a.service, RetryAfter: d}
		}
		return a.decode(resp, path, v)
	}
}

func (a *API) decode(resp *http.Response, path string, v any) error {
	defer drain(resp)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{Service: a.service, Status: resp.StatusCode, Path: path, Detail: detail(resp.Body)}
	}
	what := a.service + "'s answer"
	if resp.ContentLength > a.o.MaxBody {
		return &TooLargeError{What: what, Limit: a.o.MaxBody}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, a.o.MaxBody+1))
	if err != nil {
		return netError(a.service, err)
	}
	if int64(len(b)) > a.o.MaxBody {
		return &TooLargeError{What: what, Limit: a.o.MaxBody}
	}
	if v == nil {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return &StatusError{Service: a.service, Status: resp.StatusCode, Path: path, Detail: "the answer is not the JSON Playkeeper expects (" + err.Error() + ")"}
	}
	return nil
}

func (a *API) wait(ctx context.Context) error {
	a.mu.Lock()
	d := a.until.Sub(a.o.Now())
	a.mu.Unlock()
	if d <= 0 {
		return nil
	}
	if d > a.o.MaxWait {
		return &RateLimitError{Service: a.service, RetryAfter: d}
	}
	return a.o.Sleep(ctx, d)
}

func (a *API) pause(d time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t := a.o.Now().Add(d); t.After(a.until) {
		a.until = t
	}
}

// observe holds further requests when a service says the window is used up
// (Modrinth's X-Ratelimit-Remaining: 0), instead of running into a 429.
func (a *API) observe(h http.Header) {
	if h.Get("X-Ratelimit-Remaining") != "0" {
		return
	}
	if s, err := strconv.Atoi(h.Get("X-Ratelimit-Reset")); err == nil {
		a.pause(clampWait(time.Duration(s) * time.Second))
	}
}

func retryAfter(h http.Header, now time.Time) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if s, err := strconv.Atoi(v); err == nil {
			return clampWait(time.Duration(s) * time.Second)
		}
		if t, err := http.ParseTime(v); err == nil {
			return clampWait(t.Sub(now))
		}
	}
	if s, err := strconv.Atoi(h.Get("X-Ratelimit-Reset")); err == nil {
		return clampWait(time.Duration(s) * time.Second)
	}
	return 5 * time.Second
}

func clampWait(d time.Duration) time.Duration {
	return min(max(d, time.Second), time.Hour)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func netError(service string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return &NetError{Service: service, Err: err}
}

func drain(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

// detail pulls a service's explanation out of an error body: Modrinth sends
// "description", Hangar "message".
func detail(r io.Reader) string {
	var body struct {
		Description string `json:"description"`
		Message     string `json:"message"`
	}
	b, _ := io.ReadAll(io.LimitReader(r, 64<<10))
	if json.Unmarshal(b, &body) != nil {
		return ""
	}
	s := body.Description
	if s == "" {
		s = body.Message
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return ' '
	}, s)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return strings.TrimSpace(s)
}
