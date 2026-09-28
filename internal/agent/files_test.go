package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// fileRequest sends a request to the file browser as the dashboard does, the
// signed-in account as its actor, and returns the answer's status and body.
func (e *agentEnv) fileRequest(method, path string, body io.Reader) (int, []byte, http.Header) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("X-Playkeeper-Actor", "admin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

func (e *agentEnv) filesAt(p string) api.Files {
	e.t.Helper()
	code, out := e.call("GET", e.sp("/files?path="+url.QueryEscape(p)), nil)
	if code != 200 {
		e.t.Fatalf("list %q: %d %v", p, code, out)
	}
	return decodeAs[api.Files](e.t, out)
}

func (e *agentEnv) openFile(p string) api.FileContent {
	e.t.Helper()
	code, out := e.call("GET", e.sp("/files/content?path="+url.QueryEscape(p)), nil)
	if code != 200 {
		e.t.Fatalf("open %q: %d %v", p, code, out)
	}
	return decodeAs[api.FileContent](e.t, out)
}

// saveFile saves text over p as the editor does, expecting the version it
// opened ("" to save over whatever is there, "new" for a new file).
func (e *agentEnv) saveFile(p, expect, text string) (int, map[string]any) {
	e.t.Helper()
	return e.whenFree(func() (int, map[string]any) { return e.saveFileNow(p, expect, text) })
}

// saveFileNow is saveFile without asking again while the server is busy.
func (e *agentEnv) saveFileNow(p, expect, text string) (int, map[string]any) {
	e.t.Helper()
	code, b, _ := e.fileRequest("PUT", e.sp("/files/content?path="+url.QueryEscape(p)+"&expect="+expect), strings.NewReader(text))
	out := map[string]any{}
	json.Unmarshal(b, &out)
	return code, out
}

func (e *agentEnv) data(rel string) string {
	return filepath.Join(e.dataDir(), filepath.FromSlash(rel))
}

func (e *agentEnv) putData(rel, content string) {
	e.t.Helper()
	writeData(e.t, e.data(rel), content)
}

func writeData(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func entryNames(f api.Files) string {
	var out []string
	for _, e := range f.Entries {
		out = append(out, e.Name+":"+e.Type)
	}
	return strings.Join(out, " ")
}

func codeOf(out map[string]any) string { s, _ := out["code"].(string); return s }

// waitIdle waits for the server's operation lock to be free, which an
// operation gives back a moment after it is recorded as ended.
func (e *agentEnv) waitIdle() {
	e.t.Helper()
	e.waitFor("the operation lock free", func() bool {
		release, ok := e.srv().holdOpLock()
		if ok {
			release()
		}
		return ok
	})
}

// idleFilesServer is a stopped server with a few files, and a canary
// outside its folder: Playkeeper's own files, which nothing may reach.
func idleFilesServer(t *testing.T) (*agentEnv, string) {
	t.Helper()
	e := newAgentEnv(t)
	e.addIdleServer()
	e.putData("server.properties", "motd=A Playkeeper server\nspawn-protection=16\n")
	e.putData("plugins/Essentials/config.yml", "ops-name-color: '4'\n")
	e.putData("plugins/EssentialsX.jar", "PK\x03\x04 a plugin")
	e.putData("world/level.dat", "\x0a\x00\x00level")
	canary := filepath.Join(e.cfg.DataDir, "canary.txt")
	writeData(t, canary, "Playkeeper's own file\n")
	return e, canary
}

func TestFileBrowserListsOpensAndSavesTheServersFiles(t *testing.T) {
	e, _ := idleFilesServer(t)
	top := e.filesAt("")
	if got := entryNames(top); got != "plugins:folder server.properties:file world:folder" || top.Path != "" || top.Running || !slices.Equal(top.Worlds, []string{"world", "world_nether", "world_the_end"}) {
		t.Fatalf("the server's folder: %s %+v", got, top)
	}
	if got := entryNames(e.filesAt("plugins")); got != "Essentials:folder EssentialsX.jar:file" {
		t.Fatalf("plugins: %s", got)
	}
	if code, out := e.call("GET", e.sp("/files?path=missing"), nil); code != 404 || codeOf(out) != api.CodeNotFound {
		t.Fatalf("a missing folder: %d %v", code, out)
	}

	c := e.openFile("plugins/Essentials/config.yml")
	if c.Text != "ops-name-color: '4'\n" || c.Binary || c.ReadOnly != "" || c.Size != 20 || len(c.Version) != 64 {
		t.Fatalf("config.yml: %+v", c)
	}
	if code, out := e.saveFile("plugins/Essentials/config.yml", c.Version, "ops-name-color: 'c'\n"); code != 200 || out["version"] == c.Version {
		t.Fatalf("save: %d %v", code, out)
	}
	if got := readFile(t, e.data("plugins/Essentials/config.yml")); got != "ops-name-color: 'c'\n" {
		t.Fatalf("saved %q", got)
	}
	// A plugin wrote its settings since the editor opened them.
	e.putData("plugins/Essentials/config.yml", "rewritten by the plugin\n")
	if code, out := e.saveFile("plugins/Essentials/config.yml", c.Version, "mine\n"); code != 409 || codeOf(out) != api.CodeFileChanged {
		t.Fatalf("a save over a changed file: %d %v", code, out)
	}
	if got := readFile(t, e.data("plugins/Essentials/config.yml")); got != "rewritten by the plugin\n" {
		t.Fatalf("a refused save changed the file: %q", got)
	}
	if code, out := e.saveFile("plugins/Essentials/config.yml", "", "mine\n"); code != 200 {
		t.Fatalf("saving over it anyway: %d %v", code, out)
	}
	os.Remove(e.data("plugins/Essentials/config.yml"))
	if code, out := e.saveFile("plugins/Essentials/config.yml", c.Version, "mine\n"); code != 409 || codeOf(out) != api.CodeFileChanged || out["params"].(map[string]any)["gone"] != true {
		t.Fatalf("a save over a deleted file: %d %v", code, out)
	}

	if code, out := e.saveFile("plugins/Essentials/messages.yml", "new", ""); code != 200 {
		t.Fatalf("a new file: %d %v", code, out)
	}
	if code, out := e.saveFile("plugins/Essentials/messages.yml", "new", "x"); code != 409 || codeOf(out) != "exists" {
		t.Fatalf("a new file over one: %d %v", code, out)
	}
	if code, out := e.saveFile("server.properties", "not-a-version", "x"); code != 400 {
		t.Fatalf("a bad expect: %d %v", code, out)
	}
	if code, out := e.saveFile("server.properties", "", "motd=\x00\n"); code != 400 {
		t.Fatalf("binary in the editor: %d %v", code, out)
	}
	if code, out := e.saveFile("server.properties", "", string([]byte{0xff, 0xfe})); code != 400 {
		t.Fatalf("invalid UTF-8 in the editor: %d %v", code, out)
	}
	if code, out := e.saveFile("server.properties", "", strings.Repeat("#", maxEditBytes+1)); code != 413 || codeOf(out) != "too_large" {
		t.Fatalf("a file too large to save: %d %v", code, out)
	}

	lvl := e.openFile("world/level.dat")
	if !lvl.Binary || lvl.Text != "" {
		t.Fatalf("a binary file: %+v", lvl)
	}
	e.putData("logs/latest.log", strings.Repeat("a log line\n", maxEditBytes/10))
	if code, out := e.call("GET", e.sp("/files/content?path=logs/latest.log"), nil); code != 409 || codeOf(out) != "too_large" {
		t.Fatalf("a file too large to open: %d %v", code, out)
	}
	if code, out := e.call("GET", e.sp("/files/content?path=plugins"), nil); code != 409 || codeOf(out) != "not_a_file" {
		t.Fatalf("opening a folder: %d %v", code, out)
	}
}

// server.properties says which of its keys Playkeeper sets from the
// server's settings at each start, so the editor can say where to change
// them.
func TestServerPropertiesNamesTheKeysPlaykeeperSets(t *testing.T) {
	e, _ := idleFilesServer(t)
	c := e.openFile("server.properties")
	for _, k := range []string{"motd", "max-players", "online-mode", "white-list", "enforce-whitelist", "enable-rcon", "rcon.password", "level-name", "server-port"} {
		if !slices.Contains(c.Managed, k) {
			t.Errorf("managed = %v, without %s", c.Managed, k)
		}
	}
	if slices.Contains(c.Managed, "spawn-protection") {
		t.Errorf("managed = %v, with a key Playkeeper leaves alone", c.Managed)
	}
	if other := e.openFile("plugins/Essentials/config.yml"); other.Managed != nil {
		t.Errorf("another file names managed keys: %v", other.Managed)
	}
}

// Negative control for confinement: no path from the dashboard reaches
// outside the server's folder, whether it climbs out, starts at the top or
// names another server's files, on any route.
func TestFileBrowserPathsStayInsideTheServersFolder(t *testing.T) {
	e, canary := idleFilesServer(t)
	own := e.sid
	other := e.addIdleServer()
	e.sid = own
	otherFile := filepath.Join(e.cfg.DataDir, "servers", other, "data", "server.properties")
	writeData(t, otherFile, "motd=Someone else's\n")
	before := map[string]string{canary: readFile(t, canary), otherFile: readFile(t, otherFile)}
	upload := e.openUpload("plugins")
	bad := []string{"../canary.txt", "../../canary.txt", "/etc/passwd", "plugins/../../../canary.txt", "..", "plugins/", "plugins//config.yml", "./server.properties",
		"../../" + other + "/data/server.properties", "server.properties\x00.txt", strings.Repeat("a/", 2100) + "b"}
	for _, p := range bad {
		q := url.QueryEscape(p)
		for _, c := range []struct {
			what string
			run  func() int
		}{
			{"list", func() int { code, _ := e.call("GET", e.sp("/files?path="+q), nil); return code }},
			{"open", func() int { code, _ := e.call("GET", e.sp("/files/content?path="+q), nil); return code }},
			{"save", func() int { code, _ := e.saveFile(p, "", "overwritten\n"); return code }},
			{"make a folder", func() int {
				code, _ := e.call("POST", e.sp("/files/folder"), map[string]any{"actor": "admin", "path": p})
				return code
			}},
			{"move from", func() int {
				code, _ := e.call("POST", e.sp("/files/move"), map[string]any{"actor": "admin", "items": []any{map[string]any{"from": p, "to": "moved"}}})
				return code
			}},
			{"move to", func() int {
				code, _ := e.call("POST", e.sp("/files/move"), map[string]any{"actor": "admin", "items": []any{map[string]any{"from": "server.properties", "to": p}}})
				return code
			}},
			{"delete", func() int {
				code, _ := e.call("POST", e.sp("/files/delete"), map[string]any{"actor": "admin", "paths": []string{p}})
				return code
			}},
			{"download", func() int { code, _, _ := e.fileRequest("GET", e.sp("/files/download?path="+q), nil); return code }},
			{"upload into", func() int {
				code, _ := e.call("POST", e.sp("/files/uploads"), map[string]any{"actor": "admin", "folder": p})
				return code
			}},
			{"upload as", func() int { code, _ := e.announceFile(upload, p, 3, true); return code }},
		} {
			if code := c.run(); code != 400 {
				t.Errorf("%s %q: %d, want 400", c.what, p, code)
			}
		}
	}
	for p, was := range before {
		if got := readFile(t, p); got != was {
			t.Errorf("%s changed: %q", p, got)
		}
	}
	for _, p := range []string{e.data("moved"), filepath.Join(e.cfg.DataDir, "servers", own, "moved")} {
		if exists(p) {
			t.Errorf("%s appeared", p)
		}
	}
	if !exists(e.data("server.properties")) {
		t.Error("server.properties was moved")
	}
}

// Negative control for confinement: a link a plugin put in the server's
// folder is listed as a link and never followed. Reading, saving, zipping,
// listing or uploading through it is refused; deleting or moving it acts on
// the link, never on what it leads to.
func TestFileBrowserNeverFollowsLinksOutOfTheServer(t *testing.T) {
	e, canary := idleFilesServer(t)
	own := e.sid
	other := e.addIdleServer()
	e.sid = own
	otherData := filepath.Join(e.cfg.DataDir, "servers", other, "data")
	writeData(t, filepath.Join(otherData, "server.properties"), "motd=Someone else's\n")
	for rel, target := range map[string]string{
		"evil.yml":                    canary,
		"logs":                        otherData,
		"inside":                      "../../" + other + "/data",
		"plugins/escape":              "/",
		"plugins/Essentials/userdata": filepath.Dir(canary),
	} {
		if err := os.Symlink(target, e.data(rel)); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func() map[string]string {
		out := map[string]string{}
		filepath.WalkDir(e.cfg.DataDir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() && !strings.HasPrefix(p, e.dataDir()) && !strings.Contains(p, "agent") {
				out[p] = readFile(t, p)
			}
			return nil
		})
		return out
	}
	before := snapshot()

	top := e.filesAt("")
	if got := entryNames(top); !strings.Contains(got, "evil.yml:link") || !strings.Contains(got, "logs:link") || !strings.Contains(got, "inside:link") {
		t.Fatalf("links aren't listed as links: %s", got)
	}
	for _, c := range []struct {
		what, method, path string
		body               any
	}{
		{"list through a link", "GET", "/files?path=logs", nil},
		{"list inside a link", "GET", "/files?path=inside", nil},
		{"open a link", "GET", "/files/content?path=evil.yml", nil},
		{"open through a link", "GET", "/files/content?path=logs/server.properties", nil},
		{"open through a relative link", "GET", "/files/content?path=inside/server.properties", nil},
		{"make a folder through a link", "POST", "/files/folder", map[string]any{"actor": "admin", "path": "logs/new"}},
		{"move into a link", "POST", "/files/move", map[string]any{"actor": "admin", "items": []any{map[string]any{"from": "server.properties", "to": "logs/server.properties"}}}},
		{"delete through a link", "POST", "/files/delete", map[string]any{"actor": "admin", "paths": []string{"logs/server.properties"}}},
	} {
		code, out := e.call(c.method, e.sp(c.path), c.body)
		if code != 409 || codeOf(out) != "link" {
			t.Errorf("%s: %d %v, want 409 link", c.what, code, out)
		}
	}
	for _, p := range []string{"evil.yml", "logs/server.properties", "inside/server.properties"} {
		if code, out := e.saveFile(p, "", "overwritten\n"); code != 409 || codeOf(out) != "link" {
			t.Errorf("save %s: %d %v", p, code, out)
		}
		if code, _, _ := e.fileRequest("GET", e.sp("/files/download?path="+url.QueryEscape(p)), nil); code != 409 {
			t.Errorf("download %s: %d", p, code)
		}
	}
	code, out := e.call("POST", e.sp("/files/uploads"), map[string]any{"actor": "admin", "folder": "logs"})
	if code != 201 {
		t.Fatalf("open an upload: %d %v", code, out)
	}
	if code, out := e.call("POST", e.sp("/files/uploads/"+out["id"].(string)+"/files"), map[string]any{"actor": "admin", "name": "server.properties", "size": 3, "replace": true}); code != 409 || codeOf(out) != "link" {
		t.Errorf("upload through a link: %d %v", code, out)
	}

	// A zip of a folder with links in it leaves them out.
	code, zipped, _ := e.fileRequest("GET", e.sp("/files/download?path=plugins"), nil)
	if code != 200 {
		t.Fatalf("download plugins: %d %s", code, zipped)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil {
		t.Fatal(err)
	}
	var entries []string
	for _, f := range zr.File {
		entries = append(entries, f.Name)
	}
	if got := strings.Join(entries, " "); got != "plugins/ plugins/Essentials/ plugins/Essentials/config.yml plugins/EssentialsX.jar" {
		t.Fatalf("the zip of plugins holds %s", got)
	}

	// Moving or deleting a link acts on the link.
	if code, out := e.call("POST", e.sp("/files/move"), map[string]any{"actor": "admin", "items": []any{map[string]any{"from": "inside", "to": "put-aside"}}}); code != 200 {
		t.Fatalf("move a link: %d %v", code, out)
	}
	if code, out := e.call("POST", e.sp("/files/delete"), map[string]any{"actor": "admin", "paths": []string{"evil.yml", "logs", "put-aside", "plugins/escape"}}); code != 200 {
		t.Fatalf("delete links: %d %v", code, out)
	}
	for _, rel := range []string{"evil.yml", "logs", "put-aside", "plugins/escape"} {
		if exists(e.data(rel)) {
			t.Errorf("the link %s is still there", rel)
		}
	}
	if code, out := e.call("POST", e.sp("/files/delete"), map[string]any{"actor": "admin", "paths": []string{"plugins/Essentials"}}); code != 200 {
		t.Fatalf("delete a folder with a link in it: %d %v", code, out)
	}
	after := snapshot()
	for p, was := range before {
		if after[p] != was {
			t.Errorf("%s changed or went: %q", p, after[p])
		}
	}
	if !exists(canary) || !exists(filepath.Join(otherData, "server.properties")) || !exists("/etc") {
		t.Fatal("what a link led to was deleted")
	}
}

// While the game runs it writes its world, so the world folders are
// read-only; every other file can still change and applies on restart.
func TestTheWorldIsReadOnlyWhileTheGameRuns(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.putData("world/level.dat", "\x0a\x00\x00level")
	e.putData("world/region/r.0.0.mca", "region")
	if f := e.filesAt(""); !f.Running {
		t.Fatalf("a running server's folder: %+v", f)
	}
	if c := e.openFile("world/level.dat"); c.ReadOnly != api.CodeWorldInUse || !c.Running {
		t.Fatalf("a world file while the game runs: %+v", c)
	}
	refused := func(what string, code int, out map[string]any) {
		t.Helper()
		if code != 409 || codeOf(out) != api.CodeWorldInUse {
			t.Errorf("%s: %d %v, want 409 world_in_use", what, code, out)
		}
	}
	code, out := e.saveFile("world/level.dat", "", "x")
	refused("save", code, out)
	code, out = e.callWhenFree("POST", e.sp("/files/folder"), map[string]any{"actor": "admin", "path": "world/datapacks"})
	refused("make a folder", code, out)
	code, out = e.callWhenFree("POST", e.sp("/files/delete"), map[string]any{"actor": "admin", "paths": []string{"world"}})
	refused("delete the world", code, out)
	code, out = e.callWhenFree("POST", e.sp("/files/move"), map[string]any{"actor": "admin", "items": []any{map[string]any{"from": "world/region/r.0.0.mca", "to": "r.0.0.mca"}}})
	refused("move out of the world", code, out)
	e.putData("old_world/region/r.0.0.mca", "old")
	code, out = e.callWhenFree("POST", e.sp("/files/move"), map[string]any{"actor": "admin", "items": []any{map[string]any{"from": "old_world", "to": "world_nether"}}})
	refused("move into a world folder", code, out)
	// A world a plugin like Multiverse keeps beside the server's own.
	e.putData("survival_games/level.dat", "\x0a\x00\x00level")
	if f := e.filesAt(""); !slices.Contains(f.Worlds, "survival_games") || slices.Contains(f.Worlds, "old_world") {
		t.Fatalf("the worlds: %v", f.Worlds)
	}
	code, out = e.callWhenFree("POST", e.sp("/files/delete"), map[string]any{"actor": "admin", "paths": []string{"survival_games/level.dat"}})
	refused("delete in a plugin's world", code, out)
	code, out = e.call("POST", e.sp("/files/uploads"), map[string]any{"actor": "admin", "folder": "world"})
	if code != 201 {
		t.Fatalf("open an upload: %d %v", code, out)
	}
	code, out = e.call("POST", e.sp("/files/uploads/"+out["id"].(string)+"/files"), map[string]any{"actor": "admin", "name": "level.dat", "size": 3, "replace": true})
	refused("upload into the world", code, out)
	if readFile(t, e.data("world/level.dat")) != "\x0a\x00\x00level" || !exists(e.data("world/region/r.0.0.mca")) {
		t.Fatal("the world changed")
	}
	if code, out := e.saveFile("server.properties", "", "motd=Changed while running\n"); code != 200 {
		t.Fatalf("a file outside the world: %d %v", code, out)
	}

	if code, out := e.callWhenFree("POST", e.sp("/stop"), map[string]any{"actor": "admin"}); code != 202 {
		t.Fatalf("stop: %d %v", code, out)
	}
	e.waitFor("stopped", func() bool { return !e.srv().gameRunning(t.Context()) && !e.a.busy() })
	e.waitIdle()
	if code, out := e.saveFile("world/level.dat", "", "edited\n"); code != 200 {
		t.Fatalf("the world once the game stopped: %d %v", code, out)
	}
}

// A backup, a restore or any other operation has the server's files to
// itself: the file browser still lists and opens them, and changes nothing.
func TestFileBrowserWaitsForTheServersOperation(t *testing.T) {
	e, _ := idleFilesServer(t)
	release := e.holdOp("backup")
	busy := func(what string, code int, out map[string]any) {
		t.Helper()
		if code != 409 || codeOf(out) != api.CodeBusy {
			t.Errorf("%s during a backup: %d %v", what, code, out)
		}
	}
	code, out := e.saveFileNow("server.properties", "", "motd=x\n")
	busy("save", code, out)
	code, out = e.call("POST", e.sp("/files/folder"), map[string]any{"actor": "admin", "path": "config"})
	busy("make a folder", code, out)
	code, out = e.call("POST", e.sp("/files/delete"), map[string]any{"actor": "admin", "paths": []string{"plugins"}})
	busy("delete", code, out)
	code, out = e.announceFile(e.openUpload("config"), "paper-global.yml", 5, false)
	busy("announce an upload", code, out)
	e.filesAt("plugins")
	e.openFile("server.properties")
	release()
	e.waitIdle()
	if code, out := e.saveFile("server.properties", "", "motd=x\n"); code != 200 {
		t.Fatalf("save after the backup: %d %v", code, out)
	}
}

// holdChange makes the next change to reach path wait there until resume is
// called; held is closed once it waits.
func holdChange(t *testing.T, path string) (held <-chan struct{}, resume func()) {
	t.Helper()
	h, r := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	beforeChange = func(p string) {
		if p == path && first.CompareAndSwap(false, true) {
			close(h)
			<-r
		}
	}
	resume = sync.OnceFunc(func() { close(r) })
	t.Cleanup(func() {
		resume()
		beforeChange = func(string) {}
	})
	return h, resume
}

// A change to the server's files holds off operations until it is done, so
// that a start, such as a wake for a player who joins during a long delete,
// a backup or a restore can't begin in the middle of it. Changes don't hold
// off each other.
func TestFileChangesHoldOffOperations(t *testing.T) {
	for _, c := range []struct {
		what, path string
		run        func(e *agentEnv) (int, map[string]any)
	}{
		{"a save", "server.properties", func(e *agentEnv) (int, map[string]any) { return e.saveFile("server.properties", "", "motd=x\n") }},
		{"a new file", "config/new.yml", func(e *agentEnv) (int, map[string]any) { return e.saveFile("config/new.yml", "new", "a: 1\n") }},
		{"a new folder", "config", func(e *agentEnv) (int, map[string]any) {
			return e.call("POST", e.sp("/files/folder"), map[string]any{"actor": "admin", "path": "config"})
		}},
		{"a move", "plugins/EssentialsX.jar", func(e *agentEnv) (int, map[string]any) {
			return e.call("POST", e.sp("/files/move"), map[string]any{"actor": "admin", "items": []any{map[string]any{"from": "plugins/EssentialsX.jar", "to": "EssentialsX.jar"}}})
		}},
		{"a delete", "plugins/Essentials", func(e *agentEnv) (int, map[string]any) {
			return e.call("POST", e.sp("/files/delete"), map[string]any{"actor": "admin", "paths": []string{"plugins/Essentials"}})
		}},
		{"an upload put in place", "config/paper-global.yml", func(e *agentEnv) (int, map[string]any) {
			up := e.openUpload("config")
			if code, out := e.announceFile(up, "paper-global.yml", 5, false); code != 201 {
				return code, out
			}
			code, _, out := e.uploadPiece(up, 0, 0, strings.NewReader("x: 1\n"))
			return code, out
		}},
	} {
		t.Run(c.what, func(t *testing.T) {
			e, _ := idleFilesServer(t)
			s := e.srv()
			held, resume := holdChange(t, c.path)
			answered := make(chan string, 1)
			go func() {
				code, out := c.run(e)
				answered <- fmt.Sprint(code, " ", out)
			}()
			select {
			case <-held:
			case a := <-answered:
				t.Fatalf("%s didn't reach its change: %s", c.what, a)
			}
			noop := func(context.Context, *opHandle) error { return nil }
			if _, err := s.beginOp("backup", "admin", noop); err == nil || !strings.Contains(err.Error(), "busy with a change to its files") {
				t.Fatalf("a backup during %s: %v", c.what, err)
			}
			if code, out := e.call("POST", e.sp("/start"), map[string]any{"actor": "admin"}); code != 409 || codeOf(out) != api.CodeBusy {
				t.Fatalf("a start during %s: %d %v", c.what, code, out)
			}
			if code, out := e.saveFile("plugins/other.yml", "new", "b: 2\n"); code != 200 {
				t.Fatalf("another change during %s: %d %v", c.what, code, out)
			}
			resume()
			if a := <-answered; !strings.HasPrefix(a, "20") {
				t.Fatalf("%s: %s", c.what, a)
			}
			e.waitIdle()
			op, err := e.opWhenFree(func() (*api.Operation, error) { return s.beginOp("backup", "admin", noop) })
			if err != nil {
				t.Fatalf("a backup after %s: %v", c.what, err)
			}
			e.waitOp(op.ID)
		})
	}
}

// Deleting a folder of millions of files takes minutes, longer than the
// panel waits for an answer: the answer says the delete carries on, and it
// holds off operations until it is done.
func TestALongDeleteCarriesOnAfterItsAnswer(t *testing.T) {
	e, _ := idleFilesServer(t)
	orig := deleteAnswerAfter
	deleteAnswerAfter = 50 * time.Millisecond
	t.Cleanup(func() { deleteAnswerAfter = orig })
	_, resume := holdChange(t, "plugins/Essentials")
	code, out := e.call("POST", e.sp("/files/delete"), map[string]any{"actor": "admin", "paths": []string{"plugins/EssentialsX.jar", "plugins/Essentials"}})
	if code != 202 || out["continuing"] != true || out["deleted"] != float64(1) {
		t.Fatalf("a long delete: %d %v", code, out)
	}
	noop := func(context.Context, *opHandle) error { return nil }
	if _, err := e.srv().beginOp("backup", "admin", noop); err == nil {
		t.Fatal("a backup began while the delete carried on")
	}
	resume()
	e.waitFor("the delete to finish", func() bool { return !exists(e.data("plugins/Essentials")) && !e.srv().changingFiles() })
	if !e.auditHas("files.deleted", "succeeded", "plugins/EssentialsX.jar, plugins/Essentials") {
		t.Fatal("the delete wasn't audited once done")
	}
	e.waitIdle()
	if code, out := e.call("POST", e.sp("/files/delete"), map[string]any{"actor": "admin", "paths": []string{"server.properties"}}); code != 200 || out["continuing"] != nil || out["deleted"] != float64(1) {
		t.Fatalf("a quick delete: %d %v", code, out)
	}
}

// Of two saves of the same version, such as by two admins at once, the
// second is refused as changed, never lost.
func TestTwoSavesOfOneVersionCantBothWin(t *testing.T) {
	e, _ := idleFilesServer(t)
	v := e.openFile("server.properties").Version
	held, resume := holdChange(t, "server.properties")
	first := make(chan int, 1)
	go func() {
		code, _ := e.saveFile("server.properties", v, "motd=First\n")
		first <- code
	}()
	<-held
	second := make(chan map[string]any, 1)
	go func() {
		code, out := e.saveFile("server.properties", v, "motd=Second\n")
		out["status"] = code
		second <- out
	}()
	time.Sleep(50 * time.Millisecond)
	resume()
	if code := <-first; code != 200 {
		t.Fatalf("the first save: %d", code)
	}
	if out := <-second; out["status"] != 409 || codeOf(out) != api.CodeFileChanged {
		t.Fatalf("the second save: %v", out)
	}
	if got := readFile(t, e.data("server.properties")); got != "motd=First\n" {
		t.Fatalf("server.properties = %q", got)
	}
}

func TestFoldersMoveRenameAndDelete(t *testing.T) {
	e, _ := idleFilesServer(t)
	if code, out := e.call("POST", e.sp("/files/folder"), map[string]any{"actor": "admin", "path": "plugins/Essentials/backup"}); code != 201 || out["type"] != "folder" {
		t.Fatalf("make a folder: %d %v", code, out)
	}
	if code, out := e.call("POST", e.sp("/files/folder"), map[string]any{"actor": "admin", "path": "plugins/Essentials/backup"}); code != 409 || codeOf(out) != "exists" {
		t.Fatalf("make it again: %d %v", code, out)
	}
	if code, out := e.call("POST", e.sp("/files/folder"), map[string]any{"actor": "admin", "path": "plugins/new\nline"}); code != 400 {
		t.Fatalf("a name with a control character: %d %v", code, out)
	}
	if code, out := e.call("POST", e.sp("/files/move"), map[string]any{"actor": "admin", "items": []any{map[string]any{"from": "plugins/Essentials/config.yml", "to": "plugins/Essentials/settings.yml"}}}); code != 200 {
		t.Fatalf("rename: %d %v", code, out)
	}
	if code, out := e.call("POST", e.sp("/files/move"), map[string]any{"actor": "admin", "items": []any{
		map[string]any{"from": "plugins/Essentials/settings.yml", "to": "plugins/Essentials/backup/settings.yml"},
		map[string]any{"from": "plugins/EssentialsX.jar", "to": "plugins/Essentials/backup/EssentialsX.jar"},
	}}); code != 200 || out["moved"] != float64(2) {
		t.Fatalf("move two: %d %v", code, out)
	}
	if got := entryNames(e.filesAt("plugins/Essentials/backup")); got != "EssentialsX.jar:file settings.yml:file" {
		t.Fatalf("moved: %s", got)
	}
	e.putData("plugins/keep.yml", "keep\n")
	if code, out := e.call("POST", e.sp("/files/move"), map[string]any{"actor": "admin", "items": []any{map[string]any{"from": "plugins/Essentials/backup/settings.yml", "to": "plugins/keep.yml"}}}); code != 409 || codeOf(out) != "exists" {
		t.Fatalf("move over a file: %d %v", code, out)
	}
	if code, out := e.call("POST", e.sp("/files/move"), map[string]any{"actor": "admin", "items": []any{map[string]any{"from": "plugins", "to": "plugins/Essentials/plugins"}}}); code != 409 || codeOf(out) != "into_itself" {
		t.Fatalf("move a folder into itself: %d %v", code, out)
	}
	if code, out := e.call("POST", e.sp("/files/delete"), map[string]any{"actor": "admin", "paths": []string{"plugins/Essentials", "plugins/keep.yml", "plugins/gone.yml"}}); code != 200 {
		t.Fatalf("delete: %d %v", code, out)
	}
	if got := entryNames(e.filesAt("plugins")); got != "" {
		t.Fatalf("left in plugins: %s", got)
	}
	if code, out := e.call("POST", e.sp("/files/delete"), map[string]any{"actor": "admin", "paths": []string{}}); code != 400 {
		t.Fatalf("delete nothing: %d %v", code, out)
	}
}

// uploadPiece sends bytes of an upload's file n from offset.
func (e *agentEnv) uploadPiece(up string, n int, offset int64, body io.Reader) (int, api.FileUpload, map[string]any) {
	e.t.Helper()
	code, b, _ := e.fileRequest("PUT", e.sp(fmt.Sprintf("/files/uploads/%s/files/%d?offset=%d", up, n, offset)), body)
	var v api.FileUpload
	out := map[string]any{}
	json.Unmarshal(b, &v)
	json.Unmarshal(b, &out)
	return code, v, out
}

func (e *agentEnv) openUpload(folder string) string {
	e.t.Helper()
	code, out := e.call("POST", e.sp("/files/uploads"), map[string]any{"actor": "admin", "folder": folder})
	if code != 201 {
		e.t.Fatalf("open an upload into %q: %d %v", folder, code, out)
	}
	return out["id"].(string)
}

func (e *agentEnv) announceFile(up, name string, size int, replace bool) (int, map[string]any) {
	e.t.Helper()
	return e.call("POST", e.sp("/files/uploads/"+up+"/files"), map[string]any{"actor": "admin", "name": name, "size": size, "replace": replace})
}

// An upload keeps what arrived when its connection drops, and carries on
// from there; each file is put in place once it is whole.
func TestFileUploadsCarryOnAfterADroppedConnection(t *testing.T) {
	e, _ := idleFilesServer(t)
	jar := bytes.Repeat([]byte("jar bytes "), 5000)
	config := []byte("enabled: true\n")
	up := e.openUpload("plugins")
	for _, f := range []struct {
		name string
		size int
	}{{"LuckPerms.jar", len(jar)}, {"LuckPerms/config.yml", len(config)}, {"LuckPerms/empty.txt", 0}} {
		if code, out := e.announceFile(up, f.name, f.size, false); code != 201 {
			t.Fatalf("announce %s: %d %v", f.name, code, out)
		}
	}
	if code, out := e.announceFile(up, "LuckPerms.jar", 3, false); code != 409 {
		t.Fatalf("announce a file twice: %d %v", code, out)
	}
	if code, out := e.announceFile(up, "EssentialsX.jar", 3, false); code != 409 || codeOf(out) != "exists" {
		t.Fatalf("announce over a file without replacing it: %d %v", code, out)
	}
	if got := readFile(t, e.data("plugins/LuckPerms/empty.txt")); got != "" {
		t.Fatalf("an empty file: %q", got)
	}

	// The connection drops a third of the way through the jar.
	pr, pw := io.Pipe()
	go func() {
		pw.Write(jar[:len(jar)/3])
		time.Sleep(50 * time.Millisecond)
		pw.CloseWithError(errors.New("the connection dropped"))
	}()
	req, _ := http.NewRequest("PUT", e.ts.URL+e.sp(fmt.Sprintf("/files/uploads/%s/files/0?offset=0", up)), pr)
	req.Header.Set("X-Playkeeper-Actor", "admin")
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
	var got api.FileUpload
	e.waitFor("the upload to note what arrived", func() bool {
		code, out := e.call("GET", e.sp("/files/uploads/"+up), nil)
		got = decodeAs[api.FileUpload](t, out)
		return code == 200 && got.Files[0].Received == int64(len(jar)/3)
	})
	if got.Files[0].Placed || exists(e.data("plugins/LuckPerms.jar")) {
		t.Fatal("half a file was put in place")
	}
	if code, _, out := e.uploadPiece(up, 0, 0, bytes.NewReader(jar)); code != 409 || !strings.Contains(out["error"].(string), fmt.Sprintf("byte %d", len(jar)/3)) {
		t.Fatalf("starting over: %d %v", code, out)
	}
	code, v, out := e.uploadPiece(up, 0, int64(len(jar)/3), bytes.NewReader(jar[len(jar)/3:]))
	if code != 200 || !v.Files[0].Placed {
		t.Fatalf("the rest of the jar: %d %v", code, out)
	}
	if code, v, out := e.uploadPiece(up, 1, 0, bytes.NewReader(config)); code != 200 || !v.Files[1].Placed || !v.Files[2].Placed {
		t.Fatalf("config.yml: %d %v", code, out)
	}
	if readFile(t, e.data("plugins/LuckPerms.jar")) != string(jar) || readFile(t, e.data("plugins/LuckPerms/config.yml")) != string(config) {
		t.Fatal("the files in place aren't what was sent")
	}
	if code, _, out := e.uploadPiece(up, 1, 0, bytes.NewReader([]byte("more"))); code != 409 {
		t.Fatalf("more bytes after the end: %d %v", code, out)
	}
	if !e.auditHas("files.uploaded", "succeeded", "plugins/LuckPerms.jar") || !e.auditHas("files.uploaded", "succeeded", "plugins/LuckPerms/config.yml") {
		t.Fatal("the uploads weren't audited")
	}

	// Replacing needs saying so.
	up2 := e.openUpload("plugins/LuckPerms")
	if code, out := e.announceFile(up2, "config.yml", 5, true); code != 201 {
		t.Fatalf("announce a replacement: %d %v", code, out)
	}
	if code, v, out := e.uploadPiece(up2, 0, 0, strings.NewReader("new: 1")); code != 400 || v.Files != nil {
		t.Fatalf("more than announced: %d %v", code, out)
	}
	if code, v, out := e.uploadPiece(up2, 0, 0, strings.NewReader("new:1")); code != 200 || !v.Files[0].Placed {
		t.Fatalf("the replacement: %d %v", code, out)
	}
	if readFile(t, e.data("plugins/LuckPerms/config.yml")) != "new:1" {
		t.Fatal("not replaced")
	}
	dir := filepath.Join(e.cfg.StagingDir(), "files-"+up2)
	if code, out := e.call("DELETE", e.sp("/files/uploads/"+up2+"?actor=admin"), nil); code != 204 {
		t.Fatalf("cancel: %d %v", code, out)
	}
	if code, _ := e.call("GET", e.sp("/files/uploads/"+up2), nil); code != 404 || exists(dir) {
		t.Fatalf("a cancelled upload is still there: %d %v", code, exists(dir))
	}
	// Another server's upload isn't this server's.
	other := e.addIdleServer()
	if code, _ := e.call("GET", "/v1/servers/"+other+"/files/uploads/"+up, nil); code != 404 {
		t.Fatalf("another server's upload: %d", code)
	}
}

// A file that arrives while the server is busy waits with its bytes, and a
// request from its last byte puts it in place once the server is free.
func TestAnUploadThatCantBePutInPlaceCanBeTriedAgain(t *testing.T) {
	e, _ := idleFilesServer(t)
	up := e.openUpload("config")
	if code, out := e.announceFile(up, "paper-global.yml", 6, false); code != 201 {
		t.Fatalf("announce: %d %v", code, out)
	}
	release := e.holdOp("backup")
	code, v, out := e.uploadPiece(up, 0, 0, strings.NewReader("x: 1\n\n"))
	if code != 200 || v.Files[0].Placed || !strings.Contains(v.Files[0].Error, "busy") {
		t.Fatalf("placing during a backup: %d %v", code, out)
	}
	release()
	e.waitIdle()
	if code, v, out := e.uploadPiece(up, 0, 6, nil); code != 200 || !v.Files[0].Placed || v.Files[0].Error != "" {
		t.Fatalf("trying again: %d %v", code, out)
	}
	if readFile(t, e.data("config/paper-global.yml")) != "x: 1\n\n" {
		t.Fatal("not in place")
	}
}

func zipNames(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range zr.File {
		out = append(out, f.Name)
	}
	return strings.Join(out, " ")
}

func TestDownloadsSendAFileOrAZip(t *testing.T) {
	e, _ := idleFilesServer(t)
	code, b, h := e.fileRequest("GET", e.sp("/files/download?path=server.properties"), nil)
	if code != 200 || string(b) != "motd=A Playkeeper server\nspawn-protection=16\n" || h.Get("Content-Type") != "application/octet-stream" || h.Get("Content-Length") != "45" {
		t.Fatalf("a file: %d %q %v", code, b, h)
	}
	code, b, h = e.fileRequest("GET", e.sp("/files/download?path=plugins/Essentials"), nil)
	if code != 200 || h.Get("Content-Type") != "application/zip" || zipNames(t, b) != "Essentials/ Essentials/config.yml" {
		t.Fatalf("a folder: %d %v", code, h)
	}
	code, b, _ = e.fileRequest("GET", e.sp("/files/download?path=plugins/EssentialsX.jar&path=plugins/Essentials"), nil)
	if code != 200 || zipNames(t, b) != "EssentialsX.jar Essentials/ Essentials/config.yml" {
		t.Fatalf("two: %d %s", code, zipNames(t, b))
	}
	zr, _ := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if zr.File[0].Method != zip.Store || zr.File[2].Method != zip.Deflate {
		t.Fatalf("a jar is stored and a config compressed: %d %d", zr.File[0].Method, zr.File[2].Method)
	}
	code, b, _ = e.fileRequest("GET", e.sp("/files/download?path="), nil)
	if code != 200 || zipNames(t, b) != "plugins/ plugins/Essentials/ plugins/Essentials/config.yml plugins/EssentialsX.jar server.properties world/ world/level.dat" {
		t.Fatalf("the server's folder: %d %s", code, zipNames(t, b))
	}
	if code, _, _ := e.fileRequest("GET", e.sp("/files/download?path=server.properties&path=plugins/EssentialsX.jar"), nil); code != 400 {
		t.Fatalf("files from two folders: %d", code)
	}
	if code, _, _ := e.fileRequest("GET", e.sp("/files/download?path=missing.txt"), nil); code != 404 {
		t.Fatalf("a missing file: %d", code)
	}
	if !e.auditHas("files.downloaded", "succeeded", "server.properties") || !e.auditHas("files.downloaded", "succeeded", "plugins/Essentials") {
		t.Fatal("downloads weren't audited")
	}
}

// A zip keeps a record of each file in memory until it ends, so a download
// of more files and folders than the agent can keep track of is refused
// before it starts, and so is its check, which the dashboard asks first
// because a link can't show why a download failed.
func TestAZipOfTooManyFilesIsRefusedBeforeItStarts(t *testing.T) {
	e, _ := idleFilesServer(t)
	orig := maxZipped
	maxZipped = 5
	t.Cleanup(func() { maxZipped = orig })
	for i := range 3 {
		e.putData(fmt.Sprintf("plugins/Extra/%d.yml", i), "a: 1\n")
	}
	tooMany := func(what, query, says string) {
		t.Helper()
		for _, q := range []string{query, query + "&check=1"} {
			code, b, h := e.fileRequest("GET", e.sp("/files/download?"+q), nil)
			var out map[string]any
			json.Unmarshal(b, &out)
			if code != 409 || codeOf(out) != "too_many_entries" || !strings.HasPrefix(out["error"].(string), says+" more than 5 files and folders") || h.Get("Content-Type") == "application/zip" {
				t.Errorf("%s (%s): %d %v", what, q, code, out)
			}
		}
	}
	tooMany("a folder", "path=plugins", `"plugins" holds`)
	tooMany("the server's folder", "path=", "The server's folder holds")
	tooMany("several", "path=plugins/Essentials&path=plugins/Extra", "The files you chose hold")
	if code, b, _ := e.fileRequest("GET", e.sp("/files/download?path=plugins/Extra&check=1"), nil); code != 204 || len(b) != 0 {
		t.Fatalf("checking a folder that fits: %d %q", code, b)
	}
	if code, b, _ := e.fileRequest("GET", e.sp("/files/download?path=plugins/Extra"), nil); code != 200 || zipNames(t, b) != "Extra/ Extra/0.yml Extra/1.yml Extra/2.yml" {
		t.Fatalf("a folder that fits: %d", code)
	}
	if code, _, _ := e.fileRequest("GET", e.sp("/files/download?path=server.properties&check=1"), nil); code != 204 {
		t.Fatalf("checking a file: %d", code)
	}
	if err := os.Symlink(filepath.Join(e.cfg.DataDir, "canary.txt"), e.data("plugins/canary.txt")); err != nil {
		t.Fatal(err)
	}
	if code, b, _ := e.fileRequest("GET", e.sp("/files/download?path=plugins/canary.txt&check=1"), nil); code != 409 || !strings.Contains(string(b), `"link"`) {
		t.Fatalf("checking a link: %d %s", code, b)
	}
	list, err := e.a.listAudit(100)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if a.Action == "files.downloaded" && a.Target != "plugins/Extra" {
			t.Errorf("audited as downloaded: %+v", a)
		}
	}
}

// A chain of folders far deeper than the game's own, which each step down
// checks again from the top, is refused before a zip of it starts.
func TestAZipOfFoldersFarDownIsRefused(t *testing.T) {
	e, _ := idleFilesServer(t)
	e.putData("plugins/Deep/"+strings.Repeat("d/", 70)+"end.txt", "x")
	for _, q := range []string{"path=plugins", "path=plugins&check=1"} {
		code, b, h := e.fileRequest("GET", e.sp("/files/download?"+q), nil)
		var out map[string]any
		json.Unmarshal(b, &out)
		if code != 409 || codeOf(out) != "too_deep" || h.Get("Content-Type") == "application/zip" {
			t.Errorf("%s: %d %v", q, code, out)
		}
	}
}

// The game can name files as Linux allows. A zip names them so that no
// unpacker can write outside the folder it unpacks into, one on Windows
// that takes a backslash for a folder and a colon for a drive neither.
func TestZipNamesUnpackSafelyAnywhere(t *testing.T) {
	e, _ := idleFilesServer(t)
	e.putData(`plugins/Essentials/..\..\..\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\evil.bat`, "echo planted\r\n")
	e.putData(`C:\Users\Public\evil.bat`, "echo planted\r\n")
	e.putData("plugins/Essentials/NUL.txt", "x")
	e.putData("plugins/Essentials/trailing. ", "x")
	e.putData(`plugins/Essentials/D:\Games/start.bat`, "x")
	code, b, _ := e.fileRequest("GET", e.sp("/files/download?path=plugins/Essentials"), nil)
	want := "Essentials/ Essentials/.._.._.._AppData_Roaming_Microsoft_Windows_Start Menu_Programs_Startup_evil.bat Essentials/D__Games/ Essentials/D__Games/start.bat Essentials/_NUL.txt Essentials/config.yml Essentials/trailing__"
	if got := zipNames(t, b); code != 200 || got != want {
		t.Fatalf("a folder with planted names: %d %s", code, got)
	}
	code, b, _ = e.fileRequest("GET", e.sp("/files/download?path="), nil)
	if code != 200 {
		t.Fatalf("the server's folder: %d", code)
	}
	for name := range strings.SplitSeq(zipNames(t, b), " ") {
		if strings.ContainsAny(name, `\:`) || slices.Contains(strings.Split(name, "/"), "..") {
			t.Errorf("an unsafe name in the zip: %q", name)
		}
	}
	if !strings.Contains(zipNames(t, b), "C__Users_Public_evil.bat") {
		t.Errorf("the file at the top is missing: %s", zipNames(t, b))
	}
	for in, out := range map[string]string{"config.yml": "config.yml", "a\tb|c?.yml": "a_b_c_.yml", "con .txt": "_con .txt", "COM1": "_COM1", "lpt².log": "_lpt².log", "console.log": "console.log", "folder/NUL": "folder/_NUL"} {
		if got := zipName(in); got != out {
			t.Errorf("zipName(%q) = %q, want %q", in, got, out)
		}
	}
}

// A file the game makes shorter while a download reads it stops the
// download, rather than leave it cut short in a zip that looks whole.
func TestADownloadStopsAtAFileThatGotShorter(t *testing.T) {
	var out bytes.Buffer
	if err := copyWhole(&out, strings.NewReader("motd=x\n"), 7, "server.properties"); err != nil || out.String() != "motd=x\n" {
		t.Fatalf("a whole file: %v %q", err, out.String())
	}
	if err := copyWhole(io.Discard, strings.NewReader("motd"), 7, "server.properties"); err == nil || !strings.Contains(err.Error(), "shorter") {
		t.Fatalf("a file that got shorter: %v", err)
	}
	out.Reset()
	if err := copyWhole(&out, strings.NewReader("motd=x\nmore"), 7, "server.properties"); err != nil || out.String() != "motd=x\n" {
		t.Fatalf("a file that grew: %v %q", err, out.String())
	}
}

// One server's uploads can't take every upload the machine keeps open, and
// one whose files are all in place doesn't count.
func TestUploadsAreSharedOutBetweenServers(t *testing.T) {
	e, _ := idleFilesServer(t)
	var ups []string
	for range maxServerUploads {
		ups = append(ups, e.openUpload("config"))
	}
	code, out := e.call("POST", e.sp("/files/uploads"), map[string]any{"actor": "admin", "folder": "config"})
	if code != 409 || !strings.Contains(out["error"].(string), "has too many uploads open") {
		t.Fatalf("one upload too many for the server: %d %v", code, out)
	}
	first := e.sid
	e.addIdleServer()
	for range maxFileUploads - maxServerUploads {
		e.openUpload("")
	}
	code, out = e.call("POST", e.sp("/files/uploads"), map[string]any{"actor": "admin", "folder": ""})
	if code != 409 || !strings.Contains(out["error"].(string), "on this machine") {
		t.Fatalf("one upload too many for the machine: %d %v", code, out)
	}
	e.sid = first
	if code, out := e.announceFile(ups[0], "empty.yml", 0, false); code != 201 {
		t.Fatalf("an empty file: %d %v", code, out)
	}
	e.openUpload("config")
}

// A cancelled upload puts nothing more in place: a request that was still
// sending a file's last bytes when the upload was cancelled leaves it be,
// rather than replace what the admin cancelled replacing.
func TestACancelledUploadPutsNothingInPlace(t *testing.T) {
	e, _ := idleFilesServer(t)
	s := e.srv()
	e.putData("config/paper-global.yml", "mine\n")
	up := e.openUpload("config")
	if code, out := e.announceFile(up, "paper-global.yml", 5, true); code != 201 {
		t.Fatalf("announce: %d %v", code, out)
	}
	release := e.holdOp("backup")
	if code, v, out := e.uploadPiece(up, 0, 0, strings.NewReader("x: 1\n")); code != 200 || v.Files[0].Placed {
		t.Fatalf("the bytes, during a backup: %d %v", code, out)
	}
	release()
	e.waitIdle()
	fu, err := s.fileUpload(up)
	if err != nil {
		t.Fatal(err)
	}
	// A cancel marks the upload gone before it deletes what arrived.
	fu.mu.Lock()
	fu.gone = true
	fu.mu.Unlock()
	s.placeUpload(t.Context(), fu, 0, "admin")
	if got := readFile(t, e.data("config/paper-global.yml")); got != "mine\n" {
		t.Fatalf("a cancelled upload replaced the file: %q", got)
	}
	if e.auditHas("files.uploaded", "succeeded", "config/paper-global.yml") {
		t.Fatal("a cancelled upload was audited as put in place")
	}
}

// A file two requests put in place at once, such as an empty one announced
// from two tabs, is placed once, and the other request leaves it be rather
// than mark it failed.
func TestAFileIsPutInPlaceOnce(t *testing.T) {
	e, _ := idleFilesServer(t)
	s := e.srv()
	up := e.openUpload("config")
	if code, out := e.announceFile(up, "paper-global.yml", 5, false); code != 201 {
		t.Fatalf("announce: %d %v", code, out)
	}
	held, resume := holdChange(t, "config/paper-global.yml")
	placed := make(chan api.FileUpload, 1)
	go func() {
		_, v, _ := e.uploadPiece(up, 0, 0, strings.NewReader("x: 1\n"))
		placed <- v
	}()
	<-held
	fu, err := s.fileUpload(up)
	if err != nil {
		t.Fatal(err)
	}
	s.placeUpload(t.Context(), fu, 0, "admin")
	resume()
	if v := <-placed; len(v.Files) != 1 || !v.Files[0].Placed || v.Files[0].Error != "" {
		t.Fatalf("the file: %+v", v.Files)
	}
	if readFile(t, e.data("config/paper-global.yml")) != "x: 1\n" {
		t.Fatal("not in place")
	}
}

// A folder uploaded with more files than the recent activity reads rows for
// its lines is one line, and what happened before it still shows.
func TestABigUploadDoesntHideOlderActivity(t *testing.T) {
	e, _ := idleFilesServer(t)
	s := e.srv()
	s.audit("admin", "files.saved", "server.properties", "succeeded", "server.properties")
	time.Sleep(3 * time.Millisecond)
	for i := range 100 {
		p := fmt.Sprintf("plugins/Big/%03d.yml", i)
		s.audit("admin", "files.uploaded", p, "succeeded", p)
	}
	list, err := e.a.Activity(e.sid, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range list {
		got = append(got, fmt.Sprintf("%s %s %d", a.Kind, a.Detail, a.Count))
	}
	if want := "file_uploaded plugins/Big 100\nfile_saved server.properties 0"; strings.Join(got, "\n") != want {
		t.Fatalf("activity:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}

// However many uploads it takes to fill its lines, a look at the recent
// activity reads no more rows than its cap.
func TestRecentActivityReadsNoMoreThanItsRows(t *testing.T) {
	orig := maxActivityRows
	maxActivityRows = 50
	t.Cleanup(func() { maxActivityRows = orig })
	e, _ := idleFilesServer(t)
	s := e.srv()
	s.audit("admin", "files.saved", "server.properties", "succeeded", "server.properties")
	time.Sleep(3 * time.Millisecond)
	for i := range 100 {
		p := fmt.Sprintf("plugins/Big/%03d.yml", i)
		s.audit("admin", "files.uploaded", p, "succeeded", p)
	}
	list, err := e.a.Activity(e.sid, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Count != 50 {
		t.Fatalf("activity read past its 50 rows: %+v", list)
	}
}

// Every change is in the audit log and in the server's recent activity, and
// a run of uploads into one folder shows as one line.
func TestFileChangesAreAuditedAndShownAsActivity(t *testing.T) {
	e, _ := idleFilesServer(t)
	c := e.openFile("server.properties")
	steps := []func() (int, map[string]any){
		func() (int, map[string]any) { return e.saveFile("server.properties", c.Version, "motd=Changed\n") },
		func() (int, map[string]any) { return e.saveFile("plugins/new.yml", "new", "a: 1\n") },
		func() (int, map[string]any) {
			return e.call("POST", e.sp("/files/folder"), map[string]any{"actor": "admin", "path": "plugins/old"})
		},
		func() (int, map[string]any) {
			return e.call("POST", e.sp("/files/move"), map[string]any{"actor": "admin", "items": []any{map[string]any{"from": "plugins/new.yml", "to": "plugins/renamed.yml"}}})
		},
		func() (int, map[string]any) {
			return e.call("POST", e.sp("/files/move"), map[string]any{"actor": "admin", "items": []any{map[string]any{"from": "plugins/renamed.yml", "to": "plugins/old/renamed.yml"}}})
		},
		func() (int, map[string]any) {
			return e.call("POST", e.sp("/files/delete"), map[string]any{"actor": "admin", "paths": []string{"plugins/old"}})
		},
	}
	for i, step := range steps {
		if code, out := step(); code != 200 && code != 201 {
			t.Fatalf("step %d: %d %v", i, code, out)
		}
		// Activity is ordered by the millisecond.
		time.Sleep(3 * time.Millisecond)
	}
	for _, folder := range []string{"config", ""} {
		up := e.openUpload(folder)
		for i := range 3 {
			name := fmt.Sprintf("file-%d.yml", i)
			if code, out := e.announceFile(up, name, 2, false); code != 201 {
				t.Fatalf("announce %s in %q: %d %v", name, folder, code, out)
			}
			if code, _, out := e.uploadPiece(up, i, 0, strings.NewReader("x\n")); code != 200 {
				t.Fatalf("upload %s in %q: %d %v", name, folder, code, out)
			}
			time.Sleep(3 * time.Millisecond)
		}
	}
	list, err := e.a.Activity(e.sid, 20)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range list {
		got = append(got, fmt.Sprintf("%s %s %d", a.Kind, a.Detail, a.Count))
	}
	want := []string{"file_uploaded  3", "file_uploaded config 3", "file_deleted plugins/old 0", "file_moved plugins/renamed.yml → plugins/old/renamed.yml 0",
		"file_renamed plugins/new.yml → plugins/renamed.yml 0", "folder_made plugins/old 0", "file_created plugins/new.yml 0", "file_saved server.properties 0"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("activity:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, a := range list {
		if a.Actor != "admin" {
			t.Errorf("%s by %q", a.Kind, a.Actor)
		}
	}
}
