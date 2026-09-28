package service

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/usage"
)

// The dashboard is the service's own page: it loads nothing from anywhere
// else, asks only for the summary, holds no numbers itself and sets no
// cookie.
func TestTheDashboardLoadsOnlyItsOwnFilesAndAsksOnlyForTheSummary(t *testing.T) {
	e := newEnv(t)
	e.send(usage.PathHeartbeat, beat(1, usage.SourceSite, 3, 2))
	bodies := map[string]string{}
	for p, contentType := range map[string]string{"/dashboard": "text/html", "/dashboard/app.js": "text/javascript", "/dashboard/app.css": "text/css", "/dashboard/icon.svg": "image/svg+xml"} {
		w := e.do("GET", p, "198.51.100.60", nil)
		if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), contentType) {
			t.Fatalf("%s: %d %q", p, w.Code, w.Header().Get("Content-Type"))
		}
		csp := w.Header().Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "connect-src 'self'", "form-action 'none'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: the Content-Security-Policy %q has no %q", p, csp, want)
			}
		}
		if strings.Contains(csp, "unsafe") || strings.Contains(csp, "http") || strings.Contains(csp, "*") {
			t.Errorf("%s: the Content-Security-Policy %q lets in more than the service", p, csp)
		}
		h := w.Header()
		if h.Get("Set-Cookie") != "" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "no-referrer" || h.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: headers %v", p, h)
		}
		if strings.Contains(w.Body.String(), id(1)) {
			t.Errorf("%s holds an install ID", p)
		}
		bodies[p] = w.Body.String()
	}

	page := bodies["/dashboard"]
	loads := regexp.MustCompile(`<(?:script|link|img)\b[^>]*?\b(?:src|href)="([^"]*)"`).FindAllStringSubmatch(page, -1)
	if len(loads) < 3 {
		t.Fatalf("the page loads %d files, so the check below proves nothing", len(loads))
	}
	for _, m := range loads {
		if _, ok := dashboardFiles[m[1]]; !ok {
			t.Errorf("the page loads %s, which isn't one of the dashboard's files", m[1])
		}
	}
	if regexp.MustCompile(`<style|<script>|<script [^>]*>[^<]|\sstyle=|\son[a-z]+=`).MatchString(page) {
		t.Error("the page has inline script or style, which its Content-Security-Policy blocks")
	}

	js := bodies["/dashboard/app.js"]
	fetches := regexp.MustCompile("fetch\\(\\s*['\"`]([^'\"`]*)").FindAllStringSubmatch(js, -1)
	if len(fetches) == 0 {
		t.Fatal("the script fetches nothing, so the check below proves nothing")
	}
	for _, m := range fetches {
		if m[1] != "/v1/summary" {
			t.Errorf("the script fetches %s", m[1])
		}
	}
	for _, banned := range []string{"XMLHttpRequest", "sendBeacon", "WebSocket", "EventSource", "import(", "innerHTML", "outerHTML", "insertAdjacentHTML", "document.cookie", "eval(", "new Function"} {
		if strings.Contains(js, banned) {
			t.Errorf("the script uses %s", banned)
		}
	}
	if !strings.Contains(js, "Authorization: 'Bearer ' + token") {
		t.Error("the script doesn't send the token as a bearer token")
	}

	w := e.do("GET", "/dashboard", "198.51.100.60", nil, "If-None-Match", e.do("GET", "/dashboard", "198.51.100.60", nil).Header().Get("ETag"))
	if w.Code != http.StatusNotModified {
		t.Errorf("an unchanged page answered %d", w.Code)
	}
	if w := e.do("HEAD", "/dashboard/app.js", "198.51.100.60", nil); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("HEAD: %d with %d bytes", w.Code, w.Body.Len())
	}
	for _, p := range []string{"/dashboard/", "/dashboard/index.html", "/dashboard/../stats.db", "/dashboard/app.js.map"} {
		if w := e.do("GET", p, "198.51.100.60", nil); w.Code == http.StatusOK {
			t.Errorf("%s answered 200", p)
		}
	}
	if w := e.do("GET", "/v1/summary", "198.51.100.60", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("the summary without the token answered %d", w.Code)
	}
}
