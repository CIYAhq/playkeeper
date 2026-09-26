package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/certs"
	"github.com/CIYAhq/playkeeper/internal/names"
)

// testIP is the machine's public address in these tests, from a
// documentation range.
var testIP = netip.MustParseAddr("203.0.113.10")

// noCA stands in for Let's Encrypt in tests that expect no certificate.
func noCA(context.Context, *certs.Issuer, certs.Request) (*certs.Certificate, error) {
	return nil, errors.New("no certificate authority in these tests")
}

// fakeResolver answers lookups from its records, as public DNS would.
type fakeResolver struct {
	mu  sync.Mutex
	a   map[string][]netip.Addr
	srv map[string][]*net.SRV
}

func (f *fakeResolver) set(name string, addrs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.a == nil {
		f.a = map[string][]netip.Addr{}
	}
	f.a[name] = nil
	for _, s := range addrs {
		f.a[name] = append(f.a[name], netip.MustParseAddr(s))
	}
}

func (f *fakeResolver) setSRV(host string, port int, target string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.srv == nil {
		f.srv = map[string][]*net.SRV{}
	}
	f.srv[host] = []*net.SRV{{Target: target + ".", Port: uint16(port), Priority: 0, Weight: 5}}
}

func notFound(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f *fakeResolver) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []netip.Addr
	for _, a := range f.a[strings.TrimSuffix(host, ".")] {
		if a.Is4() == (network == "ip4") {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil, notFound(host)
	}
	return out, nil
}

func (f *fakeResolver) LookupSRV(_ context.Context, _, _, name string) (string, []*net.SRV, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.srv[strings.TrimSuffix(name, ".")]; len(s) > 0 {
		return "", s, nil
	}
	return "", nil, notFound(name)
}

func (f *fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	return nil, notFound(name)
}

// fakeIssuer is the certificate authority: it answers the DNS-01 challenge
// through the request's challenger like Let's Encrypt would, and saves a
// file where the panel would read it.
type fakeIssuer struct {
	mu   sync.Mutex
	reqs []certs.Request
	// plan says, for the nth attempt (from 1), whether the certificate is
	// due for renewal at once and what error to return instead.
	plan func(n int) (renewNow bool, err error)
}

func (f *fakeIssuer) issue(ctx context.Context, _ *certs.Issuer, req certs.Request) (*certs.Certificate, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	n, plan := len(f.reqs), f.plan
	f.mu.Unlock()
	renewNow, err := false, error(nil)
	if plan != nil {
		renewNow, err = plan(n)
	}
	if err != nil {
		return nil, err
	}
	if req.DNS01 != nil {
		fqdn, value := "_acme-challenge."+req.Names[0], strings.Repeat("v", 43)
		if err := req.DNS01.Challenger.SetTXT(ctx, fqdn, value); err != nil {
			return nil, err
		}
		if err := req.DNS01.Challenger.ClearTXT(ctx, fqdn, value); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(req.Dir, 0o700); err != nil {
		return nil, err
	}
	file := filepath.Join(req.Dir, req.Names[0]+".pem")
	if err := os.WriteFile(file, []byte("test certificate\n"), 0o600); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	c := &certs.Certificate{Names: req.Names, File: file, NotBefore: now, NotAfter: now.Add(90 * 24 * time.Hour), Issuer: "Test CA", Serial: "01", SHA256: "ab"}
	c.RenewAt = certs.RenewAt(c.NotBefore, c.NotAfter)
	if renewNow {
		c.RenewAt = now.Add(-time.Minute)
	}
	return c, nil
}

func (f *fakeIssuer) requests() []certs.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

// fakeNames is the names service: it keeps names by key, checks the
// signature of every signed request, and refuses requests on demand.
type fakeNames struct {
	srv *httptest.Server

	mu     sync.Mutex
	names  map[string]*names.Name
	owner  map[string]string
	calls  []string
	txtSet int
	txtOff int
	// pending keeps new records unpublished until publish.
	pending bool
	// fail, when it returns a refusal, answers a request with it.
	fail func(r *http.Request) *fakeRefusal
}

type fakeRefusal struct {
	status     int
	code, msg  string
	params     map[string]any
	retryAfter string
	// page answers with an HTML error page instead of JSON, like a proxy.
	page bool
}

func startFakeNames(t *testing.T) *fakeNames {
	t.Helper()
	f := &fakeNames{names: map[string]*names.Name{}, owner: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/names/{name}", f.available)
	mux.HandleFunc("PUT /v1/names/{name}", f.signed(f.claim))
	mux.HandleFunc("DELETE /v1/names/{name}", f.signed(f.release))
	mux.HandleFunc("POST /v1/names/{name}/address", f.signed(f.refresh))
	mux.HandleFunc("GET /v1/names", f.signed(f.list))
	mux.HandleFunc("PUT /v1/names/{name}/servers/{label}", f.signed(f.setServer))
	mux.HandleFunc("DELETE /v1/names/{name}/servers/{label}", f.signed(f.removeServer))
	mux.HandleFunc("PUT /v1/names/{name}/acme-challenge/{value}", f.signed(f.setTXT))
	mux.HandleFunc("DELETE /v1/names/{name}/acme-challenge/{value}", f.signed(f.clearTXT))
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		fail := f.fail
		f.mu.Unlock()
		if fail != nil {
			if rf := fail(r); rf != nil {
				if rf.retryAfter != "" {
					w.Header().Set("Retry-After", rf.retryAfter)
				}
				if rf.page {
					w.Header().Set("Content-Type", "text/html")
					w.WriteHeader(rf.status)
					io.WriteString(w, "<html><body>502 Bad Gateway</body></html>")
					return
				}
				jsonOut(w, rf.status, names.ErrorBody{Error: rf.msg, Code: rf.code, Params: rf.params})
				return
			}
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

type signedHandler func(w http.ResponseWriter, r *http.Request, key string, body []byte)

func (f *fakeNames) signed(h signedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		s, err := names.VerifyRequest(r.Header, r.Method, r.URL.RequestURI(), body, names.DefaultBase, time.Now())
		if err != nil {
			var ne *names.Error
			errors.As(err, &ne)
			refuse(w, ne.Status, ne.Code, ne.Params)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		h(w, r, s.EncodedKey(), body)
	}
}

func refuse(w http.ResponseWriter, status int, code string, params map[string]any) {
	jsonOut(w, status, names.ErrorBody{Error: "Refused: " + code + ".", Code: code, Params: params})
}

func (f *fakeNames) dns() string {
	if f.pending {
		return names.DNSPending
	}
	return names.DNSOK
}

// held is whether a released name is still kept from other keys.
func held(n *names.Name) bool {
	return n.State == names.StateReleased && time.Now().Before(n.FreedAt)
}

func (f *fakeNames) available(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.names[name]
	switch {
	case n == nil || n.State == names.StateReleased && !held(n):
		jsonOut(w, http.StatusOK, names.Availability{Name: name, Address: names.Address(name, names.DefaultBase), Available: true})
	case held(n):
		jsonOut(w, http.StatusOK, names.Availability{Name: name, Code: names.CodeNameHeld, Message: "Held.", Params: map[string]any{"until": n.FreedAt.Unix()}})
	default:
		jsonOut(w, http.StatusOK, names.Availability{Name: name, Code: names.CodeNameTaken, Message: "Taken."})
	}
}

func (f *fakeNames) claim(w http.ResponseWriter, r *http.Request, key string, _ []byte) {
	name := r.PathValue("name")
	n := f.names[name]
	switch {
	case n != nil && f.owner[name] == key && n.State == names.StateActive:
		jsonOut(w, http.StatusOK, n)
		return
	case n != nil && f.owner[name] != key && held(n):
		refuse(w, http.StatusConflict, names.CodeNameHeld, map[string]any{"until": n.FreedAt.Unix()})
		return
	case n != nil && f.owner[name] != key && n.State != names.StateReleased:
		refuse(w, http.StatusConflict, names.CodeNameTaken, nil)
		return
	}
	for other, on := range f.names {
		if other != name && f.owner[other] == key && on.State != names.StateReleased {
			refuse(w, http.StatusConflict, names.CodeLimitReached, map[string]any{"max": 1})
			return
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	n = &names.Name{Name: name, Address: names.Address(name, names.DefaultBase), State: names.StateActive, IPv4: testIP.String(),
		ClaimedAt: now, RefreshedAt: now, RefreshBy: now.Add(namesLapseAfter), FreedAt: now.Add(2 * namesLapseAfter), Servers: []names.Server{}, DNS: f.dns()}
	f.names[name], f.owner[name] = n, key
	jsonOut(w, http.StatusOK, n)
}

func (f *fakeNames) owned(w http.ResponseWriter, r *http.Request, key string) *names.Name {
	name := r.PathValue("name")
	n := f.names[name]
	switch {
	case n == nil:
		refuse(w, http.StatusNotFound, names.CodeNotClaimed, nil)
		return nil
	case f.owner[name] != key:
		refuse(w, http.StatusForbidden, names.CodeNotYourName, nil)
		return nil
	}
	return n
}

func (f *fakeNames) release(w http.ResponseWriter, r *http.Request, key string, _ []byte) {
	n := f.owned(w, r, key)
	if n == nil {
		return
	}
	if n.State == names.StateReleased {
		refuse(w, http.StatusNotFound, names.CodeNotClaimed, nil)
		return
	}
	n.State, n.FreedAt, n.RefreshBy, n.Servers, n.IPv4 = names.StateReleased, time.Now().UTC().Add(namesReleaseHold), time.Time{}, []names.Server{}, ""
	jsonOut(w, http.StatusOK, n)
}

func (f *fakeNames) refresh(w http.ResponseWriter, r *http.Request, key string, _ []byte) {
	n := f.owned(w, r, key)
	if n == nil {
		return
	}
	if n.State == names.StateReleased {
		refuse(w, http.StatusNotFound, names.CodeNotClaimed, nil)
		return
	}
	now := time.Now().UTC().Truncate(time.Second)
	n.State, n.IPv4, n.RefreshedAt, n.RefreshBy = names.StateActive, testIP.String(), now, now.Add(namesLapseAfter)
	jsonOut(w, http.StatusOK, n)
}

func (f *fakeNames) list(w http.ResponseWriter, _ *http.Request, key string, _ []byte) {
	l := names.NameList{Names: []names.Name{}}
	for name, n := range f.names {
		if f.owner[name] == key {
			l.Names = append(l.Names, *n)
		}
	}
	jsonOut(w, http.StatusOK, l)
}

func (f *fakeNames) setServer(w http.ResponseWriter, r *http.Request, key string, body []byte) {
	n := f.owned(w, r, key)
	if n == nil {
		return
	}
	var req names.ServerRequest
	if json.Unmarshal(body, &req) != nil {
		refuse(w, http.StatusBadRequest, names.CodeInvalidRequest, nil)
		return
	}
	label := strings.TrimPrefix(r.PathValue("label"), "@")
	sv := names.Server{Label: label, Address: names.ServerAddress(label, n.Name, names.DefaultBase), Port: req.Port, DNS: f.dns()}
	if i := slices.IndexFunc(n.Servers, func(s names.Server) bool { return s.Label == label }); i >= 0 {
		n.Servers[i] = sv
	} else if len(n.Servers) >= namesMaxServers {
		refuse(w, http.StatusConflict, names.CodeTooManyServers, nil)
		return
	} else {
		n.Servers = append(n.Servers, sv)
	}
	jsonOut(w, http.StatusOK, sv)
}

func (f *fakeNames) removeServer(w http.ResponseWriter, r *http.Request, key string, _ []byte) {
	n := f.owned(w, r, key)
	if n == nil {
		return
	}
	label := strings.TrimPrefix(r.PathValue("label"), "@")
	n.Servers = slices.DeleteFunc(n.Servers, func(s names.Server) bool { return s.Label == label })
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeNames) setTXT(w http.ResponseWriter, r *http.Request, key string, _ []byte) {
	n := f.owned(w, r, key)
	if n == nil {
		return
	}
	f.txtSet++
	jsonOut(w, http.StatusOK, names.Challenge{FQDN: names.ChallengeFQDN(n.Name, names.DefaultBase), Value: r.PathValue("value"), ExpiresAt: time.Now().Add(time.Hour), DNS: names.DNSOK})
}

func (f *fakeNames) clearTXT(w http.ResponseWriter, r *http.Request, key string, _ []byte) {
	if f.owned(w, r, key) == nil {
		return
	}
	f.txtOff++
	w.WriteHeader(http.StatusNoContent)
}

// publish makes every record published, like Cloudflare catching up.
func (f *fakeNames) publish() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = false
	for _, n := range f.names {
		n.DNS = names.DNSOK
		for i := range n.Servers {
			n.Servers[i].DNS = names.DNSOK
		}
	}
}

func (f *fakeNames) setPending(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = v
}

func (f *fakeNames) setFail(fn func(r *http.Request) *fakeRefusal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fn
}

func (f *fakeNames) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeNames) txtCounts() (set, cleared int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.txtSet, f.txtOff
}

func (f *fakeNames) forget(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.names, name)
}

func (f *fakeNames) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeNames) name(name string) (names.Name, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n := f.names[name]; n != nil {
		return *n, f.owner[name]
	}
	return names.Name{}, ""
}

// takenBy gives name to another install.
func (f *fakeNames) takenBy(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().UTC()
	f.names[name] = &names.Name{Name: name, State: names.StateActive, ClaimedAt: now, RefreshedAt: now, RefreshBy: now.Add(namesLapseAfter), DNS: names.DNSOK}
	f.owner[name] = "someone-else"
}

func (f *fakeNames) labels(name string) map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	if n := f.names[name]; n != nil {
		for _, s := range n.Servers {
			out[s.Label] = s.Port
		}
	}
	return out
}

// addressEnv is an agent with the fake names service, DNS and certificate
// authority.
type addressEnv struct {
	*agentEnv
	names *fakeNames
	ca    *fakeIssuer
	dns   *fakeResolver
}

func newAddressEnv(t *testing.T, tweak func(o *Options)) *addressEnv {
	t.Helper()
	ae := &addressEnv{names: startFakeNames(t), ca: &fakeIssuer{}, dns: &fakeResolver{}}
	ae.agentEnv = newAgentEnvWith(t, func(e *agentEnv) {
		e.cfg.NamesURL = ae.names.srv.URL
		e.tweak = func(o *Options) {
			o.NamesHTTP, o.Resolver, o.Issue, o.HTTP01Addr = ae.names.srv.Client(), ae.dns, ae.ca.issue, "127.0.0.1:0"
			o.HostMemoryMB = func() int { return 16384 }
			if tweak != nil {
				tweak(o)
			}
		}
	})
	return ae
}

// addServerNamed records a stopped server called name.
func (e *agentEnv) addServerNamed(name string) string {
	e.t.Helper()
	opts, _, _ := e.a.memoryFor("")
	if len(opts) == 0 {
		e.t.Fatal("no memory left for another server")
	}
	sc := api.ServerConfig{Type: api.TypePaper, VersionID: "paper-26.1.2", MinecraftVersion: "26.1.2", PaperBuild: 74, MemoryMB: opts[0], HeapMB: 512, LevelName: "world", MOTD: defaultMOTD, MaxPlayers: 10, Whitelist: true}
	s, _, err := e.a.addServer(newServerSpec{name: name, typ: api.TypePaper, config: sc, desired: api.DesiredStopped}, "", nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return s.id
}

// callInto calls the agent and decodes the answer into out, which it
// clears first.
func (e *agentEnv) callInto(method, path string, body, out any) int {
	e.t.Helper()
	v := reflect.ValueOf(out).Elem()
	v.Set(reflect.Zero(v.Type()))
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, r)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp.StatusCode
}

func (e *agentEnv) address() api.Address {
	e.t.Helper()
	var v api.Address
	if code := e.callInto("GET", "/v1/address", nil, &v); code != http.StatusOK {
		e.t.Fatalf("address: %d", code)
	}
	return v
}

// claim claims name and waits for the address to be published.
func (e *agentEnv) claim(name string) api.Address {
	e.t.Helper()
	published := func() (n int) {
		for _, a := range e.auditActions() {
			if strings.HasPrefix(a, "address.publish ") {
				n++
			}
		}
		return n
	}
	before := published()
	var v api.Address
	code := e.callInto("POST", "/v1/address/claim", map[string]any{"name": name, "acceptTerms": true, "panelHost": "203.0.113.10:8443", "actor": "admin"}, &v)
	if code != http.StatusOK || v.Kind != api.AddressPlaykeeper || (v.Operation != nil && v.Operation.Kind != "address.publish") {
		e.t.Fatalf("claim %s: %d %+v", name, code, v)
	}
	if v.Operation != nil {
		if op := e.waitOp(v.Operation.ID); op.Status != api.OpSucceeded {
			e.t.Fatalf("publishing %s: %+v", name, op)
		}
		return e.address()
	}
	// A quick publish can be over before the claim answers.
	for deadline := time.Now().Add(10 * time.Second); published() == before; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			e.t.Fatalf("publishing %s never finished", name)
		}
	}
	if !slices.Contains(e.auditActions(), "address.publish succeeded") {
		e.t.Fatalf("publishing %s: %v", name, e.auditActions())
	}
	return e.address()
}

func (e *agentEnv) auditActions() []string {
	e.t.Helper()
	list, err := e.a.listAudit(100)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, a := range list {
		out = append(out, a.Action+" "+a.Result)
	}
	return out
}

func TestFreeAddressClaimPublishesServersAndCertificate(t *testing.T) {
	e := newAddressEnv(t, nil)
	survival := e.addServerNamed("Survival")
	e.addServerNamed("Creative")

	var av api.NameAvailability
	if code := e.callInto("GET", "/v1/address/available?name=Alex.playkeeper.io", nil, &av); code != 200 || !av.Available || av.Name != "alex" || av.Address != "alex.playkeeper.io" {
		t.Fatalf("availability: %d %+v", code, av)
	}
	v := e.claim("alex")
	if v.Kind != api.AddressPlaykeeper || v.Host != "alex.playkeeper.io" || v.Free == nil || v.Free.State != names.StateActive || v.Free.DNS != names.DNSOK || v.Free.HoldDays != 30 {
		t.Fatalf("address after the claim: %+v %+v", v, v.Free)
	}
	if n, owner := e.names.name("alex"); n.State != names.StateActive || owner == "" {
		t.Fatalf("the service has %+v", n)
	}
	if got := e.names.labels("alex"); len(got) != 2 || got["survival"] != 25565 || got["creative"] != 25566 {
		t.Fatalf("server records: %v", got)
	}
	if e.names.count("POST /v1/names/alex/address") != 2 {
		t.Fatalf("the name was not refreshed over both IP versions: %v", e.names.callLog())
	}

	reqs := e.ca.requests()
	if len(reqs) != 1 || reqs[0].DNS01 == nil || reqs[0].HTTP01 != nil || !slices.Equal(reqs[0].Names, []string{"alex.playkeeper.io"}) || reqs[0].Dir != e.cfg.CertsDir() {
		t.Fatalf("certificate requests: %+v", reqs)
	}
	if set, cleared := e.names.txtCounts(); set != 1 || cleared != 1 {
		t.Fatalf("DNS-01 challenge records: set %d, cleared %d", set, cleared)
	}
	if v.Certificate == nil || v.Certificate.NotAfter == nil || v.Certificate.Challenge != "dns-01" || v.Certificate.Problem != nil {
		t.Fatalf("certificate: %+v", v.Certificate)
	}
	if _, err := os.Stat(filepath.Join(e.cfg.CertsDir(), "alex.playkeeper.io.pem")); err != nil {
		t.Fatal(err)
	}

	want := []api.JoinAddress{
		{ServerID: survival, Name: "Survival", Port: 25565, Label: "survival", Address: "survival.alex.playkeeper.io", Direct: "203.0.113.10", Published: true},
		{Name: "Creative", Port: 25566, Label: "creative", Address: "creative.alex.playkeeper.io", Direct: "203.0.113.10:25566", Published: true},
	}
	want[1].ServerID = v.Servers[1].ServerID
	if !slices.Equal(v.Servers, want) {
		t.Fatalf("join addresses:\n got %+v\nwant %+v", v.Servers, want)
	}
	if got := e.a.serverByID(survival).Status(context.Background()).JoinAddress; got != "survival.alex.playkeeper.io" {
		t.Fatalf("server status join address %q", got)
	}
	if v.TermsAccepted == nil {
		t.Fatal("accepting the terms was not recorded")
	}
	audit := e.auditActions()
	for _, want := range []string{"address.claim succeeded", "certificate.terms accepted", "address.publish succeeded"} {
		if !slices.Contains(audit, want) {
			t.Errorf("audit lacks %q: %v", want, audit)
		}
	}
}

func TestFreeAddressPublishingWaitsForTheRecords(t *testing.T) {
	e := newAddressEnv(t, nil)
	id := e.addServerNamed("Survival")
	e.names.setPending(true)
	var v api.Address
	if code := e.callInto("POST", "/v1/address/claim", map[string]any{"name": "alex", "actor": "admin"}, &v); code != 200 {
		t.Fatalf("claim: %d %+v", code, v)
	}
	e.waitFor("the publishing phase", func() bool {
		op := e.a.addressOp()
		return op != nil && op.Phase == "publishing"
	})
	v = e.address()
	if v.Servers[0].Published || e.a.serverByID(id).Status(context.Background()).JoinAddress != "" {
		t.Fatalf("an unpublished address is shown as working: %+v", v.Servers)
	}
	if code, out := e.call("POST", "/v1/address/claim", map[string]any{"name": "bob", "actor": "admin"}); code != 409 || out["code"] != api.CodeBusy {
		t.Fatalf("a claim while publishing: %d %v", code, out)
	}
	e.names.publish()
	if op := e.waitOp(v.Operation.ID); op.Status != api.OpSucceeded {
		t.Fatalf("publishing: %+v", op)
	}
	if got := e.a.serverByID(id).Status(context.Background()).JoinAddress; got != "survival.alex.playkeeper.io" {
		t.Fatalf("join address after publishing: %q", got)
	}
}

func TestFreeAddressChangeAndRelease(t *testing.T) {
	e := newAddressEnv(t, nil)
	e.addServerNamed("Survival")
	e.claim("alex")

	// A name someone else has: the change fails and alex is claimed back.
	e.names.takenBy("taken")
	code, out := e.call("POST", "/v1/address/claim", map[string]any{"name": "taken", "actor": "admin"})
	if code != 409 || out["code"] != names.CodeNameTaken {
		t.Fatalf("claiming a taken name: %d %v", code, out)
	}
	if n, _ := e.names.name("alex"); n.State != names.StateActive {
		t.Fatalf("alex was not claimed back: %+v", n)
	}
	if v := e.address(); v.Host != "alex.playkeeper.io" || v.Free.State != names.StateActive {
		t.Fatalf("address after the failed change: %+v", v)
	}

	// A change this machine can't save is undone at the names service too.
	failSaves := func(fail bool) {
		t.Helper()
		for _, op := range []string{"INSERT", "UPDATE"} {
			stmt := `DROP TRIGGER no_address_` + op
			if fail {
				stmt = `CREATE TRIGGER no_address_` + op + ` BEFORE ` + op + ` ON kv WHEN NEW.key = 'address' BEGIN SELECT RAISE(ABORT, 'disk full'); END`
			}
			if _, err := e.a.db.Exec(stmt); err != nil {
				t.Fatal(err)
			}
		}
	}
	failSaves(true)
	if code, out := e.call("POST", "/v1/address/claim", map[string]any{"name": "bob", "actor": "admin"}); code != 500 {
		t.Fatalf("a change that can't be saved: %d %v", code, out)
	}
	if n, _ := e.names.name("bob"); n.State != names.StateReleased {
		t.Fatalf("bob was kept after the change couldn't be saved: %+v", n)
	}
	if n, _ := e.names.name("alex"); n.State != names.StateActive {
		t.Fatalf("alex was not claimed back after the change couldn't be saved: %+v", n)
	}
	if v := e.address(); v.Host != "alex.playkeeper.io" || v.Free.State != names.StateActive {
		t.Fatalf("address after the change couldn't be saved: %+v", v)
	}
	// When bob can't be released either, alex can't be claimed back, as a
	// key holds one name: the machine keeps alex and its certificate.
	e.names.setFail(func(r *http.Request) *fakeRefusal {
		if r.Method == http.MethodDelete && r.URL.Path == "/v1/names/bob" {
			return &fakeRefusal{status: 503, code: names.CodeUnavailable, msg: "Down."}
		}
		return nil
	})
	if code, out := e.call("POST", "/v1/address/claim", map[string]any{"name": "bob", "actor": "admin"}); code != 500 {
		t.Fatalf("a change that can't be saved or undone: %d %v", code, out)
	}
	e.names.setFail(nil)
	failSaves(false)
	if n, _ := e.names.name("bob"); n.State != names.StateActive {
		t.Fatalf("bob after a release that failed: %+v", n)
	}
	if v := e.address(); v.Host != "alex.playkeeper.io" || e.a.loadCertificate("alex.playkeeper.io") == nil {
		t.Fatalf("address after a change that couldn't be saved or undone: %+v", v)
	}

	v := e.claim("bob")
	if v.Host != "bob.playkeeper.io" || v.Servers[0].Address != "survival.bob.playkeeper.io" {
		t.Fatalf("address after the change: %+v", v)
	}
	if n, _ := e.names.name("alex"); n.State != names.StateReleased {
		t.Fatalf("alex was not released: %+v", n)
	}
	if _, err := os.Stat(filepath.Join(e.cfg.CertsDir(), "alex.playkeeper.io.pem")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the old name's certificate is still served: %v", err)
	}
	if e.a.loadCertificate("alex.playkeeper.io") != nil || e.a.loadCertificate("bob.playkeeper.io") == nil {
		t.Fatal("certificates were not moved to the new name")
	}
	// The released name is held from others, not from this machine.
	var av api.NameAvailability
	if code := e.callInto("GET", "/v1/address/available?name=alex", nil, &av); code != 200 || !av.Available {
		t.Fatalf("this machine's released name: %d %+v", code, av)
	}

	if code := e.callInto("POST", "/v1/address/release", map[string]any{"actor": "admin"}, &v); code != 200 || v.Kind != api.AddressNone || v.Operation != nil {
		t.Fatalf("release: %d %+v", code, v)
	}
	if n, _ := e.names.name("bob"); n.State != names.StateReleased {
		t.Fatalf("bob was not released: %+v", n)
	}
	if e.a.loadCertificate("bob.playkeeper.io") != nil {
		t.Fatal("the released name's certificate was kept")
	}
	if v.Servers[0].Address != "" || v.Servers[0].Direct != "203.0.113.10" {
		t.Fatalf("join address after the release: %+v", v.Servers[0])
	}
	// Trying an own domain meanwhile doesn't make it look taken.
	if code := e.callInto("POST", "/v1/address/check", map[string]any{"domain": "play.example.com", "actor": "admin"}, &v); code != 200 || v.Kind != api.AddressOwn {
		t.Fatalf("own domain: %d %+v", code, v)
	}
	if code := e.callInto("DELETE", "/v1/address?actor=admin", nil, &v); code != 200 || v.Kind != api.AddressNone {
		t.Fatalf("removing the own domain: %d %+v", code, v)
	}
	var bob api.NameAvailability
	if code := e.callInto("GET", "/v1/address/available?name=bob", nil, &bob); code != 200 || !bob.Available {
		t.Fatalf("this machine's released name after an own domain: %d %+v", code, bob)
	}
	audit := e.auditActions()
	for _, want := range []string{"address.claim failed", "address.release succeeded"} {
		if !slices.Contains(audit, want) {
			t.Errorf("audit lacks %q: %v", want, audit)
		}
	}
}

func TestNamesServiceUnreachableBreaksNothingElse(t *testing.T) {
	e := newAgentEnv(t)
	id := e.addServerNamed("Survival")
	if v := e.address(); v.Kind != api.AddressNone || v.Names.Unreachable {
		t.Fatalf("an unused names service was contacted: %+v", v)
	}
	code, out := e.call("GET", "/v1/address/available?name=alex", nil)
	if code != 503 || out["code"] != api.CodeNamesUnreachable || out["params"].(map[string]any)["detail"] == "" {
		t.Fatalf("availability without the service: %d %v", code, out)
	}
	code, out = e.call("POST", "/v1/address/claim", map[string]any{"name": "alex", "actor": "admin"})
	if code != 503 || out["code"] != api.CodeNamesUnreachable {
		t.Fatalf("claim without the service: %d %v", code, out)
	}
	v := e.address()
	if v.Kind != api.AddressNone || !v.Names.Unreachable || v.Names.Error == "" || v.Names.URL != e.cfg.NamesURL {
		t.Fatalf("address without the service: %+v", v)
	}
	if code, _ := e.call("GET", "/v1/servers", nil); code != 200 {
		t.Fatalf("servers: %d", code)
	}
	if code, _ := e.call("GET", "/v1/machine", nil); code != 200 {
		t.Fatalf("machine: %d", code)
	}
	if st := e.a.serverByID(id).Status(context.Background()); st.JoinAddress != "" || st.Name != "Survival" {
		t.Fatalf("server status: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(e.cfg.AgentDir(), "names.key")); err != nil {
		t.Fatalf("the names key: %v", err)
	}
}

func TestANamesServiceThatNeverAnswersIsReportedInTime(t *testing.T) {
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	t.Cleanup(hang.Close)
	e := newAgentEnvWith(t, func(e *agentEnv) {
		e.cfg.NamesURL = hang.URL
		e.tweak = func(o *Options) { o.NamesCheckWait = 200 * time.Millisecond }
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, e.ts.URL+"/v1/address/available?name=alex", nil)
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("no answer about the name after %s: %v", time.Since(start).Round(time.Millisecond), err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); resp.StatusCode != http.StatusServiceUnavailable || out["code"] != api.CodeNamesUnreachable || took > 3*time.Second {
		t.Fatalf("a names service that never answers: %d %v after %s", resp.StatusCode, out, took)
	}
	if !e.address().Names.Unreachable {
		t.Fatal("the address doesn't say the service can't be reached")
	}
}

func TestNamesRefusalsKeepTheirCodeAndParams(t *testing.T) {
	e := newAddressEnv(t, nil)
	e.names.setFail(func(r *http.Request) *fakeRefusal {
		switch r.URL.Path {
		case "/v1/names/busy":
			return &fakeRefusal{status: 429, code: names.CodeRateLimited, msg: "Too many requests.", retryAfter: "120"}
		case "/v1/names/skew":
			return &fakeRefusal{status: 401, code: names.CodeClockSkew, msg: "Clock skew.", params: map[string]any{"serverTime": 1790000000, "skewSeconds": 400}}
		case "/v1/names/proxy":
			return &fakeRefusal{status: 502, page: true}
		}
		return nil
	})
	code, out := e.call("GET", "/v1/address/available?name=busy", nil)
	if code != 429 || out["code"] != names.CodeRateLimited || out["params"].(map[string]any)["retryAfterSeconds"] != float64(120) {
		t.Fatalf("rate limited: %d %v", code, out)
	}
	// A 401 from the service must not look like the dashboard's session
	// ending.
	code, out = e.call("POST", "/v1/address/claim", map[string]any{"name": "skew", "actor": "admin"})
	if code != 502 || out["code"] != names.CodeClockSkew || out["params"].(map[string]any)["skewSeconds"] != float64(400) {
		t.Fatalf("clock skew: %d %v", code, out)
	}
	code, out = e.call("GET", "/v1/address/available?name=proxy", nil)
	if code != 503 || out["code"] != api.CodeNamesUnreachable {
		t.Fatalf("an error page: %d %v", code, out)
	}
	if !e.address().Names.Unreachable {
		t.Fatal("an error page was not taken as the service being down")
	}

	e.names.takenBy("steve")
	e.names.takenBy("steve-mc")
	var av api.NameAvailability
	if code := e.callInto("GET", "/v1/address/available?name=steve", nil, &av); code != 200 || av.Available || av.Code != names.CodeNameTaken {
		t.Fatalf("taken name: %d %+v", code, av)
	}
	if !slices.Equal(av.Suggestions, []string{"stevecraft", "steve-plays", "steve-server"}) {
		t.Fatalf("suggestions: %v", av.Suggestions)
	}
	if e.address().Names.Unreachable {
		t.Fatal("an answer did not clear the unreachable state")
	}

	calls := len(e.names.callLog())
	if code := e.callInto("GET", "/v1/address/available?name=a", nil, &av); code != 200 || av.Available || av.Code != names.CodeInvalidName {
		t.Fatalf("invalid name: %d %+v", code, av)
	}
	if code, out := e.call("POST", "/v1/address/claim", map[string]any{"name": "a--b", "actor": "admin"}); code != 400 || out["code"] != names.CodeInvalidName {
		t.Fatalf("claiming an invalid name: %d %v", code, out)
	}
	if log := e.names.callLog(); len(log) != calls {
		t.Fatalf("invalid names were sent to the service: %v", log[calls:])
	}
}

func TestOwnDomainChecksTheNameBeforeHTTP01(t *testing.T) {
	e := newAddressEnv(t, nil)
	survival := e.addServerNamed("Survival")
	creative := e.addServerNamed("Creative")

	var plan api.AddressPlan
	if code := e.callInto("GET", "/v1/address/plan?domain=Play.Example.com&panelHost=203.0.113.10:8443", nil, &plan); code != 200 {
		t.Fatalf("plan: %d %+v", code, plan)
	}
	wantRecords := []api.DNSRecord{
		{Type: "A", Name: "play.example.com", Value: "203.0.113.10", TTL: 300},
		{ServerID: creative, Type: "SRV", Name: "_minecraft._tcp.creative.play.example.com", Value: "0 5 25566 play.example.com.", TTL: 300,
			SRV: &api.SRVParts{Service: "_minecraft", Protocol: "_tcp", Host: "creative.play.example.com", Priority: 0, Weight: 5, Port: 25566, Target: "play.example.com."}},
	}
	if len(plan.Records) != 2 || plan.Records[0] != wantRecords[0] || plan.Records[1].Name != wantRecords[1].Name || plan.Records[1].Value != wantRecords[1].Value || plan.Records[1].ServerID != creative {
		t.Fatalf("records:\n got %+v\nwant %+v", plan.Records, wantRecords)
	}
	if plan.Servers[0].Address != "play.example.com" || plan.Servers[1].Address != "creative.play.example.com" || plan.Servers[0].Published {
		t.Fatalf("planned join addresses: %+v", plan.Servers)
	}
	for _, bad := range []string{"alex.playkeeper.io", "playkeeper.io", "https://play.example.com/", "localhost", "203.0.113.10"} {
		if code, out := e.call("GET", "/v1/address/plan?domain="+bad, nil); code != 400 {
			t.Errorf("plan for %q: %d %v", bad, code, out)
		}
	}

	// Nothing points here yet: the domain is saved, and no certificate is
	// asked for.
	check := map[string]any{"domain": "play.example.com", "acceptTerms": true, "panelHost": "203.0.113.10:8443", "actor": "admin"}
	var v api.Address
	if code := e.callInto("POST", "/v1/address/check", check, &v); code != 200 {
		t.Fatalf("check: %d %+v", code, v)
	}
	if v.Kind != api.AddressOwn || v.Check == nil || v.Check.Name.OK || v.Check.Name.Code != certs.CodeNameMissing || v.Operation != nil {
		t.Fatalf("check before the records exist: %+v %+v", v, v.Check)
	}
	if len(e.ca.requests()) != 0 {
		t.Fatal("a certificate was asked for before the name pointed here")
	}

	e.dns.set("play.example.com", "203.0.113.10")
	e.dns.setSRV("creative.play.example.com", 25566, "play.example.com")
	if code := e.callInto("POST", "/v1/address/check", check, &v); code != 200 || !v.Check.Ready || v.Operation == nil || v.Operation.Kind != "certificate.issue" {
		t.Fatalf("check with the records: %d %+v", code, v)
	}
	if op := e.waitOp(v.Operation.ID); op.Status != api.OpSucceeded {
		t.Fatalf("certificate: %+v", op)
	}
	reqs := e.ca.requests()
	if len(reqs) != 1 || reqs[0].HTTP01 == nil || reqs[0].HTTP01.Addr != "127.0.0.1:0" || reqs[0].DNS01 != nil || !slices.Equal(reqs[0].Names, []string{"play.example.com"}) {
		t.Fatalf("certificate requests: %+v", reqs)
	}
	v = e.address()
	if v.Certificate == nil || v.Certificate.Challenge != "http-01" || v.Certificate.NotAfter == nil {
		t.Fatalf("certificate: %+v", v.Certificate)
	}
	if !v.Servers[0].Published || v.Servers[0].Address != "play.example.com" || !v.Servers[1].Published {
		t.Fatalf("join addresses: %+v", v.Servers)
	}
	if got := e.a.serverByID(survival).Status(context.Background()).JoinAddress; got != "play.example.com" {
		t.Fatalf("server status join address %q", got)
	}

	// The certificate operation looks again itself: a name that moved
	// elsewhere never opens port 80 for Let's Encrypt.
	e.dns.set("play.example.com", "198.51.100.7")
	if code := e.callInto("POST", "/v1/address/certificate", map[string]any{"actor": "admin"}, &v); code != 200 || v.Operation == nil {
		t.Fatalf("certificate request: %d %+v", code, v)
	}
	if op := e.waitOp(v.Operation.ID); op.Status != api.OpFailed || !strings.Contains(op.Error, "198.51.100.7") {
		t.Fatalf("certificate for a name elsewhere: %+v", op)
	}
	if len(e.ca.requests()) != 1 {
		t.Fatal("Let's Encrypt was asked to check a name that points elsewhere")
	}
	if v = e.address(); v.Check.Name.OK || v.Servers[0].Published {
		t.Fatalf("the failed look was not kept: %+v", v.Check)
	}
	e.a.addr.mu.Lock()
	recheck := e.a.addr.recheck
	e.a.addr.mu.Unlock()
	if limit := time.Now().Add(ownRecheckPending); recheck.After(limit) {
		t.Fatalf("after the failed look the records are looked at again at %v, later than %v", recheck, limit)
	}

	if code, out := e.call("POST", "/v1/address/claim", map[string]any{"name": "alex", "actor": "admin"}); code != 409 {
		t.Fatalf("claiming a free name while using a domain: %d %v", code, out)
	}
	if code := e.callInto("DELETE", "/v1/address?actor=admin", nil, &v); code != 200 || v.Kind != api.AddressNone {
		t.Fatalf("stop using the domain: %d %+v", code, v)
	}
	if _, err := os.Stat(filepath.Join(e.cfg.CertsDir(), "play.example.com.pem")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the domain's certificate is still served: %v", err)
	}
	if !slices.Contains(e.auditActions(), "address.remove succeeded") {
		t.Fatalf("audit: %v", e.auditActions())
	}
}

func TestCertificateRenewalAndBackoff(t *testing.T) {
	e := newAddressEnv(t, func(o *Options) { o.AddressInterval = 20 * time.Millisecond })
	e.addServerNamed("Survival")
	retryAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	e.ca.plan = func(n int) (bool, error) {
		if n == 1 {
			return true, nil
		}
		return false, &certs.Problem{Note: certs.Note{Code: certs.CodeRateLimited, Message: "Too many certificates this week."}, RetryAt: retryAt}
	}
	e.dns.set("play.example.com", "203.0.113.10")
	var v api.Address
	if code := e.callInto("POST", "/v1/address/check", map[string]any{"domain": "play.example.com", "actor": "admin"}, &v); code != 200 || v.Operation == nil {
		t.Fatalf("check: %d %+v", code, v)
	}
	// The first certificate is due for renewal at once, so the loop renews
	// it; that attempt fails and the next one waits for the CA.
	e.waitFor("a renewal attempt", func() bool { return len(e.ca.requests()) == 2 && e.a.addressOp() == nil })
	time.Sleep(200 * time.Millisecond)
	if n := len(e.ca.requests()); n != 2 {
		t.Fatalf("%d attempts: the failed renewal was retried at once", n)
	}
	v = e.address()
	c := v.Certificate
	if c == nil || c.NotAfter == nil || c.Failures != 1 || c.Problem == nil || c.Problem.Code != certs.CodeRateLimited || c.NextAttempt == nil || !c.NextAttempt.Equal(retryAt) {
		t.Fatalf("certificate after the failed renewal: %+v %+v", c, c.Problem)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'certificate.issue' AND actor = 'playkeeper'`); n != 1 {
		t.Fatalf("%d renewals by Playkeeper", n)
	}
	code, out := e.call("POST", "/v1/address/certificate", map[string]any{"actor": "admin"})
	if code != 409 || out["code"] != api.CodeRetryLater || out["params"].(map[string]any)["retryAt"] != retryAt.Format(time.RFC3339) {
		t.Fatalf("trying again before the CA allows it: %d %v", code, out)
	}
}

func TestFreeRecordsFollowTheServers(t *testing.T) {
	e := newAddressEnv(t, nil)
	survival := e.addServerNamed("Survival")
	e.claim("alex")
	creative := e.addServerNamed("Creative")
	e.waitFor("the new server's record", func() bool { return e.names.labels("alex")["creative"] == 25566 })

	code, out := e.call("POST", "/v1/servers/"+survival+"/delete", map[string]any{"confirm": "Survival", "actor": "admin"})
	if code != 202 {
		t.Fatalf("delete: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("delete: %+v", op)
	}
	e.waitFor("the deleted server's record to go", func() bool {
		l := e.names.labels("alex")
		return len(l) == 1 && l["creative"] == 25566
	})
	if got := e.a.serverByID(creative).Status(context.Background()).JoinAddress; got != "creative.alex.playkeeper.io" {
		t.Fatalf("join address %q", got)
	}
}

func TestFreeNameIsRefreshedAtStartAndDaily(t *testing.T) {
	e := newAddressEnv(t, nil)
	e.addServerNamed("Survival")
	e.claim("alex")
	refreshes := e.names.count("POST /v1/names/alex/address")
	e.stop()
	e.start()
	e.waitFor("the refresh at start", func() bool { return e.names.count("POST /v1/names/alex/address") == refreshes+2 })
	st := e.a.address()
	if d := time.Until(st.Free.NextRefresh); d < 23*time.Hour || d > 25*time.Hour {
		t.Fatalf("next refresh in %v", d)
	}

	// Due again: the loop refreshes. A failed refresh is tried again in an
	// hour.
	e.names.setFail(func(r *http.Request) *fakeRefusal {
		if strings.HasSuffix(r.URL.Path, "/address") {
			return &fakeRefusal{status: 503, code: names.CodeUnavailable, msg: "Down."}
		}
		return nil
	})
	_ = e.a.updateAddress(func(st *addressState) {
		f := *st.Free
		f.NextRefresh = time.Now().Add(-time.Minute)
		st.Free = &f
	})
	e.a.serversChanged()
	e.waitFor("a refresh attempt", func() bool { return e.names.count("POST /v1/names/alex/address") > refreshes+2 })
	e.waitFor("the retry to be planned", func() bool {
		d := time.Until(e.a.address().Free.NextRefresh)
		return d > 50*time.Minute && d < 70*time.Minute
	})
	if v := e.address(); !v.Names.Unreachable || v.Kind != api.AddressPlaykeeper {
		t.Fatalf("address after a failed refresh: %+v", v)
	}
}

func TestFreeNameGivenBackIsClaimedAgain(t *testing.T) {
	e := newAddressEnv(t, nil)
	e.claim("alex")
	// The service freed the name (two months without a refresh).
	e.names.forget("alex")
	var v api.Address
	if code := e.callInto("POST", "/v1/address/refresh", map[string]any{"actor": "admin"}, &v); code != 200 || v.Operation == nil {
		t.Fatalf("refresh: %d %+v", code, v)
	}
	if op := e.waitOp(v.Operation.ID); op.Status != api.OpSucceeded {
		t.Fatalf("refresh: %+v", op)
	}
	if n, _ := e.names.name("alex"); n.State != names.StateActive {
		t.Fatalf("alex was not claimed again: %+v", n)
	}
}

func TestAddressRoutesRejectBadInput(t *testing.T) {
	e := newAddressEnv(t, nil)
	for _, c := range []struct {
		name, method, path string
		body               any
		want               int
	}{
		{"extra field", "POST", "/v1/address/claim", `{"name":"alex","actor":"admin","key":"x"}`, 400},
		{"no actor", "POST", "/v1/address/claim", map[string]any{"name": "alex"}, 400},
		{"traversal name", "POST", "/v1/address/claim", map[string]any{"name": "../etc", "actor": "admin"}, 400},
		{"domain with a scheme", "POST", "/v1/address/check", map[string]any{"domain": "https://play.example.com", "actor": "admin"}, 400},
		{"domain with a port", "POST", "/v1/address/check", map[string]any{"domain": "play.example.com:25565", "actor": "admin"}, 400},
		{"delete without actor", "DELETE", "/v1/address", nil, 400},
		{"delete without a domain", "DELETE", "/v1/address?actor=admin", nil, 409},
		{"certificate without a name", "POST", "/v1/address/certificate", map[string]any{"actor": "admin"}, 409},
		{"release without a name", "POST", "/v1/address/release", map[string]any{"actor": "admin"}, 409},
		{"refresh without a name", "POST", "/v1/address/refresh", map[string]any{"actor": "admin"}, 409},
		{"wrong method", "PUT", "/v1/address", nil, 404},
	} {
		if code, out := e.call(c.method, c.path, c.body); code != c.want {
			t.Errorf("%s: %s %s -> %d %v, want %d", c.name, c.method, c.path, code, out, c.want)
		}
	}
	if log := e.names.callLog(); len(log) != 0 || len(e.ca.requests()) != 0 {
		t.Fatalf("rejected requests reached the names service or the CA: %v", log)
	}
}

func TestFreeViewDerivesTheLapsedState(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	refreshed := now.Add(-40 * 24 * time.Hour)
	f := &freeState{Name: names.Name{Name: "alex", State: names.StateActive, RefreshedAt: refreshed, RefreshBy: refreshed.Add(namesLapseAfter)}}
	v := freeView(f, now)
	if v.State != names.StateLapsed || v.StoppedAt == nil || !v.StoppedAt.Equal(refreshed.Add(namesLapseAfter)) {
		t.Fatalf("a name past its refresh-by time: %+v", v)
	}
	f.Name.State = names.StateLapsed
	f.Name.RefreshBy = time.Time{}
	if v := freeView(f, now); v.StoppedAt == nil || !v.StoppedAt.Equal(refreshed.Add(namesLapseAfter)) {
		t.Fatalf("a lapsed name: %+v", v)
	}
	f.Name.State, f.Name.RefreshedAt, f.Name.RefreshBy = names.StateActive, now, now.Add(namesLapseAfter)
	if v := freeView(f, now); v.State != names.StateActive || v.StoppedAt != nil {
		t.Fatalf("an active name: %+v", v)
	}
}

func TestPublicIPAndHostPort(t *testing.T) {
	for in, want := range map[string]bool{
		"203.0.113.10:8443": true, "[2001:db8::1]:8443": true, "203.0.113.10": true,
		"192.168.1.5:8443": false, "10.0.0.1": false, "100.64.1.1": false, "127.0.0.1:8443": false,
		"localhost:8443": false, "my-vps:8443": false, "[fe80::1]:8443": false, "": false,
	} {
		if _, ok := publicIP(in); ok != want {
			t.Errorf("publicIP(%q) = %v, want %v", in, ok, want)
		}
	}
	for _, c := range []struct {
		host string
		port int
		want string
	}{{"203.0.113.10", 25565, "203.0.113.10"}, {"203.0.113.10", 25566, "203.0.113.10:25566"}, {"2001:db8::1", 25565, "[2001:db8::1]"}, {"2001:db8::1", 25570, "[2001:db8::1]:25570"}} {
		if got := hostPort(c.host, c.port); got != c.want {
			t.Errorf("hostPort(%q, %d) = %q, want %q", c.host, c.port, got, c.want)
		}
	}
}

func TestLivenessCheckIsAnsweredOnlyForTheMachinesName(t *testing.T) {
	e := newAddressEnv(t, nil)
	e.names.takenBy("steve")
	const nonce = "dGVzdC1ub25jZS0yMi1jaGFycw"
	ask := func(host string) (int, names.Alive) {
		t.Helper()
		var v names.Alive
		code := e.callInto("GET", "/v1/address/alive/"+nonce+"?"+url.Values{"host": {host}}.Encode(), nil, &v)
		return code, v
	}
	if code, _ := ask("alex.playkeeper.io:8443"); code != http.StatusNotFound {
		t.Fatalf("before a claim: %d", code)
	}
	// The service checks a lapsed name's address before it answers the claim.
	during := 0
	e.names.setFail(func(r *http.Request) *fakeRefusal {
		if r.Method == "PUT" && r.URL.Path == "/v1/names/alex" {
			during, _ = ask("alex.playkeeper.io:8443")
		}
		return nil
	})
	e.claim("alex")
	e.names.setFail(nil)
	if during != http.StatusOK {
		t.Fatalf("during the claim: %d", during)
	}
	key, err := names.LoadOrCreateKey(filepath.Join(e.cfg.AgentDir(), "names.key"))
	if err != nil {
		t.Fatal(err)
	}
	pub := key.Public().(ed25519.PublicKey)
	code, v := ask("Alex.playkeeper.io.:8443")
	if code != http.StatusOK || v.Name != "alex" || !names.VerifyAlive(pub, names.DefaultBase, "alex", nonce, v.Signature) {
		t.Fatalf("held name: %d %+v", code, v)
	}
	if names.VerifyAlive(pub, names.DefaultBase, "alex", nonce+"x", v.Signature) {
		t.Fatal("the answer is not bound to the nonce")
	}
	for _, host := range []string{"bob.playkeeper.io:8443", "steve.playkeeper.io:8443", "alex.example.com:8443", "survival.alex.playkeeper.io:8443", "203.0.113.10:8443", ""} {
		if code, v := ask(host); code != http.StatusNotFound || v.Signature != "" {
			t.Errorf("%q: %d %+v", host, code, v)
		}
	}
	if code, _ := e.call("GET", "/v1/address/alive/short?host=alex.playkeeper.io", nil); code != http.StatusBadRequest {
		t.Errorf("bad nonce: %d", code)
	}

	// A name the service reports released is not held any more.
	_ = e.a.updateAddress(func(st *addressState) {
		f := *st.Free
		f.Name.State = names.StateReleased
		st.Free = &f
	})
	if code, _ := ask("alex.playkeeper.io:8443"); code != http.StatusNotFound {
		t.Errorf("released in the service: %d", code)
	}
	_ = e.a.updateAddress(func(st *addressState) {
		f := *st.Free
		f.Name.State = names.StateActive
		st.Free = &f
	})
	var a api.Address
	if code := e.callInto("POST", "/v1/address/release", map[string]any{"actor": "admin"}, &a); code != http.StatusOK {
		t.Fatalf("release: %d", code)
	}
	if code, _ := ask("alex.playkeeper.io:8443"); code != http.StatusNotFound {
		t.Errorf("released: %d", code)
	}
}

func TestCertificateLimitWaitsForTheNamesService(t *testing.T) {
	e := newAddressEnv(t, nil)
	retryAt := time.Now().Add(49 * time.Hour).UTC().Truncate(time.Second)
	e.names.setFail(func(r *http.Request) *fakeRefusal {
		if r.Method != "PUT" || !strings.Contains(r.URL.Path, "/acme-challenge/") {
			return nil
		}
		return &fakeRefusal{status: http.StatusTooManyRequests, code: names.CodeCertificateLimit, msg: "New certificates for playkeeper.io names are paused.",
			params: map[string]any{"scope": "all", "limit": 50, "retryAt": retryAt.Format(time.RFC3339)}, retryAfter: strconv.Itoa(int(time.Until(retryAt).Seconds()))}
	})
	v := e.claim("alex")
	c := v.Certificate
	if c == nil || c.Problem == nil || c.Problem.Code != certs.CodeCertificateLimit || c.Problem.Params["kind"] != "all" || c.Problem.RetryAt == nil {
		t.Fatalf("certificate: %+v", c)
	}
	if d := c.Problem.RetryAt.Sub(retryAt); d < -5*time.Second || d > 5*time.Second {
		t.Fatalf("retry at %v, want the service's Retry-After, %v", c.Problem.RetryAt, retryAt)
	}
	if c.NextAttempt == nil || c.NextAttempt.Before(*c.Problem.RetryAt) {
		t.Fatalf("next attempt %v before %v", c.NextAttempt, c.Problem.RetryAt)
	}
	code, out := e.call("POST", "/v1/address/certificate", map[string]any{"actor": "admin"})
	if code != http.StatusConflict || out["code"] != api.CodeRetryLater || !strings.HasPrefix(fmt.Sprint(out["error"]), "The free address service refuses") {
		t.Fatalf("trying again before the service allows it: %d %v", code, out)
	}
}

func TestServerAddressesWaitForTheNamesService(t *testing.T) {
	e := newAddressEnv(t, func(o *Options) { o.AddressInterval = 20 * time.Millisecond })
	e.addServerNamed("Survival")
	from := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	var mu sync.Mutex
	refuse := names.CodeServerNotYet
	setRefusal := func(code string) {
		mu.Lock()
		refuse = code
		mu.Unlock()
	}
	e.names.setFail(func(r *http.Request) *fakeRefusal {
		mu.Lock()
		code := refuse
		mu.Unlock()
		if r.Method != "PUT" || !strings.Contains(r.URL.Path, "/servers/") || code == "" {
			return nil
		}
		params := map[string]any{"name": "alex", "port": names.AlivePort}
		if code == names.CodeServerNotYet {
			params = map[string]any{"name": "alex", "from": from.Unix()}
		}
		return &fakeRefusal{status: http.StatusConflict, code: code, msg: "Not yet.", params: params}
	})
	retryNow := func() {
		_ = e.a.updateAddress(func(st *addressState) {
			f := *st.Free
			f.ServersRetry = time.Time{}
			st.Free = &f
		})
	}

	// Publishing does not wait for server records the service refuses.
	start := time.Now()
	v := e.claim("alex")
	if time.Since(start) > time.Minute {
		t.Fatalf("the claim waited %v for refused server records", time.Since(start))
	}
	if f := v.Free; f.ServersWait != names.CodeServerNotYet || f.ServersFrom == nil || !f.ServersFrom.Equal(from) || len(v.Servers) != 1 || v.Servers[0].Published {
		t.Fatalf("waiting for the name's age: %+v %+v", v.Free, v.Servers)
	}
	puts := func() int { return e.names.count("PUT /v1/names/alex/servers/") }
	n := puts()
	time.Sleep(200 * time.Millisecond)
	if puts() != n {
		t.Fatal("the loop asked again before the service allows server addresses")
	}

	setRefusal(names.CodeNotAnswering)
	retryNow()
	e.waitFor("the not-answering refusal", func() bool {
		f := e.address().Free
		return f.ServersWait == names.CodeNotAnswering && f.ServersFrom == nil
	})
	n = puts()
	time.Sleep(200 * time.Millisecond)
	if puts() != n {
		t.Fatal("the loop asked again at once after the dashboard did not answer")
	}

	setRefusal("")
	retryNow()
	e.waitFor("the server's record", func() bool {
		v := e.address()
		return v.Free.ServersWait == "" && len(v.Servers) == 1 && v.Servers[0].Published
	})
}

func TestFreeViewSaysWhyANameLapsed(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	claimed, answered, refreshed := now.Add(-20*24*time.Hour), now.Add(-9*24*time.Hour), now.Add(-31*24*time.Hour)
	for _, c := range []struct {
		name    string
		n       names.Name
		reason  string
		stopped time.Time
	}{
		{"no answer since the last one", names.Name{State: names.StateLapsed, LapseReason: names.LapseNoAnswer, ClaimedAt: claimed, RefreshedAt: now, AnsweredAt: answered}, names.LapseNoAnswer, answered.Add(7 * 24 * time.Hour)},
		{"never answered", names.Name{State: names.StateLapsed, LapseReason: names.LapseNoAnswer, ClaimedAt: claimed, RefreshedAt: now}, names.LapseNoAnswer, claimed.Add(7 * 24 * time.Hour)},
		{"not refreshed", names.Name{State: names.StateLapsed, LapseReason: names.LapseNotRefreshed, ClaimedAt: claimed, RefreshedAt: refreshed}, names.LapseNotRefreshed, refreshed.Add(namesLapseAfter)},
		{"not refreshed, by the clock", names.Name{State: names.StateActive, ClaimedAt: claimed, RefreshedAt: refreshed, RefreshBy: refreshed.Add(namesLapseAfter)}, names.LapseNotRefreshed, refreshed.Add(namesLapseAfter)},
	} {
		c.n.Name = "alex"
		v := freeView(&freeState{Name: c.n}, now)
		if v.State != names.StateLapsed || v.LapseReason != c.reason || v.StoppedAt == nil || !v.StoppedAt.Equal(c.stopped) {
			t.Errorf("%s: %s %q %v", c.name, v.State, v.LapseReason, v.StoppedAt)
		}
	}
}
