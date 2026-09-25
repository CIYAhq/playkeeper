package minecraft

// ServerType is one kind of Minecraft server software. Only available types
// can be created; the others are listed so the dashboard can show what comes
// next. Adding a type means adding its catalog and how its software is
// downloaded and verified, not changing how servers are stored.
type ServerType struct {
	ID        string
	Name      string
	Available bool
}

// Types lists the server software Playkeeper knows, in the order the
// dashboard shows it. Spigot, Bukkit and Folia are left out on purpose: Paper
// runs Spigot and Bukkit plugins, and Folia only suits very large servers.
var Types = []ServerType{
	{ID: "paper", Name: "Paper", Available: true},
	{ID: "vanilla", Name: "Vanilla"},
	{ID: "purpur", Name: "Purpur"},
	{ID: "fabric", Name: "Fabric"},
	{ID: "quilt", Name: "Quilt"},
	{ID: "neoforge", Name: "NeoForge"},
}

// TypeByID finds a server type.
func TypeByID(id string) (ServerType, bool) {
	for _, t := range Types {
		if t.ID == id {
			return t, true
		}
	}
	return ServerType{}, false
}
