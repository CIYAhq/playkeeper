package agent

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/nbt"
	"github.com/CIYAhq/playkeeper/internal/worldimport"
)

// The lines Minecraft 1.21.11 printed with both level.dat files damaged.
var levelDatCrash = []string{
	"[16:51:20] [ServerMain/WARN]: Failed to load world data from ./world/level.dat",
	"java.util.zip.ZipException: Not in GZIP format",
	"[16:51:20] [ServerMain/INFO]: Attempting to use fallback",
	"[16:51:20] [ServerMain/ERROR]: Failed to load world data from ./world/level.dat_old",
	"java.util.zip.ZipException: Not in GZIP format",
	"[16:51:20] [ServerMain/ERROR]: Failed to load world data from ./world/level.dat and ./world/level.dat_old. World files may be corrupted. Shutting down.",
}

// damageLevelDat breaks both of the world's level.dat files and crashes the
// server the way Minecraft stops over them.
func (e *agentEnv) damageLevelDat() *api.Crash {
	e.t.Helper()
	world := filepath.Join(e.dataDir(), "world")
	writeGameFile(e.t, filepath.Join(world, "level.dat"), "\x1f\x8b not the rest of a gzip stream", time.Time{})
	writeGameFile(e.t, filepath.Join(world, "level.dat_old"), "random bytes", time.Time{})
	for _, l := range levelDatCrash {
		e.fd.addLog(l)
	}
	e.fd.crash(0)
	return e.waitCrash()
}

func (e *agentEnv) rebuildFix(c *api.Crash) *api.DiagnosisAction {
	e.t.Helper()
	for i := range c.Fixes {
		if c.Fixes[i].Kind == "rebuild_level" {
			return &c.Fixes[i]
		}
	}
	return nil
}

func (e *agentEnv) properties() string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dataDir(), "server.properties"))
	if err != nil && !os.IsNotExist(err) {
		e.t.Fatal(err)
	}
	return string(b)
}

// gzipNBT gzips a saved data file the way Minecraft writes it.
func gzipNBT(t *testing.T, root nbt.Compound) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := nbt.Write(zw, "", root); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// A world saved before Minecraft 26.1 keeps its seed only in level.dat, so a
// new level.dat takes it from the newest checked backup. Nothing happens
// until the owner asks: then the world is backed up first, the seed goes
// into server.properties, both files go, and Minecraft makes a new level.dat
// with every other file of the world as it was.
func TestANewLevelDatKeepsTheWorldAndTakesItsSeedFromABackup(t *testing.T) {
	e := crashEnv(t)
	world := filepath.Join(e.dataDir(), "world")
	writeGameFile(t, filepath.Join(world, "level.dat"), levelDat(t, legacyLevelData("world", "1.21.11", 4671)), time.Time{})
	writeGameFile(t, filepath.Join(world, "region", "r.0.0.mca"), "the builds", time.Time{})
	writeGameFile(t, filepath.Join(world, "data", "world_border.dat"), "a 5,000-block border", time.Time{})
	code, out := e.callWhenFree("POST", e.sp("/backups"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("backup: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("backup: %+v", op)
	}
	e.waitFor("online after the backup", e.onlineIdle)

	c := e.damageLevelDat()
	fix := e.rebuildFix(c)
	if c.Kind != "corrupt_world" || fix == nil || fix.Recommended || len(c.Fixes) != 2 || c.Fixes[0].Kind != "restore_backup" || !c.Fixes[0].Recommended {
		t.Fatalf("got %s with fixes %+v", c.Kind, c.Fixes)
	}
	if fix.Params["world"] != "world" || fix.Params["seed_from"] != "backup" || strings.Join(stringsOf(fix.Params["resets"]), ",") != "game_rules,time,spawn" {
		t.Fatalf("the new level.dat's fix says %v", fix.Params)
	}
	if strings.Contains(e.properties(), "level-seed=-") {
		t.Fatal("the seed was written before anyone asked")
	}

	code, out = e.callWhenFree("POST", e.sp("/world/rebuild-level"), map[string]any{"actor": "admin", "start": true})
	if code != 202 {
		t.Fatalf("rebuild: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded || op.Kind != "rebuild-level" || op.Detail["seedFrom"] != "backup" {
		t.Fatalf("rebuild: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)
	if !strings.Contains(e.properties(), "level-seed=-4172144997902289642\n") {
		t.Errorf("server.properties has no seed:\n%s", e.properties())
	}
	if got := readFile(t, filepath.Join(world, "level.dat")); got != "generated world" {
		t.Errorf("level.dat is %q, want the one the server made", got)
	}
	if exists(filepath.Join(world, "level.dat_old")) {
		t.Error("the damaged level.dat_old is still there")
	}
	if readFile(t, filepath.Join(world, "region", "r.0.0.mca")) != "the builds" {
		t.Error("the world's builds changed")
	}
	before, _ := e.srv().listBackups(`note = 'Before a new level.dat'`)
	if len(before) != 1 || before[0].Verified == nil || !*before[0].Verified {
		t.Fatalf("want one checked backup made first, got %+v", before)
	}
	if !archiveHas(t, filepath.Join(e.cfg.BackupsDir(), before[0].FileName), "world/level.dat_old") {
		t.Error("the backup made first doesn't hold the world as it was")
	}
	if !e.auditHas("world.level_rebuilt", "succeeded", "seed from backup") {
		t.Error("no audit entry")
	}
}

// Since Minecraft 26.1 the seed, the game rules and the time live in files
// of their own. A new level.dat takes the seed from world_gen_settings.dat,
// which a new world would otherwise replace, and only the spawn point
// starts over. Without a backup to restore, it's the fix.
func TestANewLevelDatTakesTheSeedFromTheWorldSince26(t *testing.T) {
	e := crashEnv(t)
	world := filepath.Join(e.dataDir(), "world")
	data := filepath.Join(world, "data", "minecraft")
	writeGameFile(t, filepath.Join(data, "game_rules.dat"), "rules", time.Time{})
	writeGameFile(t, filepath.Join(data, "world_clocks.dat"), "clocks", time.Time{})
	writeGameFile(t, filepath.Join(data, "world_gen_settings.dat"), gzipNBT(t, nbt.Compound{"DataVersion": int32(5023), "data": nbt.Compound{"seed": int64(-269618914698903788)}}), time.Time{})
	writeGameFile(t, filepath.Join(world, "dimensions", "minecraft", "overworld", "data", "minecraft", "world_border.dat"), "border", time.Time{})
	writeGameFile(t, filepath.Join(e.dataDir(), "server.properties"), "level-name=world\nlevel-seed=12345\nmotd=Survival\n", time.Time{})

	c := e.damageLevelDat()
	fix := e.rebuildFix(c)
	if fix == nil || !fix.Recommended || len(c.Fixes) != 1 {
		t.Fatalf("without a backup, want the new level.dat as the fix: %+v", c.Fixes)
	}
	if fix.Params["seed_from"] != "world" || strings.Join(stringsOf(fix.Params["resets"]), ",") != "spawn" {
		t.Fatalf("the fix says %v", fix.Params)
	}
	code, out := e.callWhenFree("POST", e.sp("/world/rebuild-level"), map[string]any{"actor": "admin", "world": "world"})
	if code != 202 {
		t.Fatalf("rebuild: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded || op.Detail["seedFrom"] != "world" {
		t.Fatalf("rebuild: %+v", op)
	}
	if got := e.properties(); got != "level-name=world\nlevel-seed=-269618914698903788\nmotd=Survival\n" {
		t.Errorf("server.properties:\n%s", got)
	}
	for _, f := range []string{"level.dat", "level.dat_old"} {
		if exists(filepath.Join(world, f)) {
			t.Errorf("%s is still there", f)
		}
	}
	if readFile(t, filepath.Join(data, "game_rules.dat")) != "rules" {
		t.Error("the game rules changed")
	}
	if st := e.status(); st.Phase == api.PhaseOnline {
		t.Error("a rebuild that wasn't asked to start started the server")
	}
	if n, _ := e.srv().listBackups(``); len(n) != 1 {
		t.Errorf("want the backup made first, got %d backups", len(n))
	}
}

// A new level.dat is refused while the server runs, for a world whose
// level.dat can still be read and for a folder that isn't a world; and when
// the seed was to come from a backup that no longer gives it, nothing
// changes and no backup is made.
func TestANewLevelDatIsRefusedWhereItDoesnHelp(t *testing.T) {
	e := crashEnv(t)
	world := filepath.Join(e.dataDir(), "world")
	rebuild := func(body map[string]any) (int, map[string]any) {
		body["actor"] = "admin"
		return e.callWhenFree("POST", e.sp("/world/rebuild-level"), body)
	}
	// A checked backup whose level.dat was damaged already.
	writeGameFile(t, filepath.Join(world, "level.dat"), "damaged before the backup", time.Time{})
	code, out := e.callWhenFree("POST", e.sp("/backups"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("backup: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("backup: %+v", op)
	}
	e.waitFor("online after the backup", e.onlineIdle)
	if code, out := rebuild(map[string]any{}); code != 409 || !strings.Contains(out["error"].(string), "is running") {
		t.Fatalf("while running: %d %v", code, out)
	}

	writeGameFile(t, filepath.Join(world, "level.dat"), levelDat(t, legacyLevelData("world", "1.21.11", 4671)), time.Time{})
	e.fd.crash(1)
	e.waitCrash()
	if code, out := rebuild(map[string]any{}); code != 409 || !strings.Contains(out["error"].(string), "world/level.dat can be read") {
		t.Fatalf("with a readable level.dat: %d %v", code, out)
	}
	for _, w := range []string{"plugins", "../world", "nope"} {
		if code, out := rebuild(map[string]any{"world": w}); code != 400 {
			t.Errorf("world %q: %d %v", w, code, out)
		}
	}

	if op := e.runOp("POST", "/start"); op.Status != api.OpSucceeded {
		t.Fatalf("start: %+v", op)
	}
	if fix := e.rebuildFix(e.damageLevelDat()); fix == nil || fix.Params["seed_from"] != "backup" {
		t.Fatalf("fix: %+v", fix)
	}
	code, out = rebuild(map[string]any{"start": true})
	if code != 202 {
		t.Fatalf("rebuild: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpFailed || !strings.Contains(op.Error, "couldn't read the world's seed") {
		t.Fatalf("a seed the backups don't give: %+v", op)
	}
	if !exists(filepath.Join(world, "level.dat")) || !exists(filepath.Join(world, "level.dat_old")) {
		t.Error("files were deleted although nothing was to change")
	}
	if list, _ := e.srv().listBackups(``); len(list) != 1 {
		t.Errorf("a backup was made although nothing changed: %d backups", len(list))
	}
	if worldimport.PropertiesSeed([]byte(e.properties())) != "" {
		t.Error("a seed was written")
	}
}

func stringsOf(v any) []string {
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		out := make([]string, 0, len(l))
		for _, x := range l {
			s, _ := x.(string)
			out = append(out, s)
		}
		return out
	}
	return nil
}
