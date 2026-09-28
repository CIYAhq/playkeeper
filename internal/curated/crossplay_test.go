package curated

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/gamefiles"
)

// changedLines lists the lines of b that differ from a's, by line number,
// for two documents with the same number of lines.
func changedLines(t *testing.T, a, b []byte) []string {
	t.Helper()
	al, bl := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
	if len(al) != len(bl) {
		t.Fatalf("%d lines became %d", len(al), len(bl))
	}
	var out []string
	for i := range al {
		if al[i] != bl[i] {
			out = append(out, strings.TrimSpace(bl[i]))
		}
	}
	return out
}

func TestGeyserConfigChangesOnlyPlaykeepersLines(t *testing.T) {
	// Geyser 2.11.3's own config.yml, as it wrote it on first start.
	old, err := os.ReadFile("testdata/geyser-config.yml")
	if err != nil {
		t.Fatal(err)
	}
	b, err := GeyserConfig(old, 19133)
	if err != nil {
		t.Fatal(err)
	}
	if got := changedLines(t, old, b); !slices.Equal(got, []string{"port: 19133", "auth-type: floodgate", "log-player-ip-addresses: false"}) {
		t.Errorf("changed %q", got)
	}
	again, err := GeyserConfig(b, 19133)
	if err != nil || string(again) != string(b) {
		t.Errorf("a second pass changed the file (%v)", err)
	}
	// The other "port"s, "address"es and "java" blocks are left alone.
	for _, keep := range []string{"    broadcast-port: 0", "  java:\n    # Whether to enable HAPROXY", "      advertise-addresses: []"} {
		if !strings.Contains(string(b), keep) {
			t.Errorf("lost %q", keep)
		}
	}

	fresh, err := GeyserConfig(nil, 19132)
	want := "bedrock:\n  address: 0.0.0.0\n  port: 19132\n  clone-remote-port: false\njava:\n  auth-type: floodgate\nlog-player-ip-addresses: false\n"
	if err != nil || string(fresh) != want {
		t.Errorf("a new config is %q, %v", fresh, err)
	}
	if _, err := GeyserConfig(nil, 80); err == nil {
		t.Error("port 80 was accepted")
	}
}

func TestFloodgateConfigChangesOnlyPlaykeepersLines(t *testing.T) {
	old, err := os.ReadFile("testdata/floodgate-config.yml")
	if err != nil {
		t.Fatal(err)
	}
	b, err := FloodgateConfig(old)
	if err != nil {
		t.Fatal(err)
	}
	// The prefix and spaces are Floodgate's defaults; statistics go off.
	if got := changedLines(t, old, b); !slices.Equal(got, []string{"enabled: false"}) {
		t.Errorf("changed %q", got)
	}
	if !strings.Contains(string(b), "player-link:\n  # Whether to enable the linking system.") || !strings.Contains(string(b), "  enabled: true\n\n  # Whether to require a linked account") {
		t.Error("player-link's own enabled changed")
	}
	fresh, err := FloodgateConfig(nil)
	if want := "username-prefix: \".\"\nreplace-spaces: true\nmetrics:\n  enabled: false\n"; err != nil || string(fresh) != want {
		t.Errorf("a new config is %q, %v", fresh, err)
	}
}

func TestEditYAML(t *testing.T) {
	sets := []yamlSet{{"bedrock", "port", "19140"}, {"", "log-player-ip-addresses", "false"}}
	for _, c := range []struct{ name, in, want, err string }{
		{name: "a missing key goes into its block, at the block's indent",
			in:   "bedrock:\n    address: 0.0.0.0\n\n# a note\nlog-player-ip-addresses: true\n",
			want: "bedrock:\n    address: 0.0.0.0\n    port: 19140\n\n# a note\nlog-player-ip-addresses: false\n"},
		{name: "a value on the lines below is replaced, comments stay",
			in:   "bedrock:\n  port:\n    19132\n  # the transport\n  transport: raknet\nlog-player-ip-addresses: true\n",
			want: "bedrock:\n  port: 19140\n  # the transport\n  transport: raknet\nlog-player-ip-addresses: false\n"},
		{name: "a comment after the value goes with it",
			in:   "bedrock: # Bedrock\n  port: 19132 # the default\nlog-player-ip-addresses: true\n",
			want: "bedrock: # Bedrock\n  port: 19140\nlog-player-ip-addresses: false\n"},
		{name: "Windows line endings stay",
			in:   "bedrock:\r\n  port: 19132\r\nlog-player-ip-addresses: true\r\n",
			want: "bedrock:\r\n  port: 19140\r\nlog-player-ip-addresses: false\r\n"},
		{name: "a block that ends the file",
			in:   "log-player-ip-addresses: true\nbedrock:\n",
			want: "log-player-ip-addresses: false\nbedrock:\n  port: 19140\n"},
		{name: "keys deeper down are other keys",
			in:   "advanced:\n  bedrock:\n    port: 1\nbedrock:\n  signaling:\n    port: 2\n",
			want: "advanced:\n  bedrock:\n    port: 1\nbedrock:\n  signaling:\n    port: 2\n  port: 19140\nlog-player-ip-addresses: false\n"},
		{name: "a block written on one line", in: "bedrock: {port: 19132}\n", err: "bedrock is not a block of settings Playkeeper can change"},
		{name: "tabs", in: "bedrock:\n\tport: 19132\n", err: "it is indented with tabs, which YAML doesn't allow"},
		{name: "not text", in: "bedrock:\n  port: \xff\n", err: "it is not UTF-8 text"},
	} {
		got, err := editYAML([]byte(c.in), sets)
		switch {
		case c.err != "" && (err == nil || err.Error() != c.err):
			t.Errorf("%s: error %v, want %q", c.name, err, c.err)
		case c.err == "" && (err != nil || string(got) != c.want):
			t.Errorf("%s: got %q, %v\nwant %q", c.name, got, err, c.want)
		}
	}
}

func TestWriteCrossplayConfig(t *testing.T) {
	dir := t.TempDir()
	if err := WriteCrossplayConfig(dir, 19134, nil); err != nil {
		t.Fatal(err)
	}
	g, _ := os.ReadFile(filepath.Join(dir, GeyserConfigPath))
	f, _ := os.ReadFile(filepath.Join(dir, FloodgateConfigPath))
	if !strings.Contains(string(g), "\n  port: 19134\n") || !strings.Contains(string(g), "auth-type: floodgate") || !strings.Contains(string(f), "username-prefix: \".\"") {
		t.Errorf("Geyser's config %q, Floodgate's %q", g, f)
	}
	// A restore brought a config with another port.
	if err := os.WriteFile(filepath.Join(dir, GeyserConfigPath), []byte("bedrock:\n  port: 19132\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := WriteCrossplayConfig(dir, 19134, nil); err != nil {
		t.Fatal(err)
	}
	if g, _ := os.ReadFile(filepath.Join(dir, GeyserConfigPath)); !strings.Contains(string(g), "port: 19134") || strings.Contains(string(g), "19132") {
		t.Errorf("Geyser's config is %q", g)
	}

	linked := t.TempDir()
	if err := os.MkdirAll(filepath.Join(linked, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(linked, "plugins", "floodgate")); err != nil {
		t.Fatal(err)
	}
	err := WriteCrossplayConfig(linked, 19134, nil)
	e := wantKind(t, err, KindConfigUnusable)
	if e.Params["file"] != FloodgateConfigPath || !strings.HasPrefix(e.Msg, "Playkeeper could not write crossplay's settings to plugins/floodgate/config.yml: ") || gamefiles.KindOf(e.Err) != gamefiles.KindLink {
		t.Errorf("notice %+v (%v)", e.Notice, e.Err)
	}
	wantKind(t, WriteCrossplayConfig(dir, 1023, nil), KindPortInvalid)
}

func TestCrossplayProjects(t *testing.T) {
	var m struct {
		ID, Slug, Title string
		ServerSide      string `json:"server_side"`
		License         struct{ ID string }
		Loaders         []string
	}
	readJSON(t, "testdata/geyser-project.json", &m)
	g := Geyser()
	if m.ID != g.ID || m.Slug != g.Slug || m.Title != g.Title || m.License.ID != g.License || m.ServerSide == "unsupported" || !slices.Contains(m.Loaders, "paper") {
		t.Errorf("Geyser is listed as %+v; Modrinth has %+v", g, m)
	}
	var h struct {
		ID        int
		Name      string
		Namespace struct{ Owner, Slug string }
		Settings  struct{ License struct{ Type string } }
	}
	readJSON(t, "testdata/floodgate-project.json", &h)
	fg := Floodgate()
	if fg.ID != "17" || h.ID != 17 || h.Name != fg.Title || h.Namespace.Owner != fg.Author || h.Namespace.Slug != fg.Slug || h.Settings.License.Type != fg.License ||
		fg.PageURL != "https://hangar.papermc.io/"+h.Namespace.Owner+"/"+h.Namespace.Slug {
		t.Errorf("Floodgate is listed as %+v; Hangar has %+v", fg, h)
	}
	for _, k := range []addons.Key{{Source: addons.Modrinth, ProjectID: "wKkoqHrH"}, {Source: addons.Hangar, ProjectID: "17"}, {Source: addons.Hangar, ProjectID: "14"}} {
		if !IsCrossplayProject(k) {
			t.Errorf("%v is not crossplay's", k)
		}
	}
	for _, k := range []addons.Key{{Source: addons.Hangar, ProjectID: "wKkoqHrH"}, {Source: addons.Modrinth, ProjectID: "17"}, {Source: addons.Modrinth, ProjectID: "9eGKb6K1"}} {
		if IsCrossplayProject(k) {
			t.Errorf("%v is crossplay's", k)
		}
	}
	for _, typ := range []string{"paper", "purpur"} {
		if err := CrossplayFor(typ); err != nil {
			t.Errorf("%s: %v", typ, err)
		}
	}
	e := wantKind(t, CrossplayFor("fabric"), KindNotForType)
	if e.Msg != "Crossplay is only offered for Paper and Purpur servers; this server runs Fabric." || e.Hint != "Bedrock players can join a Paper or Purpur server." {
		t.Errorf("notice %+v", e.Notice)
	}
	if e := wantKind(t, CrossplayFor("vanilla"), KindNotForType); e.Msg != "Crossplay is only offered for Paper and Purpur servers; this server runs Vanilla." {
		t.Errorf("notice %+v", e.Notice)
	}
	wantKind(t, CrossplayFor("folia"), addons.KindUnknownServerType)
	steps := CrossplaySteps(19133)
	if len(steps) != 2 || steps[0].Kind != KindOpenPort || steps[0].Params["port"] != "19133" || steps[0].Params["protocol"] != "udp" || steps[1].Kind != KindConsoles {
		t.Errorf("steps %+v", steps)
	}
}
