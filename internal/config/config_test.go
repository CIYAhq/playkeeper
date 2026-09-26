package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadStartsFromTheDefaults(t *testing.T) {
	c, err := Load(write(t, `{"installID": "a1b2c3", "gameUID": 1001, "gameGID": 1001}`))
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	want.InstallID, want.GameUID, want.GameGID = "a1b2c3", 1001, 1001
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("got %+v, want %+v", c, want)
	}
}

func TestLoadNamesTheFileItCannotReadOrParse(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "config.json")
	if _, err := Load(missing); err == nil || !strings.Contains(err.Error(), "read config "+missing) {
		t.Fatalf("a missing file: %v", err)
	}
	broken := write(t, `{"panelPort": `)
	if _, err := Load(broken); err == nil || !strings.Contains(err.Error(), "parse config "+broken) {
		t.Fatalf("a broken file: %v", err)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("the defaults: %v", err)
	}
	c := Default()
	c.PanelPort, c.GamePort, c.DataDir, c.SocketPath = 0, 70000, "var/lib/playkeeper", "agent.sock"
	err := c.Validate()
	for _, want := range []string{"panelPort 0 out of range", "gamePort 70000 out of range", `dataDir must be absolute, got "var/lib/playkeeper"`, `socketPath must be absolute, got "agent.sock"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q in %v", want, err)
		}
	}
	c = Default()
	c.GamePort = c.PanelPort
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "panelPort and gamePort must differ") {
		t.Fatalf("the same port twice: %v", err)
	}
	if _, err := Load(write(t, `{"gamePort": 8443}`)); err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("Load validates: %v", err)
	}
}

func TestSaveWritesWhatLoadReads(t *testing.T) {
	c := Default()
	c.PanelBind, c.Domain, c.ACMEEmail, c.InstallID, c.GameUID, c.GameGID, c.ReleaseURL = "127.0.0.1", "play.example.test", "owner@example.test", "a1b2c3", 1001, 1002, "https://example.test/releases"
	path := filepath.Join(t.TempDir(), "config.json")
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || !reflect.DeepEqual(got, c) {
		t.Fatalf("got %+v (%v), want %+v", got, err, c)
	}
	b, _ := os.ReadFile(path)
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 || !strings.HasSuffix(string(b), "}\n") {
		t.Fatalf("mode %v, content %q", st.Mode(), b)
	}
}

func TestEveryPathIsUnderTheDataDir(t *testing.T) {
	c := Default()
	c.DataDir = "/srv/pk"
	for _, p := range []string{c.AgentDir(), c.PanelDir(), c.ServerDataDir(), c.BackupsDir(), c.StagingDir(), c.RCONSecretPath(), c.TLSDir(), c.SetupTokenPath(), c.ManifestPath()} {
		if !strings.HasPrefix(p, "/srv/pk/") {
			t.Errorf("%s is outside the data directory", p)
		}
	}
}
