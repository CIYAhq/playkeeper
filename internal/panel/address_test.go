package panel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func (e *env) replyStatus(method, path string, status int, body string) {
	e.agent.mu.Lock()
	e.agent.replies[method+" "+path] = body
	e.agent.statuses[method+" "+path] = status
	e.agent.mu.Unlock()
}

func (e *env) agentRequest(t *testing.T, method, path string) agentRequest {
	t.Helper()
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	for i := len(e.agent.reqs) - 1; i >= 0; i-- {
		if r := e.agent.reqs[i]; r.method == method && r.path == path {
			return r
		}
	}
	t.Fatalf("%s %s never reached the agent: %v", method, path, e.agent.hits)
	return agentRequest{}
}

func (e *env) machineID(t *testing.T, cookie string) string {
	t.Helper()
	var machines []map[string]any
	if r := e.get(t, "/api/machines", cookie, &machines); r != 200 || len(machines) != 1 {
		t.Fatalf("machines: %d %v", r, machines)
	}
	return machines[0]["id"].(string)
}

func TestAddressRoutesCarryTheDashboardHost(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	mid := e.machineID(t, cookie)
	host := strings.TrimPrefix(e.ts.URL, "https://")
	base := "/api/machines/" + mid + "/address"

	if r := e.do(t, "GET", base+"?panelHost=198.51.100.9", "", auth(cookie, "")); r.status != 200 {
		t.Fatalf("address: %d %v", r.status, r.body)
	}
	if got := e.agentRequest(t, "GET", "/v1/address").query.Get("panelHost"); got != host {
		t.Fatalf("the agent got panelHost %q, want the dashboard's own host %q", got, host)
	}
	e.do(t, "GET", base+"/plan?domain=mc.example.com", "", auth(cookie, ""))
	if q := e.agentRequest(t, "GET", "/v1/address/plan").query; q.Get("domain") != "mc.example.com" || q.Get("panelHost") != host {
		t.Fatalf("plan query: %v", q)
	}
	e.do(t, "GET", base+"/available?name=alex", "", auth(cookie, ""))
	if q := e.agentRequest(t, "GET", "/v1/address/available").query; q.Get("name") != "alex" || q.Has("panelHost") {
		t.Fatalf("availability query: %v", q)
	}

	if r := e.do(t, "POST", base+"/claim", `{"name":"alex","acceptTerms":true,"panelHost":"198.51.100.9","actor":"eve"}`, auth(cookie, csrf)); r.status != 200 {
		t.Fatalf("claim: %d %v", r.status, r.body)
	}
	b := e.agentRequest(t, "POST", "/v1/address/claim").body
	if b["name"] != "alex" || b["acceptTerms"] != true || b["panelHost"] != host || b["actor"] != "admin" {
		t.Fatalf("claim body: %v", b)
	}
	for _, p := range []string{"refresh", "release", "check", "certificate"} {
		if r := e.do(t, "POST", base+"/"+p, `{}`, auth(cookie, csrf)); r.status != 200 {
			t.Fatalf("%s: %d %v", p, r.status, r.body)
		}
		if b := e.agentRequest(t, "POST", "/v1/address/"+p).body; b["panelHost"] != host || b["actor"] != "admin" {
			t.Fatalf("%s body: %v", p, b)
		}
	}
	if r := e.do(t, "DELETE", base, "", auth(cookie, csrf)); r.status != 200 {
		t.Fatalf("stop using the domain: %d %v", r.status, r.body)
	}
	if q := e.agentRequest(t, "DELETE", "/v1/address").query; q.Get("actor") != "admin" {
		t.Fatalf("delete query: %v", q)
	}
}

func TestAddressRefusalsReachTheDashboardWithTheirParams(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	base := "/api/machines/" + e.machineID(t, cookie) + "/address"
	e.replyStatus("GET", "/v1/address/available", http.StatusServiceUnavailable,
		`{"error":"The free address service can't be reached.","code":"names_unreachable","params":{"detail":"connection refused"}}`)
	r := e.do(t, "GET", base+"/available?name=alex", "", auth(cookie, ""))
	if p, _ := r.body["params"].(map[string]any); r.status != http.StatusServiceUnavailable || r.body["code"] != "names_unreachable" || p["detail"] != "connection refused" {
		t.Fatalf("unreachable: %d %v", r.status, r.body)
	}
	e.replyStatus("POST", "/v1/address/claim", http.StatusTooManyRequests,
		`{"error":"Too many requests.","code":"rate_limited","params":{"retryAfterSeconds":120}}`)
	r = e.do(t, "POST", base+"/claim", `{"name":"alex"}`, auth(cookie, csrf))
	if p, _ := r.body["params"].(map[string]any); r.status != http.StatusTooManyRequests || p["retryAfterSeconds"] != float64(120) {
		t.Fatalf("rate limited: %d %v", r.status, r.body)
	}
	if r.cookie != "" || r.status == http.StatusUnauthorized {
		t.Fatalf("a refusal from the names service touched the session: %+v", r)
	}
}

func TestMembersCanSeeTheAddressButNotChangeIt(t *testing.T) {
	e := newEnv(t)
	cookie, _ := e.setup(t)
	mid := e.machineID(t, cookie)
	h, _ := hashPassword("member password 1")
	if _, err := e.srv.db.Exec(`INSERT INTO users(username, password_hash, created_at, password_changed_at, role) VALUES('friend', ?, 0, 0, 'member')`, h); err != nil {
		t.Fatal(err)
	}
	r := e.do(t, "POST", "/api/auth/login", `{"username":"friend","password":"member password 1"}`, map[string]string{"X-Requested-With": "playkeeper"})
	if r.status != 200 {
		t.Fatalf("member login: %d %v", r.status, r.body)
	}
	mc, mcsrf := r.cookie, r.body["csrfToken"].(string)
	base := "/api/machines/" + mid + "/address"
	if r := e.do(t, "GET", base, "", auth(mc, "")); r.status != 200 {
		t.Fatalf("a member may look: %d %v", r.status, r.body)
	}
	for _, p := range []string{"/claim", "/refresh", "/release", "/check", "/certificate"} {
		if r := e.do(t, "POST", base+p, `{}`, auth(mc, mcsrf)); r.status != http.StatusForbidden {
			t.Errorf("a member may not use %s: %d", p, r.status)
		}
	}
	if r := e.do(t, "DELETE", base, "", auth(mc, mcsrf)); r.status != http.StatusForbidden {
		t.Errorf("a member may not remove the domain: %d", r.status)
	}
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	for _, h := range e.agent.hits {
		if h != "GET /v1/address" && h != "GET /v1/machine" {
			t.Fatalf("a refused change reached the agent: %v", e.agent.hits)
		}
	}
}

// writeBundle saves a certificate for name the way the agent does: the
// chain, then the key, in <dir>/<name>.pem.
func writeBundle(t *testing.T, dir, name string, notBefore, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: notBefore, NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	b := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})...)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".pem"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTheDashboardServesTheAddressCertificateByName(t *testing.T) {
	e := newEnv(t)
	now := e.clock.now()
	writeBundle(t, e.cfg.CertsDir(), "alex.playkeeper.io", now.Add(-time.Hour), now.Add(90*24*time.Hour))
	tc, err := e.srv.tlsConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(tc.Certificates) != 0 || tc.MinVersion != tls.VersionTLS12 {
		t.Fatalf("the config must pick certificates per name: %+v", tc)
	}
	leaf := func(hello *tls.ClientHelloInfo) *x509.Certificate {
		t.Helper()
		c, err := tc.GetCertificate(hello)
		if err != nil {
			t.Fatal(err)
		}
		l, err := x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	if l := leaf(&tls.ClientHelloInfo{ServerName: "alex.playkeeper.io"}); !slices.Contains(l.DNSNames, "alex.playkeeper.io") {
		t.Fatalf("the address got %v", l.Subject)
	}
	selfSigned := func(l *x509.Certificate) bool { return slices.Contains(l.Subject.Organization, "Playkeeper self-signed") }
	for _, hello := range []*tls.ClientHelloInfo{{}, {ServerName: "localhost"}, {ServerName: "other.example.com"}} {
		if l := leaf(hello); !selfSigned(l) {
			t.Fatalf("%q got %v, want the self-signed certificate", hello.ServerName, l.Subject)
		}
	}
	e.clock.add(91 * 24 * time.Hour)
	if l := leaf(&tls.ClientHelloInfo{ServerName: "alex.playkeeper.io"}); !selfSigned(l) {
		t.Fatalf("a lapsed certificate is still served: %v", l.Subject)
	}
}

func TestHSTSIsShortOnNamesAndLongOnIPAddresses(t *testing.T) {
	e := newEnv(t)
	for host, want := range map[string]string{
		"":                        "max-age=31536000",
		"203.0.113.10:8443":       "max-age=31536000",
		"[2001:db8::1]:8443":      "max-age=31536000",
		"alex.playkeeper.io:8443": "max-age=86400",
		"mc.example.com":          "max-age=86400",
	} {
		req, _ := http.NewRequest("GET", e.ts.URL+"/healthz", nil)
		if host != "" {
			req.Host = host
		}
		r, err := e.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if got := r.Header.Get("Strict-Transport-Security"); got != want {
			t.Errorf("host %q: Strict-Transport-Security %q, want %q", host, got, want)
		}
	}
}
