package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
)

func (e *agentEnv) changeVersion(body map[string]any) (int, map[string]any) {
	e.t.Helper()
	body["actor"] = "admin"
	return e.call("POST", "/v1/server/version", body)
}

func TestCatalogIsLiveFromPaperMCAndExperimentalNeedsConsent(t *testing.T) {
	e := newAgentEnv(t)
	cat := e.a.catalogInfo(t.Context())
	if cat.VersionsError != "" || len(cat.Versions) != 3 {
		t.Fatalf("catalog: %+v", cat)
	}
	if v := cat.Versions[0]; v.ID != "paper-26.3" || !v.Experimental || v.Recommended {
		t.Fatalf("26.3 has only alpha builds and must be offered as experimental: %+v", v)
	}
	if v := cat.Versions[1]; v.ID != "paper-26.2" || !v.Recommended || v.PaperBuild != 129 {
		t.Fatalf("26.2 is the latest stable version and must be recommended: %+v", v)
	}
	req := map[string]any{"acceptEula": true, "versionId": "paper-26.3", "memoryMB": 1536, "actor": "admin"}
	if code, out := e.call("POST", "/v1/server", req); code != 400 || !strings.Contains(out["error"].(string), "experimental") {
		t.Fatalf("an experimental version must not be created without consent: %d %v", code, out)
	}
	if sc, _ := e.a.serverConfig(); sc != nil {
		t.Fatal("nothing may be created")
	}
	req["acceptExperimental"] = true
	code, out := e.call("POST", "/v1/server", req)
	if code != 202 {
		t.Fatalf("with consent it is created: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("create: %+v", op)
	}
	sc, _ := e.a.serverConfig()
	if sc.MinecraftVersion != "26.3" || sc.PaperBuild != 41 || len(sc.JarSHA256) != 64 {
		t.Fatalf("the build and its checksum must be pinned: %+v", sc)
	}

	down := newAgentEnv(t)
	down.fill.set("", []fillVersionSpec{})
	if cat := down.a.catalogInfo(t.Context()); cat.VersionsError == "" || len(cat.Versions) != 0 {
		t.Fatalf("an empty list from PaperMC must be reported: %+v", cat)
	}
}

func TestVersionChangesNeverGoBack(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	for _, tc := range []struct {
		body map[string]any
		code int
		want string
	}{
		{map[string]any{"versionId": "paper-26.1.2"}, 409, "not newer"},
		{map[string]any{"versionId": "paper-26.3"}, 400, "experimental"},
		{map[string]any{"versionId": "latest"}, 400, "Unknown server version"},
		{map[string]any{"versionId": "paper-26.2", "extra": 1}, 400, "unknown field"},
	} {
		if code, out := e.changeVersion(tc.body); code != tc.code || !strings.Contains(out["error"].(string), tc.want) {
			t.Errorf("%v: want %d %q, got %d %v", tc.body, tc.code, tc.want, code, out)
		}
	}
	if sc, _ := e.a.serverConfig(); sc.MinecraftVersion != "26.1.2" {
		t.Fatalf("a refused change must not touch the server: %+v", sc)
	}
	sc, _ := e.a.serverConfig()
	for _, target := range []api.CatalogEntry{{MinecraftVersion: "1.21.11", PaperBuild: 132}, {MinecraftVersion: "26.1.2", PaperBuild: 70}} {
		err := checkNewer(*sc, target)
		if err == nil || (target.MinecraftVersion == "1.21.11" && !strings.Contains(err.Error(), "cannot go back from 26.1.2 to 1.21.11")) {
			t.Errorf("going to %s build %d must be refused: %v", target.MinecraftVersion, target.PaperBuild, err)
		}
	}
	if err := checkNewer(*sc, api.CatalogEntry{MinecraftVersion: "26.1.2", PaperBuild: 80}); err != nil {
		t.Errorf("a newer build of the same version is an update: %v", err)
	}
}

func TestVersionChangeBacksUpFirstAndStartsTheNewVersion(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	code, out := e.changeVersion(map[string]any{"versionId": "paper-26.2"})
	if code != 202 {
		t.Fatalf("change: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded {
		t.Fatalf("op: %+v", op)
	}
	backups, _ := e.a.listBackups(`WHERE kind = 'rollback'`)
	if len(backups) != 1 || backups[0].Verified == nil || !*backups[0].Verified || !strings.Contains(backups[0].Note, "before updating from Paper 26.1.2 to 26.2") || op.Detail["backupId"] != backups[0].ID {
		t.Fatalf("a verified backup must be taken first: %+v", backups)
	}
	sc, _ := e.a.serverConfig()
	if sc.MinecraftVersion != "26.2" || sc.PaperBuild != 129 || sc.JarSHA256 == "" || sc.JarVerifiedAt == nil {
		t.Fatalf("config: %+v", sc)
	}
	e.waitFor("online on 26.2", func() bool { return e.status().Phase == api.PhaseOnline })
	e.fd.mu.Lock()
	jar := env(e.fd.byName[containerName].cfg, "CUSTOM_SERVER")
	e.fd.mu.Unlock()
	if jar != "/data/paper-26.2-129.jar" {
		t.Fatalf("the server must run the new jar: %s", jar)
	}
	if code, out := e.changeVersion(map[string]any{"versionId": "paper-26.1.2"}); code != 409 || !strings.Contains(out["error"].(string), "cannot go back from 26.2 to 26.1.2") {
		t.Fatalf("after the update, the old version is refused: %d %v", code, out)
	}
}

func TestVersionThatDoesNotStartPutsTheWorldBack(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	level := filepath.Join(e.cfg.ServerDataDir(), "world", "level.dat")
	before, err := os.ReadFile(level)
	if err != nil {
		t.Fatal(err)
	}
	e.fd.mu.Lock()
	e.fd.bootFailsOn = "26.2"
	e.fd.mu.Unlock()
	code, out := e.changeVersion(map[string]any{"versionId": "paper-26.2"})
	if code != 202 {
		t.Fatalf("change: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "put the backup from before the update back") || !strings.Contains(op.Error, "runs 26.1.2 again") {
		t.Fatalf("op: %+v", op)
	}
	if after, _ := os.ReadFile(level); string(after) != string(before) {
		t.Fatalf("the world the failed start upgraded must be replaced by the backup: %q, want %q", after, before)
	}
	sc, _ := e.a.serverConfig()
	if sc.MinecraftVersion != "26.1.2" || sc.PaperBuild != 74 {
		t.Fatalf("the previous version must be configured again: %+v", sc)
	}
	e.waitFor("online on 26.1.2", func() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() })
	matches, _ := filepath.Glob(e.cfg.ServerDataDir() + ".failed-update-*")
	if len(matches) != 0 {
		t.Fatalf("the upgraded copy must be removed once the backup is back: %v", matches)
	}
}

func TestServersFrom010KeepTheirPinnedChecksum(t *testing.T) {
	if sum, err := jarChecksum(api.ServerConfig{MinecraftVersion: "26.1.2", PaperBuild: 74}); err != nil || len(sum) != 64 {
		t.Fatalf("a 0.1.0 server must still verify its jar: %q %v", sum, err)
	}
	if _, err := jarChecksum(api.ServerConfig{MinecraftVersion: "26.2", PaperBuild: 129}); err == nil {
		t.Fatal("a build without a recorded or known checksum must not run")
	}
}
