package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/names"
)

const hookPath = "/api/webhooks/1234/hook-secret-part"

// exhaustClaims tries n new names from n networks, with a daily budget of
// one name, which alerts the owner; round keeps names and networks apart
// between calls.
func exhaustClaims(t *testing.T, e *testEnv, round, n int) {
	t.Helper()
	for i := range n {
		name := fmt.Sprintf("budget-%d-%d", round, i)
		_, err := e.install(name, newMachine(fmt.Sprintf("5.75.%d.1", 180+10*round+i), "")).Claim(context.Background(), name)
		if i > 0 && codeOf(err) != names.CodeRateLimited {
			t.Fatalf("claim %d with a budget of one: %v", i+1, err)
		}
	}
	e.svc.alerts.wait()
}

func TestAlertsReachTheWebhookAtMostOncePerKindEverySixHours(t *testing.T) {
	var mu sync.Mutex
	var got []string
	hook := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if r.Method != http.MethodPost || r.URL.Path != hookPath || r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Errorf("the webhook got %s %s (%s)", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		mu.Lock()
		got = append(got, body["content"])
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hook.Close()
	e := newEnv(t, func(e *testEnv) {
		e.cfg.ClaimsPerDay = 1
		e.cfg.AlertWebhook = hook.URL + hookPath
		e.cfg.alertTransport = hook.Client().Transport
	})
	exhaustClaims(t, e, 0, 3)
	mu.Lock()
	if len(got) != 1 || !strings.HasPrefix(got[0], "playkeeper-names for playkeeper.io: New names are refused for now") || !strings.Contains(got[0], EnvClaimsPerDay) {
		t.Errorf("webhook messages after the budget ran out: %q", got)
	}
	mu.Unlock()
	e.clk.Add(alertEvery - 1)
	exhaustClaims(t, e, 1, 2)
	e.clk.Add(1)
	exhaustClaims(t, e, 2, 2)
	mu.Lock()
	if len(got) != 2 {
		t.Errorf("%d webhook messages over six hours, want 2: %q", len(got), got)
	}
	mu.Unlock()
	if n := strings.Count(e.log.String(), "alert=claims_budget"); n != 2 {
		t.Errorf("logged the alert %d times, want 2", n)
	}
	if strings.Contains(e.log.String(), "hook-secret-part") {
		t.Error("the log shows the webhook URL")
	}
}

func TestAlertWebhookFailuresAreLoggedWithoutItsURL(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	redirecting := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirecting.Close()
	down := httptest.NewTLSServer(http.NotFoundHandler())
	downURL, downTransport := down.URL, down.Client().Transport
	down.Close()
	for _, tc := range []struct {
		name, url string
		transport http.RoundTripper
		want      []string
	}{
		{"a redirect", redirecting.URL + hookPath, redirecting.Client().Transport, []string{"refused an alert", "status=307"}},
		{"no answer", downURL + hookPath, downTransport, []string{"Could not send an alert to " + EnvAlertWebhook, "error="}},
	} {
		e := newEnv(t, func(e *testEnv) {
			e.cfg.ClaimsPerDay = 1
			e.cfg.AlertWebhook = tc.url
			e.cfg.alertTransport = tc.transport
		})
		exhaustClaims(t, e, 0, 2)
		log := e.log.String()
		for _, want := range tc.want {
			if !strings.Contains(log, want) {
				t.Errorf("%s: the log does not say %q:\n%s", tc.name, want, log)
			}
		}
		if strings.Contains(log, "hook-secret-part") || strings.Contains(log, "/api/webhooks") {
			t.Errorf("%s: the log shows the webhook URL:\n%s", tc.name, log)
		}
	}
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("the webhook's redirect was followed %d times", n)
	}
}

func TestNewRefusesAWebhookThatIsNotHTTPS(t *testing.T) {
	cf := newFakeCloudflare(t)
	cfg := testConfig(t, &testClock{t: testStart}, cf, &syncBuffer{})
	cfg.AlertWebhook = "http://discord.com" + hookPath
	if _, err := New(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), EnvAlertWebhook) || strings.Contains(err.Error(), "hook-secret-part") {
		t.Errorf("New with a plain http webhook: %v", err)
	}
}
