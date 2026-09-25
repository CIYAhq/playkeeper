package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// cannedClient is a Cloudflare client whose API is h.
func cannedClient(t *testing.T, h http.HandlerFunc) (*cloudflare, *testClock) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	clk := &testClock{t: testStart}
	return &cloudflare{api: srv.URL + "/client/v4", token: testToken, zone: testZoneID, hc: srv.Client(), now: clk.Now, perPage: 100}, clk
}

func answer(status int, body string, header ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func asCFError(t *testing.T, err error) *cfError {
	t.Helper()
	var ce *cfError
	if !errors.As(err, &ce) {
		t.Fatalf("got %v, want a Cloudflare error", err)
	}
	return ce
}

func TestCloudflareClientReadsTheZoneUsageAndEveryPage(t *testing.T) {
	ctx := context.Background()
	cf := newFakeCloudflare(t)
	c := &cloudflare{api: cf.srv.URL + "/client/v4", token: testToken, zone: testZoneID, hc: cf.srv.Client(), now: time.Now, perPage: 2}
	z, err := c.getZone(ctx)
	if err != nil || z != (cfZone{ID: testZoneID, Name: "playkeeper.io", Status: "active"}) {
		t.Fatalf("zone: %+v, %v", z, err)
	}
	u, err := c.usage(ctx)
	if err != nil || u.Quota == nil || *u.Quota != 200 || u.Usage != 18 {
		t.Fatalf("usage: %+v, %v", u, err)
	}
	cf.setQuota(nil)
	if u, err := c.usage(ctx); err != nil || u.Quota != nil {
		t.Errorf("usage of a zone without a quota: %+v, %v", u, err)
	}

	before := cf.requestCount()
	recs, err := c.under(ctx, "playkeeper.io")
	if err != nil || len(recs) != 18 {
		t.Fatalf("records under the apex: %d, %v", len(recs), err)
	}
	if got := cf.requestCount() - before; got != 4+5 {
		t.Errorf("read 8 + 10 records two at a time in %d requests, want 9", got)
	}
	seen := map[string]bool{}
	for _, r := range recs {
		if seen[r.ID] {
			t.Errorf("record %s listed twice", r.ID)
		}
		seen[r.ID] = true
		switch r.ID {
		case seedDaveSRV:
			if r.Data == nil || *r.Data != (cfSRV{Priority: 0, Weight: 0, Port: 25565, Target: "dave.example.net"}) || r.Comment != "playkeeper-names erin" || r.TTL != 60 {
				t.Errorf("SRV record: %+v", r)
			}
		case seedApexA:
			if r.Content != "5.75.160.21" || r.Proxied == nil || !*r.Proxied || r.Comment != "" {
				t.Errorf("proxied apex record: %+v", r)
			}
		case seedSPF:
			if !strings.HasPrefix(r.Content, `"v=spf1 `) || txtValue(r.Content) == r.Content {
				t.Errorf("TXT record: %+v", r)
			}
		}
	}
	if recs, err := c.under(ctx, "names.playkeeper.io"); err != nil || len(recs) != 3 {
		t.Errorf("records under names: %+v, %v", recs, err)
	}
}

func TestCloudflareClientDecodesTheDocumentedAnswers(t *testing.T) {
	c, _ := cannedClient(t, answer(http.StatusOK, string(readFixture(t, "usage.json"))))
	if u, err := c.usage(context.Background()); err != nil || u.Quota == nil || *u.Quota != 200 || u.Usage != 18 {
		t.Errorf("usage: %+v, %v", u, err)
	}
	c, _ = cannedClient(t, answer(http.StatusOK, string(readFixture(t, "delete.json"))))
	if err := c.delete(context.Background(), seedApexA); err != nil {
		t.Errorf("delete: %v", err)
	}
}

func TestCloudflareListingsAreMergedWithoutDuplicates(t *testing.T) {
	var queries []string
	c, _ := cannedClient(t, func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		answer(http.StatusOK, `{"result":[{"id":"a1","type":"A","name":"alice.playkeeper.io","content":"5.75.160.99","ttl":60,"proxied":false,"comment":"playkeeper-names alice"}],
			"result_info":{"page":1,"per_page":100,"count":1,"total_count":1,"total_pages":1},"success":true,"errors":[],"messages":[]}`)(w, r)
	})
	recs, err := c.under(context.Background(), "alice.playkeeper.io")
	if err != nil || len(recs) != 1 {
		t.Fatalf("records: %+v, %v", recs, err)
	}
	want := []string{"name.exact=alice.playkeeper.io&page=1&per_page=100", "name.endswith=.alice.playkeeper.io&page=1&per_page=100"}
	if len(queries) != 2 || queries[0] != want[0] || queries[1] != want[1] {
		t.Errorf("queries %q, want %q", queries, want)
	}
}

func TestCloudflareListingStopsAfterMaxPages(t *testing.T) {
	n := 0
	c, _ := cannedClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		answer(http.StatusOK, fmt.Sprintf(`{"result":[{"id":"r%d","type":"TXT","name":"alice.playkeeper.io","content":"\"x\"","ttl":1}],
			"result_info":{"page":%d,"per_page":1,"count":1,"total_count":100000,"total_pages":100000},"success":true,"errors":[],"messages":[]}`, n, n))(w, r)
	})
	_, err := c.list(context.Background(), url.Values{"name.exact": {"alice.playkeeper.io"}})
	if err == nil || !strings.Contains(err.Error(), "more records under one name") || n != maxPages {
		t.Errorf("after %d requests: %v", n, err)
	}
}

func TestCloudflareErrorsAreToldApart(t *testing.T) {
	html502 := "<html>\r\n<head><title>502 Bad Gateway</title></head>\r\n<body>\r\n<center><h1>502 Bad Gateway</h1></center>\r\n<hr><center>cloudflare</center>\r\n</body>\r\n</html>\r\n"
	for _, tc := range []struct {
		name            string
		status          int
		body            string
		auth, temporary bool
		text            string
	}{
		{"unknown token", 400, string(readFixture(t, "error_auth.json")), true, false,
			"Cloudflare answered HTTP 400 to GET /zones/" + testZoneID + ": 9106 Authentication failed (status: 400)"},
		{"token without the permission", 403, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}],"messages":[],"result":null}`, true, false,
			"10000 Authentication error"},
		{"unknown zone", 404, string(readFixture(t, "error_bad_route.json")), false, false, "7003 Could not route to"},
		{"rate limit", 429, string(readFixture(t, "error_rate_limited.json")), false, true, "971 Please wait"},
		{"internal error", 500, `{"success":false,"errors":[{"code":7001,"message":"Internal Error"}],"messages":[],"result":null}`, false, true, "7001 Internal Error"},
		{"gateway page", 502, html502, false, true, "HTTP 502 to GET /zones/" + testZoneID + ": the answer is not Cloudflare's JSON"},
		{"validation", 400, string(readFixture(t, "error_validation.json")), false, false,
			"1004 DNS Validation Error (9005 Content for A record must be a valid IPv4 address.)"},
		{"no success with HTTP 200", 200, `{"success":false,"errors":[],"messages":[],"result":null}`, false, false, "HTTP 200 to GET /zones/" + testZoneID + ": no error details"},
		{"HTTP 200 that is not JSON", 200, "<!doctype html><title>Captive portal</title>", false, false, "the answer is not Cloudflare's JSON"},
		{"a result of another shape", 200, `{"success":true,"errors":[],"messages":[],"result":["not","a","zone"]}`, false, false, "unexpected result"},
	} {
		c, _ := cannedClient(t, answer(tc.status, tc.body))
		_, err := c.getZone(context.Background())
		ce := asCFError(t, err)
		if ce.auth() != tc.auth || ce.temporary() != tc.temporary || !strings.Contains(ce.Error(), tc.text) {
			t.Errorf("%s: auth %v, temporary %v, %q; want %v, %v, %q", tc.name, ce.auth(), ce.temporary(), ce.Error(), tc.auth, tc.temporary, tc.text)
		}
	}

	c, _ := cannedClient(t, answer(200, `{}`))
	c.api = "http://127.0.0.1:1/client/v4"
	_, err := c.getZone(context.Background())
	if ce := asCFError(t, err); !ce.temporary() || ce.auth() || !strings.HasPrefix(ce.Error(), "could not reach Cloudflare for GET /zones/"+testZoneID+": ") {
		t.Errorf("unreachable: %v", err)
	}
}

func TestCloudflareWritesTolerateOnlyHarmlessRefusals(t *testing.T) {
	ctx := context.Background()
	rec := cfRecord{Type: "A", Name: aliceFQD, Content: aliceV4, Comment: marker("alice"), TTL: recordTTL}
	fixture := func(name string, status int) http.HandlerFunc { return answer(status, string(readFixture(t, name))) }
	for _, tc := range []struct {
		name string
		h    http.HandlerFunc
		do   func(c *cloudflare) error
		ok   bool
	}{
		{"creating an identical record", fixture("error_identical_record.json", 400), func(c *cloudflare) error { return c.create(ctx, rec) }, true},
		{"deleting a record that is gone", fixture("error_record_missing.json", 404), func(c *cloudflare) error { return c.delete(ctx, "gone") }, true},
		{"updating a record that is gone", fixture("error_record_missing.json", 404), func(c *cloudflare) error { return c.update(ctx, "gone", rec) }, false},
		{"creating next to a CNAME", fixture("error_cname_conflict.json", 400), func(c *cloudflare) error { return c.create(ctx, rec) }, false},
		{"creating in a full zone", fixture("error_quota.json", 400), func(c *cloudflare) error { return c.create(ctx, rec) }, false},
		{"creating an invalid record", fixture("error_validation.json", 400), func(c *cloudflare) error { return c.create(ctx, rec) }, false},
		{"deleting with a refused token", fixture("error_auth.json", 400), func(c *cloudflare) error { return c.delete(ctx, "x") }, false},
	} {
		c, _ := cannedClient(t, tc.h)
		err := tc.do(c)
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.name, err)
		}
		if err != nil && asCFError(t, err).temporary() {
			t.Errorf("%s: %v counts as temporary", tc.name, err)
		}
	}
}

func TestCloudflareRequestsHaveTheDocumentedShape(t *testing.T) {
	type seen struct {
		method, path, auth, contentType, body string
	}
	var got []seen
	c, _ := cannedClient(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, seen{r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(b)})
		answer(http.StatusOK, `{"success":true,"errors":[],"messages":[],"result":{"id":"x"}}`)(w, r)
	})
	ctx := context.Background()
	off := false
	a := cfRecord{Type: "A", Name: aliceFQD, Content: aliceV4, TTL: recordTTL, Proxied: &off, Comment: marker("alice")}
	srv := cfRecord{Type: "SRV", Name: "_minecraft._tcp." + aliceFQD, Data: &cfSRV{Port: 25565, Target: aliceFQD}, TTL: recordTTL, Comment: marker("alice")}
	for _, err := range []error{c.create(ctx, a), c.create(ctx, srv), c.update(ctx, "0123abcd", a), c.delete(ctx, "0123abcd"), c.delete(ctx, "../../"+testZoneID)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	records := "/client/v4/zones/" + testZoneID + "/dns_records"
	want := []seen{
		{"POST", records, "Bearer " + testToken, "application/json",
			`{"type":"A","name":"alice.playkeeper.io","content":"5.75.160.99","ttl":60,"proxied":false,"comment":"playkeeper-names alice"}`},
		{"POST", records, "Bearer " + testToken, "application/json",
			`{"type":"SRV","name":"_minecraft._tcp.alice.playkeeper.io","data":{"priority":0,"weight":0,"port":25565,"target":"alice.playkeeper.io"},"ttl":60,"comment":"playkeeper-names alice"}`},
		{"PATCH", records + "/0123abcd", "Bearer " + testToken, "application/json",
			`{"type":"A","name":"alice.playkeeper.io","content":"5.75.160.99","ttl":60,"proxied":false,"comment":"playkeeper-names alice"}`},
		{"DELETE", records + "/0123abcd", "Bearer " + testToken, "application/json", ""},
		{"DELETE", records + "/..%2F..%2F" + testZoneID, "Bearer " + testToken, "application/json", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("%d requests, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d:\n got %+v\nwant %+v", i+1, got[i], want[i])
		}
	}
}

func TestCloudflarePausesAfterItsRateLimit(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		retryAfter string
		want       time.Duration
	}{
		{"30", 30 * time.Second},
		{"", 5 * time.Minute},
		{"0", 5 * time.Minute},
		{"soon", 5 * time.Minute},
		{"Fri, 25 Sep 2026 12:01:00 GMT", 5 * time.Minute},
		{"86400", 5 * time.Minute},
	} {
		n := 0
		limited := answer(http.StatusTooManyRequests, string(readFixture(t, "error_rate_limited.json")), "Retry-After", tc.retryAfter)
		c, clk := cannedClient(t, func(w http.ResponseWriter, r *http.Request) {
			n++
			if n == 1 {
				limited(w, r)
				return
			}
			answer(http.StatusOK, string(readFixture(t, "zone.json")))(w, r)
		})
		_, err := c.getZone(ctx)
		if ce := asCFError(t, err); ce.RetryAfter != tc.want || !ce.temporary() {
			t.Errorf("Retry-After %q: waits %v, want %v", tc.retryAfter, ce.RetryAfter, tc.want)
		}
		clk.Add(tc.want - time.Second)
		_, err = c.getZone(ctx)
		if ce := asCFError(t, err); ce.RetryAfter != time.Second || n != 1 {
			t.Errorf("Retry-After %q: during the pause, waits %v after %d requests", tc.retryAfter, ce.RetryAfter, n)
		}
		clk.Add(time.Second)
		if _, err := c.getZone(ctx); err != nil || n != 2 {
			t.Errorf("Retry-After %q: after the pause: %v after %d requests", tc.retryAfter, err, n)
		}
	}
}

func TestCloudflareErrorsNeverShowTheToken(t *testing.T) {
	echo := func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		b, _ := json.Marshal(map[string]any{
			"success": false, "result": nil, "messages": []any{},
			"errors": []any{map[string]any{"code": 9109, "message": "Invalid access token " + token,
				"error_chain": []any{map[string]any{"code": 9109, "message": "token " + token + " has expired"}}}},
		})
		answer(http.StatusForbidden, string(b))(w, r)
	}
	c, _ := cannedClient(t, echo)
	_, err := c.getZone(context.Background())
	if err == nil || strings.Contains(err.Error(), testToken) || strings.Count(err.Error(), "[token]") != 2 {
		t.Errorf("an error that echoes the token: %v", err)
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", err, err), testToken) {
		t.Error("the token is in the error's fields")
	}
}

func TestCloudflareRefusesAnswersLargerThanExpected(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"announced": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", fmt.Sprint(maxCloudflareBody+1))
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"success":true}`)
		},
		"streamed": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"success":true,"result":"`)
			chunk := strings.Repeat("a", 64<<10)
			for written := 0; written <= maxCloudflareBody; written += len(chunk) {
				if _, err := io.WriteString(w, chunk); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		},
	} {
		c, _ := cannedClient(t, h)
		_, err := c.getZone(context.Background())
		if err == nil || !strings.Contains(err.Error(), "answer larger than expected") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTheServiceFollowsNoRedirects(t *testing.T) {
	elsewhere := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere++ }))
	defer other.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer api.Close()
	cfg := Config{Base: testBase, CloudflareToken: testToken, CloudflareZone: testZoneID, DataDir: t.TempDir(),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), cloudflareAPI: api.URL + "/client/v4"}
	_, err := New(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") || !strings.Contains(err.Error(), EnvZone) {
		t.Errorf("a redirecting API: %v", err)
	}
	if elsewhere != 0 {
		t.Errorf("the redirect was followed %d times", elsewhere)
	}
}
