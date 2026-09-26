package agent

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
)

var vanilla262 = map[string]any{"type": "vanilla", "versionId": "vanilla-26.2", "memoryMB": 1536}

func (e *agentEnv) runOp(method, path string) *api.Operation {
	e.t.Helper()
	code, out := e.call(method, e.sp(path), map[string]any{"actor": "admin"})
	if code != 202 {
		e.t.Fatalf("%s %s: %d %v", method, path, code, out)
	}
	return e.waitOp(out["id"].(string))
}

func (e *agentEnv) containerEnvVar(key string) string {
	e.t.Helper()
	e.fd.mu.Lock()
	defer e.fd.mu.Unlock()
	c := e.fd.byName[e.cname()]
	if c == nil {
		e.t.Fatal("no server container")
	}
	return env(c.cfg, key)
}

func (e *agentEnv) countEvents(kind string) int {
	e.t.Helper()
	var n int
	if err := e.a.db.QueryRow(`SELECT COUNT(*) FROM events WHERE server_id = ? AND kind = ?`, e.sid, kind).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestCatalogOffersEveryTypeWithReleaseDates(t *testing.T) {
	e := newAgentEnv(t)
	code, out := e.call("GET", "/v1/catalog", nil)
	if code != 200 || out["type"] != "paper" {
		t.Fatalf("catalog: %d %v", code, out)
	}
	checks := map[string]string{}
	for _, v := range out["types"].([]any) {
		ty := v.(map[string]any)
		if ty["available"] != true {
			t.Errorf("%v must be available", ty["id"])
		}
		checks[ty["id"].(string)] = ty["check"].(string)
	}
	want := map[string]string{"paper": "full", "vanilla": "full", "purpur": "weak_hash", "fabric": "full", "quilt": "full", "neoforge": "recorded_outputs"}
	for id, c := range want {
		if checks[id] != c {
			t.Errorf("%s is checked %q, want %q", id, checks[id], c)
		}
	}
	dated := false
	for _, v := range out["versions"].([]any) {
		if e := v.(map[string]any); e["minecraftVersion"] == "26.2" {
			dated = e["releasedAt"] == "2026-09-02T09:14:07Z"
		}
	}
	if !dated {
		t.Fatalf("Paper's versions carry Mojang's release dates: %v", out["versions"])
	}
	if out["latestRelease"] != "26.2" {
		t.Fatalf("the newest release, snapshots left out: %v", out["latestRelease"])
	}

	code, out = e.call("GET", "/v1/catalog?type=vanilla", nil)
	if code != 200 || out["type"] != "vanilla" {
		t.Fatalf("vanilla catalog: %d %v", code, out)
	}
	versions := out["versions"].([]any)
	if len(versions) != 2 {
		t.Fatalf("one version per family, snapshots left out: %v", versions)
	}
	first := versions[0].(map[string]any)
	sw, _ := first["software"].(map[string]any)
	if first["id"] != "vanilla-26.2" || first["recommended"] != true || first["releasedAt"] != "2026-09-02T09:14:07Z" || sw["type"] != "vanilla" || sw["minecraftVersion"] != "26.2" {
		t.Fatalf("newest vanilla version: %v", first)
	}
	if code, _ := e.call("GET", "/v1/catalog?type=forge", nil); code != 400 {
		t.Fatalf("an unknown type: %d", code)
	}
}

func TestCatalogBuildsListsEachTypesBuilds(t *testing.T) {
	e := newAgentEnv(t)
	e.up.serveFabricLists()
	code, out := e.call("GET", "/v1/catalog/builds?type=fabric&version=26.2", nil)
	if code != 200 {
		t.Fatalf("builds: %d %v", code, out)
	}
	var got []string
	for _, b := range out["builds"].([]any) {
		m := b.(map[string]any)
		got = append(got, m["version"].(string))
		if m["channel"] != "stable" || m["recommended"] != (m["version"] == "0.17.2") {
			t.Errorf("build %v", m)
		}
	}
	if strings.Join(got, " ") != "0.17.2 0.17.1" {
		t.Fatalf("newest first, loaders Playkeeper can't launch left out: %v", got)
	}
	if code, out := e.call("GET", "/v1/catalog/builds?type=vanilla&version=26.2", nil); code != 200 || len(out["builds"].([]any)) != 0 {
		t.Fatalf("Vanilla has no builds: %d %v", code, out)
	}
	for _, q := range []string{"type=paper&version=26.2", "type=fabric&version=1.20.1", "type=fabric&version=26.9", "type=fabric&version=../26.2"} {
		if code, _ := e.call("GET", "/v1/catalog/builds?"+q, nil); code != 400 {
			t.Errorf("%s: %d, want 400", q, code)
		}
	}
	code, out = e.startCreate(map[string]any{"type": "fabric", "versionId": "fabric-26.2", "build": "0.99.0"})
	if code != 400 || !strings.Contains(out["error"].(string), "no build") {
		t.Fatalf("a build Fabric doesn't list: %d %v", code, out)
	}
}

func TestVanillaInstallIsVerifiedAndCheckedBeforeEveryStart(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(vanilla262)
	sc, _ := e.srv().serverConfig()
	if sc.Type != "vanilla" || sc.Software == nil || sc.Software.Type != "vanilla" || sc.Software.MinecraftVersion != "26.2" || sc.VersionID != "vanilla-26.2" || sc.PaperBuild != 0 || sc.JarVerifiedAt == nil {
		t.Fatalf("config: %+v", sc)
	}
	jar := filepath.Join(e.dataDir(), "minecraft_server.26.2.jar")
	if got, _ := os.ReadFile(jar); !bytes.Equal(got, e.up.serverJar["26.2"]) {
		t.Fatalf("the server jar is Mojang's: %q", got)
	}
	fi, err := os.Stat(e.srv().manifestPath())
	if err != nil || fi.Mode().Perm() != 0o600 || strings.HasPrefix(e.srv().manifestPath(), e.dataDir()) {
		t.Fatalf("the install's record stays out of the container's reach: %v %v", fi, err)
	}
	if e.containerEnvVar("TYPE") != "CUSTOM" || e.containerEnvVar("CUSTOM_SERVER") != "/data/minecraft_server.26.2.jar" || e.containerEnvVar("VERSION") != "26.2" {
		t.Fatal("the container runs the verified jar")
	}
	if _, err := os.Stat(filepath.Join(e.dataDir(), "plugins")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a server without plugins gets no plugins folder: %v", err)
	}
	if op := e.runOp("POST", "/restart"); op.Status != api.OpSucceeded {
		t.Fatalf("restart: %+v", op)
	}
	e.waitFor("online", func() bool { return e.status().Phase == api.PhaseOnline })
	if n := e.up.hitCount(e.up.serverJarURL("26.2")); n != 1 {
		t.Fatalf("a matching install is not downloaded again: %d downloads", n)
	}
	if e.countEvents("server_software_verified") != 1 || e.countEvents("server_created") != 1 {
		t.Fatal("the verified install is in the activity")
	}
}

func TestChangedSoftwareKeepsTheServerOffUntilReinstalled(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(vanilla262)
	if op := e.runOp("POST", "/stop"); op.Status != api.OpSucceeded {
		t.Fatalf("stop: %+v", op)
	}
	jar := filepath.Join(e.dataDir(), "minecraft_server.26.2.jar")
	if err := os.WriteFile(jar, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	op := e.runOp("POST", "/start")
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "doesn't match what Playkeeper installed") || !strings.Contains(op.Hint, "reinstall") {
		t.Fatalf("start: %+v", op)
	}
	st := e.status()
	c := st.SoftwareChanged
	if c == nil || c.File != "minecraft_server.26.2.jar" || c.Algorithm != "sha1" || c.Recorded != sha1Hex(e.up.serverJar["26.2"]) || c.Found != sha1Hex([]byte("tampered")) ||
		c.InstalledAt == nil || c.ChangedAt == nil || c.Software != "Vanilla 26.2" {
		t.Fatalf("status: %+v", c)
	}
	if st.Phase != api.PhaseCrashed || st.Desired != api.DesiredStopped {
		t.Fatalf("phase %s, desired %s", st.Phase, st.Desired)
	}
	if got, _ := os.ReadFile(jar); string(got) != "tampered" {
		t.Fatal("a changed file is not replaced on its own")
	}
	if e.countEvents("server_software_changed") != 1 {
		t.Fatal("the change is in the activity")
	}
	if op := e.runOp("POST", "/start"); op.Status != api.OpFailed || e.countEvents("server_software_changed") != 1 {
		t.Fatalf("starting again is refused again, recorded once: %+v", op)
	}

	op = e.runOp("POST", "/software/reinstall")
	if op.Status != api.OpSucceeded || op.Kind != "reinstall" {
		t.Fatalf("reinstall: %+v", op)
	}
	e.waitFor("online", func() bool { return e.status().Phase == api.PhaseOnline })
	if got, _ := os.ReadFile(jar); !bytes.Equal(got, e.up.serverJar["26.2"]) {
		t.Fatal("the reinstall downloads Mojang's jar again")
	}
	if st := e.status(); st.SoftwareChanged != nil || st.Desired != api.DesiredRunning {
		t.Fatalf("after the reinstall: %+v", st.SoftwareChanged)
	}
}

func TestMissingSoftwareIsInstalledAgain(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(vanilla262)
	if op := e.runOp("POST", "/stop"); op.Status != api.OpSucceeded {
		t.Fatalf("stop: %+v", op)
	}
	jar := filepath.Join(e.dataDir(), "minecraft_server.26.2.jar")
	if err := os.Remove(jar); err != nil {
		t.Fatal(err)
	}
	if op := e.runOp("POST", "/start"); op.Status != api.OpSucceeded {
		t.Fatalf("start: %+v", op)
	}
	if got, _ := os.ReadFile(jar); !bytes.Equal(got, e.up.serverJar["26.2"]) || e.status().SoftwareChanged != nil {
		t.Fatal("a missing file is downloaded and checked again")
	}
}

func TestChangedPaperJarKeepsTheServerOff(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	if op := e.runOp("POST", "/stop"); op.Status != api.OpSucceeded {
		t.Fatalf("stop: %+v", op)
	}
	sc, _ := e.srv().serverConfig()
	jar := e.srv().jarPath(*sc)
	if err := os.WriteFile(jar, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if op := e.runOp("POST", "/start"); op.Status != api.OpFailed {
		t.Fatalf("start: %+v", op)
	}
	c := e.status().SoftwareChanged
	if c == nil || c.File != filepath.Base(jar) || c.Algorithm != "sha256" || c.Recorded != sc.JarSHA256 || c.Software != "Paper 26.1.2 build "+strconv.Itoa(sc.PaperBuild) {
		t.Fatalf("status: %+v", c)
	}
	if op := e.runOp("POST", "/software/reinstall"); op.Status != api.OpSucceeded {
		t.Fatalf("reinstall: %+v", op)
	}
	if got, _ := os.ReadFile(jar); !bytes.Equal(got, e.fd.jarContent) || e.status().SoftwareChanged != nil {
		t.Fatal("the reinstall downloads Paper again")
	}
}

func TestVanillaVersionChange(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(map[string]any{"type": "vanilla", "versionId": "vanilla-26.1.2"})
	code, out := e.changeVersion(map[string]any{"versionId": "vanilla-26.2"})
	if code != 202 {
		t.Fatalf("change: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded || op.Detail["to"] != "Vanilla 26.2" {
		t.Fatalf("op: %+v", op)
	}
	sc, _ := e.srv().serverConfig()
	if sc.Software == nil || sc.Software.MinecraftVersion != "26.2" || sc.VersionID != "vanilla-26.2" || sc.MinecraftVersion != "26.2" {
		t.Fatalf("config: %+v", sc)
	}
	backups, _ := e.srv().listBackups(`kind = 'rollback'`)
	if len(backups) != 1 || !strings.Contains(backups[0].Note, "before updating from Vanilla 26.1.2 to 26.2") {
		t.Fatalf("a backup first: %+v", backups)
	}
	e.waitFor("online on 26.2", func() bool { return e.status().Phase == api.PhaseOnline })
	if e.containerEnvVar("CUSTOM_SERVER") != "/data/minecraft_server.26.2.jar" {
		t.Fatal("the server runs the new jar")
	}
	if code, _ := e.changeVersion(map[string]any{"versionId": "paper-26.2"}); code != 400 {
		t.Fatalf("another type's version: %d", code)
	}
}

func TestRestoringABackupKeepsTheServerType(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(vanilla262)
	id, phrase := e.backupAndStage()
	code, preview := e.call("GET", "/v1/restore/"+id, nil)
	m, _ := preview["manifest"].(map[string]any)
	if code != 200 || preview["compatible"] != true || m["type"] != "vanilla" {
		t.Fatalf("preview: %d %v", code, preview)
	}
	if op := e.applyRestore(id, phrase); op.Status != api.OpSucceeded {
		t.Fatalf("restore: %+v", op)
	}
	sc, _ := e.srv().serverConfig()
	if sc.Type != "vanilla" || sc.Software == nil || sc.Software.Type != "vanilla" || sc.Software.MinecraftVersion != "26.2" || sc.PaperBuild != 0 {
		t.Fatalf("a restored Vanilla server stays Vanilla: %+v", sc)
	}
}
