package webmap

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// testNow is the clock every test map reads.
var testNow = time.Date(2026, 9, 25, 16, 0, 0, 0, time.UTC)

// fileTime is when the fake's files were written; fileETag is the ETag
// Undertow makes of it.
var (
	fileTime = time.Date(2026, 9, 25, 15, 58, 0, 0, time.UTC)
	fileETag = `"` + strconv.FormatInt(fileTime.UnixMilli(), 10) + `"`
)

// playersETag is what squaremap's JsonCache sends for players.json: the
// time of the last change, unquoted.
const playersETag = "1790351880000"

const overworldTile = "/tiles/minecraft_overworld/3/0_0.png"

type seenRequest struct {
	Method, Path, RawQuery string
	Header                 http.Header
}

// fakeSquaremap answers like squaremap's internal web server (Undertow):
// files under /tiles come from its web folder, with an ETag and
// Last-Modified from the file's time and 200 with no body for a tile it has
// not drawn; players.json comes from memory, as its JsonCache serves it.
type fakeSquaremap struct {
	mu       sync.Mutex
	files    map[string][]byte
	players  []byte
	extra    http.Header
	handler  http.HandlerFunc
	requests []seenRequest
}

// startFakeSquaremap serves the testdata answers and returns a Map for a
// running Paper server pointing at them.
func startFakeSquaremap(t *testing.T) (*fakeSquaremap, Map) {
	t.Helper()
	f := &fakeSquaremap{files: map[string][]byte{}}
	f.files["/tiles/settings.json"] = readTestdata(t, "squaremap/settings.json")
	for _, w := range []string{"minecraft_overworld", "minecraft_the_nether", "minecraft_the_end", "terralith_overworld"} {
		f.files["/tiles/"+w+"/settings.json"] = readTestdata(t, "squaremap/"+w+"-settings.json")
	}
	f.files[overworldTile] = tilePNG(t, 1)
	f.files["/tiles/minecraft_overworld/0/-1_0.png"] = tilePNG(t, 2)
	f.players = readTestdata(t, "squaremap/players.json")
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	client := NewClient()
	t.Cleanup(client.CloseIdleConnections)
	return f, Map{Type: "paper", Addr: srv.Listener.Addr().String(), Client: client, Now: func() time.Time { return testNow }}
}

func (f *fakeSquaremap) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, seenRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone()})
	body, isFile := f.files[r.URL.Path]
	players, extra, handler := f.players, f.extra, f.handler
	f.mu.Unlock()
	for k, v := range extra {
		w.Header()[k] = v
	}
	h := w.Header()
	switch {
	case handler != nil:
		handler(w, r)
	case r.URL.Path == "/tiles/players.json" && players != nil:
		h.Set("Content-Type", "application/json")
		h.Set("ETag", playersETag)
		if r.Header.Get("If-None-Match") == playersETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Write(players)
	case !isFile:
		if strings.HasPrefix(r.URL.Path, "/tiles/") && strings.HasSuffix(r.URL.Path, ".png") {
			h.Set("Cache-Control", "max-age=0, must-revalidate, no-cache")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	default:
		h.Set("Cache-Control", "max-age=0, must-revalidate, no-cache")
		h.Set("Content-Type", map[string]string{".png": "image/png", ".json": "application/json"}[path.Ext(r.URL.Path)])
		h.Set("ETag", fileETag)
		h.Set("Last-Modified", fileTime.Format(http.TimeFormat))
		since, err := http.ParseTime(r.Header.Get("If-Modified-Since"))
		if r.Header.Get("If-None-Match") == fileETag || err == nil && !fileTime.After(since) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method != http.MethodHead {
			w.Write(body)
		}
	}
}

func (f *fakeSquaremap) setFile(p, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[p] = []byte(body)
}

func (f *fakeSquaremap) deleteFile(p string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, p)
}

func (f *fakeSquaremap) file(p string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files[p]
}

// setPlayers replaces players.json; nil makes squaremap not have it yet.
func (f *fakeSquaremap) setPlayers(body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.players = body
}

func (f *fakeSquaremap) setHandler(h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = h
}

func (f *fakeSquaremap) seen() []seenRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seenRequest(nil), f.requests...)
}

func (f *fakeSquaremap) paths() []string {
	var out []string
	for _, r := range f.seen() {
		out = append(out, r.Path)
	}
	return out
}

// closedAddr is an address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeTestdata(t *testing.T, name string, v any) {
	t.Helper()
	if err := json.Unmarshal(readTestdata(t, name), v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// tilePNG draws a tile like squaremap's: 512×512 pixels, mostly
// transparent where nothing is explored.
func tilePNG(t *testing.T, shade uint8) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, TileSize, TileSize))
	for y := 0; y < TileSize/2; y++ {
		for x := 0; x < TileSize; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 40 * shade, G: 120 + uint8(x%16), B: 60, A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// serveMap sends one request through the proxy, mounted under /map as the
// agent and panel mount it.
func serveMap(m Map, method, target string, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		r.Header[k] = v
	}
	w := httptest.NewRecorder()
	http.StripPrefix("/map", m).ServeHTTP(w, r)
	return w
}

// apiErrorOf reads the proxy's error answer, in the API's error shape.
func apiErrorOf(t *testing.T, w *httptest.ResponseRecorder) api.Error {
	t.Helper()
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("error answer has type %q: %s", ct, w.Body)
	}
	var e api.Error
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("error answer %q: %v", w.Body, err)
	}
	if e.Error == "" || e.Code == "" {
		t.Errorf("error answer without a message or code: %s", w.Body)
	}
	return e
}
