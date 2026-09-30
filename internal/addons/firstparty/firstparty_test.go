package firstparty

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"slices"
	"strings"
	"testing"
)

// Every plugin in the registry ships in the binary: a jar whose plugin.yml
// names it and gives a version the add-on library can take as a version id
// and a file name, hashed and sized from its own bytes.
func TestEveryPluginShipsInTheBinary(t *testing.T) {
	if len(Plugins()) == 0 {
		t.Fatal("the registry is empty")
	}
	seen := map[string]bool{}
	for _, p := range Plugins() {
		switch {
		case seen[p.ID]:
			t.Errorf("%s is in the registry twice", p.ID)
		case !reVersion.MatchString(p.ID) || !reVersion.MatchString(p.Slug):
			t.Errorf("%q (%q) can't be a project id and slug", p.ID, p.Slug)
		case p.Name == "" || p.Summary == "" || p.License == "" || p.Author == "" || len(p.Types) == 0 || p.Published.IsZero():
			t.Errorf("%s is missing some of what a template's add-on shows: %+v", p.ID, p)
		}
		seen[p.ID] = true
		j, err := p.Jar()
		if err != nil {
			t.Errorf("%s: %v", p.ID, err)
			continue
		}
		b, err := jars.ReadFile(p.jar)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if j.SHA256 != hex.EncodeToString(sum[:]) || j.Size != int64(len(b)) || j.FileName != p.Slug+"-"+j.Version+".jar" {
			t.Errorf("%s's jar reads as %s, %d bytes, %s", p.ID, j.SHA256, j.Size, j.FileName)
		}
		if got, _ := io.ReadAll(j.Open()); !bytes.Equal(got, b) {
			t.Errorf("%s's jar opens as other bytes", p.ID)
		}
		if Lookup(p.ID) != p {
			t.Errorf("Lookup(%q) isn't it", p.ID)
		}
		if q, qj := ByHash(strings.ToUpper(j.SHA256)); q != p || qj.Version != j.Version {
			t.Errorf("its hash finds %v", q)
		}
	}
	if Lookup("nope") != nil {
		t.Error("Lookup finds a project the registry doesn't have")
	}
	if p, _ := ByHash(strings.Repeat("0", 64)); p != nil {
		t.Errorf("a hash no jar has finds %s", p.ID)
	}
}

// Each jar beside the package belongs to a plugin in the registry, so the
// binary carries none for nothing.
func TestEveryEmbeddedJarHasAnEntry(t *testing.T) {
	entries, err := jars.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !slices.ContainsFunc(registry, func(p *Plugin) bool { return p.jar == e.Name() }) {
			t.Errorf("%s is embedded, and no plugin in the registry is it", e.Name())
		}
	}
}

// AI Build Battle's entry is what its template and the site show, and runs
// on Paper and Purpur only.
func TestAIBuildBattlesEntry(t *testing.T) {
	p := Lookup("ai-build-battle")
	if p == nil {
		t.Fatal("ai-build-battle isn't in the registry")
	}
	if p.Slug != "ai-build-battle" || p.Name != "AI Build Battle" || p.License != "AGPL-3.0-only" || p.Author != "Playkeeper" ||
		p.Summary != "Type /aibuild and an AI model builds it in front of you, block by block." {
		t.Errorf("got %+v", p)
	}
	for typ, runs := range map[string]bool{"paper": true, "purpur": true, "fabric": false, "quilt": false, "neoforge": false, "forge": false, "vanilla": false} {
		if p.RunsOn(typ) != runs {
			t.Errorf("RunsOn(%q) = %v", typ, !runs)
		}
	}
}

func jarOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// A jar's version is its plugin.yml's, or its paper-plugin.yml's; a jar
// without one the library can use as a version id and in a file name isn't
// a build Playkeeper can install.
func TestAJarsVersionComesFromItsPluginYML(t *testing.T) {
	for name, tc := range map[string]struct {
		files   map[string]string
		version string
	}{
		"plugin.yml":                   {map[string]string{"plugin.yml": "name: Demo\nversion: 1.2.3\nmain: a.B\n"}, "1.2.3"},
		"a quoted version":             {map[string]string{"plugin.yml": "name: Demo\nversion: '2.0.1-beta' # the next\n"}, "2.0.1-beta"},
		"paper-plugin.yml":             {map[string]string{"paper-plugin.yml": "name: Demo\nversion: \"0.4\"\n", "plugin.yml": "name: Demo\nversion: 9\n"}, "0.4"},
		"a byte order mark":            {map[string]string{"plugin.yml": "\ufeffname: Demo\nversion: 3\n"}, "3"},
		"no plugin.yml":                {map[string]string{"fabric.mod.json": `{"id": "demo", "version": "1.0"}`}, ""},
		"no name":                      {map[string]string{"plugin.yml": "version: 1.0\n"}, ""},
		"no version":                   {map[string]string{"plugin.yml": "name: Demo\nmain: a.B\n"}, ""},
		"a version Gradle didn't fill": {map[string]string{"plugin.yml": "name: Demo\nversion: ${version}\n"}, ""},
		"a version with a slash":       {map[string]string{"plugin.yml": "name: Demo\nversion: 1/2\n"}, ""},
		"a version with a space":       {map[string]string{"plugin.yml": "name: Demo\nversion: '1 2'\n"}, ""},
	} {
		b := jarOf(t, tc.files)
		j, err := parseJar("demo", b)
		switch {
		case tc.version == "" && err == nil:
			t.Errorf("%s: read as version %q", name, j.Version)
		case tc.version != "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.version != "" && (j.Version != tc.version || j.FileName != "demo-"+tc.version+".jar" || j.Size != int64(len(b))):
			t.Errorf("%s: read as %+v", name, j)
		}
	}
	if _, err := parseJar("demo", []byte("not a zip")); err == nil {
		t.Error("a file that isn't a jar reads")
	}
}

func TestNewerComparesVersionsPartByPart(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.0", true},
		{"0.10.0", "0.9.2", true},
		{"1.0.0", "0.99", true},
		{"0.1.0", "0.1.0", false},
		{"0.1", "0.1.0", false},
		{"0.1.0", "0.1", false},
		{"0.1.0", "0.2.0", false},
		{"0.1.1", "0.1", true},
		{"1.0.0", "1.0.0-beta", true},
		{"1.0.0-beta", "1.0.0", false},
		{"1.0.0-rc1", "1.0.0-beta2", true},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
