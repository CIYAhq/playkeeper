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

func (e *agentEnv) applyUpdate(v string) *api.Operation {
	e.t.Helper()
	code, out := e.call("POST", "/v1/update/apply", map[string]any{"version": v, "actor": "admin"})
	if code != 202 {
		e.t.Fatalf("apply: %d %v", code, out)
	}
	return e.waitOp(out["id"].(string))
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
	if op.Status != api.OpSucceeded || op.Phase != "restarting" {
		t.Fatalf("op: %+v", op)
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

	res := update.Result{OpID: op.ID, From: "0.2.0", To: "0.2.1", Outcome: update.OutcomeUpdated, Actor: "admin", FinishedAt: time.Now().UTC()}
	rb, _ := json.Marshal(res)
	os.WriteFile(filepath.Join(dir, update.ResultFile), rb, 0o600)
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
	res := update.Result{OpID: op.ID, From: "0.2.0", To: "0.2.1", Outcome: update.OutcomeRolledBack, Error: "Playkeeper 0.2.1 did not come up healthy: the agent exited", FinishedAt: time.Now().UTC()}
	rb, _ := json.Marshal(res)
	os.WriteFile(filepath.Join(dir, update.ResultFile), rb, 0o600)
	e.waitFor("the rollback to be reported", func() bool {
		o, _ := e.a.loadOperation(op.ID)
		return o.Phase == update.OutcomeRolledBack
	})
	o, _ := e.a.loadOperation(op.ID)
	if o.Status != api.OpFailed || !strings.Contains(o.Error, "0.2.0 was put back and is running") {
		t.Fatalf("a rolled-back update must fail its operation and say what runs: %+v", o)
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
	if code, out := e.call("POST", "/v1/server/stop", map[string]any{"actor": "admin"}); code != 202 {
		t.Fatalf("operations must work again: %d %v", code, out)
	}
}
