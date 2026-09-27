package addons

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons/hangar"
	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
	"github.com/CIYAhq/playkeeper/internal/version"
)

var wantUserAgent = "CIYAhq/playkeeper/" + version.Version + " (https://github.com/CIYAhq/playkeeper)"

func TestTargetFor(t *testing.T) {
	for _, tc := range []struct {
		typ, name, kind, folder string
		loaders                 []string
		sources                 []Source
	}{
		{"paper", "Paper", "plugin", "plugins", []string{"paper", "spigot", "bukkit"}, []Source{Modrinth, Hangar}},
		{"purpur", "Purpur", "plugin", "plugins", []string{"purpur", "paper", "spigot", "bukkit"}, []Source{Modrinth, Hangar}},
		{"fabric", "Fabric", "mod", "mods", []string{"fabric"}, []Source{Modrinth}},
		{"quilt", "Quilt", "mod", "mods", []string{"quilt", "fabric"}, []Source{Modrinth}},
		{"neoforge", "NeoForge", "mod", "mods", []string{"neoforge"}, []Source{Modrinth}},
	} {
		tg, err := TargetFor(tc.typ)
		if err != nil {
			t.Errorf("TargetFor(%q): %v", tc.typ, err)
			continue
		}
		if tg.Name() != tc.name || tg.Kind != tc.kind || tg.Folder != tc.folder || !slices.Equal(tg.Loaders, tc.loaders) || !slices.Equal(tg.Sources(), tc.sources) {
			t.Errorf("TargetFor(%q) = %+v, named %q, sources %v", tc.typ, tg, tg.Name(), tg.Sources())
		}
	}

	_, err := TargetFor("vanilla")
	if e := wantKind(t, err, KindNoAddons); e.Msg != "Vanilla servers cannot load plugins or mods." || e.Hint == "" {
		t.Errorf("vanilla: %q, hint %q", e.Msg, e.Hint)
	}
	for _, typ := range []string{"forge", "velocity", "", "PAPER"} {
		_, err := TargetFor(typ)
		wantKind(t, err, KindUnknownServerType)
	}
	_, err = TargetFor("paper\n")
	if e := wantKind(t, err, KindUnknownServerType); e.Msg != `Playkeeper does not know the server type "paper?", so it cannot tell which add-ons fit.` {
		t.Errorf("message %q", e.Msg)
	}
}

func TestValidFileName(t *testing.T) {
	for _, name := range []string{
		"ViaVersion-5.12.0.jar",
		"fabric-api-0.161.0+26.2.jar",
		"Plugin Name (1).jar",
		"x.jar",
		strings.Repeat("a", 124) + ".jar",
	} {
		if !validFileName(name) {
			t.Errorf("validFileName(%q) = false", name)
		}
	}
	for _, name := range []string{
		"",
		".jar",
		"../evil.jar",
		"plugins/evil.jar",
		`plugins\evil.jar`,
		".hidden.jar",
		"-rf.jar",
		" lead.jar",
		"plugin.zip",
		"plugin.JAR",
		"plugin.jar.sh",
		"nul\x00.jar",
		"tab\t.jar",
		"rtl\u202egpj.jar",
		strings.Repeat("a", 125) + ".jar",
		"con:.jar",
		"wild*.jar",
		"\xff.jar",
	} {
		if validFileName(name) {
			t.Errorf("validFileName(%q) = true", name)
		}
	}
}

func TestReadJarMeta(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries map[string]string
		raw     string
		want    JarMeta
	}{
		{"plugin.yml with quotes and comments", map[string]string{
			"plugin.yml": "# Chunky\nname: \"Chunky\" # shown in /plugins\nversion: 1.5.3 # build 42\nmain: org.popcraft.chunky.ChunkyBukkit\n",
		}, "", JarMeta{ID: "Chunky", Version: "1.5.3", Kind: "plugin"}},
		{"paper-plugin.yml before plugin.yml", map[string]string{
			"paper-plugin.yml": "name: PaperName\nversion: '2.0'\n",
			"plugin.yml":       "name: BukkitName\nversion: 1.0\n",
		}, "", JarMeta{ID: "PaperName", Version: "2.0", Kind: "plugin"}},
		{"only top-level keys", map[string]string{
			"plugin.yml": "commands:\n  name: nested\nname: Top\nversion: 3\n",
		}, "", JarMeta{ID: "Top", Version: "3", Kind: "plugin"}},
		{"byte order mark", map[string]string{
			"plugin.yml": "\ufeffname: Bom\nversion: 1\n",
		}, "", JarMeta{ID: "Bom", Version: "1", Kind: "plugin"}},
		{"fabric.mod.json", map[string]string{
			"fabric.mod.json": `{"schemaVersion":1,"id":"chunky","name":"Chunky","version":"1.5.3"}`,
		}, "", JarMeta{ID: "chunky", Name: "Chunky", Version: "1.5.3", Kind: "mod"}},
		{"quilt.mod.json", map[string]string{
			"quilt.mod.json": `{"schema_version":1,"quilt_loader":{"id":"zconfig","version":"1.0.0","metadata":{"name":"ZConfig"}}}`,
		}, "", JarMeta{ID: "zconfig", Name: "ZConfig", Version: "1.0.0", Kind: "mod"}},
		{"neoforge.mods.toml with the version left to the build", map[string]string{
			"META-INF/neoforge.mods.toml": "modLoader=\"javafml\"\nloaderVersion=\"[1,)\"\n[[mods]]\nmodId=\"chunky\"\nversion=\"${file.jarVersion}\"\ndisplayName=\"Chunky\"\n",
		}, "", JarMeta{ID: "chunky", Name: "Chunky", Kind: "mod"}},
		{"plugin.yml without a name", map[string]string{"plugin.yml": "version: 1\n"}, "", JarMeta{}},
		{"not a jar", nil, "PK but not really a zip", JarMeta{}},
	} {
		data := []byte(tc.raw)
		if tc.entries != nil {
			data = makeJar(t, tc.entries)
		}
		if got := readJarMeta(bytes.NewReader(data), int64(len(data))); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// The default allowlists must fit what the sources really publish, and
// nothing else.
func TestDefaultHostsFitTheRealFixtures(t *testing.T) {
	l := New(nil)
	var modrinthFiles, hangarFiles, external, icons []string
	for _, path := range globFixtures(t, "modrinth/testdata/versions-*.json") {
		var vs []modrinth.Version
		decodeFixture(t, path, &vs)
		for _, v := range vs {
			for _, f := range v.Files {
				modrinthFiles = append(modrinthFiles, f.URL)
			}
		}
	}
	for _, path := range globFixtures(t, "modrinth/testdata/project-*.json") {
		var p modrinth.Project
		decodeFixture(t, path, &p)
		icons = append(icons, p.IconURL)
	}
	for _, path := range globFixtures(t, "modrinth/testdata/search-*.json") {
		var r modrinth.SearchResult
		decodeFixture(t, path, &r)
		for _, h := range r.Hits {
			icons = append(icons, h.IconURL)
		}
	}
	for _, path := range globFixtures(t, "hangar/testdata/versions-*.json") {
		var vl hangar.VersionList
		decodeFixture(t, path, &vl)
		for _, v := range vl.Result {
			for _, d := range v.Downloads {
				if d.DownloadURL != "" {
					hangarFiles = append(hangarFiles, d.DownloadURL)
				}
				if d.ExternalURL != "" {
					external = append(external, d.ExternalURL)
				}
			}
		}
	}
	for _, path := range globFixtures(t, "hangar/testdata/project-*.json") {
		var p hangar.Project
		decodeFixture(t, path, &p)
		icons = append(icons, p.AvatarURL)
	}
	if len(modrinthFiles) == 0 || len(hangarFiles) == 0 || len(external) == 0 || len(icons) == 0 {
		t.Fatalf("fixtures incomplete: %d Modrinth files, %d Hangar files, %d external links, %d icons", len(modrinthFiles), len(hangarFiles), len(external), len(icons))
	}

	for _, u := range modrinthFiles {
		if _, err := l.fileHosts(Modrinth).Check(u); err != nil {
			t.Errorf("Modrinth file refused: %v", err)
		}
		if _, err := l.fileHosts(Hangar).Check(u); err == nil {
			t.Errorf("Hangar's hosts allow %s", u)
		}
	}
	for _, u := range hangarFiles {
		if _, err := l.fileHosts(Hangar).Check(u); err != nil {
			t.Errorf("Hangar file refused: %v", err)
		}
		if _, err := l.fileHosts(Modrinth).Check(u); err == nil {
			t.Errorf("Modrinth's hosts allow %s", u)
		}
	}
	for _, u := range external {
		if _, err := l.fileHosts(Hangar).Check(u); err == nil {
			t.Errorf("external download allowed: %s", u)
		}
	}
	for _, u := range icons {
		if _, err := l.iconHosts().Check(u); err != nil {
			t.Errorf("icon refused: %v", err)
		}
	}
	if got := l.userAgent(); got != wantUserAgent {
		t.Errorf("User-Agent %q, want %q", got, wantUserAgent)
	}
}

// Installed is a row of the agent's addons table; its JSON names are the
// contract with the agent and the panel.
func TestInstalledJSON(t *testing.T) {
	rec := Installed{
		Source: Modrinth, ProjectID: "P1OZGk5p", Slug: "viaversion", Name: "ViaVersion", IconURL: "https://cdn.modrinth.com/data/P1OZGk5p/icon.webp",
		VersionID: "FaishMnD", VersionNumber: "5.12.0", Channel: "release", Published: testNow, FileName: "ViaVersion-5.12.0.jar",
		HashAlgo: "sha512", Hash: strings.Repeat("ab", 64), Size: 1234, DependencyOf: "TbHIxhx5", Requires: []string{"NpvuJQoq"}, InstalledAt: testNow,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	want := []string{"channel", "dependencyOf", "fileName", "hash", "hashAlgo", "iconUrl", "installedAt", "name", "projectId", "published", "requires", "size", "slug", "source", "versionId", "versionNumber"}
	if got := slices.Sorted(maps.Keys(m)); !slices.Equal(got, want) {
		t.Errorf("JSON keys %v, want %v", got, want)
	}
	var back Installed
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "round trip", back, rec)
	if back.Key() != (Key{Modrinth, "P1OZGk5p"}) {
		t.Errorf("key %+v", back.Key())
	}
}

func globFixtures(t *testing.T, pattern string) []string {
	t.Helper()
	f := &fakes{t: t}
	return f.glob(pattern)
}

func decodeFixture(t *testing.T, path string, v any) {
	t.Helper()
	f := &fakes{t: t}
	f.decode(path, v)
}
