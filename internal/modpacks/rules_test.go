package modpacks

import (
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	names := map[class]string{classPack: "pack", classClient: "client", classProtected: "protected", classWorld: "world"}
	for _, c := range []struct {
		world string
		want  class
		paths []string
	}{
		{"survival", classPack, []string{
			"mods/a.jar", "mods/server.jar", "mods/options.txt", "config/a.toml", "config/.hidden", "config/run.sh",
			"kubejs/server_scripts/a.js", "defaultconfigs/eula.txt", "datapacks/logs/x.zip", "credits.txt",
			"worlds/x.zip", "survival-backup/level.dat",
		}},
		{"survival", classWorld, []string{
			"survival/level.dat", "survival_nether/DIM-1/region/r.0.0.mca", "survival_the_end/DIM1/region/r.0.0.mca",
			"world", "world/level.dat", "world_nether/level.dat", "world_the_end/level.dat",
		}},
		{"survival", classClient, []string{
			"resourcepacks/r.zip", "shaderpacks/s.zip", "saves/w/level.dat", "screenshots/s.png",
			"options.txt", "optionsof.txt", "optionsshaders.txt", "servers.dat", "servers.dat_old",
		}},
		{"survival", classProtected, []string{
			"server.properties", "eula.txt", "ops.json", "whitelist.json", "banned-players.json", "banned-ips.json",
			"usercache.json", "usernamecache.json", "log4j2.xml", "user_jvm_args.txt",
			"server.jar", "Server.JAR", "run.sh", "run.BAT", "start.cmd", "start.ps1", "server.exe", "start.command",
			"libraries/net/x.jar", "versions/26.2/server-26.2.jar", "logs/latest.log", "crash-reports/c.txt", "debug/d.txt",
			".env", ".hidden", ".fabric/remapped.jar", "mods/.a.jar.playkeeper-new-0123456789ab",
		}},
		{"worlds/main", classWorld, []string{"worlds/main/level.dat", "worlds/main_nether/level.dat", "world/level.dat"}},
		{"worlds/main", classPack, []string{"worlds/other/level.dat", "worlds/mainly.txt"}},
	} {
		for _, p := range c.paths {
			if got := classify(p, c.world); got != c.want {
				t.Errorf("classify(%q, %q) = %s, want %s", p, c.world, names[got], names[c.want])
			}
		}
	}
}

func TestSuggestions(t *testing.T) {
	props := strings.Join([]string{
		"# Minecraft server properties",
		"! also a comment",
		"   difficulty=hard",
		"pvp : false",
		`level-seed=abc\:def\=ghi\\jkl`,
		"spawn-protection=",
		`generator-settings={"biome":"minecraft:plains"}`,
		"view-distance=12\r",
		"view-distance=10",
		"hardcore=false",
		`hardcore=true\`,
		"spawn-monsters=\x01",
		"spawn-monsters=false",
		`level-type=minecraft\u003aflat`,
		"gamemode=survival\x01",
		"max-world-size=\xff",
		"simulation-distance=" + strings.Repeat("8", 257),
		"PVP=true",
		"Difficulty=peaceful",
		"bad key=1",
		"=orphan",
		"no separator",
		"server-port=25570",
		"rcon.password=hunter2",
		"enable-rcon=true",
		"motd=Hello",
		"server-port=25571",
		"level-name=../elsewhere",
	}, "\n")
	got, dropped := suggestions([]byte(props))
	sameJSON(t, "suggested settings", got, map[string]string{
		"difficulty": "hard", "pvp": "false", "level-seed": `abc:def=ghi\jkl`, "spawn-protection": "",
		"generator-settings": `{"biome":"minecraft:plains"}`, "view-distance": "10", "spawn-monsters": "false",
	})
	wantList(t, "dropped settings", dropped, "enable-rcon", "gamemode", "hardcore", "level-name", "level-type",
		"max-world-size", "motd", "rcon.password", "server-port", "simulation-distance")
}

func TestPrintable(t *testing.T) {
	for in, want := range map[string]string{
		"Fabulously Optimized":            "Fabulously Optimized",
		"Création 1.2 — 日本語":              "Création 1.2 — 日本語",
		"tab\there\nnew line":             "tab?here?new line",
		"nul\x00 del\x7f csi\u009b":       "nul? del? csi?",
		"evil\u202egpj.jar":               "evil?gpj.jar",
		"zero\u200bwidth\ufeff":           "zero?width?",
		"letter mark\u061c isolate\u2066": "letter mark? isolate?",
		"line\u2028paragraph\u2029":       "line?paragraph?",
		"bad \xff\xfe bytes":              "bad ? bytes",
		strings.Repeat("é", 80):           strings.Repeat("é", 80),
		strings.Repeat("é", 81):           strings.Repeat("é", 80) + "…",
	} {
		if got := printable(in); got != want {
			t.Errorf("printable(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidID(t *testing.T) {
	for _, s := range []string{"TSTpk001", "EZaeTUP8", "vanilla-perfected", "create_plus", "1.0.2", "9100001", strings.Repeat("a", 64)} {
		if !validID(s) {
			t.Errorf("validID(%q) = false", s)
		}
	}
	for _, s := range []string{"", ".", "..", "a/b", `a\b`, "a b", "a?b", "a%2fb", "é", strings.Repeat("a", 65)} {
		if validID(s) {
			t.Errorf("validID(%q) = true", s)
		}
	}
}

func TestPlainJar(t *testing.T) {
	for _, s := range []string{"x.jar", "ferritecore-9.0.0-fabric.jar", "Mod Menu 1.0.jar", "jei_1.21+fabric.jar", strings.Repeat("a", 124) + ".jar"} {
		if !plainJar(s) {
			t.Errorf("plainJar(%q) = false", s)
		}
	}
	for _, s := range []string{
		"", ".jar", ".a.jar", "-a.jar", " a.jar", "a.zip", "a.JAR", "a.jar.exe", "a/b.jar", `a\b.jar`, "../a.jar",
		"a:b.jar", "a*.jar", "a?.jar", `a".jar`, "a<b>.jar", "a|b.jar", "a\x00.jar", "evil\u202e.jar",
		strings.Repeat("a", 125) + ".jar",
	} {
		if plainJar(s) {
			t.Errorf("plainJar(%q) = true", s)
		}
	}
}

func TestTreeClash(t *testing.T) {
	for _, c := range []struct {
		paths []string
		want  string
	}{
		{nil, ""},
		{[]string{"mods/a.jar", "mods/b.jar", "config/a/b.toml"}, ""},
		{[]string{"config/a", "config/a/b.toml"}, "config/a"},
		{[]string{"a/b/c/d", "a/b"}, "a/b"},
		{[]string{"ab/c", "a"}, ""},
		{[]string{"a/bc", "a/b"}, ""},
	} {
		if got := treeClash(c.paths); got != c.want {
			t.Errorf("treeClash(%q) = %q, want %q", c.paths, got, c.want)
		}
	}
}
