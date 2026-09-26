package agent

import (
	"archive/zip"
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// fakePack is a small Vanilla pack on the fake Modrinth: two mods it
// downloads and a config file inside its archive.
type fakePack struct {
	archiveURL string
	mods       map[string][]byte // download URL → file
	modURLs    []string
}

const (
	fakePackID      = "TPK00001"
	fakePackVersion = "TPV00001"
)

func sha512Hex(b []byte) string {
	s := sha512.Sum512(b)
	return hex.EncodeToString(s[:])
}

func (f *fakeUpstream) serveJSON(rawURL string, v any) {
	f.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		f.t.Fatal(err)
	}
	f.serve(rawURL, b)
}

// servePack puts the pack, its project and version, and its mods on the
// fake Modrinth.
func (f *fakeUpstream) servePack() *fakePack {
	f.t.Helper()
	return f.servePackWith(nil)
}

// servePackWith is servePack with more files in the pack's archive, by
// their path in it.
func (f *fakeUpstream) servePackWith(extra map[string][]byte) *fakePack {
	f.t.Helper()
	return f.servePackFor("26.2", extra)
}

// servePackFor is servePackWith for Minecraft version mc.
func (f *fakeUpstream) servePackFor(mc string, extra map[string][]byte) *fakePack {
	f.t.Helper()
	p := &fakePack{mods: map[string][]byte{}}
	var files []map[string]any
	for _, m := range []struct{ project, name string }{{"WAYS0001", "waystones"}, {"CHNK0001", "chunky"}} {
		body := []byte("fake mod " + m.name)
		u := "https://cdn.modrinth.com/data/" + m.project + "/versions/v1/" + m.name + ".jar"
		p.mods[u] = body
		p.modURLs = append(p.modURLs, u)
		f.serve(u, body)
		files = append(files, map[string]any{
			"path": "mods/" + m.name + ".jar", "hashes": map[string]string{"sha1": sha1Hex(body), "sha512": sha512Hex(body)},
			"env": map[string]string{"client": "required", "server": "required"}, "downloads": []string{u}, "fileSize": len(body),
		})
	}
	index, err := json.Marshal(map[string]any{"formatVersion": 1, "game": "minecraft", "versionId": "1.0.0", "name": "Test Pack",
		"files": files, "dependencies": map[string]string{"minecraft": mc}})
	if err != nil {
		f.t.Fatal(err)
	}
	entries := map[string][]byte{"modrinth.index.json": index, "overrides/config/testpack.toml": []byte("spawn = true\n")}
	maps.Copy(entries, extra)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			f.t.Fatal(err)
		}
		w.Write(body)
	}
	if err := zw.Close(); err != nil {
		f.t.Fatal(err)
	}
	archive := buf.Bytes()
	p.archiveURL = "https://cdn.modrinth.com/data/" + fakePackID + "/versions/" + fakePackVersion + "/test-pack-1.0.0.mrpack"
	f.serve(p.archiveURL, archive)

	version := map[string]any{
		"id": fakePackVersion, "project_id": fakePackID, "name": "Test Pack 1.0.0", "version_number": "1.0.0", "version_type": "release",
		"status": "listed", "game_versions": []string{mc}, "loaders": []string{"minecraft"}, "date_published": "2026-09-20T10:00:00Z",
		"files": []map[string]any{{"url": p.archiveURL, "filename": "test-pack-1.0.0.mrpack", "primary": true, "size": len(archive),
			"hashes": map[string]string{"sha1": sha1Hex(archive), "sha512": sha512Hex(archive)}}},
		"dependencies": []map[string]any{{"project_id": "WAYS0001", "dependency_type": "embedded"}, {"project_id": "CHNK0001", "dependency_type": "embedded"}},
	}
	project := map[string]any{
		"id": fakePackID, "slug": "testpack", "project_type": "modpack", "title": "Waystones Pack", "description": "Teleport with friends.",
		"categories": []string{"adventure"}, "loaders": []string{"minecraft"}, "game_versions": []string{mc}, "client_side": "required",
		"server_side": "required", "status": "approved", "downloads": 1234, "icon_url": "https://cdn.modrinth.com/data/" + fakePackID + "/icon.png",
		"license": map[string]string{"id": "MIT"}, "updated": "2026-09-20T10:00:00Z", "versions": []string{fakePackVersion},
	}
	f.serveJSON("https://api.modrinth.com/v2/project/testpack", project)
	f.serveJSON("https://api.modrinth.com/v2/project/"+fakePackID, project)
	f.serveJSON("https://api.modrinth.com/v2/project/"+fakePackID+"/version", []any{version})
	f.serveJSON("https://api.modrinth.com/v2/version/"+fakePackVersion, version)
	f.serveJSON("https://api.modrinth.com/v2/versions", []any{version})
	f.serveJSON("https://api.modrinth.com/v2/projects", []any{
		map[string]any{"id": "WAYS0001", "slug": "waystones", "project_type": "mod", "title": "Waystones"},
		map[string]any{"id": "CHNK0001", "slug": "chunky", "project_type": "mod", "title": "Chunky"},
	})
	f.serveJSON("https://api.modrinth.com/v2/search", map[string]any{"total_hits": 1, "offset": 0, "limit": 20, "hits": []any{map[string]any{
		"project_id": fakePackID, "project_type": "modpack", "slug": "testpack", "author": "pip", "title": "Waystones Pack",
		"description": "Teleport with friends.", "categories": []string{"adventure", "minecraft"}, "versions": []string{mc},
		"downloads": 1234, "date_modified": "2026-09-20T10:00:00Z", "latest_version": fakePackVersion, "license": "MIT",
		"client_side": "required", "server_side": "required",
	}}})
	return p
}

var packCreate = map[string]any{"versionId": "", "memoryMB": 1536, "modpack": map[string]any{"source": "modrinth", "projectId": fakePackID, "versionId": fakePackVersion}}

func TestModpackSearchAndDetails(t *testing.T) {
	e := newAgentEnv(t)
	e.up.servePack()
	code, out := e.call("GET", "/v1/modpacks?q=waystones&sort=downloads", nil)
	if code != 200 {
		t.Fatalf("search: %d %v", code, out)
	}
	cards := out["cards"].([]any)
	if len(cards) != 1 || out["sources"].([]any)[0] != "modrinth" || len(out["sources"].([]any)) != 1 {
		t.Fatalf("one card, Modrinth only without a CurseForge key: %v", out)
	}
	c := cards[0].(map[string]any)
	if c["name"] != "Waystones Pack" || c["mods"] != float64(2) || c["memoryMB"] != float64(4096) || c["types"].([]any)[0] != "vanilla" {
		t.Fatalf("card: %v", c)
	}

	code, out = e.call("GET", "/v1/modpacks/modrinth/testpack", nil)
	if code != 200 || out["newest"] != fakePackVersion || out["headline"] != "Waystones" || out["mods"] != float64(2) || out["memoryMB"] != float64(4096) {
		t.Fatalf("details: %d %v", code, out)
	}
	for _, path := range []string{"/v1/modpacks/hangar/testpack", "/v1/modpacks/modrinth/...", "/v1/modpacks/modrinth/bad!id", "/v1/modpacks?q=" + strings.Repeat("x", 200), "/v1/modpacks?offset=x"} {
		if code, out := e.call("GET", path, nil); code != 400 {
			t.Errorf("%s: %d %v", path, code, out)
		}
	}
}

func TestModpackPreviewReadsThePack(t *testing.T) {
	e := newAgentEnv(t)
	p := e.up.servePack()
	path := "/v1/modpacks/modrinth/" + fakePackID + "/versions/" + fakePackVersion + "/preview"
	code, out := e.call("GET", path, nil)
	if code != 200 {
		t.Fatalf("preview: %d %v", code, out)
	}
	if out["type"] != "vanilla" || out["minecraftVersion"] != "26.2" || out["files"] != float64(3) || out["ready"] != true ||
		out["downloadSize"] != float64(len("fake mod waystones")+len("fake mod chunky")) {
		t.Fatalf("preview: %v", out)
	}
	if code, _ := e.call("GET", path, nil); code != 200 || e.up.hitCount(p.archiveURL) != 1 {
		t.Fatalf("a second look reuses the first: %d, %d downloads", code, e.up.hitCount(p.archiveURL))
	}
	for _, u := range p.modURLs {
		if n := e.up.hitCount(u); n != 0 {
			t.Errorf("a preview downloads only the pack, yet %s was fetched %d times", u, n)
		}
	}
	left, _ := filepath.Glob(filepath.Join(e.cfg.StagingDir(), "pack-preview-*"))
	if len(left) != 0 {
		t.Fatalf("the preview's folder is removed: %v", left)
	}
}

func TestCreateFromModpack(t *testing.T) {
	e := newAgentEnv(t)
	p := e.up.servePack()
	code, out := e.startCreate(packCreate)
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded {
		t.Fatalf("create failed: %+v", op)
	}
	e.waitFor("online", func() bool { return e.status().Phase == api.PhaseOnline })
	for u, want := range p.mods {
		name := filepath.Base(u)
		if got, err := os.ReadFile(filepath.Join(e.dataDir(), "mods", name)); err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(e.dataDir(), "config", "testpack.toml")); string(got) != "spawn = true\n" {
		t.Errorf("the pack's config: %q", got)
	}
	if op.Detail["packFiles"] != float64(2) || op.Detail["packFilesTotal"] != float64(2) {
		t.Errorf("progress: %v", op.Detail)
	}
	sc, _ := e.srv().serverConfig()
	if sc.Type != "vanilla" || sc.MinecraftVersion != "26.2" || sc.Modpack == nil || sc.Modpack.Pending || sc.Modpack.Name != "Waystones Pack" ||
		sc.Modpack.VersionNumber != "1.0.0" || sc.Modpack.Mods != 2 || sc.JarVerifiedAt == nil {
		t.Fatalf("config: %+v %+v", sc, sc.Modpack)
	}
	rec, err := e.srv().packRecord()
	if err != nil || rec == nil || len(rec.Files) != 3 || rec.Pack.ProjectID != fakePackID {
		t.Fatalf("record: %+v %v", rec, err)
	}
	if row, _ := e.srv().row(); row.Type != "vanilla" {
		t.Fatalf("the server's type: %q", row.Type)
	}
	if e.countEvents("modpack_installed") != 1 || e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'modpack.installed'`) != 1 {
		t.Fatal("the install is recorded")
	}

	// Starting again leaves the pack as it is.
	e.runOp("POST", "/restart")
	for _, u := range p.modURLs {
		if n := e.up.hitCount(u); n != 1 {
			t.Errorf("%s downloaded %d times", u, n)
		}
	}
	code, _ = e.call("POST", e.sp("/delete"), map[string]any{"actor": "admin", "confirm": "My server"})
	if code != 202 && code != 200 {
		t.Fatalf("delete: %d", code)
	}
	e.waitFor("the record to go", func() bool { return e.countRows(`SELECT COUNT(*) FROM modpacks`) == 0 })
}

// packProperties is the server.properties a pack ships: settings it may
// suggest, and ones about reaching the server, operators and the server list
// that Playkeeper never takes from a pack.
const packProperties = "#Minecraft server properties\nallow-flight=true\nspawn-protection=0\ngenerator-settings={\"biome\"\\:\"minecraft\\:plains\"}\nonline-mode=false\nop-permission-level=4\nmotd=Hacked\n"

// A pack's suggested settings reach server.properties when the pack is
// installed; everything else it ships there is left out.
func TestCreateFromModpackTakesItsSuggestedSettings(t *testing.T) {
	e := newAgentEnv(t)
	e.up.servePackWith(map[string][]byte{"overrides/server.properties": []byte(packProperties)})
	code, out := e.startCreate(packCreate)
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("create failed: %+v", op)
	}
	b, err := os.ReadFile(filepath.Join(e.dataDir(), "server.properties"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"allow-flight=true\n", "spawn-protection=0\n", "generator-settings={\"biome\"\\:\"minecraft\\:plains\"}\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("server.properties lacks %q:\n%s", want, got)
		}
	}
	for _, never := range []string{"online-mode", "op-permission-level", "Hacked"} {
		if strings.Contains(got, never) {
			t.Errorf("server.properties took %q from the pack:\n%s", never, got)
		}
	}
}

// A pack for an older Minecraft version runs on the Java that version was
// made for (the real-world check's AsguhoServer: Minecraft 1.21.4 with a
// Fabric Loader that can't read Java 25's classes). The preview says so
// before the server is created, and a pack for 26.2 stays on the default.
func TestAPackForAnOlderVersionRunsOnItsJava(t *testing.T) {
	e := newAgentEnv(t)
	e.up.serveMojangReleases(append([]struct{ id, released string }{}, append(fakeReleases, struct{ id, released string }{"1.21.4", "2024-12-03T10:12:57+00:00"})...))
	e.up.servePackFor("1.21.4", nil)
	java21, _, _ := minecraft.ImageFor(21)

	code, out := e.call("GET", "/v1/modpacks/modrinth/"+fakePackID+"/versions/"+fakePackVersion+"/preview", nil)
	if code != 200 || out["minecraftVersion"] != "1.21.4" || out["java"] != float64(21) {
		t.Fatalf("the preview says which Java the pack runs on: %d %v", code, out)
	}
	code, out = e.startCreate(packCreate)
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("create failed: %+v", op)
	}
	if img := e.fd.containerImage(e.cname()); img != java21 {
		t.Fatalf("the server runs in %q, want the Java 21 image %q", img, java21)
	}
	if sc, _ := e.srv().serverConfig(); sc.MinecraftVersion != "1.21.4" || sc.Image != java21 {
		t.Fatalf("the config records the image: %+v", sc)
	}
	if st := e.status(); st.PendingRestart {
		t.Fatal("the running server's definition is the one its version needs")
	}

	e2 := newAgentEnv(t)
	e2.up.servePack()
	_, out = e2.startCreate(packCreate)
	if op := e2.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("create on 26.2: %+v", op)
	}
	if img := e2.fd.containerImage(e2.cname()); img != minecraft.Image {
		t.Fatalf("a 26.2 server runs in %q, want the default %q", img, minecraft.Image)
	}
	if _, out := e2.call("GET", "/v1/modpacks/modrinth/"+fakePackID+"/versions/"+fakePackVersion+"/preview", nil); out["java"] != nil {
		t.Fatalf("a pack on the newest Java says nothing about it: %v", out)
	}
}

func TestCreateFromModpackTriesAgainOnTheNextStart(t *testing.T) {
	e := newAgentEnv(t)
	p := e.up.servePack()
	e.up.remove(p.modURLs[1])
	code, out := e.startCreate(packCreate)
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpFailed || op.Detail["notice"] == nil {
		t.Fatalf("a mod that can't be downloaded fails the create: %+v", op)
	}
	if _, err := os.Stat(filepath.Join(e.dataDir(), "mods")); !os.IsNotExist(err) {
		t.Fatalf("nothing of the pack is written until every file is checked: %v", err)
	}
	sc, _ := e.srv().serverConfig()
	if sc.Modpack == nil || !sc.Modpack.Pending {
		t.Fatalf("the pack is still to install: %+v", sc.Modpack)
	}
	if rec, _ := e.srv().packRecord(); rec != nil {
		t.Fatal("no record until the pack is in place")
	}

	e.up.serve(p.modURLs[1], p.mods[p.modURLs[1]])
	if op := e.runOp("POST", "/start"); op.Status != api.OpSucceeded {
		t.Fatalf("start: %+v", op)
	}
	sc, _ = e.srv().serverConfig()
	if rec, _ := e.srv().packRecord(); rec == nil || sc.Modpack.Pending {
		t.Fatalf("the start installed the pack: %+v", sc.Modpack)
	}
}

func TestCreateFromModpackRefusesAChangedFile(t *testing.T) {
	e := newAgentEnv(t)
	p := e.up.servePack()
	e.up.serve(p.modURLs[0], []byte("not the mod the pack lists"))
	_, out := e.startCreate(packCreate)
	op := e.waitOp(out["id"].(string))
	notice, _ := op.Detail["notice"].(map[string]any)
	if op.Status != api.OpFailed || notice == nil {
		t.Fatalf("a file that doesn't match its hash fails the create: %+v", op)
	}
	if _, err := os.Stat(filepath.Join(e.dataDir(), "mods", "waystones.jar")); !os.IsNotExist(err) {
		t.Fatal("the changed file was written")
	}
	if e.fd.containerCount(e.cname()) != 0 {
		t.Fatal("the server was started without its pack")
	}
}

func TestCreateFromModpackChecksTheRequest(t *testing.T) {
	e := newAgentEnv(t)
	e.up.servePack()
	for _, body := range []map[string]any{
		{"type": "fabric", "modpack": packCreate["modpack"]},
		{"versionId": "", "build": "0.17.2", "modpack": packCreate["modpack"]},
		{"versionId": "", "modpack": map[string]any{"source": "hangar", "projectId": fakePackID}},
		{"versionId": "", "modpack": map[string]any{"source": "modrinth", "projectId": "../x"}},
		{"versionId": "", "modpack": map[string]any{"source": "modrinth", "projectId": fakePackID, "versionId": "NOPE0001"}},
	} {
		if code, out := e.startCreate(body); code != 400 {
			t.Errorf("%v: %d %v", body, code, out)
		}
	}
	if n := e.countRows(`SELECT COUNT(*) FROM servers`); n != 0 {
		t.Fatalf("%d servers recorded", n)
	}
}
