package site

import (
	"slices"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// ServerType is a server type the release can create, as the site shows it.
type ServerType struct {
	ID, Name string
	// Logo is the asset of its logo, the dashboard's own file.
	Logo string
	// Mods is whether it runs mods and modpacks rather than plugins.
	Mods bool
}

// The site shows each type with the dashboard's logo for it (web/src/assets,
// whose NOTICE.md gives each file's source and licence).
var typeLogos = map[string]string{
	"paper":    "app/logos/papermc_logo.min.svg",
	"purpur":   "app/logos/purpur.svg",
	"vanilla":  "app/pixel-art/type-vanilla.svg",
	"fabric":   "app/logos/fabric-icon.png",
	"quilt":    "app/logos/quilt_logo_dark.svg",
	"neoforge": "app/logos/neoforged-logo.svg",
	"forge":    "app/logos/forge-apple-touch-icon.png",
}

// modLoaders are the types that run mods and modpacks.
var modLoaders = []string{"fabric", "quilt", "neoforge", "forge"}

// Two orders: sentences go plugins, Vanilla, then mod loaders; the logo
// row keeps the design's, with Vanilla's pixel art last.
var (
	textOrder = []string{"paper", "purpur", "vanilla", "fabric", "quilt", "neoforge", "forge"}
	rowOrder  = []string{"paper", "purpur", "fabric", "quilt", "neoforge", "forge", "vanilla"}
)

// serverTypes lists the types the release can create, from the same list the
// dashboard offers (minecraft.Types), in the given order. A type added there
// shows up here, and in every count and sentence built from it.
func serverTypes(order []string) []ServerType {
	var out []ServerType
	for _, t := range minecraft.Types {
		if !t.Available {
			continue
		}
		out = append(out, ServerType{ID: t.ID, Name: t.Name, Logo: typeLogos[t.ID], Mods: slices.Contains(modLoaders, t.ID)})
	}
	rank := func(id string) int {
		if i := slices.Index(order, id); i >= 0 {
			return i
		}
		return len(order)
	}
	slices.SortStableFunc(out, func(a, b ServerType) int { return rank(a.ID) - rank(b.ID) })
	return out
}

// hasType reports whether the release can create servers of the type.
func hasType(id string) bool {
	return slices.ContainsFunc(serverTypes(textOrder), func(t ServerType) bool { return t.ID == id })
}

// loaderNames names the mod loaders the release runs, as in "Fabric, Quilt,
// NeoForge and Forge".
func loaderNames() []string {
	var out []string
	for _, t := range serverTypes(textOrder) {
		if t.Mods {
			out = append(out, t.Name)
		}
	}
	return out
}

var numberWords = []string{"no", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve"}

// countWord writes small numbers as words, as the site's copy does.
func countWord(n int) string {
	if n >= 0 && n < len(numberWords) {
		return numberWords[n]
	}
	return itoa(n)
}

// andList joins names as a sentence does: "a", "a and b", "a, b and c".
func andList(names []string) string { return joinList(names, "and") }

// joinList joins names with commas and conj before the last.
func joinList(names []string, conj string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	s := names[0]
	for _, n := range names[1 : len(names)-1] {
		s += ", " + n
	}
	return s + " " + conj + " " + names[len(names)-1]
}
