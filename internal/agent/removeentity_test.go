package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/nbt"
	"github.com/CIYAhq/playkeeper/internal/region"
)

// tickingReport is a crash report as Minecraft 26.3 writes one for an
// entity, or a block entity, that threw while it was ticked.
func tickingReport(block bool, id string, x, y, z int, dimension string) string {
	var b strings.Builder
	b.WriteString("---- Minecraft Crash Report ----\n// Oops.\n\nTime: 2026-09-29 17:42:52\n")
	if block {
		fmt.Fprintf(&b, "Description: Ticking block entity\n\njava.lang.NullPointerException: Test error\n\tat net.minecraft.world.level.block.entity.HopperBlockEntity.pushItemsTick(HopperBlockEntity.java)\n\n")
		fmt.Fprintf(&b, "-- Block entity being ticked --\nDetails:\n\tName: %s // net.minecraft.world.level.block.entity.HopperBlockEntity\n\tBlock location: World: (%d,%d,%d), Section: (at 10,8,10 in 0,7,0; chunk contains blocks 0,-64,0 to 15,319,15), Region: (0,0; contains chunks 0,0 to 31,31, blocks 0,-64,0 to 511,319,511)\nStacktrace:\n", id, x, y, z)
	} else {
		fmt.Fprintf(&b, "Description: Ticking entity\n\njava.lang.NullPointerException: Test error\n\tat net.minecraft.server.level.ServerLevel.tickNonPassenger(ServerLevel.java)\n\n")
		fmt.Fprintf(&b, "-- Entity being ticked --\nDetails:\n\tEntity Type: %s (net.minecraft.world.entity.vehicle.minecart.Minecart)\n\tEntity ID: 10\n\tEntity Name: Minecart\n\tEntity's Exact location: %d.50, %d.00, %d.50\n\tEntity's Block location: World: (%d,%d,%d), Section: (at 6,8,6 in 0,7,0; chunk contains blocks 0,-64,0 to 15,319,15), Region: (0,0; contains chunks 0,0 to 31,31, blocks 0,-64,0 to 511,319,511)\nStacktrace:\n", id, x, y, z, x, y, z)
	}
	fmt.Fprintf(&b, "\n-- Affected level --\nDetails:\n\tAll players: 0 total; \n\tLevel dimension: %s\n\tLevel name: world\nStacktrace:\n", dimension)
	return b.String()
}

// tick crashes the server over what the report names, with the lines
// Minecraft printed.
func (e *agentEnv) tick(report string) *api.Crash {
	e.t.Helper()
	writeGameFile(e.t, filepath.Join(e.dataDir(), "crash-reports", "crash-2026-09-29_17.42.52-server.txt"), report, time.Time{})
	what := "entity"
	if strings.Contains(report, "Ticking block entity") {
		what = "block entity"
	}
	for _, l := range []string{
		"[17:42:52] [Server thread/ERROR]: Encountered an unexpected exception",
		"net.minecraft.ReportedException: Ticking " + what,
		"Caused by: java.lang.NullPointerException: Test error",
		"[17:42:52] [Server thread/ERROR]: This crash report has been saved to: /data/crash-reports/crash-2026-09-29_17.42.52-server.txt",
		"[17:42:52] [Server thread/INFO]: Stopping server",
	} {
		e.fd.addLog(l)
	}
	e.fd.crash(1)
	return e.waitCrash()
}

// regionFile writes a region file of the world, with chunk 0,0.
func (e *agentEnv) regionFile(rel string, chunk nbt.Compound) {
	e.t.Helper()
	f, _ := region.Parse(nil)
	data, err := region.Encode(chunk)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := f.SetChunk(0, 0, region.Zlib, data, time.Now()); err != nil {
		e.t.Fatal(err)
	}
	writeGameFile(e.t, filepath.Join(e.dataDir(), rel), string(f.Bytes()), time.Time{})
}

func (e *agentEnv) regionChunk(rel string) nbt.Compound {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dataDir(), rel))
	if err != nil {
		e.t.Fatal(err)
	}
	f, err := region.Parse(b)
	if err != nil {
		e.t.Fatal(err)
	}
	_, data, err := f.Chunk(0, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	c, err := region.Decode(data)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func entityIDs(c nbt.Compound, key string) string {
	var out []string
	l, _ := c[key].(nbt.List)
	for _, it := range l.Items {
		id, _ := it.(nbt.Compound).String("id")
		out = append(out, id)
	}
	return strings.Join(out, " ")
}

func pos(x, y, z float64) nbt.List {
	return nbt.List{Type: nbt.TagDouble, Items: []any{x, y, z}}
}

func (e *agentEnv) removeFix(c *api.Crash) map[string]any {
	e.t.Helper()
	for _, f := range c.Fixes {
		if f.Kind == "remove_entity" {
			return f.Params
		}
	}
	return nil
}

const entities26 = "world/dimensions/minecraft/overworld/entities/r.0.0.mca"

// A minecart that throws each time it's ticked is named with where it is,
// and the fix takes just it out of the world: nothing happens until the
// owner asks, the world is backed up first, and the cow in the same chunk
// stays. No plain restart is offered: it would run the minecart again.
func TestRemovingWhatCrashedTakesOnlyThatOneOutOfTheWorld(t *testing.T) {
	e := crashEnv(t)
	e.regionFile(entities26, nbt.Compound{"DataVersion": int32(5023), "Entities": nbt.List{Type: nbt.TagCompound, Items: []any{
		nbt.Compound{"id": "minecraft:cow", "Pos": pos(3.5, 120, 3.5), "CustomName": "Bessie"},
		nbt.Compound{"id": "minecraft:minecart", "Pos": pos(6.5, 120, 6.5), "Tags": nbt.List{Type: nbt.TagString, Items: []any{"pk_crash"}}},
	}}})
	c := e.tick(tickingReport(false, "minecraft:minecart", 6, 120, 6, "minecraft:overworld"))
	fix := e.removeFix(c)
	if c.Kind != "ticking_entity" || fix == nil || !c.Fixes[0].Recommended {
		t.Fatalf("got %s with fixes %+v", c.Kind, c.Fixes)
	}
	for _, f := range c.Fixes {
		if f.Kind == "restart" {
			t.Errorf("a plain restart is offered: %+v", c.Fixes)
		}
	}
	if entityIDs(e.regionChunk(entities26), "Entities") != "minecraft:cow minecraft:minecart" {
		t.Fatal("the world changed before anyone asked")
	}

	body := map[string]any{"actor": "admin", "start": true}
	for k, v := range fix {
		body[k] = v
	}
	code, out := e.callWhenFree("POST", e.sp("/world/remove-entity"), body)
	if code != 202 {
		t.Fatalf("remove: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded || op.Kind != "remove-entity" || op.Detail["file"] != entities26 {
		t.Fatalf("remove: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)
	if got := entityIDs(e.regionChunk(entities26), "Entities"); got != "minecraft:cow" {
		t.Errorf("entities left: %q, want the cow", got)
	}
	before, _ := e.srv().listBackups(`note = 'Before removing the minecart at 6, 120, 6'`)
	if len(before) != 1 || before[0].Verified == nil || !*before[0].Verified {
		t.Fatalf("want one checked backup made first, got %+v", before)
	}
	if !e.auditHas("world.entity_removed", "succeeded", "minecart at 6, 120, 6") {
		t.Error("no audit entry")
	}
	if st := e.status(); st.Crash != nil {
		t.Errorf("the crash is still shown: %+v", st.Crash)
	}
}

// A block entity's data goes from the chunk it's saved with, in the world
// folder before Minecraft 26.1; the chest beside it keeps its diamonds.
// Paper's own Nether folder is where a Paper world keeps the Nether.
func TestRemovingWhatCrashedFindsItInEachLayout(t *testing.T) {
	e := crashEnv(t)
	e.regionFile("world/region/r.0.0.mca", nbt.Compound{"DataVersion": int32(4671), "Status": "minecraft:full", "block_entities": nbt.List{Type: nbt.TagCompound, Items: []any{
		nbt.Compound{"id": "minecraft:chest", "x": int32(1), "y": int32(120), "z": int32(1), "Items": nbt.List{Type: nbt.TagCompound, Items: []any{nbt.Compound{"id": "minecraft:diamond", "count": int32(5), "Slot": int8(0)}}}},
		nbt.Compound{"id": "minecraft:hopper", "x": int32(10), "y": int32(120), "z": int32(10)},
	}}})
	c := e.tick(tickingReport(true, "minecraft:hopper", 10, 120, 10, "minecraft:overworld"))
	fix := e.removeFix(c)
	if fix == nil || fix["what"] != "block_entity" {
		t.Fatalf("fixes %+v", c.Fixes)
	}
	body := map[string]any{"actor": "admin"}
	for k, v := range fix {
		body[k] = v
	}
	code, out := e.callWhenFree("POST", e.sp("/world/remove-entity"), body)
	if code != 202 {
		t.Fatalf("remove: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded || op.Detail["file"] != "world/region/r.0.0.mca" {
		t.Fatalf("remove: %+v", op)
	}
	chunk := e.regionChunk("world/region/r.0.0.mca")
	if got := entityIDs(chunk, "block_entities"); got != "minecraft:chest" || chunk["Status"] != "minecraft:full" {
		t.Errorf("block entities left: %q (%v)", got, chunk["Status"])
	}

	nether := "world_nether/DIM-1/entities/r.0.0.mca"
	e.regionFile(nether, nbt.Compound{"DataVersion": int32(4671), "Entities": nbt.List{Type: nbt.TagCompound, Items: []any{
		nbt.Compound{"id": "minecraft:minecart", "Pos": pos(6.5, 70, 6.5)},
	}}})
	if op := e.runOp("POST", "/start"); op.Status != api.OpSucceeded {
		t.Fatalf("start: %+v", op)
	}
	fix = e.removeFix(e.tick(tickingReport(false, "minecraft:minecart", 6, 70, 6, "minecraft:the_nether")))
	if fix == nil {
		t.Fatal("no fix for the Nether")
	}
	body = map[string]any{"actor": "admin"}
	for k, v := range fix {
		body[k] = v
	}
	code, out = e.callWhenFree("POST", e.sp("/world/remove-entity"), body)
	if code != 202 {
		t.Fatalf("remove: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded || op.Detail["file"] != nether {
		t.Fatalf("remove from Paper's Nether folder: %+v", op)
	}
	if got := entityIDs(e.regionChunk(nether), "Entities"); got != "" {
		t.Errorf("left in the Nether: %q", got)
	}
}

// Nothing is taken out while the server runs, for a request that isn't one,
// or when it isn't there any more: then no backup is made either. A crash
// whose region file isn't in the world offers no fix that can't work.
func TestRemovingWhatCrashedIsRefusedWhereItCantHelp(t *testing.T) {
	e := crashEnv(t)
	e.regionFile(entities26, nbt.Compound{"DataVersion": int32(5023), "Entities": nbt.List{Type: nbt.TagCompound, Items: []any{
		nbt.Compound{"id": "minecraft:cow", "Pos": pos(3.5, 120, 3.5)},
	}}})
	remove := func(over map[string]any) (int, map[string]any) {
		body := map[string]any{"actor": "admin", "what": "entity", "type": "minecraft:minecart", "dimension": "minecraft:overworld", "x": 6, "y": 120, "z": 6, "pos": []float64{6.5, 120, 6.5}}
		for k, v := range over {
			body[k] = v
		}
		return e.callWhenFree("POST", e.sp("/world/remove-entity"), body)
	}
	if code, out := remove(nil); code != 409 || !strings.Contains(out["error"].(string), "is running") {
		t.Fatalf("while running: %d %v", code, out)
	}
	e.fd.crash(1)
	e.waitCrash()
	for _, bad := range []map[string]any{
		{"what": "player"}, {"type": "Minecart"}, {"dimension": "minecraft:../../etc"}, {"dimension": "../x:y"},
		{"x": 40_000_000}, {"y": 99999}, {"pos": []float64{7.5, 120, 6.5}}, {"pos": []float64{6.5, 120}},
	} {
		if code, out := remove(bad); code != 400 {
			t.Errorf("%v: %d %v", bad, code, out)
		}
	}
	if code, out := remove(nil); code != 409 || !strings.Contains(out["error"].(string), "isn't in") {
		t.Errorf("a minecart that isn't there: %d %v", code, out)
	}
	if code, out := remove(map[string]any{"dimension": "minecraft:the_end"}); code != 409 || !strings.Contains(out["error"].(string), "found no entities file") {
		t.Errorf("a dimension without the file: %d %v", code, out)
	}
	if list, _ := e.srv().listBackups(``); len(list) != 0 {
		t.Errorf("a backup was made although nothing changed: %d", len(list))
	}

	if op := e.runOp("POST", "/start"); op.Status != api.OpSucceeded {
		t.Fatalf("start: %+v", op)
	}
	c := e.tick(tickingReport(false, "minecraft:minecart", 600, 70, 600, "minecraft:overworld"))
	if e.removeFix(c) != nil {
		t.Errorf("a fix whose region file isn't there: %+v", c.Fixes)
	}
}
