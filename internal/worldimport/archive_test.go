package worldimport

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"strings"
	"testing"
)

func TestInspectRefusesUnsafeEntries(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	world := withPrefix("world/", legacyWorldFiles(lv, true, true))
	cases := []struct {
		name  string
		extra []tf
		want  string
	}{
		{"traversal", []tf{f("../evil.sh", "x")}, KindUnsafePath},
		{"nested traversal", []tf{f("world/../../evil.sh", "x")}, KindUnsafePath},
		{"absolute path", []tf{f("/etc/cron.d/evil", "x")}, KindUnsafePath},
		{"drive letter", []tf{f("C:/Windows/evil.bat", "x")}, KindUnsafePath},
		{"backslash traversal", []tf{f(`world\..\..\evil.sh`, "x")}, KindUnsafePath},
		{"direction override", []tf{f("world/\u202egpj.exe", "x")}, KindUnsafePath},
		{"control character", []tf{f("world/a\x1bb", "x")}, KindUnsafePath},
		{"file without a name", []tf{f(".", "x")}, KindUnsafePath},
		{"symlink", []tf{{name: "world/playerdata/evil.dat", link: "/etc/shadow"}}, KindLink},
		{"path too long", []tf{f(strings.Repeat("deep/", 210)+"x", "x")}, KindPathTooLong},
		{"name too long", []tf{f("world/"+strings.Repeat("a", 256), "x")}, KindPathTooLong},
		{"file and folder", []tf{f("world/stats", "x")}, KindDuplicate},
	}
	for _, c := range cases {
		files := join(world, c.extra)
		t.Run("zip "+c.name, func(t *testing.T) {
			_, err := Inspect(context.Background(), []Source{upload(t, "world.zip", zipBytes(t, files))}, Limits{})
			if got := refusalKind(t, err); got != c.want {
				t.Fatalf("got %s, want %s (%v)", got, c.want, err)
			}
		})
		t.Run("tar.gz "+c.name, func(t *testing.T) {
			_, err := Inspect(context.Background(), []Source{upload(t, "world.tar.gz", gzipBytes(t, tarBytes(t, files)))}, Limits{})
			if got := refusalKind(t, err); got != c.want {
				t.Fatalf("got %s, want %s (%v)", got, c.want, err)
			}
		})
	}
}

func TestInspectRefusesTarLinksAndSpecialFiles(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	cases := []struct {
		name string
		e    tf
		want string
	}{
		{"hard link", tf{name: "world/level.dat_old", hard: "/etc/passwd"}, KindLink},
		{"fifo", tf{name: "world/pipe", typ: tar.TypeFifo}, KindSpecialFile},
		{"character device", tf{name: "world/tty", typ: tar.TypeChar}, KindSpecialFile},
		{"block device", tf{name: "world/sda", typ: tar.TypeBlock}, KindSpecialFile},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := []tf{f("world/level.dat", lv), c.e}
			_, err := Inspect(context.Background(), []Source{upload(t, "world.tar", tarBytes(t, files))}, Limits{})
			if got := refusalKind(t, err); got != c.want {
				t.Fatalf("got %s, want %s (%v)", got, c.want, err)
			}
		})
	}
}

func TestInspectRefusesDuplicateEntries(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	files := []tf{f("world/level.dat", lv), f("world/region/r.0.0.mca", "a"), f("./world/region/r.0.0.mca", "b")}
	_, err := Inspect(context.Background(), []Source{upload(t, "world.tar", tarBytes(t, files))}, Limits{})
	if got := refusalKind(t, err); got != KindDuplicate {
		t.Fatalf("got %s, want %s", got, KindDuplicate)
	}
}

type rawEntry struct {
	name   string
	data   string
	method uint16
	flags  uint16
}

// rawZip writes entries exactly as given, whatever their method and flags.
func rawZip(t *testing.T, entries []rawEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.CreateRaw(&zip.FileHeader{
			Name: e.name, Method: e.method, Flags: e.flags, CRC32: crc32.ChecksumIEEE([]byte(e.data)),
			CompressedSize64: uint64(len(e.data)), UncompressedSize64: uint64(len(e.data)),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInspectRefusesCraftedZips(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	base := []rawEntry{{name: "world/level.dat", data: lv}, {name: "world/region/r.0.0.mca", data: regionData}, {name: "world/region/r.1.0.mca", data: strings.ToUpper(regionData)}}

	many := append([]rawEntry(nil), base...)
	for i := 0; i < 30; i++ {
		many = append(many, rawEntry{name: "world/stats/" + strings.Repeat("s", i+1) + ".json", data: "{}"})
	}

	cases := []struct {
		name  string
		build func(t *testing.T) []byte
		lim   Limits
		want  string
	}{
		{"encrypted entry", func(t *testing.T) []byte {
			return rawZip(t, append(base, rawEntry{name: "world/secret.dat", data: "xxxx", flags: 0x1}))
		}, Limits{}, KindEncrypted},
		{"bzip2 entry", func(t *testing.T) []byte {
			return rawZip(t, append(base, rawEntry{name: "world/data/raids.dat", data: "BZh9", method: 12}))
		}, Limits{}, KindCompression},
		{"entries sharing data", func(t *testing.T) []byte {
			b := rawZip(t, base)
			cd := zipCentral(t, b)
			// The third entry now points at the second entry's data.
			copy(b[cd[2]+42:], b[cd[1]+42:cd[1]+46])
			return b
		}, Limits{}, KindOverlap},
		{"data past the end", func(t *testing.T) []byte {
			b := rawZip(t, base)
			cd := zipCentral(t, b)
			binary.LittleEndian.PutUint32(b[cd[1]+20:], 1<<30)
			return b
		}, Limits{}, KindArchiveTruncated},
		{"declares too many entries", func(t *testing.T) []byte {
			b := rawZip(t, base)
			end := zipEOCD(t, b)
			binary.LittleEndian.PutUint16(b[end+8:], 5000)
			binary.LittleEndian.PutUint16(b[end+10:], 5000)
			return b
		}, Limits{MaxEntries: 100}, KindTooManyEntries},
		{"hides entries from its count", func(t *testing.T) []byte {
			b := rawZip(t, many)
			end := zipEOCD(t, b)
			binary.LittleEndian.PutUint16(b[end+8:], 3)
			binary.LittleEndian.PutUint16(b[end+10:], 3)
			return b
		}, Limits{MaxEntries: 10}, KindTooManyEntries},
		{"cut off download", func(t *testing.T) []byte {
			b := rawZip(t, many)
			return b[:len(b)*6/10]
		}, Limits{}, KindArchiveCorrupt},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Inspect(context.Background(), []Source{upload(t, "world.zip", c.build(t))}, c.lim)
			if got := refusalKind(t, err); got != c.want {
				t.Fatalf("got %s, want %s (%v)", got, c.want, err)
			}
		})
	}
}

func TestInspectRefusesDamagedTarGz(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	good := gzipBytes(t, tarBytes(t, withPrefix("world/", legacyWorldFiles(lv, true, true))))

	cut := good[:len(good)*6/10]
	badCRC := append([]byte(nil), good...)
	badCRC[len(badCRC)-8] ^= 0xff
	notTar := gzipBytes(t, []byte(strings.Repeat("not a tar archive ", 100)))

	for name, c := range map[string]struct {
		data []byte
		want string
	}{
		"cut off":        {cut, KindArchiveTruncated},
		"wrong checksum": {badCRC, KindArchiveCorrupt},
		"gzip of text":   {notTar, KindArchiveFormat},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Inspect(context.Background(), []Source{upload(t, "world.tar.gz", c.data)}, Limits{})
			if got := refusalKind(t, err); got != c.want {
				t.Fatalf("got %s, want %s (%v)", got, c.want, err)
			}
		})
	}
}

func TestInspectRefusesTarGzBomb(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	files := []tf{f("world/level.dat", lv), f("world/region/r.0.0.mca", strings.Repeat("\x00", 8<<20))}
	_, err := Inspect(context.Background(), []Source{upload(t, "world.tar.gz", gzipBytes(t, tarBytes(t, files)))}, Limits{})
	if got := refusalKind(t, err); got != KindRatio {
		t.Fatalf("got %s, want %s", got, KindRatio)
	}
}

func TestInspectLimitsEntries(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	files := withPrefix("world/", legacyWorldFiles(lv, true, true))
	for _, src := range []Source{
		upload(t, "world.zip", zipBytes(t, files)),
		upload(t, "world.tar.gz", gzipBytes(t, tarBytes(t, files))),
	} {
		_, err := Inspect(context.Background(), []Source{src}, Limits{MaxEntries: 5})
		if got := refusalKind(t, err); got != KindTooManyEntries {
			t.Errorf("%s: got %s, want %s", src.Name, got, KindTooManyEntries)
		}
	}
}

func TestInspectChecksUploadNamesAndFormats(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	zipped := zipBytes(t, []tf{f("world/level.dat", lv)})
	cases := []struct {
		name, file string
		data       []byte
		want       string
		format     string
	}{
		{"slash in name", "saves/world.zip", zipped, KindArchiveName, ""},
		{"control character in name", "world\x07.zip", zipped, KindArchiveName, ""},
		{"overlong name", strings.Repeat("w", 252) + ".zip", zipped, KindArchiveName, ""},
		{"no name", "", zipped, KindArchiveName, ""},
		{"bedrock world file", "My World.mcworld", zipped, KindBedrock, ""},
		{"rar by name", "world.rar", zipped, KindArchiveFormat, "RAR"},
		{"tar.xz by name", "world.tar.xz", zipped, KindArchiveFormat, "tar.xz"},
		{"rar by content", "world.zip", []byte("Rar!\x1a\x07\x01\x00rest of a rar file"), KindArchiveFormat, "RAR"},
		{"7z by content", "world.zip", []byte("7z\xbc\xaf\x27\x1c\x00\x04rest"), KindArchiveFormat, "7z"},
		{"not an archive", "world.zip", []byte("<html>Download failed</html>"), KindArchiveFormat, ""},
		{"empty file", "world.zip", nil, KindArchiveCorrupt, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Inspect(context.Background(), []Source{upload(t, c.file, c.data)}, Limits{})
			if got := refusalKind(t, err); got != c.want {
				t.Fatalf("got %s, want %s (%v)", got, c.want, err)
			}
			var e *Error
			errors.As(err, &e)
			if c.want == KindArchiveFormat && e.Params["format"] != c.format {
				t.Errorf("format = %v, want %q", e.Params["format"], c.format)
			}
		})
	}
}

func TestInspectLimitsArchiveCount(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	src := upload(t, "world.zip", zipBytes(t, []tf{f("level.dat", lv)}))
	sources := make([]Source, maxArchives+1)
	for i := range sources {
		sources[i] = src
	}
	_, err := Inspect(context.Background(), sources, Limits{})
	if got := refusalKind(t, err); got != KindTooManyArchives {
		t.Fatalf("got %s, want %s", got, KindTooManyArchives)
	}
	if _, err := Inspect(context.Background(), nil, Limits{}); err == nil {
		t.Fatal("Inspect without archives succeeded")
	}
}

func TestInspectStopsWhenCancelled(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	files := withPrefix("world/", legacyWorldFiles(lv, false, false))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, src := range []Source{
		upload(t, "world.zip", zipBytes(t, files)),
		upload(t, "world.tar.gz", gzipBytes(t, tarBytes(t, files))),
	} {
		if _, err := Inspect(ctx, []Source{src}, Limits{}); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: got %v, want context.Canceled", src.Name, err)
		}
	}
}

func TestInspectSkipsGlobalTarHeaders(t *testing.T) {
	lv := levelDat(t, legacyLevel("world", "1.21.4", 4189))
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": "made by git archive"}}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "world/level.dat", Size: int64(len(lv)), Mode: 0o644}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(lv)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	in := inspect(t, Limits{}, upload(t, "world.tar", buf.Bytes()))
	if in.Archives[0].Format != FormatTar || in.Archives[0].Entries != 2 || len(in.Worlds) != 1 {
		t.Fatalf("archives %+v, worlds %d", in.Archives, len(in.Worlds))
	}
}

func TestClipMakesNamesSafeToShow(t *testing.T) {
	cases := map[string]string{
		"world":                    "world",
		"evil\u202egpj.exe":        "evil\ufffdgpj.exe",
		"a\x1b[31mred":             "a\ufffd[31mred",
		"bad\xffutf8":              "bad\ufffdutf8",
		strings.Repeat("x", 100):   strings.Repeat("x", 38) + "…" + strings.Repeat("x", 38),
		"\u2066hidden\u2069 names": "\ufffdhidden\ufffd names",
	}
	for in, want := range cases {
		if got := clip(in); got != want {
			t.Errorf("clip(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{0: "0 bytes", 1: "1 byte", 999: "999 bytes", 1000: "1 KB", 1500: "1.5 KB", 820_000_000: "820 MB", 64 << 30: "68.7 GB"}
	for n, want := range cases {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
