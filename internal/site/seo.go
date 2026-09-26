package site

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"time"
)

// sitemap lists every page search engines may index, and the live demo.
func sitemap(base string, pages []*Page, today string) []byte {
	type url struct {
		Loc     string `xml:"loc"`
		LastMod string `xml:"lastmod,omitempty"`
	}
	var urls []url
	for _, p := range pages {
		if p.NoIndex {
			continue
		}
		mod := p.Updated
		if mod == "" {
			mod = p.Published
		}
		if mod == "" {
			mod = today
		}
		urls = append(urls, url{Loc: p.URL(base), LastMod: mod})
	}
	urls = append(urls, url{Loc: base + "/demo/"})
	sort.Slice(urls, func(i, j int) bool { return urls[i].Loc < urls[j].Loc })
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, u := range urls {
		b.WriteString("  <url><loc>")
		_ = xml.EscapeText(&b, []byte(u.Loc))
		b.WriteString("</loc>")
		if u.LastMod != "" {
			b.WriteString("<lastmod>" + u.LastMod + "</lastmod>")
		}
		b.WriteString("</url>\n")
	}
	b.WriteString("</urlset>\n")
	return b.Bytes()
}

// robots lets crawlers in, except the live demo's app pages: /demo/ itself is
// indexed, every page under it is the same app with other sample data.
func robots(base string) []byte {
	return []byte("User-agent: *\nAllow: /demo/$\nDisallow: /demo/\n\nSitemap: " + base + "/sitemap.xml\n")
}

// feed is the blog's Atom feed.
func feed(s Settings, posts []*Page) []byte {
	var b bytes.Buffer
	esc := func(v string) string {
		var e bytes.Buffer
		_ = xml.EscapeText(&e, []byte(v))
		return e.String()
	}
	updated := "2026-09-26"
	if len(posts) > 0 {
		updated = posts[0].Published
	}
	b.WriteString(xml.Header)
	b.WriteString(`<feed xmlns="http://www.w3.org/2005/Atom">` + "\n")
	b.WriteString("  <title>Playkeeper blog</title>\n")
	b.WriteString("  <subtitle>Releases, guides and building Playkeeper in public.</subtitle>\n")
	b.WriteString(`  <link rel="alternate" type="text/html" href="` + esc(s.BaseURL+"/blog") + `"/>` + "\n")
	b.WriteString(`  <link rel="self" type="application/atom+xml" href="` + esc(s.BaseURL+"/blog/feed.xml") + `"/>` + "\n")
	b.WriteString("  <id>" + esc(s.BaseURL+"/blog") + "</id>\n")
	b.WriteString("  <updated>" + updated + "T00:00:00Z</updated>\n")
	for _, p := range posts {
		b.WriteString("  <entry>\n")
		b.WriteString("    <title>" + esc(p.Label) + "</title>\n")
		b.WriteString(`    <link rel="alternate" type="text/html" href="` + esc(p.URL(s.BaseURL)) + `"/>` + "\n")
		b.WriteString("    <id>" + esc(p.URL(s.BaseURL)) + "</id>\n")
		b.WriteString("    <published>" + p.Published + "T00:00:00Z</published>\n")
		b.WriteString("    <updated>" + firstOf(p.Updated, p.Published) + "T00:00:00Z</updated>\n")
		b.WriteString("    <author><name>" + esc(firstOf(p.Author, "Playkeeper")) + "</name></author>\n")
		b.WriteString("    <summary>" + esc(p.Summary) + "</summary>\n")
		b.WriteString("  </entry>\n")
	}
	b.WriteString("</feed>\n")
	return b.Bytes()
}

func firstOf(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// QA is one question of a page's FAQ.
type QA struct {
	Q string
	A template.HTML
}

// faqSchema is an FAQ as structured data.
func faqSchema(items []QA) map[string]any {
	var qs []map[string]any
	for _, it := range items {
		qs = append(qs, map[string]any{
			"@type":          "Question",
			"name":           it.Q,
			"acceptedAnswer": map[string]any{"@type": "Answer", "text": plainAnswer(it.A)},
		})
	}
	return map[string]any{"@context": "https://schema.org", "@type": "FAQPage", "mainEntity": qs}
}

func plainAnswer(h template.HTML) string {
	return strings.TrimSpace(reSpaces.ReplaceAllString(unescape(reTags.ReplaceAllString(string(h), "")), " "))
}

// Crumb is one step of a breadcrumb.
type Crumb struct {
	Label, Path string
}

func crumbSchema(base string, crumbs []Crumb) map[string]any {
	var items []map[string]any
	for i, c := range crumbs {
		item := map[string]any{"@type": "ListItem", "position": i + 1, "name": c.Label}
		if c.Path != "" {
			item["item"] = base + c.Path
		}
		items = append(items, item)
	}
	return map[string]any{"@context": "https://schema.org", "@type": "BreadcrumbList", "itemListElement": items}
}

// pageSchema is the structured data a page carries in its head.
func pageSchema(s Settings, p *Page, version, image string) (map[string]any, error) {
	switch p.Schema {
	case "":
		return nil, nil
	case "software":
		return map[string]any{
			"@context":               "https://schema.org",
			"@type":                  "SoftwareApplication",
			"name":                   "Playkeeper",
			"description":            p.Description,
			"url":                    s.BaseURL + "/",
			"image":                  image,
			"applicationCategory":    "GameApplication",
			"applicationSubCategory": "Minecraft server dashboard",
			"operatingSystem":        "Ubuntu 24.04 (x86_64)",
			"softwareVersion":        version,
			"license":                "https://www.gnu.org/licenses/agpl-3.0.html",
			"isAccessibleForFree":    true,
			"downloadUrl":            s.Repo + "/releases/latest",
			"codeRepository":         s.Repo,
			"offers":                 map[string]any{"@type": "Offer", "price": "0", "priceCurrency": "USD"},
		}, nil
	case "article", "posting":
		t := "Article"
		if p.Schema == "posting" {
			t = "BlogPosting"
		}
		m := map[string]any{
			"@context":         "https://schema.org",
			"@type":            t,
			"headline":         p.Label,
			"description":      p.Description,
			"image":            image,
			"mainEntityOfPage": p.URL(s.BaseURL),
			"author":           author(p),
			"publisher":        map[string]any{"@type": "Organization", "name": "Playkeeper", "url": s.BaseURL + "/"},
		}
		if p.Published != "" {
			m["datePublished"] = p.Published
		}
		if mod := firstOf(p.Updated, p.Published); mod != "" {
			m["dateModified"] = mod
		}
		return m, nil
	}
	return nil, fmt.Errorf("%s: unknown schema %q", p.Path, p.Schema)
}

func author(p *Page) map[string]any {
	if p.Author != "" {
		return map[string]any{"@type": "Person", "name": p.Author}
	}
	return map[string]any{"@type": "Organization", "name": "Playkeeper"}
}

func today(now time.Time) string { return now.UTC().Format(time.DateOnly) }
