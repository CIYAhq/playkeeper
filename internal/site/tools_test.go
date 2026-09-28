package site

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// ldBlocks are a page's structured data blocks, by @type.
func ldBlocks(t *testing.T, html string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, m := range reLD.FindAllStringSubmatch(html, -1) {
		var v map[string]any
		if err := json.Unmarshal([]byte(m[1]), &v); err != nil {
			t.Fatalf("structured data that isn't JSON: %v", err)
		}
		typ, _ := v["@type"].(string)
		out[typ] = v
	}
	return out
}

// Each free tool that exists answers the search it's made for in its title
// and heading, says it's a free web application, has an FAQ and a breadcrumb
// back to the hub, loads the tools' script before its own, says what to do
// without JavaScript, and is linked from the hub and the header's Tools menu.
func TestToolsAnswerTheirSearch(t *testing.T) {
	built := pages(build(t, Default))
	hub := built["/tools"]
	if hub == "" {
		t.Fatal("there is no /tools")
	}
	n := 0
	for _, tool := range tools {
		html, ok := built[tool.Path]
		if !ok {
			continue
		}
		n++
		title := strings.ToLower(between(html, "<title>", "</title>"))
		h1 := strings.ToLower(plainText(reH1.FindStringSubmatch(html)[2]))
		if !strings.Contains(title, tool.Keyword) || !strings.Contains(h1, tool.Keyword) {
			t.Errorf("%s: its title %q and heading %q must both say %q", tool.Path, title, h1, tool.Keyword)
		}
		ld := ldBlocks(t, html)
		app := ld["WebApplication"]
		if app == nil {
			t.Errorf("%s has no WebApplication structured data", tool.Path)
		} else {
			offer, _ := app["offers"].(map[string]any)
			if app["url"] != "https://playkeeper.io"+tool.Path || app["isAccessibleForFree"] != true || offer["price"] != "0" || app["browserRequirements"] == nil {
				t.Errorf("%s: its WebApplication data doesn't say where it is, that it's free and that it needs JavaScript: %v", tool.Path, app)
			}
		}
		if faq := ld["FAQPage"]; faq == nil {
			t.Errorf("%s has no FAQ", tool.Path)
		} else if qs, _ := faq["mainEntity"].([]any); len(qs) < 4 {
			t.Errorf("%s answers %d questions, want at least 4", tool.Path, len(qs))
		}
		crumbs, _ := ld["BreadcrumbList"]["itemListElement"].([]any)
		if len(crumbs) != 2 {
			t.Errorf("%s has %d breadcrumbs, want Tools and the tool", tool.Path, len(crumbs))
		} else if top, _ := crumbs[0].(map[string]any); top["item"] != "https://playkeeper.io/tools" {
			t.Errorf("%s: the breadcrumb's first step is %v, want the hub", tool.Path, top)
		}
		scripts := regexp.MustCompile(`<script src="/assets/js/([^"]+)\.[0-9a-f]{8}\.js"`).FindAllStringSubmatch(html, -1)
		var names []string
		for _, s := range scripts {
			names = append(names, s[1])
		}
		if len(names) < 3 || names[0] != "site" || names[1] != "tools" || !strings.HasPrefix(names[2], "tools/") {
			t.Errorf("%s loads %v, want site.js, tools.js, then its own", tool.Path, names)
		}
		if !strings.Contains(html, `<noscript><link rel="stylesheet" href="/assets/css/tools-nojs.`) || !strings.Contains(html, `class="tool-nojs"`) {
			t.Errorf("%s doesn't say what to do without JavaScript", tool.Path)
		}
		if !strings.Contains(hub, `href="`+tool.Path+`"`) {
			t.Errorf("the hub doesn't link %s", tool.Path)
		}
		if !strings.Contains(built["/"], `<li><a href="`+tool.Path+`"`) {
			t.Errorf("the header's Tools menu doesn't list %s", tool.Path)
		}
	}
	if n == 0 {
		t.Fatal("no tool pages are built")
	}
	list, _ := ldBlocks(t, hub)["ItemList"]["itemListElement"].([]any)
	if len(list) != n {
		t.Errorf("the hub's ItemList has %d tools, want the %d that exist", len(list), n)
	}
	if !strings.Contains(built["/"], `<a href="/tools">All free tools</a>`) {
		t.Error("the header's Tools menu doesn't end with the hub")
	}
}

// Each swatch the tools offer is drawn in css/tools.css in its colour.
func TestPaletteMatchesTheToolsStylesheet(t *testing.T) {
	css, err := os.ReadFile("../../site/static/css/tools.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range palette {
		if !strings.Contains(string(css), ".c-"+s.ID+" { background: "+s.Hex+"; }") {
			t.Errorf("css/tools.css doesn't draw .c-%s in %s", s.ID, s.Hex)
		}
	}
}
