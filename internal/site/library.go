package site

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/templates"
)

// LibraryPage is a template of the public library at /templates/<ID>: the
// site template it opens (site/data/templates) and what happened when an
// agent created and started a server from it on a real Playkeeper, on the
// day in Checked (site/data/library/<ID>.json).
type LibraryPage struct {
	ID       string `json:"-"`
	Template string `json:"template"`
	Checked  string `json:"checked"`
	// Release is the Playkeeper version the server was created with, and
	// Build the server software's build its plan picked.
	Release string `json:"release"`
	Build   string `json:"build"`
	// DoneSeconds is what the server's log said when it was ready: "Done
	// (12.005s)!".
	DoneSeconds float64 `json:"doneSeconds"`
	// Plugins are the template's add-ons as they installed, in its order.
	Plugins []LibraryPlugin `json:"plugins"`
	card    *TemplateCard
}

// LibraryPlugin is one add-on of a library template as it installed.
type LibraryPlugin struct {
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	Version string `json:"version"`
	// Licence is the SPDX id its source lists; LicenseRef-All-Rights-Reserved
	// for none.
	Licence string `json:"licence"`
	// Downloads is its total on its source on Checked.
	Downloads int `json:"downloads"`
}

var reRelease3 = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// Path is the page's address.
func (l *LibraryPage) Path() string { return "/templates/" + l.ID }

// Card is the template the page opens.
func (l *LibraryPage) Card() *TemplateCard { return l.card }

// T is the template itself.
func (l *LibraryPage) T() *templates.Template { return l.card.Template }

// TypeName is the server type's name, like Paper.
func (l *LibraryPage) TypeName() string {
	t, _ := minecraft.TypeByID(l.T().Server.Type)
	return t.Name
}

// Java is the Java its Minecraft version runs on.
func (l *LibraryPage) Java() int { return minecraft.JavaFor(l.T().Server.MinecraftVersion) }

// Memory is the memory the template asks for, like "4 GB", and JavaHeapMB
// the heap Playkeeper gives Java of it.
func (l *LibraryPage) Memory() string { return gigabytes(l.T().Settings.MemoryMB) }

func (l *LibraryPage) JavaHeapMB() int {
	return minecraft.HeapFor(l.T().Settings.MemoryMB, l.T().Server.Type, 0)
}

// Done writes DoneSeconds as the log rounds it, like "12".
func (l *LibraryPage) Done() string {
	return strconv.FormatFloat(l.DoneSeconds, 'f', 0, 64)
}

// Projects lists the add-ons' slugs for the itzg image's MODRINTH_PROJECTS.
func (l *LibraryPage) Projects() string {
	var out []string
	for _, p := range l.Plugins {
		out = append(out, p.Slug)
	}
	return strings.Join(out, ",")
}

// Difficulty, PVP, Hardcore and MaxPlayers are the template's rules, as a
// new server gets them when the template leaves one unset.
func (l *LibraryPage) Difficulty() string { return firstOf(l.T().Settings.Difficulty, "normal") }
func (l *LibraryPage) PVP() bool          { p := l.T().Settings.PVP; return p == nil || *p }
func (l *LibraryPage) Hardcore() bool     { h := l.T().Settings.Hardcore; return h != nil && *h }
func (l *LibraryPage) MaxPlayers() int    { return l.T().Settings.MaxPlayers }

// count writes n with thousands separators, like 417,224.
func count(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// licenceName writes an SPDX licence id the way a reader says it.
func licenceName(id string) string {
	switch id {
	case "LicenseRef-All-Rights-Reserved":
		return "all rights reserved"
	case "LicenseRef-CC-BY-NC-ND-3.0":
		return "CC BY-NC-ND 3.0"
	}
	return strings.TrimSuffix(id, "-only")
}

// Has reports whether the template installs the add-on with the slug.
func (l *LibraryPage) Has(slug string) bool {
	return slices.ContainsFunc(l.Plugins, func(p LibraryPlugin) bool { return p.Slug == slug })
}

// loadLibrary reads the library's pages in dir, one .json each, and refuses
// one that doesn't describe exactly what its template installs.
func loadLibrary(src fs.FS, dir string, cards map[string]*TemplateCard) (map[string]*LibraryPage, error) {
	entries, err := fs.ReadDir(src, dir)
	if err != nil {
		return nil, err
	}
	out := map[string]*LibraryPage{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if e.IsDir() || !ok {
			continue
		}
		b, err := fs.ReadFile(src, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		l := &LibraryPage{ID: id}
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if err := d.Decode(l); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if err := l.check(cards); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		out[id] = l
	}
	return out, nil
}

func (l *LibraryPage) check(cards map[string]*TemplateCard) error {
	c := cards[l.Template]
	switch {
	case c == nil || c.Template == nil:
		return fmt.Errorf("template %q isn't in site/data/templates", l.Template)
	case c.Template.Modpack != nil:
		return fmt.Errorf("template %q is a modpack's, whose page is under /modpacks", l.Template)
	case !hasType(c.Template.Server.Type):
		return fmt.Errorf("the release can't create %q servers", c.Template.Server.Type)
	case !reRelease3.MatchString(l.Release) || l.Build == "" || l.DoneSeconds <= 0:
		return fmt.Errorf("a library page says which release, build and start time it was checked with")
	case len(l.Plugins) != len(c.Template.Addons):
		return fmt.Errorf("it lists %d plugins, and the template installs %d", len(l.Plugins), len(c.Template.Addons))
	}
	for i, a := range c.Template.Addons {
		p := l.Plugins[i]
		if p.Name != a.Name || p.Slug != a.Slug || p.Version == "" || p.Licence == "" || p.Downloads <= 0 {
			return fmt.Errorf("plugin %d is %q (%s), which isn't the template's %q (%s) with its version, licence and downloads", i+1, p.Name, p.Slug, a.Name, a.Slug)
		}
	}
	if _, err := time.Parse(time.DateOnly, l.Checked); err != nil {
		return fmt.Errorf("checked is a day, YYYY-MM-DD: %w", err)
	}
	l.card = c
	return nil
}

func (s *Site) libraryPage(id string) (*LibraryPage, error) {
	l, ok := s.library[id]
	if !ok {
		return nil, fmt.Errorf("no library page %q in site/data/library", id)
	}
	return l, nil
}

// libraryList is every library template with a page, by name.
func (s *Site) libraryList() []*LibraryPage {
	var out []*LibraryPage
	for _, l := range s.library {
		if s.byPath[l.Path()] != nil {
			out = append(out, l)
		}
	}
	slices.SortFunc(out, func(a, b *LibraryPage) int { return strings.Compare(a.card.Name, b.card.Name) })
	return out
}
