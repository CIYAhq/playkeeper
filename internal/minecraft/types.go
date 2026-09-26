package minecraft

// ServerType is one kind of Minecraft server software. Only available types
// can be created; the others are listed so the dashboard can show what comes
// next. Adding a type means adding its catalog and how its software is
// downloaded and verified, not changing how servers are stored. Paper is
// installed by the agent's Paper setup; every other available type must be
// one the software package installs (software.Supported), which its tests
// check.
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
	{ID: "vanilla", Name: "Vanilla", Available: true},
	{ID: "purpur", Name: "Purpur", Available: true},
	{ID: "fabric", Name: "Fabric", Available: true},
	{ID: "quilt", Name: "Quilt", Available: true},
	{ID: "neoforge", Name: "NeoForge", Available: true},
	{ID: "forge", Name: "Forge", Available: true},
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
