package site

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/templates"
	"github.com/CIYAhq/playkeeper/internal/templates/checks"
)

// TemplateCard is a server template the site offers, opened in the visitor's
// own dashboard through the share page.
type TemplateCard struct {
	ID   string
	Name string
	// Art is the pixel scene on the card.
	Art string
	// Facts is "Paper · Minecraft 26.2 · 4 GB"; Short drops the version,
	// for phones.
	Facts, Short string
	// Holds is what it adds: its add-ons, or its modpack and how many mods
	// that has.
	Holds string
	// Link is the share page with the template after #.
	Link string
	// Mods is whether it runs mods (a modded server's template).
	Mods bool
	// MemoryMB is the memory it suggests, and Pack and PackVersion the
	// modpack project and version it pins, if it has one.
	MemoryMB          int
	Pack, PackVersion string
	// Template is the template itself, for the pages that describe it.
	Template *templates.Template
	// OpensFrom is the first Playkeeper release that opens the template, set
	// only while that release isn't out. Until then no page links it.
	OpensFrom string
	// Failing is set when its last check failed (site/data/checks), and no
	// page links it either until it's fixed.
	Failing bool
	// Crossplay is set when its last check turned on crossplay and Geyser
	// and Floodgate started beside its add-ons, so Bedrock friends can join
	// a server made from it once crossplay is on in its Settings.
	Crossplay bool
	// Added is the day the directory first listed it, and Popularity what
	// sorts it under Popular, most first.
	Added      string
	Popularity int
	// Categories are the directory's categories it's in, its primary first,
	// and Tags what it adds, its own and its add-ons'; the directory fills
	// them in from site/data/taxonomy.json.
	Categories []*Category
	Tags       []*Tag
	// check is what happened the last time a server was created and started
	// from it (site/data/checks), when that passed, and pack its modpack's
	// page, when it has one.
	check        *LibraryPage
	pack         *Modpack
	modpackCheck *checks.Modpack
	categoryIDs  []string
	tagIDs       []string
	modpackMods  int
	modpackTitle string
	// icons are what it installs' icons, by Project.key (Site.addIcons).
	icons map[string]*asset
}

// Held reports whether pages leave the template out: the release people
// install can't open it yet, or it failed its last check.
func (c *TemplateCard) Held() bool { return c.OpensFrom != "" || c.Failing }

// failing marks the cards whose templates failed their last check.
func failing(cards map[string]*TemplateCard, checked map[string]*checks.Check) {
	for id, c := range checked {
		if card := cards[id]; card != nil && c.Status == checks.Failing {
			card.Failing = true
		}
	}
}

// crossplays marks the cards whose templates' last check passed with
// crossplay on.
func crossplays(cards map[string]*TemplateCard, checked map[string]*checks.Check) {
	for id, c := range checked {
		if card := cards[id]; card != nil && c.Status == checks.Passing && c.Crossplay {
			card.Crossplay = true
		}
	}
}

// cardExtra is what a card shows that the template itself doesn't say.
type cardExtra struct {
	Art string `json:"art"`
	// Mods is how many mods the pinned modpack version bundles, counted as
	// the dashboard counts them: the version's embedded projects on
	// Modrinth.
	Mods      int    `json:"mods,omitempty"`
	OpensFrom string `json:"opensFrom,omitempty"`
	// The directory's: see site/data/taxonomy.json and Directory.
	Categories []string `json:"categories,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Added      string   `json:"added,omitempty"`
	Popularity int      `json:"popularity,omitempty"`
}

// loadTemplateCards reads the templates in dir (one .json each, in the format
// of internal/templates) and cards.json beside them.
func loadTemplateCards(src fs.FS, dir string) (map[string]*TemplateCard, error) {
	b, err := fs.ReadFile(src, path.Join(dir, "cards.json"))
	if err != nil {
		return nil, err
	}
	extras := map[string]cardExtra{}
	if err := json.Unmarshal(b, &extras); err != nil {
		return nil, fmt.Errorf("cards.json: %w", err)
	}
	cards := map[string]*TemplateCard{}
	for id, extra := range extras {
		if extra.OpensFrom != "" && !reRelease3.MatchString(extra.OpensFrom) {
			return nil, fmt.Errorf("cards.json: %s opens from %q, which isn't a release like 0.4.4", id, extra.OpensFrom)
		}
		raw, err := fs.ReadFile(src, path.Join(dir, id+".json"))
		if err != nil {
			return nil, err
		}
		var t templates.Template
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&t); err != nil {
			return nil, fmt.Errorf("%s.json: %w", id, err)
		}
		link, err := templates.NewLink(&t)
		if err != nil {
			return nil, fmt.Errorf("%s.json: %w", id, err)
		}
		if link.Warning != nil {
			return nil, fmt.Errorf("%s.json makes a link too long for chat apps (%d characters)", id, len(link.URL))
		}
		typeName := t.Server.Type
		if st, ok := minecraft.TypeByID(t.Server.Type); ok {
			typeName = st.Name
		}
		c := &TemplateCard{
			ID:       id,
			Name:     t.Name,
			Art:      extra.Art,
			Link:     "/t#" + link.Payload,
			Mods:     t.Modpack != nil || strings.Contains(" fabric quilt neoforge forge ", " "+t.Server.Type+" "),
			MemoryMB: t.Settings.MemoryMB,
			Template: &t,
		}
		c.OpensFrom = extra.OpensFrom
		c.Added, c.Popularity = extra.Added, extra.Popularity
		c.categoryIDs, c.tagIDs, c.modpackMods = extra.Categories, extra.Tags, extra.Mods
		if t.Modpack != nil {
			c.Pack, c.PackVersion = t.Modpack.Project, t.Modpack.Pin.VersionNumber
			c.modpackTitle = t.Modpack.Name
		}
		mem := ""
		if t.Settings.MemoryMB > 0 {
			mem = gigabytes(t.Settings.MemoryMB)
		}
		c.Facts = strings.Join(nonEmpty(typeName, "Minecraft "+t.Server.MinecraftVersion, mem), " · ")
		c.Short = strings.Join(nonEmpty(typeName, mem), " · ")
		switch {
		case t.Modpack != nil && extra.Mods > 0:
			c.Holds = fmt.Sprintf("%s, %d mods", t.Modpack.Name, extra.Mods)
		case t.Modpack != nil:
			c.Holds = t.Modpack.Name
		default:
			var names []string
			for _, a := range t.Addons {
				names = append(names, a.Name)
			}
			c.Holds = andList(names)
		}
		cards[id] = c
	}
	return cards, nil
}

func gigabytes(mb int) string {
	if mb%1024 == 0 {
		return itoa(mb/1024) + " GB"
	}
	return fmt.Sprintf("%.1f GB", float64(mb)/1024)
}

func nonEmpty(xs ...string) []string {
	var out []string
	for _, x := range xs {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}
