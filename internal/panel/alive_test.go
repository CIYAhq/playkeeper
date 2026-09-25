package panel

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/names"
)

const aliveNonce = "dGVzdC1ub25jZS0yMi1jaGFycw"

// askAlive sends the names service's liveness check to base with host as
// the Host header, as the service does, without signing in.
func askAlive(t *testing.T, c *http.Client, base, path, host string) (int, http.Header, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", base+path, nil)
	req.Host = host
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, r.Header, string(b)
}

func TestLivenessCheckIsPassedToTheAgentWithoutSignIn(t *testing.T) {
	e := newEnv(t)
	signed := `{"name":"alex","signature":"c2lnbmVk"}`
	e.agent.mu.Lock()
	e.agent.replies["GET /v1/address/alive/"+aliveNonce] = signed
	e.agent.replies["GET /v1/address/alive/"+aliveNonce+"x"] = `{"code":"no_name","error":"This dashboard does not hold that name."}`
	e.agent.statuses["GET /v1/address/alive/"+aliveNonce+"x"] = http.StatusNotFound
	e.agent.mu.Unlock()

	code, h, body := askAlive(t, e.ts.Client(), e.ts.URL, names.AlivePath+aliveNonce, "alex.playkeeper.io:8443")
	if code != http.StatusOK || body != signed || h.Get("Content-Type") != "application/json" || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("held name: %d %q %v", code, body, h)
	}
	req := e.agentRequest(t, "GET", "/v1/address/alive/"+aliveNonce)
	if req.query.Get("host") != "alex.playkeeper.io:8443" {
		t.Fatalf("the agent was not told the Host: %v", req.query)
	}
	if code, _, body := askAlive(t, e.ts.Client(), e.ts.URL, names.AlivePath+aliveNonce+"x", "steve.playkeeper.io:8443"); code != http.StatusNotFound || body == signed {
		t.Fatalf("name held elsewhere: %d %q", code, body)
	}

	// On its own listener, for panels on another port, it is all there is.
	only := httptest.NewTLSServer(e.srv.aliveHandler())
	defer only.Close()
	if code, _, body := askAlive(t, only.Client(), only.URL, names.AlivePath+aliveNonce, "alex.playkeeper.io:8443"); code != http.StatusOK || body != signed {
		t.Fatalf("alive listener: %d %q", code, body)
	}
	for _, p := range []string{"/api/health", "/api/setup/status", "/", "/setup"} {
		if code, _, _ := askAlive(t, only.Client(), only.URL, p, "alex.playkeeper.io:8443"); code != http.StatusNotFound {
			t.Errorf("alive listener serves %s: %d", p, code)
		}
	}
}

func TestLivenessChecksAreLimitedPerAddress(t *testing.T) {
	e := newEnv(t)
	path := names.AlivePath + aliveNonce
	for i := range 20 {
		if code, _, _ := askAlive(t, e.ts.Client(), e.ts.URL, path, "alex.playkeeper.io:8443"); code != http.StatusOK {
			t.Fatalf("check %d: %d", i+1, code)
		}
	}
	code, h, _ := askAlive(t, e.ts.Client(), e.ts.URL, path, "alex.playkeeper.io:8443")
	if code != http.StatusTooManyRequests || h.Get("Retry-After") == "" {
		t.Fatalf("check past the limit: %d %v", code, h)
	}
	e.clock.add(time.Minute)
	if code, _, _ := askAlive(t, e.ts.Client(), e.ts.URL, path, "alex.playkeeper.io:8443"); code != http.StatusOK {
		t.Fatalf("a minute later: %d", code)
	}

	e.srv.agent = agentclient.New(filepath.Join(t.TempDir(), "gone.sock"))
	if code, _, _ := askAlive(t, e.ts.Client(), e.ts.URL, path, "alex.playkeeper.io:8443"); code != http.StatusServiceUnavailable {
		t.Fatalf("agent down: %d", code)
	}
}
