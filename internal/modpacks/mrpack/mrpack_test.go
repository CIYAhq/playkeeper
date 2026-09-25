package mrpack

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readIndex(t *testing.T, pack string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "packs", pack, IndexName))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseRealIndexes(t *testing.T) {
	cases := []struct {
		pack, name, version, mc, loader, loaderVersion string
		files                                          int
		server                                         map[Support]int
	}{
		{"adrenaline", "Adrenaline", "26.5.0+mc26.2.fabric", "26.2", FabricLoader, "0.19.5", 30,
			map[Support]int{Required: 19, Unsupported: 11}},
		{"vanilla-perfected-1.0.3", "Vanilla Perfected", "1.0.3+26.1.2", "26.1.2", FabricLoader, "0.19.2", 10,
			map[Support]int{Required: 10}},
		{"create-plus-6.0.0-alpha-f", "Create+", "6.0.0 Alpha f", "1.21.1", NeoForge, "21.1.233", 11,
			map[Support]int{Required: 4, Unsupported: 3, Optional: 3, "unknown": 1}},
		{"create-plus-5.2.1b", "Create+", "5.2.1b", "1.19.2", Forge, "43.5.1", 3,
			map[Support]int{Required: 3}},
		{"csmp-1.5", "CSMP 1.5 Release", "1.19.2", "1.19.2", QuiltLoader, "0.20.0-beta.4", 5,
			map[Support]int{Required: 3, Unsupported: 2}},
	}
	for _, c := range cases {
		ix, err := Parse(readIndex(t, c.pack))
		if err != nil {
			t.Fatalf("%s: %v", c.pack, err)
		}
		if ix.Name != c.name || ix.VersionID != c.version || ix.Dependencies[Minecraft] != c.mc || len(ix.Files) != c.files {
			t.Errorf("%s: got %q %q mc %q with %d files", c.pack, ix.Name, ix.VersionID, ix.Dependencies[Minecraft], len(ix.Files))
		}
		id, v, err := ix.Loader()
		if err != nil || id != c.loader || v != c.loaderVersion {
			t.Errorf("%s: Loader() = %q %q %v", c.pack, id, v, err)
		}
		got := map[Support]int{}
		for _, f := range ix.Files {
			got[f.Server()]++
			if len(f.Hashes.SHA1) != 40 || len(f.Hashes.SHA512) != 128 || len(f.Downloads) == 0 || f.FileSize <= 0 {
				t.Errorf("%s: %s has %+v", c.pack, f.Path, f)
			}
		}
		for s, n := range c.server {
			if got[s] != n {
				t.Errorf("%s: %d files with server %q, want %d (all: %v)", c.pack, got[s], s, n, got)
			}
		}
	}
}

func TestOutOfFormatEnvironmentIsReportedAsIs(t *testing.T) {
	ix, err := Parse(readIndex(t, "create-plus-6.0.0-alpha-f"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range ix.Files {
		if strings.Contains(f.Path, "statuseffectbars") {
			if s := f.Server(); s != "unknown" || s.Valid() {
				t.Errorf("Server() = %q, Valid() = %v", s, s.Valid())
			}
			return
		}
	}
	t.Fatal("the fixture lost its file with an unknown environment")
}

func TestFileWithoutEnvIsRequired(t *testing.T) {
	f := File{}
	if f.Server() != Required {
		t.Errorf("Server() = %q", f.Server())
	}
}

// mutate parses the Adrenaline index as generic JSON, lets change edit it and
// returns it encoded again.
func mutate(t *testing.T, change func(ix map[string]any, first map[string]any)) []byte {
	t.Helper()
	var ix map[string]any
	if err := json.Unmarshal(readIndex(t, "adrenaline"), &ix); err != nil {
		t.Fatal(err)
	}
	change(ix, ix["files"].([]any)[0].(map[string]any))
	b, err := json.Marshal(ix)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseRefusesMalformedIndexes(t *testing.T) {
	cases := []struct {
		name   string
		change func(ix, f map[string]any)
		unsafe bool
		reason string
	}{
		{"newer format", func(ix, f map[string]any) { ix["formatVersion"] = 2 }, false, "format version 2"},
		{"other game", func(ix, f map[string]any) { ix["game"] = "terraria" }, false, "not Minecraft"},
		{"no version id", func(ix, f map[string]any) { ix["versionId"] = " " }, false, "versionId"},
		{"no name", func(ix, f map[string]any) { delete(ix, "name") }, false, "no name"},
		{"no minecraft", func(ix, f map[string]any) { delete(ix["dependencies"].(map[string]any), "minecraft") }, false, "Minecraft version"},
		{"odd loader version", func(ix, f map[string]any) { ix["dependencies"].(map[string]any)["fabric-loader"] = "0.19; rm -rf /" }, false, "malformed dependency"},
		{"no sha1", func(ix, f map[string]any) { delete(f["hashes"].(map[string]any), "sha1") }, false, "sha1"},
		{"short sha512", func(ix, f map[string]any) { f["hashes"].(map[string]any)["sha512"] = "abcd" }, false, "sha512"},
		{"negative size", func(ix, f map[string]any) { f["fileSize"] = -1 }, false, "negative"},
		{"no downloads", func(ix, f map[string]any) { f["downloads"] = []any{} }, false, "no download"},
		{"plain http", func(ix, f map[string]any) {
			f["downloads"] = []any{"http://cdn.modrinth.com/data/x/versions/y/a.jar"}
		}, false, "HTTPS"},
		{"unencoded space", func(ix, f map[string]any) {
			f["downloads"] = []any{"https://cdn.modrinth.com/data/x/versions/y/a b.jar"}
		}, false, "HTTPS"},
		{"duplicate path", func(ix, f map[string]any) {
			files := ix["files"].([]any)
			files[1].(map[string]any)["path"] = f["path"]
		}, false, "more than once"},
		{"traversal", func(ix, f map[string]any) { f["path"] = "mods/../../../etc/cron.d/x" }, true, ".."},
		{"absolute", func(ix, f map[string]any) { f["path"] = "/etc/passwd" }, true, "absolute"},
		{"drive letter", func(ix, f map[string]any) { f["path"] = "C:/Windows/x.jar" }, true, "drive"},
		{"backslash", func(ix, f map[string]any) { f["path"] = `mods\..\..\x.jar` }, true, "backslash"},
		{"not json", nil, false, "not valid JSON"},
	}
	for _, c := range cases {
		b := []byte(`{"formatVersion":1,`)
		if c.change != nil {
			b = mutate(t, c.change)
		}
		_, err := Parse(b)
		var fe *FormatError
		var pe *PathError
		switch {
		case err == nil:
			t.Errorf("%s: accepted", c.name)
		case c.unsafe && !errors.As(err, &pe):
			t.Errorf("%s: err = %v, want a PathError", c.name, err)
		case !c.unsafe && !errors.As(err, &fe):
			t.Errorf("%s: err = %v, want a FormatError", c.name, err)
		case !strings.Contains(err.Error(), c.reason):
			t.Errorf("%s: %q does not mention %q", c.name, err, c.reason)
		}
	}
}

func TestParseRefusesInvalidUTF8(t *testing.T) {
	if _, err := Parse([]byte("{\"name\":\"\xff\"}")); err == nil {
		t.Error("accepted")
	}
}

func TestParseNormalisesHashes(t *testing.T) {
	b := mutate(t, func(ix, f map[string]any) {
		h := f["hashes"].(map[string]any)
		h["sha512"] = strings.ToUpper(h["sha512"].(string))
	})
	ix, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if h := ix.Files[0].Hashes.SHA512; h != strings.ToLower(h) {
		t.Errorf("hash kept as %s", h)
	}
}

func TestCheckPath(t *testing.T) {
	ok := []string{
		"mods/fabric-api-0.152.1+26.1.2.jar",
		"config/ichunutil/themes/blue&black.json",
		"config/litematica/litematica_Mom's House.json",
		"resourcepacks/§9Drodi's Illagers x FA [v5.2].zip.rpo",
		"mods/.connector/temp/enchancement-1.21-r14$tooltipfix-1.1.1-1.20.jar",
		"shaderpacks/I Like Vanilla v1.3.6b.zip",
		".qmenu_opened.marker",
		"options.txt",
	}
	for _, p := range ok {
		if err := CheckPath(p); err != nil {
			t.Errorf("CheckPath(%q) = %v", p, err)
		}
	}
	bad := []string{
		"", "/etc/passwd", "../x", "mods/../../x", "mods/..", "./mods/x.jar", "mods/./x.jar", "mods//x.jar", "mods/",
		`mods\x.jar`, `\\server\share\x`, "C:/x.jar", "c:x.jar", "mods/x.jar:stream",
		"mods/x\x00.jar", "mods/x\n.jar", "mods/\u202egpj.jar", "mods/zero\u200bwidth.jar", "mods/\xff.jar",
		"mods/" + strings.Repeat("a", 256), strings.Repeat("a/", 600) + "x",
	}
	for _, p := range bad {
		err := CheckPath(p)
		var pe *PathError
		if !errors.As(err, &pe) {
			t.Errorf("CheckPath(%q) = %v, want a PathError", p, err)
			continue
		}
		if strings.ContainsAny(err.Error(), "\x00\n\u202e") {
			t.Errorf("CheckPath(%q) message carries raw control characters: %q", p, err)
		}
	}
}

func TestLoader(t *testing.T) {
	cases := []struct {
		deps    map[string]string
		id, ver string
		err     bool
	}{
		{map[string]string{"minecraft": "1.21.1"}, "", "", false},
		{map[string]string{"minecraft": "1.21.1", "neoforge": "21.1.77"}, NeoForge, "21.1.77", false},
		{map[string]string{"minecraft": "1.21.1", "babric": "0.1"}, "babric", "0.1", false},
		{map[string]string{"minecraft": "1.21.1", "fabric-loader": "0.16.0", "quilt-loader": "0.26.0"}, "", "", true},
	}
	for _, c := range cases {
		ix := &Index{Dependencies: c.deps}
		id, ver, err := ix.Loader()
		if id != c.id || ver != c.ver || (err != nil) != c.err {
			t.Errorf("Loader(%v) = %q %q %v", c.deps, id, ver, err)
		}
	}
}

func TestHostsAreTheFormatsListAndFreshEachCall(t *testing.T) {
	h := Hosts()
	if strings.Join(h, ",") != "cdn.modrinth.com,github.com,raw.githubusercontent.com,gitlab.com" {
		t.Errorf("Hosts() = %v", h)
	}
	h[0] = "evil.example"
	if Hosts()[0] != "cdn.modrinth.com" {
		t.Error("changing the returned list changed the next one")
	}
}
