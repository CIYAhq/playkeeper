package site

import (
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/sizing"
)

// build builds the site from this repository, as site/Dockerfile does.
func build(t *testing.T, s Settings) *Output {
	t.Helper()
	o, err := Build(Options{Root: os.DirFS("../.."), Settings: s, Now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// pages are the built pages, by address.
func pages(o *Output) map[string]string {
	out := map[string]string{}
	for name, b := range o.Files {
		if path.Ext(name) != ".html" {
			continue
		}
		p := "/" + strings.TrimSuffix(name, ".html")
		if p == "/index" {
			p = "/"
		}
		out[p] = string(b)
	}
	return out
}

var (
	reHref     = regexp.MustCompile(`\s(?:href|src)="([^"]*)"`)
	reSrcset   = regexp.MustCompile(`\s(?:srcset|imagesrcset)="([^"]*)"`)
	reID       = regexp.MustCompile(`\sid="([^"]+)"`)
	reLD       = regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)
	reImg      = regexp.MustCompile(`<img\s[^>]*>`)
	reHeadings = regexp.MustCompile(`<h([1-6])[\s>]`)
)

// routes answered by nginx or the live demo rather than by a page.
var routes = []string{"/install", "/community", "/demo/", "/favicon.svg", "/sitemap.xml", "/robots.txt", "/blog/feed.xml"}

// demoPage matches the live demo's pages the site links to, as the
// dashboard's router (web/src/lib/router.ts) reads them; the demo's servers
// are survival, creative and cobblemon, its machine q7m2vk9xpd.
var demoPage = regexp.MustCompile(`^/demo/(servers/(survival|creative|cobblemon)(/(console|players|world|map|plugins|mods|settings)(/(pregen|packs|browse))?)?|machines/q7m2vk9xpd(/settings)?|settings(/(team|addon-sources|discord))?)$`)

func TestEveryLinkAndAnchorLands(t *testing.T) {
	o := build(t, Default)
	built := pages(o)
	for p, html := range built {
		var refs []string
		for _, m := range reHref.FindAllStringSubmatch(html, -1) {
			refs = append(refs, m[1])
		}
		for _, m := range reSrcset.FindAllStringSubmatch(html, -1) {
			for _, part := range strings.Split(m[1], ",") {
				refs = append(refs, strings.Fields(part)[0])
			}
		}
		for _, ref := range refs {
			if !strings.HasPrefix(ref, "/") && !strings.HasPrefix(ref, "#") {
				continue
			}
			addr, frag, _ := strings.Cut(ref, "#")
			if addr == "" {
				addr = p
			}
			if addr == "/t" && frag != "" {
				continue // a template, read by the share page
			}
			target, isPage := built[addr]
			_, isFile := o.Files[strings.TrimPrefix(addr, "/")]
			if !isPage && !isFile && !slices.Contains(routes, addr) && !demoPage.MatchString(addr) {
				t.Errorf("%s links to %s, which nothing answers", p, ref)
				continue
			}
			if frag != "" && isPage && !strings.Contains(target, ` id="`+frag+`"`) {
				t.Errorf("%s links to %s, whose page has no #%s", p, ref, frag)
			}
		}
	}
}

// siteCheckLengths are the title and description lengths, in characters as
// grep counts them in a UTF-8 locale, that scripts/site-check.sh allows on
// the pages it serves, read from its own rules so every built page is held
// to the same ones.
func siteCheckLengths(t *testing.T) (title, desc [2]int) {
	t.Helper()
	b, err := os.ReadFile("../../scripts/site-check.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []struct {
		re   string
		into *[2]int
	}{
		{`<title>\[\^<\]\{(\d+),(\d+)\}</title>`, &title},
		{`<meta name="description" content="\[\^"\]\{(\d+),(\d+)\}">`, &desc},
	} {
		m := regexp.MustCompile(rule.re).FindStringSubmatch(string(b))
		if m == nil {
			t.Fatalf("scripts/site-check.sh has no length rule like %s", rule.re)
		}
		rule.into[0], _ = strconv.Atoi(m[1])
		rule.into[1], _ = strconv.Atoi(m[2])
	}
	return title, desc
}

func within(s string, n [2]int) bool {
	c := utf8.RuneCountInString(s)
	return c >= n[0] && c <= n[1]
}

func TestEveryPageIsWellFormed(t *testing.T) {
	o := build(t, Default)
	titleLen, descLen := siteCheckLengths(t)
	if within(strings.Repeat("d", 40), descLen) {
		t.Errorf("a 40-character description passes, but scripts/site-check.sh allows %d to %d", descLen[0], descLen[1])
	}
	titles := map[string]string{}
	for p, html := range pages(o) {
		// The share page has a heading for each of its states, and shows one.
		if n := strings.Count(html, "<h1"); n != 1 && p != "/t" {
			t.Errorf("%s has %d <h1>, want 1", p, n)
		}
		if strings.Count(html, "<main") != 1 || !strings.Contains(html, `<html lang="en">`) {
			t.Errorf("%s has no <main> or no language", p)
		}
		title := between(html, "<title>", "</title>")
		desc := unescape(between(html, `<meta name="description" content="`, `"`))
		if !within(title, titleLen) {
			t.Errorf("%s: title %q is %d characters, want %d to %d", p, title, utf8.RuneCountInString(title), titleLen[0], titleLen[1])
		}
		if !within(desc, descLen) {
			t.Errorf("%s: description %q is %d characters, want %d to %d", p, desc, utf8.RuneCountInString(desc), descLen[0], descLen[1])
		}
		noindex := strings.Contains(html, `<meta name="robots" content="noindex">`)
		if !noindex {
			if other, dup := titles[title]; dup {
				t.Errorf("%s and %s have the same title %q", p, other, title)
			}
			titles[title] = p
			if !strings.Contains(html, `<link rel="canonical" href="https://playkeeper.io`+p+`">`) {
				t.Errorf("%s does not name itself as its canonical address", p)
			}
		}
		for _, tag := range []string{`property="og:image" content="https://playkeeper.io/assets/og/`, `name="twitter:card" content="summary_large_image"`, `property="og:title"`} {
			if !strings.Contains(html, "<meta "+tag) {
				t.Errorf("%s has no <meta %s", p, tag)
			}
		}
		// The Content-Security-Policy allows no inline script or style.
		stripped := reLD.ReplaceAllString(html, "")
		if m := regexp.MustCompile(`<script>|<script [^s]|<style|\s(style|on[a-z]+)=`).FindString(stripped); m != "" {
			t.Errorf("%s has inline script or style: %q", p, m)
		}
		for _, m := range reLD.FindAllStringSubmatch(html, -1) {
			var v map[string]any
			if err := json.Unmarshal([]byte(m[1]), &v); err != nil || v["@context"] != "https://schema.org" || v["@type"] == nil {
				t.Errorf("%s has structured data that isn't valid: %v %.80s", p, err, m[1])
			}
		}
		for _, img := range reImg.FindAllString(html, -1) {
			if !strings.Contains(img, ` alt="`) {
				t.Errorf("%s has an image without alt: %s", p, img)
			}
			if !strings.Contains(img, ` width="`) || !strings.Contains(img, ` height="`) {
				t.Errorf("%s has an image without its size, which shifts the page as it loads: %s", p, img)
			}
		}
		// Headings go down one level at a time.
		last := 0
		for _, m := range reHeadings.FindAllStringSubmatch(stripped, -1) {
			level := int(m[1][0] - '0')
			if level > last+1 {
				t.Errorf("%s skips from <h%d> to <h%d>", p, last, level)
				break
			}
			last = level
		}
		if ids := reID.FindAllStringSubmatch(html, -1); len(ids) > 0 {
			seen := map[string]bool{}
			for _, id := range ids {
				if seen[id[1]] {
					t.Errorf("%s has two elements with id %q", p, id[1])
				}
				seen[id[1]] = true
			}
		}
	}
}

// reH1 matches a top heading: its attributes and what's in it.
var reH1 = regexp.MustCompile(`(?s)<h1(\s[^>]*)?>(.*?)</h1>`)

// Every page has a top heading with words in it. The share page has one for
// each of its states; the ready state's is the template's name, which
// js/t.js puts in before showing it, since a template without a name is
// damaged.
func TestEveryPageHasAHeading(t *testing.T) {
	script, err := os.ReadFile("../../site/static/js/t.js")
	if err != nil {
		t.Fatal(err)
	}
	for p, html := range pages(build(t, Default)) {
		headings := reH1.FindAllStringSubmatch(html, -1)
		if len(headings) == 0 {
			t.Errorf("%s has no <h1>", p)
		}
		for _, h := range headings {
			if plainText(h[2]) != "" {
				continue
			}
			if id := between(h[1], ` id="`, `"`); p == "/t" && id != "" && strings.Contains(string(script), "put('"+id+"', name)") {
				continue
			}
			t.Errorf("%s has an empty heading: %s", p, h[0])
		}
	}
}

func between(s, a, b string) string {
	_, rest, ok := strings.Cut(s, a)
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(rest, b)
	return v
}

func TestTheLaunchPagesExist(t *testing.T) {
	built := pages(build(t, Default))
	for _, p := range []string{"/", "/features/mods-and-modpacks", "/alternatives/aternos", "/alternatives/pterodactyl",
		"/guides/modded-minecraft-server", "/sizing", "/docs", "/pricing", "/blog", "/blog/playkeeper-0-4-0", "/t", "/404", "/start"} {
		if _, ok := built[p]; !ok {
			t.Errorf("there is no %s", p)
		}
	}
	for p, html := range built {
		// /t/<id> sends the browser straight on to the share page.
		if p == "/404" || strings.HasPrefix(p, "/docs") || p == "/t" || strings.HasPrefix(p, "/t/") {
			continue
		}
		want := "curl -fsSL https://playkeeper.io/install | sudo sh"
		if p == "/start" {
			want = "curl -fsSL https://playkeeper.io/install/start | sudo sh"
		}
		if !strings.Contains(html, want) {
			t.Errorf("%s doesn't show the install command", p)
		}
	}
}

func TestSitemapAndRobots(t *testing.T) {
	o := build(t, Default)
	sm := string(o.Files["sitemap.xml"])
	for p, html := range pages(o) {
		listed := strings.Contains(sm, "<loc>https://playkeeper.io"+p+"</loc>")
		noindex := strings.Contains(html, `content="noindex"`)
		if listed == noindex {
			t.Errorf("%s: in the sitemap %v, kept out of search engines %v", p, listed, noindex)
		}
	}
	if !strings.Contains(sm, "<loc>https://playkeeper.io/demo/</loc>") {
		t.Error("the sitemap doesn't list the live demo")
	}
	robots := string(o.Files["robots.txt"])
	for _, line := range []string{"Allow: /demo/$", "Allow: /demo/assets/", "Disallow: /demo/", "Sitemap: https://playkeeper.io/sitemap.xml"} {
		if !strings.Contains(robots, line+"\n") {
			t.Errorf("robots.txt doesn't say %q", line)
		}
	}
	if !strings.Contains(string(o.Files["blog/feed.xml"]), "https://playkeeper.io/blog/playkeeper-0-4-0") {
		t.Error("the blog's feed doesn't have the 0.4.0 post")
	}
}

// Every page search engines index is linked from at least two others they
// index, so none hangs off a single link: a docs page needs its entry in
// docGroups or the footer, not only a mention on another page.
func TestEveryPageIsLinkedFromTwoOthers(t *testing.T) {
	built := pages(build(t, Default))
	indexed := func(p string) bool {
		html, ok := built[p]
		return ok && !strings.Contains(html, `content="noindex"`)
	}
	from := map[string]map[string]bool{}
	for p, html := range built {
		if !indexed(p) {
			continue
		}
		for _, m := range reHref.FindAllStringSubmatch(html, -1) {
			addr, _, _ := strings.Cut(m[1], "#")
			if addr == p || !indexed(addr) {
				continue
			}
			if from[addr] == nil {
				from[addr] = map[string]bool{}
			}
			from[addr][p] = true
		}
	}
	for p := range built {
		if indexed(p) && len(from[p]) < 2 {
			t.Errorf("%s is linked from %d other pages, want at least 2", p, len(from[p]))
		}
	}
}

// The server types, their count and every line about Forge follow
// minecraft.Types, the list the dashboard offers.
func TestServerTypesFollowTheProduct(t *testing.T) {
	saved := slices.Clone(minecraft.Types)
	t.Cleanup(func() { minecraft.Types = saved })
	setForge := func(on bool) {
		minecraft.Types = slices.DeleteFunc(slices.Clone(saved), func(x minecraft.ServerType) bool { return x.ID == "forge" })
		if on {
			minecraft.Types = append(minecraft.Types, minecraft.ServerType{ID: "forge", Name: "Forge", Available: true})
		}
	}
	forgeData := false
	for _, dir := range []string{"../../site/data/modpacks", "../../site/data/templates"} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			var m struct {
				Type   string `json:"type"`
				Server struct {
					Type string `json:"type"`
				} `json:"server"`
			}
			if b, err := os.ReadFile(dir + "/" + e.Name()); err == nil && json.Unmarshal(b, &m) == nil && (m.Type == "forge" || m.Server.Type == "forge") {
				forgeData = true
			}
		}
	}
	for _, forge := range []bool{false, true} {
		if _, err := os.Stat("../../web/src/assets/logos/forge-apple-touch-icon.png"); forge && err != nil {
			t.Log("Forge's logo isn't in web/src/assets/logos yet, so Forge can't be shown")
			continue
		}
		if !forge && forgeData {
			t.Log("the site has Forge packs or templates, which a release without Forge can't install, so it isn't built without Forge")
			continue
		}
		setForge(forge)
		n := len(serverTypes(textOrder))
		built := pages(build(t, Default))
		landing, feature, guide := built["/"], built["/features/mods-and-modpacks"], built["/guides/modded-minecraft-server"]
		// The card's sentence starts with the count, capitalised.
		count := countWord(n)
		if card := "<p>" + strings.ToUpper(count[:1]) + count[1:] + " server types, add-ons in one click.</p>"; !strings.Contains(landing, card) {
			t.Errorf("forge %v: the landing page doesn't say %s", forge, card)
		}
		if got := strings.Count(feature, `<span class="logo-tile">`); got != n {
			t.Errorf("forge %v: the logo row has %d logos, want %d", forge, got, n)
		}
		hasForge := strings.Contains(feature, "<span>Forge</span>")
		if hasForge != forge {
			t.Errorf("forge %v: the logo row shows Forge: %v", forge, hasForge)
		}
		addMods := built["/guides/add-mods-to-minecraft-server"]
		for _, p := range []string{landing, feature, guide, addMods, built["/blog/playkeeper-0-4-0"]} {
			if strings.Contains(p, "Forge isn") || strings.Contains(p, "No Forge") {
				t.Errorf("forge %v: a page still says Forge isn't in Playkeeper", forge)
			}
		}
		if got := strings.Count(addMods, `<span class="loader-cell">`); got != n {
			t.Errorf("forge %v: the add-mods guide's table has %d server types, want %d", forge, got, n)
		}
		wantOr := "Fabric, Quilt or NeoForge"
		if forge {
			wantOr = "Fabric, Quilt, NeoForge or Forge"
		}
		if !strings.Contains(addMods, "Mods need "+wantOr+", so start") {
			t.Errorf("forge %v: the add-mods guide doesn't name the loaders as %s", forge, wantOr)
		}
		if got := strings.Contains(guide, "Forge servers run Forge mods from Modrinth"); got != forge {
			t.Errorf("forge %v: the guide's Forge answer says Forge runs: %v", forge, got)
		}
		if got := strings.Contains(guide, `id="tab-forge"`); got != forge {
			t.Errorf("forge %v: the guide's code has a Forge tab: %v", forge, got)
		}
		wantLoaders := "Fabric, Quilt and NeoForge"
		if forge {
			wantLoaders = "Fabric, Quilt, NeoForge and Forge"
		}
		if !strings.Contains(feature, "Paper and Purpur run plugins; "+wantLoaders+" run mods.") {
			t.Errorf("forge %v: the feature page doesn't name the loaders as %s", forge, wantLoaders)
		}
	}
	for _, st := range serverTypes(textOrder) {
		if _, err := os.Stat("../../web/src/assets/" + strings.TrimPrefix(st.Logo, "app/")); st.Logo == "" || err != nil {
			t.Errorf("the site has no logo for %s servers (%q): %v", st.Name, st.Logo, err)
		}
	}
}

// Where questions go is one setting; with it on the issues, no page says
// Discussions.
func TestCommunityIsOneSetting(t *testing.T) {
	for _, c := range []Community{issues, discussions} {
		s := Default
		s.Community = c
		o := build(t, s)
		for p, html := range pages(o) {
			// Docs pages say what the repository's Markdown says.
			if c == issues && !strings.HasPrefix(p, "/docs/") && strings.Contains(html, "Discussions") {
				t.Errorf("%s says Discussions while questions go to the issues", p)
			}
		}
		built := pages(o)
		for _, p := range []string{"/", "/docs", "/blog/playkeeper-0-4-0"} {
			if !strings.Contains(built[p], `href="`+c.URL+`"`) || !strings.Contains(built[p], c.Ask) {
				t.Errorf("%s doesn't send questions to %s with %q", p, c.URL, c.Ask)
			}
		}
		if !strings.Contains(string(o.Nginx), "return 302 "+c.URL+";") {
			t.Errorf("/community doesn't redirect to %s", c.URL)
		}
	}
}

// Playkeeper Cloud's seller terms are at /cloud/seller-terms, kept out of
// search engines as /cloud is, and say who's behind them, which version
// they are and where legal notices go. They link the privacy policy only
// once its page is on the site.
func TestTheSellerTerms(t *testing.T) {
	built := pages(build(t, Default))
	terms, ok := built["/cloud/seller-terms"]
	if !ok {
		t.Fatal("there is no /cloud/seller-terms")
	}
	for _, want := range []string{"CIYA TECHNOLOGIES LTD, Pyrrou 11, 4105 Limassol, Cyprus", "Version of 1 October 2026", `<a href="mailto:me@siya.digital">me@siya.digital</a>`, `<meta name="robots" content="noindex">`} {
		if !strings.Contains(terms, want) {
			t.Errorf("the seller terms don't say %s", want)
		}
	}
	if body := between(terms, "<main", "</main>"); strings.Contains(body, "[") {
		t.Errorf("the seller terms still have a bracketed proposal: %s", between(body, "[", "]"))
	}
	if _, policy := built["/privacy"]; !policy && strings.Contains(terms, `href="/privacy"`) {
		t.Error("the seller terms link a privacy policy that isn't there")
	}
}

// Nothing on the site collects an email address; the pricing cards that come
// later offer Watch releases on GitHub.
func TestNoEmailForms(t *testing.T) {
	o := build(t, Default)
	built := pages(o)
	for p, html := range built {
		if strings.Contains(html, `type="email"`) || strings.Contains(html, "<form") && p != "/t" {
			t.Errorf("%s has an email form", p)
		}
	}
	if n := strings.Count(built["/pricing"], ">Watch releases on GitHub"); n != 2 {
		t.Errorf("pricing offers Watch releases on GitHub %d times, want 2", n)
	}
	if !strings.Contains(string(o.Nginx), "form-action 'none'") {
		t.Error("the Content-Security-Policy lets forms post somewhere")
	}
}

// The share page makes no requests: it loads only site.js and t.js, and
// neither the star count nor the copy count, which site.js makes only when a
// page asks for them.
func TestSharePageLoadsOnlyItsScripts(t *testing.T) {
	o := build(t, Default)
	html := pages(o)["/t"]
	scripts := regexp.MustCompile(`<script src="([^"]+)"`).FindAllStringSubmatch(html, -1)
	if len(scripts) != 2 || !strings.Contains(scripts[0][1], "/assets/js/site.") || !strings.Contains(scripts[1][1], "/assets/js/t.") {
		t.Errorf("/t loads %v, want site.js and t.js", scripts)
	}
	if strings.Contains(html, "data-stars") {
		t.Error("/t asks GitHub for the star count")
	}
	if strings.Contains(html, `name="playkeeper-stats"`) {
		t.Error("/t names the stats service")
	}
	site := string(o.Files[strings.TrimPrefix(scripts[0][1], "/")])
	if n := strings.Count(site, "fetch("); n != 2 || !strings.Contains(site, "var star = $('[data-stars]');\n  if (star && window.fetch)") ||
		!strings.Contains(site, `var stats = $('meta[name="playkeeper-stats"]');`) || !strings.Contains(site, "if (!stats || told || !window.fetch") {
		t.Error("site.js makes a request other than the star count and the copy count, or makes one without what asks for it")
	}
}

// Every page but the share page, and the /t/<id> pages that send the
// browser straight on to it, counts its visit, and the
// Content-Security-Policy lets the analytics' script and collector in; with
// the setting empty, nothing loads and the policy names neither.
func TestAnalyticsIsOneSetting(t *testing.T) {
	tag := `<script src="https://analytics-c.ciya.so/oa.js" async data-key="oa_pk_tyJHnpyD4m-pl_XrUbi3maHu2Iqq87Uf" data-collector="https://analytics-c.ciya.so"></script>`
	o := build(t, Default)
	for p, html := range pages(o) {
		want := 1
		if p == "/t" || strings.HasPrefix(p, "/t/") {
			want = 0
		}
		if got := strings.Count(html, tag); got != want {
			t.Errorf("%s loads the analytics %d times, want %d", p, got, want)
		}
	}
	for _, want := range []string{"script-src 'self' https://analytics-c.ciya.so;", "connect-src 'self' https://api.github.com https://analytics-c.ciya.so https://stats.playkeeper.io;"} {
		if !strings.Contains(string(o.Nginx), want) {
			t.Errorf("the Content-Security-Policy doesn't say %s", want)
		}
	}
	off := Default
	off.Analytics = Analytics{}
	o = build(t, off)
	for p, html := range pages(o) {
		if strings.Contains(html, "analytics-c.ciya.so") {
			t.Errorf("%s loads the analytics while it's off", p)
		}
	}
	for _, want := range []string{"script-src 'self';", "connect-src 'self' https://api.github.com https://stats.playkeeper.io;"} {
		if !strings.Contains(string(o.Nginx), want) {
			t.Errorf("with the analytics off, the Content-Security-Policy doesn't say %s", want)
		}
	}
}

// Where copies of the install command are counted is one setting too: the
// same pages as the analytics name the stats service, and the
// Content-Security-Policy lets them reach it; with the setting empty, no
// page names it and the policy doesn't either.
func TestTheCopyCountIsOneSetting(t *testing.T) {
	tag := `<meta name="playkeeper-stats" content="https://stats.playkeeper.io">`
	o := build(t, Default)
	for p, html := range pages(o) {
		want := 1
		if p == "/t" || strings.HasPrefix(p, "/t/") {
			want = 0
		}
		if got := strings.Count(html, tag); got != want {
			t.Errorf("%s names the stats service %d times, want %d", p, got, want)
		}
	}
	if !strings.Contains(string(o.Nginx), " https://stats.playkeeper.io;") {
		t.Error("the Content-Security-Policy doesn't let pages reach the stats service")
	}
	off := Default
	off.Stats = ""
	o = build(t, off)
	for p, html := range pages(o) {
		if strings.Contains(html, `name="playkeeper-stats"`) {
			t.Errorf("%s names the stats service while it's off", p)
		}
	}
	if strings.Contains(string(o.Nginx), "stats.playkeeper.io") {
		t.Error("with the setting off, the Content-Security-Policy still names the stats service")
	}
}

// Each channel's link is the landing page with its UTM tags, and any other
// /go/ address the landing page. Only the landing page lists the codes it
// shows an install command for. nginx.conf's /install/<code> takes every code
// the settings can have, and a code it wouldn't take fails the build.
func TestChannels(t *testing.T) {
	o := build(t, Default)
	for _, c := range Default.Channels {
		want := fmt.Sprintf("location ~* ^/go/%s/?$ {\n    return 302 /?utm_source=%s&utm_medium=%s&utm_campaign=%s&utm_content=%s;\n}\n", c.Code, c.Source, c.Medium, c.Campaign, c.Code)
		if !strings.Contains(string(o.Nginx), want) {
			t.Errorf("nginx's include has no\n%s", want)
		}
	}
	if !strings.HasSuffix(string(o.Nginx), "location /go/ {\n    return 302 /;\n}\n") {
		t.Error("nginx's include doesn't end with /go/ for any other code, after the channels")
	}
	listed := `data-channels="cygnus madhu kasai doopa lth nicx linuxbtw hn selfhosted ph x whop start"`
	for p, html := range pages(o) {
		want := 0
		if p == "/" {
			want = 1
		}
		if got := strings.Count(html, listed); got != want {
			t.Errorf("%s lists the channels %d times, want %d", p, got, want)
		}
	}
	conf, err := os.ReadFile("../../site/nginx.conf")
	if err != nil {
		t.Fatal(err)
	}
	code := strings.TrimSuffix(strings.TrimPrefix(channelCode.String(), "^"), "$")
	if !strings.Contains(string(conf), `location ~* "^/install(?:/(?<channel>`+code+`))?$" {`) {
		t.Errorf("site/nginx.conf's /install/<code> doesn't take %s", code)
	}
	for _, bad := range []Channel{
		{"Cygnus", "youtube", "sponsor", "creators-oct26"},
		{"hn; }", "hackernews", "community", "launch-sep26"},
		{"hn", "hackernews", "community", "launch-sep26"},
		{"new", "", "community", "launch-sep26"},
	} {
		s := Default
		s.Channels = append(slices.Clone(Default.Channels), bad)
		if _, err := Build(Options{Root: os.DirFS("../.."), Settings: s, Now: time.Now()}); err == nil {
			t.Errorf("the site builds with the channel %+v", bad)
		}
	}
}

// /start, where the Meta ads land, has a location of its own: it lets the
// page's film in, with the policy of the pages that play one (TestFilms),
// and keeps /start out of search engines, as the page keeps itself out of
// the sitemap. Like every page, it loads no ad pixel (TestNoPageLoadsAnAdPixel).
// A page's channel must be one of the settings'.
func TestStartPage(t *testing.T) {
	o := build(t, Default)
	const share = `<button type="button" class="install-share" data-share hidden>`
	for p, html := range pages(o) {
		// Send to my computer, beside both Copy buttons on /, /start and the
		// AI build battle's page, where most visitors are on phones.
		want := 0
		if p == "/" || p == "/start" || p == "/templates/ai-build-battle" {
			want = 2
		}
		if n := strings.Count(html, share); n != want {
			t.Errorf("%s has %d Send to my computer buttons, want %d", p, n, want)
		}
	}
	start := pages(o)["/start"]
	if !strings.Contains(start, `<meta name="robots" content="noindex">`) || strings.Contains(string(o.Files["sitemap.xml"]), "/start") {
		t.Error("/start isn't kept out of search engines and the sitemap")
	}
	under := between(start, share, `class="start-how`)
	if !strings.Contains(under, `Don't have a VPS yet? They cost from a few dollars a month. <a class="link-arrow" href="/sizing">Which one to rent`) {
		t.Errorf("/start doesn't say where to rent a VPS right under its install command: %q", under)
	}
	if n := strings.Count(start, "a few dollars a month"); n != 1 {
		t.Errorf("/start says what a VPS costs %d times, want once", n)
	}
	if _, err := parsePage("{{/*\npath: /x\nshare: yes please\n*/}}"); err == nil {
		t.Error("a page's share setting takes more than true or false")
	}
	nginx := string(o.Nginx)
	site := between(nginx, `set $csp "`, `";`)
	own := between(between(nginx, "location = /start {", "}"), `set $csp "`, `";`)
	if site != o.Policy || strings.Contains(site, "media-src") {
		t.Errorf("the site's policy lets in the film only /start and the pages that play one need: %s", site)
	}
	if own != o.FilmPolicy {
		t.Errorf("/start's policy is %q, want the film policy %q", own, o.FilmPolicy)
	}
	for _, want := range []string{"set $robots noindex;", "try_files /start.html =404;", "add_header X-Robots-Tag $robots always;"} {
		if !strings.Contains(nginx, want) {
			t.Errorf("nginx's include doesn't say %s", want)
		}
	}
	noStart := Default
	noStart.Channels = slices.DeleteFunc(slices.Clone(Default.Channels), func(c Channel) bool { return c.Code == "start" })
	if _, err := Build(Options{Root: os.DirFS("../.."), Settings: noStart, Now: time.Now()}); err == nil {
		t.Error("the site builds while /start's channel isn't in Settings.Channels")
	}
}

// The AI build battle's page, for people who saw the videos, most on a phone
// from a reply on X: indexed and in the sitemap, under Templates, with the
// release the template needs, and Open in my dashboard opens the template
// itself, once, counted as the template's page. It names no hosted option and says
// nothing is coming until its hosted setting says where one is; then "Or get
// it hosted" goes there, under the install command.
func TestTheAIBuildBattlePage(t *testing.T) {
	const p = "/templates/ai-build-battle"
	o := build(t, Default)
	html := pages(o)[p]
	if html == "" {
		t.Fatalf("no page at %s", p)
	}
	if strings.Contains(html, `content="noindex"`) || !strings.Contains(string(o.Files["sitemap.xml"]), "<loc>"+Default.BaseURL+p+"</loc>") {
		t.Errorf("%s isn't indexed, or isn't in the sitemap", p)
	}
	if !strings.Contains(html, `<a class="nav-link" href="/templates" aria-current="page">Templates</a>`) {
		t.Errorf("%s isn't under Templates in the header", p)
	}
	main := between(html, "<main", "</main>")
	for _, want := range []string{
		`<h1 id="page-title" class="aibb-h1 hero-rise">The AI build battle, on your own server</h1>`,
		"Needs Playkeeper 0.4.10 or newer.",
		"<code>/aibuild a castle on a cliff</code>",
		"<code>/aibattle claude gpt a castle on a cliff</code>",
		`<div class="install install-shares aibb-install" id="install" data-install>`,
		`href="/sizing"`,
		"$0.15 with GPT-6.1 Sol, $0.30 with Claude Sonnet 5.5 and $0.75 with Claude Opus 5.5",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("%s doesn't say %s", p, want)
		}
	}
	cards, err := loadTemplateCards(os.DirFS("../.."), "site/data/templates")
	if err != nil {
		t.Fatal(err)
	}
	if c := cards["ai-build-battle"]; c == nil || strings.Count(main, `href="`+c.Link+`" data-template-open="ai-build-battle"`) != 1 || strings.Count(main, `data-template-open=`) != 1 {
		t.Errorf("%s doesn't open the AI Build Battle template once, with Open in my dashboard", p)
	}
	lower := strings.ToLower(html)
	for _, bad := range []string{`href="/t/`, `href="/cloud`, "playkeeper cloud", "coming soon", "get it hosted", "bedrock"} {
		if strings.Contains(lower, bad) {
			t.Errorf("%s says %s", p, bad)
		}
	}
	if img, alt := ogOf(html); !strings.HasPrefix(img, Default.BaseURL+"/assets/og/ai-build-battle.") || !strings.HasPrefix(alt, "The two finished nightmares from the AI build battle video") {
		t.Errorf("%s's preview is %s, described as %q", p, img, alt)
	}
	hosted, err := buildEdited(t, map[string]func(string) string{battlePage: set("hosted", "/pricing")})
	if err != nil {
		t.Fatal(err)
	}
	if under := between(pages(hosted)[p], `id="install"`, "</li>"); !strings.Contains(under, `<p class="aibb-after">Or <a class="link-arrow" href="/pricing">get it hosted`) {
		t.Errorf("with hosted: /pricing, %s's install step doesn't say Or get it hosted: %q", p, under)
	}
	for _, bad := range []string{"pricing", "//pricing.example", "http://pricing.example", "javascript:alert(1)"} {
		if _, err := parsePage("{{/*\npath: /x\nhosted: " + bad + "\n*/}}"); err == nil {
			t.Errorf("a page's hosted setting can be %q", bad)
		}
	}
	for _, good := range []string{"/pricing", "https://pricing.example/minecraft"} {
		if _, err := parsePage("{{/*\npath: /x\nhosted: " + good + "\n*/}}"); err != nil {
			t.Errorf("a page's hosted setting can't be %q: %v", good, err)
		}
	}
}

func TestTemplateCardsOpenValidTemplates(t *testing.T) {
	o := build(t, Default)
	landing := pages(o)["/"]
	links := regexp.MustCompile(`href="(/t#[A-Za-z0-9_-]+)"`).FindAllStringSubmatch(landing, -1)
	if len(links) != 4 {
		t.Fatalf("the landing page has %d template links, want 4", len(links))
	}
	cards, err := loadTemplateCards(os.DirFS("../.."), "site/data/templates")
	if err != nil {
		t.Fatal(err)
	}
	byLink := map[string]*TemplateCard{}
	for _, c := range cards {
		byLink[c.Link] = c
	}
	for _, l := range links {
		c := byLink[l[1]]
		if c == nil {
			t.Errorf("the landing page links %s, which is no template in site/data/templates", l[1])
			continue
		}
		for _, want := range []string{c.Facts, c.Holds} {
			if !strings.Contains(landing, template.HTMLEscapeString(want)) {
				t.Errorf("the landing page's %s card doesn't say %q", c.ID, want)
			}
		}
	}
	for id, want := range map[string][2]string{
		"survival-with-friends": {"Paper · Minecraft 26.2 · 4 GB", "Chunky, CoreProtect and Simple Voice Chat"},
		"cobblemon":             {"Fabric · Minecraft 1.21.1 · 6 GB", "Cobblemon Official Modpack [Fabric], 75 mods"},
	} {
		if c := cards[id]; c.Facts != want[0] || c.Holds != want[1] {
			t.Errorf("the %s card says %q and %q, want %q and %q", id, c.Facts, c.Holds, want[0], want[1])
		}
	}
}

func TestImageSizes(t *testing.T) {
	w, h, err := svgSize([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 80"></svg>`))
	if err != nil || w != 120 || h != 80 {
		t.Errorf("svgSize from a viewBox: %d %d %v", w, h, err)
	}
	w, h, err = svgSize([]byte(`<?xml version="1.0"?><svg width="64px" height="32" viewBox="0 0 8 4"/>`))
	if err != nil || w != 64 || h != 32 {
		t.Errorf("svgSize from width and height: %d %d %v", w, h, err)
	}
	if _, _, err := svgSize([]byte(`<svg/>`)); err == nil {
		t.Error("an <svg> without a size has one")
	}
	// A lossless 3 × 2 WebP.
	vp8l := []byte("RIFF\x00\x00\x00\x00WEBPVP8L\x00\x00\x00\x00\x2f")
	bits := uint32(3-1) | uint32(2-1)<<14
	vp8l = append(vp8l, byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24), 0, 0, 0, 0, 0)
	if w, h, err := webpSize(vp8l); err != nil || w != 3 || h != 2 {
		t.Errorf("webpSize of a lossless image: %d %d %v", w, h, err)
	}
	if _, _, err := webpSize([]byte("RIFF....WEBPVP9 ................")); err == nil {
		t.Error("an unknown WebP chunk has a size")
	}
	if _, _, err := avifSize([]byte("\x00\x00\x00\x10ftypmif1\x00\x00\x00\x00")); err == nil {
		t.Error("an image without the avif brand has an AVIF size")
	}
	for name, a := range build(t, Default).Files {
		if !strings.HasPrefix(name, "assets/shots/") {
			continue
		}
		size := webpSize
		if path.Ext(name) == ".avif" {
			size = avifSize
		}
		if w, h, err := size(a); err != nil || w == 0 || h == 0 {
			t.Errorf("%s: %d × %d, %v", name, w, h, err)
		}
	}
}

var (
	rePicture = regexp.MustCompile(`<picture><source type="image/avif" srcset="([^"]+)" sizes="([^"]+)"><img class="shot-img[^"]*" src="([^"]+)" srcset="([^"]+)" sizes="([^"]+)" width="\d+" height="\d+" alt="[^"]*"( fetchpriority="high"| loading="lazy") decoding="async"></picture>`)
	rePreload = regexp.MustCompile(`<link rel="preload" as="image" type="image/avif" imagesrcset="([^"]+)" imagesizes="([^"]+)"(?: media="[^"]+")? fetchpriority="high">`)
)

// srcsetFiles are a srcset's files and their widths.
func srcsetFiles(srcset string) (files, widths []string) {
	for _, c := range strings.Split(srcset, ",") {
		f := strings.Fields(c)
		if len(f) != 2 || !strings.HasSuffix(f[1], "w") {
			return nil, nil
		}
		files, widths = append(files, f[0]), append(widths, f[1])
	}
	return files, widths
}

// Every screenshot is a <picture>: the same widths in AVIF and in WebP, with
// the page's sizes, fetched lazily unless it's the first thing the page
// shows, which the head asks for early with the same files and sizes. Every
// file in site/static/shots is shown, and none is heavy.
func TestScreenshotsArePicturesWithSizes(t *testing.T) {
	o := build(t, Default)
	shown := map[string]bool{}
	for p, html := range pages(o) {
		pictures := rePicture.FindAllStringSubmatch(html, -1)
		if n := strings.Count(html, `class="shot-img`); n != len(pictures) {
			t.Errorf("%s has %d screenshots, %d of them in a <picture> with an AVIF source and sizes", p, n, len(pictures))
		}
		preloads := map[string]string{}
		for _, m := range rePreload.FindAllStringSubmatch(html, -1) {
			preloads[m[1]] = m[2]
		}
		eager := 0
		for _, m := range pictures {
			avif, avifSizes, src, webp, sizes, loading := m[1], m[2], m[3], m[4], m[5], m[6]
			avifFiles, avifWidths := srcsetFiles(avif)
			webpFiles, webpWidths := srcsetFiles(webp)
			switch {
			case avifSizes != sizes:
				t.Errorf("%s: a screenshot's AVIF and WebP have different sizes: %q and %q", p, avifSizes, sizes)
			case len(avifWidths) == 0 || !slices.Equal(avifWidths, webpWidths):
				t.Errorf("%s: a screenshot's AVIF and WebP widths differ: %q and %q", p, avif, webp)
			case !slices.Contains(webpFiles, src):
				t.Errorf("%s: a screenshot's src %s isn't one of its files", p, src)
			}
			for _, f := range append(avifFiles, webpFiles...) {
				shown[f] = true
			}
			if loading == ` fetchpriority="high"` {
				eager++
				if got, ok := preloads[avif]; !ok || got != sizes {
					t.Errorf("%s shows %s first but its head doesn't ask for it with the same sizes (%q)", p, avifFiles[0], got)
				}
			}
		}
		if eager != len(preloads) {
			t.Errorf("%s shows %d screenshots first but asks for %d early", p, eager, len(preloads))
		}
	}
	entries, err := os.ReadDir("../../site/static/shots")
	if err != nil {
		t.Fatal(err)
	}
	s := &Site{}
	if s.assets, err = loadAssets(os.DirFS("../.."), map[string]string{"": "site/static"}); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		// Template thumbnails, in shots/templates, have their own test.
		if e.IsDir() {
			continue
		}
		a := s.assets["shots/"+e.Name()]
		if a == nil || !shown[a.URL] {
			t.Errorf("site/static/shots/%s isn't shown on any page", e.Name())
			continue
		}
		// Enough for a 2x screenshot of a whole screen at AVIF's or WebP's high quality.
		if len(a.data) > 200_000 {
			t.Errorf("site/static/shots/%s is %d kB; screenshots stay under 200 kB", e.Name(), len(a.data)/1000)
		}
	}
}

func TestDocsComeFromTheRepository(t *testing.T) {
	o := build(t, Default)
	built := pages(o)
	docs := built["/docs"]
	for _, g := range docGroups {
		for _, e := range g.Entries {
			if _, ok := built["/docs/"+e.Page]; ok && !strings.Contains(docs, ">"+template.HTMLEscapeString(e.Label)+"<") {
				t.Errorf("the docs landing doesn't list %q", e.Label)
			}
		}
	}
	for p, html := range built {
		if !strings.HasPrefix(p, "/docs/") {
			continue
		}
		// Links into the repository go to its files on GitHub or their
		// docs pages; none stay relative.
		for _, m := range reHref.FindAllStringSubmatch(html, -1) {
			if ref := m[1]; strings.HasSuffix(strings.Split(ref, "#")[0], ".md") && !strings.HasPrefix(ref, "https://github.com/") {
				t.Errorf("%s links to %s, a Markdown file off the site", p, ref)
			}
		}
	}
	if _, ok := built["/docs/install"]; !ok {
		t.Fatal("README.md's install section isn't a docs page")
	}
	if !strings.Contains(built["/docs/install"], "curl -fsSL https://playkeeper.io/install | sudo sh") {
		t.Error("the install docs don't have the one-line install")
	}
}

// The blog index gives each post the reading time its own page has.
func TestBlogShowsReadingTimes(t *testing.T) {
	built := pages(build(t, Default))
	reMinutes := regexp.MustCompile(`(\d+) min read`)
	post := reMinutes.FindStringSubmatch(built["/blog/playkeeper-0-4-0"])
	if post == nil || post[1] == "0" {
		t.Fatalf("the 0.4.0 post's reading time is %v", post)
	}
	if !strings.Contains(built["/blog"], post[0]) {
		t.Errorf("the blog index doesn't say %q", post[0])
	}
}

// The docs search shows titles and snippets as text, so its index holds
// text: "Can't reach the dashboard", not "Can&#39;t reach the dashboard".
func TestDocsSearchIndexIsText(t *testing.T) {
	o := build(t, Default)
	var index []byte
	for name, b := range o.Files {
		if strings.HasPrefix(name, "assets/js/docs-index.") {
			index = b
		}
	}
	js := strings.TrimSuffix(strings.TrimPrefix(string(index), "window.playkeeperDocs = "), ";\n")
	var entries []SearchEntry
	if err := json.Unmarshal([]byte(js), &entries); err != nil {
		t.Fatalf("the docs search index: %v", err)
	}
	titles := map[string]bool{}
	for _, e := range entries {
		titles[e.Title] = true
		if reEntity.MatchString(e.Title) || reEntity.MatchString(e.Text) {
			t.Errorf("the docs search index has HTML in %q: %q", e.Title, e.Text)
		}
	}
	if !titles["Can't reach the dashboard"] {
		t.Error(`the docs search doesn't find "Can't reach the dashboard"`)
	}
}

var reEntity = regexp.MustCompile(`&(#\d+|#x[0-9a-fA-F]+|[a-z]+);`)

// A page's breadcrumb links its section's hub, also when the hub is a part
// of another page, like the landing page's features, and then gives search
// engines the trail.
func TestCrumbsLinkTheirHub(t *testing.T) {
	feature := pages(build(t, Default))["/features/mods-and-modpacks"]
	if !strings.Contains(feature, `<a href="/#features">Features</a>`) {
		t.Error("the feature page's breadcrumb doesn't link Features to the landing page's features")
	}
	if !strings.Contains(feature, `"BreadcrumbList"`) {
		t.Error("the feature page has no BreadcrumbList data")
	}
}

// A guide's contents use a heading's short label when it has one, and end
// with its questions. Its install card says what the guide's side-title and
// side-text parts say, or the modded guide's words when it has none.
func TestGuideContents(t *testing.T) {
	built := pages(build(t, Default))
	for path, want := range map[string]struct {
		toc  []string
		side string
	}{
		"/guides/modded-minecraft-server":      {[]string{`<a href="#manual">The manual way</a>`, `<a href="#memory">How much memory</a>`}, "<p class=\"side-install-title\">Skip the manual steps</p>\n        <p>Playkeeper installs modpacks in one click.</p>"},
		"/guides/add-mods-to-minecraft-server": {[]string{`<a href="#loader">Can your server run mods?</a>`, `<a href="#manual">Add the files by hand</a>`}, "<p class=\"side-install-title\">Skip the manual steps</p>\n        <p>Playkeeper adds a mod and what it needs in one click.</p>"},
		"/guides/play-minecraft-with-friends":  {[]string{`<a href="#friends-list">Add friends on Java</a>`, `<a href="#own-server">Your own server</a>`}, "<p class=\"side-install-title\">Always on, no port</p>"},
		"/guides/minecraft-server-cost":        {[]string{`<a href="#vps">A VPS by size</a>`, `<a href="#cheapest">The cheapest reliable setup</a>`}, "<p class=\"side-install-title\">Only the VPS to pay for</p>"},
	} {
		guide := built[path]
		start := strings.Index(guide, `<nav class="toc" data-toc>`)
		if start < 0 {
			t.Errorf("%s has no contents", path)
			continue
		}
		toc := guide[start : start+strings.Index(guide[start:], "</nav>")]
		for _, w := range append(want.toc, `<a href="#questions">Questions</a></li></ol>`) {
			if !strings.Contains(toc, w) {
				t.Errorf("%s's contents lack %s", path, w)
			}
		}
		if !strings.Contains(guide, want.side) {
			t.Errorf("%s's install card doesn't say %q", path, want.side)
		}
	}
}

// The cost guide's VPS prices are the providers' plans, dated with the day
// they were checked: each of its groups needs what the sizing guide says,
// and shows the plan that fits at each provider, with its price. "From" is
// the cheapest plan for the sizing guide's smallest answer, on the cost
// guide and the friends guide alike.
func TestCostGuideFollowsTheSizingGuideAndThePlans(t *testing.T) {
	built := pages(build(t, Default))
	cost, friends := built["/guides/minecraft-server-cost"], built["/guides/play-minecraft-with-friends"]
	for _, p := range providers {
		for _, pl := range p.Plans {
			if pl.USD <= 0 || (pl.Renews != 0 && pl.Renews <= pl.USD) {
				t.Errorf("%s %s has no price, or renews for less than its first price: %+v", p.Name, pl.Name, pl)
			}
		}
	}
	if !strings.Contains(cost, "checked "+Day(checkedProviders)+" on each provider") {
		t.Errorf("the cost guide doesn't date its VPS prices with %s", Day(checkedProviders))
	}
	for _, g := range []struct {
		players  int
		workload sizing.Workload
	}{{4, sizing.Vanilla}, {8, sizing.AddOns}, {4, sizing.Modpack}} {
		r, err := sizing.Recommend(g.workload, g.players)
		if err != nil {
			t.Fatal(err)
		}
		row := between(cost, `<th scope="row">`+r.Band.Label()+" friends "+sizingPhrase(r)+"</th>", "</tr>")
		if !strings.Contains(row, fmt.Sprintf(`<td data-col="Needs">%d GB · %d cores</td>`, r.MemoryGB, r.Cores)) {
			t.Errorf("the cost guide has no row for %s friends %s needing %d GB and %d cores: %q", r.Band.Label(), sizingPhrase(r), r.MemoryGB, r.Cores, row)
			continue
		}
		for _, p := range providers {
			pl := p.Fit(r.MemoryGB, r.Cores)
			want := fmt.Sprintf("%s, %d GB: <strong>%s</strong>", pl.Name, pl.MemoryGB, usd(pl.USD))
			if pl.Renews != 0 {
				want += ", then " + usd(pl.Renews)
			}
			if !strings.Contains(row, `<td data-col="`+p.Name+`"><span>`+want+"</span></td>") {
				t.Errorf("the cost guide's %s row doesn't show %s's %s", r.Band.Label()+" "+string(g.workload), p.Name, want)
			}
		}
	}
	r, err := sizing.Recommend(sizing.Vanilla, 1)
	if err != nil {
		t.Fatal(err)
	}
	from := cheapestFit(r.MemoryGB, r.Cores)
	for _, p := range providers {
		if pl := p.Fit(r.MemoryGB, r.Cores); pl.Name != "" && pl.USD < from.USD {
			t.Errorf("%s's %s costs less than the cheapest fit, %s's %s", p.Name, pl.Name, from.Provider, from.Name)
		}
	}
	if want := fmt.Sprintf("From %s for %d GB", usd(from.USD), from.MemoryGB); !strings.Contains(cost, want) {
		t.Errorf("the cost guide doesn't say %q", want)
	}
	if want := "From " + usd(from.USD) + " a month on a VPS"; !strings.Contains(friends, want) {
		t.Errorf("the friends guide doesn't say %q", want)
	}
	for v, want := range map[float64]string{20: "$20", 8.99: "$8.99", 252: "$252", 14.5: "$14.50"} {
		if got := usd(v); got != want {
			t.Errorf("usd(%v) = %q, want %q", v, got, want)
		}
	}
}

// The landing page's card for people paying a game host holds only with the
// cost guide's prices: the cheapest VPS it shows has at least twice the
// memory of a premium host's 4 GB plan (Apex Hosting's, in the cost guide),
// and costs no more than that plan, even after its first term renews.
func TestPaidHostCardFollowsTheCostGuide(t *testing.T) {
	built := pages(build(t, Default))
	landing, cost := built["/"], built["/guides/minecraft-server-cost"]
	r, err := sizing.Recommend(sizing.Vanilla, 1)
	if err != nil {
		t.Fatal(err)
	}
	vps := cheapestFit(r.MemoryGB, r.Cores)
	if want := fmt.Sprintf("%d GB on a VPS costs about what a premium host charges for 4 GB", vps.MemoryGB); !strings.Contains(landing, want) {
		t.Errorf("the landing page doesn't say %q", want)
	}
	if vps.MemoryGB < 8 {
		t.Errorf("the cheapest VPS, %s's %s, has %d GB, less than twice a premium host's 4 GB", vps.Provider, vps.Name, vps.MemoryGB)
	}
	m := regexp.MustCompile(`Apex Hosting's 4 GB plan costs \$(\d+\.\d\d)`).FindStringSubmatch(cost)
	if m == nil {
		t.Fatal("the cost guide no longer gives the premium 4 GB price the landing page compares with")
	}
	premium, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatal(err)
	}
	if most := max(vps.USD, vps.Renews); most > premium {
		t.Errorf("%s's %s costs up to %s a month, more than a premium host's 4 GB at %s", vps.Provider, vps.Name, usd(most), usd(premium))
	}
}

// The sizing guide's table works without JavaScript: a row for each number of
// friends at once, a column for each thing they run, and each size as
// internal/sizing works it out.
func TestSizingTableFollowsTheSizingGuide(t *testing.T) {
	page := pages(build(t, Default))["/sizing"]
	for _, w := range sizing.Workloads() {
		if !strings.Contains(page, `<th scope="col">`+template.HTMLEscapeString(w.Label())+`</th>`) {
			t.Errorf("the table has no column for %s", w.Label())
		}
	}
	for _, r := range sizing.Table() {
		id := "size-" + r.Band.Key() + "-" + string(r.Workload)
		want := fmt.Sprintf(`<td id="%s"><span class="mem">%d GB<span class="visually-hidden">,</span></span> <span class="more"><span class="part">%d cores</span>`, id, r.MemoryGB, r.Cores)
		if !strings.Contains(page, want) {
			t.Errorf("the table's %s isn't %d GB and %d cores", id, r.MemoryGB, r.Cores)
		}
		if !strings.Contains(page, fmt.Sprintf("%d GB disk</span>", r.DiskGB)) {
			t.Errorf("the table has no %d GB disk for %s", r.DiskGB, id)
		}
	}
	first, err := sizing.Recommend(sizingWorkload, sizingPlayers)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`id="answer-long">` + first.Title + `<`,
		fmt.Sprintf("Playkeeper itself needs at least %d CPU cores, %d GB of memory and %d GB of free disk.", sizing.MinCores, sizing.MinMemoryGB, sizing.MinFreeDiskGB),
		fmt.Sprintf("a VPS with %d GB of memory", first.MemoryGB),
	} {
		if !strings.Contains(page, want) {
			t.Errorf("/sizing doesn't say %q", want)
		}
	}
}

// sizing-data.js has every answer the table has, as internal/sizing words it,
// with each provider's plans, in ASCII whatever the page is served as.
func TestSizingDataHasEveryAnswer(t *testing.T) {
	o := build(t, Default)
	var js string
	for name, b := range o.Files {
		if strings.HasPrefix(name, "assets/js/sizing-data.") {
			js = string(b)
		}
	}
	for i := 0; i < len(js); i++ {
		if js[i] >= 0x80 {
			t.Fatalf("sizing-data.js has a byte past ASCII at %d", i)
		}
	}
	_, body, ok := strings.Cut(js, "window.playkeeperSizing = ")
	if !ok {
		t.Fatal("sizing-data.js doesn't set window.playkeeperSizing")
	}
	var data struct {
		Answers map[string]map[string]SizingAnswer
		Plans   map[string][]sizingPlan
	}
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimSpace(body), ";")), &data); err != nil {
		t.Fatal(err)
	}
	for _, r := range sizing.Table() {
		a, ok := data.Answers[r.Band.Key()][string(r.Workload)]
		if !ok {
			t.Errorf("no answer for %s friends on %s", r.Band.Key(), r.Workload)
			continue
		}
		if a.Title != r.Title || a.Summary != r.Summary || a.MemoryGB != r.MemoryGB || a.Cores != r.Cores || len(a.Reasons) != len(r.Reasons) {
			t.Errorf("%s/%s: the answer %+v doesn't match %+v", r.Band.Key(), r.Workload, a, r)
			continue
		}
		for i, x := range r.Reasons {
			if a.Reasons[i] != (SizingReason{Label: x.Topic.Label(), Value: x.Value, Text: x.Text}) {
				t.Errorf("%s/%s: reason %+v, want %+v", r.Band.Key(), r.Workload, a.Reasons[i], x)
			}
		}
	}
	if fit := data.Answers["11-20"]["modpack"].Fit; fit != "Fits your answer, 11–20 friends on a big modpack: 24 GB of memory, 6 fast cores" {
		t.Errorf("the providers' line for 11–20 friends on a big modpack is %q", fit)
	}
	for _, p := range providers {
		if len(data.Plans[p.Name]) != len(p.Plans) {
			t.Errorf("sizing-data.js has %d of %s's %d plans", len(data.Plans[p.Name]), p.Name, len(p.Plans))
		}
	}
}

// Structured data is JSON: html/template takes application/ld+json for a
// JavaScript type and JSON-encodes what the layouts put there. Every block on
// every page parses, none is Go's map text, and the pages that need them have
// theirs: software on the landing page, an article, a post, an FAQ and
// breadcrumbs, with their nested parts as JSON arrays and objects.
func TestStructuredDataIsJSON(t *testing.T) {
	built := pages(build(t, Default))
	blocks := func(p string) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, m := range reLD.FindAllStringSubmatch(built[p], -1) {
			var v map[string]any
			if err := json.Unmarshal([]byte(m[1]), &v); err != nil {
				t.Errorf("%s has structured data that isn't JSON: %v: %.120s", p, err, m[1])
				continue
			}
			if strings.Contains(m[1], "map[") {
				t.Errorf("%s has structured data with Go's map text in it: %.120s", p, m[1])
			}
			typ, _ := v["@type"].(string)
			out[typ] = v
		}
		return out
	}
	for p := range built {
		blocks(p)
	}
	for p, types := range map[string][]string{
		"/":                                    {"SoftwareApplication", "FAQPage"},
		"/guides/modded-minecraft-server":      {"Article", "FAQPage"},
		"/guides/add-mods-to-minecraft-server": {"Article", "FAQPage"},
		"/guides/play-minecraft-with-friends":  {"Article", "FAQPage"},
		"/guides/minecraft-server-cost":        {"Article", "FAQPage"},
		"/blog/playkeeper-0-4-0":               {"BlogPosting", "BreadcrumbList"},
		"/features/mods-and-modpacks":          {"BreadcrumbList", "FAQPage"},
	} {
		got := blocks(p)
		for _, typ := range types {
			if got[typ] == nil {
				t.Errorf("%s has no %s structured data", p, typ)
			}
		}
	}
	// Validators check each property against the type: codeRepository, say,
	// is SoftwareSourceCode's, not SoftwareApplication's.
	softwareProperties := []string{"@context", "@type", "name", "description", "url", "image", "sameAs",
		"applicationCategory", "applicationSubCategory", "operatingSystem", "processorRequirements", "memoryRequirements",
		"storageRequirements", "softwareVersion", "softwareRequirements", "downloadUrl", "installUrl", "featureList",
		"screenshot", "releaseNotes", "license", "isAccessibleForFree", "offers", "author", "publisher", "aggregateRating", "review"}
	for property := range blocks("/")["SoftwareApplication"] {
		if !slices.Contains(softwareProperties, property) {
			t.Errorf("the landing page's SoftwareApplication has %q, which schema.org doesn't give that type", property)
		}
	}
	faq := blocks("/")["FAQPage"]
	questions, _ := faq["mainEntity"].([]any)
	if len(questions) == 0 {
		t.Fatal("the landing page's FAQ data has no questions")
	}
	first, _ := questions[0].(map[string]any)
	answer, _ := first["acceptedAnswer"].(map[string]any)
	if first["@type"] != "Question" || first["name"] == "" || answer["@type"] != "Answer" || answer["text"] == "" {
		t.Errorf("the landing page's first FAQ entry isn't a question with an answer: %v", first)
	}
	crumbs, _ := blocks("/features/mods-and-modpacks")["BreadcrumbList"]["itemListElement"].([]any)
	if len(crumbs) != 2 {
		t.Fatalf("the feature page's breadcrumbs have %d items, want 2", len(crumbs))
	}
	if top, _ := crumbs[0].(map[string]any); top["position"] != 1.0 || top["name"] != "Features" || top["item"] != "https://playkeeper.io/#features" {
		t.Errorf("the feature page's first breadcrumb is %v", top)
	}
}

var (
	reCopy        = regexp.MustCompile(`\sdata-copy="([^"]*)"`)
	reInstallLine = regexp.MustCompile(`<span class="install-line">([^<]*)</span>`)
)

// The install box's own attribute, not the header's data-install-link.
const installBox = " data-install>"

// The install command is one setting: every page shows the same one, in its
// install boxes, Copy buttons and terminals, and follows the setting when it
// changes. A page with a channel, /start, shows that channel's everywhere.
// Docs pages say what the repository's Markdown says.
func TestInstallCommandIsOneSetting(t *testing.T) {
	channelOf := map[string]string{"/start": "start"}
	for p, html := range pages(build(t, Default)) {
		want, err := installFor(Default.InstallCommand, channelOf[p])
		if err != nil {
			t.Fatal(err)
		}
		shown := 0
		for _, m := range reInstallLine.FindAllStringSubmatch(html, -1) {
			shown++
			if got := unescape(m[1]); got != want {
				t.Errorf("%s shows the install command as %q", p, got)
			}
		}
		for _, m := range reCopy.FindAllStringSubmatch(html, -1) {
			if got := unescape(m[1]); strings.HasPrefix(got, "curl ") && got != want {
				t.Errorf("%s copies %q as the install command", p, got)
			}
		}
		if strings.Contains(html, installBox) && shown == 0 {
			t.Errorf("%s has an install box without the command", p)
		}
	}
	other := Default
	other.InstallCommand = "curl -fsSL https://example.test/install | sudo bash"
	for p, html := range pages(build(t, other)) {
		if strings.HasPrefix(p, "/docs/") {
			continue
		}
		if strings.Contains(html, "playkeeper.io/install") {
			t.Errorf("%s still shows playkeeper.io/install with another install command set", p)
		}
		want, err := installFor(other.InstallCommand, channelOf[p])
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(html, installBox) && !strings.Contains(html, `<span class="install-line">`+template.HTMLEscapeString(want)+`</span>`) {
			t.Errorf("%s doesn't show the install command that's set", p)
		}
	}
	for _, want := range []string{"curl -fsSL", "https://example.test/install", "| sudo bash"} {
		if lines := installLines(other.InstallCommand); !slices.Contains(lines, want) {
			t.Errorf("a phone's install lines %q lack %q", lines, want)
		}
	}
}

// site/README.md's Coolify settings are the ones the image builds with: the
// repository root, site/Dockerfile and its port, and its Go and Node stages
// on the versions in scripts/toolchains.txt, as its Updating notes say.
func TestHostingNotesMatchTheDockerfile(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile("../../" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	readme, dockerfile, toolchains := read("site/README.md"), read("site/Dockerfile"), read("scripts/toolchains.txt")
	for _, want := range []string{
		"**Base Directory**: `/`", "**Dockerfile Location**: `/site/Dockerfile`", "**Ports Exposes**: `80`",
		"**Base Directory** from `/site` to `/`", "**Dockerfile Location** from `/Dockerfile` to `/site/Dockerfile`",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("site/README.md doesn't say %s", want)
		}
	}
	if !strings.Contains(dockerfile, "\nEXPOSE 80\n") {
		t.Error("site/Dockerfile doesn't expose port 80, which site/README.md has Coolify use")
	}
	for _, tool := range []struct{ name, image string }{{"go", "golang"}, {"node", "node"}} {
		m := regexp.MustCompile(`(?m)^` + tool.name + ` (\S+)$`).FindStringSubmatch(toolchains)
		if m == nil {
			t.Fatalf("scripts/toolchains.txt has no %s version", tool.name)
		}
		if !strings.Contains(dockerfile, "FROM "+tool.image+":"+m[1]+"-alpine@sha256:") {
			t.Errorf("site/Dockerfile doesn't build on %s:%s-alpine, the %s in scripts/toolchains.txt", tool.image, m[1], tool.name)
		}
	}
}

// The docs name only make targets the Makefile has: CONTRIBUTING.md and the
// rest become docs pages, and a command that's gone reads as a broken step.
func TestDocsNameRealMakeTargets(t *testing.T) {
	makefile, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^([a-z0-9-]+):`).FindAllStringSubmatch(string(makefile), -1) {
		targets[m[1]] = true
	}
	reMake := regexp.MustCompile("`make ([a-z0-9-]+)[^`]*`")
	for _, name := range []string{"README.md", "CONTRIBUTING.md", "SECURITY.md", "docs/RECOVERY.md", "docs/TROUBLESHOOTING.md", "site/README.md"} {
		b, err := os.ReadFile("../../" + name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range reMake.FindAllStringSubmatch(string(b), -1) {
			if !targets[m[1]] {
				t.Errorf("%s names make %s, which the Makefile doesn't have", name, m[1])
			}
		}
	}
}

// Bold lead-ins get their anchors in tight lists as in paragraphs: README.md
// lists its parts, and the docs landing links to them.
func TestLeadInsInListsGetAnchors(t *testing.T) {
	for _, md := range []string{"- **Backups:** one\n- **Schedules:** two\n", "- **Backups:** one\n\n- **Schedules:** two\n", "**Backups:** one\n\n**Schedules:** two\n"} {
		html, err := renderMarkdown(md, "README.md", map[string]string{}, map[string]string{}, Default)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"backups", "schedules"} {
			if !strings.Contains(html, ` id="`+id+`"`) {
				t.Errorf("%q has no anchor #%s: %s", md, id, html)
			}
		}
	}
}
