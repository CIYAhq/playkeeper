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
}

// cardExtra is what a card shows that the template itself doesn't say.
type cardExtra struct {
	Art string `json:"art"`
	// Mods is how many mods the pinned modpack version bundles, counted as
	// the dashboard counts them: the version's embedded projects on
	// Modrinth.
	Mods int `json:"mods,omitempty"`
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
			ID:   id,
			Name: t.Name,
			Art:  extra.Art,
			Link: "/t#" + link.Payload,
			Mods: t.Modpack != nil || strings.Contains(" fabric quilt neoforge forge ", " "+t.Server.Type+" "),
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
