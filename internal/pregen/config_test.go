package pregen

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func onlyFile(t *testing.T, dir, name string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != name {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("%s holds %v, want only %s", dir, names, name)
	}
}

func TestWriteConfigYAML(t *testing.T) {
	dir := t.TempDir()
	if err := WriteConfig(dir, Bukkit, Config{ContinueOnRestart: true}, nil); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "plugins", "Chunky", "config.yml")
	// Chunky 1.5.3's default config.yml with the setting changed.
	want := "version: 2\nlanguage: en\ncontinue-on-restart: true\nforce-load-existing-chunks: false\nsilent: false\nupdate-interval: 1\n"
	if got := readFile(t, p); got != want {
		t.Errorf("new config.yml = %q, want %q", got, want)
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o644 {
		t.Errorf("config.yml mode = %v, %v", st.Mode(), err)
	}

	edited := "version: 2\nlanguage: de\r\ncontinue-on-restart: true\nforce-load-existing-chunks: true\nsilent: true\nupdate-interval: 30\n# kept\ncustom:\n  language: fr\nlanguage: es\n"
	if err := os.WriteFile(p, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(dir, Bukkit, Config{}, nil); err != nil {
		t.Fatal(err)
	}
	want = "version: 2\nlanguage: en\ncontinue-on-restart: false\nforce-load-existing-chunks: true\nsilent: true\nupdate-interval: 30\n# kept\ncustom:\n  language: fr\nlanguage: en\n"
	if got := readFile(t, p); got != want {
		t.Errorf("edited config.yml = %q, want %q", got, want)
	}

	if err := os.WriteFile(p, []byte("version: 2\nsilent: true"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(dir, Bukkit, Config{UpdateInterval: 10}, nil); err != nil {
		t.Fatal(err)
	}
	want = "version: 2\nsilent: true\nlanguage: en\ncontinue-on-restart: false\nupdate-interval: 10\n"
	if got := readFile(t, p); got != want {
		t.Errorf("sparse config.yml = %q, want %q", got, want)
	}
	onlyFile(t, filepath.Dir(p), "config.yml")

	bad := "language: \xff\xfe\n"
	if err := os.WriteFile(p, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	err := WriteConfig(dir, Bukkit, Config{}, nil)
	if e := wantCode(t, err, CodeConfig); e.Params["file"] != "plugins/Chunky/config.yml" || e.Hint == "" {
		t.Errorf("error = %+v", e)
	}
	if got := readFile(t, p); got != bad {
		t.Errorf("refused config.yml was changed to %q", got)
	}
	onlyFile(t, filepath.Dir(p), "config.yml")
}

func TestWriteConfigJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config", "chunky", "config.json")
	if err := WriteConfig(dir, Fabric, Config{ContinueOnRestart: true}, nil); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(readFile(t, p)), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"version": 2.0, "language": "en", "continueOnRestart": true, "forceLoadExistingChunks": false, "silent": false, "updateInterval": 1.0}
	if len(got) != len(want) {
		t.Errorf("new config.json = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("new config.json %s = %v, want %v", k, got[k], v)
		}
	}

	// Chunky's Gson output, with a saved task and a key from a later version.
	gson := `{
  "version": 2,
  "language": "zh_cn",
  "continueOnRestart": true,
  "forceLoadExistingChunks": true,
  "silent": false,
  "updateInterval": 1,
  "tasks": {"minecraft:overworld": {"cancelled": false, "radius": 2500.0}},
  "future": [1, 2, 3]
}`
	if err := os.WriteFile(p, []byte(gson), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(dir, NeoForge, Config{UpdateInterval: 60}, nil); err != nil {
		t.Fatal(err)
	}
	var merged map[string]json.RawMessage
	out := readFile(t, p)
	if err := json.Unmarshal([]byte(out), &merged); err != nil || !strings.HasSuffix(out, "}\n") {
		t.Fatalf("merged config.json = %s, %v", out, err)
	}
	for k, v := range map[string]string{
		"language": `"en"`, "continueOnRestart": "false", "updateInterval": "60", "forceLoadExistingChunks": "true",
		"tasks": `{"minecraft:overworld": {"cancelled": false, "radius": 2500.0}}`, "future": "[1, 2, 3]",
	} {
		if got := compact(t, merged[k]); got != compact(t, json.RawMessage(v)) {
			t.Errorf("merged %s = %s, want %s", k, got, v)
		}
	}

	for body, msg := range map[string]string{"{\"language\": ": "not valid JSON", "[1, 2]": "does not hold a JSON object", "null": "does not hold a JSON object"} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		err := WriteConfig(dir, Fabric, Config{}, nil)
		if e := wantCode(t, err, CodeConfig); !strings.Contains(e.Msg, msg) || e.Params["file"] != "config/chunky/config.json" {
			t.Errorf("config.json %q: error = %v", body, e.Msg)
		}
		if got := readFile(t, p); got != body {
			t.Errorf("refused config.json was changed to %q", got)
		}
	}

	if err := os.WriteFile(p, []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(dir, Fabric, Config{}, nil); err != nil || !strings.Contains(readFile(t, p), `"silent": false`) {
		t.Errorf("empty config.json not replaced by defaults: %v", err)
	}
	onlyFile(t, filepath.Dir(p), "config.json")
}

func compact(t *testing.T, b json.RawMessage) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

func TestWriteConfigRefuses(t *testing.T) {
	dir := t.TempDir()
	if err := WriteConfig(dir, "", Config{}, nil); err == nil {
		t.Error("unknown platform accepted")
	}
	for _, n := range []int{-1, 3601} {
		if err := WriteConfig(dir, Bukkit, Config{UpdateInterval: n}, nil); err == nil {
			t.Errorf("update interval %d accepted", n)
		}
	}
	if err := WriteConfig(filepath.Join(dir, "missing"), Bukkit, Config{}, nil); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing data directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugins"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	wantCode(t, WriteConfig(dir, Bukkit, Config{}, nil), CodeConfig)
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("data directory holds %d entries after a refused write", len(entries))
	}
}

func TestReadTask(t *testing.T) {
	dir := t.TempDir()
	if _, found, err := ReadTask(dir, Bukkit, "world"); found || err != nil {
		t.Errorf("ReadTask without tasks = %v, %v", found, err)
	}
	// As Chunky 1.5.3's TaskLoader writes it after the radius-640 task in
	// testdata/paper-chunky.log finished.
	writeTask(t, dir, "plugins/Chunky/tasks/world.properties", "world=world\ncancelled=true\ncenter-x=0.0\ncenter-z=0.0\nradius=640.0\nshape=square\npattern=region\nchunks=6561\ntime=154321\n")
	task, found, err := ReadTask(dir, Bukkit, "world")
	want := Task{World: "world", Cancelled: true, Radius: 640, Shape: "square", Pattern: "region", Chunks: 6561, ElapsedSeconds: 154, Total: 6561}
	if err != nil || !found || task != want || !task.Finished() || task.Percent() != 100 {
		t.Errorf("ReadTask = %+v, %v, %v; want %+v", task, found, err, want)
	}

	writeTask(t, dir, "plugins/Chunky/tasks/far.properties", "# saved by hand\r\nworld=far\r\ncancelled=false\r\ncenter-x=2.9999984E7\r\ncenter-z=-1.25E4\r\nradius=1000.0\r\nchunks=-5\r\n")
	task, found, err = ReadTask(dir, Bukkit, "far")
	if err != nil || !found || task.CenterX != 29_999_984 || task.CenterZ != -12_500 || task.Shape != "square" || task.Pattern != "region" || task.Chunks != 0 || task.Total != 127*127 || task.Finished() {
		t.Errorf("hand-edited task = %+v, %v, %v", task, found, err)
	}

	writeTask(t, dir, "plugins/Chunky/tasks/csv.properties", "world=csv\ncancelled=false\nradius=1000.0\nshape=square\npattern=csv\ncsv=chunks\nchunks=10\n")
	if task, _, _ := ReadTask(dir, Bukkit, "csv"); task.Total != 0 || task.Percent() != 0 {
		t.Errorf("csv task = %+v", task)
	}

	writeTask(t, dir, "plugins/Chunky/tasks/big.properties", "world=big\n"+strings.Repeat("#", maxTaskBytes))
	if _, _, err := ReadTask(dir, Bukkit, "big"); err == nil {
		t.Error("oversized task file accepted")
	}
	if _, _, err := ReadTask(dir, Bukkit, "../plugins"); !errors.Is(err, &Error{Code: CodeInvalidWorld}) {
		t.Errorf("traversal: %v", err)
	}
	if _, _, err := ReadTask(dir, Fabric, "minecraft:../../x"); !errors.Is(err, &Error{Code: CodeInvalidWorld}) {
		t.Errorf("dimension traversal: %v", err)
	}
}
