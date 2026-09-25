package agent

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/nbt"
	"github.com/CIYAhq/playkeeper/internal/worldimport"
)

const importPlayer = "4566e69f-c907-48ee-8d71-d7ba5aa00d20"

// worldEntry is one file of an archive a test uploads.
type worldEntry struct{ name, body string }

func worldZip(t *testing.T, files []worldEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func nbtStrings(items ...string) nbt.List {
	l := nbt.List{Type: nbt.TagString}
	for _, s := range items {
		l.Items = append(l.Items, s)
	}
	return l
}

// legacyLevelData is the Data compound of a level.dat saved before
// Minecraft 26.1, with the fields Playkeeper reads.
func legacyLevelData(name, version string, dataVersion int32) nbt.Compound {
	return nbt.Compound{
		"LevelName":        name,
		"DataVersion":      dataVersion,
		"Version":          nbt.Compound{"Id": dataVersion, "Name": version, "Series": "main", "Snapshot": int8(0)},
		"version":          int32(19133),
		"GameType":         int32(0),
		"Difficulty":       int8(2),
		"hardcore":         int8(0),
		"initialized":      int8(1),
		"DataPacks":        nbt.Compound{"Enabled": nbtStrings("vanilla", "file/Graves.zip"), "Disabled": nbt.List{Type: nbt.TagEnd}},
		"ServerBrands":     nbtStrings("vanilla"),
		"WorldGenSettings": nbt.Compound{"seed": int64(-4172144997902289642), "generate_features": int8(1), "bonus_chest": int8(0), "dimensions": nbt.Compound{}},
		"SpawnX":           int32(40), "SpawnY": int32(64), "SpawnZ": int32(12),
	}
}

// levelDat gzips a level.dat the way Minecraft writes it.
func levelDat(t *testing.T, data nbt.Compound) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := nbt.Write(zw, "", nbt.Compound{"Data": data}); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

var importRegion = strings.Repeat("region-sector-", 64)

// singleplayerUpload is a world folder from a Minecraft 1.21.4 saves
// folder, zipped the way people upload it, and its level.dat.
func singleplayerUpload(t *testing.T) ([]byte, string) {
	t.Helper()
	data := legacyLevelData("Survival-2024", "1.21.4", 4189)
	data["Player"] = nbt.Compound{"UUID": []int32{0x4566e69f, -0x36f8b712, -0x728e2846, 0x5aa00d20}, "Health": float32(20), "Dimension": "minecraft:overworld"}
	level := levelDat(t, data)
	return worldZip(t, []worldEntry{
		{"Survival-2024/level.dat", level},
		{"Survival-2024/session.lock", "\u2603"},
		{"Survival-2024/region/r.0.0.mca", importRegion + "overworld"},
		{"Survival-2024/DIM-1/region/r.0.0.mca", importRegion + "nether"},
		{"Survival-2024/DIM1/region/r.0.0.mca", importRegion + "end"},
		{"Survival-2024/playerdata/" + importPlayer + ".dat", "player"},
		{"Survival-2024/datapacks/Graves.zip", "pack"},
	}), level
}

// paperServerUpload is a Paper 1.21.11 server's folder as a host's file
// manager zips it, with its own RCON password and plugins, and its level.dat.
func paperServerUpload(t *testing.T) ([]byte, string) {
	t.Helper()
	data := legacyLevelData("world", "1.21.11", 4671)
	data["ServerBrands"] = nbtStrings("Paper")
	data["Difficulty"] = int8(3)
	level := levelDat(t, data)
	return worldZip(t, []worldEntry{
		{"server.properties", "#Minecraft server properties\nlevel-name=world\nlevel-seed=12345\ndifficulty=hard\ngamemode=survival\nenable-rcon=true\nrcon.password=hunter2\nserver-port=25570\nmotd=Old host\n"},
		{"eula.txt", "eula=true\n"},
		{"paper-1.21.11-99.jar", "jar"},
		{"plugins/EssentialsX.jar", "plugin jar"},
		{"ops.json", `[{"uuid":"` + importPlayer + `","name":"Steve","level":4,"bypassesPlayerLimit":false}]`},
		{"whitelist.json", `[{"uuid":"` + importPlayer + `","name":"Steve"}]`},
		{"world/level.dat", level},
		{"world/session.lock", "lock"},
		{"world/uid.dat", "uid-overworld"},
		{"world/region/r.0.0.mca", importRegion + "overworld"},
		{"world/playerdata/" + importPlayer + ".dat", "player"},
		{"world_nether/level.dat", level},
		{"world_nether/uid.dat", "uid-nether"},
		{"world_nether/DIM-1/region/r.0.0.mca", importRegion + "nether"},
		{"world_the_end/level.dat", level},
		{"world_the_end/uid.dat", "uid-end"},
		{"world_the_end/DIM1/region/r.0.0.mca", importRegion + "end"},
	}), level
}

func importPath(imp, rest string) string { return "/v1/world-imports/" + imp + rest }

func (e *agentEnv) openImport(path string) string {
	e.t.Helper()
	code, out := e.call("POST", path, map[string]any{"actor": "admin"})
	if code != 201 {
		e.t.Fatalf("open upload: %d %v", code, out)
	}
	return out["id"].(string)
}

func (e *agentEnv) announce(imp, name string, size int) (int, map[string]any) {
	e.t.Helper()
	return e.call("POST", importPath(imp, "/files"), map[string]any{"name": name, "size": size, "actor": "admin"})
}

// sendBytes sends bytes of an upload's file n from offset. It never fails
// the test, so a goroutine can use it.
func sendBytes(url, imp string, n int, offset int64, body io.Reader) (int, map[string]any, error) {
	req, err := http.NewRequest("PUT", fmt.Sprintf("%s%s?offset=%d", url, importPath(imp, fmt.Sprintf("/files/%d", n)), offset), body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-Playkeeper-Actor", "admin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, nil
}

// uploadWorld uploads archive as name to an upload opened at path, and
// checks it.
func (e *agentEnv) uploadWorld(path, name string, archive []byte) string {
	e.t.Helper()
	imp := e.openImport(path)
	if code, out := e.announce(imp, name, len(archive)); code != 201 {
		e.t.Fatalf("announce %s: %d %v", name, code, out)
	}
	if code, out, err := sendBytes(e.ts.URL, imp, 0, 0, bytes.NewReader(archive)); err != nil || code != 200 {
		e.t.Fatalf("upload %s: %d %v %v", name, code, out, err)
	}
	if code, out := e.call("POST", importPath(imp, "/inspect"), map[string]any{"actor": "admin"}); code != 200 {
		e.t.Fatalf("check %s: %d %v", name, code, out)
	}
	return imp
}

func (e *agentEnv) importView(imp string) api.WorldImport {
	e.t.Helper()
	code, out := e.call("GET", importPath(imp, ""), nil)
	if code != 200 {
		e.t.Fatalf("upload %s: %d %v", imp, code, out)
	}
	return decodeAs[api.WorldImport](e.t, out)
}

func (e *agentEnv) importPreview(imp string, body map[string]any) api.WorldImportPreview {
	e.t.Helper()
	body["actor"] = "admin"
	code, out := e.call("POST", importPath(imp, "/preview"), body)
	if code != 200 {
		e.t.Fatalf("preview: %d %v", code, out)
	}
	return decodeAs[api.WorldImportPreview](e.t, out)
}

func decodeAs[T any](t *testing.T, m map[string]any) T {
	t.Helper()
	var v T
	b, _ := json.Marshal(m)
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func (e *agentEnv) auditHas(action, result, detail string) bool {
	e.t.Helper()
	list, err := e.a.listAudit(100)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, a := range list {
		if a.Action == action && a.Result == result && strings.Contains(a.Detail, detail) {
			return true
		}
	}
	return false
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// archiveHas reports whether a backup archive holds the data file rel.
func archiveHas(t *testing.T, path, rel string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name == "playkeeper-backup/data/"+rel {
			return true
		}
	}
}

type droppedConnection struct{}

func (droppedConnection) Read([]byte) (int, error) { return 0, errors.New("the connection dropped") }

type stalledConnection struct{ until chan struct{} }

func (s stalledConnection) Read([]byte) (int, error) {
	<-s.until
	return 0, errors.New("the connection dropped")
}

// An upload keeps what arrived when its connection drops and carries on
// from there, as the UI does: it asks where the upload stands and sends the
// rest. A connection that stalls instead holds the file only until the next
// request takes over.
func TestWorldUploadCarriesOnAfterTheConnectionDrops(t *testing.T) {
	e := newAgentEnv(t)
	archive := make([]byte, 1<<20)
	rand.Read(archive)
	sum := sha256.Sum256(archive)
	cut := 256 << 10

	// resume sends the rest from where the agent says the upload stands,
	// asking again when another request was still finishing.
	resume := func(t *testing.T, imp string) int64 {
		t.Helper()
		for try := 0; ; try++ {
			from := e.importView(imp).Files[0].Received
			code, out, err := sendBytes(e.ts.URL, imp, 0, from, bytes.NewReader(archive[from:]))
			if err == nil && code == 200 {
				return from
			}
			if err != nil || code != 409 || !strings.Contains(fmt.Sprint(out["error"]), "carries on from byte") || try == 50 {
				t.Fatalf("carrying on from byte %d: %d %v %v", from, code, out, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	complete := func(t *testing.T, imp string) {
		t.Helper()
		f := e.importView(imp).Files[0]
		if f.Received != int64(len(archive)) || f.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("the upload is not the file that was sent: %+v", f)
		}
		if got := sha256.Sum256([]byte(readFile(t, filepath.Join(e.cfg.StagingDir(), "import-"+imp, "uploads", "0.bin")))); got != sum {
			t.Fatal("the uploaded file on disk differs from the file that was sent")
		}
	}

	t.Run("dropped", func(t *testing.T) {
		imp := e.openImport("/v1/world-imports")
		if code, out := e.announce(imp, "Survival-2024.zip", len(archive)); code != 201 {
			t.Fatalf("announce: %d %v", code, out)
		}
		if _, _, err := sendBytes(e.ts.URL, imp, 0, 0, io.MultiReader(bytes.NewReader(archive[:cut]), droppedConnection{})); err == nil {
			t.Fatal("the upload whose connection dropped should fail")
		}
		from := resume(t, imp)
		if from <= 0 || from > int64(cut) {
			t.Fatalf("the upload carried on from byte %d; the connection dropped after %d", from, cut)
		}
		complete(t, imp)
		code, out, _ := sendBytes(e.ts.URL, imp, 0, 5, strings.NewReader("again"))
		if code != 409 || !strings.Contains(fmt.Sprint(out["error"]), fmt.Sprintf("carries on from byte %d", len(archive))) {
			t.Fatalf("sending from the wrong byte: %d %v", code, out)
		}
		if code, _, _ := sendBytes(e.ts.URL, imp, 0, int64(len(archive)), strings.NewReader("")); code != 200 {
			t.Fatalf("a finished upload sent again: %d", code)
		}
		if !e.auditHas("world_import.uploaded", "received", "sha256 "+hex.EncodeToString(sum[:])) {
			t.Fatal("the finished upload is not in the audit log with its checksum")
		}
	})

	t.Run("stalled", func(t *testing.T) {
		imp := e.openImport("/v1/world-imports")
		if code, out := e.announce(imp, "Survival-2024.zip", len(archive)); code != 201 {
			t.Fatalf("announce: %d %v", code, out)
		}
		stall := make(chan struct{})
		t.Cleanup(func() { close(stall) })
		url := e.ts.URL
		go sendBytes(url, imp, 0, 0, io.MultiReader(bytes.NewReader(archive[:cut]), stalledConnection{stall}))
		part := filepath.Join(e.cfg.StagingDir(), "import-"+imp, "uploads", "0.bin")
		e.waitFor("the stalled upload's bytes", func() bool {
			fi, err := os.Stat(part)
			return err == nil && fi.Size() >= int64(cut-8<<10)
		})
		from := resume(t, imp)
		if from < int64(cut-8<<10) || from > int64(cut) {
			t.Fatalf("the upload carried on from byte %d; the stalled connection had sent %d", from, cut)
		}
		complete(t, imp)
	})
}

// A new server starts from a singleplayer world. On the recommended version
// the world is upgraded, so the world as uploaded is kept as a verified
// backup before the first start; on the world's own version it isn't, and
// Paper's separate Nether and End folders are made. The settings the world
// carries reach the server either way.
func TestServerStartsFromASingleplayerWorld(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(map[bool]string{false: "upgraded", true: "own version"}[keep], func(t *testing.T) {
			e := newAgentEnv(t)
			e.fill.set("", append(defaultFill(), fillVersionSpec{"1.21.4", "UNSUPPORTED", []fillBuildSpec{{232, "STABLE"}}}))
			archive, level := singleplayerUpload(t)
			imp := e.uploadWorld("/v1/world-imports", "Survival-2024.zip", archive)
			in := e.importView(imp).Inspection
			if in == nil || len(in.Worlds) != 1 || in.Worlds[0].Origin != worldimport.OriginSingleplayer {
				t.Fatalf("inspection: %+v", in)
			}

			pv := e.importPreview(imp, map[string]any{})
			if len(pv.Versions) != 2 || pv.Versions[0].ID != "paper-26.2" || pv.Versions[0].Keep || pv.Versions[1].ID != "paper-1.21.4" || !pv.Versions[1].Keep {
				t.Fatalf("a 1.21.4 world is offered the recommended version, then its own: %+v", pv.Versions)
			}
			if pv.VersionID != "paper-26.2" || !pv.KeepsOriginal || pv.MemoryMB == 0 || pv.Preview.Version.Compat != worldimport.CompatUpgrade || !slices.Equal(pv.Preview.Folders, []string{"world"}) {
				t.Fatalf("preview on the recommended version: %+v %+v", pv, pv.Preview.Version)
			}
			version, nether := "26.2", filepath.Join("world", "DIM-1", "region", "r.0.0.mca")
			if keep {
				pv = e.importPreview(imp, map[string]any{"versionId": "paper-1.21.4"})
				if pv.VersionID != "paper-1.21.4" || pv.KeepsOriginal || pv.Preview.Version.Compat != worldimport.CompatSame || !slices.Contains(pv.Preview.Folders, "world_nether") {
					t.Fatalf("preview on the world's own version: %+v %+v", pv, pv.Preview.Folders)
				}
				version, nether = "1.21.4", filepath.Join("world_nether", "DIM-1", "region", "r.0.0.mca")
			}

			code, out := e.call("POST", importPath(imp, "/create"), map[string]any{"versionId": "paper-" + version, "name": "Survival", "memoryMB": 1536, "acceptEula": true, "actor": "admin"})
			if code != 202 {
				t.Fatalf("create: %d %v", code, out)
			}
			e.sid = out["serverId"].(string)
			op := e.waitOp(out["id"].(string))
			if op.Status != api.OpSucceeded {
				t.Fatalf("creating the server from the world: %+v", op)
			}
			e.waitFor("online", func() bool { return e.status().Phase == api.PhaseOnline })

			if readFile(t, filepath.Join(e.dataDir(), "world", "level.dat")) != level {
				t.Fatal("the server runs another world than the uploaded one")
			}
			if !strings.Contains(readFile(t, filepath.Join(e.dataDir(), nether)), "nether") {
				t.Fatalf("the Nether is not at %s", nether)
			}
			if props := readFile(t, filepath.Join(e.dataDir(), "server.properties")); !strings.Contains(props, "difficulty=normal") {
				t.Fatalf("the world's difficulty is not in server.properties:\n%s", props)
			}
			sc, err := e.srv().serverConfig()
			if err != nil {
				t.Fatal(err)
			}
			if sc.MinecraftVersion != version || sc.Gameplay.Difficulty != "normal" || sc.Gameplay.GameMode != "survival" || sc.Gameplay.Hardcore == nil || *sc.Gameplay.Hardcore {
				t.Fatalf("server settings: %+v", sc)
			}
			e.fd.mu.Lock()
			difficulty := env(e.fd.byName[e.cname()].cfg, "DIFFICULTY")
			e.fd.mu.Unlock()
			if difficulty != "normal" {
				t.Fatalf("the container runs difficulty %q; the world carries normal", difficulty)
			}

			originals, _ := e.srv().listBackups(`kind = 'rollback'`)
			if keep {
				if len(originals) != 0 || op.Detail["originalBackupId"] != nil {
					t.Fatalf("a world that isn't upgraded needs no copy: %+v", originals)
				}
			} else {
				if len(originals) != 1 || originals[0].MinecraftVersion != "1.21.4" || originals[0].Verified == nil || !*originals[0].Verified || !strings.Contains(originals[0].Note, "as uploaded") || op.Detail["originalBackupId"] != originals[0].ID {
					t.Fatalf("the world as uploaded must be a verified backup of 1.21.4: %+v %v", originals, op.Detail)
				}
				if !archiveHas(t, filepath.Join(e.cfg.BackupsDir(), originals[0].FileName), "world/level.dat") {
					t.Fatal("the backup of the world as uploaded holds no level.dat")
				}
			}
			if e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'world_imported'`) != 1 || !e.auditHas("world_import.applied", "succeeded", "Survival-2024.zip") {
				t.Fatal("the import is not in the history and the audit log")
			}
			if code, out := e.call("GET", importPath(imp, ""), nil); code != 404 || exists(filepath.Join(e.cfg.StagingDir(), "import-"+imp)) {
				t.Fatalf("the finished upload must be gone: %d %v", code, out)
			}
		})
	}
}

// Importing into a server works like a restore: the upload is unpacked
// while the server runs, a verified rollback archive of the current world
// is saved, and only the world folders are swapped, so the server keeps its
// own settings, RCON password, player lists and plugins.
func TestImportReplacesAServersWorldLikeARestore(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	live := e.dataDir()
	write := func(rel, body string) {
		t.Helper()
		os.MkdirAll(filepath.Dir(filepath.Join(live, rel)), 0o750)
		if err := os.WriteFile(filepath.Join(live, rel), []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	write("world/marker.txt", "nonce-before")
	write("server.properties", "level-name=world\nrcon.password=live-secret\nmotd=Live server\ndifficulty=easy\n")
	write("whitelist.json", `[{"uuid":"`+importPlayer+`","name":"Alex"}]`)
	write("plugins/Live.jar", "live plugin")
	archive, level := paperServerUpload(t)
	imp := e.uploadWorld(e.sp("/world-imports"), "paper-server.zip", archive)

	pv := e.importPreview(imp, map[string]any{})
	if pv.ServerID != e.sid || pv.CurrentWorld == nil || !pv.CurrentWorld.Exists || !pv.WillCreateRollback || pv.ConfirmPhrase != "replace world" || len(pv.Steps) != 6 || len(pv.Versions) != 0 {
		t.Fatalf("preview of replacing the world: %+v", pv)
	}
	code, out := e.call("POST", importPath(imp, "/apply"), map[string]any{"confirm": "replace", "actor": "admin"})
	if code != 400 || !strings.Contains(fmt.Sprint(out["error"]), `Type "replace world"`) || !exists(filepath.Join(live, "world", "marker.txt")) {
		t.Fatalf("an import without its confirmation phrase: %d %v", code, out)
	}
	code, out = e.call("POST", importPath(imp, "/apply"), map[string]any{"confirm": "replace world", "actor": "admin"})
	if code != 202 {
		t.Fatalf("apply: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded {
		t.Fatalf("the import failed: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)

	if exists(filepath.Join(live, "world", "marker.txt")) || readFile(t, filepath.Join(live, "world", "level.dat")) != level {
		t.Fatal("the imported world did not replace the previous one")
	}
	for _, folder := range pv.Preview.Folders {
		if !exists(filepath.Join(live, folder)) {
			t.Fatalf("the imported folder %s is missing", folder)
		}
	}
	props := readFile(t, filepath.Join(live, "server.properties"))
	for _, want := range []string{"rcon.password=live-secret", "motd=Live server", "difficulty=hard"} {
		if !strings.Contains(props, want) {
			t.Fatalf("server.properties lacks %q:\n%s", want, props)
		}
	}
	if strings.Contains(props, "hunter2") || strings.Contains(props, "25570") || strings.Contains(props, "Old host") {
		t.Fatalf("the old host's settings came along:\n%s", props)
	}
	if !strings.Contains(readFile(t, filepath.Join(live, "whitelist.json")), "Alex") || !exists(filepath.Join(live, "plugins", "Live.jar")) ||
		exists(filepath.Join(live, "plugins", "EssentialsX.jar")) || exists(filepath.Join(live, "paper-1.21.11-99.jar")) || exists(filepath.Join(live, "ops.json")) {
		t.Fatal("the server must keep its own player lists and plugins, and take no server files the owner didn't ask for")
	}
	sc, _ := e.srv().serverConfig()
	if sc.Gameplay.Difficulty != "hard" || sc.MinecraftVersion != "26.1.2" {
		t.Fatalf("server settings after the import: %+v", sc)
	}
	rollbacks, _ := e.srv().listBackups(`kind = 'rollback'`)
	if len(rollbacks) != 1 || rollbacks[0].Verified == nil || !*rollbacks[0].Verified || op.Detail["rollbackBackupId"] != rollbacks[0].ID || !strings.Contains(rollbacks[0].Note, "before importing") {
		t.Fatalf("the import needs a verified rollback archive: %+v %v", rollbacks, op.Detail)
	}
	if !archiveHas(t, filepath.Join(e.cfg.BackupsDir(), rollbacks[0].FileName), "world/marker.txt") {
		t.Fatal("the rollback archive doesn't hold the previous world")
	}
	if left, _ := filepath.Glob(live + ".*-import-*"); len(left) != 0 {
		t.Fatalf("the swap left copies behind: %v", left)
	}
	if !e.auditHas("world_import.applied", "refused", "confirmation") || !e.auditHas("world_import.applied", "succeeded", "rollback archive "+rollbacks[0].ID) {
		t.Fatal("the refused and the finished import must be in the audit log")
	}
	if code, _ := e.call("GET", importPath(imp, ""), nil); code != 404 {
		t.Fatal("the finished upload must be gone")
	}
}

// An imported world that fails to start is swapped back out: the previous
// world runs again, and the imported one is kept aside for inspection.
func TestImportedWorldThatFailsToStartIsSwappedBack(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	live := e.dataDir()
	if err := os.WriteFile(filepath.Join(live, "world", "marker.txt"), []byte("nonce-before"), 0o640); err != nil {
		t.Fatal(err)
	}
	before := worldHash(t, live)
	archive, level := paperServerUpload(t)
	imp := e.uploadWorld(e.sp("/world-imports"), "paper-server.zip", archive)
	pv := e.importPreview(imp, map[string]any{})

	e.fd.mu.Lock()
	e.fd.startErr = "driver failed programming external connectivity on endpoint: Bind for 0.0.0.0:25565 failed: port is already allocated"
	e.fd.mu.Unlock()
	// The previous world starts again once the imported one is out of the way.
	renameDir = func(from, to string) error {
		if strings.Contains(to, ".failed-import-") {
			e.fd.mu.Lock()
			e.fd.startErr = ""
			e.fd.mu.Unlock()
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { renameDir = os.Rename })

	code, out := e.call("POST", importPath(imp, "/apply"), map[string]any{"confirm": pv.ConfirmPhrase, "actor": "admin"})
	if code != 202 {
		t.Fatalf("apply: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "Your previous world was put back and is running.") || !strings.Contains(op.Hint, live+".failed-import-") {
		t.Fatalf("a failed start must put the previous world back and say where the import is: %+v", op)
	}
	e.waitFor("the previous world online", e.onlineIdle)
	if worldHash(t, live) != before {
		t.Fatal("the previous world is not back")
	}
	failed, _ := filepath.Glob(live + ".failed-import-*")
	if len(failed) != 1 || readFile(t, filepath.Join(failed[0], "world", "level.dat")) != level {
		t.Fatalf("the imported world must be kept aside: %v", failed)
	}
	if left, _ := filepath.Glob(live + ".import-aside-*"); len(left) != 0 {
		t.Fatalf("the previous world's aside copy was left behind: %v", left)
	}
	if sc, _ := e.srv().serverConfig(); sc.Gameplay.Difficulty != "" {
		t.Fatalf("the imported world's settings stayed: %+v", sc.Gameplay)
	}
	if code, _ := e.call("GET", importPath(imp, ""), nil); code != 200 {
		t.Fatal("the upload must stay for another try")
	}
}

func TestWorldImportRefusals(t *testing.T) {
	e := newAgentEnv(t)
	errOf := func(out map[string]any) string { return fmt.Sprint(out["error"]) }
	imp := e.openImport("/v1/world-imports")

	for name, want := range map[string]string{
		"saves/world.zip":  "can't use the file name",
		"My World.mcworld": "Bedrock Edition world",
		"world.rar":        "RAR",
	} {
		if code, out := e.announce(imp, name, 100); code != 422 || !strings.Contains(errOf(out), want) {
			t.Errorf("announcing %q: %d %v", name, code, out)
		}
	}
	if code, out := e.announce(imp, "world.zip", 0); code != 400 {
		t.Errorf("an empty file: %d %v", code, out)
	}
	e.diskFree.Store(2 << 30)
	if code, out := e.announce(imp, "world.zip", 1<<30); code != 413 || out["code"] != api.CodeInsufficientSpace {
		t.Errorf("a file larger than the disk allows: %d %v", code, out)
	}
	e.diskFree.Store(0)

	if code, out := e.announce(imp, "world.zip", 100); code != 201 {
		t.Fatalf("announce: %d %v", code, out)
	}
	req, _ := http.NewRequest("PUT", e.ts.URL+importPath(imp, "/files/0?offset=0"), strings.NewReader("x"))
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 400 {
		t.Errorf("bytes without an actor: %v %v", resp, err)
	}
	if code, out, _ := sendBytes(e.ts.URL, imp, 1, 0, strings.NewReader("x")); code != 404 {
		t.Errorf("bytes for a file that wasn't announced: %d %v", code, out)
	}
	if code, out, _ := sendBytes(e.ts.URL, imp, 0, 0, strings.NewReader(strings.Repeat("x", 50))); code != 200 || e.importView(imp).Files[0].Received != 50 {
		t.Fatalf("half of the file: %d %v", code, out)
	}
	if code, out := e.call("POST", importPath(imp, "/inspect"), map[string]any{"actor": "admin"}); code != 409 || !strings.Contains(errOf(out), "hasn't finished uploading") {
		t.Errorf("checking a world that is still uploading: %d %v", code, out)
	}
	if code, out, _ := sendBytes(e.ts.URL, imp, 0, 50, strings.NewReader(strings.Repeat("x", 51))); code != 400 || !strings.Contains(errOf(out), "larger than announced") || e.importView(imp).Files[0].Received != 0 {
		t.Errorf("more bytes than announced: %d %v", code, out)
	}
	if code, out := e.call("POST", importPath(imp, "/preview"), map[string]any{"actor": "admin"}); code != 409 || !strings.Contains(errOf(out), "Check the world first") {
		t.Errorf("a preview before the check: %d %v", code, out)
	}
	if code, _ := e.call("DELETE", importPath(imp, ""), nil); code != 400 {
		t.Errorf("cancelling without an actor: %d", code)
	}
	if code, _ := e.call("DELETE", importPath(imp, "?actor=admin"), nil); code != 204 || exists(filepath.Join(e.cfg.StagingDir(), "import-"+imp)) {
		t.Errorf("cancelling: %d", code)
	}
	if code, out := e.call("GET", importPath(imp, ""), nil); code != 404 || !strings.Contains(errOf(out), "isn't here anymore") {
		t.Errorf("a cancelled upload: %d %v", code, out)
	}
	if code, _ := e.call("GET", importPath("NOT-AN-ID", ""), nil); code != 400 {
		t.Errorf("an invalid upload id: %d", code)
	}

	// A new server's upload can't replace a world, nor start without the EULA.
	archive, _ := singleplayerUpload(t)
	imp = e.uploadWorld("/v1/world-imports", "Survival-2024.zip", archive)
	if code, out := e.call("POST", importPath(imp, "/apply"), map[string]any{"confirm": "import", "actor": "admin"}); code != 409 || !strings.Contains(errOf(out), "for a new server") {
		t.Errorf("applying a new server's upload: %d %v", code, out)
	}
	if code, out := e.call("POST", importPath(imp, "/create"), map[string]any{"versionId": "paper-26.2", "memoryMB": 1536, "acceptEula": false, "actor": "admin"}); code != 400 || out["code"] != api.CodeEULARequired {
		t.Errorf("creating without the EULA: %d %v", code, out)
	}
	if code, out := e.call("POST", importPath(imp, "/create"), map[string]any{"versionId": "paper-26.1.2", "memoryMB": 1536, "acceptEula": true, "actor": "admin"}); code != 400 || !strings.Contains(errOf(out), "listed versions") {
		t.Errorf("creating on a version the preview didn't offer: %d %v", code, out)
	}

	// A world newer than any version Playkeeper can run is shown with its
	// problem and can't be imported.
	future := nbt.Compound{
		"LevelName": "Future", "DataVersion": int32(5023), "version": int32(19133), "GameType": int32(0), "initialized": int8(1),
		"Version":             nbt.Compound{"Id": int32(5023), "Name": "26.3", "Series": "main", "Snapshot": int8(0)},
		"difficulty_settings": nbt.Compound{"difficulty": "hard", "hardcore": int8(0), "locked": int8(0)},
		"DataPacks":           nbt.Compound{"Enabled": nbtStrings("vanilla"), "Disabled": nbt.List{Type: nbt.TagEnd}},
		"ServerBrands":        nbtStrings("vanilla"),
	}
	imp = e.uploadWorld("/v1/world-imports", "Future.zip", worldZip(t, []worldEntry{
		{"Future/level.dat", levelDat(t, future)},
		{"Future/dimensions/minecraft/overworld/region/r.0.0.mca", importRegion},
	}))
	if pv := e.importPreview(imp, map[string]any{}); pv.Preview.OK() || pv.Preview.Version.Compat != worldimport.CompatNewer {
		t.Errorf("a world from 26.3 on 26.2: %+v", pv.Preview)
	}
	if code, out := e.call("POST", importPath(imp, "/create"), map[string]any{"memoryMB": 1536, "acceptEula": true, "actor": "admin"}); code != 409 || !strings.Contains(errOf(out), "can't be imported") || !strings.Contains(errOf(out), "26.3") {
		t.Errorf("creating a server from a world too new: %d %v", code, out)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM servers`); n != 0 {
		t.Errorf("refused imports made %d servers", n)
	}

	// A server's upload can't make a new server.
	e.addIdleServer()
	imp = e.uploadWorld(e.sp("/world-imports"), "Survival-2024.zip", archive)
	if code, out := e.call("POST", importPath(imp, "/create"), map[string]any{"memoryMB": 1536, "acceptEula": true, "actor": "admin"}); code != 409 || !strings.Contains(errOf(out), "replaces a server's world") {
		t.Errorf("creating a server from a server's upload: %d %v", code, out)
	}
	if code, out := e.call("POST", "/v1/world-imports", map[string]any{"actor": "admin"}); code != 201 {
		t.Fatalf("open: %d %v", code, out)
	}
	if code, out := e.call("POST", "/v1/world-imports", map[string]any{"actor": "admin"}); code != 409 || !strings.Contains(errOf(out), "Too many world uploads") {
		t.Errorf("a fifth open upload: %d %v", code, out)
	}
}

// Uploads live in the staging folder, which the agent clears when it starts.
func TestAgentRestartForgetsWorldUploads(t *testing.T) {
	e := newAgentEnv(t)
	imp := e.openImport("/v1/world-imports")
	if code, out := e.announce(imp, "world.zip", 100); code != 201 {
		t.Fatalf("announce: %d %v", code, out)
	}
	e.stop()
	e.start()
	if code, _ := e.call("GET", importPath(imp, ""), nil); code != 404 || exists(filepath.Join(e.cfg.StagingDir(), "import-"+imp)) {
		t.Fatalf("an upload from before the restart: %d", code)
	}
}
