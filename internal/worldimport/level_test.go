package worldimport

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/nbt"
)

func parseLevel(t *testing.T, data nbt.Compound) *Level {
	t.Helper()
	lv, err := ParseLevel([]byte(levelDat(t, data)), 1<<20)
	if err != nil {
		t.Fatalf("ParseLevel: %v", err)
	}
	return lv
}

func rawNBT(t *testing.T, root nbt.Compound) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := nbt.Write(&buf, "", root); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseLevelBefore26_1(t *testing.T) {
	data := singleplayerLevel("My \u202eWorld", "1.21.4", 4189)
	data["GameType"] = int32(2)
	data["Difficulty"] = int8(3)
	data["hardcore"] = int8(1)
	data["DataPacks"] = nbt.Compound{"Enabled": stringList("vanilla", "file/terralith.zip"), "Disabled": stringList("bundle")}
	data["enabled_features"] = stringList("minecraft:vanilla", "minecraft:trade_rebalance")
	data["ServerBrands"] = stringList("vanilla", "fabric")
	data["WasModded"] = int8(1)

	want := &Level{
		Name: "My World", Version: "1.21.4", DataVersion: 4189, Series: "main", GameMode: "adventure",
		Hardcore: true, Difficulty: "hard", LastPlayed: testTime,
		DataPacks: []string{"vanilla", "file/terralith.zip"}, DisabledPacks: []string{"bundle"},
		Features: []string{"minecraft:trade_rebalance"}, Brands: []string{"vanilla", "fabric"}, Modded: true,
		Seed: "-4172144997902289642", Owner: uuidOnline, Spawn: &Spawn{X: -120, Z: 32}, ownerInLevel: true,
	}
	if got := parseLevel(t, data); !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestParseLevelSpawn(t *testing.T) {
	modern := modernLevel("Survival", "26.1.2", 4786)
	modern["spawn"] = nbt.Compound{"pos": []int32{40, 71, 12}, "dimension": "minecraft:overworld", "yaw": float32(0), "pitch": float32(0)}
	short := modernLevel("Survival", "26.1.2", 4786)
	short["spawn"] = nbt.Compound{"pos": []int32{40, 71}}
	halfLegacy := legacyLevel("Old", "1.21.4", 4189)
	delete(halfLegacy, "SpawnZ")
	cases := []struct {
		name string
		data nbt.Compound
		want *Spawn
	}{
		{"SpawnX and SpawnZ before 1.21.9", legacyLevel("Old", "1.21.4", 4189), &Spawn{X: -120, Z: 32}},
		{"the spawn compound since 1.21.9", modern, &Spawn{X: 40, Z: 12}},
		{"no spawn", modernLevel("Survival", "26.1.2", 4786), nil},
		{"a pos without three numbers", short, nil},
		{"SpawnX without SpawnZ", halfLegacy, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseLevel(t, c.data).Spawn; !reflect.DeepEqual(got, c.want) {
				t.Errorf("spawn = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestParseLevel26_1(t *testing.T) {
	want := &Level{
		Name: "Creative Build", Version: "26.2", DataVersion: 4903, Series: "main", GameMode: "creative",
		Difficulty: "hard", LastPlayed: testTime, DataPacks: []string{"vanilla"}, Brands: []string{"vanilla"},
		Owner: uuidOnline,
	}
	if got := parseLevel(t, modernLevel("Creative Build", "26.2", 4903)); !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestParseLevelFields(t *testing.T) {
	manyPacks := make([]string, 300)
	for i := range manyPacks {
		manyPacks[i] = "file/pack" + strings.Repeat("x", i%5) + ".zip"
	}
	cases := []struct {
		name  string
		edit  func(c nbt.Compound)
		check func(lv *Level) bool
	}{
		{"data version from Version.Id", func(c nbt.Compound) { delete(c, "DataVersion") },
			func(lv *Level) bool { return lv.DataVersion == 4189 }},
		{"DataVersion before Version.Id", func(c nbt.Compound) { c["Version"].(nbt.Compound)["Id"] = int32(100) },
			func(lv *Level) bool { return lv.DataVersion == 4189 }},
		{"seed before 1.16", func(c nbt.Compound) { delete(c, "WorldGenSettings"); c["RandomSeed"] = int64(42) },
			func(lv *Level) bool { return lv.Seed == "42" }},
		{"no seed", func(c nbt.Compound) { delete(c, "WorldGenSettings") },
			func(lv *Level) bool { return lv.Seed == "" }},
		{"seed of the wrong type", func(c nbt.Compound) { c["WorldGenSettings"] = nbt.Compound{"seed": int32(5)} },
			func(lv *Level) bool { return lv.Seed == "" }},
		{"never played", func(c nbt.Compound) { c["LastPlayed"] = int64(0) },
			func(lv *Level) bool { return lv.LastPlayed.IsZero() }},
		{"played before 1970", func(c nbt.Compound) { c["LastPlayed"] = int64(-1) },
			func(lv *Level) bool { return lv.LastPlayed.IsZero() }},
		{"played after year 9999", func(c nbt.Compound) { c["LastPlayed"] = int64(maxLastPlayed + 1) },
			func(lv *Level) bool { return lv.LastPlayed.IsZero() }},
		{"unknown game mode and difficulty", func(c nbt.Compound) { c["GameType"] = int32(9); c["Difficulty"] = int8(7) },
			func(lv *Level) bool { return lv.GameMode == "" && lv.Difficulty == "" }},
		{"difficulty settings win", func(c nbt.Compound) {
			c["difficulty_settings"] = nbt.Compound{"difficulty": "PEACEFUL", "hardcore": int8(1), "locked": int8(1)}
		}, func(lv *Level) bool { return lv.Difficulty == "peaceful" && lv.Hardcore }},
		{"fields of the wrong type", func(c nbt.Compound) {
			c["LevelName"] = int32(5)
			c["GameType"] = "creative"
			c["Difficulty"] = "hard"
			c["hardcore"] = "yes"
			c["DataPacks"] = "vanilla"
			c["ServerBrands"] = nbt.List{Type: nbt.TagInt, Items: []any{int32(1)}}
			c["Version"] = "1.21.4"
		}, func(lv *Level) bool {
			return lv.Name == "" && lv.GameMode == "" && lv.Difficulty == "" && !lv.Hardcore && lv.DataPacks == nil &&
				lv.Brands == nil && lv.Version == "" && lv.DataVersion == 4189
		}},
		{"long name", func(c nbt.Compound) { c["LevelName"] = strings.Repeat("a", 150) },
			func(lv *Level) bool { return len([]rune(lv.Name)) == 100 && strings.HasSuffix(lv.Name, "…") }},
		{"unsafe characters in lists", func(c nbt.Compound) { c["ServerBrands"] = stringList("\x1bfabric\u202e", "", "  ") },
			func(lv *Level) bool { return reflect.DeepEqual(lv.Brands, []string{"fabric"}) }},
		{"many data packs", func(c nbt.Compound) {
			c["DataPacks"] = nbt.Compound{"Enabled": stringList(manyPacks...)}
		}, func(lv *Level) bool { return len(lv.DataPacks) == maxListItems }},
		{"player with a malformed UUID", func(c nbt.Compound) { c["Player"] = nbt.Compound{"UUID": []int32{1, 2}} },
			func(lv *Level) bool { return lv.Owner == "" && lv.ownerInLevel }},
		{"singleplayer UUID of 26.1", func(c nbt.Compound) { c["singleplayer_uuid"] = uuidInts },
			func(lv *Level) bool { return lv.Owner == uuidOnline && !lv.ownerInLevel }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := legacyLevel("world", "1.21.4", 4189)
			c.edit(data)
			if lv := parseLevel(t, data); !c.check(lv) {
				t.Errorf("unexpected result %+v", lv)
			}
		})
	}
}

// deepNBT is a root compound with depth compounds nested inside it, each
// named Data.
func deepNBT(depth int) []byte {
	b := []byte{nbt.TagCompound, 0, 0}
	for i := 0; i < depth; i++ {
		b = append(b, nbt.TagCompound, 0, 4, 'D', 'a', 't', 'a')
	}
	for i := 0; i <= depth; i++ {
		b = append(b, nbt.TagEnd)
	}
	return b
}

func TestParseLevelRefusesMalformed(t *testing.T) {
	good := []byte(levelDat(t, legacyLevel("world", "1.21.4", 4189)))
	plain := rawNBT(t, nbt.Compound{"Data": legacyLevel("world", "1.21.4", 4189)})
	big := legacyLevel("world", "1.21.4", 4189)
	big["Junk"] = make([]byte, 1<<20)

	cases := []struct {
		name     string
		data     []byte
		maxBytes int64
		want     error
		problem  string
	}{
		{"not gzip-compressed", []byte("level.dat"), 1 << 20, nbt.ErrFormat, "damaged"},
		{"uncompressed NBT", plain, 1 << 20, nbt.ErrFormat, "damaged"},
		{"gzip of text", gzipBytes(t, []byte("hello, world")), 1 << 20, nbt.ErrFormat, "damaged"},
		{"no Data compound", gzipBytes(t, rawNBT(t, nbt.Compound{"data": nbt.Compound{}})), 1 << 20, nbt.ErrFormat, "damaged"},
		{"Data is a list", gzipBytes(t, rawNBT(t, nbt.Compound{"Data": stringList("x")})), 1 << 20, nbt.ErrFormat, "damaged"},
		{"cut off", good[:len(good)/2], 1 << 20, nbt.ErrFormat, "damaged"},
		{"NBT ends early", gzipBytes(t, plain[:len(plain)/2]), 1 << 20, nbt.ErrFormat, "damaged"},
		{"nested too deeply", gzipBytes(t, deepNBT(600)), 1 << 20, nbt.ErrTooDeep, "nested more deeply"},
		{"too large", []byte(levelDat(t, big)), 64 << 10, nbt.ErrTooLarge, "larger than"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lv, err := ParseLevel(c.data, c.maxBytes)
			if !errors.Is(err, c.want) {
				t.Fatalf("got %v, %v; want %v", lv, err, c.want)
			}
			if p := levelProblem("level.dat", err); !strings.HasPrefix(p, "level.dat ") || !strings.Contains(p, c.problem) || !strings.HasSuffix(p, ".") {
				t.Errorf("problem %q", p)
			}
		})
	}
}

func TestReadSeed(t *testing.T) {
	cases := map[string]struct {
		data []byte
		want string
	}{
		"world_gen_settings.dat": {[]byte(worldGenSettings(t, 4903, 99887766)), "99887766"},
		"seed at the top level":  {gzipBytes(t, rawNBT(t, nbt.Compound{"seed": int64(-5)})), "-5"},
		"seed of the wrong type": {gzipBytes(t, rawNBT(t, nbt.Compound{"data": nbt.Compound{"seed": "12"}})), ""},
		"damaged":                {[]byte("garbage"), ""},
	}
	for name, c := range cases {
		if got := readSeed(c.data, 1<<20); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}
