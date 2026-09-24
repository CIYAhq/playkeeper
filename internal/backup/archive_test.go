package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtureDataDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	region := make([]byte, 200_000)
	rand.Read(region)
	write(t, d, "server.properties", "level-name=world\nrcon.password=hunter2-secret\nmanagement-server-secret=abc123secret\nonline-mode=true\nmotd=Test\n")
	write(t, d, "whitelist.json", `[{"uuid":"x","name":"PkBotFriend"}]`)
	write(t, d, "world/level.dat", "level-data")
	write(t, d, "world/region/r.0.0.mca", string(region))
	write(t, d, "world_nether/DIM-1/region/r.0.0.mca", "nether")
	write(t, d, "config/paper-global.yml", "paper: true")
	write(t, d, "plugins/.paper-remapped/cache.jar", "remapped")
	write(t, d, "plugins/spark/config.json", "{}")
	write(t, d, "paper-26.1.2-74.jar", "SERVER JAR")
	write(t, d, "libraries/lib.jar", "LIB")
	write(t, d, "versions/26.1.2/server.jar", "MOJANG")
	write(t, d, ".rcon-cli.env", "password=hunter2-secret")
	write(t, d, "eula.txt", "eula=true")
	write(t, d, "logs/latest.log", "log")
	return d
}

func createArchive(t *testing.T, dataDir string) ([]byte, Manifest) {
	t.Helper()
	var buf bytes.Buffer
	m, err := Create(&buf, dataDir, Manifest{CreatedAt: time.Now().UTC(), MinecraftVersion: "26.1.2", VersionID: "paper-26.1.2", PaperBuild: 74})
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), m
}

func TestRoundTripAllowlistAndSecrets(t *testing.T) {
	src := fixtureDataDir(t)
	arch, m := createArchive(t, src)
	if m.LevelName != "world" || m.Format != FormatVersion {
		t.Fatalf("manifest: %+v", m)
	}
	got := map[string]bool{}
	for _, f := range m.Files {
		got[f.Path] = true
	}
	for _, want := range []string{"server.properties", "whitelist.json", "world/level.dat", "world/region/r.0.0.mca", "world_nether/DIM-1/region/r.0.0.mca", "config/paper-global.yml", "plugins/spark/config.json"} {
		if !got[want] {
			t.Errorf("archive is missing %s", want)
		}
	}
	for _, never := range []string{"paper-26.1.2-74.jar", "libraries/lib.jar", "versions/26.1.2/server.jar", ".rcon-cli.env", "eula.txt", "logs/latest.log", "plugins/.paper-remapped/cache.jar"} {
		if got[never] {
			t.Errorf("archive must not contain %s", never)
		}
	}
	if bytes.Contains(arch, []byte("hunter2")) {
		t.Fatal("compressed archive leaks the RCON password")
	}
	vm, err := Verify(bytes.NewReader(arch), DefaultLimits())
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(vm.Files) != len(m.Files) {
		t.Fatal("verify returned a different manifest")
	}
	dest := filepath.Join(t.TempDir(), "restored")
	if _, err := Extract(bytes.NewReader(arch), dest, DefaultLimits()); err != nil {
		t.Fatalf("extract: %v", err)
	}
	for _, f := range m.Files {
		b, err := os.ReadFile(filepath.Join(dest, f.Path))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Errorf("%s restored with wrong content", f.Path)
		}
	}
	props, _ := os.ReadFile(filepath.Join(dest, "server.properties"))
	if strings.Contains(string(props), "rcon.password") || strings.Contains(string(props), "management-server-secret") {
		t.Fatalf("secrets not stripped: %s", props)
	}
	if !strings.Contains(string(props), "online-mode=true") {
		t.Fatal("non-secret settings must be kept")
	}
	if _, err := Extract(bytes.NewReader(arch), dest, DefaultLimits()); err == nil {
		t.Fatal("extracting over an existing directory must fail")
	}
}

func TestCreateRefusesMissingWorld(t *testing.T) {
	d := t.TempDir()
	write(t, d, "server.properties", "level-name=world\n")
	if _, err := Create(&bytes.Buffer{}, d, Manifest{}); err == nil {
		t.Fatal("archiving a data dir without a world must fail")
	}
}

func TestFlippedByteIsRefused(t *testing.T) {
	arch, _ := createArchive(t, fixtureDataDir(t))
	for _, pos := range []int{len(arch) / 3, len(arch) / 2, len(arch) - 20} {
		bad := append([]byte(nil), arch...)
		bad[pos] ^= 0x40
		if _, err := Verify(bytes.NewReader(bad), DefaultLimits()); err == nil {
			t.Fatalf("archive with a flipped byte at %d was accepted", pos)
		}
	}
}

func TestTruncatedArchiveIsRefused(t *testing.T) {
	arch, _ := createArchive(t, fixtureDataDir(t))
	for _, n := range []int{10, len(arch) / 2, len(arch) - 5} {
		if _, err := Verify(bytes.NewReader(arch[:n]), DefaultLimits()); err == nil {
			t.Fatalf("archive truncated to %d bytes was accepted", n)
		}
	}
}

type entry struct {
	name     string
	body     string
	typeflag byte
	link     string
}

func craft(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		tf := e.typeflag
		if tf == 0 {
			tf = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tf, Linkname: e.link, ModTime: time.Now()}
		if tf != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if tf == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func manifestFor(files map[string]string) string {
	m := Manifest{Format: FormatVersion, LevelName: "world", MinecraftVersion: "26.1.2"}
	for p, body := range files {
		sum := sha256.Sum256([]byte(body))
		m.Files = append(m.Files, FileEntry{Path: p, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])})
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func TestMaliciousArchivesAreRefused(t *testing.T) {
	good := map[string]string{"world/level.dat": "ok"}
	cases := map[string][]entry{
		"dot-dot traversal": {
			{name: dataPrefix + "../../etc/passwd", body: "x"},
			{name: manifestName, body: manifestFor(map[string]string{"../../etc/passwd": "x"})},
		},
		"absolute path": {
			{name: "/etc/passwd", body: "x"},
			{name: manifestName, body: manifestFor(good)},
		},
		"outside data prefix": {
			{name: "playkeeper-backup/evil.sh", body: "x"},
			{name: manifestName, body: manifestFor(good)},
		},
		"symlink": {
			{name: dataPrefix + "world/level.dat", typeflag: tar.TypeSymlink, link: "/etc/shadow"},
			{name: manifestName, body: manifestFor(good)},
		},
		"hardlink": {
			{name: dataPrefix + "world/level.dat", typeflag: tar.TypeLink, link: "/etc/shadow"},
			{name: manifestName, body: manifestFor(good)},
		},
		"missing manifest": {
			{name: dataPrefix + "world/level.dat", body: "ok"},
		},
		"file not in manifest": {
			{name: dataPrefix + "world/level.dat", body: "ok"},
			{name: dataPrefix + "world/extra.dat", body: "sneaky"},
			{name: manifestName, body: manifestFor(good)},
		},
		"tampered content": {
			{name: dataPrefix + "world/level.dat", body: "tampered"},
			{name: manifestName, body: manifestFor(good)},
		},
		"entry after manifest": {
			{name: dataPrefix + "world/level.dat", body: "ok"},
			{name: manifestName, body: manifestFor(good)},
			{name: dataPrefix + "world/late.dat", body: "late"},
		},
		"duplicate entry": {
			{name: dataPrefix + "world/level.dat", body: "ok"},
			{name: dataPrefix + "world/level.dat", body: "ok"},
			{name: manifestName, body: manifestFor(good)},
		},
		"unknown manifest field": {
			{name: dataPrefix + "world/level.dat", body: "ok"},
			{name: manifestName, body: strings.Replace(manifestFor(good), `{"format"`, `{"exec":"rm -rf /","format"`, 1)},
		},
		"future format": {
			{name: dataPrefix + "world/level.dat", body: "ok"},
			{name: manifestName, body: strings.Replace(manifestFor(good), `"format":1`, `"format":99`, 1)},
		},
		"no world": {
			{name: dataPrefix + "server.properties", body: "x"},
			{name: manifestName, body: manifestFor(map[string]string{"server.properties": "x"})},
		},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			arch := craft(t, entries)
			if _, err := Verify(bytes.NewReader(arch), DefaultLimits()); err == nil {
				t.Fatal("Verify accepted a malicious archive")
			}
			dest := filepath.Join(t.TempDir(), "x")
			if _, err := Extract(bytes.NewReader(arch), dest, DefaultLimits()); err == nil {
				t.Fatal("Extract accepted a malicious archive")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "etc")); err == nil {
				t.Fatal("extraction escaped its destination")
			}
		})
	}
}

func TestLimitsAreEnforced(t *testing.T) {
	arch, _ := createArchive(t, fixtureDataDir(t))
	lim := DefaultLimits()
	lim.MaxFiles = 2
	if _, err := Verify(bytes.NewReader(arch), lim); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("file-count limit not enforced: %v", err)
	}
	lim = DefaultLimits()
	lim.MaxTotalBytes = 1000
	if _, err := Verify(bytes.NewReader(arch), lim); err == nil {
		t.Fatal("size limit not enforced")
	}
}

func TestNotGzip(t *testing.T) {
	if _, err := Verify(strings.NewReader("PK\x03\x04 zip file"), DefaultLimits()); err == nil {
		t.Fatal("non-gzip input accepted")
	}
}
