package site

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

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

func TestEveryPageIsWellFormed(t *testing.T) {
	o := build(t, Default)
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
		desc := between(html, `<meta name="description" content="`, `"`)
		if len(title) < 10 || len(title) > 70 {
			t.Errorf("%s: title %q is %d characters, want 10 to 70", p, title, len(title))
		}
		if len(desc) < 30 || len(desc) > 170 {
			t.Errorf("%s: description is %d characters, want 30 to 170", p, len(desc))
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
		"/guides/modded-minecraft-server", "/sizing", "/docs", "/pricing", "/blog", "/blog/playkeeper-0-4-0", "/t", "/404"} {
		if _, ok := built[p]; !ok {
			t.Errorf("there is no %s", p)
		}
	}
	for p, html := range built {
		if p == "/404" || strings.HasPrefix(p, "/docs") || p == "/t" {
			continue
		}
		if !strings.Contains(html, "curl -fsSL https://playkeeper.io/install | sudo sh") {
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
	for _, line := range []string{"Allow: /demo/$", "Disallow: /demo/", "Sitemap: https://playkeeper.io/sitemap.xml"} {
		if !strings.Contains(robots, line+"\n") {
			t.Errorf("robots.txt doesn't say %q", line)
		}
	}
	if !strings.Contains(string(o.Files["blog/feed.xml"]), "https://playkeeper.io/blog/playkeeper-0-4-0") {
		t.Error("the blog's feed doesn't have the 0.4.0 post")
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
	for _, forge := range []bool{false, true} {
		if _, err := os.Stat("../../web/src/assets/logos/forge-apple-touch-icon.png"); forge && err != nil {
			t.Log("Forge's logo isn't in web/src/assets/logos yet, so Forge can't be shown")
			continue
		}
		setForge(forge)
		n := len(serverTypes(textOrder))
		built := pages(build(t, Default))
		landing, feature, guide := built["/"], built["/features/mods-and-modpacks"], built["/guides/modded-minecraft-server"]
		if !strings.Contains(landing, countWord(n)+" server types, add-ons in one click.") {
			t.Errorf("forge %v: the landing page doesn't say %s server types", forge, countWord(n))
		}
		if got := strings.Count(feature, `<span class="logo-tile">`); got != n {
			t.Errorf("forge %v: the logo row has %d logos, want %d", forge, got, n)
		}
		hasForge := strings.Contains(feature, "<span>Forge</span>")
		if hasForge != forge {
			t.Errorf("forge %v: the logo row shows Forge: %v", forge, hasForge)
		}
		for _, p := range []string{landing, feature, guide, built["/blog/playkeeper-0-4-0"]} {
			if strings.Contains(p, "Forge isn") || strings.Contains(p, "No Forge") {
				t.Errorf("forge %v: a page still says Forge isn't in Playkeeper", forge)
			}
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

// The share page makes no requests: it loads only site.js and t.js, and no
// star count.
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
	site := string(o.Files[strings.TrimPrefix(scripts[0][1], "/")])
	if n := strings.Count(site, "fetch("); n != 1 || !strings.Contains(site, "var star = $('[data-stars]');\n  if (star && window.fetch)") {
		t.Error("site.js makes a request other than the star count, or makes it without the header asking for it")
	}
}

func TestTemplateCardsOpenValidTemplates(t *testing.T) {
	o := build(t, Default)
	landing := pages(o)["/"]
	links := regexp.MustCompile(`href="/t#([A-Za-z0-9_-]+)"`).FindAllStringSubmatch(landing, -1)
	if len(links) != 4 {
		t.Fatalf("the landing page has %d template links, want 4", len(links))
	}
	for _, want := range []string{"Paper · Minecraft 26.2 · 4 GB", "Fabric · Minecraft 1.21.1 · 6 GB", "Cobblemon Official Modpack [Fabric], 75 mods", "Chunky, CoreProtect and Simple Voice Chat"} {
		if !strings.Contains(landing, want) {
			t.Errorf("the landing page's template cards don't say %q", want)
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
	for name, a := range build(t, Default).Files {
		if strings.HasPrefix(name, "assets/shots/") && path.Ext(name) == ".webp" {
			if _, _, err := webpSize(a); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	}
}

func TestDocsComeFromTheRepository(t *testing.T) {
	o := build(t, Default)
	built := pages(o)
	docs := built["/docs"]
	for _, g := range docGroups {
		for _, e := range g.Entries {
			for _, target := range append([]DocTarget{{e.Page, e.Anchor}}, e.Or...) {
				if _, ok := built["/docs/"+target.Page]; ok {
					if !strings.Contains(docs, ">"+template.HTMLEscapeString(e.Label)+"<") {
						t.Errorf("the docs landing doesn't list %q", e.Label)
					}
					break
				}
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

// readme is this repository with README.md replaced.
type readme struct {
	fs.FS
	md string
}

func (r readme) Open(name string) (fs.File, error) {
	if name == "README.md" {
		return fstest.MapFS{name: {Data: []byte(r.md)}}.Open(name)
	}
	return r.FS.Open(name)
}

// A docs entry that README.md covers inside another section points there,
// and at its own page once README.md gives it a section.
func TestDocsEntriesFollowTheReadme(t *testing.T) {
	md, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(md), "\n## Addresses\n") {
		t.Skip("README.md has its own Addresses section")
	}
	for _, c := range []struct{ md, want string }{
		{string(md), `href="/docs/install#a-name-instead-of-the-ip"`},
		{string(md) + "\n## Addresses\n\nA free name or your own domain.\n", `href="/docs/addresses"`},
	} {
		o, err := Build(Options{Root: readme{os.DirFS("../.."), c.md}, Settings: Default, Now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
		if docs := pages(o)["/docs"]; !strings.Contains(docs, c.want) {
			t.Errorf("the docs landing's Addresses doesn't link %s", c.want)
		}
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
// with its questions.
func TestGuideContents(t *testing.T) {
	guide := pages(build(t, Default))["/guides/modded-minecraft-server"]
	start := strings.Index(guide, `<nav class="toc" data-toc>`)
	if start < 0 {
		t.Fatal("the modded server guide has no contents")
	}
	toc := guide[start : start+strings.Index(guide[start:], "</nav>")]
	for _, want := range []string{`<a href="#manual">The manual way</a>`, `<a href="#memory">How much memory</a>`, `<a href="#questions">Questions</a>`} {
		if !strings.Contains(toc, want) {
			t.Errorf("the guide's contents lack %s", want)
		}
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
		"/":                               {"SoftwareApplication", "FAQPage"},
		"/guides/modded-minecraft-server": {"Article", "FAQPage"},
		"/blog/playkeeper-0-4-0":          {"BlogPosting", "BreadcrumbList"},
		"/features/mods-and-modpacks":     {"BreadcrumbList", "FAQPage"},
	} {
		got := blocks(p)
		for _, typ := range types {
			if got[typ] == nil {
				t.Errorf("%s has no %s structured data", p, typ)
			}
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
