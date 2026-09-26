package templates

import (
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

func TestFixturesAreCanonical(t *testing.T) {
	for _, name := range []string{"paper-server.json", "fabric-modpack.json"} {
		t.Run(name, func(t *testing.T) {
			tp := fixture(t, name)
			file, err := MarshalFile(tp)
			if err != nil {
				t.Fatal(err)
			}
			if string(file) != string(readFile(t, name)) {
				t.Errorf("MarshalFile does not write the fixture back:\n%s", file)
			}
			roundTrip(t, tp)
		})
	}
}

func TestValidateRefuses(t *testing.T) {
	a := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct {
		name    string
		modpack bool // edit the Fabric modpack fixture instead of the Paper one
		edit    func(*Template)
		kind    Kind
		field   string
		problem string
	}{
		{name: "format 0", edit: func(tp *Template) { tp.Format = 0 }, field: "playkeeperTemplate", problem: "value"},
		{name: "newer format", edit: func(tp *Template) { tp.Format = 2 }, kind: KindNewer},
		{name: "no name", edit: func(tp *Template) { tp.Name = "" }, field: "name", problem: "missing"},
		{name: "blank name", edit: func(tp *Template) { tp.Name = "   " }, field: "name", problem: "missing"},
		{name: "long name", edit: func(tp *Template) { tp.Name = a(33) }, field: "name", problem: "too_long"},
		{name: "name on two lines", edit: func(tp *Template) { tp.Name = "Survival\nwith friends" }, field: "name", problem: "characters"},
		{name: "name that reads backwards", edit: func(tp *Template) { tp.Name = "Survival \u202esdneirf" }, field: "name", problem: "characters"},
		{name: "name with a colour code", edit: func(tp *Template) { tp.Name = "§aSurvival" }, field: "name", problem: "characters"},
		{name: "name with spaces around", edit: func(tp *Template) { tp.Name = " Survival" }, field: "name", problem: "spaces"},
		{name: "long description", edit: func(tp *Template) { tp.Description = a(281) }, field: "description", problem: "too_long"},
		{name: "other game", edit: func(tp *Template) { tp.Game = "minecraft-bedrock" }, field: "game", problem: "value"},
		{name: "long author", edit: func(tp *Template) { tp.Author = a(65) }, field: "author", problem: "too_long"},
		{name: "author on two lines", edit: func(tp *Template) { tp.Author = "siya\nadmin" }, field: "author", problem: "characters"},
		{name: "day in words", edit: func(tp *Template) { tp.Created = "25 Sep 2026" }, field: "created", problem: "value"},
		{name: "day with a time", edit: func(tp *Template) { tp.Created = "2026-09-25T18:04:00Z" }, field: "created", problem: "value"},
		{name: "day before templates", edit: func(tp *Template) { tp.Created = "2019-12-31" }, field: "created", problem: "value"},

		{name: "no type", edit: func(tp *Template) { tp.Server.Type = "" }, field: "server.type", problem: "value"},
		{name: "type in capitals", edit: func(tp *Template) { tp.Server.Type = "Paper" }, field: "server.type", problem: "value"},
		{name: "type with a path", edit: func(tp *Template) { tp.Server.Type = "paper/../purpur" }, field: "server.type", problem: "value"},
		{name: "version that is a word", edit: func(tp *Template) { tp.Server.MinecraftVersion = "latest" }, field: "server.minecraftVersion", problem: "value"},
		{name: "version with a space", edit: func(tp *Template) { tp.Server.MinecraftVersion = "26.2 " }, field: "server.minecraftVersion", problem: "value"},
		{name: "five build details", edit: func(tp *Template) {
			tp.Server.Build = map[string]string{"a": "1", "b": "1", "c": "1", "d": "1", "e": "1"}
		}, field: "server.build", problem: "too_many"},
		{name: "build detail name with a dash", edit: func(tp *Template) { tp.Server.Build = map[string]string{"paper-build": "129"} }, field: "server.build.paper-build", problem: "value"},
		{name: "build detail with a path", edit: func(tp *Template) { tp.Server.Build = map[string]string{"paperBuild": "../129"} }, field: "server.build.paperBuild", problem: "value"},
		{name: "empty build detail", edit: func(tp *Template) { tp.Server.Build = map[string]string{"paperBuild": ""} }, field: "server.build.paperBuild", problem: "value"},

		{name: "unknown difficulty", edit: func(tp *Template) { tp.Settings.Difficulty = "insane" }, field: "settings.difficulty", problem: "value"},
		{name: "unknown game mode", edit: func(tp *Template) { tp.Settings.GameMode = "god" }, field: "settings.gameMode", problem: "value"},
		{name: "view distance too short", edit: func(tp *Template) { tp.Settings.ViewDistance = 2 }, field: "settings.viewDistance", problem: "value"},
		{name: "view distance too far", edit: func(tp *Template) { tp.Settings.ViewDistance = 33 }, field: "settings.viewDistance", problem: "value"},
		{name: "unknown world type", edit: func(tp *Template) { tp.Settings.LevelType = "custom" }, field: "settings.levelType", problem: "value"},
		{name: "too many players", edit: func(tp *Template) { tp.Settings.MaxPlayers = 101 }, field: "settings.maxPlayers", problem: "value"},
		{name: "negative players", edit: func(tp *Template) { tp.Settings.MaxPlayers = -1 }, field: "settings.maxPlayers", problem: "value"},
		{name: "long server list message", edit: func(tp *Template) { tp.Settings.MOTD = a(60) }, field: "settings.motd", problem: "value"},
		{name: "server list message with a colour code", edit: func(tp *Template) { tp.Settings.MOTD = "§cRed alert" }, field: "settings.motd", problem: "value"},
		{name: "server list message on two lines", edit: func(tp *Template) { tp.Settings.MOTD = "Line one\nLine two" }, field: "settings.motd", problem: "value"},
		{name: "unknown play style", edit: func(tp *Template) { tp.Settings.PlayStyle = "pro" }, field: "settings.playStyle", problem: "value"},
		{name: "too little memory", edit: func(tp *Template) { tp.Settings.MemoryMB = 256 }, field: "settings.memoryMB", problem: "value"},
		{name: "too much memory", edit: func(tp *Template) { tp.Settings.MemoryMB = 65537 }, field: "settings.memoryMB", problem: "value"},

		{name: "251 add-ons", edit: func(tp *Template) { tp.Addons = manyAddons(251) }, field: "addons", problem: "too_many"},
		{name: "add-ons on Vanilla", edit: func(tp *Template) { tp.Server.Type = "vanilla" }, field: "addons", problem: "not_allowed"},
		{name: "add-on from another source", edit: func(tp *Template) { tp.Addons[0].Source = "curseforge" }, field: "addons[0].source", problem: "value"},
		{name: "Modrinth id with a slash", edit: func(tp *Template) { tp.Addons[0].Project = "fALz/amp" }, field: "addons[0].project", problem: "value"},
		{name: "Hangar id that is a name", edit: func(tp *Template) { tp.Addons[1].Project = "ViaVersion" }, field: "addons[1].project", problem: "value"},
		{name: "Hangar id with a leading zero", edit: func(tp *Template) { tp.Addons[1].Project = "031" }, field: "addons[1].project", problem: "value"},
		{name: "slug with a slash", edit: func(tp *Template) { tp.Addons[0].Slug = "chunky/x" }, field: "addons[0].slug", problem: "value"},
		{name: "add-on without a name", edit: func(tp *Template) { tp.Addons[0].Name = "" }, field: "addons[0].name", problem: "missing"},
		{name: "Hangar plugin on Fabric", edit: func(tp *Template) { tp.Server.Type = "fabric" }, field: "addons[1].source", problem: "not_allowed"},
		{name: "neither pinned nor newest", edit: func(tp *Template) { tp.Addons[0].Pin = nil }, field: "addons[0].pin", problem: "missing"},
		{name: "pinned and newest", edit: func(tp *Template) { tp.Addons[0].Latest = true }, field: "addons[0].latest", problem: "not_allowed"},
		{name: "version id with a dash", edit: func(tp *Template) { tp.Addons[0].Pin.VersionID = "MdY6-ATr" }, field: "addons[0].pin.versionId", problem: "value"},
		{name: "no version number", edit: func(tp *Template) { tp.Addons[0].Pin.VersionNumber = "" }, field: "addons[0].pin.versionNumber", problem: "missing"},
		{name: "unknown channel", edit: func(tp *Template) { tp.Addons[0].Pin.Channel = "nightly" }, field: "addons[0].pin.channel", problem: "value"},
		{name: "Modrinth add-on with a SHA-256", edit: func(tp *Template) { tp.Addons[0].Pin.HashAlgo = "sha256" }, field: "addons[0].pin.hashAlgo", problem: "value"},
		{name: "hash in capitals", edit: func(tp *Template) { tp.Addons[0].Pin.Hash = strings.ToUpper(tp.Addons[0].Pin.Hash) }, field: "addons[0].pin.hash", problem: "hash"},
		{name: "short hash", edit: func(tp *Template) { tp.Addons[0].Pin.Hash = tp.Addons[0].Pin.Hash[:64] }, field: "addons[0].pin.hash", problem: "hash"},
		{name: "add-on twice", edit: func(tp *Template) { tp.Addons = append(tp.Addons, tp.Addons[0]) }, field: "addons[3]", problem: "duplicate"},
		{name: "needed by itself", edit: func(tp *Template) { tp.Addons[1].DependencyOf = "31" }, field: "addons[1].dependencyOf", problem: "value"},
		{name: "needed by an add-on not listed", edit: func(tp *Template) { tp.Addons[1].DependencyOf = "999" }, field: "addons[1].dependencyOf", problem: "value"},
		{name: "needed by each other", edit: func(tp *Template) { tp.Addons[2].DependencyOf = "31" }, field: "addons", problem: "cycle"},

		{name: "modpack from Hangar", modpack: true, edit: func(tp *Template) { tp.Modpack.Source = addons.Hangar }, field: "modpack.source", problem: "value"},
		{name: "modpack id with a path", modpack: true, edit: func(tp *Template) { tp.Modpack.Project = "../x" }, field: "modpack.project", problem: "value"},
		{name: "modpack without a name", modpack: true, edit: func(tp *Template) { tp.Modpack.Name = "" }, field: "modpack.name", problem: "missing"},
		{name: "modpack on Paper", modpack: true, edit: func(tp *Template) { tp.Server.Type = "paper" }, field: "modpack", problem: "not_allowed"},
		{name: "modpack with a SHA-256", modpack: true, edit: func(tp *Template) { tp.Modpack.Pin.HashAlgo = "sha256" }, field: "modpack.pin.hashAlgo", problem: "value"},

		{name: "34 packs", edit: func(tp *Template) {
			tp.Packs = nil
			for i := range 34 {
				tp.Packs = append(tp.Packs, dataPack(i))
			}
		}, field: "packs", problem: "too_many"},
		{name: "pack without a name", edit: func(tp *Template) { tp.Packs[0].Name = "" }, field: "packs[0].name", problem: "value"},
		{name: "pack name with a slash", edit: func(tp *Template) { tp.Packs[0].Name = "a/b" }, field: "packs[0].name", problem: "value"},
		{name: "hidden pack name", edit: func(tp *Template) { tp.Packs[0].Name = ".hidden" }, field: "packs[0].name", problem: "value"},
		{name: "pack over plain HTTP", edit: func(tp *Template) { tp.Packs[0].URL = "http://cdn.modrinth.com/pack.zip" }, field: "packs[0].url", problem: "address"},
		{name: "pack on a home network", edit: func(tp *Template) { tp.Packs[0].URL = "https://192.168.1.20/pack.zip" }, field: "packs[0].url", problem: "address"},
		{name: "same pack twice", edit: func(tp *Template) { tp.Packs[1].URL = tp.Packs[0].URL }, field: "packs[1].url", problem: "duplicate"},
		{name: "SHA-1 that is not one", edit: func(tp *Template) { tp.Packs[0].SHA1 = "xyz" }, field: "packs[0]", problem: "hash"},
		{name: "resource pack without a SHA-1", edit: func(tp *Template) { tp.Packs[0].SHA1 = "" }, field: "packs[0].sha1", problem: "missing"},
		{name: "long prompt", edit: func(tp *Template) { tp.Packs[0].Prompt = a(121) }, field: "packs[0].prompt", problem: "too_long"},
		{name: "prompt on two lines", edit: func(tp *Template) { tp.Packs[0].Prompt = "Accept\nplease" }, field: "packs[0].prompt", problem: "characters"},
		{name: "data pack without a checksum", edit: func(tp *Template) { tp.Packs[1].SHA1, tp.Packs[1].SHA256 = "", "" }, field: "packs[1].sha256", problem: "missing"},
		{name: "required data pack", edit: func(tp *Template) { tp.Packs[1].Required = true }, field: "packs[1]", problem: "not_allowed"},
		{name: "data packs with one name", edit: func(tp *Template) {
			p := dataPack(1)
			p.Name = "terralith"
			tp.Packs = append(tp.Packs, p)
		}, field: "packs[2].name", problem: "duplicate"},
		{name: "two resource packs", edit: func(tp *Template) {
			p := tp.Packs[0]
			p.URL = "https://cdn.modrinth.com/data/xYz/stay-true.zip"
			tp.Packs = append(tp.Packs, p)
		}, field: "packs", problem: "too_many"},
		{name: "33 data packs", edit: func(tp *Template) {
			tp.Packs = nil
			for i := range 33 {
				tp.Packs = append(tp.Packs, dataPack(i))
			}
		}, field: "packs", problem: "too_many"},
		{name: "shader pack", edit: func(tp *Template) { tp.Packs[0].Kind = "shader" }, field: "packs[0].kind", problem: "value"},

		{name: "larger than a template can be", edit: func(tp *Template) {
			tp.Addons = manyAddons(250)
			for i := range tp.Addons {
				tp.Addons[i].Name = strings.Repeat("𝓜", maxLabel)
				tp.Addons[i].Pin.VersionNumber = strings.Repeat("𝓥", maxLabel)
			}
		}, kind: KindTooLarge},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tp := fixture(t, "paper-server.json")
			if c.modpack {
				tp = fixture(t, "fabric-modpack.json")
			}
			c.edit(tp)
			want := c.kind
			if want == "" {
				want = KindInvalid
			}
			e := refused(t, tp.Validate(), want)
			if e.Params["field"] != c.field || e.Params["problem"] != c.problem {
				t.Errorf("got field %q problem %q, want %q %q (%s)", e.Params["field"], e.Params["problem"], c.field, c.problem, e.Msg)
			}
			if _, err := NewLink(tp); kindOf(err) != want {
				t.Errorf("NewLink: got %v, want %s", err, want)
			}
			if _, err := MarshalFile(tp); kindOf(err) != want {
				t.Errorf("MarshalFile: got %v, want %s", err, want)
			}
		})
	}
	if kindOf((*Template)(nil).Validate()) != KindNotTemplate {
		t.Error("a nil template is valid")
	}
}

func TestValidateAccepts(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Template)
	}{
		{"no settings", func(tp *Template) { tp.Settings = Settings{} }},
		{"settings at their lowest", func(tp *Template) {
			tp.Settings.ViewDistance, tp.Settings.MaxPlayers, tp.Settings.MemoryMB = 3, 1, 512
		}},
		{"settings at their highest", func(tp *Template) {
			tp.Settings.ViewDistance, tp.Settings.MaxPlayers, tp.Settings.MemoryMB = 32, 100, 65536
			tp.Settings.MOTD = strings.Repeat("x", 59)
		}},
		{"centred server list message", func(tp *Template) { tp.Settings.MOTD = "      Survival with friends" }},
		{"every other choice", func(tp *Template) {
			tp.Settings = Settings{Difficulty: "peaceful", PVP: boolPtr(true), GameMode: "spectator", Hardcore: boolPtr(true),
				LevelType: "amplified", PlayStyle: "solo"}
		}},
		{"only the server", func(tp *Template) {
			tp.Description, tp.Settings, tp.Addons, tp.Packs = "", Settings{}, nil, nil
		}},
		{"newest versions", func(tp *Template) {
			for i := range tp.Addons {
				tp.Addons[i].Pin, tp.Addons[i].Latest = nil, true
			}
		}},
		{"type this Playkeeper does not know yet", func(tp *Template) { tp.Server.Type = "folia" }},
		{"snapshot", func(tp *Template) { tp.Server.MinecraftVersion, tp.Server.Build = "26.3-snapshot-2", nil }},
		{"letters from everywhere", func(tp *Template) {
			tp.Name = "Überleben ⛏️ mit Freunden"
			tp.Description = "Ein Server für Freunde — 友達と遊ぶ"
		}},
		{"characters HTML treats specially", func(tp *Template) {
			tp.Name = `<b>Survival</b> & "co"`
			tp.Addons[0].Name = `Chunky \ Pregen`
		}},
		{"pack on port 443", func(tp *Template) { tp.Packs[0].URL = "https://cdn.modrinth.com:443/data/50dA9Sha/pack.zip" }},
		{"data pack with a SHA-1 only", func(tp *Template) { tp.Packs[1].SHA256 = "" }},
		{"a resource pack and 32 data packs", func(tp *Template) {
			tp.Packs = tp.Packs[:1]
			for i := range MaxDataPacks {
				tp.Packs = append(tp.Packs, dataPack(i))
			}
		}},
		{"250 add-ons", func(tp *Template) { tp.Addons = manyAddons(MaxAddons) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tp := fixture(t, "paper-server.json")
			c.edit(tp)
			if err := tp.Validate(); err != nil {
				t.Fatal(err)
			}
			roundTrip(t, tp)
		})
	}
}

func TestMarshalFileWithoutIndentWhenLarge(t *testing.T) {
	tp := fixture(t, "paper-server.json")
	tp.Addons = manyAddons(MaxAddons)
	for i := range tp.Addons {
		tp.Addons[i].Name += " " + strings.Repeat("n", 55)
		tp.Addons[i].Pin.VersionNumber += "+" + strings.Repeat("v", 55)
	}
	js, err := canonicalJSON(tp)
	if err != nil {
		t.Fatal(err)
	}
	file, err := MarshalFile(tp)
	if err != nil {
		t.Fatal(err)
	}
	if len(js) > MaxFileSize || string(file) != string(js) {
		t.Fatalf("got a %d byte file from %d bytes of JSON, want the JSON without indentation", len(file), len(js))
	}
	roundTrip(t, tp)
}

func TestPublicURL(t *testing.T) {
	cases := map[string]bool{
		"https://cdn.modrinth.com/data/50dA9Sha/versions/RGIzA5em/FreshAnimations_v1.10.5.zip": true,
		"https://github.com/owner/repo/releases/download/v1.0/pack.zip":                        true,
		"https://cdn.modrinth.com:443/pack.zip":                                                true,
		"https://example.co.uk/my%20pack.zip":                                                  true,
		"https://xn--bcher-kva.example.com/pack.zip":                                           true,

		"":                                                        false,
		"http://cdn.modrinth.com/pack.zip":                        false,
		"ftp://cdn.modrinth.com/pack.zip":                         false,
		"//cdn.modrinth.com/pack.zip":                             false,
		"https:cdn.modrinth.com/pack.zip":                         false,
		"https:///pack.zip":                                       false,
		"https://cdn.modrinth.com/pack.zip?token=abc":             false,
		"https://cdn.modrinth.com/pack.zip#part":                  false,
		"https://user:secret@cdn.modrinth.com/p.zip":              false,
		"https://cdn.modrinth.com:8443/pack.zip":                  false,
		"https://203.0.113.7/pack.zip":                            false,
		"https://[2001:db8::1]/pack.zip":                          false,
		"https://127.1/pack.zip":                                  false,
		"https://2130706433/pack.zip":                             false,
		"https://localhost/pack.zip":                              false,
		"https://files.localhost/pack.zip":                        false,
		"https://nas.local/pack.zip":                              false,
		"https://files.internal/pack.zip":                         false,
		"https://router.lan/pack.zip":                             false,
		"https://intranet/pack.zip":                               false,
		"https://hidden.onion/pack.zip":                           false,
		"https://-bad-.com/pack.zip":                              false,
		"https://bücher.example.com/pack.zip":                     false,
		"https://cdn.modrinth.com/my pack.zip":                    false,
		"https://cdn.modrinth.com/pack.zip\n":                     false,
		"javascript:alert(1)":                                     false,
		"https://cdn.modrinth.com/" + strings.Repeat("a", maxURL): false,
	}
	for u, want := range cases {
		if got := publicURL(u); got != want {
			t.Errorf("publicURL(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestFileName(t *testing.T) {
	cases := map[string]string{
		"Survival with friends":   "survival-with-friends",
		"  Über Server!! #2 ":     "über-server-2",
		"../../etc/passwd":        "etc-passwd",
		"":                        "server",
		"⛏️⛏️":                    "server",
		strings.Repeat("ab ", 30): strings.Repeat("ab-", 13) + "a",
	}
	for name, want := range cases {
		if got := FileName(&Template{Name: name}); got != want+FileExtension {
			t.Errorf("FileName(%q) = %q, want %q", name, got, want+FileExtension)
		}
	}
}
