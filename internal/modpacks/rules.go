package modpacks

import (
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

// class is what happens to a pack path on a server.
type class int

const (
	// classPack files are the pack's: written, recorded, updated, removed.
	classPack class = iota
	// classClient files are for the game client only and are left out.
	classClient
	// classProtected files belong to the server or Playkeeper and are
	// never written: settings, player lists, start files, hidden files.
	classProtected
	// classWorld files go into the world only when the server has none
	// yet, and are never recorded.
	classWorld
)

var (
	clientFolders    = []string{"resourcepacks", "shaderpacks", "saves", "screenshots"}
	clientFiles      = []string{"options.txt", "optionsof.txt", "optionsshaders.txt", "servers.dat", "servers.dat_old"}
	protectedFolders = []string{"libraries", "versions", "logs", "crash-reports", "debug"}
	protectedFiles   = []string{
		"eula.txt", "server.properties", "ops.json", "whitelist.json", "banned-players.json", "banned-ips.json",
		"usercache.json", "usernamecache.json", "log4j2.xml", "user_jvm_args.txt",
	}
	startFiles = []string{".jar", ".sh", ".bat", ".cmd", ".ps1", ".exe", ".command"}
)

// classify sorts a checked pack path. world is the server's world folder;
// packs name theirs "world".
func classify(p, world string) class {
	first, _, nested := strings.Cut(p, "/")
	switch {
	case inWorld(p, world) || inWorld(p, "world"):
		return classWorld
	case strings.HasPrefix(first, "."), strings.Contains(path.Base(p), ".playkeeper-"):
		return classProtected
	case nested && slices.Contains(clientFolders, first):
		return classClient
	case nested && slices.Contains(protectedFolders, first):
		return classProtected
	case nested:
		return classPack
	case slices.Contains(clientFiles, first):
		return classClient
	case slices.Contains(protectedFiles, first), slices.Contains(startFiles, strings.ToLower(path.Ext(first))):
		return classProtected
	}
	return classPack
}

// inWorld reports whether p is in one of the world's folders: the world
// itself and the Nether and End folders Paper and Purpur keep beside it.
// level-name may name a folder inside another folder.
func inWorld(p, world string) bool {
	for _, dir := range [...]string{world, world + "_nether", world + "_the_end"} {
		if p == dir || strings.HasPrefix(p, dir+"/") {
			return true
		}
	}
	return false
}

// suggestible are the server.properties settings a pack may suggest: how the
// game plays, never how the server is reached, who may join or where the
// world is.
var suggestible = []string{
	"allow-flight", "allow-nether", "difficulty", "enable-command-block", "entity-broadcast-range-percentage",
	"force-gamemode", "function-permission-level", "gamemode", "generate-structures", "generator-settings",
	"hardcore", "initial-disabled-packs", "initial-enabled-packs", "level-seed", "level-type",
	"max-chained-neighbor-updates", "max-tick-time", "max-world-size", "network-compression-threshold",
	"op-permission-level", "player-idle-timeout", "pvp", "rate-limit", "simulation-distance", "spawn-animals",
	"spawn-monsters", "spawn-npcs", "spawn-protection", "sync-chunk-writes", "view-distance",
}

// suggestions reads the server.properties a pack ships. It returns the
// settings a pack may suggest and the names of the others, which are
// dropped. As in Java, a setting's last line wins.
func suggestions(b []byte) (map[string]string, []string) {
	raw := map[string]string{}
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || line[0] == '#' || line[0] == '!' {
			continue
		}
		i := strings.IndexAny(line, "=:")
		if i <= 0 {
			continue
		}
		if k := strings.TrimSpace(line[:i]); propertyName(k) {
			raw[k] = strings.TrimSpace(line[i+1:])
		}
	}
	props := map[string]string{}
	var dropped []string
	for k, v := range raw {
		if v, ok := unescape(v); ok && slices.Contains(suggestible, k) {
			props[k] = v
		} else {
			dropped = append(dropped, k)
		}
	}
	slices.Sort(dropped)
	return props, dropped
}

// unescape undoes the escapes Java writes into properties values and
// refuses values it cannot show plainly.
func unescape(v string) (string, bool) {
	if len(v) > 256 || !utf8.ValidString(v) {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c < 0x20 || c == 0x7f {
			return "", false
		}
		if c == '\\' {
			if i+1 == len(v) || !strings.ContainsRune(`:=#! \`, rune(v[i+1])) {
				return "", false
			}
			i++
			c = v[i]
		}
		b.WriteByte(c)
	}
	return b.String(), true
}

func propertyName(k string) bool {
	if k == "" || len(k) > 64 {
		return false
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_') {
			return false
		}
	}
	return true
}
