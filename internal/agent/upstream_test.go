package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// fakeUpstream is one HTTPS server standing in for every upstream host the
// software package reads (Mojang, Fabric and the others). Its client dials
// it whatever host a URL names, so tests go through the real URLs, host
// allowlists and TLS without touching the network.
type fakeUpstream struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	routes map[string][]byte
	// handlers answer a host and path instead of a fixed body.
	handlers map[string]http.HandlerFunc
	hits     map[string]int
	// serverJar is what the fake Mojang serves as each version's server jar.
	serverJar map[string][]byte
}

func startFakeUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{t: t, routes: map[string][]byte{}, handlers: map[string]http.HandlerFunc{}, hits: map[string]int{}, serverJar: map[string][]byte{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Host + r.URL.Path
		f.mu.Lock()
		body, ok := f.routes[key]
		h := f.handlers[key]
		f.hits[key]++
		f.mu.Unlock()
		if h != nil {
			h(w, r)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeUpstream) client() *http.Client {
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	addr := f.srv.Listener.Addr().String()
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "example.com"},
	}}
}

func (f *fakeUpstream) key(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		f.t.Fatal(err)
	}
	return u.Host + u.Path
}

func (f *fakeUpstream) serve(rawURL string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[f.key(rawURL)] = body
}

// handle answers rawURL's host and path with h.
func (f *fakeUpstream) handle(rawURL string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[f.key(rawURL)] = h
}

func (f *fakeUpstream) remove(rawURL string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.routes, f.key(rawURL))
}

func (f *fakeUpstream) hitCount(rawURL string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[f.key(rawURL)]
}

// fakeReleases are the Minecraft releases the fake Mojang lists, with the
// times Mojang released them.
var fakeReleases = []struct{ id, released string }{
	{"26.2", "2026-09-02T09:14:07+00:00"},
	{"26.1.2", "2026-04-02T12:00:00+00:00"},
}

// serveMojang serves Mojang's version manifest, a version file for each of
// fakeReleases and each version's server jar, pinned by SHA-1 as Mojang
// pins them. A snapshot is listed too, which no catalog offers.
func (f *fakeUpstream) serveMojang() {
	f.t.Helper()
	f.serveMojangReleases(fakeReleases)
}

// serveMojangReleases is serveMojang with other releases, newest first.
func (f *fakeUpstream) serveMojangReleases(releases []struct{ id, released string }) {
	f.t.Helper()
	versions := []map[string]any{{"id": "26.3-snapshot-2", "type": "snapshot", "url": "https://piston-meta.mojang.com/v1/packages/0/26.3-snapshot-2.json",
		"sha1": "0000000000000000000000000000000000000000", "releaseTime": "2026-09-20T10:00:00+00:00"}}
	for _, r := range releases {
		jar, ok := f.serverJar[r.id]
		if !ok {
			jar = []byte("fake minecraft server " + r.id)
			f.serverJar[r.id] = jar
		}
		jarURL := "https://piston-data.mojang.com/v1/objects/" + sha1Hex(jar) + "/server.jar"
		f.serve(jarURL, jar)
		file, err := json.Marshal(map[string]any{
			"id": r.id, "type": "release",
			"javaVersion": map[string]any{"component": "java-runtime-delta", "majorVersion": 21},
			"downloads":   map[string]any{"server": map[string]any{"sha1": sha1Hex(jar), "size": len(jar), "url": jarURL}},
		})
		if err != nil {
			f.t.Fatal(err)
		}
		fileURL := "https://piston-meta.mojang.com/v1/packages/" + sha1Hex(file) + "/" + r.id + ".json"
		f.serve(fileURL, file)
		versions = append(versions, map[string]any{"id": r.id, "type": "release", "url": fileURL, "sha1": sha1Hex(file), "releaseTime": r.released})
	}
	man, err := json.Marshal(map[string]any{"latest": map[string]string{"release": "26.2", "snapshot": "26.3-snapshot-2"}, "versions": versions})
	if err != nil {
		f.t.Fatal(err)
	}
	f.serve("https://piston-meta.mojang.com/mc/game/version_manifest_v2.json", man)
}

// serverJarURL is where the fake Mojang serves a version's server jar.
func (f *fakeUpstream) serverJarURL(mc string) string {
	return "https://piston-data.mojang.com/v1/objects/" + sha1Hex(f.serverJar[mc]) + "/server.jar"
}

// serveFabricLists serves Fabric's lists of Minecraft versions and loaders.
func (f *fakeUpstream) serveFabricLists() {
	f.serve("https://meta.fabricmc.net/v2/versions/game", []byte(`[{"version":"26.2","stable":true},{"version":"26.1.2","stable":true},{"version":"26.3-snapshot-2","stable":false}]`))
	f.serve("https://meta.fabricmc.net/v2/versions/loader", []byte(`[{"version":"0.17.2","stable":true},{"version":"0.17.1","stable":false},{"version":"0.12.5","stable":false}]`))
}
