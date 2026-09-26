package site

import (
	"fmt"
	"html/template"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Page is one page of the site: its address, what search engines and social
// previews show, and where it sits among the others.
type Page struct {
	// Path is the address, like /alternatives/aternos; the landing page is /.
	Path string
	// Title is the <title>. Description is the meta description.
	Title, Description string
	// Label names the page in menus, the footer, breadcrumbs and inline
	// links. H1 is its heading when that's longer, as a guide's is.
	Label, H1 string
	// Card is the title a "Keep reading" card shows, and Kind its eyebrow:
	// Guide, Feature, Compare, Tool, Docs or Blog.
	Card, Kind string
	// Section is the header item the page belongs to: features, guides,
	// compare, docs or pricing.
	Section string
	// Layout is page, guide, post, docs or bare (no header or footer).
	Layout string
	// Crumb is the parent in the breadcrumb, a section's label.
	Crumb string
	// OG names the social preview image, og/<OG>.png.
	OG string
	// Published and Updated are days, YYYY-MM-DD.
	Published, Updated string
	// NoIndex keeps the page out of search engines and the sitemap.
	NoIndex bool
	// Closing picks the closing band: default, guide, sizing or none.
	Closing string
	// Scripts are the page's own scripts, after site.js.
	Scripts []string
	// Preload is an image the page shows first, fetched early.
	Preload string
	// Blog posts: Tags, Author, Summary and Cover.
	Tags            []string
	Author, Summary string
	Cover           string
	// Schema is the structured data the page carries in its head: software,
	// article or posting. FAQs and breadcrumbs carry their own.
	Schema string

	file   string
	source string
	// minutes is the reading time of a guide or post.
	minutes int
	// body is the page's own HTML for docs pages, which come from Markdown.
	body template.HTML
	toc  []Heading
	// docs pages: the entry in the docs nav.
	docs *DocPage
}

// Heading is one entry in a page's contents.
type Heading struct {
	ID, Number, Text string
}

// URL is the page's full address.
func (p *Page) URL(base string) string { return base + p.Path }

// Minutes is the reading time, in whole minutes.
func (p *Page) Minutes() int { return p.minutes }

// TOC is the page's contents, from its numbered sections.
func (p *Page) TOC() []Heading { return p.toc }

// Docs is the docs page a page was built from, or nil.
func (p *Page) Docs() *DocPage { return p.docs }

// Day formats a YYYY-MM-DD day as "26 Sep 2026".
func Day(s string) string {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return s
	}
	return t.Format("2 Jan 2006")
}

// outFile is where the page is written, relative to the site's root:
// /pricing is pricing.html, which nginx answers for /pricing.
func (p *Page) outFile() string {
	if p.Path == "/" {
		return "index.html"
	}
	return strings.TrimPrefix(p.Path, "/") + ".html"
}

// loadPages reads every page under dir in src: HTML templates that start
// with their settings in a comment.
func loadPages(src fs.FS, dir string) ([]*Page, error) {
	var pages []*Page
	err := fs.WalkDir(src, dir, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(name) != ".html" {
			return err
		}
		b, err := fs.ReadFile(src, name)
		if err != nil {
			return err
		}
		p, err := parsePage(string(b))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		p.file = name
		pages = append(pages, p)
		return nil
	})
	sort.Slice(pages, func(i, j int) bool { return pages[i].Path < pages[j].Path })
	return pages, err
}

// parsePage reads a page's settings: the first thing in the file is a
// template comment with one "key: value" per line.
func parsePage(src string) (*Page, error) {
	const open, close = "{{/*", "*/}}"
	s := strings.TrimLeft(src, " \t\r\n")
	if !strings.HasPrefix(s, open) {
		return nil, fmt.Errorf("a page starts with its settings in %s … %s", open, close)
	}
	end := strings.Index(s, close)
	if end < 0 {
		return nil, fmt.Errorf("the settings comment is not closed with %s", close)
	}
	p := &Page{Layout: "page", Closing: "default", source: src}
	for i, line := range strings.Split(s[len(open):end], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("settings line %d is not key: value: %q", i+1, line)
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "path":
			p.Path = value
		case "title":
			p.Title = value
		case "description":
			p.Description = value
		case "label":
			p.Label = value
		case "h1":
			p.H1 = value
		case "card":
			p.Card = value
		case "kind":
			p.Kind = value
		case "section":
			p.Section = value
		case "layout":
			p.Layout = value
		case "crumb":
			p.Crumb = value
		case "og":
			p.OG = value
		case "published":
			p.Published = value
		case "updated":
			p.Updated = value
		case "noindex":
			b, err := strconv.ParseBool(value)
			if err != nil {
				return nil, fmt.Errorf("noindex is true or false, not %q", value)
			}
			p.NoIndex = b
		case "closing":
			p.Closing = value
		case "scripts":
			p.Scripts = fields(value)
		case "preload":
			p.Preload = value
		case "tags":
			p.Tags = fields(value)
		case "author":
			p.Author = value
		case "summary":
			p.Summary = value
		case "cover":
			p.Cover = value
		case "schema":
			p.Schema = value
		default:
			return nil, fmt.Errorf("unknown setting %q", key)
		}
	}
	if !strings.HasPrefix(p.Path, "/") {
		return nil, fmt.Errorf("path %q must start with /", p.Path)
	}
	if p.Label == "" {
		p.Label = p.Title
	}
	if p.Card == "" {
		p.Card = p.Label
	}
	if p.H1 == "" {
		p.H1 = p.Label
	}
	return p, nil
}

func fields(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}
