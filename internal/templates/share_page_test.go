package templates

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The share page on playkeeper.io, site/pages/t.html and site/static/js/t.js,
// reads links in the browser. These checks keep it in step with this package,
// and keep the template in the browser: the page reads it from the address
// after # only, makes no requests, and shows it only as text.
// internal/site's tests check the page as built, and scripts/site-check.sh
// opens it in a browser.

func sitePage(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "site", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func matches(pattern, s string) []string {
	var out []string
	for _, m := range regexp.MustCompile(pattern).FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestSharePageReadsLinksAsThisPackage(t *testing.T) {
	js := sitePage(t, "static/js/t.js")
	for _, want := range []string{
		fmt.Sprintf("var MAX_LINK = %d;", MaxLinkLength),
		fmt.Sprintf("var MAX_JSON = %d;", MaxFileSize),
		fmt.Sprintf("var LINK_FORMAT = %d;", linkVersion),
		fmt.Sprintf("var FORMAT = %d;", Format),
		fmt.Sprintf("raw.length < %d", headerSize),
		fmt.Sprintf("raw.subarray(%d)", headerSize),
		fmt.Sprintf("text(t.name, %d)", maxName),
		fmt.Sprintf("text(t.description, %d)", maxDescription),
		fmt.Sprintf("text(a.name, %d)", maxLabel),
		fmt.Sprintf("text(p.name, %d)", maxLabel),
		fmt.Sprintf("text(t.modpack.name, %d)", maxLabel),
		"new DecompressionStream('deflate-raw')",
		"crypto.subtle.digest('SHA-256', body)",
		"replace(/^template=/, '')",
		"window.location.assign(origin + '/servers/new#template=' + payload)",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("site/static/js/t.js does not have %q", want)
		}
	}
	if html := sitePage(t, "pages/t.html"); !strings.Contains(html, ShareURL+"#") {
		t.Errorf("site/pages/t.html does not say that template links start with %s#", ShareURL)
	}
}

func TestSharePageKeepsTheTemplateInTheBrowser(t *testing.T) {
	js := sitePage(t, "static/js/t.js")
	for _, bad := range []string{
		"fetch(", "XMLHttpRequest", "sendBeacon", "WebSocket", "EventSource", "import(", "Image(", ".src",
		"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "createElement",
		"eval(", "Function(", "setTimeout", "setInterval",
		"document.URL", "document.referrer", "document.cookie", "sessionStorage", "window.open", "postMessage",
	} {
		if strings.Contains(js, bad) {
			t.Errorf("site/static/js/t.js has %q", bad)
		}
	}
	for _, c := range []struct{ pattern, allowed string }{
		{`location\.(\w+)`, "hash assign"},
		{`localStorage\.(\w+)`, "getItem setItem"},
		{`\.(\w+) = `, "hidden textContent value title port"},
	} {
		for _, m := range regexp.MustCompile(c.pattern).FindAllStringSubmatch(js, -1) {
			if !slices.Contains(strings.Fields(c.allowed), m[1]) {
				t.Errorf("site/static/js/t.js has %q, where it may only use %s", m[0], c.allowed)
			}
		}
	}
	if got := matches(`setItem\(([^)]*)\)`, js); !slices.Equal(got, []string{"STORE, origin"}) {
		t.Errorf("site/static/js/t.js stores %q, want only the dashboard's address", got)
	}
}

func TestSharePageMarkup(t *testing.T) {
	js, html := sitePage(t, "static/js/t.js"), sitePage(t, "pages/t.html")
	// The layout loads the page's scripts, deferred, from its settings.
	if n := strings.Count(html, "<script"); n != 0 || !strings.Contains(html, "\nscripts: js/t.js\n") {
		t.Errorf("site/pages/t.html has %d scripts of its own, want none, and t.js in its settings", n)
	}
	if m := regexp.MustCompile(`\s(style|on[a-z]+)=`).FindString(html); m != "" {
		t.Errorf("site/pages/t.html has an inline %q, which the site's Content-Security-Policy refuses", strings.TrimSpace(m))
	}
	for _, id := range append(matches(`getElementById\('([^']+)'\)`, js), matches(`put\('([^']+)'`, js)...) {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("site/static/js/t.js uses #%s, which site/pages/t.html does not have", id)
		}
	}
	var shown []string
	for _, v := range matches(`data-show="([^"]+)"`, html) {
		shown = append(shown, strings.Fields(v)...)
	}
	states := append(matches(`return '([a-z]+)'`, js), matches(`show\('([a-z]+)'\)`, js)...)
	for _, s := range states {
		if !slices.Contains(shown, s) {
			t.Errorf("site/pages/t.html shows nothing for the state %q", s)
		}
	}
	for _, s := range shown {
		if !slices.Contains(states, s) {
			t.Errorf("site/pages/t.html has a part for %q, a state site/static/js/t.js never has", s)
		}
	}
	// Without JavaScript, the page says how to do by hand what t.js does,
	// with the create flow's own names for its steps.
	noscript := strings.Join(matches(`(?s)<noscript>(.*?)</noscript>`, html), "")
	if !strings.Contains(noscript, "/servers/new#template=") || !strings.Contains(noscript, "New server › A template") || strings.Contains(noscript, "paste") {
		t.Errorf("site/pages/t.html's no-JavaScript line can't be followed: %q", noscript)
	}
	handoff := strings.Join(matches(`'([a-z]+)'`, strings.Join(matches(`var HANDOFF = \[([^\]]*)\]`, js), "")), " ")
	if form := `<form id="open" data-show="` + handoff + `"`; handoff == "" || !strings.Contains(html, form) {
		t.Errorf("site/pages/t.html does not have %s…>: the form shows when there is a template to send on", form)
	}
}
