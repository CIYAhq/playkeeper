package worldimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// stageTo plans and stages an import, checks that both agree, and returns
// the preview and the staged files.
func stageTo(t *testing.T, in *Inspection, target Target, o Options) (*Preview, map[string]string) {
	t.Helper()
	plan, err := in.Plan(target, o)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "staging")
	p, err := in.Stage(context.Background(), dir, target, o)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if !reflect.DeepEqual(plan, p) {
		t.Errorf("Stage's preview differs from Plan's")
	}
	files := staged(t, dir)
	var size int64
	for _, b := range files {
		size += int64(len(b))
	}
	if p.FileCount != len(files) || p.SizeBytes != size {
		t.Errorf("preview says %d files and %d bytes, Stage wrote %d files and %d bytes", p.FileCount, p.SizeBytes, len(files), size)
	}
	return p, files
}

func withDir(prefix string, names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = prefix + n
	}
	return out
}

func checkFiles(t *testing.T, got map[string]string, want []string) {
	t.Helper()
	if keys := sortedKeys(got); !equalLists(keys, want) {
		t.Errorf("staged files:\n  %s\nwant:\n  %s", strings.Join(keys, "\n  "), strings.Join(want, "\n  "))
	}
}

func leftOutKinds(p *Preview) map[string]int {
	out := map[string]int{}
	for _, l := range p.LeftOut {
		out[l.Kind] = l.Files
	}
	return out
}

func TestStageSingleplayerWorld(t *testing.T) {
	lv := levelDat(t, singleplayerLevel("My World", "1.21.4", 4189))
	files := append(withPrefix("My World/", legacyWorldFiles(lv, true, true)), f("My World/.DS_Store", "finder"), f("__MACOSX/My World/._level.dat", "apple"))
	in := inspect(t, Limits{}, upload(t, "2026-09-01_12-00-00_My World.zip", zipBytes(t, files)))

	overworld := []string{
		"advancements/" + uuidOnline + ".json", "data/raids.dat", "datapacks/terralith.zip", "entities/r.0.0.mca", "icon.png",
		"level.dat", "playerdata/" + uuidOnline + ".dat", "poi/r.0.0.mca", "region/r.-1.0.mca", "region/r.0.0.mca",
		"stats/" + uuidOnline + ".json",
	}
	merged := withDir("world/", append([]string{"DIM-1/data/raids.dat", "DIM-1/region/r.0.0.mca", "DIM1/region/r.0.0.mca"}, overworld...))
	split := append(withDir("world/", overworld), "world_nether/DIM-1/data/raids.dat", "world_nether/DIM-1/region/r.0.0.mca",
		"world_nether/level.dat", "world_the_end/DIM1/region/r.0.0.mca", "world_the_end/level.dat")
	three := []string{"world", "world_nether", "world_the_end"}

	cases := []struct {
		target   Target
		want     []string
		folders  []string
		warnings []string
	}{
		{Target{Type: TypePaper, MinecraftVersion: "1.21.4"}, split, three, []string{KindBukkitSplit}},
		{Target{Type: TypePurpur, MinecraftVersion: "1.21.4"}, split, three, []string{KindBukkitSplit}},
		{Target{Type: TypeFolia, MinecraftVersion: "1.21.4"}, split, three, []string{KindBukkitSplit}},
		{Target{Type: TypeVanilla, MinecraftVersion: "1.21.4"}, merged, []string{"world"}, nil},
		{Target{Type: TypeFabric, MinecraftVersion: "1.21.4"}, merged, []string{"world"}, nil},
		{Target{Type: TypeQuilt, MinecraftVersion: "1.21.4"}, merged, []string{"world"}, nil},
		{Target{Type: TypeForge, MinecraftVersion: "1.21.4"}, merged, []string{"world"}, nil},
		{Target{Type: TypeNeoForge, MinecraftVersion: "1.21.4"}, merged, []string{"world"}, nil},
		{Target{Type: TypePaper, MinecraftVersion: "26.2"}, merged, []string{"world"}, []string{KindUpgrade, KindLayoutUpgrade}},
		{Target{Type: TypeVanilla, MinecraftVersion: "26.2"}, merged, []string{"world"}, []string{KindUpgrade, KindLayoutUpgrade}},
	}
	for _, c := range cases {
		t.Run(c.target.Type+" "+c.target.MinecraftVersion, func(t *testing.T) {
			p, got := stageTo(t, in, c.target, Options{})
			checkFiles(t, got, c.want)
			if !equalLists(p.Folders, c.folders) || !equalLists(kinds(p.Warnings), c.warnings) || len(p.Problems) != 0 {
				t.Errorf("folders %v, warnings %v, problems %v", p.Folders, kinds(p.Warnings), kinds(p.Problems))
			}
			if got["world/level.dat"] != lv || got["world/region/r.0.0.mca"] != regionData+"overworld" {
				t.Error("staged files differ from the upload")
			}
			if len(c.folders) == 3 && (got["world_nether/level.dat"] != lv || got["world_the_end/level.dat"] != lv) {
				t.Error("the Nether and End folders didn't get a copy of level.dat")
			}
		})
	}

	p, err := in.Plan(Target{Type: TypePaper, MinecraftVersion: "1.21.4"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	wantDims := []Dimension{{ID: dimOverworld, Folder: "world", Files: 4}, {ID: dimNether, Folder: "world_nether/DIM-1", Files: 2}, {ID: dimEnd, Folder: "world_the_end/DIM1", Files: 1}}
	for i := range p.Dimensions {
		p.Dimensions[i].Bytes = 0
	}
	if !reflect.DeepEqual(p.Dimensions, wantDims) {
		t.Errorf("dimensions %+v", p.Dimensions)
	}
	wantSettings := []Setting{
		{Key: "level-seed", Value: "-4172144997902289642", Source: SourceLevel},
		{Key: "gamemode", Value: "survival", Source: SourceLevel},
		{Key: "difficulty", Value: "normal", Source: SourceLevel},
		{Key: "hardcore", Value: "false", Source: SourceLevel},
	}
	if !reflect.DeepEqual(p.Settings, wantSettings) || p.RefusedSettings != nil || p.IgnoredSettings != nil {
		t.Errorf("settings %+v, refused %v, ignored %v", p.Settings, p.RefusedSettings, p.IgnoredSettings)
	}
	if !equalLists(p.DataPacks, []string{"file/terralith.zip"}) || p.Players != 1 || p.Version.Compat != CompatSame || !p.OK() {
		t.Errorf("preview %+v", p)
	}
	if want := map[string]int{LeftSessionLock: 1, LeftSystemFiles: 2}; !reflect.DeepEqual(leftOutKinds(p), want) {
		t.Errorf("left out %+v", p.LeftOut)
	}
}

func TestStageWritesPrivateFiles(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	in := inspect(t, Limits{}, upload(t, "world.zip", zipBytes(t, withPrefix("world/", legacyWorldFiles(lv, true, true)))))
	dir := filepath.Join(t.TempDir(), "staging")
	if _, err := in.Stage(context.Background(), dir, Target{Type: TypePaper, MinecraftVersion: "1.21.4"}, Options{}); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		mask := fs.FileMode(0o137)
		if d.IsDir() {
			mask = 0o027
		}
		if fi.Mode().Perm()&mask != 0 {
			t.Errorf("%s has mode %v", p, fi.Mode())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStagePaperServer(t *testing.T) {
	files := paperServerFiles(t)
	zipped := inspect(t, Limits{}, upload(t, "server.zip", zipBytes(t, files)))
	worldFiles := withDir("world/", []string{"data/raids.dat", "datapacks/bukkit/pack.mcmeta", "entities/r.0.0.mca", "level.dat",
		"paper-world.yml", "playerdata/" + uuidOnline + ".dat", "poi/r.0.0.mca", "region/r.0.0.mca", "uid.dat"})

	t.Run("to vanilla", func(t *testing.T) {
		p, got := stageTo(t, zipped, Target{Type: TypeVanilla, MinecraftVersion: "1.21.11"}, Options{})
		checkFiles(t, got, append([]string{"world/DIM-1/region/r.0.0.mca", "world/DIM1/region/r.0.0.mca"}, worldFiles...))
		if got["world/DIM-1/region/r.0.0.mca"] != regionData+"nether" || got["world/uid.dat"] != "uid-overworld" {
			t.Error("staged files differ from the upload")
		}
		if !equalLists(kinds(p.Warnings), []string{KindMerged}) || !equalLists(p.Folders, []string{"world"}) ||
			p.Files != nil || p.Addons != nil || p.Operators != nil {
			t.Errorf("preview %+v", p)
		}
		wantLeft := map[string]int{LeftSessionLock: 2, LeftLogs: 2, LeftCaches: 2, LeftServerSoftware: 3, LeftServerConfig: 5,
			LeftAddons: 3, LeftPlayerLists: 2, LeftOperators: 1, LeftCompanionFiles: 5}
		if got := leftOutKinds(p); !reflect.DeepEqual(got, wantLeft) {
			t.Errorf("left out %v, want %v", got, wantLeft)
		}
		order := []string{LeftSessionLock, LeftLogs, LeftCaches, LeftServerSoftware, LeftServerConfig, LeftAddons, LeftPlayerLists, LeftOperators, LeftCompanionFiles}
		for i, l := range p.LeftOut {
			if l.Kind != order[i] || !strings.HasSuffix(l.Text, ".") || len(l.Examples) == 0 || len(l.Examples) > 3 {
				t.Errorf("left out #%d: %+v", i, l)
			}
		}
		if s := p.Settings[0]; s.Key != "level-seed" || s.Value != "-4172144997902289642" || s.Source != SourceLevel {
			t.Errorf("seed %+v: level.dat should win over server.properties", s)
		}
		if want := []string{"enable-rcon", "level-name", "online-mode", "rcon.password", "server-port"}; !equalLists(p.RefusedSettings, want) {
			t.Errorf("refused %v, want %v", p.RefusedSettings, want)
		}
		if !equalLists(p.IgnoredSettings, []string{"motd"}) {
			t.Errorf("ignored %v", p.IgnoredSettings)
		}
	})

	t.Run("to paper, keeping everything", func(t *testing.T) {
		o := Options{KeepAddons: true, KeepPlayerLists: true, KeepOperators: true}
		p, got := stageTo(t, zipped, Target{Type: TypePaper, MinecraftVersion: "1.21.11"}, o)
		want := append([]string{"banned-players.json", "ops.json", "plugins/Essentials/config.yml", "plugins/EssentialsX.jar", "whitelist.json"}, worldFiles...)
		want = append(want, "world_nether/DIM-1/region/r.0.0.mca", "world_nether/level.dat", "world_nether/paper-world.yml", "world_nether/uid.dat",
			"world_the_end/DIM1/region/r.0.0.mca", "world_the_end/level.dat", "world_the_end/uid.dat")
		checkFiles(t, got, want)
		if got["world_nether/uid.dat"] != "uid-nether" || got["plugins/EssentialsX.jar"] != "plugin jar" {
			t.Error("staged files differ from the upload")
		}
		if !equalLists(p.Files, []string{"banned-players.json", "ops.json", "whitelist.json"}) || !equalLists(p.Addons, []string{"plugins"}) ||
			!equalLists(p.Operators, []string{"Steve"}) || !equalLists(p.Folders, []string{"world", "world_nether", "world_the_end"}) {
			t.Errorf("preview %+v", p)
		}
		if want := []string{KindAddonsKept, KindOperatorsKept}; !equalLists(kinds(p.Warnings), want) {
			t.Errorf("warnings %v, want %v", kinds(p.Warnings), want)
		}
		wantLeft := map[string]int{LeftSessionLock: 2, LeftLogs: 2, LeftCaches: 3, LeftServerSoftware: 3, LeftServerConfig: 5}
		if got := leftOutKinds(p); !reflect.DeepEqual(got, wantLeft) {
			t.Errorf("left out %v, want %v", got, wantLeft)
		}

		tarred := inspect(t, Limits{}, upload(t, "server.tar.gz", gzipBytes(t, tarBytes(t, files))))
		if _, fromTar := stageTo(t, tarred, Target{Type: TypePaper, MinecraftVersion: "1.21.11"}, o); !reflect.DeepEqual(fromTar, got) {
			t.Error("staging the .tar.gz gave other files than staging the .zip")
		}
	})
}

func TestStageVanillaServerForPaper(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	files := join(
		[]tf{f("server.properties", "level-name=world\ndifficulty=easy\n"), f("eula.txt", "eula=true\n"), f("server.jar", "jar"), f("logs/latest.log", "log")},
		withPrefix("world/", legacyWorldFiles(lv, true, true)),
	)
	in := inspect(t, Limits{}, upload(t, "my-server.zip", zipBytes(t, files)))
	p, got := stageTo(t, in, Target{Type: TypePaper, MinecraftVersion: "1.21.4"}, Options{})
	if got["world_nether/DIM-1/region/r.0.0.mca"] != regionData+"nether" || got["world_the_end/level.dat"] != lv || len(got) != 16 {
		t.Errorf("staged %v", sortedKeys(got))
	}
	if want := map[string]int{LeftSessionLock: 1, LeftLogs: 1, LeftServerSoftware: 1, LeftServerConfig: 2}; !reflect.DeepEqual(leftOutKinds(p), want) {
		t.Errorf("left out %v", leftOutKinds(p))
	}
	if s := p.Settings[2]; s.Key != "difficulty" || s.Value != "normal" || s.Source != SourceLevel {
		t.Errorf("difficulty %+v", s)
	}
}

func TestStageFromSeveralArchives(t *testing.T) {
	in := inspect(t, Limits{}, aternosUploads(t)...)

	p, got := stageTo(t, in, Target{Type: TypePaper, MinecraftVersion: "1.21.4"}, Options{})
	checkFiles(t, got, []string{"world/level.dat", "world/playerdata/" + uuidOnline + ".dat", "world/region/r.0.0.mca", "world/uid.dat",
		"world_nether/DIM-1/region/r.0.0.mca", "world_nether/level.dat", "world_nether/uid.dat",
		"world_the_end/DIM1/region/r.0.0.mca", "world_the_end/level.dat"})
	if got["world_nether/DIM-1/region/r.0.0.mca"] != regionData+"nether" || got["world_the_end/DIM1/region/r.0.0.mca"] != regionData+"end" {
		t.Error("staged files differ from the upload")
	}
	if len(p.Warnings) != 0 {
		t.Errorf("warnings %v", kinds(p.Warnings))
	}

	p, got = stageTo(t, in, Target{Type: TypeVanilla, MinecraftVersion: "1.21.4"}, Options{})
	checkFiles(t, got, []string{"world/DIM-1/region/r.0.0.mca", "world/DIM1/region/r.0.0.mca", "world/level.dat",
		"world/playerdata/" + uuidOnline + ".dat", "world/region/r.0.0.mca", "world/uid.dat"})
	if !equalLists(kinds(p.Warnings), []string{KindMerged}) || leftOutKinds(p)[LeftCompanionFiles] != 3 {
		t.Errorf("warnings %v, left out %v", kinds(p.Warnings), leftOutKinds(p))
	}
	if folders := p.Warnings[0].Params["folders"]; !reflect.DeepEqual(folders, []string{"world_nether (1).zip", "world_the_end.zip"}) {
		t.Errorf("merged folders %v", folders)
	}
}

func TestStageModernPaperWorld(t *testing.T) {
	files := modernPaperWorldFiles(t)
	in := inspect(t, Limits{}, upload(t, "world.zip", zipBytes(t, withPrefix("world/", files))))

	t.Run("to vanilla", func(t *testing.T) {
		p, got := stageTo(t, in, Target{Type: TypeVanilla, MinecraftVersion: "26.2"}, Options{})
		checkFiles(t, got, []string{
			"world/data/minecraft/game_rules.dat",
			"world/data/minecraft/scoreboard.dat",
			"world/data/minecraft/weather.dat",
			"world/data/minecraft/world_gen_settings.dat",
			"world/datapacks/pack.zip",
			"world/dimensions/minecraft/overworld/data/minecraft/raids.dat",
			"world/dimensions/minecraft/overworld/entities/r.0.0.mca",
			"world/dimensions/minecraft/overworld/poi/r.0.0.mca",
			"world/dimensions/minecraft/overworld/region/r.0.0.mca",
			"world/dimensions/minecraft/the_end/region/r.0.0.mca",
			"world/dimensions/minecraft/the_nether/region/r.0.0.mca",
			"world/level.dat",
			"world/players/advancements/" + uuidOnline + ".json",
			"world/players/data/" + uuidOnline + ".dat",
			"world/players/stats/" + uuidOnline + ".json",
		})
		if got["world/data/minecraft/weather.dat"] != "paper weather" {
			t.Errorf("weather.dat is %q, want Paper's copy", got["world/data/minecraft/weather.dat"])
		}
		if !equalLists(kinds(p.Warnings), []string{KindPaperFiles}) || leftOutKinds(p)[LeftStaleCopies] != 1 {
			t.Errorf("warnings %v, left out %v", kinds(p.Warnings), leftOutKinds(p))
		}
		if p.Settings[0].Value != "99887766" {
			t.Errorf("settings %+v", p.Settings)
		}
		wantDims := []Dimension{
			{ID: dimOverworld, Folder: "world/dimensions/minecraft/overworld", Files: 4},
			{ID: dimNether, Folder: "world/dimensions/minecraft/the_nether", Files: 1},
			{ID: dimEnd, Folder: "world/dimensions/minecraft/the_end", Files: 1},
		}
		for i := range p.Dimensions {
			p.Dimensions[i].Bytes = 0
		}
		if !reflect.DeepEqual(p.Dimensions, wantDims) {
			t.Errorf("dimensions %+v", p.Dimensions)
		}
	})

	t.Run("to paper", func(t *testing.T) {
		p, got := stageTo(t, in, Target{Type: TypePaper, MinecraftVersion: "26.2"}, Options{})
		var want []string
		for _, e := range files {
			if e.name != "session.lock" {
				want = append(want, "world/"+e.name)
			}
		}
		sort.Strings(want)
		checkFiles(t, got, want)
		if got["world/dimensions/minecraft/overworld/data/minecraft/weather.dat"] != "paper weather" || len(p.Warnings) != 0 {
			t.Errorf("warnings %v", kinds(p.Warnings))
		}
	})
}

func TestStageModernWorld(t *testing.T) {
	files := modernWorldFiles(t)
	in := inspect(t, Limits{}, upload(t, "Creative Build.zip", zipBytes(t, withPrefix("Creative Build/", files))))
	var want []string
	for _, e := range files {
		if e.name != "session.lock" {
			want = append(want, "world/"+e.name)
		}
	}
	sort.Strings(want)
	for _, typ := range []string{TypePaper, TypeVanilla, TypeFabric} {
		t.Run(typ, func(t *testing.T) {
			p, got := stageTo(t, in, Target{Type: typ, MinecraftVersion: "26.2"}, Options{})
			checkFiles(t, got, want)
			if len(p.Warnings) != 0 || !equalLists(p.Folders, []string{"world"}) {
				t.Errorf("warnings %v, folders %v", kinds(p.Warnings), p.Folders)
			}
		})
	}

	p, err := in.Plan(Target{Type: TypePaper, MinecraftVersion: "1.21.11"}, Options{})
	if err != nil || !equalLists(kinds(p.Problems), []string{KindWorldNewer}) {
		t.Errorf("to an older server: %v, problems %v", err, kinds(p.Problems))
	}
}

func TestPlanRefusesLayoutsItCantConvert(t *testing.T) {
	spigot := modernLevel("world", "26.1", 4786)
	spigot["ServerBrands"] = stringList("Spigot")
	delete(spigot, "singleplayer_uuid")
	spigotLevel := levelDat(t, spigot)
	legacy := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	modern := levelDat(t, modernLevel("world", "26.2", 4903))

	cases := []struct {
		name  string
		files []tf
		want  []string
	}{
		{"Spigot 26.1", []tf{
			f("world/level.dat", spigotLevel), f("world/region/r.0.0.mca", regionData),
			f("world_nether/level.dat", spigotLevel), f("world_nether/DIM-1/region/r.0.0.mca", regionData),
		}, []string{KindSpigotLayout}},
		{"old world with a new Nether", []tf{
			f("world/level.dat", legacy), f("world/region/r.0.0.mca", regionData),
			f("world/dimensions/minecraft/the_nether/region/r.0.0.mca", regionData),
		}, []string{KindMixedLayout}},
		{"new world with an old Nether", []tf{
			f("world/level.dat", modern), f("world/dimensions/minecraft/overworld/region/r.0.0.mca", regionData),
			f("world/DIM-1/region/r.0.0.mca", regionData),
		}, []string{KindMixedLayout}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := inspect(t, Limits{}, upload(t, "server.zip", zipBytes(t, c.files)))
			target := Target{Type: TypePaper, MinecraftVersion: "26.2"}
			p, err := in.Plan(target, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if !equalLists(kinds(p.Problems), c.want) || p.OK() {
				t.Fatalf("problems %v, want %v", kinds(p.Problems), c.want)
			}
			dir := filepath.Join(t.TempDir(), "staging")
			if _, err := in.Stage(context.Background(), dir, target, Options{}); refusalKind(t, err) != KindBlocked {
				t.Errorf("Stage: %v", err)
			}
		})
	}
}

func TestStageRefusesWhileThereAreProblems(t *testing.T) {
	lv := levelDat(t, singleplayerLevel("My World", "1.21.11", 4671))
	src := upload(t, "My World.zip", zipBytes(t, legacyWorldFiles(lv, true, true)))
	target := Target{Type: TypePaper, MinecraftVersion: "1.21.4"}

	in := inspect(t, Limits{}, src)
	dir := filepath.Join(t.TempDir(), "staging")
	p, err := in.Stage(context.Background(), dir, target, Options{})
	if got := refusalKind(t, err); got != KindBlocked {
		t.Fatalf("got %s", got)
	}
	var e *Error
	errors.As(err, &e)
	if p == nil || !reflect.DeepEqual(e.Params["problems"], []string{KindWorldNewer}) || e.Hint != p.Problems[0].Hint || e.Msg != p.Problems[0].Text {
		t.Errorf("error %+v, preview %+v", e, p)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stage created the staging directory: %v", err)
	}

	in = inspect(t, Limits{MaxFiles: 2}, src)
	_, err = in.Stage(context.Background(), dir, target, Options{})
	if errors.As(err, &e); !reflect.DeepEqual(e.Params["problems"], []string{KindWorldNewer, KindTooManyFiles}) ||
		!strings.HasSuffix(e.Msg, " The preview lists 1 more problem.") {
		t.Errorf("error %+v", e)
	}
}

func TestStageCustomLevelName(t *testing.T) {
	lv := levelDat(t, singleplayerLevel("My World", "1.21.4", 4189))
	in := inspect(t, Limits{}, upload(t, "My World.zip", zipBytes(t, legacyWorldFiles(lv, true, true))))
	p, got := stageTo(t, in, Target{Type: TypePaper, MinecraftVersion: "1.21.4", LevelName: "survival"}, Options{})
	if !equalLists(p.Folders, []string{"survival", "survival_nether", "survival_the_end"}) || got["survival_the_end/level.dat"] != lv ||
		got["survival/region/r.0.0.mca"] != regionData+"overworld" || p.Warnings[0].Params["levelName"] != "survival" {
		t.Errorf("folders %v, warnings %+v", p.Folders, p.Warnings)
	}
}

func TestPlanRefusesTwoFilesForOnePlace(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	files := []tf{
		f("world/level.dat", lv), f("world/region/r.0.0.mca", regionData), f("world/DIM-1", "not a folder"),
		f("world_nether/level.dat", lv), f("world_nether/DIM-1/region/r.0.0.mca", regionData),
	}
	in := inspect(t, Limits{}, upload(t, "server.zip", zipBytes(t, files)))
	_, err := in.Plan(Target{Type: TypeVanilla, MinecraftVersion: "1.21.4"}, Options{})
	if got := refusalKind(t, err); got != KindDuplicate {
		t.Fatalf("got %s", got)
	}
	var e *Error
	if errors.As(err, &e); e.Params["path"] != "world/DIM-1" {
		t.Errorf("path %v", e.Params["path"])
	}
}

func TestStageModdedServer(t *testing.T) {
	in := inspect(t, Limits{}, upload(t, "fabric.zip", zipBytes(t, fabricServerFiles(t))))

	t.Run("to fabric with its mods", func(t *testing.T) {
		p, got := stageTo(t, in, Target{Type: TypeFabric, MinecraftVersion: "1.21.4"}, Options{KeepAddons: true})
		for _, name := range []string{"mods/fabric-api-0.119.2.jar", "mods/lithium-fabric-0.15.0.jar", "config/lithium.properties", "config/fabric/indigo-renderer.properties", "world/DIM1/region/r.0.0.mca"} {
			if _, ok := got[name]; !ok {
				t.Errorf("%s is missing", name)
			}
		}
		if !equalLists(p.Addons, []string{"config", "mods"}) || !equalLists(kinds(p.Warnings), []string{KindAddonsKept}) {
			t.Errorf("addons %v, warnings %v", p.Addons, kinds(p.Warnings))
		}
		if want := map[string]int{LeftSessionLock: 1, LeftCaches: 1, LeftServerSoftware: 1, LeftServerConfig: 1}; !reflect.DeepEqual(leftOutKinds(p), want) {
			t.Errorf("left out %v", leftOutKinds(p))
		}
	})

	t.Run("to paper", func(t *testing.T) {
		p, got := stageTo(t, in, Target{Type: TypePaper, MinecraftVersion: "1.21.4"}, Options{KeepAddons: true})
		for name := range got {
			if strings.HasPrefix(name, "mods/") || strings.HasPrefix(name, "config/") {
				t.Errorf("%s was staged for a Paper server", name)
			}
		}
		if p.Addons != nil || !hasKind(p.Warnings, KindModded) || !hasKind(p.Warnings, KindBukkitSplit) {
			t.Errorf("addons %v, warnings %v", p.Addons, kinds(p.Warnings))
		}
		if l := leftOutKinds(p); l[LeftAddons] != 2 || l[LeftServerConfig] != 3 {
			t.Errorf("left out %v", l)
		}
	})
}

func TestPlanChecksLimits(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	src := upload(t, "world.zip", zipBytes(t, withPrefix("world/", legacyWorldFiles(lv, false, false))))
	for _, c := range []struct {
		lim  Limits
		want string
	}{
		{Limits{MaxFiles: 3}, KindTooManyFiles},
		{Limits{MaxTotalBytes: 100}, KindTooLarge},
		{Limits{MaxFileBytes: 100}, KindFileTooLarge},
	} {
		p, err := inspect(t, c.lim, src).Plan(Target{Type: TypeVanilla, MinecraftVersion: "1.21.4"}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !equalLists(kinds(p.Problems), []string{c.want}) {
			t.Errorf("%+v: problems %v, want %s", c.lim, kinds(p.Problems), c.want)
		}
	}
}

func TestPlanRefusesZipBombs(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	zeros := func(n int) string { return strings.Repeat("\x00", n) }
	target := Target{Type: TypeVanilla, MinecraftVersion: "1.21.4"}

	one := []tf{f("world/level.dat", lv), f("world/region/r.0.0.mca", zeros(2<<20))}
	many := []tf{f("world/level.dat", lv)}
	for i := 0; i < 8; i++ {
		many = append(many, f(fmt.Sprintf("world/region/r.%d.0.mca", i), zeros(512<<10)))
	}
	for name, files := range map[string][]tf{"one file": one, "many files": many} {
		in := inspect(t, Limits{}, upload(t, "world.zip", zipBytes(t, files)))
		_, err := in.Plan(target, Options{})
		if got := refusalKind(t, err); got != KindRatio {
			t.Errorf("%s: got %s", name, got)
		}
	}

	leftOutLog := []tf{f("server.properties", "level-name=world\n"), f("logs/latest.log", zeros(4<<20)), f("world/level.dat", lv), f("world/region/r.0.0.mca", regionData)}
	in := inspect(t, Limits{}, upload(t, "server.zip", zipBytes(t, leftOutLog)))
	if p, err := in.Plan(target, Options{}); err != nil || leftOutKinds(p)[LeftLogs] != 1 {
		t.Errorf("a file that is left out counted: %v", err)
	}
}

// planWorld plans the import of a world folder uploaded on its own.
func planWorld(t *testing.T, files []tf, target Target) *Preview {
	t.Helper()
	in := inspect(t, Limits{}, upload(t, "world.zip", zipBytes(t, withPrefix("world/", files))))
	p, err := in.Plan(target, Options{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return p
}

func TestPlanWarnings(t *testing.T) {
	vanilla := Target{Type: TypeVanilla, MinecraftVersion: "1.21.4"}
	level := func(edit func(c map[string]any)) string {
		c := legacyLevel("world", "1.21.4", 4189)
		edit(c)
		return levelDat(t, c)
	}
	terrain := f("region/r.0.0.mca", regionData)

	t.Run("singleplayer player in level.dat", func(t *testing.T) {
		p := planWorld(t, []tf{f("level.dat", levelDat(t, singleplayerLevel("world", "1.21.4", 4189))), terrain}, vanilla)
		if !hasKind(p.Warnings, KindOwnerData) {
			t.Errorf("warnings %v", kinds(p.Warnings))
		}
	})
	t.Run("offline-mode players", func(t *testing.T) {
		p := planWorld(t, []tf{f("level.dat", level(func(map[string]any) {})), terrain,
			f("playerdata/"+uuidOffline+".dat", "offline"), f("playerdata/"+uuidOnline+".dat", "online")}, vanilla)
		i := indexOfKind(p.Warnings, KindOfflinePlayers)
		if i < 0 || p.Warnings[i].Params["count"] != 1 || p.Players != 2 {
			t.Errorf("warnings %+v", p.Warnings)
		}
	})
	t.Run("mods", func(t *testing.T) {
		files := []tf{f("level.dat", level(func(c map[string]any) { c["ServerBrands"] = stringList("vanilla", "forge") })), terrain}
		p := planWorld(t, files, vanilla)
		i := indexOfKind(p.Warnings, KindModded)
		if i < 0 || !reflect.DeepEqual(p.Warnings[i].Params["brands"], []string{"forge"}) {
			t.Errorf("warnings %+v", p.Warnings)
		}
		if p := planWorld(t, files, Target{Type: TypeForge, MinecraftVersion: "1.21.4"}); hasKind(p.Warnings, KindModded) {
			t.Error("warned about mods on a server with the same mod loader")
		}
	})
	t.Run("experimental features", func(t *testing.T) {
		p := planWorld(t, []tf{f("level.dat", level(func(c map[string]any) {
			c["enabled_features"] = stringList("minecraft:vanilla", "minecraft:trade_rebalance")
		})), terrain}, vanilla)
		i := indexOfKind(p.Warnings, KindExperimental)
		if i < 0 || !reflect.DeepEqual(p.Warnings[i].Params["features"], []string{"minecraft:trade_rebalance"}) {
			t.Errorf("warnings %+v", p.Warnings)
		}
	})
	t.Run("hardcore", func(t *testing.T) {
		p := planWorld(t, []tf{f("level.dat", level(func(c map[string]any) { c["hardcore"] = int8(1) })), terrain}, vanilla)
		if !hasKind(p.Warnings, KindHardcore) || p.Settings[3] != (Setting{Key: "hardcore", Value: "true", Source: SourceLevel}) {
			t.Errorf("warnings %v, settings %+v", kinds(p.Warnings), p.Settings)
		}
	})
	t.Run("custom dimensions", func(t *testing.T) {
		p := planWorld(t, []tf{f("level.dat", level(func(map[string]any) {})), terrain,
			f("dimensions/mymod/mining/region/r.0.0.mca", regionData)}, vanilla)
		i := indexOfKind(p.Warnings, KindCustomDimensions)
		if i < 0 || !reflect.DeepEqual(p.Warnings[i].Params["dimensions"], []string{"mymod:mining"}) || len(p.Dimensions) != 2 {
			t.Errorf("warnings %+v, dimensions %+v", p.Warnings, p.Dimensions)
		}
	})
	t.Run("no terrain", func(t *testing.T) {
		p := planWorld(t, []tf{f("level.dat", level(func(map[string]any) {}))}, vanilla)
		if !equalLists(kinds(p.Warnings), []string{KindNoTerrain}) {
			t.Errorf("warnings %v", kinds(p.Warnings))
		}
	})
	t.Run("older copy of the Overworld", func(t *testing.T) {
		files := append(modernWorldFiles(t), f("region/r.0.0.mca", "old"), f("entities/r.0.0.mca", "old"))
		p := planWorld(t, files, Target{Type: TypeVanilla, MinecraftVersion: "26.2"})
		i := indexOfKind(p.Warnings, KindStaleDimension)
		if i < 0 || p.Warnings[i].Params["kept"] != "world/dimensions/minecraft/overworld" || p.Warnings[i].Params["dropped"] != "world/region" ||
			leftOutKinds(p)[LeftStaleCopies] != 2 || !p.OK() {
			t.Errorf("warnings %+v, left out %v", p.Warnings, leftOutKinds(p))
		}
	})
	t.Run("level.dat_old", func(t *testing.T) {
		p := planWorld(t, []tf{f("level.dat", "garbage"), f("level.dat_old", level(func(map[string]any) {})), terrain}, vanilla)
		if !hasKind(p.Warnings, KindLevelBackup) || !p.OK() {
			t.Errorf("warnings %v, problems %v", kinds(p.Warnings), kinds(p.Problems))
		}
	})
	t.Run("unreadable level.dat", func(t *testing.T) {
		p := planWorld(t, []tf{f("level.dat", "garbage"), terrain}, vanilla)
		if !equalLists(kinds(p.Problems), []string{KindLevelUnreadable}) || p.Problems[0].Params["detail"] != "level.dat is damaged or incomplete." {
			t.Errorf("problems %+v", p.Problems)
		}
	})
}

func TestStageKeepsTheCurrentCopyOfADimension(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	files := []tf{
		f("world/level.dat", lv), f("world/region/r.0.0.mca", regionData), f("world/DIM-1/region/r.0.0.mca", "old nether"),
		f("world_nether/level.dat", lv), f("world_nether/DIM-1/region/r.0.0.mca", "new nether"),
	}
	in := inspect(t, Limits{}, upload(t, "server.zip", zipBytes(t, files)))
	for _, c := range []struct {
		target Target
		nether string
	}{
		{Target{Type: TypeVanilla, MinecraftVersion: "1.21.4"}, "world/DIM-1/region/r.0.0.mca"},
		{Target{Type: TypePaper, MinecraftVersion: "1.21.4"}, "world_nether/DIM-1/region/r.0.0.mca"},
	} {
		p, got := stageTo(t, in, c.target, Options{})
		i := indexOfKind(p.Warnings, KindStaleDimension)
		if got[c.nether] != "new nether" || i < 0 || p.Warnings[i].Params["kept"] != "world_nether" || p.Warnings[i].Params["dropped"] != "world/DIM-1" {
			t.Errorf("%s: staged %v, warnings %+v", c.target.Type, sortedKeys(got), p.Warnings)
		}
	}
}

func indexOfKind(ms []Message, kind string) int {
	for i, m := range ms {
		if m.Kind == kind {
			return i
		}
	}
	return -1
}

func TestPlanChecksTarget(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	in := inspect(t, Limits{}, upload(t, "world.zip", zipBytes(t, []tf{f("world/level.dat", lv)})))
	paper := func(level string) Target {
		return Target{Type: TypePaper, MinecraftVersion: "1.21.4", LevelName: level}
	}
	cases := []struct {
		target Target
		want   string
	}{
		{Target{Type: "spigot", MinecraftVersion: "1.21.4"}, KindTargetType},
		{Target{Type: "craftbukkit", MinecraftVersion: "1.21.4"}, KindTargetType},
		{Target{Type: "", MinecraftVersion: "1.21.4"}, KindTargetType},
		{Target{Type: TypePaper}, KindTargetVersion},
		{Target{Type: TypePaper, MinecraftVersion: "latest"}, KindTargetVersion},
		{Target{Type: TypePaper, MinecraftVersion: "1.21.4; stop"}, KindTargetVersion},
		{paper("../world"), KindTargetLevelName},
		{paper(".."), KindTargetLevelName},
		{paper("worlds/main"), KindTargetLevelName},
		{paper(`worlds\main`), KindTargetLevelName},
		{paper(" world"), KindTargetLevelName},
		{paper("plugins"), KindTargetLevelName},
		{paper("Server.Properties"), KindTargetLevelName},
		{paper("wor\u202eld"), KindTargetLevelName},
	}
	for _, c := range cases {
		_, err := in.Plan(c.target, Options{})
		if got := refusalKind(t, err); got != c.want {
			t.Errorf("%+v: got %s, want %s", c.target, got, c.want)
		}
	}
	if _, err := in.Plan(paper("My World"), Options{}); err != nil {
		t.Errorf("level-name with a space: %v", err)
	}
}

func TestStageNoticesChangedUploads(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	files := withPrefix("world/", legacyWorldFiles(lv, false, false))
	renamed := append([]tf(nil), files...)
	for i := range renamed {
		if renamed[i].name == "world/region/r.0.0.mca" {
			renamed[i].name = "world/region/r.0.1.mca"
		}
	}
	target := Target{Type: TypeVanilla, MinecraftVersion: "1.21.4"}
	formats := map[string]func(t *testing.T, files []tf) []byte{
		"world.zip": zipBytes,
		"world.tar": tarBytes,
		"world.tgz": func(t *testing.T, files []tf) []byte { return gzipBytes(t, tarBytes(t, files)) },
	}
	for name, build := range formats {
		changes := map[string]func(t *testing.T, src Source){
			"other file": func(t *testing.T, src Source) {
				if err := os.WriteFile(src.Path, build(t, files[:3]), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			"deleted": func(t *testing.T, src Source) {
				if err := os.Remove(src.Path); err != nil {
					t.Fatal(err)
				}
			},
		}
		if name != "world.tgz" {
			// Renaming an entry keeps a zip or tar the same size, so with
			// the old modification time only reading it again shows the
			// change; compressing the tar may change its size.
			changes["renamed entry"] = func(t *testing.T, src Source) {
				fi, err := os.Stat(src.Path)
				if err != nil {
					t.Fatal(err)
				}
				b := build(t, renamed)
				if int64(len(b)) != fi.Size() {
					t.Fatalf("the changed archive has %d bytes, the original %d", len(b), fi.Size())
				}
				if err := os.WriteFile(src.Path, b, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(src.Path, fi.ModTime(), fi.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
		}
		for change, apply := range changes {
			t.Run(name+" "+change, func(t *testing.T) {
				src := upload(t, name, build(t, files))
				in := inspect(t, Limits{}, src)
				apply(t, src)
				dir := filepath.Join(t.TempDir(), "staging")
				_, err := in.Stage(context.Background(), dir, target, Options{})
				if got := refusalKind(t, err); got != KindArchiveChanged {
					t.Fatalf("got %s (%v)", got, err)
				}
				if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("Stage left the staging directory behind: %v", err)
				}
			})
		}
	}
}

func TestStageNeedsANewDirectory(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	in := inspect(t, Limits{}, upload(t, "world.zip", zipBytes(t, withPrefix("world/", legacyWorldFiles(lv, false, false)))))
	dir := t.TempDir()
	keep := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Stage(context.Background(), dir, Target{Type: TypeVanilla, MinecraftVersion: "1.21.4"}, Options{}); !errors.Is(err, fs.ErrExist) {
		t.Errorf("got %v, want fs.ErrExist", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("Stage touched an existing directory: %v", err)
	}
}

func TestStageStopsWhenCancelled(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	files := withPrefix("world/", legacyWorldFiles(lv, false, false))
	for _, src := range []Source{
		upload(t, "world.zip", zipBytes(t, files)),
		upload(t, "world.tar.gz", gzipBytes(t, tarBytes(t, files))),
	} {
		in := inspect(t, Limits{}, src)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		dir := filepath.Join(t.TempDir(), "staging")
		if _, err := in.Stage(ctx, dir, Target{Type: TypeVanilla, MinecraftVersion: "1.21.4"}, Options{}); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: got %v, want context.Canceled", src.Name, err)
		}
		if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: Stage left the staging directory behind: %v", src.Name, err)
		}
	}
}

func TestPreviewJSON(t *testing.T) {
	src := upload(t, "server.zip", zipBytes(t, paperServerFiles(t)))
	in := inspect(t, Limits{}, src)
	p, err := in.Plan(Target{Type: TypeVanilla, MinecraftVersion: "1.21.11"}, Options{KeepOperators: true})
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{"inspection": in, "preview": p} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if s := string(b); strings.Contains(s, "hunter2") || strings.Contains(s, src.Path) {
			t.Errorf("%s JSON has a secret or a path on disk: %s", name, s)
		}
	}
	b, _ := json.Marshal(p)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"world", "target", "version", "folders", "files", "fileCount", "sizeBytes", "dimensions",
		"players", "operators", "settings", "refusedSettings", "ignoredSettings", "leftOut", "warnings"} {
		if _, ok := m[key]; !ok {
			t.Errorf("preview JSON has no %q", key)
		}
	}
}
