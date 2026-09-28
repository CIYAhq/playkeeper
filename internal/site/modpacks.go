package site

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/modpacks"
)

// Modpack is the pack a page under /modpacks is about: facts checked at the
// pack's source and with Playkeeper's own install plan on the day in Checked
// (site/data/modpacks/<ID>.json), and what the release works out from them,
// so the page says what the dashboard would.
type Modpack struct {
	ID string `json:"-"`
	// Name is the pack's name; Short is what players call it, like ATM10.
	Name  string `json:"name"`
	Short string `json:"short,omitempty"`
	// Source is modrinth or curseforge, Project the pack's id there and Page
	// its page.
	Source  string `json:"source"`
	Project string `json:"project"`
	Page    string `json:"page"`
	Author  string `json:"author"`
	// Downloads is the pack's total on its source on Checked.
	Downloads int `json:"downloads"`
	// Version is the pack version the page describes, released on Released.
	Version  string `json:"version"`
	Released string `json:"released"`
	// Type is the server type (a registry id) and Loader its version.
	Type      string `json:"type"`
	Loader    string `json:"loader"`
	Minecraft string `json:"minecraft"`
	// Mods are the mod jars the server runs; PlayerMods the pack's mods that
	// only players need, which the install leaves off the server.
	Mods       int `json:"mods"`
	PlayerMods int `json:"playerMods,omitempty"`
	// DownloadMB is what Playkeeper's install downloads.
	DownloadMB int `json:"downloadMB"`
	// HeapMB is the Java heap the pack's own settings ask for, 0 when they
	// don't say.
	HeapMB int `json:"heapMB,omitempty"`
	// ServerFiles are the files the pack's authors publish for servers, when
	// they publish any.
	ServerFiles *ServerFiles `json:"serverFiles,omitempty"`
	// Template is the site's template (site/data/templates) that opens this
	// pack version in the visitor's own dashboard.
	Template string `json:"template,omitempty"`
	Checked  string `json:"checked"`
	card     *TemplateCard
}

// ServerFiles is a pack's own download for servers.
type ServerFiles struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	MB   int    `json:"mb"`
	Mods int    `json:"mods"`
}

// Path is the pack's page.
func (m *Modpack) Path() string { return "/modpacks/" + m.ID + "-server" }

// OneClick reports whether the page offers the pack's template, which it
// does unless the template is held (TemplateCard.Held). While it is, the
// page offers New server › A modpack alone.
func (m *Modpack) OneClick() bool { return m.card != nil && !m.card.Held() }

// Label is what the page calls the pack in running text.
func (m *Modpack) Label() string { return firstOf(m.Short, m.Name) }

// TypeName is the server type's name, like NeoForge.
func (m *Modpack) TypeName() string {
	t, _ := minecraft.TypeByID(m.Type)
	return t.Name
}

// SourceName is where the pack comes from, like CurseForge.
func (m *Modpack) SourceName() string {
	if m.Source == string(modpacks.CurseForge) {
		return "CurseForge"
	}
	return "Modrinth"
}

// Java is the Java the pack's Minecraft version runs on.
func (m *Modpack) Java() int { return minecraft.JavaFor(m.Minecraft) }

// MemoryMB is the memory Playkeeper suggests for the pack, as New server
// does: for its mods, or more when its own settings ask for a bigger heap.
func (m *Modpack) MemoryMB() int { return minecraft.PackNeedMB(m.Type, m.Mods, m.HeapMB) }

// Memory writes MemoryMB, like "12 GB".
func (m *Modpack) Memory() string { return gigabytes(m.MemoryMB()) }

// HeapGB writes the heap the pack asks for.
func (m *Modpack) HeapGB() string { return gigabytes(m.HeapMB) }

// JavaHeapMB is the Java heap Playkeeper gives the pack from MemoryMB: the
// rest is for the loader and the mods outside the heap.
func (m *Modpack) JavaHeapMB() int { return minecraft.HeapFor(m.MemoryMB(), m.Type, m.Mods) }

// JavaHeap writes JavaHeapMB, like "8.2 GB".
func (m *Modpack) JavaHeap() string { return gigabytes(m.JavaHeapMB()) }

// DownloadSize writes DownloadMB, like "1.4 GB" or "390 MB".
func (m *Modpack) DownloadSize() string { return megabytes(m.DownloadMB) }

// DownloadCount writes Downloads the way pack sites round them: "22M",
// "6.8M", "305k".
func (m *Modpack) DownloadCount() string {
	switch n := float64(m.Downloads); {
	case n >= 10e6:
		return strconv.FormatFloat(n/1e6, 'f', 0, 64) + "M"
	case n >= 1e6:
		return strings.TrimSuffix(strconv.FormatFloat(n/1e6, 'f', 1, 64), ".0") + "M"
	case n >= 1e3:
		return strconv.FormatFloat(n/1e3, 'f', 0, 64) + "k"
	}
	return itoa(m.Downloads)
}

// gbFlag writes memory as Java's and Docker's flags take it: "12G", or
// "6656M" when it isn't whole gigabytes.
func gbFlag(mb int) string {
	if mb%1024 == 0 {
		return itoa(mb/1024) + "G"
	}
	return itoa(mb) + "M"
}

func megabytes(mb int) string {
	if mb >= 1000 {
		return strings.TrimSuffix(strconv.FormatFloat(float64(mb)/1000, 'f', 1, 64), ".0") + " GB"
	}
	return itoa(mb) + " MB"
}

// loadModpacks reads the packs in dir, one .json each, and refuses a pack
// the release couldn't install as the page says: a type it doesn't run, a
// Minecraft version older than the packs it offers, or a template that
// isn't in the site's templates.
func loadModpacks(src fs.FS, dir string, cards map[string]*TemplateCard) (map[string]*Modpack, error) {
	entries, err := fs.ReadDir(src, dir)
	if err != nil {
		return nil, err
	}
	out := map[string]*Modpack{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if e.IsDir() || !ok {
			continue
		}
		b, err := fs.ReadFile(src, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		m := &Modpack{ID: id}
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if err := d.Decode(m); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if err := m.check(cards); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		out[id] = m
	}
	return out, nil
}

func (m *Modpack) check(cards map[string]*TemplateCard) error {
	switch {
	case m.Source != string(modpacks.CurseForge) && m.Source != string(addons.Modrinth):
		return fmt.Errorf("source %q is neither modrinth nor curseforge", m.Source)
	case m.Name == "" || m.Project == "" || m.Author == "" || m.Version == "" || m.Loader == "":
		return fmt.Errorf("a pack needs its name, project, author, version and loader version")
	case !strings.HasPrefix(m.Page, "https://"):
		return fmt.Errorf("page %q is not an https:// address", m.Page)
	case !hasType(m.Type) || !minecraft.ModLoader(m.Type):
		return fmt.Errorf("%q is not a mod loader the release runs", m.Type)
	case minecraft.CompareMinecraft(m.Minecraft, modpacks.DefaultMinMinecraft) < 0:
		return fmt.Errorf("Minecraft %s is older than %s, the oldest the release installs packs for", m.Minecraft, modpacks.DefaultMinMinecraft)
	case m.Mods <= 0 || m.Downloads <= 0 || m.DownloadMB <= 0:
		return fmt.Errorf("mods, downloads and downloadMB are counted, so none is zero")
	case m.Template != "" && cards[m.Template] == nil:
		return fmt.Errorf("template %q isn't in site/data/templates", m.Template)
	case m.Template != "" && (cards[m.Template].Pack != m.Project || cards[m.Template].PackVersion != m.Version):
		return fmt.Errorf("template %q opens %s %s, not the version the page describes", m.Template, cards[m.Template].Pack, cards[m.Template].PackVersion)
	case m.Template != "" && cards[m.Template].MemoryMB < m.MemoryMB():
		return fmt.Errorf("template %q suggests %d MB, less than the %d MB Playkeeper suggests for the pack", m.Template, cards[m.Template].MemoryMB, m.MemoryMB())
	case m.ServerFiles != nil && (!strings.HasPrefix(m.ServerFiles.URL, "https://") || m.ServerFiles.MB <= 0 || m.ServerFiles.Mods <= 0):
		return fmt.Errorf("server files need their address, size and mods")
	}
	for _, day := range []string{m.Released, m.Checked} {
		if _, err := time.Parse(time.DateOnly, day); err != nil {
			return fmt.Errorf("released and checked are days, YYYY-MM-DD: %w", err)
		}
	}
	m.card = cards[m.Template]
	return nil
}

func (s *Site) modpack(id string) (*Modpack, error) {
	m, ok := s.packs[id]
	if !ok {
		return nil, fmt.Errorf("no modpack %q in site/data/modpacks", id)
	}
	return m, nil
}

// modpackList is every pack with a page, the most downloaded first.
func (s *Site) modpackList() []*Modpack {
	var out []*Modpack
	for _, m := range s.packs {
		if s.byPath[m.Path()] != nil {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b *Modpack) int {
		return cmp.Or(cmp.Compare(b.Downloads, a.Downloads), cmp.Compare(a.ID, b.ID))
	})
	return out
}
