package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

func (e *agentEnv) changeVersion(body map[string]any) (int, map[string]any) {
	e.t.Helper()
	body["actor"] = "admin"
	return e.call("POST", e.sp("/version"), body)
}

func TestCatalogIsLiveFromPaperMCAndExperimentalNeedsConsent(t *testing.T) {
	e := newAgentEnv(t)
	cat := e.a.catalogInfo(t.Context(), "")
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
	if code, out := e.call("POST", "/v1/servers", req); code != 400 || !strings.Contains(out["error"].(string), "experimental") {
		t.Fatalf("an experimental version must not be created without consent: %d %v", code, out)
	}
	if n := len(e.a.serverList()); n != 0 {
		t.Fatal("nothing may be created")
	}
	req["acceptExperimental"] = true
	code, out := e.startCreate(req)
	if code != 202 {
		t.Fatalf("with consent it is created: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("create: %+v", op)
	}
	sc, _ := e.srv().serverConfig()
	if sc.MinecraftVersion != "26.3" || sc.PaperBuild != 41 || len(sc.JarSHA256) != 64 {
		t.Fatalf("the build and its checksum must be pinned: %+v", sc)
	}

	down := newAgentEnv(t)
	down.fill.set("", []fillVersionSpec{})
	if cat := down.a.catalogInfo(t.Context(), ""); cat.VersionsError == "" || len(cat.Versions) != 0 {
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
	if sc, _ := e.srv().serverConfig(); sc.MinecraftVersion != "26.1.2" {
		t.Fatalf("a refused change must not touch the server: %+v", sc)
	}
	sc, _ := e.srv().serverConfig()
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
	backups, _ := e.srv().listBackups(`kind = 'rollback'`)
	if len(backups) != 1 || backups[0].Verified == nil || !*backups[0].Verified || !strings.Contains(backups[0].Note, "before updating from Paper 26.1.2 to 26.2") || op.Detail["backupId"] != backups[0].ID {
		t.Fatalf("a verified backup must be taken first: %+v", backups)
	}
	sc, _ := e.srv().serverConfig()
	if sc.MinecraftVersion != "26.2" || sc.PaperBuild != 129 || sc.JarSHA256 == "" || sc.JarVerifiedAt == nil {
		t.Fatalf("config: %+v", sc)
	}
	e.waitFor("online on 26.2", func() bool { return e.status().Phase == api.PhaseOnline })
	e.fd.mu.Lock()
	jar := env(e.fd.byName[e.cname()].cfg, "CUSTOM_SERVER")
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
	level := filepath.Join(e.dataDir(), "world", "level.dat")
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
	sc, _ := e.srv().serverConfig()
	if sc.MinecraftVersion != "26.1.2" || sc.PaperBuild != 74 {
		t.Fatalf("the previous version must be configured again: %+v", sc)
	}
	e.waitFor("online on 26.1.2", func() bool { return e.status().Phase == api.PhaseOnline && !e.a.busy() })
	matches, _ := filepath.Glob(e.dataDir() + ".failed-update-*")
	if len(matches) != 0 {
		t.Fatalf("the upgraded copy must be removed once the backup is back: %v", matches)
	}
}

// setVersionStep runs fn at each step of a version change once its journal
// is written, until the test ends.
func setVersionStep(t *testing.T, fn func(ctx context.Context, step string)) {
	versionStep = fn
	t.Cleanup(func() { versionStep = func(context.Context, string) {} })
}

// markStartsOn has every start of the server on Minecraft version v leave a
// file in its world, as playing on it would, so a world the backup from
// before an update replaced can be told apart. It returns the file's path.
func (e *agentEnv) markStartsOn(v string) string {
	e.t.Helper()
	marker := filepath.Join(e.dataDir(), "world", "played-on-"+v+".dat")
	e.fd.mu.Lock()
	e.fd.started = func(c *fakeContainer) {
		if strings.HasPrefix(env(c.cfg, "CUSTOM_SERVER"), "/data/paper-"+v+"-") {
			os.WriteFile(marker, []byte("played"), 0o640)
		}
	}
	e.fd.mu.Unlock()
	return marker
}

// bootingOn waits until the server's container runs Minecraft version v,
// started after since, and is still booting.
func (e *agentEnv) bootingOn(v string, since time.Time) bool {
	e.fd.mu.Lock()
	defer e.fd.mu.Unlock()
	c := e.fd.byName[e.cname()]
	return c != nil && c.running && c.started.After(since) && strings.HasPrefix(env(c.cfg, "CUSTOM_SERVER"), "/data/paper-"+v+"-")
}

func (e *agentEnv) versionLeftovers() (copies []string, journal bool) {
	copies, _ = filepath.Glob(e.dataDir() + ".failed-update-*")
	_, err := os.Stat(filepath.Join(filepath.Dir(e.dataDir()), versionJournalFile))
	return copies, err == nil
}

// Stopping the agent, or the agent process dying, while the new Minecraft
// version starts never rolls the update back: the next agent process checks
// the new version again, keeps it once it is online, and records that it did.
func TestVersionChangeSurvivesTheAgentStopping(t *testing.T) {
	for _, tc := range []struct {
		name string
		// step is the version change's step where the agent process dies;
		// without one, the agent stops while the new version boots.
		step string
	}{
		{name: "dies with the new settings saved", step: "starting"},
		{name: "dies once the new version is online", step: "online"},
		{name: "stops while the new version boots"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAgentEnv(t)
			e.create()
			marker := e.markStartsOn("26.2")
			reached := make(chan struct{})
			if tc.step != "" {
				setVersionStep(t, func(ctx context.Context, step string) {
					if step == tc.step {
						close(reached)
						runtime.Goexit()
					}
				})
			} else {
				e.fd.mu.Lock()
				e.fd.bootDelay = 2 * time.Second
				e.fd.mu.Unlock()
			}
			asked := time.Now()
			code, out := e.changeVersion(map[string]any{"versionId": "paper-26.2"})
			if code != 202 {
				t.Fatalf("change: %d %v", code, out)
			}
			opID := out["id"].(string)
			if tc.step != "" {
				waitClosed(t, reached, "the version change's step "+tc.step)
			} else {
				e.waitFor("the new version to boot", func() bool { return e.bootingOn("26.2", asked) })
			}
			e.stop()
			versionStep = func(context.Context, string) {}
			if op := e.opAtRest(opID); op.Status != api.OpRunning || op.Error != "" {
				t.Fatalf("the version change must stay running for the next agent process to finish: %+v", op)
			}
			if copies, journal := e.versionLeftovers(); len(copies) != 0 || !journal {
				t.Fatalf("the agent going away must leave the update and its journal as they are: copies %v, journal %v", copies, journal)
			}
			e.fd.mu.Lock()
			e.fd.bootDelay = 30 * time.Millisecond
			e.fd.mu.Unlock()
			e.start()
			op := e.waitOp(opID)
			if op.Status != api.OpSucceeded || op.Detail["resumedAfterRestart"] != true || op.Phase != string(api.PhaseOnline) {
				t.Fatalf("the next agent process must finish the version change once the new version is online: %+v", op)
			}
			if sc, _ := e.srv().serverConfig(); sc == nil || sc.MinecraftVersion != "26.2" || sc.PaperBuild != 129 {
				t.Fatalf("the new version is not kept: %+v", sc)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatal("the world the new version started on was replaced")
			}
			if copies, journal := e.versionLeftovers(); len(copies) != 0 || journal {
				t.Fatalf("the finished version change left copies %v or its journal (%v)", copies, journal)
			}
			if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'server.version' AND actor = 'admin' AND result = 'succeeded' AND detail LIKE '%; finished after the Playkeeper agent restarted'`); n != 1 {
				t.Fatalf("want the version change audited once as finished after the restart, got %d", n)
			}
			if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'server.version' AND result <> 'succeeded'`); n != 0 {
				t.Fatalf("the version change was also audited as rolled back or failed %d times", n)
			}
			if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'update-version'`); n != 1 || e.opsOf("update-version", api.OpSucceeded) != 1 {
				t.Fatalf("want the version change recorded once, as succeeded; %d audit rows", n)
			}
			e.waitFor("online on 26.2", e.onlineIdle)
		})
	}
}

// A new version that does not start is rolled back even when the agent stops
// or dies in the middle of the rollback: the next agent process finishes
// putting the backup from before the update and the previous settings back,
// and starts the previous version. A new version that was still starting when
// the agent stopped, and then fails, is rolled back the same way.
func TestVersionRollbackSurvivesTheAgentStopping(t *testing.T) {
	for _, tc := range []struct {
		name string
		// step is the rollback's step where the agent process dies, or where
		// the agent stops with stop; without one, the agent stops while the
		// new version boots.
		step string
		stop bool
	}{
		{name: "dies before the world moves", step: "reverting"},
		{name: "dies with the new version's world moved aside", step: "moved"},
		{name: "stops before the world moves", step: "reverting", stop: true},
		{name: "stops while the new version boots"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAgentEnv(t)
			e.create()
			level := filepath.Join(e.dataDir(), "world", "level.dat")
			before, err := os.ReadFile(level)
			if err != nil {
				t.Fatal(err)
			}
			reached := make(chan struct{})
			if tc.step != "" {
				e.fd.mu.Lock()
				e.fd.bootFailsOn = "26.2"
				e.fd.mu.Unlock()
				setVersionStep(t, func(ctx context.Context, step string) {
					if step != tc.step {
						return
					}
					close(reached)
					if tc.stop {
						<-ctx.Done()
						return
					}
					runtime.Goexit()
				})
			} else {
				e.fd.mu.Lock()
				e.fd.bootDelay = 3 * time.Second
				e.fd.mu.Unlock()
			}
			asked := time.Now()
			code, out := e.changeVersion(map[string]any{"versionId": "paper-26.2"})
			if code != 202 {
				t.Fatalf("change: %d %v", code, out)
			}
			opID := out["id"].(string)
			if tc.step != "" {
				waitClosed(t, reached, "the rollback's step "+tc.step)
			} else {
				e.waitFor("the new version to boot", func() bool {
					if !e.bootingOn("26.2", asked) {
						return false
					}
					// It fails when its boot ends, after the agent restarted.
					e.fd.mu.Lock()
					e.fd.bootFailsOn, e.fd.bootDelay = "26.2", 30*time.Millisecond
					e.fd.mu.Unlock()
					return true
				})
			}
			e.stop()
			versionStep = func(context.Context, string) {}
			if op := e.opAtRest(opID); op.Status != api.OpRunning || op.Error != "" {
				t.Fatalf("the version change must stay running for the next agent process to finish: %+v", op)
			}
			e.start()
			op := e.waitOp(opID)
			if op.Status != api.OpFailed || !strings.Contains(op.Error, "did not start (The server stopped while starting (exit code 1).") ||
				!strings.HasSuffix(op.Error, "so Playkeeper put the backup from before the update back. The server runs 26.1.2 again.") || op.Detail["resumedAfterRestart"] != true {
				t.Fatalf("the next agent process must finish the rollback and say so: %+v", op)
			}
			if after, _ := os.ReadFile(level); string(after) != string(before) {
				t.Fatalf("the world the failed start upgraded must be replaced by the backup: %q, want %q", after, before)
			}
			if sc, _ := e.srv().serverConfig(); sc == nil || sc.MinecraftVersion != "26.1.2" || sc.PaperBuild != 74 {
				t.Fatalf("the previous version must be configured again: %+v", sc)
			}
			if copies, journal := e.versionLeftovers(); len(copies) != 0 || journal {
				t.Fatalf("the finished rollback left copies %v or its journal (%v)", copies, journal)
			}
			if left, _ := os.ReadDir(e.cfg.StagingDir()); len(left) != 0 {
				t.Fatalf("the rollback left a stage: %v", left)
			}
			if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'server.version' AND result = 'rolled back'`); n != 1 {
				t.Fatalf("want the version change audited once as rolled back, got %d", n)
			}
			if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'update-version'`); n != 1 || e.opsOf("update-version", api.OpFailed) != 1 {
				t.Fatalf("want the version change recorded once, as failed; %d audit rows", n)
			}
			e.waitFor("online on 26.1.2", e.onlineIdle)
		})
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
