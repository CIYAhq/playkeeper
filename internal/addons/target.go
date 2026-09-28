package addons

import (
	"github.com/CIYAhq/playkeeper/internal/addons/hangar"
)

// Target is where and how add-ons go on one server type.
type Target struct {
	Type string `json:"type"`
	// Kind is plugin or mod, Modrinth's project type for the server.
	Kind string `json:"kind"`
	// Folder is plugins or mods, inside the server's data directory.
	Folder string `json:"folder"`
	// Loaders are the Modrinth loaders the server runs, its own platform's
	// first.
	Loaders []string `json:"loaders"`
	// own counts the loaders at the start of Loaders that are the server's
	// own platform. The rest are platforms whose add-ons it runs too:
	// Spigot and Bukkit on Paper and Purpur, Fabric on Quilt.
	own int
	// HangarPlatform is the Hangar platform, or empty when Hangar has
	// nothing for this type.
	HangarPlatform string `json:"hangarPlatform,omitempty"`
}

// TargetFor maps a server type (the ids in internal/minecraft's registry)
// to where its add-ons come from and go.
func TargetFor(serverType string) (Target, error) {
	switch serverType {
	case "paper":
		// Paper loads Bukkit and Spigot plugins as well as its own.
		return Target{Type: serverType, Kind: "plugin", Folder: "plugins", Loaders: []string{"paper", "spigot", "bukkit"}, own: 1, HangarPlatform: hangar.Paper}, nil
	case "purpur":
		// Purpur is a fork of Paper, so Paper's builds are its own.
		return Target{Type: serverType, Kind: "plugin", Folder: "plugins", Loaders: []string{"purpur", "paper", "spigot", "bukkit"}, own: 2, HangarPlatform: hangar.Paper}, nil
	case "fabric":
		return Target{Type: serverType, Kind: "mod", Folder: "mods", Loaders: []string{"fabric"}, own: 1}, nil
	case "quilt":
		// Quilt loads most Fabric mods.
		return Target{Type: serverType, Kind: "mod", Folder: "mods", Loaders: []string{"quilt", "fabric"}, own: 1}, nil
	case "neoforge":
		return Target{Type: serverType, Kind: "mod", Folder: "mods", Loaders: []string{"neoforge"}, own: 1}, nil
	case "forge":
		return Target{Type: serverType, Kind: "mod", Folder: "mods", Loaders: []string{"forge"}, own: 1}, nil
	case "vanilla":
		return Target{}, fail(KindNoAddons, kv("type", serverType),
			"Vanilla servers cannot load plugins or mods.",
			"Switch the server to Paper for plugins, or to Fabric, Quilt, NeoForge or Forge for mods.")
	}
	return Target{}, fail(KindUnknownServerType, kv("type", serverType),
		"Playkeeper does not know the server type \""+printable(serverType)+"\", so it cannot tell which add-ons fit.",
		"Choose a supported server type in the server's settings.")
}

// Name is the server type's name for messages.
func (t Target) Name() string {
	switch t.Type {
	case "paper":
		return "Paper"
	case "purpur":
		return "Purpur"
	case "fabric":
		return "Fabric"
	case "quilt":
		return "Quilt"
	case "neoforge":
		return "NeoForge"
	case "forge":
		return "Forge"
	}
	return t.Type
}

// ownLoaders are the Modrinth loaders of the server's own platform.
func (t Target) ownLoaders() []string {
	return t.Loaders[:t.own]
}

// Sources lists where add-ons for the target come from.
func (t Target) Sources() []Source {
	if t.HangarPlatform != "" {
		return []Source{Modrinth, Hangar}
	}
	return []Source{Modrinth}
}

func (t Target) supports(s Source) error {
	switch s {
	case Modrinth:
		return nil
	case Hangar:
		if t.HangarPlatform != "" {
			return nil
		}
		return fail(KindSourceUnsupported, kv("source", s.Name(), "type", t.Type),
			"Hangar only has plugins, and this server runs mods.", "Search Modrinth instead.")
	}
	return fail(KindInvalid, kv("field", "source"), "Add-ons come from Modrinth or Hangar, not \""+printable(string(s))+"\".", "")
}

func printable(s string) string {
	r := []rune(s)
	if len(r) > 40 {
		r = append(r[:40], '…')
	}
	for i, c := range r {
		if c < 0x20 || c == 0x7f {
			r[i] = '?'
		}
	}
	return string(r)
}
