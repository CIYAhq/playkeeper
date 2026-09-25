package worldimport

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// dataDir builds a server folder: names ending in "/" are folders, the
// others files.
func dataDir(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, n := range names {
		p := filepath.Join(root, filepath.FromSlash(n))
		if n[len(n)-1] == '/' {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestWorldFolders(t *testing.T) {
	dir := dataDir(t,
		"world/region/r.0.0.mca", "world/level.dat",
		"world_nether/DIM-1/region/",
		"world_the_end/DIM1/region/",
		"world_mymod_mining/dimensions/mymod/mining/region/",
		"world_my_mod_deep_dark/dimensions/my_mod/deep_dark/",
		"world_fake_dim/dimensions/fake/dim",
		"world_backup/region/",
		"world_old_nether/region/",
		"world2/region/",
		"survival/level.dat",
		"plugins/Essentials.jar",
		"server.properties", "world.zip",
	)
	want := []string{"world", "world_my_mod_deep_dark", "world_mymod_mining", "world_nether", "world_the_end"}
	for _, name := range []string{"world", ""} {
		got, err := WorldFolders(dir, name)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("level-name %q: got %q, want %q", name, got, want)
		}
	}
	got, err := WorldFolders(dir, "survival")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"survival"}) {
		t.Errorf("level-name survival: got %q", got)
	}
}

func TestWorldFoldersWithoutWorld(t *testing.T) {
	got, err := WorldFolders(filepath.Join(t.TempDir(), "missing"), "world")
	if got != nil || err != nil {
		t.Errorf("missing data directory: got %q, %v", got, err)
	}
	got, err = WorldFolders(dataDir(t, "server.properties", "plugins/"), "world")
	if got != nil || err != nil {
		t.Errorf("server without a world: got %q, %v", got, err)
	}
	file := filepath.Join(dataDir(t, "data"), "data")
	var e *Error
	if _, err := WorldFolders(file, "world"); err == nil || errors.As(err, &e) {
		t.Errorf("data directory that is a file: got %v, want a plain error", err)
	}
}

func TestWorldFoldersRefusesLinks(t *testing.T) {
	for _, name := range []string{"world", "world_nether", "world_the_end"} {
		t.Run(name, func(t *testing.T) {
			names := []string{"world_backup/level.dat"}
			if name != "world" {
				names = append(names, "world/")
			}
			dir := dataDir(t, names...)
			if err := os.Symlink(filepath.Join(dir, "world_backup"), filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			_, err := WorldFolders(dir, "world")
			if got := refusalKind(t, err); got != KindFolderLink {
				t.Fatalf("got %s (%v)", got, err)
			}
			var e *Error
			if errors.As(err, &e); e.Params["folder"] != name || e.Hint == "" {
				t.Errorf("params %v, hint %q", e.Params, e.Hint)
			}
		})
	}
}

func TestWorldFoldersRefusesLevelNames(t *testing.T) {
	dir := dataDir(t, "world/")
	for _, name := range []string{"../world", "worlds/world", `world\nether`, ".", "..", " world", "plugins", "Server.properties", "wor\x00ld"} {
		_, err := WorldFolders(dir, name)
		if got := refusalKind(t, err); got != KindTargetLevelName {
			t.Errorf("%q: got %s (%v)", name, got, err)
		}
	}
}
