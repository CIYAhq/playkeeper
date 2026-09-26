package modpacks

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/modpacks/mrpack"
)

// releaseVersion matches Minecraft release versions ("1.21.1", "26.2").
var releaseVersion = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}(\.[0-9]{1,3})?$`)

// typeForLoader maps a loader, as Modrinth's dependency ids and CurseForge's
// loader names spell it, to a server type.
func typeForLoader(loader string) (string, bool) {
	switch loader {
	case "":
		return "vanilla", true
	case mrpack.FabricLoader, "fabric":
		return "fabric", true
	case mrpack.QuiltLoader, "quilt":
		return "quilt", true
	case mrpack.NeoForge:
		return "neoforge", true
	case mrpack.Forge:
		return "forge", true
	}
	return "", false
}

func typeName(t string) string {
	switch t {
	case "fabric":
		return "Fabric"
	case "quilt":
		return "Quilt"
	case "neoforge":
		return "NeoForge"
	case "forge":
		return "Forge"
	case "vanilla":
		return "Vanilla"
	case "paper":
		return "Paper"
	case "purpur":
		return "Purpur"
	}
	return printable(t)
}

// requirements checks what a pack needs against what Playkeeper can run.
func (l *Library) requirements(name, loader, loaderVersion, mc string) (Requirements, error) {
	t, ok := typeForLoader(loader)
	switch {
	case !ok:
		return Requirements{}, fail(KindUnknownLoader, kv("pack", name, "loader", printable(loader)),
			fmt.Sprintf("%s needs a mod loader Playkeeper does not know (%s).", name, printable(loader)),
			"Choose another pack.")
	}
	if n := l.minecraftUnsupported(name, mc); n != nil {
		return Requirements{}, &addons.Error{Notice: *n}
	}
	if !slices.Contains(l.types(), t) {
		return Requirements{}, fail(KindTypeUnavailable, kv("pack", name, "type", t),
			fmt.Sprintf("%s needs a %s server, and Playkeeper cannot run %s servers yet.", name, typeName(t), typeName(t)),
			"Choose a pack for a server type Playkeeper runs.")
	}
	r := Requirements{Type: t, MinecraftVersion: mc}
	if t != "vanilla" {
		r.LoaderVersion = loaderVersion
	}
	// Some packs name a Forge build the way Forge's Maven does, with its
	// Minecraft version first: "26.2-65.1.3" is Forge 65.1.3.
	if t == "forge" {
		r.LoaderVersion = strings.TrimPrefix(r.LoaderVersion, mc+"-")
	}
	return r, nil
}

// minecraftUnsupported explains why Playkeeper cannot run Minecraft mc for
// a pack, or returns nil when it can.
func (l *Library) minecraftUnsupported(name, mc string) *addons.Notice {
	switch {
	case !releaseVersion.MatchString(mc):
		n := notice(KindMinecraft, kv("pack", name, "minecraft", printable(mc)),
			fmt.Sprintf("%s is for Minecraft %s, which is not a release version.", name, printable(mc)),
			"Playkeeper runs release versions of Minecraft. Choose another version of the pack.")
		return &n
	case minecraft.CompareMinecraft(mc, l.minMinecraft()) < 0:
		n := notice(KindMinecraft, kv("pack", name, "minecraft", mc, "oldest", l.minMinecraft()),
			fmt.Sprintf("%s is for Minecraft %s, and Playkeeper runs Minecraft %s and newer.", name, mc, l.minMinecraft()),
			"Choose a newer version of the pack.")
		return &n
	}
	return nil
}
