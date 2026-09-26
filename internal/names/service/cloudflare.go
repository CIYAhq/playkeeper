package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// cloudflareAPI is Cloudflare's API v4.
const cloudflareAPI = "https://api.cloudflare.com/client/v4"

const (
	maxCloudflareBody = 4 << 20
	// maxPages bounds a listing: the records under one name are the few the
	// service creates plus whatever someone added by hand.
	maxPages = 20
)

// Cloudflare error codes the service tells apart.
const (
	cfCodeRecordMissing   = 81044
	cfCodeIdenticalRecord = 81058
)

// cloudflare is a client for the parts of the DNS records API the service
// uses. Only dns.go calls its mutating methods, through the guard.
type cloudflare struct {
	api, token, zone string
	hc               *http.Client
	now              func() time.Time
	perPage          int

	mu          sync.Mutex
	pausedUntil time.Time
}

type cfRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content,omitempty"`
	Data    *cfSRV `json:"data,omitempty"`
	TTL     int    `json:"ttl,omitempty"`
	Proxied *bool  `json:"proxied,omitempty"`
	Comment string `json:"comment,omitempty"`
}

type cfSRV struct {
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
	Port     int    `json:"port"`
	Target   string `json:"target"`
}

type cfMessage struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Chain   []cfMessage `json:"error_chain,omitempty"`
}

func (m cfMessage) String() string {
	s := m.Message
	if m.Code != 0 {
		s = strconv.Itoa(m.Code) + " " + s
	}
	if len(m.Chain) > 0 {
		var parts []string
		for _, c := range m.Chain {
			parts = append(parts, c.String())
		}
		s += " (" + strings.Join(parts, "; ") + ")"
	}
	return s
}

type cfEnvelope struct {
	Success    bool            `json:"success"`
	Errors     []cfMessage     `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

type cfZone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type cfUsage struct {
	Quota *int `json:"record_quota"`
	Usage int  `json:"record_usage"`
}

// cfError is a failed Cloudflare call. It never holds the token: requests
// are described by method and path only.
type cfError struct {
	Op         string
	Status     int
	Errors     []cfMessage
	RetryAfter time.Duration
	Err        error
}

func (e *cfError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("could not reach Cloudflare for %s: %v", e.Op, e.Err)
	}
	var msgs []string
	for _, m := range e.Errors {
		msgs = append(msgs, m.String())
	}
	if len(msgs) == 0 {
		msgs = append(msgs, "no error details")
	}
	return fmt.Sprintf("Cloudflare answered HTTP %d to %s: %s", e.Status, e.Op, strings.Join(msgs, "; "))
}

func (e *cfError) Unwrap() error { return e.Err }

func (e *cfError) has(code int) bool {
	return slices.ContainsFunc(e.Errors, func(m cfMessage) bool { return m.Code == code })
}

// temporary reports whether trying again later may work, as opposed to a
// refused token, a wrong zone or a request Cloudflare will never accept.
func (e *cfError) temporary() bool {
	return e.Err != nil || e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// auth reports whether Cloudflare refused the token. An unknown or revoked
// token gets HTTP 400 with code 9106, not 401.
func (e *cfError) auth() bool {
	if e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden {
		return true
	}
	for _, code := range []int{6003, 6111, 9103, 9106, 9107, 9109, 10000} {
		if e.has(code) {
			return true
		}
	}
	return false
}

func (c *cloudflare) paused() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pausedUntil.Sub(c.now())
}

func (c *cloudflare) pause(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if until := c.now().Add(d); until.After(c.pausedUntil) {
		c.pausedUntil = until
	}
}

func (c *cloudflare) do(ctx context.Context, method, path string, query url.Values, in, out any) (*cfEnvelope, error) {
	op := method + " " + path
	if wait := c.paused(); wait > 0 {
		return nil, &cfError{Op: op, Status: http.StatusTooManyRequests, RetryAfter: wait,
			Errors: []cfMessage{{Code: 971, Message: "paused after Cloudflare's rate limit"}}}
	}
	u := c.api + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "playkeeper-names (https://github.com/CIYAhq/playkeeper)")
	resp, err := c.hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, &cfError{Op: op, Err: err}
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxCloudflareBody {
		return nil, &cfError{Op: op, Status: resp.StatusCode, Errors: []cfMessage{{Message: "answer larger than expected"}}}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxCloudflareBody+1))
	if err != nil {
		return nil, &cfError{Op: op, Err: err}
	}
	if len(b) > maxCloudflareBody {
		return nil, &cfError{Op: op, Status: resp.StatusCode, Errors: []cfMessage{{Message: "answer larger than expected"}}}
	}
	var env cfEnvelope
	jsonErr := json.Unmarshal(b, &env)
	if c.token != "" {
		scrub(env.Errors, c.token)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		// Cloudflare blocks a token for five minutes once it is over the limit.
		wait := 5 * time.Minute
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s <= 3600 {
			wait = time.Duration(s) * time.Second
		}
		c.pause(wait)
		return nil, &cfError{Op: op, Status: resp.StatusCode, Errors: env.Errors, RetryAfter: wait}
	}
	if resp.StatusCode/100 != 2 || jsonErr != nil || !env.Success {
		e := &cfError{Op: op, Status: resp.StatusCode, Errors: env.Errors}
		if jsonErr != nil && len(e.Errors) == 0 {
			e.Errors = []cfMessage{{Message: "the answer is not Cloudflare's JSON"}}
		}
		return nil, e
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return nil, &cfError{Op: op, Status: resp.StatusCode, Errors: []cfMessage{{Message: "unexpected result: " + err.Error()}}}
		}
	}
	return &env, nil
}

// scrub removes the token from messages Cloudflare may echo it in.
func scrub(ms []cfMessage, token string) {
	for i := range ms {
		ms[i].Message = strings.ReplaceAll(ms[i].Message, token, "[token]")
		scrub(ms[i].Chain, token)
	}
}

func (c *cloudflare) records() string { return "/zones/" + c.zone + "/dns_records" }

func (c *cloudflare) getZone(ctx context.Context) (cfZone, error) {
	var z cfZone
	_, err := c.do(ctx, http.MethodGet, "/zones/"+c.zone, nil, nil, &z)
	return z, err
}

func (c *cloudflare) usage(ctx context.Context) (cfUsage, error) {
	var u cfUsage
	_, err := c.do(ctx, http.MethodGet, c.records()+"/usage", nil, nil, &u)
	return u, err
}

func (c *cloudflare) list(ctx context.Context, filter url.Values) ([]cfRecord, error) {
	var all []cfRecord
	for page := 1; page <= maxPages; page++ {
		q := url.Values{}
		for k, v := range filter {
			q[k] = v
		}
		q.Set("per_page", strconv.Itoa(c.perPage))
		q.Set("page", strconv.Itoa(page))
		var recs []cfRecord
		env, err := c.do(ctx, http.MethodGet, c.records(), q, nil, &recs)
		if err != nil {
			return nil, err
		}
		all = append(all, recs...)
		if env.ResultInfo == nil || page >= env.ResultInfo.TotalPages || len(recs) == 0 {
			return all, nil
		}
	}
	return nil, &cfError{Op: "GET " + c.records(), Status: http.StatusOK, Errors: []cfMessage{{Message: "more records under one name than the service ever creates"}}}
}

// under lists the records at fqdn and below it.
func (c *cloudflare) under(ctx context.Context, fqdn string) ([]cfRecord, error) {
	at, err := c.list(ctx, url.Values{"name.exact": {fqdn}})
	if err != nil {
		return nil, err
	}
	below, err := c.list(ctx, url.Values{"name.endswith": {"." + fqdn}})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []cfRecord
	for _, r := range append(at, below...) {
		if !seen[r.ID] {
			seen[r.ID] = true
			out = append(out, r)
		}
	}
	return out, nil
}

func (c *cloudflare) create(ctx context.Context, r cfRecord) error {
	_, err := c.do(ctx, http.MethodPost, c.records(), nil, r, nil)
	var ce *cfError
	if errors.As(err, &ce) && ce.has(cfCodeIdenticalRecord) {
		return nil
	}
	return err
}

func (c *cloudflare) update(ctx context.Context, id string, r cfRecord) error {
	_, err := c.do(ctx, http.MethodPatch, c.records()+"/"+url.PathEscape(id), nil, r, nil)
	return err
}

func (c *cloudflare) delete(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, c.records()+"/"+url.PathEscape(id), nil, nil, nil)
	var ce *cfError
	if errors.As(err, &ce) && ce.has(cfCodeRecordMissing) {
		return nil
	}
	return err
}
