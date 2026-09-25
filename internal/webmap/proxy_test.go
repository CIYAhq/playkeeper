package webmap

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const tileTarget = "/map" + overworldTile

func TestProxyServesTiles(t *testing.T) {
	f, m := startFakeSquaremap(t)
	for _, p := range []string{overworldTile, "/tiles/minecraft_overworld/0/-1_0.png"} {
		w := serveMap(m, "GET", "/map"+p, nil)
		want := f.file(p)
		if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), want) {
			t.Fatalf("%s: %d, %d bytes", p, w.Code, w.Body.Len())
		}
		for k, v := range map[string]string{
			"Content-Type":           "image/png",
			"Content-Length":         strconv.Itoa(len(want)),
			"Cache-Control":          "private, max-age=30",
			"ETag":                   fileETag,
			"Last-Modified":          fileTime.Format(http.TimeFormat),
			"X-Content-Type-Options": "nosniff",
		} {
			if got := w.Header().Get(k); got != v {
				t.Errorf("%s: %s is %q, want %q", p, k, got, v)
			}
		}
	}
	for _, r := range f.seen() {
		if r.Method != "GET" || !strings.HasPrefix(r.Path, "/tiles/minecraft_overworld/") {
			t.Errorf("squaremap was asked %s %s", r.Method, r.Path)
		}
	}
}

func TestProxyAnswersHeadRequestsWithoutABody(t *testing.T) {
	f, m := startFakeSquaremap(t)
	for target, status := range map[string]int{
		tileTarget:     http.StatusOK,
		"/map/worlds":  http.StatusOK,
		"/map/players": http.StatusOK,
		"/map/tiles/minecraft_overworld/3/5_5.png": http.StatusNotFound,
		"/map/index.html":                          http.StatusNotFound,
	} {
		w := serveMap(m, "HEAD", target, nil)
		if w.Code != status || w.Body.Len() != 0 || w.Header().Get("Content-Length") == "" {
			t.Errorf("HEAD %s: %d, %d bytes, length %q", target, w.Code, w.Body.Len(), w.Header().Get("Content-Length"))
		}
	}
	for _, r := range f.seen() {
		if r.Method != "GET" {
			t.Errorf("squaremap was asked %s %s", r.Method, r.Path)
		}
	}
}

func TestProxyPassesConditionalTileRequests(t *testing.T) {
	for _, h := range []http.Header{
		{"If-None-Match": {fileETag}},
		{"If-Modified-Since": {fileTime.Format(http.TimeFormat)}},
	} {
		f, m := startFakeSquaremap(t)
		w := serveMap(m, "GET", tileTarget, h)
		if w.Code != http.StatusNotModified || w.Body.Len() != 0 || w.Header().Get("ETag") != fileETag || w.Header().Get("Cache-Control") != tileCache {
			t.Errorf("%v: %d %v", h, w.Code, w.Header())
		}
		for k := range h {
			if got := f.seen()[0].Header.Get(k); got != h.Get(k) {
				t.Errorf("squaremap got %s %q", k, got)
			}
		}
	}
}

func TestProxyTellsATileIsNotDrawnYet(t *testing.T) {
	f, m := startFakeSquaremap(t)
	check := func(why string) {
		t.Helper()
		w := serveMap(m, "GET", "/map/tiles/minecraft_overworld/3/7_-2.png", nil)
		if e := apiErrorOf(t, w); w.Code != http.StatusNotFound || e.Code != string(KindNotDrawn) || w.Header().Get("Cache-Control") != tileCache {
			t.Errorf("%s: %d %s %v", why, w.Code, w.Body, w.Header())
		}
	}
	check("squaremap answers 200 with no body")
	f.setHandler(http.NotFound)
	check("squaremap answers 404")
}

func TestProxyRefusesEverythingElse(t *testing.T) {
	f, m := startFakeSquaremap(t)
	for _, target := range []string{
		"/map", "/map/", "/map/index.html", "/map/js/main.js", "/map/images/icon/registered/x.png",
		"/map/tiles/settings.json", "/map/tiles/players.json",
		"/map/tiles/minecraft_overworld/settings.json", "/map/tiles/minecraft_overworld/markers.json",
		"/map/worlds/", "/map/players/Notch", "/map//worlds", "/map/WORLDS",
		"/map/tiles/minecraft_overworld/3/0_0.jpg",
		"/map/tiles/minecraft_overworld/3/0_0.PNG",
		"/map/tiles/minecraft_overworld/3/0_0.png/",
		"/map/tiles/minecraft_overworld/3/0_0.png.png",
		"/map/tiles/minecraft_overworld/3/0_0.png;jsessionid=x",
		"/map/tiles/minecraft_overworld/3/0_0",
		"/map/tiles/minecraft_overworld/3/0-0.png",
		"/map/tiles/minecraft_overworld/3/00_0.png",
		"/map/tiles/minecraft_overworld/3/-0_0.png",
		"/map/tiles/minecraft_overworld/3/+1_0.png",
		"/map/tiles/minecraft_overworld/3/12345678_0.png",
		"/map/tiles/minecraft_overworld/4/0_0.png",
		"/map/tiles/minecraft_overworld/-1/0_0.png",
		"/map/tiles/minecraft_overworld/03/0_0.png",
		"/map/tiles/minecraft_overworld/x/0_0.png",
		"/map/tiles/Minecraft_Overworld/3/0_0.png",
		"/map/tiles/.hidden/3/0_0.png",
		"/map/tiles/" + strings.Repeat("a", 101) + "/3/0_0.png",
		"/map/tiles/../settings.json",
		"/map/tiles/../tiles/minecraft_overworld/3/0_0.png",
		"/map/tiles/minecraft_overworld/../../index.html/0_0.png",
		"/map/tiles/../../../etc/3/0_0.png",
		"/map/tiles/..%2fsettings.json/3/0_0.png",
		"/map/tiles/%2e%2e/3/0_0.png",
		"/map/tiles/.%2e/3/0_0.png",
		"/map/tiles/minecraft_overworld%2f..%2fminecraft_the_nether/3/0_0.png",
		"/map/tiles/minecraft_overworld/3/..%2f..%2fsettings.json",
		"/map/tiles/minecraft_overworld%00/3/0_0.png",
		"/map/tiles/minecraft_overworld/3/0_0.png%00.json",
		"/map/tiles/minecraft_overworld/3/0_0.png%3f.json",
		"/map/tiles/minecraft..overworld/3/0_0.png",
	} {
		w := serveMap(m, "GET", target, nil)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", target, w.Code)
		} else if e := apiErrorOf(t, w); e.Code != string(KindNotFound) || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %s %v", target, w.Body, w.Header())
		}
	}
	if paths := f.paths(); len(paths) != 0 {
		t.Errorf("squaremap was asked for %v", paths)
	}
}

func TestProxyForwardsNoQuery(t *testing.T) {
	f, m := startFakeSquaremap(t)
	for _, target := range []string{tileTarget + "?url=http://example.invalid/", "/map/worlds?x=../settings.json", "/map/players?callback=x"} {
		if w := serveMap(m, "GET", target, nil); w.Code != http.StatusOK {
			t.Errorf("%s: %d", target, w.Code)
		}
	}
	for _, r := range f.seen() {
		if r.RawQuery != "" {
			t.Errorf("squaremap was asked %s?%s", r.Path, r.RawQuery)
		}
	}
}

func TestProxyAnswersOnlyGetAndHead(t *testing.T) {
	f, m := startFakeSquaremap(t)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS", "TRACE", "PROPFIND"} {
		for _, target := range []string{tileTarget, "/map/worlds", "/map/players"} {
			w := serveMap(m, method, target, nil)
			if e := apiErrorOf(t, w); w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" || e.Code != string(KindMethod) {
				t.Errorf("%s %s: %d %v %s", method, target, w.Code, w.Header(), w.Body)
			}
		}
	}
	if paths := f.paths(); len(paths) != 0 {
		t.Errorf("squaremap was asked for %v", paths)
	}
}

func TestProxyRefusesOversizedAnswers(t *testing.T) {
	bigTile := append(append([]byte(nil), pngSignature...), make([]byte, maxTileBytes)...)
	bigJSON := []byte(`{"worlds":[],"players":[],"pad":"` + strings.Repeat("a", maxJSONBytes) + `"}`)
	withLength := func(ct string, b []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Content-Length", strconv.Itoa(len(b)))
			w.Write(b)
		}
	}
	chunked := func(ct string, b []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ct)
			w.(http.Flusher).Flush()
			w.Write(b)
		}
	}
	for _, tc := range []struct {
		name, target string
		handler      http.HandlerFunc
	}{
		{"tile with its length", tileTarget, withLength("image/png", bigTile)},
		{"tile without a length", tileTarget, chunked("image/png", bigTile)},
		{"world list with its length", "/map/worlds", withLength("application/json", bigJSON)},
		{"world list without a length", "/map/worlds", chunked("application/json", bigJSON)},
		{"players", "/map/players", chunked("application/json", bigJSON)},
	} {
		f, m := startFakeSquaremap(t)
		f.setHandler(tc.handler)
		w := serveMap(m, "GET", tc.target, nil)
		if e := apiErrorOf(t, w); w.Code != http.StatusBadGateway || e.Code != string(KindBadAnswer) || !strings.Contains(e.Error, "more than") {
			t.Errorf("%s: %d %s", tc.name, w.Code, w.Body)
		}
	}
}

func TestProxyRefusesAnswersOfTheWrongType(t *testing.T) {
	html := readTestdata(t, "squaremap/spa-fallback.html")
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`)
	players := readTestdata(t, "squaremap/players.json")
	answer := func(ct string, b []byte) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if ct == "" {
				w.Header()["Content-Type"] = nil
			} else {
				w.Header().Set("Content-Type", ct)
			}
			w.Write(b)
		}
	}
	for _, tc := range []struct {
		name, target, reason string
		handler              http.HandlerFunc
	}{
		{"tile as a web page", tileTarget, "a tile that is not a PNG image", answer("text/html; charset=utf-8", html)},
		{"tile as SVG", tileTarget, "a tile that is not a PNG image", answer("image/svg+xml", svg)},
		{"web page labelled PNG", tileTarget, "a tile that is not a PNG image", answer("image/png", html)},
		{"tile without a type", tileTarget, "a tile that is not a PNG image", answer("", tilePNG(t, 1))},
		{"world list as a web page", "/map/worlds", "not JSON", answer("text/html; charset=utf-8", html)},
		{"world list cut short", "/map/worlds", "unreadable JSON", answer("application/json", []byte(`{"worlds":[`))},
		{"world list of the wrong shape", "/map/worlds", "unreadable JSON", answer("application/json", []byte(`{"worlds":{"name":"x"}}`))},
		{"players as text", "/map/players", "not JSON", answer("text/plain", players)},
		{"players as JavaScript", "/map/players", "not JSON", answer("application/javascript", players)},
	} {
		f, m := startFakeSquaremap(t)
		f.setHandler(tc.handler)
		w := serveMap(m, "GET", tc.target, nil)
		e := apiErrorOf(t, w)
		if w.Code != http.StatusBadGateway || e.Code != string(KindBadAnswer) || !strings.Contains(e.Error, "("+tc.reason+")") || e.Hint == "" {
			t.Errorf("%s: %d %s", tc.name, w.Code, w.Body)
		}
		for _, s := range []string{"doctype", "onload", "Notch"} {
			if strings.Contains(w.Body.String(), s) {
				t.Errorf("%s: squaremap's answer reached the viewer: %s", tc.name, w.Body)
			}
		}
	}
}

func TestProxyDoesNotFollowRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
	}))
	defer other.Close()
	f, m := startFakeSquaremap(t)
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/evil.png", http.StatusFound)
	})
	for _, target := range []string{tileTarget, "/map/worlds", "/map/players"} {
		w := serveMap(m, "GET", target, nil)
		if e := apiErrorOf(t, w); w.Code != http.StatusBadGateway || e.Code != string(KindBadAnswer) || w.Header().Get("Location") != "" {
			t.Errorf("%s: %d %v %s", target, w.Code, w.Header(), w.Body)
		}
	}
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("followed %d redirects", n)
	}
}

func TestProxyGivesUpOnASlowSquaremap(t *testing.T) {
	wait := func(r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}
	for _, tc := range []struct {
		name, target string
		handler      http.HandlerFunc
	}{
		{"no answer", tileTarget, func(w http.ResponseWriter, r *http.Request) { wait(r) }},
		{"half a tile", tileTarget, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			w.Write(pngSignature)
			w.(http.Flusher).Flush()
			wait(r)
		}},
		{"no world list", "/map/worlds", func(w http.ResponseWriter, r *http.Request) { wait(r) }},
		{"half the players", "/map/players", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"players":[`))
			w.(http.Flusher).Flush()
			wait(r)
		}},
	} {
		f, m := startFakeSquaremap(t)
		m.Timeout = 50 * time.Millisecond
		f.setHandler(tc.handler)
		start := time.Now()
		w := serveMap(m, "GET", tc.target, nil)
		if e := apiErrorOf(t, w); w.Code != http.StatusGatewayTimeout || e.Code != string(KindTooSlow) {
			t.Errorf("%s: %d %s", tc.name, w.Code, w.Body)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%s: gave up after %v", tc.name, d)
		}
	}
}

func TestProxyWhenSquaremapIsNotThere(t *testing.T) {
	for _, tc := range []struct {
		addr   string
		status int
		kind   Kind
	}{
		{closedAddr(t), http.StatusBadGateway, KindNotAnswering},
		{"", http.StatusServiceUnavailable, KindNotRunning},
		{"localhost:25580", http.StatusServiceUnavailable, KindNotRunning},
	} {
		m := Map{Type: "paper", Addr: tc.addr, Client: NewClient()}
		for _, target := range []string{tileTarget, "/map/worlds", "/map/players"} {
			w := serveMap(m, "GET", target, nil)
			if e := apiErrorOf(t, w); w.Code != tc.status || e.Code != string(tc.kind) || e.Hint == "" {
				t.Errorf("%q %s: %d %s", tc.addr, target, w.Code, w.Body)
			}
		}
	}
}

func TestProxyTellsTheMapIsStartingBeforeItListsWorlds(t *testing.T) {
	f, m := startFakeSquaremap(t)
	f.deleteFile("/tiles/settings.json")
	for _, target := range []string{"/map/worlds", "/map/players"} {
		w := serveMap(m, "GET", target, nil)
		if e := apiErrorOf(t, w); w.Code != http.StatusServiceUnavailable || e.Code != string(KindStarting) {
			t.Errorf("%s: %d %s", target, w.Code, w.Body)
		}
	}
}

func TestProxyPassesNothingOfTheViewerToSquaremap(t *testing.T) {
	f, m := startFakeSquaremap(t)
	viewer := http.Header{
		"Cookie":              {"playkeeper_session=secret-session"},
		"Authorization":       {"Bearer secret-token"},
		"Proxy-Authorization": {"Basic c2VjcmV0"},
		"X-Forwarded-For":     {"203.0.113.9"},
		"X-Real-Ip":           {"203.0.113.9"},
		"Forwarded":           {"for=203.0.113.9"},
		"Referer":             {"https://play.example.com/map/survival"},
		"Origin":              {"https://play.example.com"},
		"User-Agent":          {"Mozilla/5.0 (viewer)"},
		"Accept-Language":     {"de-DE"},
		"X-Csrf-Token":        {"secret-csrf"},
		"If-None-Match":       {fileETag},
	}
	for _, target := range []string{tileTarget, "/map/worlds", "/map/players"} {
		serveMap(m, "GET", target, viewer)
	}
	allowed := []string{"User-Agent", "Accept", "Accept-Encoding", "If-None-Match", "If-Modified-Since"}
	for _, r := range f.seen() {
		for k := range r.Header {
			if !slices.Contains(allowed, k) {
				t.Errorf("%s: squaremap got %s: %q", r.Path, k, r.Header[k])
			}
		}
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("%s: squaremap got User-Agent %q", r.Path, r.Header.Get("User-Agent"))
		}
		if s := fmt.Sprint(r.Header); strings.Contains(s, "secret") || strings.Contains(s, "203.0.113.9") || strings.Contains(s, "play.example.com") {
			t.Errorf("%s: squaremap got %s", r.Path, s)
		}
		if r.Path != overworldTile && r.Header.Get("If-None-Match") != "" {
			t.Errorf("%s: squaremap got a conditional request for JSON", r.Path)
		}
	}
}

func TestProxyPassesOnlyValidatorsOfSquaremapsHeaders(t *testing.T) {
	f, m := startFakeSquaremap(t)
	f.extra = http.Header{
		"Set-Cookie":                  {"JSESSIONID=abc; Path=/"},
		"Access-Control-Allow-Origin": {"*"},
		"Server":                      {"Undertow"},
		"X-Powered-By":                {"Undertow/1"},
		"Link":                        {"<https://mc-heads.net/>; rel=preconnect"},
		"Refresh":                     {"0; url=https://example.invalid/"},
		"Content-Security-Policy":     {"default-src *"},
		"Content-Disposition":         {"attachment; filename=x.html"},
	}
	allowed := []string{"Content-Type", "Content-Length", "Cache-Control", "Etag", "Last-Modified", "X-Content-Type-Options"}
	for _, target := range []string{tileTarget, "/map/tiles/minecraft_overworld/3/9_9.png", "/map/worlds", "/map/players"} {
		w := serveMap(m, "GET", target, nil)
		for k := range w.Header() {
			if !slices.Contains(allowed, k) {
				t.Errorf("%s: the viewer got %s: %q", target, k, w.Header()[k])
			}
		}
	}
}

func TestProxyDropsOverlongValidators(t *testing.T) {
	f, m := startFakeSquaremap(t)
	long := `"` + strings.Repeat("a", 250) + `"`
	tile := tilePNG(t, 1)
	f.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("ETag", long)
		w.Write(tile)
	})
	w := serveMap(m, "GET", tileTarget, http.Header{"If-None-Match": {long}})
	if w.Code != http.StatusOK || w.Header().Get("ETag") != "" {
		t.Errorf("%d %v", w.Code, w.Header())
	}
	if got := f.seen()[0].Header.Get("If-None-Match"); got != "" {
		t.Errorf("squaremap got If-None-Match %q", got)
	}
}
