package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/update"
	"github.com/CIYAhq/playkeeper/internal/version"
)

// fakeRelease serves a release location: a signed manifest, its signature
// and the tarball.
type fakeRelease struct {
	mu       sync.Mutex
	manifest []byte
	sig      []byte
	tarball  []byte
	binary   []byte
	srv      *httptest.Server
}

func (r *fakeRelease) publish(t *testing.T, priv ed25519.PrivateKey, v string) {
	t.Helper()
	binary := []byte("playkeeper " + v + " binary")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "playkeeper-" + v + "-linux-amd64/playkeeper", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg})
	tw.Write(binary)
	tw.Close()
	gz.Close()
	path := filepath.Join(t.TempDir(), update.TarballFile)
	os.WriteFile(path, buf.Bytes(), 0o644)
	m, err := update.BuildManifest(v, "2026-10-01T00:00:00Z", "- What changed in "+v, path)
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.manifest, r.sig, r.tarball, r.binary = m, update.Sign(priv, m), buf.Bytes(), binary
	r.mu.Unlock()
}

func (r *fakeRelease) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch filepath.Base(req.URL.Path) {
	case update.ManifestFile:
		w.Write(r.manifest)
	case update.SignatureFile:
		w.Write(r.sig)
	case update.TarballFile:
		w.Write(r.tarball)
	default:
		http.NotFound(w, req)
	}
}

// updateEnv is an agent built as release 0.2.0 that trusts a test key and
// finds releases at a local release location.
func updateEnv(t *testing.T) (*agentEnv, *fakeRelease, ed25519.PrivateKey) {
	t.Helper()
	old := version.Version
	version.Version = "0.2.0"
	t.Cleanup(func() { version.Version = old })
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	rel := &fakeRelease{}
	rel.srv = httptest.NewServer(http.HandlerFunc(rel.serve))
	t.Cleanup(rel.srv.Close)
	rel.publish(t, priv, "0.2.1")
	e := newAgentEnv(t)
	e.stop()
	e.cfg.Dev = false
	e.cfg.ReleaseURL = rel.srv.URL
	e.updateKeys = []ed25519.PublicKey{pub}
	e.stagedVersion = "0.2.1"
	e.start()
	return e, rel, priv
}

func (e *agentEnv) updateInfo() api.UpdateInfo {
	e.t.Helper()
	code, out := e.call("POST", "/v1/update/check", map[string]any{"actor": "admin"})
	if code != 200 {
		e.t.Fatalf("check: %d %v", code, out)
	}
	b, _ := json.Marshal(out)
	var info api.UpdateInfo
	json.Unmarshal(b, &info)
	return info
}

// applyUpdate starts an update and waits until the agent's part is done: the
// release is handed to the updater (the operation keeps running) or refused.
func (e *agentEnv) applyUpdate(v string) *api.Operation {
	e.t.Helper()
	code, out := e.call("POST", "/v1/update/apply", map[string]any{"version": v, "actor": "admin"})
	if code != 202 {
		e.t.Fatalf("apply: %d %v", code, out)
	}
	id := out["id"].(string)
	var op *api.Operation
	e.waitFor("the update to be handed to the updater or refused", func() bool {
		if cur := e.a.currentOp(); cur != nil && cur.ID == id {
			return false
		}
		op, _ = e.a.loadOperation(id)
		return op != nil && (op.Status != api.OpRunning || op.Phase == "restarting")
	})
	return op
}

func (e *agentEnv) writeUpdateResult(res update.Result) {
	e.t.Helper()
	rb, _ := json.Marshal(res)
	if err := os.WriteFile(filepath.Join(e.cfg.AgentDir(), "update", update.ResultFile), rb, 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func TestUpdateChecksOnlyTrustSignedNewerReleases(t *testing.T) {
	plain := newAgentEnv(t)
	code, out := plain.call("GET", "/v1/update", nil)
	if code != 200 || out["supported"] != false || !strings.Contains(out["reason"].(string), "signing key") {
		t.Fatalf("a build without a release key cannot update: %d %v", code, out)
	}

	e, rel, _ := updateEnv(t)
	info := e.updateInfo()
	if !info.Supported || !info.Available || info.Latest != "0.2.1" || info.Notes != "- What changed in 0.2.1" || info.CheckError != "" {
		t.Fatalf("a signed newer release must be offered with its notes: %+v", info)
	}
	if st := e.status(); st.UpdateAvailable != "0.2.1" {
		t.Fatalf("the status must say an update is available: %q", st.UpdateAvailable)
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	rel.publish(t, other, "0.2.2")
	if info := e.updateInfo(); info.Latest != "0.2.1" || !strings.Contains(info.CheckError, "not signed") {
		t.Fatalf("a release signed by another key must not be offered: %+v", info)
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	e.stop()
	e.updateKeys = []ed25519.PublicKey{priv.Public().(ed25519.PublicKey)}
	e.start()
	rel.publish(t, priv, "0.1.9")
	if info := e.updateInfo(); info.Available || info.Latest != "0.1.9" {
		t.Fatalf("an older release is never offered: %+v", info)
	}
}

func TestUpdateIsVerifiedStagedAndHandedToTheUpdater(t *testing.T) {
	e, rel, _ := updateEnv(t)
	e.create()
	op := e.applyUpdate("0.2.1")
	if op.Status != api.OpRunning || op.Phase != "restarting" || op.FinishedAt != nil {
		t.Fatalf("an update handed to the updater must keep running until the updater reports: %+v", op)
	}
	dir := filepath.Join(e.cfg.AgentDir(), "update")
	if b, _ := os.ReadFile(filepath.Join(dir, update.StagedDir, update.BinaryFile)); !bytes.Equal(b, rel.binary) {
		t.Fatal("the verified binary must be staged")
	}
	for _, f := range []string{update.ManifestFile, update.SignatureFile} {
		if _, err := os.Stat(filepath.Join(dir, update.StagedDir, f)); err != nil {
			t.Errorf("%s must be staged for the updater to check again: %v", f, err)
		}
	}
	var req update.Request
	b, _ := os.ReadFile(filepath.Join(dir, update.RequestFile))
	if json.Unmarshal(b, &req) != nil || req.OpID != op.ID || req.Version != "0.2.1" || req.From != "0.2.0" || req.Actor != "admin" {
		t.Fatalf("request: %s", b)
	}
	if code, out := e.call("POST", "/v1/server/stop", map[string]any{"actor": "admin"}); code != 409 || !strings.Contains(out["error"].(string), "installing update 0.2.1") {
		t.Fatalf("nothing else may run while the update is installed: %d %v", code, out)
	}
	if e.status().UpdateInstalling != "0.2.1" {
		t.Fatal("the status must show the update being installed")
	}

	e.writeUpdateResult(update.Result{OpID: op.ID, From: "0.2.0", To: "0.2.1", Outcome: update.OutcomeUpdated, Actor: "admin", FinishedAt: time.Now().UTC()})
	e.waitFor("the result to be reported", func() bool {
		o, _ := e.a.loadOperation(op.ID)
		return o.Phase == update.OutcomeUpdated
	})
	if o, _ := e.a.loadOperation(op.ID); o.Status != api.OpSucceeded || o.Error != "" {
		t.Fatalf("op: %+v", o)
	}
	code, out := e.call("GET", "/v1/update", nil)
	last, _ := out["lastResult"].(map[string]any)
	if code != 200 || last["outcome"] != update.OutcomeUpdated || out["installing"] != nil {
		t.Fatalf("update info: %v", out)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'update.updated'`); n != 1 {
		t.Fatalf("the update must be audited, got %d", n)
	}
	if _, err := os.Stat(filepath.Join(dir, update.ResultFile)); err == nil {
		t.Fatal("the result is reported once")
	}
}

func TestAnUpdateKeepsRunningAcrossAgentRestartsUntilTheUpdaterReports(t *testing.T) {
	e, _, _ := updateEnv(t)
	e.create()
	op := e.applyUpdate("0.2.1")
	dir := filepath.Join(e.cfg.AgentDir(), "update")
	// The updater takes the request and restarts the agent.
	e.stop()
	if err := os.Rename(filepath.Join(dir, update.RequestFile), filepath.Join(dir, update.ApplyingFile)); err != nil {
		t.Fatal(err)
	}
	e.start()
	if o, _ := e.a.loadOperation(op.ID); o.Status != api.OpRunning {
		t.Fatalf("an update the updater is still installing must not be marked interrupted: %+v", o)
	}
	if e.status().UpdateInstalling != "0.2.1" {
		t.Fatal("the restarted agent must know the update is still being installed")
	}
	// The updater reports while the agent is down; the agent records it as it starts.
	e.stop()
	e.writeUpdateResult(update.Result{OpID: op.ID, From: "0.2.0", To: "0.2.1", Outcome: update.OutcomeUpdated, Actor: "admin", FinishedAt: time.Now().UTC()})
	os.Remove(filepath.Join(dir, update.ApplyingFile))
	e.start()
	if o, _ := e.a.loadOperation(op.ID); o.Status != api.OpSucceeded || o.Phase != update.OutcomeUpdated {
		t.Fatalf("the updater's result must be recorded, not an interruption: %+v", o)
	}
}

func TestUpdateRefusesDownloadsThatDoNotMatchAndStaleChoices(t *testing.T) {
	e, rel, priv := updateEnv(t)
	dir := filepath.Join(e.cfg.AgentDir(), "update")
	rel.mu.Lock()
	rel.tarball = append([]byte(nil), rel.tarball...)
	rel.tarball[len(rel.tarball)/2] ^= 0xff
	rel.mu.Unlock()
	if op := e.applyUpdate("0.2.1"); op.Status != api.OpFailed || !strings.Contains(op.Error, "could not be downloaded safely") {
		t.Fatalf("a changed tarball must be refused: %+v", op)
	}
	if _, err := os.Stat(filepath.Join(dir, update.RequestFile)); err == nil {
		t.Fatal("nothing may be handed to the updater")
	}
	if _, err := os.Stat(filepath.Join(dir, update.StagedDir)); err == nil {
		t.Fatal("a refused download must not stay staged")
	}
	rel.publish(t, priv, "0.2.1")
	if op := e.applyUpdate("0.2.2"); op.Status != api.OpFailed || !strings.Contains(op.Error, "latest release is now 0.2.1") {
		t.Fatalf("only the release the admin reviewed may be installed: %+v", op)
	}
	e.stagedVersion = "6.6.6"
	if op := e.applyUpdate("0.2.1"); op.Status != api.OpFailed || !strings.Contains(op.Error, "reports version") {
		t.Fatalf("a binary reporting another version must be refused: %+v", op)
	}
	e.stagedVersion = "0.2.1"
	rel.publish(t, priv, "0.1.9")
	if op := e.applyUpdate("0.1.9"); op.Status != api.OpFailed || !strings.Contains(op.Error, "never goes back") {
		t.Fatalf("an older release must be refused: %+v", op)
	}
}

func TestFailedUpdatesAreReportedAndDoNotBlockTheDashboard(t *testing.T) {
	e, _, _ := updateEnv(t)
	e.create()
	dir := filepath.Join(e.cfg.AgentDir(), "update")
	op := e.applyUpdate("0.2.1")
	e.writeUpdateResult(update.Result{OpID: op.ID, From: "0.2.0", To: "0.2.1", Outcome: update.OutcomeRolledBack, Error: "wait until Playkeeper 0.2.1 is healthy: no healthy answer in time (last error: the agent exited)", FinishedAt: time.Now().UTC()})
	e.waitFor("the rollback to be reported", func() bool {
		o, _ := e.a.loadOperation(op.ID)
		return o.Phase == update.OutcomeRolledBack
	})
	o, _ := e.a.loadOperation(op.ID)
	if o.Status != api.OpFailed || !strings.Contains(o.Error, "0.2.0 was put back and is running") || !strings.Contains(o.Error, "What went wrong: wait until Playkeeper 0.2.1 is healthy") {
		t.Fatalf("a rolled-back update must fail its operation and say what runs and why: %+v", o)
	}

	// The updater never starts (for example, its trigger is disabled).
	op = e.applyUpdate("0.2.1")
	e.a.upd.mu.Lock()
	e.a.upd.since = e.a.now().Add(-updaterStartTimeout - time.Second)
	e.a.upd.mu.Unlock()
	e.waitFor("the handoff to time out", func() bool { return e.a.installingUpdate() == "" })
	if o, _ := e.a.loadOperation(op.ID); o.Status != api.OpFailed || !strings.Contains(o.Error, "updater did not start") || !strings.Contains(o.Hint, "playkeeper-update.path") {
		t.Fatalf("op: %+v", o)
	}
	if _, err := os.Stat(filepath.Join(dir, update.RequestFile)); err == nil {
		t.Fatal("the unpicked request must be withdrawn")
	}

	// The updater takes the request but never reports.
	op = e.applyUpdate("0.2.1")
	if err := os.Rename(filepath.Join(dir, update.RequestFile), filepath.Join(dir, update.ApplyingFile)); err != nil {
		t.Fatal(err)
	}
	e.a.upd.mu.Lock()
	e.a.upd.since = e.a.now().Add(-updaterRunTimeout - time.Second)
	e.a.upd.mu.Unlock()
	e.waitFor("the silent updater to be given up on", func() bool { return e.a.installingUpdate() == "" })
	if o, _ := e.a.loadOperation(op.ID); o.Status != api.OpFailed || o.Phase != update.OutcomeFailed || !strings.Contains(o.Error, "has not said how the update to Playkeeper 0.2.1 ended") || !strings.Contains(o.Hint, "journalctl -u playkeeper-update") {
		t.Fatalf("an update the updater never reported on must not look successful: %+v", o)
	}
	code, out := e.call("GET", "/v1/update", nil)
	if last, _ := out["lastResult"].(map[string]any); code != 200 || last["outcome"] != update.OutcomeFailed || last["to"] != "0.2.1" {
		t.Fatalf("Settings must show that the update did not finish: %v", out)
	}

	// Its applying.json is still there; a restart must not wait for it again.
	e.stop()
	e.start()
	if v := e.a.installingUpdate(); v != "" {
		t.Fatalf("an update the agent gave up on must not block the dashboard again after a restart (installing %s)", v)
	}
	os.Remove(filepath.Join(dir, update.ApplyingFile))

	// Nor does a restart start the handoff timeouts over.
	op = e.applyUpdate("0.2.1")
	applying := filepath.Join(dir, update.ApplyingFile)
	if err := os.Rename(filepath.Join(dir, update.RequestFile), applying); err != nil {
		t.Fatal(err)
	}
	longAgo := time.Now().Add(-updaterRunTimeout - time.Minute)
	os.Chtimes(applying, longAgo, longAgo)
	e.stop()
	e.start()
	e.waitFor("an update handed over long ago to be given up on right away", func() bool { return e.a.installingUpdate() == "" })
	if o, _ := e.a.loadOperation(op.ID); o.Status != api.OpFailed || o.Phase != update.OutcomeFailed {
		t.Fatalf("op: %+v", o)
	}
	os.Remove(applying)

	if code, out := e.call("POST", "/v1/server/stop", map[string]any{"actor": "admin"}); code != 202 {
		t.Fatalf("operations must work again: %d %v", code, out)
	}
}
