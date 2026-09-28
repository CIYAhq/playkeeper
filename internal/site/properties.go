package site

import (
	"slices"
	"strconv"
	"strings"
)

// The server.properties editor (/tools/server-properties) lists every setting
// the Minecraft server reads, in groups, each with what it does, its default
// and a control. js/tools/server-properties.js reads the rows the page draws
// from these, so this is the one list; properties_test.go holds it to the
// file the 26.3 server writes (testdata/server-26.3.properties). Descriptions
// follow minecraft.wiki's Server.properties (28 Sep 2026), checked against
// the server's code where it was unclear.

// A ServerProperty is one setting. Kind is bool, int, choice or text; an int
// has a Min and Max when the server limits it ("" for none), and a choice
// its Choices.
type ServerProperty struct {
	Key, Default, Kind, What string
	Min, Max                 string
	Choices                  []PropertyChoice
}

// A PropertyChoice is a value a choice can take, with its name.
type PropertyChoice struct{ Value, Label string }

// A PropertyGroup is a heading on the page and the settings under it.
type PropertyGroup struct {
	ID, Name string
	Props    []ServerProperty
}

func boolProp(key, def, what string) ServerProperty {
	return ServerProperty{Key: key, Default: def, Kind: "bool", What: what}
}

func intProp(key, def, min, max, what string) ServerProperty {
	return ServerProperty{Key: key, Default: def, Kind: "int", Min: min, Max: max, What: what}
}

func textProp(key, def, what string) ServerProperty {
	return ServerProperty{Key: key, Default: def, Kind: "text", What: what}
}

func choiceProp(key, def, what string, choices ...PropertyChoice) ServerProperty {
	return ServerProperty{Key: key, Default: def, Kind: "choice", What: what, Choices: choices}
}

// Range says an int's limits, like "3 to 32", or "".
func (p ServerProperty) Range() string {
	switch {
	case p.Min != "" && p.Max != "":
		return p.Min + " to " + p.Max
	case p.Min != "":
		return p.Min + " or more"
	}
	return ""
}

// ID is the row's id, for links like #view-distance.
func (p ServerProperty) ID() string { return strings.ReplaceAll(p.Key, ".", "-") }

const maxPort = "65534"

var propertyGroups = []PropertyGroup{
	{"gameplay", "Gameplay", []ServerProperty{
		choiceProp("difficulty", "easy", "How much mobs hurt and hunger drains. Peaceful has no hostile mobs.",
			PropertyChoice{"peaceful", "Peaceful"}, PropertyChoice{"easy", "Easy"}, PropertyChoice{"normal", "Normal"}, PropertyChoice{"hard", "Hard"}),
		choiceProp("gamemode", "survival", "The game mode players start in.",
			PropertyChoice{"survival", "Survival"}, PropertyChoice{"creative", "Creative"}, PropertyChoice{"adventure", "Adventure"}, PropertyChoice{"spectator", "Spectator"}),
		boolProp("force-gamemode", "false", "Puts players back in that game mode every time they join."),
		boolProp("hardcore", "false", "Makes new worlds hardcore: the hardest difficulty, and a player who dies becomes a spectator."),
		boolProp("allow-flight", "false", "Lets players fly in Survival with a mod. Off, the server kicks players it sees flying."),
		intProp("spawn-protection", "16", "0", "", "How far around spawn only operators can build: 16 protects a 33 × 33 square. 0 turns it off, and so does having no operators."),
	}},
	{"joining", "Who can join", []ServerProperty{
		intProp("max-players", "20", "0", "", "The most players online at once. Operators with bypassesPlayerLimit in ops.json can join a full server."),
		boolProp("white-list", "true", "Only players on the whitelist, in whitelist.json, can join; operators always can. On by default since 26.3."),
		boolProp("enforce-whitelist", "false", "Kicks online players who aren't on the whitelist when it's reloaded."),
		boolProp("online-mode", "true", "Checks each player's account with Mojang. Off, anyone can join under any name: only for servers behind a proxy that checks them."),
		boolProp("enforce-secure-profile", "true", "Only lets in players with a chat key Mojang signed. Off, chat isn't signed and can't be reported."),
		boolProp("prevent-proxy-connections", "false", "Kicks players who connect from a different network than the one they signed in to Mojang from, as through a VPN."),
		boolProp("accepts-transfers", "false", "Lets other servers send players here with /transfer. Off, they're disconnected."),
		intProp("player-idle-timeout", "0", "0", "", "Kicks players idle for this many minutes. 0 never does."),
		intProp("chat-spam-threshold-seconds", "10", "0", "", "Kicks players who chat too fast: each message adds a second that drains in real time, and reaching this many kicks. 0 turns it off."),
		intProp("command-spam-threshold-seconds", "10", "0", "", "The same for commands."),
		intProp("rate-limit", "0", "0", "", "Kicks players who send more than this many packets a second on average. 0 turns it off."),
		boolProp("enable-code-of-conduct", "false", "Shows players the code of conduct in the codeofconduct folder, one file per language like en_us.txt."),
	}},
	{"operators", "Operators and the console", []ServerProperty{
		intProp("op-permission-level", "4", "0", "4", "The permission level /op gives. 4 allows every command, /stop included."),
		intProp("function-permission-level", "2", "1", "4", "The permission level data pack functions run with."),
		boolProp("broadcast-console-to-ops", "true", "Sends operators online what commands typed in the console reply."),
		boolProp("broadcast-rcon-to-ops", "true", "Sends operators online what commands sent over RCON reply."),
		boolProp("log-ips", "true", "Writes players' IP addresses in the console and the log."),
	}},
	{"list", "Server list", []ServerProperty{
		textProp("motd", "A Minecraft Server", "The message under the server's name in the server list, on up to two lines. The MOTD generator writes colours and line breaks as this file needs them."),
		boolProp("enable-status", "true", "Answers the server list. Off, the server shows as offline, but players can still join."),
		boolProp("hide-online-players", "false", "Leaves who's online out of the server list."),
		textProp("bug-report-link", "", "A web address players get as the server's Report Bug link. Empty sends none."),
	}},
	{"world", "World", []ServerProperty{
		textProp("level-name", "world", "The world's folder. If there's no world in it yet, the server makes a new one there."),
		textProp("level-seed", "", "The seed for a new world. Empty picks one at random."),
		choiceProp("level-type", "minecraft:normal", "The kind of world a new one is.",
			PropertyChoice{"minecraft:normal", "Normal"}, PropertyChoice{"minecraft:flat", "Flat"}, PropertyChoice{"minecraft:large_biomes", "Large biomes"}, PropertyChoice{"minecraft:amplified", "Amplified"}, PropertyChoice{"minecraft:single_biome_surface", "Single biome"}),
		textProp("generator-settings", "{}", "Settings for that kind, as JSON: a flat world's layers, or a single biome's biome."),
		boolProp("generate-structures", "true", "Generates villages, temples and other structures in new chunks. Dungeons generate either way."),
		intProp("max-world-size", "29999984", "1", "29999984", "How far from the centre, in blocks, the world border can go."),
		textProp("initial-enabled-packs", "vanilla", "Data packs a new world starts with, separated by commas. Feature packs, like an upcoming update's, have to be listed here."),
		textProp("initial-disabled-packs", "", "Data packs a new world leaves off, separated by commas."),
		choiceProp("region-file-compression", "deflate", "How chunks are compressed on disk. lz4 is faster and takes more space; a chunk changes over when it's next saved.",
			PropertyChoice{"deflate", "deflate"}, PropertyChoice{"lz4", "lz4"}, PropertyChoice{"none", "none"}),
		boolProp("sync-chunk-writes", "true", "Waits for each chunk to reach the disk, so a crash can't lose or break it, at some cost in speed."),
	}},
	{"performance", "Performance", []ServerProperty{
		intProp("view-distance", "10", "3", "32", "How far the server sends the world around each player, in chunks. Further needs more memory."),
		intProp("simulation-distance", "10", "3", "32", "How far around each player mobs, crops and redstone keep running, in chunks."),
		intProp("entity-broadcast-range-percentage", "100", "10", "1000", "How far away players see mobs and other entities, as a percentage of the usual distance."),
		intProp("pause-when-empty-seconds", "60", "", "", "Pauses the server this many seconds after the last player leaves. 0 or less never pauses."),
		intProp("max-tick-time", "60000", "-1", "", "Stops the server, as crashed, when one tick takes longer than this many milliseconds. -1 never does."),
		intProp("network-compression-threshold", "256", "-1", "", "Compresses packets of this many bytes or more. -1 compresses none, 0 all."),
		intProp("max-chained-neighbor-updates", "1000000", "", "", "How many block updates one change can set off in a row before the rest are skipped. Negative has no limit."),
		boolProp("use-native-transport", "true", "Uses Linux's faster networking when it can."),
	}},
	{"network", "Network", []ServerProperty{
		textProp("server-ip", "", "The address the server listens on. Empty listens on all of them, which is what you want."),
		intProp("server-port", "25565", "1", maxPort, "The TCP port players connect to. Any other than 25565 goes after the address, like :25566."),
		boolProp("enable-query", "false", "Turns on query, which tools use to read the server's details and who's online."),
		intProp("query.port", "25565", "1", maxPort, "The UDP port query listens on."),
		boolProp("enable-rcon", "false", "Turns on RCON, a remote console. It isn't encrypted, so keep its port off the internet."),
		intProp("rcon.port", "25575", "1", maxPort, "The TCP port RCON listens on."),
		textProp("rcon.password", "", "RCON's password. With RCON on and no password, RCON doesn't start."),
	}},
	{"pack", "Resource pack", []ServerProperty{
		textProp("resource-pack", "", "A link to a resource pack players are offered when they join, of 250 MiB at most."),
		boolProp("require-resource-pack", "false", "Disconnects players who turn the pack down."),
		textProp("resource-pack-sha1", "", "The pack's SHA-1, 40 characters in lower case, so players' games can check the download."),
		textProp("resource-pack-id", "", "A UUID for the pack, so games can tell it from others."),
		textProp("resource-pack-prompt", "", "The message offering a required pack, as a text component. Empty uses the game's own."),
	}},
	{"management", "Management API", []ServerProperty{
		boolProp("management-server-enabled", "false", "Turns on the Minecraft Server Management Protocol, which lets tools manage the server over a WebSocket."),
		textProp("management-server-host", "localhost", "The address the API listens on."),
		intProp("management-server-port", "0", "0", maxPort, "The API's port. 0 picks a new one at each start."),
		textProp("management-server-secret", "", "The secret tools send: 40 letters and digits. Empty, the server makes one and writes it here."),
		boolProp("management-server-tls-enabled", "true", "Encrypts the API with TLS. With TLS on and no keystore, the server won't start."),
		textProp("management-server-tls-keystore", "", "The keystore file with the API's TLS certificate."),
		textProp("management-server-tls-keystore-password", "", "The keystore's password. The MINECRAFT_MANAGEMENT_TLS_KEYSTORE_PASSWORD environment variable can hold it instead."),
		textProp("management-server-allowed-origins", "", "Web origins allowed to connect, separated by commas. Empty, no tool in a browser can."),
		intProp("status-heartbeat-interval", "0", "0", "", "How often the API sends connected tools a heartbeat. 0 sends none."),
	}},
	{"advanced", "Advanced", []ServerProperty{
		boolProp("enable-jmx-monitoring", "false", "Publishes tick times over JMX for monitoring tools; Java needs JMX turned on too."),
		textProp("text-filtering-config", "", "Chat filtering settings Realms uses, not documented for other servers."),
		intProp("text-filtering-version", "0", "0", "1", "The format of text-filtering-config: 0 or 1."),
	}},
}

// retiredProperties are settings the server no longer reads, with where they
// went, for a file from an older version.
var retiredProperties = map[string]string{
	"pvp":                  "A game rule since 1.21.9: /gamerule pvp false.",
	"allow-nether":         "A game rule since 1.21.9: allow_entering_nether_using_portals.",
	"enable-command-block": "A game rule since 1.21.9: command_blocks_work.",
	"spawn-monsters":       "A game rule since 1.21.9: spawn_monsters.",
	"spawn-animals":        "Removed in 1.21.2.",
	"spawn-npcs":           "Removed in 1.21.2.",
}

// propertiesHeader starts the file the editor writes; the server puts its own
// two lines there when it next starts.
const propertiesHeader = "#Minecraft server properties\n#Made at playkeeper.io/tools/server-properties\n"

// escapeProperty writes a value as java.util.Properties does: \, :, =, # and
// ! escaped, a leading space too, and anything past ASCII as \uXXXX, which
// the server reads whatever the file's encoding.
func escapeProperty(v string) string {
	var b strings.Builder
	for i, r := range v {
		switch {
		case r == '\\' || r == ':' || r == '=' || r == '#' || r == '!' || r == ' ' && i == 0:
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20 || r > 0x7e:
			for _, u := range utf16Units(r) {
				b.WriteString(`\u` + strings.ToUpper(strconv.FormatInt(int64(u)|0x10000, 16)[1:]))
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func utf16Units(r rune) []uint16 {
	if r < 0x10000 {
		return []uint16{uint16(r)}
	}
	r -= 0x10000
	return []uint16{uint16(0xd800 + r>>10), uint16(0xdc00 + r&0x3ff)}
}

// allProperties is every setting, sorted by key as the server writes them.
func allProperties() []ServerProperty {
	var out []ServerProperty
	for _, g := range propertyGroups {
		out = append(out, g.Props...)
	}
	slices.SortFunc(out, func(a, b ServerProperty) int { return strings.Compare(a.Key, b.Key) })
	return out
}

// defaultProperties is the file with every setting at its default.
func defaultProperties() string {
	var b strings.Builder
	b.WriteString(propertiesHeader)
	for _, p := range allProperties() {
		b.WriteString(p.Key + "=" + escapeProperty(p.Default) + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// PropertiesData is what the page shows from Go.
type PropertiesData struct {
	Groups  []PropertyGroup
	Retired map[string]string
	Count   int
	File    string
}

func init() {
	toolData["properties"] = func() any {
		return PropertiesData{Groups: propertyGroups, Retired: retiredProperties, Count: len(allProperties()), File: defaultProperties()}
	}
}
