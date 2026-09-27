package worldimport

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// filesAndBytes counts the files of an archive built by a test.
func filesAndBytes(files []tf) (n int, size int64) {
	for _, e := range files {
		if !e.dir {
			n++
			size += int64(len(e.body))
		}
	}
	return n, size
}

func onlyWorld(t *testing.T, in *Inspection) World {
	t.Helper()
	if len(in.Worlds) != 1 {
		t.Fatalf("found %d worlds, want 1: %+v", len(in.Worlds), in.Worlds)
	}
	return in.Worlds[0]
}

func TestInspectSingleplayerBackup(t *testing.T) {
	lv := levelDat(t, singleplayerLevel("My World", "1.21.4", 4189))
	world := append(legacyWorldFiles(lv, true, true), f(".DS_Store", "finder"))
	files := append(withPrefix("My World/", world), f("__MACOSX/My World/._level.dat", "apple"))
	in := inspect(t, Limits{}, upload(t, "2026-09-01_12-00-00_My World.zip", zipBytes(t, files)))

	a := in.Archives[0]
	if a.Name != "2026-09-01_12-00-00_My World.zip" || a.Format != FormatZip || a.Entries != len(files) || a.Bytes == 0 {
		t.Errorf("archive %+v", a)
	}
	w := onlyWorld(t, in)
	n, size := filesAndBytes(world)
	if w.ID != "2026-09-01_12-00-00_My World/My World" || w.Path != "My World" || w.Archive != a.Name ||
		w.Origin != OriginSingleplayer || !w.Default || w.Software != "" || w.Players != 1 || len(w.Companions) != 0 ||
		w.Files != n || w.SizeBytes != size || w.LevelError != "" {
		t.Errorf("world %+v", w)
	}
	if want := []string{dimOverworld, dimNether, dimEnd}; !equalLists(w.Dimensions, want) {
		t.Errorf("dimensions %v, want %v", w.Dimensions, want)
	}
	if w.Level == nil || w.Level.Name != "My World" || w.Level.Owner != uuidOnline || w.Level.Version != "1.21.4" {
		t.Errorf("level %+v", w.Level)
	}
	if len(in.Warnings) != 0 {
		t.Errorf("warnings %v", kinds(in.Warnings))
	}
}

func TestInspectWorldAtTopLevel(t *testing.T) {
	lv := levelDat(t, singleplayerLevel("My World", "1.21.4", 4189))
	in := inspect(t, Limits{}, upload(t, "My World.zip", zipBytes(t, legacyWorldFiles(lv, true, false))))
	w := onlyWorld(t, in)
	if w.ID != "My World" || w.Path != "" || w.Archive != "My World.zip" || !w.Default {
		t.Errorf("world %+v", w)
	}
	if want := []string{dimOverworld, dimNether}; !equalLists(w.Dimensions, want) {
		t.Errorf("dimensions %v, want %v", w.Dimensions, want)
	}
}

func TestInspectPaperServer(t *testing.T) {
	files := paperServerFiles(t)
	for _, src := range []Source{
		upload(t, "server.zip", zipBytes(t, files)),
		upload(t, "server.tar.gz", gzipBytes(t, tarBytes(t, files))),
	} {
		t.Run(src.Name, func(t *testing.T) {
			w := onlyWorld(t, inspect(t, Limits{}, src))
			var inWorld []tf
			for _, e := range files {
				if strings.HasPrefix(e.name, "world") {
					inWorld = append(inWorld, e)
				}
			}
			n, size := filesAndBytes(inWorld)
			if w.ID != "server/world" || w.Path != "world" || w.Software != "paper" || w.Origin != OriginServer ||
				!w.Default || w.Players != 1 || w.Files != n || w.SizeBytes != size {
				t.Errorf("world %+v", w)
			}
			if want := []string{"world_nether", "world_the_end"}; !equalLists(w.Companions, want) {
				t.Errorf("companions %v, want %v", w.Companions, want)
			}
			if want := []string{dimOverworld, dimNether, dimEnd}; !equalLists(w.Dimensions, want) {
				t.Errorf("dimensions %v, want %v", w.Dimensions, want)
			}
		})
	}
}

func TestInspectVanillaServer(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	files := join(
		[]tf{f("server.properties", "level-name=world\ndifficulty=easy\n"), f("eula.txt", "eula=true\n"), f("server.jar", "jar"), f("logs/latest.log", "log")},
		withPrefix("world/", legacyWorldFiles(lv, true, true)),
	)
	w := onlyWorld(t, inspect(t, Limits{}, upload(t, "my-server.zip", zipBytes(t, files))))
	if w.ID != "my-server/world" || w.Software != "vanilla" || w.Origin != OriginServer || !w.Default || len(w.Companions) != 0 {
		t.Errorf("world %+v", w)
	}
}

func fabricServerFiles(t *testing.T) []tf {
	level := legacyLevel("world", "1.21.4", 4189)
	level["ServerBrands"] = stringList("fabric")
	level["WasModded"] = int8(1)
	return join(
		[]tf{
			f("server.properties", "level-name=world\n"),
			f("fabric-server-launch.jar", "launcher"),
			f(".fabric/remappedJars/minecraft-1.21.4.jar", "remapped"),
			f("mods/fabric-api-0.119.2.jar", "fabric api"),
			f("mods/lithium-fabric-0.15.0.jar", "lithium"),
			f("config/lithium.properties", "mixin.ai=true\n"),
			f("config/fabric/indigo-renderer.properties", "ambient-occlusion-mode=hybrid\n"),
		},
		withPrefix("world/", legacyWorldFiles(levelDat(t, level), true, true)),
	)
}

func TestInspectFabricServer(t *testing.T) {
	w := onlyWorld(t, inspect(t, Limits{}, upload(t, "fabric.zip", zipBytes(t, fabricServerFiles(t)))))
	if w.Software != "fabric" || w.Origin != OriginServer || w.Level == nil || !w.Level.Modded {
		t.Errorf("world %+v", w)
	}
}

func TestInspectNestedFolder(t *testing.T) {
	lv := levelDat(t, legacyLevel("survival world", "1.21.4", 4189))
	files := withPrefix("Backups/2026-09-01/survival world/", legacyWorldFiles(lv, false, false))
	w := onlyWorld(t, inspect(t, Limits{}, upload(t, "backup.tar.gz", gzipBytes(t, tarBytes(t, files)))))
	if w.Path != "Backups/2026-09-01/survival world" || w.Origin != OriginUnknown || w.Software != "" || !w.Default {
		t.Errorf("world %+v", w)
	}
}

func TestInspectSeveralWorlds(t *testing.T) {
	a := levelDat(t, legacyLevel("World A", "1.21.4", 4189))
	b := levelDat(t, legacyLevel("World B", "1.20.1", 3465))
	files := []tf{
		f("saves/World B/level.dat", b), f("saves/World B/region/r.0.0.mca", regionData),
		f("saves/World A/level.dat", a), f("saves/World A/region/r.0.0.mca", regionData), f("saves/World A/region/r.1.0.mca", regionData),
	}
	in := inspect(t, Limits{}, upload(t, "saves.zip", zipBytes(t, files)))
	if len(in.Worlds) != 2 {
		t.Fatalf("found %d worlds", len(in.Worlds))
	}
	if in.Worlds[0].ID != "saves/saves/World A" || in.Worlds[1].ID != "saves/saves/World B" || in.Worlds[0].Default || in.Worlds[1].Default {
		t.Errorf("worlds %+v", in.Worlds)
	}
	if in.Worlds[0].Level.Name != "World A" || in.Worlds[1].Level.Version != "1.20.1" || in.Worlds[0].Files != 3 {
		t.Errorf("levels %+v %+v", in.Worlds[0], in.Worlds[1])
	}

	target := Target{Type: TypePaper, MinecraftVersion: "1.21.4"}
	_, err := in.Plan(target, Options{})
	if got := refusalKind(t, err); got != KindChooseWorld {
		t.Errorf("without a choice: got %s", got)
	}
	_, err = in.Plan(target, Options{World: "saves/saves/World C"})
	if got := refusalKind(t, err); got != KindUnknownWorld {
		t.Errorf("unknown world: got %s", got)
	}
	p, err := in.Plan(target, Options{World: "saves/saves/World B"})
	if err != nil {
		t.Fatal(err)
	}
	if l := leftOut(p, LeftOtherWorlds); l == nil || l.Files != 3 || p.World.Level.Name != "World B" {
		t.Errorf("preview %+v", p)
	}
}

func TestInspectServerWithSeveralWorlds(t *testing.T) {
	survival := levelDat(t, legacyLevel("survival", "1.21.4", 4189))
	lobby := levelDat(t, legacyLevel("lobby", "1.21.4", 4189))
	files := join(
		[]tf{f("server.properties", "level-name=survival\n"), f("eula.txt", "eula=true\n"), f("purpur.yml", "settings: {}\n"), f("purpur-1.21.4.jar", "jar")},
		withPrefix("survival/", legacyWorldFiles(survival, false, false)),
		withPrefix("survival_nether/", []tf{f("level.dat", survival), f("DIM-1/region/r.0.0.mca", regionData+"nether")}),
		withPrefix("survival_the_end/", []tf{f("level.dat", survival), f("DIM1/region/r.0.0.mca", regionData+"end")}),
		withPrefix("lobby/", []tf{f("level.dat", lobby), f("region/r.0.0.mca", regionData+"lobby")}),
	)
	in := inspect(t, Limits{}, upload(t, "server-backup.tar.gz", gzipBytes(t, tarBytes(t, files))))
	if len(in.Worlds) != 2 {
		t.Fatalf("found %d worlds", len(in.Worlds))
	}
	s, l := in.Worlds[0], in.Worlds[1]
	if s.Path != "survival" || !s.Default || s.Software != "purpur" || !equalLists(s.Companions, []string{"survival_nether", "survival_the_end"}) {
		t.Errorf("default world %+v", s)
	}
	if l.Path != "lobby" || l.Default || len(l.Companions) != 0 || !equalLists(l.Dimensions, []string{dimOverworld}) {
		t.Errorf("other world %+v", l)
	}
}

// aternosUploads are the three downloads Aternos offers for a Paper world,
// the Nether's with the " (1)" a browser adds to a second download.
func aternosUploads(t *testing.T) []Source {
	level := legacyLevel("world", "1.21.4", 4189)
	level["ServerBrands"] = stringList("Paper")
	lv := levelDat(t, level)
	world := upload(t, "world.zip", zipBytes(t, []tf{
		f("level.dat", lv), f("session.lock", "lock"), f("uid.dat", "uid"), f("region/r.0.0.mca", regionData+"overworld"),
		f("playerdata/"+uuidOnline+".dat", "player"),
	}))
	nether := upload(t, "world_nether (1).zip", zipBytes(t, []tf{f("level.dat", lv), f("uid.dat", "uid"), f("DIM-1/region/r.0.0.mca", regionData+"nether")}))
	end := upload(t, "world_the_end.zip", zipBytes(t, []tf{f("level.dat", lv), f("DIM1/region/r.0.0.mca", regionData+"end")}))
	return []Source{end, world, nether}
}

func TestInspectAternosDownloads(t *testing.T) {
	in := inspect(t, Limits{}, aternosUploads(t)...)
	w := onlyWorld(t, in)
	if w.ID != "world" || w.Archive != "world.zip" || w.Path != "" || w.Origin != OriginServer || !w.Default || w.Files != 10 {
		t.Errorf("world %+v", w)
	}
	if want := []string{"world_nether (1).zip", "world_the_end.zip"}; !equalLists(w.Companions, want) {
		t.Errorf("companions %v, want %v", w.Companions, want)
	}
	if want := []string{dimOverworld, dimNether, dimEnd}; !equalLists(w.Dimensions, want) {
		t.Errorf("dimensions %v, want %v", w.Dimensions, want)
	}
}

func TestInspectSameArchiveNameTwice(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	a := upload(t, "world.zip", zipBytes(t, []tf{f("level.dat", lv), f("region/r.0.0.mca", regionData)}))
	b := upload(t, "world.zip", zipBytes(t, []tf{f("level.dat", lv), f("region/r.0.0.mca", regionData)}))
	in := inspect(t, Limits{}, a, b)
	if len(in.Worlds) != 2 || in.Worlds[0].ID != "world" || in.Worlds[1].ID != "world (2)" {
		t.Errorf("worlds %+v", in.Worlds)
	}
}

func TestInspectModernWorlds(t *testing.T) {
	t.Run("singleplayer", func(t *testing.T) {
		w := onlyWorld(t, inspect(t, Limits{}, upload(t, "Creative Build.zip", zipBytes(t, withPrefix("Creative Build/", modernWorldFiles(t))))))
		if w.Level == nil || w.Level.Seed != "1234567" || w.Origin != OriginSingleplayer || w.Players != 1 {
			t.Errorf("world %+v, level %+v", w, w.Level)
		}
		if want := []string{dimOverworld, dimNether, dimEnd}; !equalLists(w.Dimensions, want) {
			t.Errorf("dimensions %v, want %v", w.Dimensions, want)
		}
	})
	t.Run("paper", func(t *testing.T) {
		w := onlyWorld(t, inspect(t, Limits{}, upload(t, "world.zip", zipBytes(t, withPrefix("world/", modernPaperWorldFiles(t))))))
		if w.Level == nil || w.Level.Seed != "99887766" || w.Level.DataVersion != 4903 || w.Origin != OriginUnknown || w.Players != 1 {
			t.Errorf("world %+v, level %+v", w, w.Level)
		}
		if want := []string{dimOverworld, dimNether, dimEnd}; !equalLists(w.Dimensions, want) {
			t.Errorf("dimensions %v, want %v", w.Dimensions, want)
		}
	})
}

// modernWorldFiles is a singleplayer world saved by Minecraft 26.2.
func modernWorldFiles(t *testing.T) []tf {
	return []tf{
		f("level.dat", levelDat(t, modernLevel("Creative Build", "26.2", 4903))),
		f("session.lock", "lock"),
		f("dimensions/minecraft/overworld/region/r.0.0.mca", regionData+"overworld"),
		f("dimensions/minecraft/overworld/entities/r.0.0.mca", regionData+"entities"),
		f("dimensions/minecraft/the_nether/region/r.0.0.mca", regionData+"nether"),
		f("dimensions/minecraft/the_end/region/r.0.0.mca", regionData+"end"),
		f("data/minecraft/world_gen_settings.dat", worldGenSettings(t, 4903, 1234567)),
		f("data/minecraft/game_rules.dat", "rules"),
		f("players/data/"+uuidOnline+".dat", "player"),
	}
}

func TestInspectBedrockWorlds(t *testing.T) {
	bedrock := []tf{
		f("My Bedrock World/db/000001.ldb", "leveldb"),
		f("My Bedrock World/db/CURRENT", "MANIFEST-000001\n"),
		f("My Bedrock World/levelname.txt", "My Bedrock World"),
		f("My Bedrock World/level.dat", "\x0a\x00\x00\x00\x44\x0b\x00\x00\x0a\x00\x00"),
	}
	_, err := Inspect(context.Background(), []Source{upload(t, "bedrock.zip", zipBytes(t, bedrock))}, Limits{})
	if got := refusalKind(t, err); got != KindBedrock {
		t.Errorf("Bedrock only: got %s", got)
	}

	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	files := append(bedrock, f("java/level.dat", lv), f("java/region/r.0.0.mca", regionData))
	in := inspect(t, Limits{}, upload(t, "worlds.zip", zipBytes(t, files)))
	w := onlyWorld(t, in)
	if w.Path != "java" || !equalLists(kinds(in.Warnings), []string{KindBedrockIgnored}) || in.Warnings[0].Params["count"] != 1 {
		t.Errorf("world %+v, warnings %+v", w, in.Warnings)
	}
}

func TestInspectFindsNoWorld(t *testing.T) {
	cases := map[string]struct {
		files []tf
		hint  string
	}{
		"region files only": {[]tf{f("world/region/r.0.0.mca", regionData)}, "no level.dat"},
		"no world at all":   {[]tf{f("readme.txt", "hello")}, "whole server folder"},
		"only system files": {[]tf{f("__MACOSX/world/level.dat", "apple")}, "whole server folder"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Inspect(context.Background(), []Source{upload(t, "upload.zip", zipBytes(t, c.files))}, Limits{})
			if got := refusalKind(t, err); got != KindNoWorld {
				t.Fatalf("got %s", got)
			}
			var e *Error
			if errors.As(err, &e); !strings.Contains(e.Hint, c.hint) {
				t.Errorf("hint %q", e.Hint)
			}
		})
	}
}

func TestInspectLimitsWorlds(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	files := []tf{f("a/level.dat", lv), f("b/level.dat", lv), f("c/level.dat", lv)}
	_, err := Inspect(context.Background(), []Source{upload(t, "worlds.zip", zipBytes(t, files))}, Limits{MaxWorlds: 2})
	if got := refusalKind(t, err); got != KindTooManyWorlds {
		t.Fatalf("got %s", got)
	}
}

func TestInspectDamagedLevel(t *testing.T) {
	good := levelDat(t, legacyLevel("world", "1.21.4", 4189))

	in := inspect(t, Limits{}, upload(t, "world.zip", zipBytes(t, []tf{
		f("world/level.dat", "garbage"), f("world/level.dat_old", good), f("world/region/r.0.0.mca", regionData),
	})))
	if w := onlyWorld(t, in); w.Level == nil || !w.Level.FromBackup || w.Level.Name != "world" || w.LevelError != "" {
		t.Errorf("with level.dat_old: world %+v", w)
	}

	in = inspect(t, Limits{}, upload(t, "world.zip", zipBytes(t, []tf{f("world/level.dat", "garbage"), f("world/region/r.0.0.mca", regionData)})))
	if w := onlyWorld(t, in); w.Level != nil || w.LevelError != "level.dat is damaged or incomplete." {
		t.Errorf("without level.dat_old: world %+v", w)
	}

	big := strings.Repeat("x", 5<<20)
	in = inspect(t, Limits{}, upload(t, "world.zip", zipBytes(t, []tf{f("world/level.dat", big), f("world/region/r.0.0.mca", regionData)})))
	if w := onlyWorld(t, in); w.Level != nil || !strings.Contains(w.LevelError, "larger than 4.2 MB") {
		t.Errorf("oversized level.dat: world %+v", w)
	}
}

func TestInspectCompanions(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	world := withPrefix("world/", legacyWorldFiles(lv, false, false))

	t.Run("guessed", func(t *testing.T) {
		in := inspect(t, Limits{},
			upload(t, "world.zip", zipBytes(t, world)),
			upload(t, "download.zip", zipBytes(t, []tf{f("level.dat", lv), f("DIM-1/region/r.0.0.mca", regionData+"nether")})))
		w := onlyWorld(t, in)
		if !equalLists(w.Companions, []string{"download.zip"}) || !equalLists(w.Dimensions, []string{dimOverworld, dimNether}) {
			t.Errorf("world %+v", w)
		}
		p, err := in.Plan(Target{Type: TypeVanilla, MinecraftVersion: "1.21.4"}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !hasKind(p.Warnings, KindCompanionGuessed) || !hasKind(p.Warnings, KindMerged) {
			t.Errorf("warnings %v", kinds(p.Warnings))
		}
	})

	t.Run("orphan", func(t *testing.T) {
		files := append(world, f("backup_nether/DIM-1/region/r.0.0.mca", regionData+"nether"))
		in := inspect(t, Limits{}, upload(t, "world.zip", zipBytes(t, files)))
		w := onlyWorld(t, in)
		if len(w.Companions) != 0 || !equalLists(kinds(in.Warnings), []string{KindOrphanDimension}) || in.Warnings[0].Params["folder"] != "backup_nether" {
			t.Errorf("world %+v, warnings %+v", w, in.Warnings)
		}
		p, err := in.Plan(Target{Type: TypePaper, MinecraftVersion: "1.21.4"}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if l := leftOut(p, LeftOther); l == nil || l.Files != 1 {
			t.Errorf("left out %+v", p.LeftOut)
		}
	})

	t.Run("without level.dat", func(t *testing.T) {
		files := append(world, f("world_the_end/DIM1/region/r.0.0.mca", regionData+"end"))
		in := inspect(t, Limits{}, upload(t, "world.zip", zipBytes(t, files)))
		w := onlyWorld(t, in)
		if !equalLists(w.Companions, []string{"world_the_end"}) || len(in.Warnings) != 0 {
			t.Errorf("world %+v, warnings %v", w, kinds(in.Warnings))
		}
	})

	t.Run("custom dimension", func(t *testing.T) {
		files := append(world, f("world_mymod_mining/level.dat", lv), f("world_mymod_mining/dimensions/mymod/mining/region/r.0.0.mca", regionData+"mining"))
		w := onlyWorld(t, inspect(t, Limits{}, upload(t, "world.zip", zipBytes(t, files))))
		if !equalLists(w.Companions, []string{"world_mymod_mining"}) || !equalLists(w.Dimensions, []string{dimOverworld, "mymod:mining"}) {
			t.Errorf("world %+v", w)
		}
	})
}

func TestTrimCopySuffix(t *testing.T) {
	cases := map[string]string{
		"world_nether (1)": "world_nether",
		"world_nether(2)":  "world_nether",
		"world (12)":       "world",
		"world ()":         "world ()",
		"world (a)":        "world (a)",
		"world":            "world",
		"(3)":              "",
	}
	for in, want := range cases {
		if got := trimCopySuffix(in); got != want {
			t.Errorf("trimCopySuffix(%q) = %q, want %q", in, got, want)
		}
	}
}
