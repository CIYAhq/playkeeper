package site

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/templates"
	"github.com/CIYAhq/playkeeper/internal/templates/checks"
	"github.com/CIYAhq/playkeeper/internal/templates/library"
)

// LibraryPage is a template of the public library at /templates/<ID>: the
// site template it opens (site/data/library/<ID>.json names it) and what
// happened the last time cmd/template-check created and started a server from
// it on a real Playkeeper (site/data/checks/<template>.json).
type LibraryPage struct {
	ID       string `json:"-"`
	Template string `json:"template"`
	// Checked is the day of the check; Release the Playkeeper version it
	// ran on, and Build the server software's build its plan picked.
	Checked string `json:"-"`
	Release string `json:"-"`
	Build   string `json:"-"`
	// DoneSeconds is what the server's log said when it was ready: "Done
	// (12.005s)!".
	DoneSeconds float64 `json:"-"`
	// Plugins are the template's add-ons as they installed, in its order.
	Plugins []LibraryPlugin `json:"-"`
	card    *TemplateCard
}

// LibraryPlugin is one add-on of a library template as it installed.
type LibraryPlugin struct {
	Name string
	// Source is the source the template lists it from.
	Source  string
	Slug    string
	Version string
	// Licence is the SPDX id its source lists; LicenseRef-All-Rights-Reserved
	// for none.
	Licence string
	// Downloads is its total on its source on Checked. Nothing counts those
	// of Playkeeper's own plugins, which have none.
	Downloads int
}

// FirstParty reports whether it's one of Playkeeper's own plugins, which
// ships inside Playkeeper: no registry lists it, so it has no page, icon or
// downloads to show.
func (p LibraryPlugin) FirstParty() bool { return p.Source == string(addons.Playkeeper) }

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

// Projects lists the add-ons for the itzg image's MODRINTH_PROJECTS: each
// slug with the version the template pins, so the command installs what the
// template does. Playkeeper's own plugins are left out, since no registry
// has them.
func (l *LibraryPage) Projects() string {
	var out []string
	for _, a := range l.T().Addons {
		if a.Source == addons.Playkeeper {
			continue
		}
		p := a.Slug
		if a.Pin != nil {
			p += ":" + a.Pin.VersionID
		}
		out = append(out, p)
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

// loadLibrary reads the library's pages in dir, one .json each, takes their
// facts from their templates' checks, and refuses a page whose template
// has no passing check or whose check doesn't list exactly what it installs.
func loadLibrary(src fs.FS, dir string, cards map[string]*TemplateCard, checked map[string]*checks.Check) (map[string]*LibraryPage, error) {
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
		if err := l.fill(checked[l.Template]); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if err := l.check(cards); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		out[id] = l
	}
	return out, nil
}

// fill takes the page's facts from its template's last check.
func (l *LibraryPage) fill(c *checks.Check) error {
	switch {
	case c == nil:
		return fmt.Errorf("template %q has no check in site/data/checks: run cmd/template-check with -write", l.Template)
	case c.Status != checks.Passing:
		return fmt.Errorf("template %q failed its last check (%s): fix it or take its page down", l.Template, c.Failure)
	}
	l.Checked, l.Release, l.Build, l.DoneSeconds = c.Checked, c.Release, c.Build, c.DoneSeconds
	l.Plugins = nil
	for _, a := range c.Addons {
		l.Plugins = append(l.Plugins, LibraryPlugin{Name: a.Name, Source: a.Source, Slug: a.Slug, Version: versionName(a.Version), Licence: a.Licence, Downloads: a.Downloads})
	}
	return nil
}

var (
	reLoaderTag = regexp.MustCompile(`(?i)^(?:bukkit|spigot|paper|purpur|fabric|quilt|neoforge|forge)[-_]|[-_+](?:bukkit|spigot|paper|purpur|fabric|quilt|neoforge|forge)$`)
	reMCPrefix  = regexp.MustCompile(`^mc\d+(?:\.\d+)+[-_]`)
	reMCSuffix  = regexp.MustCompile(`\+(?:mc)?\d+(?:\.\d+)+$`)
)

// versionName is an add-on's version as a reader says it: without the
// loader and Minecraft version its source adds, such as "bukkit-2.6.24" or
// "mc26.2-0.25.3-fabric", or a leading v.
func versionName(v string) string {
	s := v
	for range 3 {
		s = reMCSuffix.ReplaceAllString(reMCPrefix.ReplaceAllString(reLoaderTag.ReplaceAllString(s, ""), ""), "")
	}
	if len(s) > 1 && s[0] == 'v' && s[1] >= '0' && s[1] <= '9' {
		s = s[1:]
	}
	if s == "" {
		return v
	}
	return s
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
		if p.Name != a.Name || p.Source != string(a.Source) || p.Slug != a.Slug || p.Version == "" || p.Licence == "" || (p.Downloads <= 0 && !p.FirstParty()) {
			return fmt.Errorf("plugin %d is %q (%s %s), which isn't the template's %q (%s %s) with its version, licence and downloads", i+1, p.Name, p.Source, p.Slug, a.Name, a.Source, a.Slug)
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

// DashboardLibrary is the template list each release carries for New server
// › A template (internal/templates/library): every template in
// site/data/templates whose last check passed, by name, with its page and
// that check's day and release.
func DashboardLibrary(root fs.FS, s Settings) ([]byte, error) {
	cards, err := loadTemplateCards(root, "site/data/templates")
	if err != nil {
		return nil, err
	}
	checked, err := checks.Read(root, "site/data/checks")
	if err != nil {
		return nil, err
	}
	pages, err := loadLibrary(root, "site/data/library", cards, checked)
	if err != nil {
		return nil, err
	}
	packs, err := loadModpacks(root, "site/data/modpacks", cards)
	if err != nil {
		return nil, err
	}
	out := make([]library.Template, 0, len(cards))
	for id, c := range cards {
		ck := checked[id]
		if ck == nil || ck.Status != checks.Passing {
			continue
		}
		file, err := fs.ReadFile(root, path.Join("site/data/templates", id+".json"))
		if err != nil {
			return nil, err
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, file); err != nil {
			return nil, fmt.Errorf("%s.json: %w", id, err)
		}
		t := library.Template{ID: id, Name: c.Name, Art: path.Base(c.Art), OpensFrom: c.OpensFrom, Checked: ck.Checked, Release: ck.Release, File: compact.Bytes()}
		for _, m := range packs {
			if m.Template == id {
				t.Page = s.BaseURL + m.Path()
			}
		}
		for _, l := range pages {
			if l.Template == id {
				t.Page = s.BaseURL + l.Path()
			}
		}
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b library.Template) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	if err := e.Encode(out); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
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
