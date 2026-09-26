package software

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeNet is one HTTPS server standing in for every upstream host. Its
// client dials it whatever host a URL names, so tests go through the real
// upstream URLs, host allowlists and TLS without touching the network.
type fakeNet struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	routes map[string]http.HandlerFunc
	hits   map[string]int
}

func newFakeNet(t *testing.T) *fakeNet {
	t.Helper()
	f := &fakeNet{t: t, routes: map[string]http.HandlerFunc{}, hits: map[string]int{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Host + r.URL.Path
		f.mu.Lock()
		h := f.routes[key]
		f.hits[key]++
		f.mu.Unlock()
		switch {
		case r.Header.Get("User-Agent") != userAgent:
			http.Error(w, "requests need a User-Agent", http.StatusForbidden)
		case h == nil:
			http.NotFound(w, r)
		default:
			h(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeNet) client() *http.Client {
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

func (f *fakeNet) sources() Sources { return Sources{Client: f.client()} }

func (f *fakeNet) handle(rawURL string, h http.HandlerFunc) {
	f.t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		f.t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[u.Host+u.Path] = h
}

func (f *fakeNet) serve(rawURL string, body []byte) {
	f.handle(rawURL, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })
}

func (f *fakeNet) status(rawURL string, code int) {
	f.handle(rawURL, func(w http.ResponseWriter, r *http.Request) { http.Error(w, http.StatusText(code), code) })
}

func (f *fakeNet) hitCount(rawURL string) int {
	u, _ := url.Parse(rawURL)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[u.Host+u.Path]
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hexSum(a Algorithm, b []byte) string {
	switch a {
	case SHA512:
		s := sha512.Sum512(b)
		return hex.EncodeToString(s[:])
	case SHA256:
		s := sha256.Sum256(b)
		return hex.EncodeToString(s[:])
	case SHA1:
		s := sha1.Sum(b)
		return hex.EncodeToString(s[:])
	case MD5:
		s := md5.Sum(b)
		return hex.EncodeToString(s[:])
	}
	panic("unknown algorithm")
}

// zipOf builds a jar from name/body pairs.
func zipOf(t *testing.T, nameBody ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i+1 < len(nameBody); i += 2 {
		w, err := zw.Create(nameBody[i])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(nameBody[i+1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// editJSON decodes b, lets edit change it and encodes what edit returns.
func editJSON(t *testing.T, b []byte, edit func(v any) any) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(edit(v))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// obj returns the JSON object at path inside v.
func obj(v any, path ...string) map[string]any {
	m := v.(map[string]any)
	for _, k := range path {
		m = m[k].(map[string]any)
	}
	return m
}

// reverseList reverses the JSON array at path (the whole document when
// path is empty), so tests can check that nothing depends on the order an
// upstream lists things in.
func reverseList(t *testing.T, b []byte, path ...string) []byte {
	t.Helper()
	return editJSON(t, b, func(v any) any {
		if len(path) == 0 {
			slices.Reverse(v.([]any))
			return v
		}
		slices.Reverse(obj(v, path[:len(path)-1]...)[path[len(path)-1]].([]any))
		return v
	})
}

// reverseXMLVersions reverses the <version> lines of a Maven metadata file.
func reverseXMLVersions(b []byte) []byte {
	lines := strings.Split(string(b), "\n")
	var at []int
	for i, l := range lines {
		if strings.Contains(l, "<version>") {
			at = append(at, i)
		}
	}
	for i, j := 0, len(at)-1; i < j; i, j = i+1, j-1 {
		lines[at[i]], lines[at[j]] = lines[at[j]], lines[at[i]]
	}
	return []byte(strings.Join(lines, "\n"))
}

// wantKind checks that err is an Error of the given kind, explained in full
// sentences with a hint.
func wantKind(t *testing.T, err error, kind Kind) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("got %v (%T), want an Error of kind %s", err, err, kind)
	}
	if e.Kind != kind {
		t.Fatalf("got kind %s (%q), want %s", e.Kind, e.Msg, kind)
	}
	if !strings.HasSuffix(e.Msg, ".") || e.Hint == "" || strings.Contains(e.Msg, "%!") || ((e.Msg[0] < 'A' || e.Msg[0] > 'Z') && e.Msg[0] != '"') {
		t.Fatalf("an error must be full sentences with a hint, got Msg=%q Hint=%q", e.Msg, e.Hint)
	}
	return e
}

var mojangFixtureVersions = []string{"26.3", "26.2", "26.1.2", "1.21.11", "1.21.8", "1.21.1"}

// mojangFiles are the trimmed version files the fake Mojang serves.
func mojangFiles(t *testing.T) map[string][]byte {
	out := map[string][]byte{}
	for _, id := range mojangFixtureVersions {
		out[id] = readFixture(t, "mojang/"+id+".json")
	}
	return out
}

// serveMojang serves Mojang's version manifest, with extra entries first,
// and the given version files, pinning each by its SHA-1 as Mojang does.
// It returns the manifest it serves.
func serveMojang(t *testing.T, f *fakeNet, files map[string][]byte, extra ...mojangEntry) []byte {
	t.Helper()
	var m struct {
		Latest   json.RawMessage  `json:"latest"`
		Versions []map[string]any `json:"versions"`
	}
	if err := json.Unmarshal(readFixture(t, "mojang/version_manifest_v2.json"), &m); err != nil {
		t.Fatal(err)
	}
	var added []map[string]any
	for _, e := range extra {
		added = append(added, map[string]any{"id": e.ID, "type": e.Type, "url": e.URL, "sha1": e.SHA1})
	}
	m.Versions = append(added, m.Versions...)
	for _, v := range m.Versions {
		body, ok := files[v["id"].(string)]
		if !ok {
			continue
		}
		v["sha1"] = hexSum(SHA1, body)
		f.serve(v["url"].(string), body)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	f.serve(mojangManifestURL, b)
	return b
}

// mojangVersionFile is a version file whose server jar is server, served
// by the fake Mojang at the URL it names.
func mojangVersionFile(t *testing.T, f *fakeNet, id string, java int, server []byte) []byte {
	t.Helper()
	sum := hexSum(SHA1, server)
	jarURL := "https://piston-data.mojang.com/v1/objects/" + sum + "/server.jar"
	f.serve(jarURL, server)
	b, err := json.Marshal(map[string]any{
		"id": id, "type": "release",
		"javaVersion": map[string]any{"component": "java-runtime-test", "majorVersion": java},
		"downloads":   map[string]any{"server": map[string]any{"sha1": sum, "size": len(server), "url": jarURL}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeServerJar is a Mojang bundler jar listing libs (path to content) in
// META-INF/libraries.list.
func fakeServerJar(t *testing.T, mc string, libs map[string]string) []byte {
	t.Helper()
	var list strings.Builder
	files := []string{"META-INF/MANIFEST.MF", "Manifest-Version: 1.0\r\nMain-Class: net.minecraft.bundler.Main\r\nBundler-Format: 1.0\r\n\r\n"}
	for _, p := range sortedKeys(libs) {
		list.WriteString(hexSum(SHA256, []byte(libs[p])) + "\tcom.example:" + p + "\t" + p + "\n")
		files = append(files, "META-INF/libraries/"+p, libs[p])
	}
	files = append(files, "META-INF/libraries.list", list.String(), "META-INF/versions/"+mc+"/server-"+mc+".jar", "server "+mc)
	return zipOf(t, files...)
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

func writeData(t *testing.T, dataDir, rel string, body []byte) {
	t.Helper()
	p := filepath.Join(dataDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}

func servePurpur(t *testing.T, f *fakeNet) {
	f.serve(purpurAPI, readFixture(t, "purpur/project.json"))
	for _, mc := range []string{"26.3", "26.2", "26.1.2", "1.21.11"} {
		f.serve(purpurAPI+"/"+mc, readFixture(t, "purpur/"+mc+".json"))
	}
	f.serve(purpurAPI+"/26.2/2633", readFixture(t, "purpur/26.2-2633.json"))
}

func serveNeoForgeMetadata(t *testing.T, f *fakeNet) {
	f.serve(neoforgeMaven+"/maven-metadata.xml", readFixture(t, "neoforge/maven-metadata.xml"))
}

// fakeLibrary is the content the fake Maven repositories serve for a
// library: a small jar, with a manifest for the loaders.
func fakeLibrary(t *testing.T, coord string) []byte {
	switch {
	case strings.HasPrefix(coord, "net.fabricmc:fabric-loader:"):
		return zipOf(t, "META-INF/MANIFEST.MF", "Manifest-Version: 1.0\r\nMain-Class: net.fabricmc.loader.impl.launch.server.FabricServerLauncher\r\nMulti-Release: true\r\n\r\nName: net/fabricmc/loader/Example.class\r\nSHA-256-Digest: AAAA\r\n\r\n",
			"net/fabricmc/loader/impl/launch/server/FabricServerLauncher.class", "loader "+coord)
	case strings.HasPrefix(coord, "org.quiltmc:quilt-loader:"):
		return zipOf(t, "META-INF/MANIFEST.MF", "Manifest-Version: 1.0\r\nMain-Class: net.fabricmc.loader.launch.server.FabricServerLauncher\r\n\r\n",
			"org/quiltmc/loader/impl/launch/server/QuiltServerLauncher.class", "loader "+coord)
	}
	return zipOf(t, "lib.txt", "library "+coord)
}

// serveLoaderProfile serves a Fabric or Quilt server profile and, for every
// library, a fake jar with its SHA-512 file on the library's Maven host.
// Hashes the profile lists are replaced by the fake jars' hashes unless
// keepListed is set.
func serveLoaderProfile(t *testing.T, f *fakeNet, profileURL string, profile []byte, keepListed bool) {
	t.Helper()
	body := editJSON(t, profile, func(v any) any {
		for _, l := range obj(v)["libraries"].([]any) {
			lib := l.(map[string]any)
			name := lib["name"].(string)
			rel, ok := mavenPath(name)
			if !ok {
				continue
			}
			jar := fakeLibrary(t, name)
			fileURL := strings.TrimSuffix(lib["url"].(string), "/") + "/" + rel
			f.serve(fileURL, jar)
			f.serve(fileURL+".sha512", []byte(hexSum(SHA512, jar)+"  "+filepath.Base(rel)+"\n"))
			if _, listed := lib["sha512"]; listed && !keepListed {
				lib["sha512"], lib["size"] = hexSum(SHA512, jar), len(jar)
			}
		}
		return v
	})
	f.serve(profileURL, body)
}

func serveFabric(t *testing.T, f *fakeNet, keepListed bool) {
	f.serve(fabricMeta+"/versions/game", readFixture(t, "fabric/game.json"))
	f.serve(fabricMeta+"/versions/loader", readFixture(t, "fabric/loader.json"))
	for _, mc := range []string{"26.3", "1.21.8"} {
		serveLoaderProfile(t, f, fabricMeta+"/versions/loader/"+mc+"/0.19.5/server/json",
			readFixture(t, "fabric/server-"+mc+"-0.19.5.json"), keepListed)
	}
}

func serveQuilt(t *testing.T, f *fakeNet) {
	f.serve(quiltMeta+"/versions/game", readFixture(t, "quilt/game.json"))
	f.serve(quiltMeta+"/versions/loader", readFixture(t, "quilt/loader.json"))
	for _, mc := range []string{"26.3", "1.21.8"} {
		serveLoaderProfile(t, f, quiltMeta+"/versions/loader/"+mc+"/0.30.1/server/json",
			readFixture(t, "quilt/server-"+mc+"-0.30.1.json"), false)
	}
}
