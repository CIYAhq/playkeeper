package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/usage"
)

// playkeeper.io's own numbers for the funnel: copies of the install command,
// which its pages count here, and its visitors and demo opens, which the
// service reads from Open Analytics with its own read key.

// SiteOrigin is the only page origin whose counts are taken.
const SiteOrigin = "https://playkeeper.io"

// EventInstallCopied is a copy of the install command on playkeeper.io.
const EventInstallCopied = "install_copied"

const (
	// maxSiteEvent bounds a count's body.
	maxSiteEvent = 256
	// Counts from one address, on top of the address's limit for every
	// request: the site sends one a page view at most.
	siteEventsPerIPBurst, siteEventsPerIPHour = 10, 20
	// siteEventsPerDay bounds the counts taken in a day, everyone together.
	siteEventsPerDay = 20000
)

// siteEvent is what a page sends: the event and the playkeeper.io/install
// channel code, if any. Nothing else is read.
type siteEvent struct {
	Event   string `json:"event"`
	Channel string `json:"channel"`
}

// hSiteEvent takes a count from one of playkeeper.io's pages. The page
// sends it as a CORS request, which every browser sends with the page's
// origin, and as text/plain, so it needs no preflight; the answer lets the
// page see it arrived. The address it came from is used for the rate limits
// and dropped.
func (s *Service) hSiteEvent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", SiteOrigin)
	w.Header().Set("Vary", "Origin")
	if r.Header.Get("Origin") != SiteOrigin {
		writeError(w, http.StatusForbidden, "forbidden", "Counts come from "+SiteOrigin+" alone.")
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || (mt != "text/plain" && mt != "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Counts are JSON, sent as text/plain.")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSiteEvent+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The count could not be read.")
		return
	}
	if len(body) > maxSiteEvent {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "The count is too large.")
		return
	}
	var e siteEvent
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&e); err != nil || dec.More() {
		writeError(w, http.StatusBadRequest, "invalid_request", "The count is not JSON.")
		return
	}
	if e.Event != EventInstallCopied {
		writeError(w, http.StatusBadRequest, "invalid_request", "event is not valid.")
		return
	}
	if usage.CleanChannel(e.Channel) != e.Channel {
		writeError(w, http.StatusBadRequest, "invalid_request", "channel is not valid.")
		return
	}
	addr, _ := s.clientAddr(r)
	if ok, wait := s.perIPSite.allow(addrBucket(addr)); !ok {
		rateLimited(w, wait)
		return
	}
	if ok, wait := s.siteEvents.allow(""); !ok {
		rateLimited(w, wait)
		return
	}
	_, err = s.db.ExecContext(r.Context(), `INSERT INTO site_hours(hour, event, channel, n) VALUES(?, ?, ?, 1)
		ON CONFLICT(hour, event, channel) DO UPDATE SET n = n + 1`, hour(s.now()), e.Event, e.Channel)
	s.accepted(w, err)
}

// siteCopies counts the copies of the install command from since on.
func (s *Service) siteCopies(ctx context.Context, since int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(n), 0) FROM site_hours WHERE event = ? AND hour >= ?`, EventInstallCopied, since).Scan(&n)
	return n, err
}

// How often the site's numbers are read from Open Analytics: at most every
// siteEvery, and after a failed read, not again for siteRetry.
const (
	siteEvery = 15 * time.Minute
	siteRetry = 2 * time.Minute
)

// siteNumbers are the site's visitors and demo opens in one window.
type siteNumbers struct {
	visitors, demoOpens int
}

// siteReader reads playkeeper.io's numbers from Open Analytics, and keeps
// them for siteEvery.
type siteReader struct {
	api, key string
	client   *http.Client

	mu      sync.Mutex
	windows map[string]siteNumbers
	readAt  time.Time
	triedAt time.Time
	err     string
}

func newSiteReader(cfg Config) *siteReader {
	if cfg.OAKey == "" {
		return nil
	}
	api := cfg.OAAPI
	if api == "" {
		api = DefaultOAAPI
	}
	c := cfg.OAClient
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	nc := *c
	// The key goes to Open Analytics and nowhere a redirect points.
	nc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &siteReader{api: strings.TrimRight(api, "/"), key: cfg.OAKey, client: &nc}
}

// numbers are the site's numbers for each window, when they were read, and
// why the latest read failed, if it did. A read that fails keeps the numbers
// from before. A read outlives the request that started it, so one dropped
// halfway isn't taken for Open Analytics failing; requests that come
// meanwhile wait for it, at most its 15 seconds.
func (r *siteReader) numbers(ctx context.Context, now time.Time) (map[string]siteNumbers, time.Time, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fresh := !r.readAt.IsZero() && now.Sub(r.readAt) < siteEvery
	if !fresh && (r.triedAt.IsZero() || now.Sub(r.triedAt) >= siteRetry) {
		r.triedAt = now
		windows, err := r.read(context.WithoutCancel(ctx), now)
		if err != nil {
			r.err = err.Error()
		} else {
			r.windows, r.readAt, r.err = windows, now, ""
		}
	}
	return r.windows, r.readAt, r.err
}

// read asks Open Analytics for each window's visitors and demo opens, the
// windows starting on the hour as the service's own do and ending with this
// hour.
func (r *siteReader) read(ctx context.Context, now time.Time) (map[string]siteNumbers, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	type result struct {
		window string
		n      siteNumbers
		err    error
	}
	results := make(chan result, len(windows))
	for _, w := range windows {
		go func() {
			q := url.Values{
				"from":     {time.Unix(hour(now)-int64(w.span/time.Second), 0).UTC().Format("2006-01-02T15:04:05.000Z")},
				"to":       {time.Unix(hour(now)+3600, 0).UTC().Format("2006-01-02T15:04:05.000Z")},
				"timezone": {"UTC"},
			}
			var n siteNumbers
			var overview struct {
				Totals struct {
					Visitors *int `json:"visitors"`
				} `json:"totals"`
			}
			err := r.get(ctx, "analytics/overview", q, &overview)
			if err == nil && overview.Totals.Visitors == nil {
				err = errors.New("its overview has no visitors")
			}
			if err == nil {
				n.visitors = *overview.Totals.Visitors
				q.Set("limit", "500")
				var pages struct {
					Items []struct {
						Path      string `json:"page_path"`
						Visitors  int    `json:"visitors"`
						Entrances int    `json:"entrances"`
					} `json:"items"`
				}
				err = r.get(ctx, "analytics/pages", q, &pages)
				// The demo opens: its visitors at its start, and those who
				// arrived straight on one of its pages, which a link can
				// point to.
				for _, p := range pages.Items {
					switch {
					case p.Path == "/demo/" || p.Path == "/demo":
						n.demoOpens += p.Visitors
					case strings.HasPrefix(p.Path, "/demo/"):
						n.demoOpens += p.Entrances
					}
				}
			}
			results <- result{w.name, n, err}
		}()
	}
	out := map[string]siteNumbers{}
	var errs []error
	for range windows {
		res := <-results
		if res.err != nil {
			errs = append(errs, res.err)
			continue
		}
		out[res.window] = res.n
	}
	if len(errs) > 0 {
		return nil, errs[0]
	}
	return out, nil
}

// get reads one of Open Analytics' read endpoints into v. Errors say what
// failed, never the key; Summary puts them after "Open Analytics: ".
func (r *siteReader) get(ctx context.Context, path string, q url.Values, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.api+"/v1/read/"+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	// A read key is bound to its site: Open Analytics refuses one sent
	// with the site's header too.
	req.Header.Set("Authorization", "Bearer "+r.key)
	req.Header.Set("Accept", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("it could not be reached for %s", path)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("its answer for %s could not be read", path)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		status := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if code := reErrorCode.FindString(e.Error.Code); code != "" {
			status += " " + code
		}
		return fmt.Errorf("it answered %s for %s", status, path)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("its answer for %s is not what the service reads", path)
	}
	return nil
}

// reErrorCode is as much of an Open Analytics error code as the summary
// passes on.
var reErrorCode = regexp.MustCompile(`^[A-Z0-9_]{1,40}`)
