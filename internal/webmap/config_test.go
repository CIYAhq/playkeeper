package webmap

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// squaremapKeys are the keys squaremap's Config reads, and
// squaremapWorldKeys the ones its WorldConfig reads under
// world-settings.<world> (commit ac71dd2f).
var (
	squaremapKeys = []string{
		"settings.commands.main-command-label", "settings.debug-mode",
		"settings.image-quality.compress-images.enabled", "settings.image-quality.compress-images.value",
		"settings.internal-webserver.bind", "settings.internal-webserver.enabled",
		"settings.internal-webserver.flush-json-immediately", "settings.internal-webserver.port",
		"settings.language-file", "settings.render-progress-logging.enabled",
		"settings.render-progress-logging.interval-seconds", "settings.ui.coordinates.enabled",
		"settings.ui.link.enabled", "settings.ui.sidebar.pinned", "settings.update-checker",
		"settings.web-address", "settings.web-directory.auto-update", "settings.web-directory.path",
	}
	squaremapWorldKeys = []string{
		"map.background-render.enabled", "map.background-render.interval-seconds",
		"map.background-render.max-chunks-per-interval", "map.background-render.max-render-threads",
		"map.biomes.blend-biomes", "map.biomes.enabled", "map.display-name", "map.enabled", "map.glass.clear",
		"map.icon", "map.iterate-up", "map.lava.checkerboard", "map.max-height", "map.max-render-threads",
		"map.order", "map.water.checkerboard", "map.water.clear-depth", "map.zoom.default", "map.zoom.extra",
		"map.zoom.maximum", "player-tracker.default-hidden", "player-tracker.enabled",
		"player-tracker.hide.invisible", "player-tracker.hide.map-invisibility-equipment",
		"player-tracker.hide.spectators", "player-tracker.layer-priority", "player-tracker.nameplate.enabled",
		"player-tracker.nameplate.heads-url", "player-tracker.nameplate.show-armor",
		"player-tracker.nameplate.show-head", "player-tracker.nameplate.show-health",
		"player-tracker.show-controls", "player-tracker.update-interval-seconds",
		"player-tracker.use-display-names", "player-tracker.z-index",
	}
)

func TestConfigGolden(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    Settings
	}{
		{"default", Settings{}},
		{"shared", Settings{Link: "https://play.example.com/map/survival", RenderThreads: 2}},
	} {
		got, err := Config(tc.s)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		golden := filepath.Join("testdata", "config-"+tc.name+".yml")
		if *update {
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: config differs from %s (run with -update after a deliberate change):\n%s", tc.name, golden, got)
		}
	}
}

// flattenYAML reads the block-style YAML Config writes as key paths and raw
// values: settings.internal-webserver.port → 25580.
func flattenYAML(t *testing.T, b []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	var stack []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(trimmed)
		if indent%2 != 0 || indent/2 > len(stack) {
			t.Fatalf("unexpected indentation: %q", line)
		}
		stack = stack[:indent/2]
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			t.Fatalf("not a key: %q", line)
		}
		if value = strings.TrimSpace(value); value == "" {
			stack = append(stack, key)
			continue
		}
		p := strings.Join(append(slices.Clone(stack), key), ".")
		if _, dup := out[p]; dup {
			t.Fatalf("%s appears twice", p)
		}
		out[p] = value
	}
	return out
}

func TestConfigKeepsSquaremapPrivateAndQuiet(t *testing.T) {
	b, err := Config(Settings{})
	if err != nil {
		t.Fatal(err)
	}
	got := flattenYAML(t, b)
	const world = "world-settings.default."
	for key, want := range map[string]string{
		"config-version":                                         "2",
		"settings.update-checker":                                "false",
		"settings.web-address":                                   "''",
		"settings.web-directory.path":                            "web",
		"settings.internal-webserver.enabled":                    "true",
		"settings.internal-webserver.bind":                       "0.0.0.0",
		"settings.internal-webserver.port":                       strconv.Itoa(Port),
		"settings.commands.main-command-label":                   "squaremap",
		world + "map.enabled":                                    "true",
		world + "map.max-render-threads":                         "1",
		world + "map.zoom.maximum":                               strconv.Itoa(ZoomMax),
		world + "map.background-render.max-render-threads":       "1",
		world + "player-tracker.nameplate.show-head":             "false",
		world + "player-tracker.nameplate.heads-url":             "''",
		world + "player-tracker.nameplate.show-armor":            "false",
		world + "player-tracker.nameplate.show-health":           "false",
		world + "player-tracker.hide.invisible":                  "true",
		world + "player-tracker.hide.spectators":                 "true",
		world + "player-tracker.hide.map-invisibility-equipment": "true",
	} {
		if got[key] != want {
			t.Errorf("%s is %q, want %q", key, got[key], want)
		}
	}
}

func TestConfigUsesOnlyKeysSquaremapReads(t *testing.T) {
	b, err := Config(Settings{Link: "https://play.example.com/map/survival", RenderThreads: MaxRenderThreads})
	if err != nil {
		t.Fatal(err)
	}
	for key := range flattenYAML(t, b) {
		k, perWorld := strings.CutPrefix(key, "world-settings.default.")
		if key == "config-version" || perWorld && slices.Contains(squaremapWorldKeys, k) || !perWorld && slices.Contains(squaremapKeys, key) {
			continue
		}
		t.Errorf("squaremap does not read %s", key)
	}
}

func TestConfigRefusesUnsafeSettings(t *testing.T) {
	for _, s := range []Settings{
		{Link: "http://play.example.com/map/survival"},
		{Link: "https://play.example.com/map/survival?x=1"},
		{Link: "https://play.example.com/map/survival?"},
		{Link: "https://play.example.com/map/survival#top"},
		{Link: "https://owner:secret@play.example.com/map/survival"},
		{Link: "https://play.example.com/map/it's"},
		{Link: "https://play.example.com/map/survival'\nsettings:\n  update-checker: true"},
		{Link: "https://play.example.com/map/sur vival"},
		{Link: `https://play.example.com/map/"survival"`},
		{Link: `https://play.example.com/map\survival`},
		{Link: "https://play.example.com/map/überleben"},
		{Link: "https:play.example.com"},
		{Link: "https://"},
		{Link: "javascript:alert(1)"},
		{Link: "https://play.example.com/" + strings.Repeat("a", 200)},
		{RenderThreads: -1},
		{RenderThreads: MaxRenderThreads + 1},
	} {
		b, err := Config(s)
		var e *Error
		if !errors.As(err, &e) || e.Kind != KindInvalid || e.Params["field"] == "" || b != nil {
			t.Errorf("%+v: got %#v", s, err)
		}
	}
}

func TestWriteConfigPutsItInSquaremapsFolder(t *testing.T) {
	for _, tc := range []struct{ typ, file string }{
		{"paper", "plugins/squaremap/config.yml"},
		{"purpur", "plugins/squaremap/config.yml"},
		{"fabric", "squaremap/config.yml"},
		{"quilt", "squaremap/config.yml"},
		{"neoforge", "squaremap/config.yml"},
	} {
		dir := t.TempDir()
		file := filepath.Join(dir, filepath.FromSlash(tc.file))
		folder := filepath.Dir(file)
		mustWrite(t, filepath.Join(folder, "config.yml"), "settings:\n  update-checker: true\n")
		mustWrite(t, filepath.Join(folder, "web", "index.html"), "<!doctype html>")
		m := Map{Dir: dir, Type: tc.typ, Owner: &Owner{UID: os.Getuid(), GID: os.Getgid()}}
		s := Settings{Link: "https://play.example.com/map/survival"}
		for range 2 {
			if err := m.WriteConfig(s); err != nil {
				t.Fatalf("%s: %v", tc.typ, err)
			}
		}
		want, _ := Config(s)
		if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: config.yml is %q, %v", tc.typ, got, err)
		}
		if fi, err := os.Lstat(file); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o027 != 0 {
			t.Errorf("%s: config.yml is %v, %v", tc.typ, fi.Mode(), err)
		}
		entries, _ := os.ReadDir(folder)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		if !slices.Equal(names, []string{"config.yml", "web"}) {
			t.Errorf("%s: squaremap's folder holds %v", tc.typ, names)
		}
	}
}

func TestWriteConfigCreatesSquaremapsFolderForTheGame(t *testing.T) {
	dir := t.TempDir()
	if err := (Map{Dir: dir, Type: "paper", Owner: &Owner{UID: os.Getuid(), GID: os.Getgid()}}).WriteConfig(Settings{}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"plugins", "plugins/squaremap"} {
		if fi, err := os.Lstat(filepath.Join(dir, p)); err != nil || !fi.IsDir() || fi.Mode().Perm()&0o027 != 0 {
			t.Errorf("%s is %v, %v", p, fi.Mode(), err)
		}
	}
	if os.Geteuid() == 0 {
		return
	}
	err := Map{Dir: t.TempDir(), Type: "paper", Owner: &Owner{UID: 0, GID: 0}}.WriteConfig(Settings{})
	if KindOf(err) != KindFolderUnusable || !errors.Is(err, fs.ErrPermission) {
		t.Errorf("handing the folder to another user without the right to: %v", err)
	}
}

func TestWriteConfigRefusesLinksAndFilesInItsWay(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		setup        func(t *testing.T, dir, outside string)
	}{
		{"plugins is a link", "plugins is not a folder", func(t *testing.T, dir, outside string) {
			mustSymlink(t, outside, filepath.Join(dir, "plugins"))
		}},
		{"squaremap's folder is a link", "plugins/squaremap is not a folder", func(t *testing.T, dir, outside string) {
			mustSymlink(t, outside, filepath.Join(dir, "plugins", "squaremap"))
		}},
		{"squaremap's folder is a file", "plugins/squaremap is not a folder", func(t *testing.T, dir, outside string) {
			mustWrite(t, filepath.Join(dir, "plugins", "squaremap"), "squaremap")
		}},
	} {
		dir, outside := t.TempDir(), t.TempDir()
		tc.setup(t, dir, outside)
		err := Map{Dir: dir, Type: "paper"}.WriteConfig(Settings{})
		var e *Error
		if !errors.As(err, &e) || e.Kind != KindFolderUnusable || e.Params["folder"] != "plugins/squaremap" || e.Params["reason"] != tc.reason {
			t.Errorf("%s: got %#v", tc.name, err)
			continue
		}
		if !strings.Contains(e.Msg, "("+tc.reason+")") || e.Hint == "" {
			t.Errorf("%s: message %q, hint %q", tc.name, e.Msg, e.Hint)
		}
		if entries, _ := os.ReadDir(outside); len(entries) != 0 {
			t.Errorf("%s: wrote outside the server's files: %v", tc.name, entries)
		}
	}
}

func TestWriteConfigReplacesALinkedConfigInsteadOfWritingThroughIt(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	victim := filepath.Join(outside, "victim.yml")
	mustWrite(t, victim, "keep me\n")
	config := filepath.Join(dir, "plugins", "squaremap", "config.yml")
	mustSymlink(t, victim, config)
	if err := (Map{Dir: dir, Type: "paper"}).WriteConfig(Settings{}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(config); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("config.yml is %v, %v", fi.Mode(), err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep me\n" {
		t.Errorf("the linked file became %q", b)
	}
}

func TestWriteConfigLeavesNoTemporaryFileWhenItFails(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "squaremap")
	if err := os.MkdirAll(filepath.Join(folder, "config.yml"), 0o750); err != nil {
		t.Fatal(err)
	}
	err := Map{Dir: dir, Type: "fabric"}.WriteConfig(Settings{})
	if KindOf(err) != KindFolderUnusable || strings.Contains(err.Error(), "playkeeper-") {
		t.Errorf("got %v", err)
	}
	if entries, _ := os.ReadDir(folder); len(entries) != 1 {
		t.Errorf("squaremap's folder holds %v", entries)
	}
}

func TestWriteConfigExplainsAFolderItCannotWriteIn(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write in any folder")
	}
	dir := t.TempDir()
	plugins := filepath.Join(dir, "plugins")
	if err := os.Mkdir(plugins, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(plugins, 0o700) })
	err := Map{Dir: dir, Type: "paper"}.WriteConfig(Settings{})
	var e *Error
	if !errors.As(err, &e) || e.Params["reason"] != "permission denied" || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("got %#v", err)
	}
	if want := "Playkeeper cannot write squaremap's settings in plugins/squaremap in the server's files (permission denied)."; e.Msg != want {
		t.Errorf("message %q, want %q", e.Msg, want)
	}
}

func TestWriteConfigRefusesWhatConfigRefuses(t *testing.T) {
	for _, tc := range []struct {
		m    Map
		s    Settings
		kind Kind
	}{
		{Map{Type: "vanilla"}, Settings{}, KindUnsupported},
		{Map{Type: "paper"}, Settings{Link: "http://play.example.com/map/survival"}, KindInvalid},
	} {
		dir := t.TempDir()
		tc.m.Dir = dir
		if err := tc.m.WriteConfig(tc.s); KindOf(err) != tc.kind {
			t.Errorf("%s: got %v", tc.m.Type, err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s: wrote %v", tc.m.Type, entries)
		}
	}
}

func mustWrite(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}
