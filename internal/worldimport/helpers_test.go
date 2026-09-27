package worldimport

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/nbt"
)

const (
	uuidOnline  = "4566e69f-c907-48ee-8d71-d7ba5aa00d20"
	uuidOffline = "0b1ba8f5-4f8c-3fb4-9b6c-bcd3a8fbc59b"
	testSeed    = -4172144997902289642
)

var (
	testTime   = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	uuidInts   = []int32{0x4566e69f, -0x36f8b712, -0x728e2846, 0x5aa00d20}
	regionData = strings.Repeat("region-sector-", 8)
)

// tf is one entry of an archive built by a test.
type tf struct {
	name string
	body string
	dir  bool
	link string // symbolic link target
	hard string // hard link target, tar only
	typ  byte   // another tar type flag, such as tar.TypeFifo
}

func f(name, body string) tf { return tf{name: name, body: body} }

// withPrefix puts entries under a folder.
func withPrefix(prefix string, files []tf) []tf {
	out := make([]tf, len(files))
	for i, e := range files {
		e.name = prefix + e.name
		out[i] = e
	}
	return out
}

func join(parts ...[]tf) []tf {
	var out []tf
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// upload writes data to a file whose path doesn't depend on name, so tests
// can use any upload name.
func upload(t *testing.T, name string, data []byte) Source {
	t.Helper()
	p := filepath.Join(t.TempDir(), "upload.bin")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return Source{Name: name, Path: p}
}

func zipBytes(t *testing.T, files []tf) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range files {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: testTime}
		body := e.body
		switch {
		case e.dir:
			h.Name = strings.TrimSuffix(e.name, "/") + "/"
			h.Method = zip.Store
			h.SetMode(fs.ModeDir | 0o755)
		case e.link != "":
			h.SetMode(fs.ModeSymlink | 0o777)
			body = e.link
		default:
			h.SetMode(0o644)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tarBytes(t *testing.T, files []tf) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range files {
		h := &tar.Header{Name: e.name, Mode: 0o644, ModTime: testTime}
		switch {
		case e.dir:
			h.Typeflag, h.Name, h.Mode = tar.TypeDir, strings.TrimSuffix(e.name, "/")+"/", 0o755
		case e.link != "":
			h.Typeflag, h.Linkname = tar.TypeSymlink, e.link
		case e.hard != "":
			h.Typeflag, h.Linkname = tar.TypeLink, e.hard
		case e.typ != 0:
			h.Typeflag = e.typ
		default:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// zipEOCD returns the offset of a zip's end of central directory record.
func zipEOCD(t *testing.T, b []byte) int {
	t.Helper()
	i := bytes.LastIndex(b, []byte("PK\x05\x06"))
	if i < 0 {
		t.Fatal("no end of central directory")
	}
	return i
}

// zipCentral returns the offsets of a zip's central directory headers.
func zipCentral(t *testing.T, b []byte) []int {
	t.Helper()
	end := zipEOCD(t, b)
	off := int(binary.LittleEndian.Uint32(b[end+16:]))
	var out []int
	for off+46 <= end && string(b[off:off+4]) == "PK\x01\x02" {
		out = append(out, off)
		n := int(binary.LittleEndian.Uint16(b[off+28:]))
		m := int(binary.LittleEndian.Uint16(b[off+30:]))
		k := int(binary.LittleEndian.Uint16(b[off+32:]))
		off += 46 + n + m + k
	}
	return out
}

func inspect(t *testing.T, lim Limits, sources ...Source) *Inspection {
	t.Helper()
	in, err := Inspect(context.Background(), sources, lim)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	return in
}

// refusalKind returns the Kind of the *Error err must be, and checks that
// its message is a full sentence.
func refusalKind(t *testing.T, err error) string {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("got error %v (%T), want an *Error", err, err)
	}
	if e.Msg == "" || !strings.HasSuffix(e.Msg, ".") {
		t.Errorf("%s: message %q is not a sentence", e.Kind, e.Msg)
	}
	return e.Kind
}

func kinds(ms []Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Kind
	}
	return out
}

func hasKind(ms []Message, kind string) bool {
	for _, m := range ms {
		if m.Kind == kind {
			return true
		}
	}
	return false
}

func leftOut(p *Preview, kind string) *LeftOut {
	for i := range p.LeftOut {
		if p.LeftOut[i].Kind == kind {
			return &p.LeftOut[i]
		}
	}
	return nil
}

// staged lists the files under dir with their contents, by slash path.
func staged(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !d.Type().IsRegular() {
			t.Errorf("%s is not a regular file", p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(r)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalLists(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringList(items ...string) nbt.List {
	l := nbt.List{Type: nbt.TagString}
	for _, s := range items {
		l.Items = append(l.Items, s)
	}
	return l
}

// levelDat gzips a level.dat holding data as its Data compound, the way
// Minecraft writes it.
func levelDat(t *testing.T, data nbt.Compound) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := nbt.Write(zw, "", nbt.Compound{"Data": data}); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// legacyLevel is the Data compound of a level.dat saved before Minecraft
// 26.1, trimmed to the fields Playkeeper reads plus a few others.
func legacyLevel(name, version string, dv int32) nbt.Compound {
	return nbt.Compound{
		"LevelName":    name,
		"DataVersion":  dv,
		"Version":      nbt.Compound{"Id": dv, "Name": version, "Series": "main", "Snapshot": int8(0)},
		"version":      int32(19133),
		"GameType":     int32(0),
		"Difficulty":   int8(2),
		"hardcore":     int8(0),
		"initialized":  int8(1),
		"LastPlayed":   int64(1788264000000),
		"DataPacks":    nbt.Compound{"Enabled": stringList("vanilla", "file/terralith.zip"), "Disabled": nbt.List{Type: nbt.TagEnd}},
		"ServerBrands": stringList("vanilla"),
		"WasModded":    int8(0),
		"WorldGenSettings": nbt.Compound{
			"seed": int64(testSeed), "generate_features": int8(1), "bonus_chest": int8(0), "dimensions": nbt.Compound{},
		},
		"SpawnX": int32(-120), "SpawnY": int32(64), "SpawnZ": int32(32),
		"GameRules": nbt.Compound{"keepInventory": "false"},
	}
}

// singleplayerLevel adds the player a singleplayer world keeps in level.dat.
func singleplayerLevel(name, version string, dv int32) nbt.Compound {
	c := legacyLevel(name, version, dv)
	c["Player"] = nbt.Compound{"UUID": uuidInts, "Health": float32(20), "Dimension": "minecraft:overworld"}
	return c
}

// modernLevel is the Data compound of a level.dat saved by Minecraft 26.1
// or newer: difficulty moved into difficulty_settings, the seed into
// world_gen_settings.dat and the player into players/data.
func modernLevel(name, version string, dv int32) nbt.Compound {
	return nbt.Compound{
		"LevelName":           name,
		"DataVersion":         dv,
		"Version":             nbt.Compound{"Id": dv, "Name": version, "Series": "main", "Snapshot": int8(0)},
		"version":             int32(19133),
		"GameType":            int32(1),
		"difficulty_settings": nbt.Compound{"difficulty": "hard", "hardcore": int8(0), "locked": int8(0)},
		"initialized":         int8(1),
		"LastPlayed":          int64(1788264000000),
		"DataPacks":           nbt.Compound{"Enabled": stringList("vanilla"), "Disabled": nbt.List{Type: nbt.TagEnd}},
		"enabled_features":    stringList("minecraft:vanilla"),
		"ServerBrands":        stringList("vanilla"),
		"WasModded":           int8(0),
		"singleplayer_uuid":   uuidInts,
	}
}

// worldGenSettings is a world_gen_settings.dat, a saved data file of 26.1
// and newer.
func worldGenSettings(t *testing.T, dv int32, seed int64) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	err := nbt.Write(zw, "", nbt.Compound{"DataVersion": dv, "data": nbt.Compound{
		"seed": seed, "generate_structures": int8(1), "bonus_chest": int8(0), "dimensions": nbt.Compound{},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// legacyWorldFiles are the files of a world folder saved before 26.1, with
// the Nether and the End inside it as DIM-1 and DIM1 when asked for.
func legacyWorldFiles(level string, nether, end bool) []tf {
	files := []tf{
		f("level.dat", level),
		f("session.lock", "\u2603"),
		f("icon.png", "png"),
		f("region/r.0.0.mca", regionData+"overworld"),
		f("region/r.-1.0.mca", regionData+"overworld2"),
		f("entities/r.0.0.mca", regionData+"entities"),
		f("poi/r.0.0.mca", regionData+"poi"),
		f("data/raids.dat", "raids"),
		f("playerdata/"+uuidOnline+".dat", "player"),
		f("advancements/"+uuidOnline+".json", "{}"),
		f("stats/"+uuidOnline+".json", "{}"),
		f("datapacks/terralith.zip", "pack"),
	}
	if nether {
		files = append(files, f("DIM-1/region/r.0.0.mca", regionData+"nether"), f("DIM-1/data/raids.dat", "nether raids"))
	}
	if end {
		files = append(files, f("DIM1/region/r.0.0.mca", regionData+"end"))
	}
	return files
}

// paperServerFiles is the folder of a Paper server before 26.1, with its
// world split into world, world_nether and world_the_end.
func paperServerFiles(t *testing.T) []tf {
	level := legacyLevel("world", "1.21.11", 4671)
	level["ServerBrands"] = stringList("Paper")
	lv := levelDat(t, level)
	return join(
		[]tf{
			f("server.properties", "#Minecraft server properties\nlevel-name=world\nlevel-seed=12345\ndifficulty=hard\ngamemode=survival\nenable-rcon=true\nrcon.password=hunter2\nserver-port=25570\nonline-mode=false\nmotd=Hello there\n"),
			f("eula.txt", "eula=true\n"),
			f("paper-1.21.11-99.jar", "jar"),
			f("bukkit.yml", "settings: {}\n"),
			f("spigot.yml", "settings: {}\n"),
			f("config/paper-global.yml", "_version: 30\n"),
			f("plugins/EssentialsX.jar", "plugin jar"),
			f("plugins/Essentials/config.yml", "ops-name-color: '4'\n"),
			f("plugins/.paper-remapped/EssentialsX.jar", "remapped"),
			f("logs/latest.log", "[12:00:00] Done\n"),
			f("crash-reports/crash-2026-09-01.txt", "crash"),
			f("cache/mojang_1.21.11.jar", "cache"),
			f("libraries/com/google/guava.jar", "lib"),
			f("versions/1.21.11/paper-1.21.11.jar", "paper"),
			f("usercache.json", "[]"),
			f("ops.json", `[{"uuid":"`+uuidOnline+`","name":"Steve","level":4,"bypassesPlayerLimit":false}]`),
			f("whitelist.json", `[{"uuid":"`+uuidOnline+`","name":"Steve"}]`),
			f("banned-players.json", "[]"),
		},
		withPrefix("world/", []tf{
			f("level.dat", lv),
			f("session.lock", "lock"),
			f("uid.dat", "uid-overworld"),
			f("paper-world.yml", "_version: 31\n"),
			f("region/r.0.0.mca", regionData+"overworld"),
			f("entities/r.0.0.mca", regionData+"entities"),
			f("poi/r.0.0.mca", regionData+"poi"),
			f("data/raids.dat", "raids"),
			f("playerdata/"+uuidOnline+".dat", "player"),
			f("datapacks/bukkit/pack.mcmeta", "{}"),
		}),
		withPrefix("world_nether/", []tf{
			f("level.dat", lv),
			f("session.lock", "lock"),
			f("uid.dat", "uid-nether"),
			f("paper-world.yml", "_version: 31\n"),
			f("DIM-1/region/r.0.0.mca", regionData+"nether"),
		}),
		withPrefix("world_the_end/", []tf{
			f("level.dat", lv),
			f("uid.dat", "uid-end"),
			f("DIM1/region/r.0.0.mca", regionData+"end"),
		}),
	)
}

// modernPaperWorldFiles is a world folder of Paper 26.1 or newer: the
// vanilla 26.1 layout, with Paper keeping five data files in the
// Overworld's folder.
func modernPaperWorldFiles(t *testing.T) []tf {
	level := modernLevel("world", "26.2", 4903)
	level["ServerBrands"] = stringList("Paper")
	delete(level, "singleplayer_uuid")
	return []tf{
		f("level.dat", levelDat(t, level)),
		f("session.lock", "lock"),
		f("dimensions/minecraft/overworld/region/r.0.0.mca", regionData+"overworld"),
		f("dimensions/minecraft/overworld/entities/r.0.0.mca", regionData+"entities"),
		f("dimensions/minecraft/overworld/poi/r.0.0.mca", regionData+"poi"),
		f("dimensions/minecraft/overworld/data/minecraft/game_rules.dat", "rules"),
		f("dimensions/minecraft/overworld/data/minecraft/weather.dat", "paper weather"),
		f("dimensions/minecraft/overworld/data/minecraft/world_gen_settings.dat", worldGenSettings(t, 4903, 99887766)),
		f("dimensions/minecraft/overworld/data/minecraft/raids.dat", "raids"),
		f("dimensions/minecraft/the_nether/region/r.0.0.mca", regionData+"nether"),
		f("dimensions/minecraft/the_end/region/r.0.0.mca", regionData+"end"),
		f("data/minecraft/weather.dat", "stale weather"),
		f("data/minecraft/scoreboard.dat", "scores"),
		f("players/data/"+uuidOnline+".dat", "player"),
		f("players/advancements/"+uuidOnline+".json", "{}"),
		f("players/stats/"+uuidOnline+".json", "{}"),
		f("datapacks/pack.zip", "pack"),
	}
}
