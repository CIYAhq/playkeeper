// Package fetch is the HTTP plumbing the add-on clients share: HTTPS only,
// hosts from an allowlist, redirects checked against the same list, a size
// limit on everything read, polite handling of rate limits, and downloads
// that are verified against the publisher's hash before anyone uses them.
package fetch

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Hosts is an allowlist of host names. An entry carries a port only for
// servers that do not listen on 443 (test servers).
type Hosts []string

// Allows reports whether u is an https URL without user info on one of h.
func (h Hosts) Allows(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || u.Host == "" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Host), ":443")
	for _, a := range h {
		if strings.ToLower(a) == host {
			return true
		}
	}
	return false
}

// Check parses raw and returns it when h allows it.
func (h Hosts) Check(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, &HostError{Reason: "is not a valid address"}
	}
	if u.Scheme != "https" {
		return nil, &HostError{Host: u.Hostname(), Reason: "is not an HTTPS address"}
	}
	if !h.Allows(u) {
		return nil, &HostError{Host: u.Hostname(), Reason: "is not on the list of allowed hosts"}
	}
	return u, nil
}

// Guard returns a copy of c whose redirects must stay on h. Anything else is
// refused with a *RedirectError (wrapped in the *url.Error from Do).
func Guard(c *http.Client, h Hosts) *http.Client {
	if c == nil {
		c = http.DefaultClient
	}
	g := *c
	g.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("stopped after 5 redirects")
		}
		if !h.Allows(req.URL) {
			return &RedirectError{From: via[len(via)-1].URL.Hostname(), To: req.URL.Hostname()}
		}
		return nil
	}
	return &g
}

// HostError refuses an address outside the allowlist.
type HostError struct {
	Host   string
	Reason string
}

func (e *HostError) Error() string {
	if e.Host == "" {
		return "the download address " + e.Reason
	}
	return e.Host + " " + e.Reason
}

// RedirectError refuses a redirect that leaves the allowlist.
type RedirectError struct{ From, To string }

func (e *RedirectError) Error() string {
	return fmt.Sprintf("%s redirected to %s, which is not an allowed host", e.From, e.To)
}

// NetError is a request that got no answer.
type NetError struct {
	Service string
	Err     error
}

func (e *NetError) Error() string { return "could not reach " + e.Service + ": " + e.Err.Error() }
func (e *NetError) Unwrap() error { return e.Err }

// StatusError is an answer with an unexpected HTTP status. Detail is the
// service's own explanation, when it gave one.
type StatusError struct {
	Service string
	Status  int
	Path    string
	Detail  string
}

// ErrNotFound matches a *StatusError for HTTP 404 with errors.Is.
var ErrNotFound = errors.New("not found")

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("%s answered HTTP %d for %s", e.Service, e.Status, e.Path)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

func (e *StatusError) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound
}

// RateLimitError is returned when a service asked for a pause longer than
// the caller is willing to wait.
type RateLimitError struct {
	Service    string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s asked Playkeeper to slow down; try again in %s", e.Service, roundUp(e.RetryAfter))
}

// TooLargeError refuses a body or file above its limit.
type TooLargeError struct {
	What  string
	Limit int64
}

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("%s is larger than the %s limit", e.What, Size(e.Limit))
}

// SizeError is a download whose length differs from what its publisher said.
type SizeError struct {
	Want, Got int64
}

func (e *SizeError) Error() string {
	return fmt.Sprintf("the download is %d bytes but its publisher lists %d bytes", e.Got, e.Want)
}

// HashError is a download whose hash differs from what its publisher said.
type HashError struct {
	Algo, Want, Got string
}

func (e *HashError) Error() string {
	return fmt.Sprintf("the download's %s hash does not match the one its publisher lists", e.Algo)
}

// Size formats a byte count for messages.
func Size(n int64) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return fmt.Sprintf("%d GiB", n>>30)
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

func roundUp(d time.Duration) time.Duration {
	if r := d.Round(time.Second); r >= d {
		return r
	}
	return d.Round(time.Second) + time.Second
}
