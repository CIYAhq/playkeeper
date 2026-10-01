package site

import (
	"encoding/binary"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// battlePage is the AI build battle's page, which plays a film and has a
// short address.
const battlePage = "site/pages/ai-build-battle.html"

// buildEdited builds the site with files of the repository, by path, changed
// by their edits.
func buildEdited(t *testing.T, edits map[string]func(string) string) (*Output, error) {
	t.Helper()
	over := fstest.MapFS{}
	for name, edit := range edits {
		b, err := os.ReadFile("../../" + name)
		if err != nil {
			t.Fatal(err)
		}
		over[name] = &fstest.MapFile{Data: []byte(edit(string(b)))}
	}
	return Build(Options{Root: overlay{os.DirFS("../.."), over}, Settings: Default, Now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)})
}

// set changes a page's settings, key and value after key and value: an
// empty value drops the setting.
func set(kv ...string) func(string) string {
	return func(src string) string {
		head, body, _ := strings.Cut(src, "*/}}")
		for i := 0; i+1 < len(kv); i += 2 {
			var keep []string
			for _, l := range strings.SplitAfter(head, "\n") {
				if !strings.HasPrefix(l, kv[i]+":") {
					keep = append(keep, l)
				}
			}
			if kv[i+1] != "" {
				keep = append(keep, kv[i]+": "+kv[i+1]+"\n")
			}
			head = strings.Join(keep, "")
		}
		return head + "*/}}" + body
	}
}

// boxes are an MP4's top-level boxes, in order.
func boxes(b []byte) []string {
	var out []string
	for len(b) >= 8 {
		size := uint64(binary.BigEndian.Uint32(b))
		out = append(out, string(b[4:8]))
		switch {
		case size == 0:
			size = uint64(len(b))
		case size == 1 && len(b) >= 16:
			size = binary.BigEndian.Uint64(b[8:])
		}
		if size < 8 || size > uint64(len(b)) {
			break
		}
		b = b[size:]
	}
	return out
}

// A page that plays a film of the site's own (film: true), like the AI build
// battle's, gets a location in nginx's include whose policy lets the site's
// media in, as /start's does; the site's own policy doesn't. A <video> on a
// page without the setting, or the setting on a page without one, stops the
// build. A film is under 1.5 MB and starts playing before it has all
// arrived: its moov box comes before its mdat.
func TestFilms(t *testing.T) {
	o := build(t, Default)
	const p = "/templates/ai-build-battle"
	nginx := string(o.Nginx)
	loc := between(nginx, "location = "+p+" {", "}")
	policy := between(loc, `set $csp "`, `";`)
	if policy != o.FilmPolicy || !strings.Contains(policy, "media-src 'self';") {
		t.Errorf("%s's policy is %q, want the film policy %q, which lets the site's media in", p, policy, o.FilmPolicy)
	}
	if policy != strings.Replace(o.Policy, "img-src 'self';", "img-src 'self'; media-src 'self';", 1) {
		t.Errorf("%s's policy is %q, not the site's with media-src", p, policy)
	}
	for _, want := range []string{"expires -1;", "try_files /templates/ai-build-battle.html =404;"} {
		if !strings.Contains(loc, want) {
			t.Errorf("%s's location doesn't say %s: %q", p, want, loc)
		}
	}
	if strings.Count(nginx, "location = /start {") != 1 {
		t.Error("/start has a film location besides its own")
	}
	if strings.Contains(o.Policy, "media-src") {
		t.Errorf("the site's policy lets media in: %s", o.Policy)
	}
	if !slices.Equal(o.Films, []string{p}) {
		t.Errorf("the pages with a film location are %v, want %s", o.Films, p)
	}
	html := pages(o)[p]
	if !strings.Contains(html, `<video data-film muted loop playsinline controls preload="none" width="1280" height="720" poster="/assets/film/ai-build-battle-poster.`) || !strings.Contains(html, `<source src="/assets/film/ai-build-battle.`) {
		t.Errorf("%s doesn't play its film, muted, from its poster", p)
	}
	films := 0
	for name, b := range o.Files {
		if !strings.HasPrefix(name, "assets/film/") || !strings.HasSuffix(name, ".mp4") {
			continue
		}
		films++
		if len(b) > 1_500_000 {
			t.Errorf("%s is %d bytes, over 1.5 MB", name, len(b))
		}
		order := boxes(b)
		if moov, mdat := slices.Index(order, "moov"), slices.Index(order, "mdat"); moov < 0 || mdat < 0 || moov > mdat {
			t.Errorf("%s's boxes are %v: it can't start before it has all arrived", name, order)
		}
	}
	if films < 2 {
		t.Errorf("%d films; /start and the AI build battle's page have one each", films)
	}
	for name, edits := range map[string]map[string]func(string) string{
		"a <video> without film: true": {battlePage: set("film", "")},
		"film: true without a <video>": {"site/pages/pricing.html": set("film", "true")},
	} {
		if _, err := buildEdited(t, edits); err == nil || !strings.Contains(err.Error(), "film") {
			t.Errorf("the site builds with %s: %v", name, err)
		}
	}
	if _, err := parsePage("{{/*\npath: /x\nfilm: sometimes\n*/}}"); err == nil {
		t.Error("a page's film setting takes more than true or false")
	}
}

// A page's short address for posts and videos (short: /ai) is a 302 in
// nginx's include to the page with the query string, so a post's UTM tags
// reach it, in any case and with or without a trailing slash. It comes before
// site/nginx.conf's own locations, where / on the end is a 301 that drops the
// query string, and the channels' links. It can't be a page, an address nginx
// answers itself, or another page's short address.
func TestShortAddresses(t *testing.T) {
	o := build(t, Default)
	nginx := string(o.Nginx)
	const want = "location ~* ^/ai/?$ {\n    return 302 /templates/ai-build-battle$is_args$args;\n}\n"
	at := strings.Index(nginx, want)
	if at < 0 {
		t.Fatalf("nginx's include has no\n%s", want)
	}
	if strings.Count(nginx, "return 302 /templates/ai-build-battle") != 1 {
		t.Error("nginx's include has more than one short address for /templates/ai-build-battle")
	}
	if channels := strings.Index(nginx, "location ~* ^/go/"); channels < at {
		t.Error("the short addresses come after the channels' links")
	}
	match := regexp.MustCompile(`(?i)^/ai/?$`)
	for addr, ok := range map[string]bool{"/ai": true, "/AI": true, "/ai/": true, "/Ai/": true, "/aim": false, "/ai/x": false, "/x/ai": false} {
		if match.MatchString(addr) != ok {
			t.Errorf("/ai's location answers %s: %v, want %v", addr, !ok, ok)
		}
	}
	conf, err := os.ReadFile("../../site/nginx.conf")
	if err != nil {
		t.Fatal(err)
	}
	if include, slash := strings.Index(string(conf), "include /etc/nginx/playkeeper-site.conf;"), strings.Index(string(conf), "location ~ ^/(?!demo/)(.+)/$"); include < 0 || slash < include {
		t.Error("site/nginx.conf's trailing-slash location comes before the include, so /ai/ would lose its query string on the way")
	}
	for name, edits := range map[string]map[string]func(string) string{
		"a page's address":             {battlePage: set("short", "/pricing")},
		"an address nginx answers":     {battlePage: set("short", "/demo")},
		"the channels' links":          {battlePage: set("short", "/go")},
		"another page's short address": {"site/pages/pricing.html": set("short", "/ai")},
	} {
		if _, err := buildEdited(t, edits); err == nil || !strings.Contains(err.Error(), "short address") {
			t.Errorf("the site builds with a short address that's %s: %v", name, err)
		}
	}
	for _, bad := range []string{"ai", "/AI", "/ai/", "/a/b", "/ai build", "/-ai", "/ai;", "/"} {
		if _, err := parsePage("{{/*\npath: /x\nshort: " + bad + "\n*/}}"); err == nil {
			t.Errorf("a page's short address can be %q", bad)
		}
	}
}
