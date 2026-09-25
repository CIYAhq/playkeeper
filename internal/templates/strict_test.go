package templates

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseFileRefuses(t *testing.T) {
	base := string(readFile(t, "paper-server.json"))
	edit := func(old, new string) string {
		t.Helper()
		if !strings.Contains(base, old) {
			t.Fatalf("the fixture has no %q", old)
		}
		return strings.Replace(base, old, new, 1)
	}
	const desc = `"description": "Paper with Chunky, and ViaVersion so friends on older clients can join. Terralith world generation."`
	var buildMembers, addonItems []string
	for i := range maxMembers + 1 {
		buildMembers = append(buildMembers, `"k`+strings.Repeat("x", i)+`": "1"`)
	}
	for range maxItems + 44 {
		addonItems = append(addonItems, `{"source": "modrinth"}`)
	}
	cases := []struct {
		name    string
		data    string
		kind    Kind
		field   string
		problem string
	}{
		{name: "text", data: "Survival with friends", kind: KindNotTemplate},
		{name: "nothing", data: "", kind: KindNotTemplate},
		{name: "other JSON", data: `{"name": "Survival with friends"}`, kind: KindNotTemplate},
		{name: "a list", data: `[1, 2, 3]`, kind: KindNotTemplate},
		{name: "cut off", data: base[:len(base)/2], kind: KindFileDamaged},
		{name: "not UTF-8", data: edit(`"Chunky"`, "\"Chunk\xffy\""), kind: KindFileDamaged},
		{name: "something after it", data: base + "{}", kind: KindFileDamaged},
		{name: "nested past any template", data: `{"playkeeperTemplate": 1, "x": ` + strings.Repeat("[", 20000) + strings.Repeat("]", 20000) + `}`, kind: KindFileDamaged},
		{name: "newer format with fields this one lacks", data: edit(`"playkeeperTemplate": 1,`, `"playkeeperTemplate": 2, "worldBorder": 5000,`), kind: KindNewer},
		{name: "format as text", data: edit(`"playkeeperTemplate": 1,`, `"playkeeperTemplate": "1",`), kind: KindInvalid, field: "playkeeperTemplate", problem: "value"},
		{name: "format 0", data: edit(`"playkeeperTemplate": 1,`, `"playkeeperTemplate": 0,`), kind: KindInvalid, field: "playkeeperTemplate", problem: "value"},

		{name: "unknown field", data: edit(`"game":`, `"author": "siya", "game":`), kind: KindUnknownField, field: "author"},
		{name: "unknown server detail", data: edit(`"type": "paper",`, `"type": "paper", "jar": "paper-26.2-129.jar",`), kind: KindUnknownField, field: "server.jar"},
		{name: "unknown setting", data: edit(`"difficulty": "normal",`, `"difficulty": "normal", "spawnProtection": 16,`), kind: KindUnknownField, field: "settings.spawnProtection"},
		{name: "field in capitals", data: edit(`"name": "Survival with friends"`, `"Name": "Survival with friends"`), kind: KindUnknownField, field: "Name"},

		{name: "online mode", data: edit(`"pvp": false,`, `"pvp": false, "onlineMode": false,`), kind: KindForbiddenField, field: "settings.onlineMode"},
		{name: "online-mode as server.properties has it", data: edit(`"pvp": false,`, `"pvp": false, "online-mode": false,`), kind: KindForbiddenField, field: "settings.online-mode"},
		{name: "allowlist", data: edit(`"pvp": false,`, `"pvp": false, "whitelist": ["siya"],`), kind: KindForbiddenField, field: "settings.whitelist"},
		{name: "operators", data: edit(`"game":`, `"ops": [{"name": "siya", "level": 4}], "game":`), kind: KindForbiddenField, field: "ops"},
		{name: "RCON password", data: edit(`"pvp": false,`, `"pvp": false, "rconPassword": "hunter2",`), kind: KindForbiddenField, field: "settings.rconPassword"},
		{name: "port", data: edit(`"type": "paper",`, `"type": "paper", "port": 25565,`), kind: KindForbiddenField, field: "server.port"},
		{name: "query port", data: edit(`"pvp": false,`, `"pvp": false, "query.port": 25565,`), kind: KindForbiddenField, field: "settings.query.port"},
		{name: "seed", data: edit(`"pvp": false,`, `"pvp": false, "levelSeed": "-4172144997902289642",`), kind: KindForbiddenField, field: "settings.levelSeed"},
		{name: "world", data: edit(`"game":`, `"world": {"url": "https://example.org/world.zip"}, "game":`), kind: KindForbiddenField, field: "world"},
		{name: "console commands", data: edit(`"game":`, `"startupCommands": ["op siya"], "game":`), kind: KindForbiddenField, field: "startupCommands"},
		{name: "download token", data: edit(`"slug": "chunky",`, `"slug": "chunky", "downloadToken": "mrp_abc",`), kind: KindForbiddenField, field: "addons[0].downloadToken"},
		{name: "webhook", data: edit(`"game":`, `"discordWebhook": "https://discord.com/api/webhooks/1/abc", "game":`), kind: KindForbiddenField, field: "discordWebhook"},
		{name: "key among build details", data: edit(`"paperBuild": "129"`, `"paperBuild": "129", "apiKey": "abc"`), kind: KindForbiddenField, field: "server.build.apiKey"},

		{name: "name twice", data: edit(`"name": "Survival with friends",`, `"name": "Survival with friends", "name": "Something else",`), kind: KindInvalid, field: "name", problem: "duplicate"},
		{name: "build detail twice", data: edit(`"paperBuild": "129"`, `"paperBuild": "129", "paperBuild": "130"`), kind: KindInvalid, field: "server.build.paperBuild", problem: "duplicate"},
		{name: "null description", data: edit(desc, `"description": null`), kind: KindInvalid, field: "description", problem: "null"},
		{name: "null modpack", data: edit(`"packs": [`, `"modpack": null, "packs": [`), kind: KindInvalid, field: "modpack", problem: "null"},
		{name: "number as text", data: edit(`"viewDistance": 10,`, `"viewDistance": "10",`), kind: KindInvalid, field: "settings.viewDistance", problem: "type"},
		{name: "yes and no as text", data: edit(`"pvp": false,`, `"pvp": "no",`), kind: KindInvalid, field: "settings.pvp", problem: "type"},
		{name: "fraction", data: edit(`"maxPlayers": 10,`, `"maxPlayers": 10.5,`), kind: KindInvalid, field: "settings.maxPlayers", problem: "number"},
		{name: "exponent", data: edit(`"maxPlayers": 10,`, `"maxPlayers": 1e1,`), kind: KindInvalid, field: "settings.maxPlayers", problem: "number"},
		{name: "huge number", data: edit(`"memoryMB": 4096`, `"memoryMB": 99999999999`), kind: KindInvalid, field: "settings.memoryMB", problem: "number"},
		{name: "object for a list", data: edit(`"packs": [`, `"packs": {}, "x": [`), kind: KindInvalid, field: "packs", problem: "type"},
		{name: "list for text", data: edit(`"game": "minecraft-java"`, `"game": ["minecraft-java"]`), kind: KindInvalid, field: "game", problem: "type"},
		{name: "long text", data: edit(desc, `"description": "`+strings.Repeat("a", maxString+1)+`"`), kind: KindInvalid, field: "description", problem: "too_long"},
		{name: "too many add-ons to read", data: edit(`"addons": [`, `"addons": [`+strings.Join(addonItems, ", ")+`,`), kind: KindInvalid, field: "addons", problem: "too_many"},
		{name: "too many build details to read", data: edit(`"paperBuild": "129"`, strings.Join(buildMembers, ", ")), kind: KindInvalid, field: "server.build", problem: "too_many"},
		{name: "too large", data: base + strings.Repeat(" ", MaxFileSize), kind: KindTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseFile([]byte(c.data))
			e := refused(t, err, c.kind)
			if e.Params["field"] != c.field || e.Params["problem"] != c.problem {
				t.Errorf("got field %q problem %q, want %q %q (%s)", e.Params["field"], e.Params["problem"], c.field, c.problem, e.Msg)
			}
			if !strings.HasPrefix(c.data, "{") {
				return
			}
			if _, err := Decode([]byte(c.data)); kindOf(err) != c.kind {
				t.Errorf("Decode: got %v, want %s", err, c.kind)
			}
		})
	}
}

func TestParseFileAcceptsHandEdits(t *testing.T) {
	data := string(readFile(t, "paper-server.json"))
	data = "\ufeff" + strings.ReplaceAll(data, "\n", "\r\n")
	data = strings.Replace(data, `"name": "Survival with friends"`, `"name": "  Survival with friends "`, 1)
	data = strings.Replace(data, "43ffecc6e6a734b7", "43FFECC6E6A734B7", 1)
	data = strings.Replace(data, "0d069c5583d1dd45", "0D069C5583D1DD45", 1)
	tp, err := ParseFile([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	sameTemplate(t, tp, fixture(t, "paper-server.json"))
	if got, err := Decode([]byte(data)); err != nil {
		t.Errorf("Decode: %v", err)
	} else {
		sameTemplate(t, got, tp)
	}
}

func TestForbiddenKeys(t *testing.T) {
	for _, k := range []string{"whitelist", "white-list", "enforce-whitelist", "online-mode", "ONLINE_MODE", "op", "ops", "server-ip", "server-port",
		"rcon.port", "rcon.password", "level-seed", "levelName", "world", "bannedPlayers", "usercache", "apiKey", "api_key", "authToken",
		"privateKey", "webhookURL", "adminPassword", "clientSecret", "credentials", "functionCommands"} {
		if !forbidden(k) {
			t.Errorf("%q is not forbidden", k)
		}
	}
	for _, k := range []string{"name", "description", "game", "server", "type", "minecraftVersion", "build", "paperBuild", "settings", "difficulty",
		"pvp", "gameMode", "hardcore", "viewDistance", "levelType", "maxPlayers", "motd", "playStyle", "memoryMB", "addons", "source", "project",
		"slug", "pin", "versionId", "versionNumber", "channel", "hashAlgo", "hash", "latest", "dependencyOf", "modpack", "packs", "kind",
		"url", "sha1", "sha256", "required", "prompt", "playkeeperTemplate"} {
		if forbidden(k) {
			t.Errorf("the template's own field %q is forbidden", k)
		}
	}
}

func TestWalkerStopsAtDepth(t *testing.T) {
	dec := json.NewDecoder(strings.NewReader(`[[[[[["deep"]]]]]]`))
	dec.UseNumber()
	e := walker{dec}.value(reflect.TypeFor[[][][][][][]string](), "x", 0)
	if e == nil || e.Params["problem"] != "depth" {
		t.Fatalf("got %v, want a refusal for depth", e)
	}
}
