package agent

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/config"
)

const testAIKey = "sk-or-v1-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// aiKeyRequest sends body to path as it is, again while the server is busy
// (see whenFree), and returns the answer as it came.
func (e *agentEnv) aiKeyRequest(method, path, body string) (int, string) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, e.ts.URL+path, r)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			e.t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var out map[string]any
		json.Unmarshal(b, &out)
		if resp.StatusCode != http.StatusConflict || out["code"] != api.CodeBusy || time.Now().After(deadline) {
			return resp.StatusCode, string(b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func aiKeyBody(key string) string {
	b, _ := json.Marshal(map[string]string{"key": key, "actor": "admin"})
	return string(b)
}

// aiKeysAnswer is the AI keys an answer of 200 carries.
func (e *agentEnv) aiKeysAnswer(what string, code int, body string) api.AIKeys {
	e.t.Helper()
	var out api.AIKeys
	if code != http.StatusOK || json.Unmarshal([]byte(body), &out) != nil {
		e.t.Fatalf("%s: %d %s", what, code, body)
	}
	return out
}

func (e *agentEnv) aiKeys() api.AIKeys {
	e.t.Helper()
	code, body := e.aiKeyRequest("GET", e.sp("/ai-keys"), "")
	return e.aiKeysAnswer("the AI keys", code, body)
}

func (e *agentEnv) putAIKey(key string) api.AIKeys {
	e.t.Helper()
	code, body := e.aiKeyRequest("PUT", e.sp("/ai-keys/openrouter"), aiKeyBody(key))
	return e.aiKeysAnswer("saving the key", code, body)
}

// openSecretsAtEnd lets the test's folder be removed when it ends: nothing
// can delete from a secrets folder until it's opened, but root.
func (e *agentEnv) openSecretsAtEnd() {
	e.t.Cleanup(func() {
		dirs, _ := filepath.Glob(filepath.Join(e.cfg.DataDir, "servers", "*", secretsFolder))
		for _, d := range dirs {
			os.Chmod(d, 0o700)
		}
	})
}

func (e *agentEnv) containerBinds() []string {
	e.t.Helper()
	e.fd.mu.Lock()
	defer e.fd.mu.Unlock()
	c := e.fd.byName[e.cname()]
	if c == nil {
		e.t.Fatal("the server has no container")
	}
	return slices.Clone(c.cfg.HostConfig.Binds)
}

// addAIBuildBattle gives the current server the record of the AI Build
// Battle plugin, which ships inside Playkeeper.
func (e *agentEnv) addAIBuildBattle() {
	e.t.Helper()
	rec := addons.Installed{Source: aiBuildBattleSource, ProjectID: aiBuildBattleProject, Slug: aiBuildBattleProject, Name: "AI Build Battle",
		VersionID: "1.0.0", VersionNumber: "1.0.0", Channel: "release", FileName: "AIBuildBattle-1.0.0.jar", HashAlgo: "sha256",
		Hash: strings.Repeat("a", 64), InstalledAt: e.a.now()}
	if err := e.srv().saveAddons([]addons.Installed{rec}, nil, false); err != nil {
		e.t.Fatal(err)
	}
}

// checkSecretFile fails unless path is the game user's, with mode and, for
// a file, content.
func checkSecretFile(t *testing.T, cfg config.Config, path string, mode fs.FileMode, content string) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if fi.Mode() != mode || int(st.Uid) != cfg.GameUID || int(st.Gid) != cfg.GameGID {
		t.Fatalf("%s: %v %d:%d, want %v %d:%d", path, fi.Mode(), st.Uid, st.Gid, mode, cfg.GameUID, cfg.GameGID)
	}
	if fi.Mode().IsRegular() {
		if b, err := os.ReadFile(path); err != nil || string(b) != content {
			t.Fatalf("%s holds %d bytes (%v), not the key alone", path, len(b), err)
		}
	}
}

// The key is a file the game user alone can read, in a folder beside the
// world's that the container mounts read-only once it's made again; until
// then the key waits for a restart. A new key replaces it while the server
// runs, and removing it leaves the folder mounted.
func TestAnAIKeyIsKeptBesideTheWorldForTheGameUserAlone(t *testing.T) {
	e := newAgentEnv(t)
	e.openSecretsAtEnd()
	e.create()
	if k := e.aiKeys(); k.Available || k.Pending || len(k.Keys) != 1 || k.Keys["openrouter"].Set {
		t.Fatalf("a server without the plugin or a key: %+v", k)
	}
	dir := filepath.Join(e.cfg.DataDir, "servers", e.sid, secretsFolder)
	file := filepath.Join(dir, "openrouter_api_key")
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a server without the plugin or a key has a secrets folder: %v", err)
	}
	if binds := e.containerBinds(); len(binds) != 2 {
		t.Fatalf("a server without a secrets folder mounts %q", binds)
	}

	if k := e.putAIKey("  " + testAIKey + "\n"); !k.Keys["openrouter"].Set || !k.Pending {
		t.Fatalf("a key saved for a container made without the folder: %+v", k)
	}
	if filepath.Dir(dir) != filepath.Dir(e.dataDir()) || strings.HasPrefix(dir, e.dataDir()) {
		t.Fatalf("the secrets folder %s isn't beside the world's, %s", dir, e.dataDir())
	}
	checkSecretFile(t, e.cfg, dir, fs.ModeDir|0o500, "")
	checkSecretFile(t, e.cfg, file, 0o400, testAIKey)
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("the secrets folder holds %d files, not the key alone", len(entries))
	}
	if !e.status().PendingRestart {
		t.Fatal("a running server whose container lacks its secrets folder doesn't ask for a restart")
	}

	if op := e.runOp("POST", "/restart"); op.Status != api.OpSucceeded {
		t.Fatalf("restart: %+v", op)
	}
	if binds := e.containerBinds(); len(binds) != 3 || binds[2] != dir+":"+secretsMount+":ro" {
		t.Fatalf("a server with its secrets folder mounts %q", binds)
	}
	if k := e.aiKeys(); k.Pending || !k.Keys["openrouter"].Set {
		t.Fatalf("a key once the container has the folder: %+v", k)
	}
	if e.status().PendingRestart {
		t.Fatal("a restart that mounted the folder still asks for one")
	}

	next := "sk-or-v1-" + strings.Repeat("f", 64)
	if k := e.putAIKey(next); k.Pending || !k.Keys["openrouter"].Set {
		t.Fatalf("a key replaced while the folder is mounted: %+v", k)
	}
	checkSecretFile(t, e.cfg, file, 0o400, next)
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("the secrets folder holds %d files after a replace", len(entries))
	}

	for range 2 {
		code, body := e.aiKeyRequest("DELETE", e.sp("/ai-keys/openrouter?actor=admin"), "")
		if k := e.aiKeysAnswer("removing the key", code, body); k.Pending || k.Keys["openrouter"].Set {
			t.Fatalf("after removing the key: %+v", k)
		}
	}
	if _, err := os.Lstat(file); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the removed key is still there: %v", err)
	}
	checkSecretFile(t, e.cfg, dir, fs.ModeDir|0o500, "")
	if e.status().PendingRestart {
		t.Fatal("removing the key asks for a restart")
	}

	rows, err := e.a.db.Query(`SELECT actor, action, target, result, detail FROM audit WHERE action LIKE 'ai_key.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var actor, action, target, result, detail string
		rows.Scan(&actor, &action, &target, &result, &detail)
		got = append(got, strings.Join([]string{actor, action, target, result, detail}, " "))
	}
	want := []string{"admin ai_key.saved openrouter succeeded ", "admin ai_key.saved openrouter succeeded ", "admin ai_key.removed openrouter succeeded "}
	if !slices.Equal(got, want) {
		t.Fatalf("audit %q, want %q", got, want)
	}
}

// A server with the AI Build Battle plugin has its secrets folder, empty,
// from its start, so a key saved while it runs needs no restart.
func TestAServerWithThePluginMountsItsSecretsFolderFromItsStart(t *testing.T) {
	e := newAgentEnv(t)
	e.openSecretsAtEnd()
	e.create()
	if op := e.runOp("POST", "/stop"); op.Status != api.OpSucceeded {
		t.Fatalf("stop: %+v", op)
	}
	dir := filepath.Join(e.cfg.DataDir, "servers", e.sid, secretsFolder)
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a server without the plugin has a secrets folder: %v", err)
	}
	e.addAIBuildBattle()
	if k := e.aiKeys(); !k.Available || k.Pending || k.Keys["openrouter"].Set {
		t.Fatalf("a stopped server with the plugin: %+v", k)
	}
	if op := e.runOp("POST", "/start"); op.Status != api.OpSucceeded {
		t.Fatalf("start: %+v", op)
	}
	checkSecretFile(t, e.cfg, dir, fs.ModeDir|0o500, "")
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a new secrets folder holds %d files", len(entries))
	}
	if binds := e.containerBinds(); len(binds) != 3 || binds[2] != dir+":"+secretsMount+":ro" {
		t.Fatalf("the plugin's server mounts %q", binds)
	}
	if k := e.putAIKey(testAIKey); !k.Available || k.Pending || !k.Keys["openrouter"].Set {
		t.Fatalf("a key saved while the folder is mounted: %+v", k)
	}
	if e.status().PendingRestart {
		t.Fatal("a key saved while the folder is mounted asks for a restart")
	}
	if op := e.runOp("POST", "/stop"); op.Status != api.OpSucceeded {
		t.Fatalf("stop: %+v", op)
	}
	if k := e.putAIKey(testAIKey); k.Pending || !k.Keys["openrouter"].Set {
		t.Fatalf("a key saved while the server is stopped: %+v", k)
	}
}

// The secrets folder is mounted, read-only and relabelled like the other
// binds, only when the server has it, and never into the container that
// downloads the software; without it the definition, and so its hash, is
// what it was.
func TestOnlyAServerWithASecretsFolderMountsIt(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.InstallID = "0f1e2d3c-golden"
	cfg.GameUID, cfg.GameGID = 999, 998
	sc := api.ServerConfig{VersionID: "paper-26.1.2", MinecraftVersion: "26.1.2", PaperBuild: 74, MemoryMB: 3072, HeapMB: 2304, LevelName: "world", MOTD: "Playkeeper update test", MaxPlayers: 7, Whitelist: true}
	for _, selinux := range []bool{false, true} {
		a := &Agent{cfg: cfg, opts: Options{StopTimeout: 90 * time.Second}, selinuxKnown: true, selinux: selinux}
		for _, layout := range []string{layoutV1, layoutV2} {
			s := &server{Agent: a, id: "abcdefghij", layout: layout, gamePort: 25566}
			if err := os.MkdirAll(s.dataDir(), 0o755); err != nil {
				t.Fatal(err)
			}
			before, hash := s.containerSpec(sc, false, nil)
			if len(before.HostConfig.Binds) != 2 {
				t.Fatalf("%s, SELinux %v: without a secrets folder the binds are %q", layout, selinux, before.HostConfig.Binds)
			}
			if err := os.Mkdir(s.secretsDir(), 0o500); err != nil {
				t.Fatal(err)
			}
			want := s.secretsDir() + ":/run/playkeeper/secrets:ro"
			if selinux {
				want += ",z"
			}
			spec, with := s.containerSpec(sc, false, nil)
			if len(spec.HostConfig.Binds) != 3 || !slices.Equal(spec.HostConfig.Binds[:2], before.HostConfig.Binds) || spec.HostConfig.Binds[2] != want || with == hash {
				t.Fatalf("%s, SELinux %v: with a secrets folder the binds are %q (hash %s, was %s)", layout, selinux, spec.HostConfig.Binds, with, hash)
			}
			if setup, _ := s.containerSpec(sc, true, nil); len(setup.HostConfig.Binds) != 2 {
				t.Fatalf("%s: the container that downloads the software mounts %q", layout, setup.HostConfig.Binds)
			}
			if err := os.Remove(s.secretsDir()); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(s.dataDir(), s.secretsDir()); err != nil {
				t.Fatal(err)
			}
			if spec, h := s.containerSpec(sc, false, nil); len(spec.HostConfig.Binds) != 2 || h != hash {
				t.Fatalf("%s: a link where the secrets folder goes is mounted: %q", layout, spec.HostConfig.Binds)
			}
			os.Remove(s.secretsDir())
			if _, h := s.containerSpec(sc, false, nil); h != hash {
				t.Fatalf("%s: once the folder went the hash is %s, not %s", layout, h, hash)
			}
		}
	}
}

// A key is checked before anything is written, and no answer quotes it or
// the body it came in.
func TestAnAIKeyIsCheckedWithoutBeingQuoted(t *testing.T) {
	e := newAgentEnv(t)
	e.openSecretsAtEnd()
	e.create()
	dir := filepath.Join(e.cfg.DataDir, "servers", e.sid, secretsFolder)
	const mark = "Zq9Vw3"
	long := "sk-or-v1-" + mark + strings.Repeat("x", maxAIKey)
	keyErrors := []struct{ key, msg, reason string }{
		{"", "Paste your OpenRouter key.", "ai_key_missing"},
		{" \t\n", "Paste your OpenRouter key.", "ai_key_missing"},
		{"sk-proj-" + mark + "abcdefghijklmnopqrstuvwxyz", "That isn't an OpenRouter key: those start with sk-or-.", "ai_key_prefix"},
		{"SK-OR-V1-" + mark + "abcdefghijklmnopqrstuvwxyz", "That isn't an OpenRouter key: those start with sk-or-.", "ai_key_prefix"},
		{"sk-or-v1-" + mark + " abcdefghijklmnop", "Keys have no spaces or characters like that. Copy it again from openrouter.ai/keys.", "ai_key_characters"},
		{"sk-or-v1-" + mark + "é-abcdefghijklmnop", "Keys have no spaces or characters like that. Copy it again from openrouter.ai/keys.", "ai_key_characters"},
		{"sk-or-v1-" + mark + "\x7f-abcdefghijklmnop", "Keys have no spaces or characters like that. Copy it again from openrouter.ai/keys.", "ai_key_characters"},
		{"sk-or-" + mark + "1234567", "That key is too short. Copy all of it from openrouter.ai/keys.", "ai_key_short"},
		{long[:maxAIKey+1], "That key is too long. Copy only the key from openrouter.ai/keys.", "ai_key_long"},
	}
	for _, c := range keyErrors {
		code, body := e.aiKeyRequest("PUT", e.sp("/ai-keys/openrouter"), aiKeyBody(c.key))
		var out api.Error
		json.Unmarshal([]byte(body), &out)
		if code != 400 || out.Code != api.CodeInvalid || out.Field != "key" || out.Error != c.msg || out.Reason != c.reason {
			t.Errorf("key %q: %d %+v, want %q (%s)", c.key, code, out, c.msg, c.reason)
		}
		if strings.Contains(body, mark) {
			t.Errorf("key %q: the answer quotes it: %s", c.key, body)
		}
	}
	bodies := []struct{ body, msg string }{
		{`{"key":"sk-or-v1-` + mark + `abcdefghijklmnop","actor":"admin","note":"` + mark + `"}`, "Invalid request body."},
		{`{"key":"sk-or-v1-` + mark + `abcdefghijklmnop","actor":"admin"} ` + mark, "Invalid request body."},
		{`{"key":"sk-or-v1-` + mark + `abcdefghijklmnop`, "Invalid request body."},
		{`{"key":["sk-or-v1-` + mark + `abcdefghijklmnop"],"actor":"admin"}`, "Invalid request body."},
		{`{"key":"sk-or-v1-` + mark + `abcdefghijklmnop"}`, "actor is required"},
	}
	for _, c := range bodies {
		code, body := e.aiKeyRequest("PUT", e.sp("/ai-keys/openrouter"), c.body)
		var out api.Error
		json.Unmarshal([]byte(body), &out)
		if code != 400 || out.Error != c.msg || strings.Contains(body, mark) {
			t.Errorf("body %s: %d %s, want %q", c.body, code, body, c.msg)
		}
	}
	if code, body := e.aiKeyRequest("PUT", e.sp("/ai-keys/openai"), aiKeyBody(testAIKey)); code != 404 || strings.Contains(body, testAIKey) {
		t.Errorf("a provider there's no key for: %d %s", code, body)
	}
	if code, body := e.aiKeyRequest("DELETE", e.sp("/ai-keys/openai?actor=admin"), ""); code != 404 {
		t.Errorf("removing a provider there's no key for: %d %s", code, body)
	}
	if code, body := e.aiKeyRequest("DELETE", e.sp("/ai-keys/openrouter"), ""); code != 400 {
		t.Errorf("removing a key without an actor: %d %s", code, body)
	}
	release := e.holdWhenFree(e.srv())
	for _, c := range []struct {
		method string
		body   any
	}{{"PUT", map[string]any{"key": testAIKey, "actor": "admin"}}, {"DELETE", nil}} {
		if code, out := e.call(c.method, e.sp("/ai-keys/openrouter?actor=admin"), c.body); code != 409 || out["code"] != api.CodeBusy {
			t.Errorf("%s while the server is busy: %d %v", c.method, code, out)
		}
	}
	release()
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("keys that weren't saved made the secrets folder: %v", err)
	}
	for _, key := range []string{"sk-or-" + strings.Repeat("a", minAIKey-6), "sk-or-" + strings.Repeat("b", maxAIKey-6)} {
		if k := e.putAIKey(key); !k.Keys["openrouter"].Set {
			t.Fatalf("a key of %d characters: %+v", len(key), k)
		}
		checkSecretFile(t, e.cfg, filepath.Join(dir, "openrouter_api_key"), 0o400, key)
	}
}

// The key is in no log line, audit entry, answer or error, nor anywhere in
// Playkeeper's database, a backup, the template export or the Files tab.
func TestAnAIKeyIsNowhereButItsFile(t *testing.T) {
	var logs logBuffer
	e := newAgentEnvWith(t, func(e *agentEnv) {
		e.tweak = func(o *Options) {
			o.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		}
	})
	e.openSecretsAtEnd()
	e.create()
	secret := "sk-or-v1-" + strings.Repeat("7c3e", 16)
	part := secret[len(secret)-12:]
	var answers strings.Builder
	send := func(method, path, body string) int {
		t.Helper()
		code, b := e.aiKeyRequest(method, path, body)
		answers.WriteString(b)
		return code
	}
	if code := send("PUT", e.sp("/ai-keys/openrouter"), aiKeyBody(secret)); code != 200 {
		t.Fatalf("saving the key: %d", code)
	}
	for _, body := range []string{
		aiKeyBody(secret + " x"), aiKeyBody("sk-ant-" + secret), aiKeyBody(secret + strings.Repeat("9", maxAIKey)),
		`{"key":"` + secret + `","actor":"admin"}` + secret, `{"key":"` + secret, `{"key":["` + secret + `"],"actor":"admin"}`,
		`{"key":"` + secret + `","actor":"admin","extra":"` + secret + `"}`, `{"key":"` + secret + `","actor":""}`,
	} {
		if code := send("PUT", e.sp("/ai-keys/openrouter"), body); code != 400 {
			t.Fatalf("a refused key: %d", code)
		}
	}
	send("GET", e.sp("/ai-keys"), "")
	for _, p := range []string{"/files?path=..", "/files/content?path=" + url.QueryEscape("../secrets/openrouter_api_key"),
		"/files/download?path=" + url.QueryEscape("../secrets/openrouter_api_key"), "/template"} {
		code, _, b := e.get(e.sp(p))
		answers.Write(b)
		if strings.HasPrefix(p, "/files") && code == 200 {
			t.Errorf("the Files tab reaches outside the world's folder: %s", p)
		}
	}

	backupID := e.backup()
	backups, err := e.srv().listBackups("id = ?", backupID)
	if err != nil || len(backups) != 1 {
		t.Fatal(backups, err)
	}
	archive, err := os.ReadFile(e.a.backupPath(backups[0].FileName))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tarred, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(tarred, []byte(part)) || bytes.Contains(tarred, []byte(secretsFolder+"/")) {
		t.Error("the backup holds the key or its folder")
	}

	// What Playkeeper explains, and what it only logs.
	first := e.sid
	other := e.addIdleServer()
	otherDir := filepath.Join(e.cfg.DataDir, "servers", other)
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, secretsFolder), []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := send("PUT", "/v1/servers/"+other+"/ai-keys/openrouter", aiKeyBody(secret)); code != 409 {
		t.Errorf("a key where something else is in the folder's place: %d", code)
	}
	if os.Geteuid() != 0 {
		if err := os.Remove(filepath.Join(otherDir, secretsFolder)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(otherDir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(otherDir, 0o755) })
		if code := send("PUT", "/v1/servers/"+other+"/ai-keys/openrouter", aiKeyBody(secret)); code != 500 {
			t.Errorf("a key that can't be written: %d", code)
		}
		if !strings.Contains(logs.String(), "an AI key could not be changed") {
			t.Error("a key that couldn't be written isn't logged")
		}
	}
	e.sid = first
	if code := send("DELETE", e.sp("/ai-keys/openrouter?actor=admin"), ""); code != 200 {
		t.Errorf("removing the key: %d", code)
	}

	if strings.Contains(answers.String(), part) {
		t.Error("an answer quotes the key")
	}
	if strings.Contains(logs.String(), part) {
		t.Error("the log quotes the key")
	}
	var audit strings.Builder
	rows, err := e.a.db.Query(`SELECT actor || action || target || result || detail FROM audit`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		rows.Scan(&s)
		audit.WriteString(s)
	}
	rows.Close()
	if strings.Contains(audit.String(), part) || !strings.Contains(audit.String(), "ai_key.saved") {
		t.Errorf("the audit log: %s", audit.String())
	}
	for _, suffix := range []string{"", "-wal"} {
		if b, err := os.ReadFile(filepath.Join(e.cfg.AgentDir(), "agent.db") + suffix); err == nil && bytes.Contains(b, []byte(part)) {
			t.Errorf("agent.db%s holds the key", suffix)
		}
	}
}

// Deleting a server deletes its key with it, and leaves nothing that holds it.
func TestDeletingAServerDeletesItsAIKey(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.putAIKey(testAIKey)
	dir := e.srv().dir()
	code, out := e.callWhenFree("POST", e.sp("/delete"), map[string]any{"confirm": e.srv().name(), "actor": "admin"})
	if code != 202 || e.waitOp(out["id"].(string)).Status != api.OpSucceeded {
		t.Fatalf("delete: %d %v", code, out)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the deleted server's folder: %v", err)
	}
	if left, _ := filepath.Glob(dir + ".deleting-*"); len(left) != 0 {
		t.Fatalf("the deleted server left %q", left)
	}
	filepath.WalkDir(e.cfg.DataDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte(testAIKey)) {
				t.Errorf("%s holds the deleted server's key", p)
			}
		}
		return nil
	})
}
