// Package site builds playkeeper.io: the pages in site/pages, laid out by the
// templates in site/layouts, the docs from the repository's own Markdown, and
// the assets in site/static and web/src/assets under hashed names. Facts the
// product owns come from the product: the server types from minecraft.Types,
// the templates' links from internal/templates, the version from
// CHANGELOG.md.
package site

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	texttemplate "text/template"
	"time"
)

// Options say what to build the site from.
type Options struct {
	// Root is the repository.
	Root     fs.FS
	Settings Settings
	// Now dates the pages that don't carry a day of their own in the
	// sitemap, and the footer's year.
	Now time.Time
}

// Output is the built site.
type Output struct {
	// Files are the web root's files, by path, like "pricing.html" or
	// "assets/css/site.1a2b3c4d.css".
	Files map[string][]byte
	// Nginx is included in nginx.conf's server block: the headers and
	// redirects that follow the settings.
	Nginx []byte
}

// Site is the site being built, for the templates.
type Site struct {
	opts    Options
	Version string
	pages   []*Page
	byPath  map[string]*Page
	assets  assets
	cards   map[string]*TemplateCard
	docs    *docsBuild
	posts   []*Page
	root    *template.Template
}

// Build builds the whole site.
func Build(o Options) (*Output, error) {
	s := &Site{opts: o, byPath: map[string]*Page{}}
	var err error
	if s.Version, err = releaseVersion(o.Root); err != nil {
		return nil, err
	}
	if s.assets, err = loadAssets(o.Root, map[string]string{"": "site/static", "app": "web/src/assets"}); err != nil {
		return nil, err
	}
	if s.cards, err = loadTemplateCards(o.Root, "site/data/templates"); err != nil {
		return nil, err
	}
	if s.pages, err = loadPages(o.Root, "site/pages"); err != nil {
		return nil, err
	}
	if s.docs, err = buildDocs(o.Root, o.Settings); err != nil {
		return nil, err
	}
	s.pages = append(s.pages, s.docs.pages...)
	for _, p := range s.pages {
		// Settings can use the templates' functions, so a count in a
		// description follows the product like the page does.
		for _, f := range []*string{&p.Title, &p.Description, &p.Summary} {
			if *f, err = s.expand(*f); err != nil {
				return nil, fmt.Errorf("%s: %w", p.Path, err)
			}
		}
		if _, dup := s.byPath[p.Path]; dup {
			return nil, fmt.Errorf("two pages are at %s", p.Path)
		}
		s.byPath[p.Path] = p
		if p.Layout == "post" {
			s.posts = append(s.posts, p)
		}
	}
	sort.SliceStable(s.posts, func(i, j int) bool { return s.posts[i].Published > s.posts[j].Published })
	if err := s.addSearchIndex(); err != nil {
		return nil, err
	}
	if s.root, err = s.parseLayouts(); err != nil {
		return nil, err
	}

	out := &Output{Files: map[string][]byte{}}
	// Articles first: rendering one works out its reading time and contents,
	// which the blog index and the cards that list it show.
	order := slices.Clone(s.pages)
	slices.SortStableFunc(order, func(a, b *Page) int { return cmp.Compare(articleFirst(a), articleFirst(b)) })
	for _, p := range order {
		html, err := s.render(p)
		if err != nil {
			src := p.file
			if p.docs != nil {
				src = p.docs.Source
			}
			return nil, fmt.Errorf("%s (%s): %w", p.Path, src, err)
		}
		out.Files[p.outFile()] = html
	}
	for _, a := range s.assets {
		out.Files[strings.TrimPrefix(a.URL, "/")] = a.data
	}
	fav, err := fs.ReadFile(o.Root, "site/static/favicon.svg")
	if err != nil {
		return nil, err
	}
	out.Files["favicon.svg"] = fav
	day := today(o.Now)
	out.Files["sitemap.xml"] = sitemap(o.Settings.BaseURL, s.pages, day)
	out.Files["robots.txt"] = robots(o.Settings.BaseURL)
	out.Files["blog/feed.xml"] = feed(o.Settings, s.posts)
	out.Nginx = nginxInclude(o.Settings)
	return out, nil
}

var reRelease = regexp.MustCompile(`(?m)^## (\d+\.\d+\.\d+)\s*$`)

// releaseVersion is the newest version CHANGELOG.md has a section for: the
// release the site describes.
func releaseVersion(root fs.FS) (string, error) {
	b, err := fs.ReadFile(root, "CHANGELOG.md")
	if err != nil {
		return "", err
	}
	m := reRelease.FindSubmatch(b)
	if m == nil {
		return "", fmt.Errorf("CHANGELOG.md has no ## X.Y.Z section")
	}
	return string(m[1]), nil
}

// expand runs a page setting that uses the templates' functions, like
// {{countWord typeCount}}.
func (s *Site) expand(v string) (string, error) {
	if !strings.Contains(v, "{{") {
		return v, nil
	}
	t, err := texttemplate.New("setting").Funcs(texttemplate.FuncMap(s.funcs())).Parse(v)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, nil); err != nil {
		return "", err
	}
	return b.String(), nil
}

// addSearchIndex publishes the docs search's index as a script.
func (s *Site) addSearchIndex() error {
	b, err := json.Marshal(s.docs.search)
	if err != nil {
		return err
	}
	x, err := newAsset("js/docs-index.js", []byte("window.playkeeperDocs = "+string(b)+";\n"))
	if err != nil {
		return err
	}
	s.assets["js/docs-index.js"] = x
	return nil
}

func (s *Site) parseLayouts() (*template.Template, error) {
	root := template.New("site").Funcs(s.funcs())
	err := fs.WalkDir(s.opts.Root, "site/layouts", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(name) != ".html" {
			return err
		}
		b, err := fs.ReadFile(s.opts.Root, name)
		if err != nil {
			return err
		}
		_, err = root.New(name).Parse(string(b))
		return err
	})
	return root, err
}

// View is what a page's templates see.
type View struct {
	Site     *Site
	Page     *Page
	Settings Settings
	// Canonical is the page's address; Image its social preview's.
	Canonical, Image string
	// Head is the page's own structured data.
	Head any
	// Main, Article and After are the page's parts, rendered, for its
	// layout.
	Main, Article, After template.HTML
	// Short is a guide's short answer, for readers and AI overviews.
	Short template.HTML
	// ClosingTitle and ClosingSub replace the closing band's words.
	ClosingTitle, ClosingSub template.HTML
	// Related is a guide's or post's Keep reading, after its article.
	Related template.HTML
	// HasInstall is whether the page shows the install command before the
	// closing band, which then leaves #install to it.
	HasInstall bool
	// HasQuestions says the page has an FAQ at #questions, for a guide's
	// contents.
	HasQuestions bool
	Year         int
	Crumbs       []Crumb
}

func (s *Site) render(p *Page) ([]byte, error) {
	t, err := s.root.Clone()
	if err != nil {
		return nil, err
	}
	if p.source != "" {
		if _, err := t.New(p.file).Parse(p.source); err != nil {
			return nil, err
		}
	}
	v := &View{Site: s, Page: p, Settings: s.opts.Settings, Canonical: p.URL(s.opts.Settings.BaseURL), Year: s.opts.Now.Year()}
	og := firstOf(p.OG, "default")
	img, ok := s.assets["og/"+og+".png"]
	if !ok {
		return nil, fmt.Errorf("no social preview image og/%s.png", og)
	}
	v.Image = s.opts.Settings.BaseURL + img.URL
	if v.Head, err = pageSchema(s.opts.Settings, p, s.Version, v.Image); err != nil {
		return nil, err
	}
	v.Crumbs = s.crumbs(p)
	part := func(name string) (template.HTML, error) {
		if t.Lookup(name) == nil {
			return "", nil
		}
		var b bytes.Buffer
		if err := t.ExecuteTemplate(&b, name, v); err != nil {
			return "", err
		}
		return template.HTML(b.String()), nil
	}
	switch p.Layout {
	case "docs":
		if p.body != "" {
			v.Main = p.body
		} else if v.Main, err = part("main"); err != nil {
			return nil, err
		}
	case "guide", "post":
		if v.Article, err = part("article"); err != nil {
			return nil, err
		}
		p.toc = headings(string(v.Article))
		p.minutes = readingMinutes(string(v.Article))
		if v.After, err = part("after"); err != nil {
			return nil, err
		}
	default:
		if v.Main, err = part("main"); err != nil {
			return nil, err
		}
	}
	for name, dst := range map[string]*template.HTML{"short": &v.Short, "closing-title": &v.ClosingTitle, "closing-sub": &v.ClosingSub, "keep-reading": &v.Related} {
		if *dst, err = part(name); err != nil {
			return nil, err
		}
	}
	v.HasInstall = strings.Contains(string(v.Main)+string(v.Article)+string(v.After), `id="install"`)
	v.HasQuestions = strings.Contains(string(v.After), `id="questions"`)
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, "layout", v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// crumbs is the page's breadcrumb: its section, then the page.
func (s *Site) crumbs(p *Page) []Crumb {
	if p.Crumb == "" {
		return nil
	}
	parent := Crumb{Label: p.Crumb}
	for _, n := range navSections {
		if n.Label == p.Crumb {
			hubPage, _, _ := strings.Cut(n.Hub, "#")
			switch {
			case n.Link != "":
				parent.Path = n.Link
			case s.byPath[hubPage] != nil:
				parent.Path = n.Hub
			}
		}
	}
	if p.Crumb == "Blog" {
		parent.Path = "/blog"
	}
	return []Crumb{parent, {Label: p.Label, Path: p.Path}}
}

var (
	reHeading = regexp.MustCompile(`(?s)<h2 id="([^"]+)"([^>]*)>(?:\s*<span class="n"[^>]*>([^<]*)</span>)?(.*?)</h2>`)
	reTOC     = regexp.MustCompile(`\sdata-toc="([^"]*)"`)
	reWords   = regexp.MustCompile(`[\p{L}\p{N}]+`)
)

// headings lists a guide's numbered sections, for its contents.
func headings(article string) []Heading {
	var out []Heading
	for _, m := range reHeading.FindAllStringSubmatch(article, -1) {
		text := strings.TrimSpace(unescape(reTags.ReplaceAllString(m[4], "")))
		if short := reTOC.FindStringSubmatch(m[2]); short != nil {
			text = unescape(short[1])
		}
		out = append(out, Heading{ID: m[1], Number: strings.TrimSpace(m[3]), Text: text})
	}
	return out
}

func articleFirst(p *Page) int {
	if p.Layout == "guide" || p.Layout == "post" {
		return 0
	}
	return 1
}

// readingMinutes is how long an article takes to read, at 230 words a minute.
func readingMinutes(article string) int {
	n := len(reWords.FindAllString(reTags.ReplaceAllString(article, " "), -1))
	return max(1, (n+229)/230)
}

// NavItem is one header item as a page shows it.
type NavItem struct {
	NavSection
	Active bool
	// Pages are the section's pages that exist, for its menu.
	Pages []*Page
	// HubPath is the overview's address, when it exists.
	HubPath string
}

func (s *Site) nav(p *Page) []NavItem {
	var out []NavItem
	for _, n := range navSections {
		item := NavItem{NavSection: n, Active: p != nil && p.Section == n.Key}
		for _, path := range n.Items {
			if q := s.byPath[path]; q != nil {
				item.Pages = append(item.Pages, q)
			}
		}
		if n.Hub != "" && s.exists(n.Hub) {
			item.HubPath = n.Hub
		}
		out = append(out, item)
	}
	return out
}

// exists reports whether a site address answers: a page, an anchor of a page
// or one of the site's other routes.
func (s *Site) exists(addr string) bool {
	path, frag, _ := strings.Cut(addr, "#")
	switch path {
	case "/demo/", "/install", "/t":
		return frag == ""
	}
	p, ok := s.byPath[path]
	if !ok {
		return false
	}
	if frag == "" {
		return true
	}
	// Anchors are checked in the built pages by the site's tests.
	return p != nil
}

type footerView struct {
	Title string
	Links []FooterLink
}

func (s *Site) footer() []footerView {
	var out []footerView
	for _, c := range footerColumns(s.opts.Settings) {
		v := footerView{Title: c.Title}
		for _, l := range c.Links {
			switch {
			case l.URL != "":
			case s.exists(l.Path):
				l.URL = l.Path
			case l.Else != "":
				l.URL = l.Else
			default:
				continue
			}
			v.Links = append(v.Links, l)
		}
		out = append(out, v)
	}
	return out
}

func (s *Site) funcs() template.FuncMap {
	return template.FuncMap{
		"asset": func(name string) (string, error) {
			a, ok := s.assets[name]
			if !ok {
				return "", fmt.Errorf("no asset %q", name)
			}
			return a.URL, nil
		},
		"img": func(name string) (*asset, error) {
			a, ok := s.assets[name]
			if !ok {
				return nil, fmt.Errorf("no image %q", name)
			}
			return a, nil
		},
		"hasAsset": func(name string) bool { _, ok := s.assets[name]; return ok },
		// imageSrcset writes a preload's imagesrcset, which html/template
		// would take for a single URL (its name has "src" in it).
		"imageSrcset": func(v any) template.HTMLAttr {
			return template.HTMLAttr(`imagesrcset="` + template.HTMLEscapeString(fmt.Sprint(v)) + `"`)
		},
		// shot is a screenshot: shots/<name>.webp, and @2x beside it for
		// sharp screens when there is one.
		"shot": func(name string) (map[string]any, error) {
			a, ok := s.assets["shots/"+name+".webp"]
			if !ok {
				return nil, fmt.Errorf("no screenshot shots/%s.webp", name)
			}
			m := map[string]any{"Src": a.URL, "Width": a.Width, "Height": a.Height}
			if x2, ok := s.assets["shots/"+name+"@2x.webp"]; ok {
				m["Srcset"] = a.URL + " 1x, " + x2.URL + " 2x"
			}
			return m, nil
		},
		"page": func(path string) *Page { return s.byPath[path] },
		// first is the first of the pages that exists: a link to a page
		// that's planned can name what stands in for it until then.
		"first": func(paths ...string) (*Page, error) {
			for _, p := range paths {
				if q := s.byPath[p]; q != nil {
					return q, nil
				}
			}
			return nil, fmt.Errorf("none of %v exists", paths)
		},
		// related is up to n of the pages that exist, in the order given.
		"related": func(n int, paths ...string) []*Page {
			var out []*Page
			for _, p := range paths {
				if q := s.byPath[p]; q != nil && len(out) < n {
					out = append(out, q)
				}
			}
			return out
		},
		"exists": s.exists,
		"nav":    s.nav,
		"footer": s.footer,
		"dict": func(kv ...any) (map[string]any, error) {
			if len(kv)%2 != 0 {
				return nil, fmt.Errorf("dict needs key and value pairs")
			}
			m := map[string]any{}
			for i := 0; i < len(kv); i += 2 {
				k, ok := kv[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict key %v is not a string", kv[i])
				}
				m[k] = kv[i+1]
			}
			return m, nil
		},
		"list": func(xs ...any) []any { return xs },
		"html": func(s string) template.HTML { return template.HTML(s) },
		"qa":   func(q, a string) QA { return QA{Q: q, A: template.HTML(a)} },
		"faqSchema": func(items []any) (map[string]any, error) {
			var qs []QA
			for _, it := range items {
				q, ok := it.(QA)
				if !ok {
					return nil, fmt.Errorf("an FAQ item is made with qa, not %T", it)
				}
				qs = append(qs, q)
			}
			return faqSchema(qs), nil
		},
		"crumbSchema": func(crumbs []Crumb) map[string]any { return crumbSchema(s.opts.Settings.BaseURL, crumbs) },
		"types":       func() []ServerType { return serverTypes(rowOrder) },
		"typeCount":   func() int { return len(serverTypes(textOrder)) },
		"typeNames": func() string {
			var names []string
			for _, t := range serverTypes(textOrder) {
				names = append(names, t.Name)
			}
			return andList(names)
		},
		"loaderNames": func() string { return andList(loaderNames()) },
		// loaders names the mod loaders joined with "and" or "or".
		"loaders": func(conj string) string { return joinList(loaderNames(), conj) },
		// ifForge picks the words that are true of the release: whether it
		// runs Forge servers follows minecraft.Types.
		"ifForge": func(yes, no string) string {
			if hasType("forge") {
				return yes
			}
			return no
		},
		"hasType":   hasType,
		"countWord": countWord,
		"card": func(id string) (*TemplateCard, error) {
			c, ok := s.cards[id]
			if !ok {
				return nil, fmt.Errorf("no template %q in site/data/templates", id)
			}
			return c, nil
		},
		"providers": func() []Provider { return providers },
		"referral":  anyReferral,
		"checked":   func() string { return Day(checkedProviders) },
		"posts":     func() []*Page { return s.posts },
		"docGroups": func() []DocGroup { return s.docs.groups },
		"day":       Day,
		"abs":       func(p string) string { return s.opts.Settings.BaseURL + p },
		"seq": func(n int) []int {
			out := make([]int, n)
			for i := range out {
				out[i] = i + 1
			}
			return out
		},
		"add":      func(a, b int) int { return a + b },
		"contains": func(xs []string, x string) bool { return slices.Contains(xs, x) },
		"join":     strings.Join,
		"split":    strings.Split,
		"lower":    strings.ToLower,
		"words":    func(s string) []string { return strings.Fields(s) },
		"pathOf":   func(addr string) string { p, _, _ := strings.Cut(addr, "#"); return p },
		// title capitalises the first letter, for a count at the start of a
		// sentence: "Seven server types".
		"title": func(s string) string {
			if s == "" {
				return s
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
	}
}
