package region

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/nbt"
)

var saved = time.Date(2026, 9, 29, 17, 42, 52, 0, time.UTC)

func mob(id string, x, y, z float64, extra nbt.Compound) nbt.Compound {
	e := nbt.Compound{"id": id, "Pos": nbt.List{Type: nbt.TagDouble, Items: []any{x, y, z}}}
	for k, v := range extra {
		e[k] = v
	}
	return e
}

func entities(items ...any) nbt.Compound {
	return nbt.Compound{"DataVersion": int32(5023), "Position": []int32{0, 0}, "Entities": nbt.List{Type: nbt.TagCompound, Items: items}}
}

func ids(t *testing.T, chunk nbt.Compound) string {
	t.Helper()
	var out []string
	var walk func(l nbt.List, indent string)
	walk = func(l nbt.List, indent string) {
		for _, it := range l.Items {
			e := it.(nbt.Compound)
			id, _ := e.String("id")
			out = append(out, indent+id)
			if r, ok := e["Passengers"].(nbt.List); ok {
				walk(r, indent+">")
			}
		}
	}
	walk(chunk["Entities"].(nbt.List), "")
	return strings.Join(out, " ")
}

func file(t *testing.T, chunks map[[2]int]nbt.Compound) *File {
	t.Helper()
	f, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	for at, c := range chunks {
		data, err := Encode(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.SetChunk(at[0], at[1], Zlib, data, saved); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func chunkOf(t *testing.T, f *File, cx, cz int) nbt.Compound {
	t.Helper()
	_, data, err := f.Chunk(cx, cz)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A chunk written reads back the same, in whichever region file slot its
// world coordinates give it, negative ones too, with the time it was saved.
func TestChunksReadBackAsWritten(t *testing.T) {
	f := file(t, map[[2]int]nbt.Compound{{0, 0}: entities(mob("minecraft:cow", 3.5, 120, 3.5, nil)), {-1, -1}: entities(mob("minecraft:pig", -3.5, 70, -3.5, nil))})
	if got := ids(t, chunkOf(t, f, 0, 0)); got != "minecraft:cow" {
		t.Errorf("chunk 0,0: %s", got)
	}
	if got := ids(t, chunkOf(t, f, -1, -1)); got != "minecraft:pig" {
		t.Errorf("chunk -1,-1: %s", got)
	}
	if got := binary.BigEndian.Uint32(f.Bytes()[sector+4*1023:]); int64(got) != saved.Unix() {
		t.Errorf("chunk -1,-1 saved at %d", got)
	}
	if _, _, err := f.Chunk(5, 5); !errors.Is(err, ErrNoChunk) {
		t.Errorf("a chunk never saved: %v", err)
	}
	if len(f.Bytes())%sector != 0 {
		t.Errorf("the file is %d bytes, not whole sectors", len(f.Bytes()))
	}
}

// Only the entity in the block and at the exact place a crash report gives
// goes; without the place, every one of that type in the block does. What
// rode it stays, in the chunk's own list, and a rider can go on its own.
func TestRemoveEntityTakesOnlyTheOneNamed(t *testing.T) {
	chunk := entities(
		mob("minecraft:cow", 3.5, 120, 3.5, nbt.Compound{"CustomName": "Bessie"}),
		mob("minecraft:minecart", 6.5, 120, 6.5, nil),
		mob("minecraft:minecart", 6.2, 120, 6.8, nil),
		mob("minecraft:oak_boat", 9.5, 63, 9.5, nbt.Compound{"Passengers": nbt.List{Type: nbt.TagCompound, Items: []any{mob("minecraft:villager", 9.5, 63.5, 9.5, nil)}}}),
	)
	if n := RemoveEntity(chunk, "minecraft:minecart", 6, 120, 6, []float64{6.5, 120, 6.5}); n != 1 {
		t.Fatalf("removed %d", n)
	}
	if got := ids(t, chunk); got != "minecraft:cow minecraft:minecart minecraft:oak_boat >minecraft:villager" {
		t.Errorf("after the minecart: %s", got)
	}
	if n := RemoveEntity(chunk, "minecraft:cow", 6, 120, 6, nil); n != 0 {
		t.Errorf("a cow of another block went: %d", n)
	}
	if n := RemoveEntity(chunk, "minecraft:minecart", 6, 120, 6, nil); n != 1 {
		t.Errorf("without a place, want the other minecart gone, got %d", n)
	}
	if n := RemoveEntity(chunk, "minecraft:oak_boat", 9, 63, 9, nil); n != 1 {
		t.Fatalf("boat: %d", n)
	}
	if got := ids(t, chunk); got != "minecraft:cow minecraft:villager" {
		t.Errorf("the boat's rider should stay: %s", got)
	}
	edge := entities(mob("minecraft:minecart", 6.998, 120, 6.5, nil))
	if n := RemoveEntity(edge, "minecraft:minecart", 6, 120, 6, []float64{7.00, 120, 6.5}); n != 1 {
		t.Errorf("a minecart at 6.998, which a crash report prints as 7.00, stayed: %d", n)
	}
	rider := entities(mob("minecraft:oak_boat", 9.5, 63, 9.5, nbt.Compound{"Passengers": nbt.List{Type: nbt.TagCompound, Items: []any{mob("minecraft:minecart", 9.5, 63.5, 9.5, nil)}}}))
	if n := RemoveEntity(rider, "minecraft:minecart", 9, 63, 9, nil); n != 1 || ids(t, rider) != "minecraft:oak_boat" {
		t.Errorf("a rider on its own: %d, %s", n, ids(t, rider))
	}
}

// A block entity's data goes by its place and id; the block and the chests
// beside it stay.
func TestRemoveBlockEntityTakesOnlyThatBlocksData(t *testing.T) {
	chunk := nbt.Compound{"Status": "minecraft:full", "block_entities": nbt.List{Type: nbt.TagCompound, Items: []any{
		nbt.Compound{"id": "minecraft:chest", "x": int32(1), "y": int32(120), "z": int32(1), "Items": nbt.List{Type: nbt.TagEnd}},
		nbt.Compound{"id": "minecraft:hopper", "x": int32(10), "y": int32(120), "z": int32(10), "CustomName": "pk_crash"},
	}}}
	if RemoveBlockEntity(chunk, "minecraft:chest", 10, 120, 10) {
		t.Error("the hopper went as a chest")
	}
	if !RemoveBlockEntity(chunk, "minecraft:hopper", 10, 120, 10) {
		t.Fatal("the hopper's data is still there")
	}
	l := chunk["block_entities"].(nbt.List)
	if len(l.Items) != 1 || l.Items[0].(nbt.Compound)["id"] != "minecraft:chest" || chunk["Status"] != "minecraft:full" {
		t.Errorf("after: %v", chunk)
	}
}

// A chunk that no longer fits its sectors moves to the end of the file, one
// that shrinks stays where it is, and the other chunks don't move.
func TestSetChunkMovesAChunkThatGrew(t *testing.T) {
	f := file(t, map[[2]int]nbt.Compound{{0, 0}: entities(mob("minecraft:cow", 3.5, 120, 3.5, nil)), {1, 0}: entities(mob("minecraft:pig", 20.5, 70, 3.5, nil))})
	loc := func(cx int) uint32 { return binary.BigEndian.Uint32(f.Bytes()[4*cx:]) }
	pig := loc(1)
	big := make([]any, 0, 400)
	for i := range 400 {
		big = append(big, mob("minecraft:item", 3.5, 70, 3.5, nbt.Compound{"Tag": strings.Repeat("x", i)}))
	}
	data, _ := Encode(entities(big...))
	if err := f.SetChunk(0, 0, Uncompressed, data, saved); err != nil {
		t.Fatal(err)
	}
	if loc(0)>>8 <= pig>>8 || loc(0)&0xff < 2 || loc(1) != pig {
		t.Errorf("chunk 0,0 at %x, chunk 1,0 at %x (was %x)", loc(0), loc(1), pig)
	}
	if got := ids(t, chunkOf(t, f, 1, 0)); got != "minecraft:pig" {
		t.Errorf("the other chunk: %s", got)
	}
	moved := loc(0)
	small, _ := Encode(entities())
	if err := f.SetChunk(0, 0, Zlib, small, saved); err != nil {
		t.Fatal(err)
	}
	if loc(0)>>8 != moved>>8 || loc(0)&0xff != 1 {
		t.Errorf("a chunk that shrank moved: %x, was %x", loc(0), moved)
	}
	if c := chunkOf(t, f, 0, 0); len(c["Entities"].(nbt.List).Items) != 0 {
		t.Errorf("chunk 0,0: %v", c)
	}
}

// Chunks Playkeeper can't read are refused, never guessed at: LZ4, one kept
// in a file of its own, and one that points outside the file.
func TestChunkRefusesWhatItCantRead(t *testing.T) {
	f := file(t, map[[2]int]nbt.Compound{{0, 0}: entities()})
	b := f.Bytes()
	start := int(binary.BigEndian.Uint32(b)>>8) * sector
	for _, c := range []struct {
		compression byte
		want        string
	}{{4, "LZ4"}, {2 | 128, "a file of its own"}, {9, "compression type 9"}} {
		b[start+4] = c.compression
		if _, _, err := f.Chunk(0, 0); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("compression %d: %v", c.compression, err)
		}
	}
	binary.BigEndian.PutUint32(b, 400<<8|1)
	if _, _, err := f.Chunk(0, 0); !errors.Is(err, ErrFormat) {
		t.Errorf("outside the file: %v", err)
	}
	if _, err := Parse(make([]byte, 100)); !errors.Is(err, ErrFormat) {
		t.Errorf("a file shorter than its header: %v", err)
	}
}
