package packs

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

// The sizes and signatures of a zip's end records, for tests that damage
// them.
const (
	directoryEndLen         = 22
	directory64LocLen       = 20
	directory64EndLen       = 56
	directory64LocSignature = 0x07064b50
	directory64EndSignature = 0x06064b50
)

func le16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }

func withMode(mode fs.FileMode) func(*zip.FileHeader) {
	return func(fh *zip.FileHeader) { fh.SetMode(mode) }
}

func formatsText(r *FormatRange) string {
	if r == nil {
		return "none"
	}
	return fmt.Sprintf("%s to %s", r.Min, r.Max)
}

// renamed renames the entry old to new, of the same length, in both places
// a zip records its name.
func renamed(t *testing.T, b []byte, old, new string) []byte {
	t.Helper()
	if len(old) != len(new) || bytes.Count(b, []byte(old)) != 2 {
		t.Fatalf("can't rename %s in the zip", old)
	}
	return bytes.ReplaceAll(b, []byte(old), []byte(new))
}

// toZip64 moves the counts of a zip without a comment into zip64 end
// records, as zips with more than 65,535 entries or 4 GiB of data have.
func toZip64(t *testing.T, b []byte) []byte {
	t.Helper()
	end := len(b) - directoryEndLen
	if le32(b[end:]) != 0x06054b50 {
		t.Fatal("the zip has a comment")
	}
	le := binary.LittleEndian
	records := uint64(le16(b[end+10:]))
	rec := make([]byte, directory64EndLen)
	le.PutUint32(rec, directory64EndSignature)
	le.PutUint64(rec[4:], directory64EndLen-12)
	le.PutUint16(rec[12:], 45)
	le.PutUint16(rec[14:], 45)
	le.PutUint64(rec[24:], records)
	le.PutUint64(rec[32:], records)
	le.PutUint64(rec[40:], uint64(le32(b[end+12:])))
	le.PutUint64(rec[48:], uint64(le32(b[end+16:])))
	loc := make([]byte, directory64LocLen)
	le.PutUint32(loc, directory64LocSignature)
	le.PutUint64(loc[8:], uint64(end))
	le.PutUint32(loc[16:], 1)
	eocd := slices.Clone(b[end:])
	le.PutUint16(eocd[8:], 0xffff)
	le.PutUint16(eocd[10:], 0xffff)
	le.PutUint32(eocd[12:], 0xffffffff)
	le.PutUint32(eocd[16:], 0xffffffff)
	return slices.Concat(b[:end], rec, loc, eocd)
}

func TestInspect(t *testing.T) {
	load := `{"values": []}`
	data := dataPack(t, dir("data/"), file("data/test/tags/function/load.json", load))
	info, err := inspect(t, data, Data, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	s1, s256 := sha1.Sum(data), sha256.Sum256(data)
	want := Info{
		Kind:        Data,
		Description: "A test data pack",
		Formats:     &FormatRange{Format{48, 0}, Format{48, 0}},
		Size:        int64(len(data)),
		Files:       3,
		Unpacked:    int64(len(dataMcmeta) + len("say hello\n") + len(load)),
		SHA1:        hex.EncodeToString(s1[:]),
		SHA256:      hex.EncodeToString(s256[:]),
	}
	if !reflect.DeepEqual(info, want) {
		t.Errorf("Inspect = %+v\nwant %+v", info, want)
	}
	j, err := json.Marshal(info)
	if err != nil || !bytes.Contains(j, []byte(`{"kind":"data","description":"A test data pack","formats":{"min":"48.0","max":"48.0"},"size":`)) ||
		bytes.Contains(j, []byte("formatProblem")) || bytes.Contains(j, []byte("features")) {
		t.Errorf("Info as JSON = %s, %v", j, err)
	}

	info, err = inspect(t, resourcePack(t), Resource, Limits{})
	if err != nil || info.Kind != Resource || info.Description != "A test resource pack" || formatsText(info.Formats) != "34.0 to 34.0" {
		t.Errorf("resource pack: %+v, %v", info, err)
	}

	both := buildZip(t, file("pack.mcmeta", dataMcmeta), file("data/test/function/a.mcfunction", ""), file("assets/test/lang/en_us.json", "{}"))
	for _, use := range []Kind{Data, Resource} {
		if info, err := inspect(t, both, use, Limits{}); err != nil || info.Kind != Both {
			t.Errorf("a zip with data and assets, as a %s pack: %+v, %v", use, info, err)
		}
	}

	future := buildZip(t,
		file("pack.mcmeta", `{"pack": {"description": "From the future", "pack_format": 99}, "features": {"enabled": ["trade_rebalance"]}}`),
		file("data/test/function/a.mcfunction", ""))
	info, err = inspect(t, future, Data, Limits{})
	if err != nil || info.Formats != nil || !slices.Equal(info.Features, []string{"minecraft:trade_rebalance"}) ||
		info.FormatProblem != "Pack declares support for version newer than 81, but is missing mandatory fields min_format and max_format" {
		t.Errorf("a pack for a newer game: %+v, %v", info, err)
	}

	var e *Error
	if _, err := inspect(t, data, Both, Limits{}); err == nil || errors.As(err, &e) {
		t.Errorf("Inspect for both uses = %v, want a plain error", err)
	}
}

// Real packs' zips differ in how they record folders.
func TestInspectRealLayouts(t *testing.T) {
	mcmeta := func(name string) string {
		b, err := os.ReadFile("testdata/" + name + ".mcmeta")
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// Zips written with Java, as Veinminer's is, give folders Unix modes
	// and compress them to an empty Deflate stream.
	javaDir := func(fh *zip.FileHeader) {
		fh.SetMode(fs.ModeDir | 0o755)
		fh.CompressedSize64 = 2
	}
	b := buildZip(t,
		entry{name: "data#", raw: []byte{3, 0}, edit: javaDir},
		entry{name: "data/veinminer/", edit: withMode(fs.ModeDir | 0o755)},
		entry{name: "data/veinminer/function/load.mcfunction", body: "say loaded\n", edit: withMode(0o644)},
		entry{name: "pack.mcmeta", body: mcmeta("veinminer-1.3.6"), edit: withMode(0o644)},
	)
	b = renamed(t, b, "data#", "data/")
	info, err := inspect(t, b, Data, Limits{})
	if err != nil || info.Kind != Data || info.Description != "Veinminer | 26.3+\nBy Miraculixx" || formatsText(info.Formats) != "121.0 to 121.*" || info.Files != 2 {
		t.Errorf("zip laid out like Veinminer's: %+v, %v", info, err)
	}

	// Zips written on Windows, as Low on Fire's is, mark folders with an
	// MS-DOS attribute, and some leave out the folders above the files.
	fat := func(attrs uint32) func(*zip.FileHeader) {
		return func(fh *zip.FileHeader) { fh.CreatorVersion, fh.ExternalAttrs = 0, attrs }
	}
	b = buildZip(t,
		entry{name: "assets/minecraft/", edit: fat(0x10)},
		entry{name: "assets/minecraft/textures/", edit: fat(0x10)},
		entry{name: "assets/minecraft/textures/block/fire_0.png", body: "png", edit: fat(0x20)},
		entry{name: "pack.mcmeta", body: mcmeta("low-on-fire-26.3"), edit: fat(0x20)},
		entry{name: "pack.png", body: "png", edit: fat(0x21)},
	)
	info, err = inspect(t, b, Resource, Limits{})
	if err != nil || info.Kind != Resource || info.Description != "Handle the heat!\nby Haikis" || formatsText(info.Formats) != "15.0 to 200.*" || info.Files != 3 {
		t.Errorf("zip laid out like Low on Fire's: %+v, %v", info, err)
	}
}

func TestInspectContent(t *testing.T) {
	overlay := `{"pack": {"description": "d", "pack_format": 48}, "overlays": {"entries": [{"directory": "ov", "min_format": 90, "max_format": 95}]}}`
	info, err := inspect(t, buildZip(t, file("pack.mcmeta", overlay), file("ov/data/test/function/a.mcfunction", "")), Data, Limits{})
	if err != nil || info.Kind != Data {
		t.Errorf("a pack whose data is in an overlay: %+v, %v", info, err)
	}

	for name, b := range map[string][]byte{
		"a file directly in data": buildZip(t, file("pack.mcmeta", dataMcmeta), file("data/test.json", "{}")),
		"an undeclared overlay":   buildZip(t, file("pack.mcmeta", dataMcmeta), file("ov/data/test/function/a.mcfunction", "")),
		"only folders":            buildZip(t, file("pack.mcmeta", dataMcmeta), dir("data/"), dir("data/test/")),
		"other folders":           buildZip(t, file("pack.mcmeta", dataMcmeta), file("textures/block/stone.png", "")),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := inspect(t, b, Data, Limits{})
			wantCode(t, err, CodeNoContent)
		})
	}

	for _, tc := range []struct {
		b         []byte
		use       Kind
		msg, hint string
	}{
		{resourcePack(t), Data, "This zip is a resource pack, not a data pack.", "Add it under Resource packs instead."},
		{dataPack(t), Resource, "This zip is a data pack, not a resource pack.", "Add it under Data packs instead."},
	} {
		_, err := inspect(t, tc.b, tc.use, Limits{})
		e := wantCode(t, err, CodeWrongKind)
		if e.Msg != tc.msg || e.Hint != tc.hint || e.Params["expected"] != tc.use || !errors.Is(err, ErrWrongKind) {
			t.Errorf("wrong kind for %s: %+v", tc.use, e)
		}
	}
}

func TestInspectNoMcmeta(t *testing.T) {
	for _, tc := range []struct {
		name         string
		entries      []entry
		param, value string
		msg          string
	}{
		{"folder", []entry{dir("My Pack/"), file("My Pack/pack.mcmeta", dataMcmeta), file("My Pack/data/test/function/a.mcfunction", "")},
			"folder", "My Pack", `The zip has no pack.mcmeta file at its top: the pack is inside the folder "My Pack".`},
		{"folder over a mod", []entry{file("fabric.mod.json", "{}"), file("x/pack.mcmeta", dataMcmeta)},
			"folder", "x", `The zip has no pack.mcmeta file at its top: the pack is inside the folder "x".`},
		{"capitalized", []entry{file("Pack.mcmeta", dataMcmeta), file("data/test/function/a.mcfunction", "")},
			"found", "Pack.mcmeta", `The zip has no pack.mcmeta file at its top, only "Pack.mcmeta".`},
		{"extension", []entry{file("pack.mcmeta.txt", dataMcmeta)},
			"found", "pack.mcmeta.txt", `The zip has no pack.mcmeta file at its top, only "pack.mcmeta.txt".`},
		{"fabric", []entry{file("fabric.mod.json", "{}"), file("assets/test/icon.png", "")},
			"mod", "fabric", "This zip looks like a Fabric mod, not a pack."},
		{"forge", []entry{file("META-INF/mods.toml", "")}, "mod", "forge", "This zip looks like a Forge mod, not a pack."},
		{"plugin", []entry{file("plugin.yml", "")}, "mod", "bukkit", "This zip looks like a Paper or Spigot plugin, not a pack."},
		{"nested", []entry{file("Terralith.zip", "PK")},
			"nested", "Terralith.zip", `The zip has no pack.mcmeta file at its top, but it holds another zip, "Terralith.zip", which may be the pack.`},
		{"nothing", []entry{file("readme.txt", "hi")}, "", "", "The zip has no pack.mcmeta file at its top, so it isn't a Minecraft pack."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := inspect(t, buildZip(t, tc.entries...), Data, Limits{})
			e := wantCode(t, err, CodeNoMcmeta)
			if e.Msg != tc.msg || e.Hint == "" || tc.param != "" && e.Params[tc.param] != tc.value || tc.param == "" && len(e.Params) != 0 {
				t.Errorf("got %q, %v", e.Msg, e.Params)
			}
		})
	}
}

func TestInspectMcmetaProblems(t *testing.T) {
	big := buildZip(t,
		file("pack.mcmeta", `{"pack": {"description": "`+strings.Repeat("a", maxMcmetaBytes)+`", "pack_format": 48}}`),
		file("data/test/function/a.mcfunction", ""))
	_, err := inspect(t, big, Data, Limits{})
	if e := wantCode(t, err, CodeInvalidMcmeta); e.Params["problem"] != "too_large" ||
		e.Msg != "The pack's pack.mcmeta file is larger than 1.0 MB, which no real pack needs." {
		t.Errorf("large pack.mcmeta: %+v", e)
	}

	_, err = inspect(t, buildZip(t, file("pack.mcmeta", "{"), file("data/test/function/a.mcfunction", "")), Data, Limits{})
	if e := wantCode(t, err, CodeInvalidMcmeta); e.Params["problem"] != "json" {
		t.Errorf("broken pack.mcmeta: %+v", e)
	}
}

func TestInspectLimits(t *testing.T) {
	pack := dataPack(t)
	size := int64(len(pack))
	_, err := inspect(t, pack, Data, Limits{MaxBytes: size - 1})
	if e := wantCode(t, err, CodeTooLarge); e.Params["size"] != size || e.Params["max"] != size-1 || !errors.Is(err, ErrTooLarge) {
		t.Errorf("too large: %+v", e)
	}
	if _, err := inspect(t, pack, Data, Limits{MaxBytes: size}); err != nil {
		t.Errorf("a pack of exactly the largest size: %v", err)
	}
	_, err = Inspect(context.Background(), bytes.NewReader(nil), ResourcePackMaxBytes+1, Resource, Limits{MaxBytes: 1 << 40})
	if e := wantCode(t, err, CodeTooLarge); e.Params["max"] != int64(ResourcePackMaxBytes) {
		t.Errorf("resource pack over what games download: %+v", e)
	}

	_, err = inspect(t, pack, Data, Limits{MaxFiles: 1})
	if e := wantCode(t, err, CodeTooManyFiles); e.Msg != "The zip holds 2 files and folders, more than the 1 Playkeeper accepts." {
		t.Errorf("too many files: %+v", e)
	}
	if _, err := inspect(t, pack, Data, Limits{MaxFiles: 2}); err != nil {
		t.Errorf("a pack with exactly the most files: %v", err)
	}
	if msg := tooManyFiles(1<<40, 100_000).Msg; msg != "The zip holds 1,099,511,627,776 files and folders, more than the 100,000 Playkeeper accepts." {
		t.Errorf("tooManyFiles message = %q", msg)
	}

	unpacked := int64(len(dataMcmeta) + len("say hello\n"))
	_, err = inspect(t, pack, Data, Limits{MaxUnpackedBytes: unpacked - 1})
	if e := wantCode(t, err, CodeTooMuchData); e.Params["max"] != unpacked-1 {
		t.Errorf("too much data: %+v", e)
	}
	if info, err := inspect(t, pack, Data, Limits{MaxUnpackedBytes: unpacked}); err != nil || info.Unpacked != unpacked {
		t.Errorf("a pack with exactly the most data: %+v, %v", info, err)
	}
}

func TestInspectNames(t *testing.T) {
	for _, tc := range []struct{ name, problem string }{
		{"", "empty"},
		{"data/" + strings.Repeat("a", 1100), "too_long"},
		{"data/\xff.json", "encoding"},
		{"data/a\x01b.json", "control"},
		{"data/a\u0085b.json", "control"},
		{`data\test\function\a.mcfunction`, "backslash"},
		{"/etc/passwd", "absolute"},
		{"C:/Windows/win.ini", "absolute"},
		{"c:win.ini", "absolute"},
		{"data//test.json", "empty_segment"},
		{"./pack.png", "dot_segment"},
		{"data/./test.json", "dot_segment"},
		{"../evil.json", "traversal"},
		{"data/../../evil.json", "traversal"},
		{"../", "traversal"},
		{"pack.mcmeta", "duplicate"},
	} {
		t.Run(tc.problem, func(t *testing.T) {
			_, err := inspect(t, dataPack(t, entry{name: tc.name}), Data, Limits{})
			e := wantCode(t, err, CodeUnsafePath)
			if e.Params["problem"] != tc.problem || e.Params["entry"] != shortName(tc.name) {
				t.Errorf("%q: params %v", tc.name, e.Params)
			}
		})
	}

	_, err := inspect(t, dataPack(t, file("data/test/x", ""), dir("data/test/x/")), Data, Limits{})
	if e := wantCode(t, err, CodeUnsafePath); e.Params["problem"] != "duplicate" || e.Params["entry"] != "data/test/x/" {
		t.Errorf("a file and a folder of the same name: %v", e.Params)
	}
	_, err = inspect(t, dataPack(t, entry{name: "../evil.json"}), Data, Limits{})
	if e := wantCode(t, err, CodeUnsafePath); e.Msg != `The zip holds an entry whose path climbs out of the pack with "..", which could be written outside the folder the zip is unpacked into: "../evil.json".` {
		t.Errorf("traversal message = %q", e.Msg)
	}
	_, err = inspect(t, dataPack(t, entry{name: `data\x.json`}), Data, Limits{})
	if e := wantCode(t, err, CodeUnsafePath); !strings.Contains(e.Hint, "forward slashes") {
		t.Errorf("backslash hint = %q", e.Hint)
	}
}

func TestInspectEntryTypes(t *testing.T) {
	const folderMode = "it is marked as a folder, but its name doesn't end with a slash"
	for _, tc := range []struct {
		name  string
		e     entry
		code  string
		param string // the type of an unsupported entry, or what is corrupt
	}{
		{"symlink", entry{name: "data/test/link", body: "../../etc/passwd", edit: withMode(fs.ModeSymlink | 0o777)}, CodeUnsupportedEntry, "symlink"},
		{"pipe", entry{name: "data/test/fifo", edit: withMode(fs.ModeNamedPipe | 0o644)}, CodeUnsupportedEntry, "special"},
		{"device", entry{name: "data/test/tty", edit: withMode(fs.ModeDevice | fs.ModeCharDevice | 0o644)}, CodeUnsupportedEntry, "special"},
		{"socket", entry{name: "data/test/sock", edit: withMode(fs.ModeSocket | 0o644)}, CodeUnsupportedEntry, "special"},
		{"encrypted", entry{name: "data/test/secret.json", body: "{}", edit: func(fh *zip.FileHeader) { fh.Flags |= 0x1 }}, CodeEncrypted, ""},
		{"strongly encrypted", entry{name: "data/test/secret.json", body: "{}", edit: func(fh *zip.FileHeader) { fh.Flags |= 0x40 }}, CodeEncrypted, ""},
		{"masked header", entry{name: "data/test/secret.json", body: "{}", edit: func(fh *zip.FileHeader) { fh.Flags |= 0x2000 }}, CodeEncrypted, ""},
		{"unknown compression", entry{name: "data/test/x.json", body: "{}", edit: func(fh *zip.FileHeader) { fh.Method = 12 }}, CodeUnsupportedCompression, ""},
		{"folder mode without a slash", entry{name: "data/test/x", edit: withMode(fs.ModeDir | 0o755)}, CodeCorrupt, folderMode},
		{"MS-DOS folder without a slash", entry{name: "data/test/y", edit: func(fh *zip.FileHeader) { fh.CreatorVersion, fh.ExternalAttrs = 0, 0x10 }}, CodeCorrupt, folderMode},
		{"folder with data", entry{name: "data/test/", raw: []byte{}, edit: func(fh *zip.FileHeader) {
			fh.Method, fh.UncompressedSize64 = zip.Store, 5
		}}, CodeCorrupt, "it is a folder that holds data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := inspect(t, dataPack(t, tc.e), Data, Limits{})
			e := wantCode(t, err, tc.code)
			if e.Params["entry"] != tc.e.name {
				t.Errorf("entry %v, want %s", e.Params["entry"], tc.e.name)
			}
			switch tc.code {
			case CodeUnsupportedEntry:
				if e.Params["type"] != tc.param {
					t.Errorf("type %v, want %s", e.Params["type"], tc.param)
				}
			case CodeCorrupt:
				if e.Params["detail"] != tc.param {
					t.Errorf("detail %v, want %s", e.Params["detail"], tc.param)
				}
			case CodeUnsupportedCompression:
				if e.Params["method"] != uint16(12) {
					t.Errorf("method %v, want 12", e.Params["method"])
				}
			}
		})
	}
}

func TestInspectDamagedEntries(t *testing.T) {
	check := func(b []byte, entry, detail string) {
		t.Helper()
		_, err := inspect(t, b, Data, Limits{})
		if e := wantCode(t, err, CodeCorrupt); e.Params["entry"] != entry || e.Params["detail"] != detail {
			t.Errorf("damaged %s: %v, want %s", entry, e.Params, detail)
		}
	}

	b := dataPack(t, entry{name: "data/test/x.json", body: "stored content", edit: func(fh *zip.FileHeader) { fh.Method = zip.Store }})
	b[bytes.Index(b, []byte("stored content"))] ^= 1
	check(b, "data/test/x.json", "its checksum doesn't match its contents")

	b = dataPack(t, entry{name: "data/test/y.json", raw: []byte{0xff, 0xff, 0xff, 0xff}, edit: func(fh *zip.FileHeader) {
		fh.CompressedSize64, fh.UncompressedSize64 = 4, 10
	}})
	check(b, "data/test/y.json", "its compressed data is damaged")

	// A file that unpacks to more than its header says, as in zip bombs.
	var bomb bytes.Buffer
	fw, err := flate.NewWriter(&bomb, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(make([]byte, 1<<20)); err != nil || fw.Close() != nil {
		t.Fatal("compressing failed")
	}
	b = dataPack(t, entry{name: "data/test/bomb.json", raw: bomb.Bytes(), edit: func(fh *zip.FileHeader) {
		fh.CompressedSize64, fh.UncompressedSize64 = uint64(bomb.Len()), 10
		fh.CRC32 = crc32.ChecksumIEEE(make([]byte, 10))
	}})
	check(b, "data/test/bomb.json", "it is cut short or malformed")

	b = dataPack(t)
	b[0] = 'X' // the signature of pack.mcmeta's local header
	check(b, "pack.mcmeta", "it is cut short or malformed")
}

func TestInspectDirectory(t *testing.T) {
	pack := dataPack(t)
	end := len(pack) - directoryEndLen
	dirOffset := int(le32(pack[end+16:]))
	le := binary.LittleEndian
	patched := func(b []byte, patch func(b []byte)) []byte {
		b = slices.Clone(b)
		patch(b)
		return b
	}

	z := toZip64(t, pack)
	rec := len(z) - directoryEndLen - directory64LocLen - directory64EndLen
	loc := len(z) - directoryEndLen - directory64LocLen
	if zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z))); err != nil || len(zr.File) != 2 {
		t.Fatalf("archive/zip can't read the zip64 test zip: %v", err)
	}
	if info, err := inspect(t, z, Data, Limits{}); err != nil || info.Files != 2 {
		t.Errorf("zip64 end records: %+v, %v", info, err)
	}

	const (
		split     = "it is one part of a zip split into several files, which Minecraft can't read"
		misplaced = "its table of contents isn't where the zip says it is"
		malformed = "its table of contents is malformed"
		more      = "its table of contents lists more entries than it declares"
		fewer     = "its table of contents lists fewer entries than it declares"
	)
	for _, tc := range []struct {
		name   string
		b      []byte
		lim    Limits
		code   string
		detail string
	}{
		{"fewer entries than declared", patched(pack, func(b []byte) { le.PutUint16(b[end+8:], 3); le.PutUint16(b[end+10:], 3) }), Limits{}, CodeCorrupt, fewer},
		{"more entries than declared", patched(pack, func(b []byte) { le.PutUint16(b[end+8:], 1); le.PutUint16(b[end+10:], 1) }), Limits{}, CodeCorrupt, more},
		{"another disk", patched(pack, func(b []byte) { le.PutUint16(b[end+4:], 1) }), Limits{}, CodeCorrupt, split},
		{"directory on another disk", patched(pack, func(b []byte) { le.PutUint16(b[end+6:], 1) }), Limits{}, CodeCorrupt, split},
		{"entries on other disks", patched(pack, func(b []byte) { le.PutUint16(b[end+8:], 1) }), Limits{}, CodeCorrupt, split},
		{"too many entries", patched(pack, func(b []byte) { le.PutUint16(b[end+8:], 60000); le.PutUint16(b[end+10:], 60000) }), Limits{MaxFiles: 1000}, CodeTooManyFiles, ""},
		{"huge directory", patched(pack, func(b []byte) { le.PutUint32(b[end+12:], maxDirectoryBytes+1) }), Limits{}, CodeCorrupt, "its table of contents is larger than 32 MB"},
		{"directory past its end", patched(pack, func(b []byte) { le.PutUint32(b[end+16:], uint32(end+1)) }), Limits{}, CodeCorrupt, misplaced},
		{"directory size off", patched(pack, func(b []byte) { le.PutUint32(b[end+12:], le32(b[end+12:])-1) }), Limits{}, CodeCorrupt, misplaced},
		{"data before the zip", append([]byte("#!/bin/sh\nexit 0\n"), pack...), Limits{}, CodeCorrupt, misplaced},
		{"bad header signature", patched(pack, func(b []byte) { b[dirOffset] = 'X' }), Limits{}, CodeCorrupt, malformed},
		{"header past the directory", patched(pack, func(b []byte) { le.PutUint16(b[dirOffset+32:], 1000) }), Limits{}, CodeCorrupt, malformed},
		{"zip64 record damaged", patched(z, func(b []byte) { b[rec] = 'X' }), Limits{}, CodeCorrupt, "its zip64 end record is malformed"},
		{"zip64 record outside the file", patched(z, func(b []byte) { le.PutUint64(b[loc+8:], uint64(len(z))) }), Limits{}, CodeCorrupt, "its zip64 end record is outside the file"},
		{"zip64 another disk", patched(z, func(b []byte) { le.PutUint32(b[rec+16:], 1) }), Limits{}, CodeCorrupt, split},
		{"zip64 more entries than declared", patched(z, func(b []byte) { le.PutUint64(b[rec+24:], 1); le.PutUint64(b[rec+32:], 1) }), Limits{}, CodeCorrupt, more},
		{"zip64 too many entries", patched(z, func(b []byte) { le.PutUint64(b[rec+24:], 1<<40); le.PutUint64(b[rec+32:], 1<<40) }), Limits{}, CodeTooManyFiles, ""},
		{"no end record", pack[:end], Limits{}, CodeNotZip, ""},
		{"truncated comment", patched(pack, func(b []byte) { le.PutUint16(b[end+20:], 10) }), Limits{}, CodeNotZip, ""},
		{"random bytes", bytes.Repeat([]byte{0x5a, 0xc3, 0x11}, 100), Limits{}, CodeNotZip, ""},
		{"too short", []byte("PK\x05\x06"), Limits{}, CodeNotZip, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := inspect(t, tc.b, Data, tc.lim)
			e := wantCode(t, err, tc.code)
			if detail, _ := e.Params["detail"].(string); detail != tc.detail {
				t.Errorf("detail %q, want %q", detail, tc.detail)
			}
		})
	}
}

// archive/zip compares the number of entries it reads with the declared
// number only modulo 65,536.
func TestInspectEntryCountWrap(t *testing.T) {
	b := buildZip(t, file("a", ""))
	end := len(b) - directoryEndLen
	dirSize, dirOffset := int(le32(b[end+12:])), int(le32(b[end+16:]))
	z := slices.Clone(b[:dirOffset])
	for range 65537 {
		z = append(z, b[dirOffset:dirOffset+dirSize]...)
	}
	eocd := slices.Clone(b[end:])
	binary.LittleEndian.PutUint32(eocd[12:], uint32(65537*dirSize))
	z = append(z, eocd...)

	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		t.Fatalf("archive/zip refuses the zip (%v); the test expects it to read all its entries", err)
	}
	if len(zr.File) != 65537 {
		t.Fatalf("archive/zip read %d entries; the test expects all 65,537", len(zr.File))
	}
	_, err = inspect(t, z, Data, Limits{})
	if e := wantCode(t, err, CodeCorrupt); e.Params["detail"] != "its table of contents lists more entries than it declares" {
		t.Errorf("detail %v", e.Params["detail"])
	}
}

func TestInspectCanceled(t *testing.T) {
	pack := dataPack(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Inspect(ctx, bytes.NewReader(pack), int64(len(pack)), Data, Limits{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Inspect with a canceled context = %v", err)
	}
}

func TestStage(t *testing.T) {
	dir := t.TempDir()
	pack := dataPack(t)
	f, n, err := Stage(dir, bytes.NewReader(pack), Data, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := io.ReadAll(io.NewSectionReader(f, 0, n))
	if err != nil || n != int64(len(pack)) || !bytes.Equal(got, pack) {
		t.Errorf("Stage stored %d bytes, %v", n, err)
	}
	if _, err := Inspect(context.Background(), f, n, Data, Limits{}); err != nil {
		t.Errorf("Inspect of the staged upload: %v", err)
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Errorf("Stage left %q in its folder", names)
	}

	_, _, err = Stage(dir, bytes.NewReader(make([]byte, 101)), Data, Limits{MaxBytes: 100})
	if e := wantCode(t, err, CodeTooLarge); e.Msg != "The data pack is larger than 100 bytes, the most Playkeeper accepts." {
		t.Errorf("too large: %q", e.Msg)
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Errorf("Stage left %q in its folder", names)
	}

	_, _, err = Stage(filepath.Join(dir, "missing"), bytes.NewReader(pack), Data, Limits{})
	wantCode(t, err, CodeFileFailed)
	_, _, err = Stage(dir, iotest.ErrReader(errors.New("connection reset")), Data, Limits{})
	if e := wantCode(t, err, CodeFileFailed); e.Msg != "Playkeeper couldn't store the upload: connection reset." {
		t.Errorf("failed upload: %q", e.Msg)
	}
}
