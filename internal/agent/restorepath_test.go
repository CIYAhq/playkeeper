package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// failPuttingBack makes a restore's swap fail and its undo fail too, the way
// a disk fault does: the restored world can't move into place and the
// previous world can't move back, so the live world folder is missing.
func failPuttingBack(t *testing.T, e *agentEnv, id string) {
	t.Helper()
	live, staged := e.dataDir(), filepath.Join(e.cfg.StagingDir(), id, "data")
	renameDir = func(from, to string) error {
		if to == live && (strings.HasPrefix(from, live+".replaced-") || from == staged) {
			return errors.New("injected rename failure")
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { renameDir = os.Rename })
}

// While a restore that didn't finish leaves the world folder missing, the
// status says where the previous world is, for as long as it takes, and a
// start or a backup is refused with the same words and marked so the
// dashboard drops the failure once the world is back.
func TestAWorldFolderARestoreLeftMissingIsShownUntilItIsBack(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	id, phrase := e.backupAndStage()
	live := e.dataDir()
	failPuttingBack(t, e, id)
	if op := e.applyRestore(id, phrase); op.Status != api.OpFailed {
		t.Fatalf("restore: %+v", op)
	}
	asides, _ := filepath.Glob(live + ".replaced-*")
	if len(asides) != 1 {
		t.Fatalf("want the previous world's copy, got %v", asides)
	}
	m := e.status().WorldMissing
	if m == nil || m.Previous != asides[0] || m.DataDir != live || m.SetAsideAt.IsZero() {
		t.Fatalf("the status must say where the previous world is: %+v", m)
	}
	for _, path := range []string{"/start", "/backups"} {
		code, out := e.call("POST", e.sp(path), map[string]any{"actor": "admin"})
		if code != 202 {
			t.Fatalf("%s: %d %v", path, code, out)
		}
		op := e.waitOp(out["id"].(string))
		if op.Status != api.OpFailed || !strings.Contains(op.Error, "the previous world is at "+asides[0]) ||
			op.Detail["errorKind"] != codeWorldMissing || strings.Contains(op.Hint, "disk") {
			t.Fatalf("%s must be refused for the missing world folder: %+v", path, op)
		}
	}
	if _, err := os.Stat(live); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused start or backup made a world folder: %v", err)
	}
	renameDir = os.Rename
	e.stop()
	e.start()
	if m := e.status().WorldMissing; m != nil {
		t.Fatalf("the previous world is back, but the status still says it's missing: %+v", m)
	}
}
