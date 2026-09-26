package panel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
	for _, p := range []string{names.AlivePath, names.AlivePath + aliveNonce + "/more"} {
		if code, _, _ := askAlive(t, e.ts.Client(), e.ts.URL, p, "alex.playkeeper.io:8443"); code != http.StatusNotFound {
			t.Errorf("%s: %d", p, code)
		}
	}
	post, _ := http.NewRequest("POST", e.ts.URL+names.AlivePath+aliveNonce, nil)
	post.Host = "alex.playkeeper.io:8443"
	if r, err := e.ts.Client().Do(post); err != nil || r.StatusCode != http.StatusNotFound {
		t.Fatalf("POST: %v %v", r, err)
	} else {
		r.Body.Close()
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
	only := httptest.NewTLSServer(e.srv.aliveHandler())
	defer only.Close()
	if code, _, _ := askAlive(t, only.Client(), only.URL, path, "alex.playkeeper.io:8443"); code != http.StatusTooManyRequests {
		t.Fatalf("the port-8443 listener past the limit: %d", code)
	}
	e.clock.add(time.Minute)
	if code, _, _ := askAlive(t, e.ts.Client(), e.ts.URL, path, "alex.playkeeper.io:8443"); code != http.StatusOK {
		t.Fatalf("a minute later: %d", code)
	}

	e.srv.agent = agentclient.New(filepath.Join(t.TempDir(), "gone.sock"))
	if code, h, _ := askAlive(t, e.ts.Client(), e.ts.URL, path, "alex.playkeeper.io:8443"); code != http.StatusNotFound || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("agent down: %d %v", code, h)
	}
}

// The names service asks as internal/names/service does: HTTPS to the
// panel's port with the name as Host, any certificate and no redirects. The
// answer must reach it intact, so the signature verifies with the name's key.
func TestTheNamesServiceGetsItsSignedAnswerOnThePanelsPort(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	e.agent.mu.Lock()
	e.agent.replies["GET /v1/address/alive/"+aliveNonce] = fmt.Sprintf(`{"name":"alex","signature":%q}`, names.SignAlive(key, names.DefaultBase, "alex", aliveNonce))
	e.agent.mu.Unlock()
	tc, err := e.srv.tlsConfig()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.srv.serve(ctx, ln, tc) }()
	defer func() {
		cancel()
		<-done
	}()

	c := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{DisableKeepAlives: true,
			TLSClientConfig: &tls.Config{ServerName: "alex.playkeeper.io", InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}},
	}
	code, _, body := askAlive(t, c, "https://"+ln.Addr().String(), names.AlivePath+aliveNonce, "alex.playkeeper.io:8443")
	var a names.Alive
	if code != http.StatusOK || json.Unmarshal([]byte(body), &a) != nil || a.Name != "alex" || !names.VerifyAlive(pub, names.DefaultBase, "alex", aliveNonce, a.Signature) {
		t.Fatalf("the names service would not accept the answer: %d %q", code, body)
	}
}
