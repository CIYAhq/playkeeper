package pregen

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/zipdir/zipdirtest"
)

// makeJar writes a jar holding files (name to content) and a class file.
func makeJar(t *testing.T, path string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	files["org/popcraft/chunky/Chunky.class"] = "\xca\xfe\xba\xbe"
	for name, body := range files {
		w, err := zw.Create(name)
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
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	return readFile(t, filepath.Join("testdata", name))
}

func TestDetect(t *testing.T) {
	dir := t.TempDir()
	if _, err := Detect(dir, Bukkit); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Detect without plugins = %v", err)
	}
	plugins := filepath.Join(dir, "plugins")
	makeJar(t, filepath.Join(plugins, "LuckPerms-Bukkit-5.5.jar"), map[string]string{"plugin.yml": "name: LuckPerms\nversion: 5.5.17\n"})
	makeJar(t, filepath.Join(plugins, "Other.jar"), map[string]string{"paper-plugin.yml": "name: Other\nversion: 1\n", "plugin.yml": fixture(t, "plugin.yml")})
	makeJar(t, filepath.Join(plugins, "a-fabric-build.jar"), map[string]string{"fabric.mod.json": fixture(t, "fabric.mod.json")})
	if err := os.WriteFile(filepath.Join(plugins, "broken.jar"), []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(plugins, "Chunky"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Detect(dir, Bukkit); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Detect without Chunky = %v", err)
	}

	makeJar(t, filepath.Join(plugins, "pregen.JAR"), map[string]string{"plugin.yml": fixture(t, "plugin.yml")})
	got, err := Detect(dir, Bukkit)
	if err != nil || got != (Installed{File: "plugins/pregen.JAR", Version: "1.5.3"}) {
		t.Errorf("Detect(Bukkit) = %+v, %v", got, err)
	}
	if _, err := Detect(dir, Fabric); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Fabric found Chunky among plugins: %v", err)
	}

	mods := filepath.Join(dir, "mods")
	makeJar(t, filepath.Join(mods, "Chunky-Bukkit-1.5.3.jar"), map[string]string{"plugin.yml": fixture(t, "plugin.yml")})
	makeJar(t, filepath.Join(mods, "fabric-api-0.140.0.jar"), map[string]string{"fabric.mod.json": `{"schemaVersion": 1, "id": "fabric-api", "version": "0.140.0"}`})
	makeJar(t, filepath.Join(mods, "Chunky-Fabric-1.5.3.jar"), map[string]string{"fabric.mod.json": fixture(t, "fabric.mod.json")})
	got, err = Detect(dir, Fabric)
	if err != nil || got != (Installed{File: "mods/Chunky-Fabric-1.5.3.jar", Version: "1.5.3"}) {
		t.Errorf("Detect(Fabric) = %+v, %v", got, err)
	}

	if _, err := Detect(dir, NeoForge); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("NeoForge found Chunky without neoforge.mods.toml: %v", err)
	}
	makeJar(t, filepath.Join(mods, "Chunky-NeoForge-1.5.4.jar"), map[string]string{"META-INF/neoforge.mods.toml": fixture(t, "neoforge.mods.toml")})
	got, err = Detect(dir, NeoForge)
	if err != nil || got != (Installed{File: "mods/Chunky-NeoForge-1.5.4.jar", Version: "1.5.4"}) {
		t.Errorf("Detect(NeoForge) = %+v, %v", got, err)
	}
	if _, err := Detect(dir, Forge); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Forge found Chunky in a NeoForge jar: %v", err)
	}
	makeJar(t, filepath.Join(mods, "Chunky-Forge-1.5.4.jar"), map[string]string{"META-INF/mods.toml": fixture(t, "forge.mods.toml")})
	got, err = Detect(dir, Forge)
	if err != nil || got != (Installed{File: "mods/Chunky-Forge-1.5.4.jar", Version: "1.5.4"}) {
		t.Errorf("Detect(Forge) = %+v, %v", got, err)
	}

	if _, err := Detect(dir, "folia"); err == nil || errors.Is(err, ErrNotInstalled) {
		t.Errorf("Detect(unknown platform) = %v", err)
	}
}

// The game can replace the plugins folder with a named pipe, which would
// hold Detect in open(2) until something writes to it.
func TestDetectDoesNotWaitOnAPipe(t *testing.T) {
	dir := t.TempDir()
	pipe := filepath.Join(dir, "plugins")
	if err := syscall.Mkfifo(pipe, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Detect(dir, Bukkit)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotInstalled) {
			t.Errorf("Detect with a pipe for plugins = %v", err)
		}
	case <-time.After(5 * time.Second):
		if fd, err := syscall.Open(pipe, syscall.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			syscall.Close(fd)
		}
		<-done
		t.Fatal("Detect waited on the pipe")
	}
}

// The game can put jars in the plugins folder whose table of contents
// archive/zip would hold in memory by the hundred megabytes. Detect reads
// the jars in name order, so these come before Chunky's.
func TestDetectDoesNotReadHugeTablesOfContents(t *testing.T) {
	dir := t.TempDir()
	plugins := filepath.Join(dir, "plugins")
	makeJar(t, filepath.Join(plugins, "Chunky.jar"), map[string]string{"plugin.yml": fixture(t, "plugin.yml")})
	for name, b := range map[string][]byte{"Bomb-count.jar": zipdirtest.Bomb(300_000, false), "Bomb-zip64.jar": zipdirtest.Bomb(200_000, true)} {
		if err := os.WriteFile(filepath.Join(plugins, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := Detect(dir, Bukkit)
	runtime.ReadMemStats(&after)
	if n := after.TotalAlloc - before.TotalAlloc; n > 8<<20 {
		t.Errorf("Detect allocated %d MiB", n>>20)
	}
	if err != nil || got != (Installed{File: "plugins/Chunky.jar", Version: "1.5.3"}) {
		t.Errorf("Detect = %+v, %v", got, err)
	}
}

func TestTOMLMod(t *testing.T) {
	multi := `modLoader="javafml"
loaderVersion="[3,)"
[[mods]]
modId="bundle_core"
version="2.0.0"
[[mods]]
  modId = 'chunky' # the pre-generator
  version = "1.5.4"
[[dependencies.chunky]]
   modId="neoforge"
   version="99"
`
	if v, ok := tomlMod(multi, "chunky"); !ok || v != "1.5.4" {
		t.Errorf("tomlMod(second mod) = %q, %v", v, ok)
	}
	if v, ok := tomlMod(multi, "bundle_core"); !ok || v != "2.0.0" {
		t.Errorf("tomlMod(first mod) = %q, %v", v, ok)
	}
	if _, ok := tomlMod(multi, "neoforge"); ok {
		t.Error("a dependency counted as a mod")
	}
	if v, ok := tomlMod(fixture(t, "neoforge.mods.toml"), "chunky"); !ok || v != "1.5.4" {
		t.Errorf("tomlMod(Chunky's own file) = %q, %v", v, ok)
	}
	m := yamlTopLevel(fixture(t, "plugin.yml"))
	if m["name"] != "Chunky" || m["version"] != "1.5.3" || m["description"] != "Pre-generates chunks, quickly, efficiently, and safely" {
		t.Errorf("plugin.yml top level = %v", m)
	}
	if m := yamlTopLevel("name: 'Chunky' # quoted\r\nversion: \"1.5.3\"\n"); m["name"] != "Chunky" || m["version"] != "1.5.3" {
		t.Errorf("quoted plugin.yml = %v", m)
	}
}
