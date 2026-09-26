package site

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// DocPage is a docs page built from the repository's own Markdown, so the
// docs never drift from the product: one "## " section of a file, or a whole
// file.
type DocPage struct {
	Slug, Title, Description string
	// Source is the file, from the repository's root; Section the heading
	// of the part of it this page is, or "" for all of it.
	Source, Section string
}

// docPages are the docs, in the order the docs nav lists them. README.md's
// sections are the manual; a section missing from README.md leaves its page
// out, along with the docs landing's entries that point into it.
var docPages = []DocPage{
	{"install", "Install Playkeeper", "What your VPS needs, the one-line install and what it changes, and the setup link it prints.", "README.md", "Install on your VPS"},
	{"servers", "Servers", "New servers, server types, modpacks, templates, your own world and Minecraft versions.", "README.md", "Servers"},
	{"add-ons", "Plugins, mods and the world", "Plugins, mods, voice chat, friends' mod packs, pre-generation, resource and data packs and the live map.", "README.md", "Plugins, mods and the world"},
	{"keep-it-running", "Keep it running", "Backups, backup rules, off-site copies, schedules, sleep, crash and lag help and disk space.", "README.md", "Keep it running"},
	{"friends-and-team", "Friends and your team", "Invite links, player pages, team roles, Discord and two-factor sign-in.", "README.md", "Friends and your team"},
	{"addresses", "Addresses", "A free yourname.playkeeper.io name or your own domain, with a real certificate.", "README.md", "Addresses"},
	{"machines-and-ai-agents", "More machines and AI agents", "Servers on a second VPS or a home server, and running them from an AI agent over MCP.", "README.md", "More machines and AI agents"},
	{"updates", "Update, upgrade and uninstall", "Signed updates that roll back, upgrading older versions, uninstalling, and the command line.", "README.md", "Update, upgrade and uninstall"},
	{"recovery", "Recover or move a world", "Restore a backup on this machine or a new one, and move a world between machines.", "docs/RECOVERY.md", ""},
	{"troubleshooting", "Troubleshooting", "When the dashboard won't open, friends can't join, the browser warns or a server runs out of memory.", "docs/TROUBLESHOOTING.md", ""},
	{"contributing", "Contributing", "Build Playkeeper, run the tests and send a pull request.", "CONTRIBUTING.md", ""},
	{"security", "Security", "How Playkeeper protects your server, and how to report a problem privately.", "SECURITY.md", ""},
}

// DocEntry is one entry point on the docs landing and in the docs nav.
type DocEntry struct {
	Label, Blurb string
	// Page and Anchor say where it goes: /docs/<Page>#<Anchor>. Or lists
	// where else the same thing is written, tried in order when Page isn't
	// built, as when README.md covers it inside another section.
	Page, Anchor string
	Or           []DocTarget
	// URL is where it went, once built.
	URL string
}

// DocTarget is a docs page and, optionally, an anchor on it.
type DocTarget struct{ Page, Anchor string }

// DocGroup is a group of entries, like "Get started".
type DocGroup struct {
	Title, Icon string
	Entries     []DocEntry
}

var docGroups = []DocGroup{
	{Title: "Get started", Entries: []DocEntry{
		{Label: "Requirements", Blurb: "Ubuntu 24.04 on x86_64, 2 CPU cores, 3 GB of memory, 5 GB of disk.", Page: "install", Anchor: "you-need"},
		{Label: "Install", Blurb: "One command, and every change it makes.", Page: "install"},
		{Label: "Open the ports", Blurb: "8443, 25565 and the rest, in your provider's firewall.", Page: "troubleshooting", Anchor: "friends-cant-join"},
		{Label: "Your first server", Blurb: "New server picks a version and memory for you.", Page: "servers", Or: []DocTarget{{"install", "more-servers"}}},
	}},
	{Title: "Everyday", Icon: "everyday", Entries: []DocEntry{
		{Label: "Addresses", Blurb: "The free name, your own domain, join addresses", Page: "addresses", Or: []DocTarget{{"install", "a-name-instead-of-the-ip"}}},
		{Label: "Add-ons", Blurb: "Plugins, mods and modpacks", Page: "add-ons", Or: []DocTarget{{"install", "plugins-and-mods"}}},
		{Label: "Backups and recovery", Blurb: "Backup rules, off-site copies, the recovery key", Page: "keep-it-running", Anchor: "backups", Or: []DocTarget{{"install", "backups"}}},
		{Label: "Team and Discord", Blurb: "Roles, invites and alerts", Page: "friends-and-team"},
		{Label: "Schedules and sleep", Blurb: "Restarts, backups and sleeping when nobody plays", Page: "keep-it-running", Anchor: "schedules"},
	}},
	{Title: "Advanced", Icon: "advanced", Entries: []DocEntry{
		{Label: "More machines", Blurb: "A second VPS or a home server", Page: "machines-and-ai-agents", Anchor: "servers-on-more-machines"},
		{Label: "AI agents", Blurb: "Claude, Cursor or any MCP client", Page: "machines-and-ai-agents", Anchor: "ai-agents"},
		{Label: "Updates", Blurb: "Signed, and they roll back if they fail", Page: "updates", Anchor: "update-playkeeper", Or: []DocTarget{{"install", "update-playkeeper"}}},
		{Label: "Uninstall", Blurb: "Remove Playkeeper, keep your worlds", Page: "updates", Anchor: "uninstall", Or: []DocTarget{{"install", "uninstall"}}},
		{Label: "Commands", Blurb: "The playkeeper command line", Page: "updates", Anchor: "other-commands"},
	}},
	{Title: "Troubleshooting", Icon: "troubleshooting", Entries: []DocEntry{
		{Label: "Can't reach the dashboard", Page: "troubleshooting", Anchor: "cant-reach-the-dashboard"},
		{Label: "Friends can't join", Page: "troubleshooting", Anchor: "friends-cant-join"},
		{Label: "Certificate warning", Page: "troubleshooting", Anchor: "certificate-warning"},
		{Label: "Out of memory", Page: "troubleshooting", Anchor: "out-of-memory"},
	}},
	{Title: "Project", Entries: []DocEntry{
		{Label: "Contributing", Blurb: "Build it, run the tests, send a pull request.", Page: "contributing"},
	}},
}

// SearchEntry is one place the docs search can find: a page, or a part of
// one with its own anchor.
type SearchEntry struct {
	Title string `json:"t"`
	Page  string `json:"p"`
	URL   string `json:"u"`
	Text  string `json:"x"`
}

// docsBuild is the docs as built: the pages, the landing's entries that
// point at something that exists, and the search index.
type docsBuild struct {
	pages  []*Page
	groups []DocGroup
	search []SearchEntry
}

var (
	reH2       = regexp.MustCompile(`(?m)^## (.+)$`)
	reTags     = regexp.MustCompile(`<[^>]+>`)
	reSpaces   = regexp.MustCompile(`\s+`)
	reAnchorID = regexp.MustCompile(`\sid="([^"]+)"`)
)

// buildDocs renders docPages from the repository in root.
func buildDocs(root fs.FS, s Settings) (*docsBuild, error) {
	var out docsBuild
	files := map[string]string{}
	read := func(name string) (string, bool, error) {
		if b, ok := files[name]; ok {
			return b, true, nil
		}
		b, err := fs.ReadFile(root, name)
		if err != nil {
			if isNotExist(err) {
				return "", false, nil
			}
			return "", false, err
		}
		files[name] = string(b)
		return string(b), true, nil
	}
	// Where each README anchor ends up, so links between sections still land,
	// and the headings that became a page's top.
	anchors, tops := map[string]string{}, map[string]string{}
	type part struct {
		doc DocPage
		md  string
	}
	var parts []part
	for _, d := range docPages {
		src, ok, err := read(d.Source)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		md := src
		if d.Section != "" {
			md, ok = section(src, d.Section)
			if !ok {
				continue
			}
		} else {
			md = dropTitle(md)
		}
		parts = append(parts, part{d, md})
		for _, id := range ids(md) {
			if _, taken := anchors[d.Source+"#"+id]; !taken {
				anchors[d.Source+"#"+id] = d.Slug
			}
		}
		if d.Section != "" {
			// The section's own heading becomes the page's title, so a
			// link to it goes to the page.
			tops[d.Source+"#"+slugify(d.Section)] = d.Slug
		}
		if d.Section == "" {
			anchors[d.Source] = d.Slug
		}
	}
	built := map[string]*Page{}
	for _, pt := range parts {
		body, err := renderMarkdown(pt.md, pt.doc.Source, anchors, tops, s)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", pt.doc.Source, err)
		}
		d := pt.doc
		p := &Page{
			Path: "/docs/" + d.Slug, Title: d.Title + " · Playkeeper docs", Description: d.Description,
			Label: d.Title, Card: d.Title, Kind: "Docs", Section: "docs", Layout: "docs", Crumb: "Docs",
			OG: "docs", Closing: "none", body: template.HTML(body), docs: &pt.doc,
		}
		built[d.Slug] = p
		out.pages = append(out.pages, p)
		out.search = append(out.search, searchEntries(p, body)...)
	}
	for _, g := range docGroups {
		kept := g
		kept.Entries = nil
		for _, e := range g.Entries {
			for _, t := range append([]DocTarget{{e.Page, e.Anchor}}, e.Or...) {
				p, ok := built[t.Page]
				if !ok {
					continue
				}
				if t.Anchor != "" && !strings.Contains(string(p.body), ` id="`+t.Anchor+`"`) {
					return nil, fmt.Errorf("the docs entry %q points at #%s, which %s no longer has; update docGroups in internal/site/docs.go", e.Label, t.Anchor, p.docs.Source)
				}
				e.URL = p.Path
				if t.Anchor != "" {
					e.URL += "#" + t.Anchor
				}
				kept.Entries = append(kept.Entries, e)
				break
			}
		}
		if len(kept.Entries) > 0 {
			out.groups = append(out.groups, kept)
		}
	}
	return &out, nil
}

// section returns the part of a Markdown file under "## heading", without
// the heading, up to the next "## ".
func section(md, heading string) (string, bool) {
	locs := reH2.FindAllStringSubmatchIndex(md, -1)
	for i, l := range locs {
		if strings.TrimSpace(md[l[2]:l[3]]) != heading {
			continue
		}
		end := len(md)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		return md[l[1]:end], true
	}
	return "", false
}

// dropTitle removes a file's "# Title" line: the page shows its own.
func dropTitle(md string) string {
	s := strings.TrimLeft(md, "\n")
	if strings.HasPrefix(s, "# ") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			return s[i+1:]
		}
		return ""
	}
	return md
}

// ids lists the anchors a Markdown part will have once rendered.
func ids(md string) []string {
	var out []string
	for _, m := range reAnchorID.FindAllStringSubmatch(mustRender(md), -1) {
		out = append(out, m[1])
	}
	return out
}

func mustRender(md string) string {
	var b bytes.Buffer
	if err := newMarkdown(nil).Convert([]byte(md), &b); err != nil {
		return ""
	}
	return b.String()
}

func searchEntries(p *Page, body string) []SearchEntry {
	out := []SearchEntry{{Title: p.docs.Title, Page: p.docs.Title, URL: p.Path, Text: snippet(body, 220)}}
	// Each anchored part: a heading, or a paragraph or list item that
	// starts with a bold lead-in.
	re := regexp.MustCompile(`(?s)<(h[23]|p|li) id="([^"]+)"[^>]*>(.*?)</(?:h[23]|p|li)>`)
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		text := plainText(m[3])
		title := text
		if m[1] != "h2" && m[1] != "h3" {
			if lead, _, ok := strings.Cut(text, ":"); ok {
				title = lead
			}
		}
		if len(title) > 80 {
			title = title[:80]
		}
		out = append(out, SearchEntry{Title: title, Page: p.docs.Title, URL: p.Path + "#" + m[2], Text: snippet(m[3], 220)})
	}
	return out
}

func plainText(h string) string {
	return strings.TrimSpace(reSpaces.ReplaceAllString(template.HTMLEscapeString(unescape(reTags.ReplaceAllString(h, " "))), " "))
}

func snippet(h string, n int) string {
	s := strings.TrimSpace(reSpaces.ReplaceAllString(unescape(reTags.ReplaceAllString(h, " ")), " "))
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}

var entities = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&rsquo;", "’", "&lsquo;", "‘", "&hellip;", "…", "&ndash;", "–", "&mdash;", "—", "&rsaquo;", "›", "&nbsp;", " ")

func unescape(s string) string { return entities.Replace(s) }

var reSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slugify makes an anchor the way the docs do: lowercase letters, digits
// and dashes, apostrophes dropped ("Can't reach it" is cant-reach-it).
func slugify(s string) string {
	s = strings.ToLower(strings.NewReplacer("'", "", "’", "", "`", "").Replace(s))
	return strings.Trim(reSlug.ReplaceAllString(s, "-"), "-")
}

// newMarkdown is the Markdown the docs are written in: GitHub's, with an
// anchor on every heading and on every paragraph or list item that starts
// with a bold lead-in ("**Backups:** …" is #backups), and the repository's
// links made to work on the site (nil leaves links as they are).
func newMarkdown(links *linkRewriter) goldmark.Markdown {
	transformers := []util.PrioritizedValue{util.Prioritized(&anchorer{}, 900)}
	if links != nil {
		transformers = append(transformers, util.Prioritized(links, 910))
	}
	return goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID(), parser.WithASTTransformers(transformers...)),
		goldmark.WithRendererOptions(html.WithUnsafe()),
	)
}

// renderMarkdown renders one docs page's Markdown from the file source.
func renderMarkdown(md, source string, anchors, tops map[string]string, s Settings) (string, error) {
	var b bytes.Buffer
	lr := &linkRewriter{source: source, anchors: anchors, tops: tops, settings: s}
	if err := newMarkdown(lr).Convert([]byte(md), &b); err != nil {
		return "", err
	}
	if len(lr.broken) > 0 {
		return "", fmt.Errorf("links to anchors that don't exist: %s", strings.Join(lr.broken, ", "))
	}
	// Code blocks and tables wider than the page scroll, so the keyboard can
	// reach them.
	html := strings.ReplaceAll(b.String(), "<pre><code", `<pre tabindex="0"><code`)
	html = strings.ReplaceAll(html, "<table>", `<div class="table-scroll" tabindex="0"><table>`)
	return strings.ReplaceAll(html, "</table>", "</table></div>"), nil
}

// anchorer gives bold lead-ins their anchors, and moves headings up a level:
// a page's "### " parts are its <h2>s.
type anchorer struct{}

func (a *anchorer) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	src := reader.Source()
	taken := map[string]bool{}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Heading:
			if n.Level > 2 {
				n.Level--
			}
			if id, ok := n.AttributeString("id"); ok {
				taken[string(id.([]byte))] = true
			}
		case *ast.Paragraph:
			target := ast.Node(n)
			if li, ok := n.Parent().(*ast.ListItem); ok && li.FirstChild() == n {
				target = li
			}
			if id := leadIn(n, src); id != "" && !taken[id] {
				taken[id] = true
				target.SetAttributeString("id", []byte(id))
			}
		}
		return ast.WalkContinue, nil
	})
}

// leadIn is the anchor of a paragraph that starts with bold text ending in a
// colon, like "**Backup rules:**".
func leadIn(p *ast.Paragraph, src []byte) string {
	em, ok := p.FirstChild().(*ast.Emphasis)
	if !ok || em.Level != 2 {
		return ""
	}
	label := string(nodeText(em, src))
	if !strings.HasSuffix(label, ":") {
		return ""
	}
	return slugify(strings.TrimSuffix(label, ":"))
}

func nodeText(n ast.Node, src []byte) []byte {
	var b bytes.Buffer
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if t, ok := c.(*ast.Text); ok {
				b.Write(t.Segment.Value(src))
			}
			if t, ok := c.(*ast.String); ok {
				b.Write(t.Value)
			}
		}
		return ast.WalkContinue, nil
	})
	return b.Bytes()
}

// linkRewriter makes the repository's links work on the site: links to a
// file the docs publish go to its page, links to other files to GitHub, and
// links to playkeeper.io stay on the site.
type linkRewriter struct {
	source        string
	anchors, tops map[string]string
	settings      Settings
	broken        []string
}

func (lr *linkRewriter) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if l, ok := n.(*ast.Link); ok && entering {
			l.Destination = []byte(lr.rewrite(string(l.Destination)))
		}
		return ast.WalkContinue, nil
	})
}

func (lr *linkRewriter) rewrite(dest string) string {
	u, err := url.Parse(dest)
	if err != nil {
		return dest
	}
	if u.Scheme != "" || u.Host != "" {
		if u.Host == "playkeeper.io" && (u.Scheme == "https" || u.Scheme == "http") {
			return u.RequestURI() + frag(u.Fragment)
		}
		return dest
	}
	file := lr.source
	if u.Path != "" {
		file = path.Clean(path.Join(path.Dir(lr.source), u.Path))
	}
	if u.Fragment != "" {
		if slug, ok := lr.tops[file+"#"+u.Fragment]; ok {
			return "/docs/" + slug
		}
		if slug, ok := lr.anchors[file+"#"+u.Fragment]; ok {
			return "/docs/" + slug + "#" + u.Fragment
		}
		if file == lr.source || strings.HasSuffix(file, ".md") && lr.anchors[file] != "" {
			lr.broken = append(lr.broken, dest)
		}
	}
	if slug, ok := lr.anchors[file]; ok {
		return "/docs/" + slug + frag(u.Fragment)
	}
	if file == "README.md" {
		return "/docs" + frag(u.Fragment)
	}
	return lr.settings.Repo + "/blob/main/" + file + frag(u.Fragment)
}

func frag(f string) string {
	if f == "" {
		return ""
	}
	return "#" + f
}
