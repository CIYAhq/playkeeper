package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/minecraft/software"
)

// sourcesUnused is the server types' sources for a check that asks only
// PaperMC and the libraries.
func sourcesUnused() software.Sources { return software.Sources{} }

// fakeUpstreams answers for every host the checker asks, by host and path.
type fakeUpstreams struct {
	srv    *httptest.Server
	mu     sync.Mutex
	routes map[string]func(w http.ResponseWriter)
}

func startFakeUpstreams(t *testing.T) *fakeUpstreams {
	f := &fakeUpstreams{routes: map[string]func(http.ResponseWriter){}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		h := f.routes[r.Host+r.URL.Path]
		f.mu.Unlock()
		if h == nil {
			http.NotFound(w, r)
			return
		}
		h(w)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeUpstreams) serve(hostPath string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[hostPath] = func(w http.ResponseWriter) { w.Write(body) }
}

func (f *fakeUpstreams) down(hostPath string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[hostPath] = func(w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) }
}

func (f *fakeUpstreams) client() *http.Client {
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	addr := f.srv.Listener.Addr().String()
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "example.com"},
	}}
}

// The live check reports each upstream that answers and serves what its
// list says, and fails on one that doesn't, as PaperMC's API didn't on
// 1 Oct 2026: the job that runs it then goes red, and nothing else does.
func TestTheLiveCheckFailsOnAnUpstreamThatIsDown(t *testing.T) {
	f := startFakeUpstreams(t)
	jar := []byte("a Paper jar")
	jarSum := fmt.Sprintf("%x", sha256.Sum256(jar))
	f.serve("fill.papermc.io/v3/projects/paper/versions", []byte(`{"versions":[{"version":{"id":"26.2","support":{"status":"SUPPORTED"},"java":{"version":{"minimum":25}}}}]}`))
	f.serve("fill.papermc.io/v3/projects/paper/versions/26.2/builds", fmt.Appendf(nil,
		`[{"id":135,"channel":"STABLE","downloads":{"server:default":{"name":"paper-26.2-135.jar","checksums":{"sha256":%q},"url":"https://fill-data.papermc.io/v1/objects/%s/paper-26.2-135.jar"}}}]`, jarSum, jarSum))
	f.serve("fill-data.papermc.io/v1/objects/"+jarSum+"/paper-26.2-135.jar", jar)
	plugin := []byte("LuckPerms")
	pluginSum := sha512.Sum512(plugin)
	f.serve("api.modrinth.com/v2/project/luckperms/version", fmt.Appendf(nil, `[{"files":[{"url":"https://cdn.modrinth.com/data/Vebnzrzj/LuckPerms.jar","hashes":{"sha512":%q}}]}]`, hex.EncodeToString(pluginSum[:])))
	f.serve("cdn.modrinth.com/data/Vebnzrzj/LuckPerms.jar", plugin)
	f.serve("hangar.papermc.io/api/v1/projects/GeyserMC/Floodgate", []byte(`{"name":"Floodgate"}`))

	check := func() (*checker, string) {
		var out bytes.Buffer
		hc := f.client()
		c := newChecker(&out, minecraft.Fill{BaseURL: minecraft.DefaultFillURL, Client: hc}, sourcesUnused(), hc, t.TempDir(), time.Now())
		c.actions, c.cfKey = true, ""
		c.paper()
		c.libraries()
		return c, out.String()
	}
	c, out := check()
	for _, want := range []string{"ok    Paper: 1 versions; 26.2 build 135 downloads", "ok    Modrinth:", "ok    Hangar:", "skip  CurseForge"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report must say %q:\n%s", want, out)
		}
	}
	if len(c.failed) != 0 || !strings.Contains(out, "::warning title=Paper's built-in list::") {
		t.Fatalf("every upstream answers, and the built-in list is behind the fake's 26.2 build 135: %v\n%s", c.failed, out)
	}

	f.down("fill.papermc.io/v3/projects/paper/versions")
	f.serve("cdn.modrinth.com/data/Vebnzrzj/LuckPerms.jar", []byte("not LuckPerms"))
	c, out = check()
	if strings.Join(c.failed, ",") != "Paper,Modrinth" || !strings.Contains(out, "::error title=Paper::") || !strings.Contains(out, "HTTP 503") || !strings.Contains(out, "doesn't match its checksum") {
		t.Fatalf("PaperMC's 503 and Modrinth's wrong file must fail the check: %v\n%s", c.failed, out)
	}
}
