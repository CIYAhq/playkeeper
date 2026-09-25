package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDiscord stands in for Discord's webhook API as its docs describe it:
// Execute Webhook answers with the message when wait=true, Edit Webhook
// Message edits a message the webhook posted, and errors come with the
// bodies Discord sends (testdata). Replies queued with reply answer the
// next requests instead, in order. Every request is checked against what
// the docs ask of clients (check).
type fakeDiscord struct {
	t   *testing.T
	srv *httptest.Server
	got chan fakeRequest

	mu       sync.Mutex
	tokens   map[string]string // webhook id → token
	messages map[string]bool
	posted   int
	replies  []fakeReply
	requests []fakeRequest
}

// fakeReply is a scripted answer. With Status 0 the fake answers as usual
// and only adds Header.
type fakeReply struct {
	Status int
	Header map[string]string
	Body   string
}

type fakeRequest struct {
	Method string
	Host   string
	Path   string
	Query  url.Values
	Header http.Header
	Raw    string
	Msg    message
}

func newFakeDiscord(t *testing.T) *fakeDiscord {
	f := &fakeDiscord{t: t, got: make(chan fakeRequest, 100), tokens: map[string]string{testID: testToken}, messages: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// client sends requests for https://discord.com to the fake, and fails the
// test for any other destination.
func (f *fakeDiscord) client() *http.Client {
	return &http.Client{Transport: toFake{t: f.t, addr: f.srv.Listener.Addr().String(), base: f.srv.Client().Transport}}
}

type toFake struct {
	t    *testing.T
	addr string
	base http.RoundTripper
}

func (tf toFake) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "discord.com" {
		tf.t.Errorf("a request went to %s://%s instead of https://discord.com", r.URL.Scheme, r.URL.Host)
		return nil, errors.New("only discord.com is allowed")
	}
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host, r.Host = "http", tf.addr, "discord.com"
	base := tf.base
	if base == nil {
		base = &http.Transport{}
	}
	return base.RoundTrip(r)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (f *fakeDiscord) serve(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		f.t.Errorf("reading the request: %v", err)
	}
	req := fakeRequest{Method: r.Method, Host: r.Host, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(), Raw: string(raw)}
	if err := json.Unmarshal(raw, &req.Msg); err != nil {
		f.t.Errorf("%s %s: the body is not JSON: %v", r.Method, r.URL.Path, err)
	}
	f.check(req)
	f.mu.Lock()
	f.requests = append(f.requests, req)
	var reply fakeReply
	if len(f.replies) > 0 {
		reply, f.replies = f.replies[0], f.replies[1:]
	}
	f.mu.Unlock()
	select {
	case f.got <- req:
	default:
	}
	// The rate limit headers of a real answer from Discord.
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("X-RateLimit-Bucket", "3d2712a9e4fe17cc9d3fed4a8e672e5f")
	h.Set("X-RateLimit-Limit", "5")
	h.Set("X-RateLimit-Remaining", "4")
	h.Set("X-RateLimit-Reset-After", "0.400")
	for k, v := range reply.Header {
		h.Set(k, v)
	}
	status, body := reply.Status, reply.Body
	if status == 0 {
		status, body = f.answer(req)
	}
	w.WriteHeader(status)
	io.WriteString(w, body)
}

// check fails the test if a request breaks what Discord's docs ask for:
// JSON with the User-Agent they describe, allowed_mentions that allows no
// mentions, no content, and embeds within the limits.
func (f *fakeDiscord) check(r fakeRequest) {
	t := f.t
	if r.Host != "discord.com" || !strings.HasPrefix(r.Path, "/api/v10/webhooks/") {
		t.Errorf("request to %s%s", r.Host, r.Path)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q", ct)
	}
	if ua := r.Header.Get("User-Agent"); ua != "DiscordBot (https://github.com/CIYAhq/playkeeper, dev)" {
		t.Errorf("User-Agent %q", ua)
	}
	if !strings.Contains(r.Raw, `"allowed_mentions":{"parse":[]}`) {
		t.Errorf("allowed_mentions must be sent with an empty parse list: %s", r.Raw)
	}
	if strings.Contains(r.Raw, `"content"`) {
		t.Errorf("Playkeeper sends embeds only: %s", r.Raw)
	}
	if len(r.Msg.Embeds) == 0 || len(r.Msg.Embeds) > maxEmbeds {
		t.Errorf("%d embeds", len(r.Msg.Embeds))
	}
	total := 0
	for _, e := range r.Msg.Embeds {
		checkLimits(t, e)
		total += e.size()
	}
	if total > maxEmbedsTotal {
		t.Errorf("the embeds have %d characters together", total)
	}
	if r.Method == http.MethodPatch && r.Msg.Flags != 0 {
		t.Errorf("Edit Webhook Message takes no SUPPRESS_NOTIFICATIONS flag: flags %d", r.Msg.Flags)
	}
}

// answer is what Discord says when nothing was scripted.
func (f *fakeDiscord) answer(r fakeRequest) (int, string) {
	parts := strings.Split(strings.TrimPrefix(r.Path, "/api/v10/webhooks/"), "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	token, known := f.tokens[parts[0]]
	switch {
	case len(parts) < 2 || !known:
		return http.StatusNotFound, fixture(f.t, "unknown_webhook.json")
	case parts[1] != token:
		return http.StatusUnauthorized, fixture(f.t, "invalid_webhook_token.json")
	case len(parts) == 2 && r.Method == http.MethodPost:
		if r.Query.Get("wait") != "true" {
			return http.StatusNoContent, ""
		}
		f.posted++
		id := fmt.Sprintf("12893459990000%05d", f.posted)
		f.messages[id] = true
		return http.StatusOK, f.messageJSON(id, r.Msg, false)
	case len(parts) == 4 && parts[2] == "messages" && r.Method == http.MethodPatch:
		if !f.messages[parts[3]] {
			return http.StatusNotFound, fixture(f.t, "unknown_message.json")
		}
		return http.StatusOK, f.messageJSON(parts[3], r.Msg, true)
	}
	return http.StatusMethodNotAllowed, `{"message": "405: Method Not Allowed", "code": 0}`
}

func (f *fakeDiscord) messageJSON(id string, m message, edited bool) string {
	var v map[string]any
	if err := json.Unmarshal([]byte(fixture(f.t, "message.json")), &v); err != nil {
		f.t.Errorf("message.json: %v", err)
	}
	v["id"], v["embeds"], v["flags"] = id, m.Embeds, m.Flags
	if edited {
		v["edited_timestamp"] = "2026-09-25T14:05:00.000000+00:00"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func fixture(t *testing.T, name string) string {
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Errorf("fixture: %v", err)
	}
	return string(b)
}

// reply queues answers for the next requests.
func (f *fakeDiscord) reply(r ...fakeReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, r...)
}

// take returns the requests since the last call.
func (f *fakeDiscord) take() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.requests
	f.requests = nil
	return r
}

// wait returns the next request, for tests that run the Notifier on its own
// goroutine.
func (f *fakeDiscord) wait(t *testing.T) fakeRequest {
	t.Helper()
	select {
	case r := <-f.got:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no request reached Discord")
		return fakeRequest{}
	}
}

func (f *fakeDiscord) addWebhook(id, token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens[id] = token
}

func (f *fakeDiscord) deleteWebhook(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.tokens, id)
}

func (f *fakeDiscord) deleteMessage(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.messages, id)
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func (c *fakeClock) Add(d time.Duration) { c.Set(c.Now().Add(d)) }

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// harness drives a Notifier step by step on a fake clock against the fake
// Discord, and records what it reports. Its cleanup fails the test if the
// token shows up in the log or in a reported delivery.
type harness struct {
	*Notifier
	t          *testing.T
	fake       *fakeDiscord
	clock      *fakeClock
	logs       *syncBuffer
	ids        []string
	deliveries []Delivery
}

func newHarness(t *testing.T, s Settings) *harness {
	h := &harness{t: t, fake: newFakeDiscord(t), clock: &fakeClock{now: t0}, logs: &syncBuffer{}}
	h.Notifier = New(Options{
		Settings:        s,
		Server:          survival,
		Client:          h.fake.client(),
		Now:             h.clock.Now,
		Logger:          slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		StatusInterval:  30 * time.Second,
		OnStatusMessage: func(id string) { h.ids = append(h.ids, id) },
		OnDelivery:      func(d Delivery) { h.deliveries = append(h.deliveries, d) },
	})
	t.Cleanup(func() {
		if strings.Contains(h.logs.String(), testToken) {
			t.Errorf("the token leaked into the log:\n%s", h.logs.String())
		}
		if s := fmt.Sprintf("%+v", h.deliveries); strings.Contains(s, testToken) {
			t.Errorf("the token leaked into a delivery report: %s", s)
		}
	})
	return h
}

// sendDue makes every request that is due now, and returns when the next
// one is due (zero when nothing is waiting).
func (h *harness) sendDue() time.Time {
	h.t.Helper()
	for range 100 {
		next := h.step(context.Background())
		if next.IsZero() || next.After(h.clock.Now()) {
			return next
		}
	}
	h.t.Fatal("the Notifier keeps sending")
	return time.Time{}
}

// settle lets time pass until nothing is waiting.
func (h *harness) settle() {
	h.t.Helper()
	for range 100 {
		next := h.sendDue()
		if next.IsZero() {
			return
		}
		h.clock.Set(next)
	}
	h.t.Fatal("the Notifier never settles")
}
