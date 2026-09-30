package agent

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// errorOf is an answer's error message.
func errorOf(out map[string]any) string { s, _ := out["error"].(string); return s }

// setLimits replaces the machine's disk limits, as the dashboard does.
func (e *agentEnv) setLimits(limits ...map[string]any) {
	e.t.Helper()
	list := make([]any, len(limits))
	for i, l := range limits {
		list[i] = l
	}
	var set []api.DiskLimit
	if code := e.callInto("PUT", "/v1/disk-limits", map[string]any{"limits": list, "actor": "admin"}, &set); code != 200 {
		e.t.Fatalf("setting the disk limits: %d %+v", code, set)
	}
}

// openImportFor opens an upload for a new server against the disk limit
// called limit.
func (e *agentEnv) openImportFor(limit string) (int, map[string]any) {
	e.t.Helper()
	return e.call("POST", "/v1/world-imports", map[string]any{"actor": "alex", "diskLimit": limit})
}

// uploadChunked uploads archive to path without saying its size.
func (e *agentEnv) uploadChunked(path string, archive []byte) (int, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.ts.URL+path, io.NopCloser(bytes.NewReader(archive)))
	req.Header.Set("X-Playkeeper-Actor", "alex")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// serverIDs is the ids of the machine's servers.
func (e *agentEnv) serverIDs() []string {
	var ids []string
	for _, s := range e.a.serverList() {
		ids = append(ids, s.id)
	}
	return ids
}

// A world uploaded for a new server against a disk limit, as the dashboard
// sends a creator's, counts against it from its first announced byte, and
// the server it makes joins it. A limit the machine hasn't been sent is
// refused rather than left uncounted, the limit is named properly, only an
// upload for a new server names one, and one account keeps one upload open.
func TestAWorldForANewServerCountsAgainstItsDiskLimit(t *testing.T) {
	e := newAgentEnv(t)
	if code, out := e.openImportFor("account-6"); code != http.StatusConflict || !strings.Contains(errorOf(out), "hasn't reached the machine") {
		t.Fatalf("an upload against a limit the machine hasn't got: %d %v", code, out)
	}
	e.setLimits(map[string]any{"id": "account-6", "limitBytes": 1 << 10, "servers": []string{}})
	for _, bad := range []string{"Account 6", "../6"} {
		if code, out := e.openImportFor(bad); code != 400 {
			t.Fatalf("an upload against the limit %q: %d %v", bad, code, out)
		}
	}
	code, out := e.openImportFor("account-6")
	if code != 201 || out["diskLimit"] != "account-6" {
		t.Fatalf("an upload against account-6: %d %v", code, out)
	}
	imp := out["id"].(string)
	if code, out := e.openImportFor("account-6"); code != http.StatusConflict || !strings.Contains(errorOf(out), "open already") {
		t.Fatalf("a second upload for the same account: %d %v", code, out)
	}
	archive, _ := singleplayerUpload(t)
	if code, out := e.announce(imp, "Survival-2024.zip", len(archive)); code != 507 || codeOf(out) != api.CodeDiskLimit {
		t.Fatalf("a world past the limit: %d %v", code, out)
	}
	e.setLimits(map[string]any{"id": "account-6", "limitBytes": len(archive) * 3 / 2, "servers": []string{}})
	if code, out := e.announce(imp, "Survival-2024.zip", len(archive)); code != 201 {
		t.Fatalf("a world inside the limit: %d %v", code, out)
	}
	if code, out := e.announce(imp, "Survival-2024-nether.zip", len(archive)); code != 507 || codeOf(out) != api.CodeDiskLimit {
		t.Fatalf("a second archive past what the first leaves: %d %v", code, out)
	}
	if code, out, err := sendBytes(e.ts.URL, imp, 0, 0, bytes.NewReader(archive)); err != nil || code != 200 {
		t.Fatalf("upload: %d %v %v", code, out, err)
	}
	if code, out := e.call("POST", importPath(imp, "/inspect"), map[string]any{"actor": "alex"}); code != 200 {
		t.Fatalf("check: %d %v", code, out)
	}
	create := func() (int, map[string]any) {
		return e.call("POST", importPath(imp, "/create"), map[string]any{"versionId": "paper-26.2", "name": "Survival", "memoryMB": 1536, "acceptEula": true, "actor": "alex"})
	}
	e.setLimits(map[string]any{"id": "account-6", "limitBytes": 1 << 10, "servers": []string{}})
	if code, out := create(); code != 507 || codeOf(out) != api.CodeDiskLimit {
		t.Fatalf("creating a server whose world doesn't fit: %d %v", code, out)
	}
	e.setLimits(map[string]any{"id": "account-6", "limitBytes": 1 << 30, "servers": []string{}})
	code, out = create()
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("create: %+v", op)
	}
	made := out["serverId"].(string)
	if l := e.a.diskLimitOf(made); l == nil || l.ID != "account-6" {
		t.Fatalf("the new server counts against %+v", l)
	}
	if account := e.a.accountOf(made); account != "account-6" {
		t.Fatalf("the new server is one of %q's servers, not account-6's", account)
	}
	e.sid = made
	if code, out := e.call("POST", e.sp("/world-imports"), map[string]any{"actor": "alex", "diskLimit": "account-6"}); code != 400 {
		t.Fatalf("an upload into a server naming a limit: %d %v", code, out)
	}
}

// A backup uploaded to make a new server against a disk limit is read no
// further than the limit has room for, the world it unpacks to has to fit,
// and the server it makes joins it. A newer upload against the same limit
// replaces the one staged before.
func TestABackupForANewServerCountsAgainstItsDiskLimit(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	_, raw := e.compressibleBackup()
	e.sid = ""
	upload := func(limit string) (int, map[string]any) {
		return e.uploadTo("/v1/restore/upload?diskLimit="+limit, raw)
	}
	if code, out := upload("account-7"); code != http.StatusConflict {
		t.Fatalf("a restore against a limit the machine hasn't got: %d %v", code, out)
	}
	if code, out := upload("Account%207"); code != 400 {
		t.Fatalf("a restore against the limit \"Account 7\": %d %v", code, out)
	}
	e.setLimits(map[string]any{"id": "account-7", "limitBytes": int64(len(raw)) / 2, "servers": []string{}})
	if code, out := upload("account-7"); code != 507 || codeOf(out) != api.CodeDiskLimit {
		t.Fatalf("a backup larger than the limit's room: %d %v", code, out)
	}
	if code, out := e.uploadChunked("/v1/restore/upload?diskLimit=account-7", raw); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a backup larger than the limit's room, of a size it didn't say: %d %v", code, out)
	}
	e.setLimits(map[string]any{"id": "account-7", "limitBytes": int64(len(raw)) + 64<<10, "servers": []string{}})
	if code, out := upload("account-7"); code != 507 || codeOf(out) != api.CodeDiskLimit {
		t.Fatalf("a backup whose world doesn't fit: %d %v", code, out)
	}
	e.setLimits(map[string]any{"id": "account-7", "limitBytes": 1 << 30, "servers": []string{}})
	code, first := upload("account-7")
	if code != 200 || first["diskLimit"] != "account-7" {
		t.Fatalf("a backup inside the limit: %d %v", code, first)
	}
	code, preview := upload("account-7")
	if code != 200 {
		t.Fatalf("a second backup: %d %v", code, preview)
	}
	if left, _ := filepath.Glob(e.a.stageDir(first["id"].(string)) + "*"); len(left) != 0 {
		t.Fatalf("the backup staged first is still there beside the newer one: %v", left)
	}
	// The staged backup counts against the limit until it's applied or
	// replaced: a world for a new server gets only what it leaves, and a
	// newer backup may use its room, as it replaces it.
	f, err := readStageFile(e.a.stageDir(preview["id"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	staged := f.Preview.SizeBytes + unpackedBytes(f.Manifest)
	e.setLimits(map[string]any{"id": "account-7", "limitBytes": staged + 1000, "servers": []string{}})
	code, imp := e.openImportFor("account-7")
	if code != 201 {
		t.Fatalf("an upload of a world against account-7: %d %v", code, imp)
	}
	if code, out := e.announce(imp["id"].(string), "Survival-2024.zip", 2000); code != 507 || codeOf(out) != api.CodeDiskLimit {
		t.Fatalf("a world past what the staged backup leaves: %d %v", code, out)
	}
	code, preview = upload("account-7")
	if code != 200 {
		t.Fatalf("a newer backup in the room of the one it replaces: %d %v", code, preview)
	}
	e.setLimits(map[string]any{"id": "account-7", "limitBytes": staged - f.Preview.SizeBytes/2, "servers": []string{}})
	if code, out := upload("account-7"); code != 507 || codeOf(out) != api.CodeDiskLimit {
		t.Fatalf("a newer backup whose archive and world together pass the room: %d %v", code, out)
	}
	apply := func() (int, map[string]any) {
		return e.callWhenFree("POST", "/v1/restore/"+preview["id"].(string)+"/apply", map[string]any{"confirm": preview["confirmPhrase"], "acceptEula": true, "actor": "alex"})
	}
	e.setLimits(map[string]any{"id": "account-7", "limitBytes": int64(len(raw)), "servers": []string{}})
	if code, out := apply(); code != 507 || codeOf(out) != api.CodeDiskLimit {
		t.Fatalf("restoring a world that no longer fits: %d %v", code, out)
	}
	e.setLimits(map[string]any{"id": "account-7", "limitBytes": 1 << 30, "servers": []string{}})
	code, out := apply()
	if code != 202 {
		t.Fatalf("apply: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("restore: %+v", op)
	}
	if l := e.a.diskLimitOf(out["serverId"].(string)); l == nil || l.ID != "account-7" {
		t.Fatalf("the restored server counts against %+v", l)
	}
	e.sid = out["serverId"].(string)
	if code, out := e.uploadTo(e.sp("/restore/upload")+"?diskLimit=account-7", raw); code != 400 {
		t.Fatalf("an upload into a server naming a limit: %d %v", code, out)
	}
}

// A restore claims the upload it applies from before it reads it until its
// operation is over. A newer upload against the same disk limit, coming in
// as the operation starts, doesn't take it away, nor does discarding it, and
// it can't be applied twice at once. An upload whose restore left its swap
// journal isn't replaced either.
func TestARestoreKeepsTheUploadItApplies(t *testing.T) {
	var (
		raw     []byte
		preview map[string]any
		once    sync.Once
		seen    = make(chan whileRestoring, 1)
	)
	e := newAgentEnvWith(t, func(e *agentEnv) {
		e.tweak = func(o *Options) {
			o.RestoreStarting = func(string) {
				once.Do(func() { seen <- interfere(e.ts.URL, preview, raw) })
			}
		}
	})
	e.create()
	_, raw = e.compressibleBackup()
	e.sid = ""
	e.setLimits(map[string]any{"id": "account-8", "limitBytes": 1 << 30, "servers": []string{}})
	code, preview := e.uploadTo("/v1/restore/upload?diskLimit=account-8", raw)
	if code != 200 {
		t.Fatalf("upload: %d %v", code, preview)
	}
	code, out := e.callWhenFree("POST", "/v1/restore/"+preview["id"].(string)+"/apply", map[string]any{"confirm": preview["confirmPhrase"], "acceptEula": true, "actor": "alex"})
	if code != 202 {
		t.Fatalf("apply: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("the restore, after a newer upload came in as it started: %+v", op)
	}
	var m whileRestoring
	select {
	case m = <-seen:
	default:
		t.Fatal("the restore ran without saying it started")
	}
	switch {
	case m.err != nil:
		t.Fatal(m.err)
	case m.discard != http.StatusConflict:
		t.Fatalf("discarding the upload a restore is starting from: %d", m.discard)
	case m.again != http.StatusConflict:
		t.Fatalf("applying the upload a restore is starting from: %d", m.again)
	case m.upload != 200 || !exists(e.a.stageDir(m.newer)):
		t.Fatalf("the newer upload: %d, staged %v", m.upload, exists(e.a.stageDir(m.newer)))
	}
	if code, again := e.call("POST", "/v1/restore/"+preview["id"].(string)+"/apply", map[string]any{"confirm": preview["confirmPhrase"], "acceptEula": true, "actor": "alex"}); code != 404 {
		t.Fatalf("applying the upload again once its restore is over: %d %v", code, again)
	}
	if l := e.a.diskLimitOf(out["serverId"].(string)); l == nil || l.ID != "account-8" {
		t.Fatalf("the restored server counts against %+v", l)
	}
	j := &swapJournal{ServerID: "abcdefghjk", Aside: "data.replaced-20260929-120000", Failed: "data.failed-restore-20260929-120000", State: swapChecking}
	if err := writeSwapJournal(e.a.stageDir(m.newer), j); err != nil {
		t.Fatal(err)
	}
	code, last := e.uploadTo("/v1/restore/upload?diskLimit=account-8", raw)
	if code != 200 {
		t.Fatalf("an upload after one whose restore left its journal: %d %v", code, last)
	}
	if !exists(e.a.stageDir(m.newer)) {
		t.Fatal("a newer upload replaced one whose restore left its swap journal")
	}
	if code, out := e.call("DELETE", "/v1/restore/"+last["id"].(string), nil); code != http.StatusNoContent {
		t.Fatalf("discarding the last upload: %d %v", code, out)
	}
	if left, _ := filepath.Glob(e.a.stageDir(last["id"].(string)) + "*"); len(left) != 0 {
		t.Fatalf("a discarded upload left %v", left)
	}
}

// A server made from a customer's upload is one of their account's, the one
// the upload names: the name its backup had is theirs beside a server of
// that name that isn't theirs, with no number after it.
func TestAServerFromACustomersUploadIsOneOfTheirs(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	_, raw := e.compressibleBackup()
	e.sid = ""
	e.setLimits(map[string]any{"id": "account-8", "limitBytes": 1 << 30, "servers": []string{}})
	code, preview := e.uploadTo("/v1/restore/upload?diskLimit=account-8", raw)
	if code != 200 {
		t.Fatalf("upload: %d %v", code, preview)
	}
	code, out := e.callWhenFree("POST", "/v1/restore/"+preview["id"].(string)+"/apply", map[string]any{"confirm": preview["confirmPhrase"], "acceptEula": true, "actor": "alex"})
	if code != 202 {
		t.Fatalf("apply: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("the restore: %+v", op)
	}
	e.sid = out["serverId"].(string)
	row, err := e.srv().row()
	if err != nil {
		t.Fatal(err)
	}
	if account := e.a.accountOf(e.sid); row.Name != "My server" || account != "account-8" {
		t.Fatalf("made from alex's upload beside the machine's own My server: %q in %q", row.Name, account)
	}
}

// whileRestoring is what the agent answered as a restore started (see
// interfere).
type whileRestoring struct {
	discard, again, upload int
	// newer is the stage of the upload.
	newer string
	err   error
}

// interfere discards the upload preview is of, applies it again and uploads
// raw against its disk limit, at url, as its restore starts.
func interfere(url string, preview map[string]any, raw []byte) (m whileRestoring) {
	send := func(method, path string, body io.Reader) (int, map[string]any) {
		if m.err != nil {
			return 0, nil
		}
		req, _ := http.NewRequest(method, url+path, body)
		req.Header.Set("X-Playkeeper-Actor", "alex")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			m.err = err
			return 0, nil
		}
		defer resp.Body.Close()
		out := map[string]any{}
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	id := preview["id"].(string)
	m.discard, _ = send("DELETE", "/v1/restore/"+id, nil)
	again, _ := json.Marshal(map[string]any{"confirm": preview["confirmPhrase"], "acceptEula": true, "actor": "alex"})
	m.again, _ = send("POST", "/v1/restore/"+id+"/apply", bytes.NewReader(again))
	code, out := send("POST", "/v1/restore/upload?diskLimit="+preview["diskLimit"].(string), bytes.NewReader(raw))
	m.upload, m.newer = code, fmt.Sprint(out["id"])
	return m
}

// hostileZip is a zip holding files as they're named, however they're named.
func hostileZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		w.Write(body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// hostileTarGz is a gzipped tar of the headers given, each with its body.
func hostileTarGz(t *testing.T, entries []tar.Header, bodies [][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for i, h := range entries {
		h.Size = int64(len(bodies[i]))
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		tw.Write(bodies[i])
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

// A customer's world upload for a new server is treated as hostile: paths
// that leave the upload's folder, links, and what isn't a world archive are
// refused when it's checked, and a zip bomb when a server would be made
// from it, before anything is unpacked. None of them makes a server or
// writes outside the upload.
func TestACustomersWorldUploadIsTreatedAsHostile(t *testing.T) {
	e := newAgentEnv(t)
	e.setLimits(map[string]any{"id": "account-6", "limitBytes": 1 << 30, "servers": []string{}})
	level := levelDat(t, legacyLevelData("World", "1.21.4", 4189))
	bomb := hostileZip(t, map[string][]byte{"world/level.dat": []byte(level), "world/region/r.0.0.mca": make([]byte, 64<<20)})
	code, out := e.openImportFor("account-6")
	if code != 201 {
		t.Fatalf("a zip bomb: open: %d %v", code, out)
	}
	imp := out["id"].(string)
	if code, out := e.announce(imp, "bomb.zip", len(bomb)); code != 201 {
		t.Fatalf("a zip bomb: announce: %d %v", code, out)
	}
	if code, out, err := sendBytes(e.ts.URL, imp, 0, 0, bytes.NewReader(bomb)); err != nil || code != 200 {
		t.Fatalf("a zip bomb: upload: %d %v %v", code, out, err)
	}
	if code, out := e.call("POST", importPath(imp, "/inspect"), map[string]any{"actor": "alex"}); code != 200 {
		t.Fatalf("a zip bomb: check: %d %v", code, out)
	}
	code, out = e.call("POST", importPath(imp, "/create"), map[string]any{"versionId": "paper-26.2", "name": "Bomb", "memoryMB": 1536, "acceptEula": true, "actor": "alex"})
	if code != 422 || !strings.Contains(errorOf(out), "expands to more than") {
		t.Fatalf("a zip bomb: create: %d %v", code, out)
	}
	e.call("DELETE", importPath(imp, "?actor=alex"), nil)
	for _, c := range []struct {
		name, file string
		archive    []byte
		refusal    string
	}{
		{"a path out of the upload", "escape.zip", hostileZip(t, map[string][]byte{"world/level.dat": []byte(level), "../escape.txt": []byte("out")}), "escape.txt"},
		{"an absolute path", "absolute.zip", hostileZip(t, map[string][]byte{"world/level.dat": []byte(level), "/etc/cron.d/evil": []byte("* * * * * root sh")}), "evil"},
		{"a link", "link.tar.gz", hostileTarGz(t, []tar.Header{{Name: "world/level.dat", Typeflag: tar.TypeReg}, {Name: "world/region", Typeflag: tar.TypeSymlink, Linkname: "/etc"}}, [][]byte{[]byte(level), nil}), "link"},
		{"a file that isn't an archive", "world.zip", []byte("#!/bin/sh\nrm -rf /\n"), "archive"},
	} {
		code, out := e.openImportFor("account-6")
		if code != 201 {
			t.Fatalf("%s: open: %d %v", c.name, code, out)
		}
		imp := out["id"].(string)
		if code, out := e.announce(imp, c.file, len(c.archive)); code != 201 {
			t.Fatalf("%s: announce: %d %v", c.name, code, out)
		}
		if code, out, err := sendBytes(e.ts.URL, imp, 0, 0, bytes.NewReader(c.archive)); err != nil || code != 200 {
			t.Fatalf("%s: upload: %d %v %v", c.name, code, out, err)
		}
		code, out = e.call("POST", importPath(imp, "/inspect"), map[string]any{"actor": "alex"})
		if code != 422 || !strings.Contains(strings.ToLower(errorOf(out)), c.refusal) {
			t.Fatalf("%s: check: %d %v", c.name, code, out)
		}
		if code, out := e.call("DELETE", importPath(imp, "?actor=alex"), nil); code != 204 {
			t.Fatalf("%s: cancel: %d %v", c.name, code, out)
		}
	}
	for _, name := range []string{"world.7z", "world.rar", "world.mcworld"} {
		code, out := e.openImportFor("account-6")
		if code != 201 {
			t.Fatalf("open: %d %v", code, out)
		}
		imp := out["id"].(string)
		if code, out := e.announce(imp, name, 1<<10); code != 422 {
			t.Fatalf("announcing %s: %d %v", name, code, out)
		}
		e.call("DELETE", importPath(imp, "?actor=alex"), nil)
	}
	code, out = e.openImportFor("account-6")
	if code != 201 {
		t.Fatalf("open: %d %v", code, out)
	}
	imp = out["id"].(string)
	if code, out := e.announce(imp, "small.zip", 10); code != 201 {
		t.Fatalf("announce: %d %v", code, out)
	}
	if code, out, _ := sendBytes(e.ts.URL, imp, 0, 0, bytes.NewReader(make([]byte, 64<<10))); code != 400 || !strings.Contains(errorOf(out), "larger than announced") {
		t.Fatalf("more bytes than announced: %d %v", code, out)
	}
	if ids := e.serverIDs(); len(ids) != 0 {
		t.Fatalf("a hostile upload made servers %v", ids)
	}
	for _, p := range []string{filepath.Join(e.cfg.StagingDir(), "..", "escape.txt"), filepath.Join(e.cfg.DataDir, "escape.txt")} {
		if exists(p) {
			t.Fatalf("a hostile upload wrote %s", p)
		}
	}
}

// A customer's backup upload for a new server is treated as hostile too:
// paths out of the stage, links, and what isn't a Playkeeper backup are
// refused, and a world far larger than its archive stops at the disk
// limit's room before it's written. None of them stays staged.
func TestACustomersBackupUploadIsTreatedAsHostile(t *testing.T) {
	e := newAgentEnv(t)
	e.setLimits(map[string]any{"id": "account-7", "limitBytes": 1 << 30, "servers": []string{}})
	reg := func(name string, body []byte) ([]tar.Header, [][]byte) {
		return []tar.Header{{Name: name, Typeflag: tar.TypeReg}}, [][]byte{body}
	}
	for _, c := range []struct {
		name, refusal string
		archive       []byte
	}{
		{"a path out of the stage", "unsafe path", hostileTarGz(t, []tar.Header{{Name: "playkeeper-backup/data/../../escape.txt", Typeflag: tar.TypeReg}}, [][]byte{[]byte("out")})},
		{"an absolute path", "unsafe path", hostileTarGz(t, []tar.Header{{Name: "playkeeper-backup/data//etc/cron.d/evil", Typeflag: tar.TypeReg}}, [][]byte{[]byte("* * * * * root sh")})},
		{"a path outside the backup", "outside the backup data directory", hostileTarGz(t, []tar.Header{{Name: "/etc/cron.d/evil", Typeflag: tar.TypeReg}}, [][]byte{[]byte("* * * * * root sh")})},
		{"a link", "links and devices are refused", hostileTarGz(t, []tar.Header{{Name: "playkeeper-backup/data/world", Typeflag: tar.TypeSymlink, Linkname: "/etc"}}, [][]byte{nil})},
		{"a zip", "not a gzip archive", hostileZip(t, map[string][]byte{"playkeeper-backup/data/world/level.dat": []byte("level")})},
		{"a script", "not a gzip archive", []byte("#!/bin/sh\nrm -rf /\n")},
	} {
		code, out := e.uploadTo("/v1/restore/upload?diskLimit=account-7", c.archive)
		if code != 422 || !strings.Contains(errorOf(out), c.refusal) {
			t.Fatalf("%s: %d %v", c.name, code, out)
		}
	}
	e.setLimits(map[string]any{"id": "account-7", "limitBytes": 1 << 20, "servers": []string{}})
	headers, bodies := reg("playkeeper-backup/data/world/region/r.0.0.mca", make([]byte, 64<<20))
	bomb := hostileTarGz(t, headers, bodies)
	if len(bomb) > 1<<20 {
		t.Fatalf("the bomb's archive takes %d bytes", len(bomb))
	}
	if code, out := e.uploadTo("/v1/restore/upload?diskLimit=account-7", bomb); code != 507 || codeOf(out) != api.CodeDiskLimit {
		t.Fatalf("a bomb: %d %v", code, out)
	}
	e.create()
	e.limitTo(e.limitTo(1<<40) + 1<<20)
	if code, out := e.uploadTo(e.sp("/restore/upload"), bomb); code != 507 || !strings.Contains(errorOf(out), "disk limit has left") {
		t.Fatalf("a bomb into a server: %d %v", code, out)
	}
	entries, _ := os.ReadDir(e.cfg.StagingDir())
	for _, en := range entries {
		t.Errorf("a hostile backup left %s staged", en.Name())
	}
	for _, p := range []string{filepath.Join(e.cfg.StagingDir(), "..", "escape.txt"), filepath.Join(e.cfg.DataDir, "escape.txt")} {
		if exists(p) {
			t.Fatalf("a hostile backup wrote %s", p)
		}
	}

	// A backup restored into a server is charged the world it unpacks to,
	// as the restore holds it, and not its archive besides.
	e.limitTo(1 << 40)
	_, raw := e.compressibleBackup()
	used := e.limitTo(1 << 40)
	code, probe := e.uploadTo(e.sp("/restore/upload"), raw)
	if code != 200 {
		t.Fatalf("a backup into a server: %d %v", code, probe)
	}
	f, err := readStageFile(e.a.stageDir(probe["id"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	e.call("DELETE", "/v1/restore/"+probe["id"].(string), nil)
	e.limitTo(used + unpackedBytes(f.Manifest) + int64(len(raw))/2)
	if code, out := e.uploadTo(e.sp("/restore/upload"), raw); code != 200 {
		t.Fatalf("a backup whose world fits into a server's room, but not with its archive: %d %v", code, out)
	}
}
