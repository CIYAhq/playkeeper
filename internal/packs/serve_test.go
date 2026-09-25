package packs

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// servedStore is a store holding one 100-byte pack, returned with its hash
// and contents.
func servedStore(t *testing.T) (Store, string, []byte) {
	t.Helper()
	body := []byte(strings.Repeat("0123456789", 10))
	h := sha1.Sum(body)
	sum := hex.EncodeToString(h[:])
	s := Store{Dir: t.TempDir()}
	writeFile(t, filepath.Join(s.Dir, sum+".zip"), string(body))
	return s, sum, body
}

func serve(h http.Handler, method, target string, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		r.Header[k] = v
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPackHandler(t *testing.T) {
	s, sum, body := servedStore(t)
	var asked []string
	h := NewHandler(s, func(got string) bool {
		asked = append(asked, got)
		return got == sum
	})
	path := PackPath(sum)
	if path != "/resource-packs/"+sum+".zip" {
		t.Errorf("PackPath = %q", path)
	}

	w := serve(h, http.MethodGet, path, nil)
	if w.Code != http.StatusOK || w.Body.String() != string(body) {
		t.Fatalf("GET = %d %q", w.Code, w.Body)
	}
	for k, want := range map[string]string{
		"Content-Type":           "application/zip",
		"Content-Length":         "100",
		"ETag":                   `"` + sum + `"`,
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
		"Content-Disposition":    `attachment; filename="` + sum + `.zip"`,
		"Accept-Ranges":          "bytes",
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if w.Header().Get("Last-Modified") == "" {
		t.Error("no Last-Modified")
	}
	if !slices.Equal(asked, []string{sum}) {
		t.Errorf("asked whether %q are active, want only the pack requested", asked)
	}

	w = serve(h, http.MethodHead, path, nil)
	if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "100" {
		t.Errorf("HEAD = %d, %d bytes, Content-Length %q", w.Code, w.Body.Len(), w.Header().Get("Content-Length"))
	}
	for _, tc := range []struct {
		header http.Header
		code   int
		body   string
	}{
		{http.Header{"Range": {"bytes=10-19"}}, http.StatusPartialContent, string(body[10:20])},
		{http.Header{"Range": {"bytes=90-"}}, http.StatusPartialContent, string(body[90:])},
		{http.Header{"Range": {"bytes=200-"}}, http.StatusRequestedRangeNotSatisfiable, ""},
		{http.Header{"If-None-Match": {`"` + sum + `"`}}, http.StatusNotModified, ""},
		{http.Header{"If-Range": {`"` + sum + `"`}, "Range": {"bytes=0-9"}}, http.StatusPartialContent, string(body[:10])},
		{http.Header{"If-Range": {`"changed"`}, "Range": {"bytes=0-9"}}, http.StatusOK, string(body)},
	} {
		w := serve(h, http.MethodGet, path, tc.header)
		if w.Code != tc.code || tc.body != "" && w.Body.String() != tc.body {
			t.Errorf("GET with %v = %d %q, want %d %q", tc.header, w.Code, w.Body, tc.code, tc.body)
		}
	}
	if w := serve(h, http.MethodGet, path, http.Header{"Range": {"bytes=10-19"}}); w.Header().Get("Content-Range") != "bytes 10-19/100" {
		t.Errorf("Content-Range = %q", w.Header().Get("Content-Range"))
	}

	other := strings.Repeat("ab", 20)
	for _, target := range []string{
		"/", "/resource-packs", "/resource-packs/", "/resource-packs/" + sum, "/resource-packs/" + sum + ".zip/", "/resource-packs/" + sum + ".ZIP",
		"/resource-packs/" + strings.ToUpper(sum) + ".zip", "/resource-packs/" + sum + ".zip.zip", "/resource-packs//" + sum + ".zip",
		"/resource-packs/../resource-packs/" + sum + ".zip", "/resource-packs/%2e%2e/" + sum + ".zip", "/" + sum + ".zip",
		"/resource-packs/x/" + sum + ".zip", "/resource-packs/" + sum[:39] + ".zip", "/resource-packs/" + other + ".zip",
		"/packs/" + sum + ".zip",
	} {
		if w := serve(h, http.MethodGet, target, nil); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, w.Code)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		if w := serve(h, method, path, nil); w.Code != http.StatusNotFound || w.Header().Get("Allow") != "" {
			t.Errorf("%s = %d, want 404", method, w.Code)
		}
	}

	always := func(string) bool { return true }
	missingDir := Store{Dir: filepath.Join(t.TempDir(), "missing")}
	folder := Store{Dir: t.TempDir()}
	if err := os.Mkdir(filepath.Join(folder.Dir, sum+".zip"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]http.Handler{
		"no active packs":    NewHandler(s, nil),
		"an inactive pack":   NewHandler(s, func(string) bool { return false }),
		"a missing store":    NewHandler(missingDir, always),
		"a missing pack":     NewHandler(Store{Dir: t.TempDir()}, always),
		"a folder as a pack": NewHandler(folder, always),
	} {
		if w := serve(h, http.MethodGet, path, nil); w.Code != http.StatusNotFound {
			t.Errorf("%s: GET = %d, want 404", name, w.Code)
		}
	}
}

func TestPlainHandler(t *testing.T) {
	packs := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Packs", "yes")
		io.WriteString(w, r.URL.Path)
	})
	h := NewPlainHandler(packs)
	local := &net.TCPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 8443}
	request := func(method, target, host string, localAddr net.Addr) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, nil)
		r.Host = host
		if localAddr != nil {
			r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, localAddr))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	for _, target := range []string{PackPath(testSum), "/resource-packs/anything"} {
		if w := request(http.MethodGet, target, "mc.example.com:8443", local); w.Code != http.StatusOK || w.Header().Get("X-Packs") != "yes" || w.Body.String() != target {
			t.Errorf("GET %s = %d %q, want it passed to the pack handler", target, w.Code, w.Body)
		}
	}

	for _, tc := range []struct {
		method, target, host string
		local                net.Addr
		location             string
	}{
		{http.MethodGet, "/", "mc.example.com:8443", local, "https://mc.example.com:8443/"},
		{http.MethodGet, "/servers/1?tab=world", "mc.example.com:8443", nil, "https://mc.example.com:8443/servers/1?tab=world"},
		{http.MethodGet, "/", "mc.example.com", local, "https://mc.example.com:8443/"},
		{http.MethodGet, "/", "mc.example.com:443", nil, "https://mc.example.com/"},
		{http.MethodGet, "/", "203.0.113.7:8443", nil, "https://203.0.113.7:8443/"},
		{http.MethodGet, "/", "[2001:db8::1]:8443", nil, "https://[2001:db8::1]:8443/"},
		{http.MethodGet, "/", "[2001:db8::1]", local, "https://[2001:db8::1]:8443/"},
		{http.MethodGet, "/", "Game_1.example:8443", nil, "https://Game_1.example:8443/"},
		{http.MethodGet, "/resource-packs", "mc.example.com:8443", nil, "https://mc.example.com:8443/resource-packs"},
		{http.MethodGet, "//evil.example/x", "mc.example.com:8443", nil, "https://mc.example.com:8443//evil.example/x"},
		{http.MethodPost, "/api/login", "mc.example.com:8443", nil, "https://mc.example.com:8443/api/login"},
	} {
		w := request(tc.method, tc.target, tc.host, tc.local)
		if w.Code != http.StatusPermanentRedirect || w.Header().Get("Location") != tc.location {
			t.Errorf("%s %s to %q = %d %q, want 308 %q", tc.method, tc.target, tc.host, w.Code, w.Header().Get("Location"), tc.location)
		}
	}

	for _, tc := range []struct {
		host  string
		local net.Addr
	}{
		{"", nil}, {"mc.example.com", nil}, {"mc.example.com:0", nil}, {"mc.example.com:65536", nil},
		{"mc.example.com:08443", nil}, {"mc.example.com:+8443", nil}, {"mc.example.com:http", nil},
		{"evil.example/x:8443", nil}, {"user@mc.example.com:8443", nil}, {"mc..example.com:8443", nil},
		{".example.com:8443", nil}, {"exa mple.com:8443", nil}, {"[fe80::1%25eth0]:8443", nil},
		{"mc.example.com:8443:1", local}, {strings.Repeat("a", 254) + ":8443", nil},
	} {
		w := request(http.MethodGet, "/", tc.host, tc.local)
		if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
			t.Errorf("GET / to %q = %d %q, want 400", tc.host, w.Code, w.Header().Get("Location"))
		}
	}
}
