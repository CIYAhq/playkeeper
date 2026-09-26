package templates

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
)

// paperSetup is the server the Paper fixture was exported from, as
// Playkeeper records it.
func paperSetup(t *testing.T) Setup {
	t.Helper()
	return Setup{
		Name:             "Survival with friends",
		Description:      "Paper with Chunky, and ViaVersion so friends on older clients can join. Terralith world generation.",
		Type:             "paper",
		MinecraftVersion: "26.2",
		Build:            map[string]string{"paperBuild": "129"},
		Settings: Settings{Difficulty: "normal", PVP: boolPtr(false), GameMode: "survival", ViewDistance: 10, MaxPlayers: 10,
			MOTD: "§aSurvival with friends §7— be nice!", PlayStyle: "friends", MemoryMB: 4096},
		Addons: installedRows(t),
		Packs: []PackSetup{
			{Pack: Pack{Kind: ResourcePack, Name: "Fresh Animations", URL: "https://cdn.modrinth.com/data/50dA9Sha/versions/RGIzA5em/FreshAnimations_v1.10.5.zip",
				SHA1: "0d069c5583d1dd4591e55ec5ff6dd0905f3f8615", Prompt: "Smoother mob animations for everyone."}},
			{Pack: Pack{Kind: DataPack, Name: "Terralith", URL: "https://cdn.modrinth.com/data/8oi3bsk5/versions/CzijfXJQ/Terralith_26.2_v2.6.4.zip",
				SHA1: "96ccd25be9ba5240ebe8150cc29240aca781f0e1", SHA256: "5ac86ed13cc9fc617cbb57e14ce02b1ecac2f2776c36f0536a519378e546e23e"}},
		},
		OwnHosts: []string{"https://survival.example.net:8443"},
	}
}

func modrinthRow(project, slug, name, version string) addons.Installed {
	return addons.Installed{Source: addons.Modrinth, ProjectID: project, Slug: slug, Name: name, VersionID: version, VersionNumber: "1.0.0",
		Channel: "release", FileName: slug + "-1.0.0.jar", HashAlgo: "sha512", Hash: digest("sha512", project), Size: 100_000}
}

// fabricSetup is the server the Fabric modpack fixture was exported from:
// the modpack installed Lithium and FerriteCore, and Chunky was added.
func fabricSetup() Setup {
	return Setup{
		Name:             "Fast Fabric SMP",
		Description:      "The Adrenaserver performance pack, plus Chunky to pregenerate the world.",
		Type:             "fabric",
		MinecraftVersion: "1.21.1",
		Settings:         Settings{Difficulty: "hard", MaxPlayers: 20, MemoryMB: 6144},
		Addons: []addons.Installed{
			modrinthRow("gvQqBUqZ", "lithium", "Lithium", "Lz5nMrMw"),
			modrinthRow("uXXizFIs", "ferrite-core", "FerriteCore", "wmIZ4wP4"),
			modrinthRow("fALzjamp", "chunky", "Chunky", "RDDLpNhp"),
		},
		Modpack: &ModpackSetup{
			Modpack: Modpack{Source: addons.Modrinth, Project: "H9OFWiay", Slug: "adrenaserver", Name: "Adrenaserver",
				Pin: Pin{VersionID: "7U9BhPKK", VersionNumber: "1.7.0+1.21.1.fabric", Channel: "release", HashAlgo: "sha512",
					Hash: "7872791b236bee9897c67e391c5f0a13a4588784927656d60bd0edba89aa3a56fcb00c22cf4d2bc4dd26c71a642a57d6c8d6d285875e9faaf89d555ce64d3020"}},
			Includes: []addons.Key{{Source: addons.Modrinth, ProjectID: "gvQqBUqZ"}, {Source: addons.Modrinth, ProjectID: "uXXizFIs"}},
		},
	}
}

// managed is the folder scan of files Playkeeper installed, unchanged.
func managed(rows []addons.Installed) []addons.ScanEntry {
	var es []addons.ScanEntry
	for i := range rows {
		es = append(es, addons.ScanEntry{FileName: rows[i].FileName, Size: rows[i].Size, Status: addons.FileManaged, Installed: &rows[i]})
	}
	return es
}

func addonNames(tp *Template) []string {
	var names []string
	for _, a := range tp.Addons {
		names = append(names, a.Name)
	}
	return names
}

func TestExportPaperServer(t *testing.T) {
	tp, rep, err := Export(paperSetup(t), ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sameTemplate(t, tp, fixture(t, "paper-server.json"))
	wantKinds(t, "left out", rep.LeftOut, KindLeftOutFormatting)
	if wantKinds(t, "notes", rep.Notes, KindNoteWorld, KindNotePlayers, KindNoteAddonConfig) {
		if n := rep.Notes[2]; n.Params["kind"] != "plugin" || !strings.HasPrefix(n.Msg, "Plugin settings stay on this server") {
			t.Errorf("got %+v, want a note about plugin settings", n)
		}
	}
	roundTrip(t, tp)

	file, err := MarshalFile(tp)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"survival.example.net", ".jar", "hangarcdn", "iconUrl", "installed", "304616", "§"} {
		if strings.Contains(string(file), private) {
			t.Errorf("the template carries %q", private)
		}
	}
	js, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(js), `{"leftOut":[{"kind":"left_out_formatting"`) {
		t.Errorf("the report's JSON is %s", js)
	}
}

// A template says who made it and on which day, when asked to, and a
// template without either still reads.
func TestExportNamesItsAuthorAndDay(t *testing.T) {
	made := time.Date(2026, 9, 25, 23, 30, 0, 0, time.FixedZone("CEST", 2*60*60))
	tp, _, err := Export(paperSetup(t), ExportOptions{Author: " §asiya\n", Created: made})
	if err != nil {
		t.Fatal(err)
	}
	if tp.Author != "siya" || tp.Created != "2026-09-25" {
		t.Fatalf("author %q, made %q: want the name as plain text and the day in UTC", tp.Author, tp.Created)
	}
	roundTrip(t, tp)
	file, err := MarshalFile(tp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(file), `"author": "siya",`) || !strings.Contains(string(file), `"created": "2026-09-25",`) {
		t.Fatalf("the file says who and when after its name: %s", file)
	}
	back, err := DecodeLink(mustLink(t, tp).URL)
	if err != nil || back.Author != "siya" || back.Created != "2026-09-25" {
		t.Fatalf("the link carries who and when: %+v %v", back, err)
	}

	old := fixture(t, "paper-server.json")
	if old.Author != "" || old.Created != "" {
		t.Fatalf("the fixture from before authors and days: %+v", old)
	}
	if err := old.Validate(); err != nil {
		t.Fatalf("a template without an author or day is still valid: %v", err)
	}
	tp, _, err = Export(paperSetup(t), ExportOptions{})
	if err != nil || tp.Author != "" || tp.Created != "" {
		t.Fatalf("an export not asked to name them leaves both out: %+v %v", tp, err)
	}
}

func TestExportFabricModpack(t *testing.T) {
	tp, rep, err := Export(fabricSetup(), ExportOptions{Latest: true})
	if err != nil {
		t.Fatal(err)
	}
	sameTemplate(t, tp, fixture(t, "fabric-modpack.json"))
	wantKinds(t, "left out", rep.LeftOut)
	if wantKinds(t, "notes", rep.Notes, KindNoteWorld, KindNotePlayers, KindNoteAddonConfig, KindNoteLatest, KindNoteModpackAddons) {
		if n := rep.Notes[2]; n.Params["kind"] != "mod" {
			t.Errorf("got %+v, want a note about mod settings", n)
		}
		if n := rep.Notes[4]; n.Params["count"] != "2" || n.Params["name"] != "Adrenaserver" {
			t.Errorf("got %+v, want 2 mods travelling with Adrenaserver", n)
		}
	}
	roundTrip(t, tp)
}

func TestExportLeavesOut(t *testing.T) {
	basic := []Kind{KindNoteWorld, KindNotePlayers, KindNoteAddonConfig}
	cases := []struct {
		name    string
		fabric  bool
		edit    func(*Setup)
		opts    ExportOptions
		leftOut []Kind
		notes   []Kind // basic when nil
		check   func(*testing.T, *Template, *Report)
	}{
		{name: "add-on from a source templates do not carry", edit: func(s *Setup) {
			s.Addons = append(s.Addons, addons.Installed{Source: "spigotmc", ProjectID: "81534", Slug: "chunkyborder", Name: "ChunkyBorder",
				VersionID: "561228", VersionNumber: "1.2.23", HashAlgo: "sha256", Hash: digest("sha256", "chunkyborder")})
		}, leftOut: []Kind{KindLeftOutAddon}, check: func(t *testing.T, tp *Template, r *Report) {
			if len(tp.Addons) != 3 || r.LeftOut[0].Params["name"] != "ChunkyBorder" || r.LeftOut[0].Params["source"] != "spigotmc" {
				t.Errorf("got %v and %+v", addonNames(tp), r.LeftOut[0])
			}
		}},
		{name: "Hangar plugins on a Fabric server", edit: func(s *Setup) {
			s.Type, s.MinecraftVersion, s.Build = "fabric", "1.21.1", nil
		}, leftOut: []Kind{KindLeftOutAddon, KindLeftOutAddon}, check: func(t *testing.T, tp *Template, r *Report) {
			if fmt.Sprint(addonNames(tp)) != "[Chunky]" || r.LeftOut[0].Params["name"] != "ViaBackwards" || r.LeftOut[1].Params["name"] != "ViaVersion" {
				t.Errorf("got %v and %v", addonNames(tp), r.LeftOut)
			}
		}},
		{name: "record without a hash", edit: func(s *Setup) { s.Addons[0].Hash = "" },
			leftOut: []Kind{KindLeftOutAddon}, check: func(t *testing.T, tp *Template, r *Report) {
				if fmt.Sprint(addonNames(tp)) != "[ViaVersion ViaBackwards]" || r.LeftOut[0].Params["name"] != "Chunky" || r.LeftOut[0].Hint == "" {
					t.Errorf("got %v and %+v", addonNames(tp), r.LeftOut[0])
				}
			}},
		{name: "file added by hand", edit: func(s *Setup) {
			s.Folder = &addons.ScanResult{Folder: "plugins", Entries: append(managed(s.Addons), addons.ScanEntry{
				FileName: "EssentialsX-2.21.2.jar", Size: 1_234_567, Status: addons.FileUnknown,
				Meta: addons.JarMeta{ID: "Essentials", Name: "Essentials", Version: "2.21.2", Kind: "plugin"}})}
		}, leftOut: []Kind{KindLeftOutUpload}, check: func(t *testing.T, tp *Template, r *Report) {
			if len(tp.Addons) != 3 || r.LeftOut[0].Params["name"] != "Essentials" || r.LeftOut[0].Params["file"] != "EssentialsX-2.21.2.jar" {
				t.Errorf("got %v and %+v", addonNames(tp), r.LeftOut[0])
			}
		}},
		{name: "file no longer there", edit: func(s *Setup) {
			s.Folder = &addons.ScanResult{Folder: "plugins", Entries: managed(s.Addons[:2]), Missing: s.Addons[2:]}
		}, leftOut: []Kind{KindLeftOutMissing}, check: func(t *testing.T, tp *Template, r *Report) {
			if fmt.Sprint(addonNames(tp)) != "[Chunky ViaBackwards]" || !strings.Contains(r.LeftOut[0].Msg, "ViaVersion was left out: its file is no longer in the plugins folder.") {
				t.Errorf("got %v and %+v", addonNames(tp), r.LeftOut[0])
			}
		}},
		{name: "changed file, and one Modrinth knows", edit: func(s *Setup) {
			luckPerms := modrinthRow("Vebnzrzj", "luckperms", "LuckPerms", "OrIs0S6b")
			entries := managed(s.Addons)
			entries[0].Status = addons.FileModified
			s.Folder = &addons.ScanResult{Folder: "plugins", Entries: append(entries, addons.ScanEntry{
				FileName: "LuckPerms-Bukkit-5.5.17.jar", Status: addons.FileIdentified, Identified: &luckPerms})}
		}, notes: append(basic, KindNoteChanged, KindNoteIdentified), check: func(t *testing.T, tp *Template, r *Report) {
			if fmt.Sprint(addonNames(tp)) != "[Chunky LuckPerms ViaVersion ViaBackwards]" || r.Notes[3].Params["name"] != "Chunky" || r.Notes[4].Params["name"] != "LuckPerms" {
				t.Errorf("got %v and %v", addonNames(tp), r.Notes)
			}
		}},
		{name: "more add-ons than a template holds", edit: func(s *Setup) {
			for i := range MaxAddons + 7 {
				s.Addons = append(s.Addons, modrinthRow(fmt.Sprintf("M%07d", i), fmt.Sprintf("mod-%d", i), fmt.Sprintf("Mod %d", i), fmt.Sprintf("V%07d", i)))
			}
		}, leftOut: []Kind{KindLeftOutAddonsLimit}, check: func(t *testing.T, tp *Template, r *Report) {
			if len(tp.Addons) != MaxAddons || r.LeftOut[0].Params["count"] != "10" {
				t.Errorf("got %d add-ons and %+v", len(tp.Addons), r.LeftOut[0])
			}
		}},
		{name: "settings out of range", edit: func(s *Setup) { s.Settings.ViewDistance, s.Settings.MaxPlayers = 64, 500 },
			leftOut: []Kind{KindLeftOutSetting, KindLeftOutSetting}, check: func(t *testing.T, tp *Template, r *Report) {
				st := tp.Settings
				if r.LeftOut[0].Params["setting"] != "viewDistance" || r.LeftOut[1].Params["setting"] != "maxPlayers" || st.ViewDistance != 0 || st.MaxPlayers != 0 || st.Difficulty != "normal" {
					t.Errorf("got %+v and %v", st, r.LeftOut)
				}
			}},
		{name: "server list message too long", edit: func(s *Setup) { s.Settings.MOTD = "§6" + strings.Repeat("Welcome ", 9) },
			leftOut: []Kind{KindLeftOutSetting}, check: func(t *testing.T, tp *Template, r *Report) {
				if tp.Settings.MOTD != "" || r.LeftOut[0].Params["setting"] != "motd" {
					t.Errorf("got %q and %+v", tp.Settings.MOTD, r.LeftOut[0])
				}
			}},
		{name: "coloured server list message on two lines", edit: func(s *Setup) { s.Settings.MOTD = "§6§lSurvival\n§7with friends" },
			leftOut: []Kind{KindLeftOutFormatting}, check: func(t *testing.T, tp *Template, r *Report) {
				if tp.Settings.MOTD != "Survival with friends" {
					t.Errorf("got %q", tp.Settings.MOTD)
				}
			}},
		{name: "build details a template cannot carry", edit: func(s *Setup) { s.Build["jarPath"] = "/opt/paper/paper.jar" },
			leftOut: []Kind{KindLeftOutBuild}, check: func(t *testing.T, tp *Template, r *Report) {
				if !maps.Equal(tp.Server.Build, map[string]string{"paperBuild": "129"}) {
					t.Errorf("got %v", tp.Server.Build)
				}
			}},
		{name: "server icon", edit: func(s *Setup) { s.HasIcon = true }, leftOut: []Kind{KindLeftOutIcon}},
		{name: "uploaded pack", edit: func(s *Setup) { s.Packs[0].Uploaded = true },
			leftOut: []Kind{KindLeftOutPackUpload}, check: func(t *testing.T, tp *Template, r *Report) {
				if len(tp.Packs) != 1 || tp.Packs[0].Name != "Terralith" || r.LeftOut[0].Params["name"] != "Fresh Animations" {
					t.Errorf("got %+v and %v", tp.Packs, r.LeftOut)
				}
			}},
		{name: "pack this dashboard serves", edit: func(s *Setup) {
			s.Packs[0].URL = "https://survival.example.net/resource-packs/0d069c5583d1dd4591e55ec5ff6dd0905f3f8615.zip"
		}, leftOut: []Kind{KindLeftOutPackAddress}},
		{name: "pack on a home network", edit: func(s *Setup) { s.Packs[0].URL = "https://192.168.1.20/fresh.zip" },
			leftOut: []Kind{KindLeftOutPackAddress}},
		{name: "pack address with a key", edit: func(s *Setup) { s.Packs[1].URL = "https://files.example.org/terralith.zip?key=hunter2" },
			leftOut: []Kind{KindLeftOutPackAddress}, check: func(t *testing.T, tp *Template, r *Report) {
				if js, _ := json.Marshal(r); strings.Contains(string(js), "hunter2") {
					t.Errorf("the report repeats the key: %s", js)
				}
			}},
		{name: "pack without a checksum", edit: func(s *Setup) { s.Packs[1].SHA1, s.Packs[1].SHA256 = "", "" },
			leftOut: []Kind{KindLeftOutPackHash}},
		{name: "resource pack with a SHA-256 only", edit: func(s *Setup) { s.Packs[0].SHA1, s.Packs[0].SHA256 = "", digest("sha256", "fresh") },
			leftOut: []Kind{KindLeftOutPackHash}},
		{name: "second resource pack", edit: func(s *Setup) {
			s.Packs = append(s.Packs, PackSetup{Pack: Pack{Kind: ResourcePack, Name: "Stay True", URL: "https://cdn.modrinth.com/data/xYz/stay-true.zip", SHA1: digest("sha1", "stay")}})
		}, leftOut: []Kind{KindLeftOutPacksLimit}},
		{name: "33 data packs", edit: func(s *Setup) {
			for i := range MaxDataPacks {
				s.Packs = append(s.Packs, PackSetup{Pack: dataPack(i)})
			}
		}, leftOut: []Kind{KindLeftOutPacksLimit}, check: func(t *testing.T, tp *Template, r *Report) {
			if len(tp.Packs) != 1+MaxDataPacks {
				t.Errorf("got %d packs", len(tp.Packs))
			}
		}},
		{name: "data packs with one name", edit: func(s *Setup) {
			p := dataPack(1)
			p.Name = "terralith.zip"
			s.Packs = append(s.Packs, PackSetup{Pack: p})
		}, leftOut: []Kind{KindLeftOutPacksLimit}},
		{name: "one pack twice", edit: func(s *Setup) { s.Packs = append(s.Packs, s.Packs[1]) },
			check: func(t *testing.T, tp *Template, r *Report) {
				if len(tp.Packs) != 2 {
					t.Errorf("got %d packs", len(tp.Packs))
				}
			}},
		{name: "names that need tidying", edit: func(s *Setup) {
			s.Name, s.Description = "§6§lSurvival\u202e with   friends\n", "Paper with Chunky.\n\nJoin us!"
		}, check: func(t *testing.T, tp *Template, r *Report) {
			if tp.Name != "Survival with friends" || tp.Description != "Paper with Chunky. Join us!" {
				t.Errorf("got %q and %q", tp.Name, tp.Description)
			}
		}},
		{name: "long name", edit: func(s *Setup) { s.Name = "Survival with friends and family and everyone else" },
			check: func(t *testing.T, tp *Template, r *Report) {
				if tp.Name != "Survival with friends and family" {
					t.Errorf("got %q", tp.Name)
				}
			}},
		{name: "no name", edit: func(s *Setup) { s.Name = "" }, check: func(t *testing.T, tp *Template, r *Report) {
			if tp.Name != "My server" {
				t.Errorf("got %q", tp.Name)
			}
		}},
		{name: "server made before 0.3.0", edit: func(s *Setup) { s.Type = "" }, check: func(t *testing.T, tp *Template, r *Report) {
			if tp.Server.Type != "paper" {
				t.Errorf("got %q", tp.Server.Type)
			}
		}},
		{name: "without add-ons", opts: ExportOptions{WithoutAddons: true},
			notes: []Kind{KindNoteWorld, KindNotePlayers}, check: func(t *testing.T, tp *Template, r *Report) {
				if tp.Addons != nil {
					t.Errorf("got %v", addonNames(tp))
				}
			}},
		{name: "without settings", opts: ExportOptions{WithoutSettings: true}, check: func(t *testing.T, tp *Template, r *Report) {
			if tp.Settings != (Settings{}) {
				t.Errorf("got %+v", tp.Settings)
			}
		}},
		{name: "without packs", opts: ExportOptions{WithoutPacks: true}, check: func(t *testing.T, tp *Template, r *Report) {
			if tp.Packs != nil {
				t.Errorf("got %+v", tp.Packs)
			}
		}},
		{name: "newest versions", opts: ExportOptions{Latest: true},
			notes: append(basic, KindNoteLatest), check: func(t *testing.T, tp *Template, r *Report) {
				for _, a := range tp.Addons {
					if !a.Latest || a.Pin != nil {
						t.Errorf("got %+v", a)
					}
				}
			}},
		{name: "dependency of an add-on left behind", edit: func(s *Setup) { s.Addons = []addons.Installed{s.Addons[0], s.Addons[2]} },
			check: func(t *testing.T, tp *Template, r *Report) {
				if fmt.Sprint(addonNames(tp)) != "[Chunky ViaVersion]" || tp.Addons[1].DependencyOf != "" {
					t.Errorf("got %+v", tp.Addons)
				}
			}},
		{name: "add-ons that need each other", edit: func(s *Setup) {
			s.Addons[1].DependencyOf, s.Addons[2].Requires = "31", []string{"12"}
		}},

		{name: "modpack from Hangar", fabric: true, edit: func(s *Setup) { s.Modpack.Source = addons.Hangar },
			leftOut: []Kind{KindLeftOutModpack}, check: func(t *testing.T, tp *Template, r *Report) {
				if tp.Modpack != nil || fmt.Sprint(addonNames(tp)) != "[Chunky FerriteCore Lithium]" {
					t.Errorf("got %+v and %v: without the modpack, its mods travel one by one", tp.Modpack, addonNames(tp))
				}
			}},
		{name: "modpack on a Paper server", fabric: true, edit: func(s *Setup) { s.Type = "paper" },
			leftOut: []Kind{KindLeftOutModpack}},
		{name: "modpack on a Vanilla server", fabric: true, edit: func(s *Setup) { s.Type, s.Addons, s.Modpack.Includes = "vanilla", nil, nil },
			notes: []Kind{KindNoteWorld, KindNotePlayers}, check: func(t *testing.T, tp *Template, r *Report) {
				if tp.Server.Type != "vanilla" || tp.Modpack == nil || tp.Modpack.Project != "H9OFWiay" {
					t.Errorf("got %+v on %s: a Vanilla server's pack travels", tp.Modpack, tp.Server.Type)
				}
			}},
		{name: "incomplete modpack record", fabric: true, edit: func(s *Setup) { s.Modpack.Pin.Hash = "" },
			leftOut: []Kind{KindLeftOutModpack}},

		{name: "hundreds of add-ons with long names", edit: func(s *Setup) {
			s.Addons = nil
			for i := range MaxAddons {
				r := modrinthRow(fmt.Sprintf("M%07d", i), fmt.Sprintf("mod-%d", i), strings.Repeat("𝓜", maxLabel), fmt.Sprintf("V%07d", i))
				r.VersionNumber = strings.Repeat("𝓥", maxLabel)
				s.Addons = append(s.Addons, r)
			}
		}, leftOut: []Kind{KindLeftOutAddonsSize}, check: func(t *testing.T, tp *Template, r *Report) {
			js, _ := canonicalJSON(tp)
			if len(tp.Addons) == 0 || len(tp.Addons) >= MaxAddons || len(js) > MaxFileSize || r.LeftOut[0].Params["count"] != fmt.Sprint(MaxAddons-len(tp.Addons)) {
				t.Errorf("kept %d add-ons in %d bytes; %+v", len(tp.Addons), len(js), r.LeftOut[0])
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := fabricSetup()
			if !c.fabric {
				s = paperSetup(t)
				s.Settings.MOTD = "Survival with friends — be nice!"
			}
			if c.edit != nil {
				c.edit(&s)
			}
			tp, rep, err := Export(s, c.opts)
			if err != nil {
				t.Fatal(err)
			}
			if c.notes == nil {
				c.notes = basic
			}
			left := wantKinds(t, "left out", rep.LeftOut, c.leftOut...)
			notes := wantKinds(t, "notes", rep.Notes, c.notes...)
			if c.check != nil && left && notes {
				c.check(t, tp, rep)
			}
			roundTrip(t, tp)
		})
	}
}

func TestExportRefuses(t *testing.T) {
	for _, s := range []Setup{
		{Name: "No version", Type: "paper"},
		{Name: "Odd type", Type: "Paper Spigot", MinecraftVersion: "26.2"},
		{Name: "Odd version", Type: "paper", MinecraftVersion: "../26.2"},
	} {
		_, _, err := Export(s, ExportOptions{})
		refused(t, err, KindExportInvalid)
	}
}
