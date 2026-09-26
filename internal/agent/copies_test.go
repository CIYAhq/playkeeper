package agent

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/diskusage"
	"github.com/CIYAhq/playkeeper/internal/offsite"
)

// fetchDest is a destination whose downloads each step of a test decides.
type fetchDest struct {
	fakeDest
	dl       sync.Mutex
	download func(context.Context, offsite.Download) (offsite.Archive, error)
}

func (d *fetchDest) Download(ctx context.Context, dl offsite.Download) (offsite.Archive, error) {
	d.dl.Lock()
	fn := d.download
	d.dl.Unlock()
	return fn(ctx, dl)
}

func (d *fetchDest) answer(fn func(context.Context, offsite.Download) (offsite.Archive, error)) {
	d.dl.Lock()
	d.download = fn
	d.dl.Unlock()
}

func failWith(err error) func(context.Context, offsite.Download) (offsite.Archive, error) {
	return func(context.Context, offsite.Download) (offsite.Archive, error) { return offsite.Archive{}, err }
}

// fromBackup answers a download with the backup at path, as a copy that
// decrypted and matched its record.
func fromBackup(path string) func(context.Context, offsite.Download) (offsite.Archive, error) {
	return func(_ context.Context, dl offsite.Download) (offsite.Archive, error) {
		archive := strings.TrimSuffix(dl.Name, ".age")
		in, err := os.Open(path)
		if err != nil {
			return offsite.Archive{}, err
		}
		defer in.Close()
		out := filepath.Join(dl.Dir, archive)
		f, err := os.Create(out)
		if err != nil {
			return offsite.Archive{}, err
		}
		defer f.Close()
		if _, err := io.Copy(f, in); err != nil {
			return offsite.Archive{}, err
		}
		return offsite.Archive{Name: archive, Path: out, Matched: dl.ArchiveSHA256 != ""}, nil
	}
}

// withCopies starts a server with one backup, copied to dest.
func withCopies(t *testing.T, dest offsiteDest) (e *agentEnv, backupID, file string) {
	t.Helper()
	prev := openOffsite
	openOffsite = func(offsite.Config, offsite.Keys, offsite.Options) (offsiteDest, error) { return dest, nil }
	t.Cleanup(func() { openOffsite = prev })
	e = newAgentEnv(t)
	e.create()
	backupID = e.backup()
	if err := e.a.db.QueryRow(`SELECT file_name FROM backups WHERE id = ?`, backupID).Scan(&file); err != nil {
		t.Fatal(err)
	}
	s3 := map[string]any{"provider": "minio", "endpoint": "203.0.113.10:9000", "bucket": "worlds", "accessKeyId": "PKEXAMPLE"}
	code, out := e.call("POST", e.sp("/offsite"), map[string]any{"actor": "admin", "enabled": true, "config": map[string]any{"type": "s3", "s3": s3}, "secretKey": "wJalrXUtnFEMI-example-secret"})
	if code != http.StatusOK {
		t.Fatalf("turn copies on: %d %v", code, out)
	}
	e.waitFor("the copy", func() bool {
		return e.countRows(`SELECT COUNT(*) FROM offsite_copies WHERE backup_id = ?`, backupID) == 1
	})
	return e, backupID, file
}

func (e *agentEnv) staged() []string {
	entries, _ := os.ReadDir(e.a.cfg.StagingDir())
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	return names
}

func TestACopyIsCheckedAgainOrDeletedFromItsRow(t *testing.T) {
	dest := &fetchDest{fakeDest: fakeDest{stored: map[string]offsite.Copy{}}}
	e, backupID, file := withCopies(t, dest)
	name := offsite.CopyName(file)
	var sum string
	if err := e.a.db.QueryRow(`SELECT sha256 FROM backups WHERE id = ?`, backupID).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	row := func() map[string]any {
		t.Helper()
		code, out := e.call("GET", e.sp("/offsite/copies"), nil)
		if code != http.StatusOK {
			t.Fatalf("copies: %d %v", code, out)
		}
		for _, c := range out["copies"].([]any) {
			if c := c.(map[string]any); c["name"] == name {
				return c
			}
		}
		return nil
	}
	check := func() *api.Operation {
		t.Helper()
		code, out := e.call("POST", e.sp("/offsite/copies/"+url.PathEscape(name)+"/check"), map[string]any{"actor": "admin"})
		if code != http.StatusAccepted {
			t.Fatalf("check: %d %v", code, out)
		}
		return e.waitOp(out["id"].(string))
	}
	if c := row(); c == nil || c["sha256"] != sum || c["checkError"] != nil || c["checked"] != offsite.CheckedSize {
		t.Fatalf("the copy's row: %v", c)
	}

	const damaged = "The copy doesn't match the backup it was made from."
	dest.answer(failWith(&offsite.Error{Kind: offsite.KindVerifyFailed, Msg: damaged}))
	if op := check(); op.Status != api.OpFailed || op.Kind != "offsite-check" || op.Detail["name"] != name {
		t.Fatalf("checking a damaged copy: %+v", op)
	}
	if c := row(); c["checkError"] != damaged || c["checked"] != offsite.CheckedSize {
		t.Fatalf("a damaged copy's row: %v", c)
	}

	// Not reaching the storage says nothing about the copy itself.
	dest.answer(failWith(&offsite.Error{Kind: offsite.KindNetwork, Msg: "Couldn't reach the storage.", Retry: true}))
	if op := check(); op.Status != api.OpFailed || op.Error != "Couldn't reach the storage." {
		t.Fatalf("checking without the storage: %+v", op)
	}
	if c := row(); c["checkError"] != damaged {
		t.Fatalf("a check that never reached the copy changed its row: %v", c)
	}

	dest.answer(fromBackup(e.a.backupPath(file)))
	if op := check(); op.Status != api.OpSucceeded {
		t.Fatalf("checking a good copy: %+v", op)
	}
	if c := row(); c["checked"] != offsite.CheckedDecrypted || c["checkError"] != nil {
		t.Fatalf("a good copy's row: %v", c)
	}
	if left := e.staged(); len(left) != 0 {
		t.Fatalf("checks left %v behind", left)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'offsite.copy_checked' AND actor = 'admin' AND target = ?`, backupID); n != 2 {
		t.Fatalf("audited %d checks of the copy, want the damaged one and the good one", n)
	}

	copyPath := e.sp("/offsite/copies/" + url.PathEscape(name))
	if code, _ := e.call("DELETE", copyPath, nil); code != http.StatusBadRequest {
		t.Fatalf("deleting a copy with nobody named: %d", code)
	}
	if code, _ := e.call("DELETE", e.sp("/offsite/copies/"+url.PathEscape(file))+"?actor=admin", nil); code != http.StatusBadRequest {
		t.Fatalf("deleting something that isn't a copy: %d", code)
	}
	code, out := e.call("DELETE", copyPath+"?actor=admin", nil)
	if code != http.StatusOK || out["deleted"] != name {
		t.Fatalf("delete: %d %v", code, out)
	}
	dest.mu.Lock()
	deleted := append([]string(nil), dest.deleted...)
	dest.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != name {
		t.Fatalf("deleted %q where copies are kept, want %s", deleted, name)
	}
	if c := row(); c != nil {
		t.Fatalf("the deleted copy is still listed: %v", c)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'offsite.copy_deleted' AND actor = 'admin' AND target = ? AND result = 'succeeded'`, backupID); n != 1 {
		t.Fatalf("audited the deletion %d times", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM backups WHERE id = ?`, backupID); n != 1 {
		t.Fatal("deleting the copy deleted the backup on this machine")
	}
	if code, _ := e.call("DELETE", copyPath+"?actor=admin", nil); code != http.StatusNotFound {
		t.Fatalf("deleting it again: %d", code)
	}
	if code, _ := e.call("POST", copyPath+"/check", map[string]any{"actor": "admin"}); code != http.StatusNotFound {
		t.Fatalf("checking a deleted copy: %d", code)
	}
}

func TestCancellingARestoreFromACopyLeavesTheServerAsItWas(t *testing.T) {
	dest := &fetchDest{fakeDest: fakeDest{stored: map[string]offsite.Copy{}}}
	e, _, file := withCopies(t, dest)
	name := offsite.CopyName(file)
	e.waitFor("the server to be online and idle", e.onlineIdle)
	container := func() string {
		e.fd.mu.Lock()
		defer e.fd.mu.Unlock()
		c := e.fd.byName[e.cname()]
		return fmt.Sprintf("%s running=%v started=%v", c.id, c.running, c.started)
	}
	files, box, backups := tree(t, e.dataDir()), container(), e.countRows(`SELECT COUNT(*) FROM backups`)

	// The download stops halfway, leaving what it fetched so far.
	downloading := make(chan string, 1)
	dest.answer(func(ctx context.Context, dl offsite.Download) (offsite.Archive, error) {
		part := filepath.Join(dl.Dir, dl.Name+".part")
		if err := os.WriteFile(part, make([]byte, 1<<20), 0o600); err != nil {
			return offsite.Archive{}, err
		}
		downloading <- part
		<-ctx.Done()
		return offsite.Archive{}, &offsite.Error{Kind: offsite.KindCanceled, Msg: "The download stopped."}
	})
	code, out := e.call("POST", e.sp("/offsite/restore"), map[string]any{"actor": "admin", "name": name})
	if code != http.StatusAccepted {
		t.Fatalf("restore: %d %v", code, out)
	}
	id := out["id"].(string)
	var part string
	select {
	case part = <-downloading:
	case <-time.After(10 * time.Second):
		t.Fatal("the download never started")
	}

	if code, _ := e.call("POST", e.sp("/offsite/restore/cancel"), map[string]any{"actor": "admin", "operationId": "qrstuvwxyz"}); code != http.StatusConflict {
		t.Fatalf("cancelling an operation that isn't running: %d", code)
	}
	if code, _ := e.call("POST", e.sp("/offsite/restore/cancel"), map[string]any{"operationId": id}); code != http.StatusBadRequest {
		t.Fatalf("cancelling with nobody named: %d", code)
	}
	code, out = e.call("POST", e.sp("/offsite/restore/cancel"), map[string]any{"actor": "admin", "operationId": id})
	if code != http.StatusAccepted || out["id"] != id {
		t.Fatalf("cancel: %d %v", code, out)
	}
	if op := e.waitOp(id); op.Status != api.OpCancelled || op.Error != "" || op.Detail["restoreId"] != nil {
		t.Fatalf("the cancelled restore: %+v", op)
	}

	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf("the partial download is still there: %v", err)
	}
	if left := e.staged(); len(left) != 0 {
		t.Fatalf("staging still holds %v", left)
	}
	if !maps.Equal(tree(t, e.dataDir()), files) {
		t.Fatal("cancelling touched the server's files")
	}
	if got := container(); got != box {
		t.Fatalf("the container went from %s to %s", box, got)
	}
	if st := e.status(); st.Phase != api.PhaseOnline || st.Operation != nil {
		t.Fatalf("after cancelling: %s, %+v", st.Phase, st.Operation)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM backups`); n != backups {
		t.Fatalf("%d backups, there were %d", n, backups)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'offsite.restore_cancelled' AND actor = 'admin' AND detail = ?`, name); n != 1 {
		t.Fatalf("audited the cancel %d times", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'offsite-restore' AND result = 'cancelled'`); n != 1 {
		t.Fatalf("audited %d cancelled restores", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'restore.staged'`); n != 0 {
		t.Fatalf("audited %d staged restores", n)
	}

	// It left no swap journal, so the next start has nothing to finish or undo.
	e.stop()
	e.start()
	e.waitFor("the server to be online and idle", e.onlineIdle)
	if op := e.waitOp(id); op.Status != api.OpCancelled || op.Error != "" {
		t.Fatalf("after a restart, the cancelled restore: %+v", op)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'restore'`); n != 0 {
		t.Fatalf("the restart ran %d restores", n)
	}
	if previous, failed := restoreCopies(e.dataDir()); len(previous)+len(failed) != 0 || !maps.Equal(tree(t, e.dataDir()), files) || container() != box {
		t.Fatalf("the restart after cancelling touched the server (world copies %v %v)", previous, failed)
	}

	// The next restore runs to the end; once it has, there's nothing to cancel.
	dest.answer(fromBackup(e.a.backupPath(file)))
	code, out = e.call("POST", e.sp("/offsite/restore"), map[string]any{"actor": "admin", "name": name})
	if code != http.StatusAccepted {
		t.Fatalf("restore again: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded || op.Detail["restoreId"] == nil {
		t.Fatalf("the second restore: %+v", op)
	}
	if code, _ := e.call("POST", e.sp("/offsite/restore/cancel"), map[string]any{"actor": "admin", "operationId": op.ID}); code != http.StatusConflict {
		t.Fatalf("cancelling a finished restore: %d", code)
	}
}

// A restore from a copy that the agent stops in while it downloads is settled
// at the next start: it staged nothing and wrote no swap journal, so the
// start records it as interrupted and deletes the download, even one a crash
// left. Restoring the copy again then goes the way every restore goes, the
// rollback archive first.
func TestARestoreFromACopyTheAgentStoppedInIsSettledAtTheNextStart(t *testing.T) {
	dest := &fetchDest{fakeDest: fakeDest{stored: map[string]offsite.Copy{}}}
	e, _, file := withCopies(t, dest)
	name := offsite.CopyName(file)
	e.waitFor("the server to be online and idle", e.onlineIdle)
	restored := worldHash(t, e.dataDir())
	if err := os.WriteFile(filepath.Join(e.dataDir(), "world", "later.dat"), []byte("built after the backup"), 0o640); err != nil {
		t.Fatal(err)
	}
	files := tree(t, e.dataDir())

	downloading := make(chan string, 1)
	dest.answer(func(ctx context.Context, dl offsite.Download) (offsite.Archive, error) {
		if err := os.WriteFile(filepath.Join(dl.Dir, dl.Name+".part"), make([]byte, 1<<20), 0o600); err != nil {
			return offsite.Archive{}, err
		}
		downloading <- dl.Dir
		<-ctx.Done()
		return offsite.Archive{}, &offsite.Error{Kind: offsite.KindCanceled, Msg: "The download stopped."}
	})
	code, out := e.call("POST", e.sp("/offsite/restore"), map[string]any{"actor": "admin", "name": name})
	if code != http.StatusAccepted {
		t.Fatalf("restore: %d %v", code, out)
	}
	id := out["id"].(string)
	var dir string
	select {
	case dir = <-downloading:
	case <-time.After(10 * time.Second):
		t.Fatal("the download never started")
	}
	e.stop()
	if op := e.opAtRest(id); op.Status != api.OpRunning {
		t.Fatalf("the agent stopped during the download, and the restore was recorded as %s: %+v", op.Status, op)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".part"), make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}

	e.start()
	op := e.waitOp(id)
	if op.Status != api.OpFailed || op.Error != interruptedDownload || op.Hint != interruptedDownloadHint || op.Detail["restoreId"] != nil {
		t.Fatalf("the interrupted restore at the next start: %+v", op)
	}
	if left := e.staged(); len(left) != 0 {
		t.Fatalf("staging still holds %v", left)
	}
	if !maps.Equal(tree(t, e.dataDir()), files) {
		t.Fatal("the interrupted restore changed the server's files")
	}
	if previous, failed := restoreCopies(e.dataDir()); len(previous)+len(failed) != 0 || e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'restore'`) != 0 {
		t.Fatalf("the next start restored something (world copies %v %v)", previous, failed)
	}

	dest.answer(fromBackup(e.a.backupPath(file)))
	code, out = e.call("POST", e.sp("/offsite/restore"), map[string]any{"actor": "admin", "name": name})
	if code != http.StatusAccepted {
		t.Fatalf("restore again: %d %v", code, out)
	}
	fetched := e.waitOp(out["id"].(string))
	stage, _ := fetched.Detail["restoreId"].(string)
	if fetched.Status != api.OpSucceeded || stage == "" {
		t.Fatalf("fetching the copy again: %+v", fetched)
	}
	code, preview := e.call("GET", "/v1/restore/"+stage, nil)
	if code != http.StatusOK {
		t.Fatalf("preview: %d %v", code, preview)
	}
	applied := e.applyRestore(stage, preview["confirmPhrase"].(string))
	rollback, _ := applied.Detail["rollbackBackupId"].(string)
	if applied.Status != api.OpSucceeded || rollback == "" || e.countRows(`SELECT COUNT(*) FROM backups WHERE id = ? AND kind = 'rollback'`, rollback) != 1 {
		t.Fatalf("the restore of the copy: %+v", applied)
	}
	if worldHash(t, e.dataDir()) != restored {
		t.Fatal("the copy's world was not restored")
	}
	if left := e.staged(); len(left) != 0 {
		t.Fatalf("the restore left %v in staging", left)
	}
}

// neededBy is the unfinished restore or update the rules keep a backup on
// this machine for, if any.
func neededBy(t *testing.T, s *server, id string) string {
	t.Helper()
	res, err := s.retentionPlan()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.OnHost.Decisions {
		for _, r := range d.Reasons {
			if d.ID == id && r.Code == "needed" {
				return r.Params["by"]
			}
		}
	}
	return ""
}

// serverBusy says whether the Disk space page counts the server as busy.
func serverBusy(t *testing.T, l diskusage.Layout, id string) bool {
	t.Helper()
	for _, sv := range l.Servers {
		if sv.ID == id {
			return sv.Busy
		}
	}
	t.Fatalf("the layout has no server %s", id)
	return false
}

// diskOffered is the paths the Disk space page offers to delete.
func (e *agentEnv) diskOffered() map[string]bool {
	e.t.Helper()
	rep, err := diskusage.Scan(context.Background(), e.a.diskLayout(context.Background()), e.a.diskOptions(time.UTC))
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]bool{}
	for _, c := range rep.Candidates {
		out[c.Path] = true
	}
	return out
}

// Until a restore is over, whether it is running or its stage keeps the swap
// journal of a swap it couldn't settle, the rules keep its rollback archive
// and the Disk space page offers nothing of its server, nor the stage.
func TestARestoreThatIsNotOverKeepsItsRollbackArchiveAndStage(t *testing.T) {
	e := newAgentEnv(t)
	id, phrase, _, _ := e.restoreScenario()
	s := e.srv()
	busy := func(l diskusage.Layout) bool { return serverBusy(t, l, s.id) }

	reached, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	let := func() { releaseOnce.Do(func() { close(release) }) }
	// A failed check must not leave the restore waiting while the agent stops.
	t.Cleanup(let)
	setRestoreStep(t, func(_ context.Context, step string) {
		if step == "checking" {
			close(reached)
			<-release
		}
	})
	opID := e.startRestore(id, phrase)
	waitClosed(t, reached, "the restored world to be in place")
	rollback, _ := e.a.currentOp().Detail["rollbackBackupId"].(string)
	if rollback == "" {
		t.Fatal("the restore recorded no rollback archive")
	}
	if by := neededBy(t, s, rollback); by != "restore" {
		t.Fatalf("while the restore runs, the rules keep its rollback archive for %q", by)
	}
	if l := e.a.diskLayout(context.Background()); !busy(l) || !slices.Equal(l.ActiveStages, []string{id}) {
		t.Fatalf("while the restore runs: busy %v, active stages %v", busy(l), l.ActiveStages)
	}
	let()
	if op := e.waitOp(opID); op.Status != api.OpSucceeded {
		t.Fatalf("the restore: %+v", op)
	}
	if by := neededBy(t, s, rollback); by != "" {
		t.Fatalf("once the restore is kept, the rules still keep its rollback archive for %q", by)
	}

	// A restore that couldn't put the previous world back keeps its stage and
	// journal, and the previous world's copy, until the next start settles it.
	stage := "0123456789abcdef"
	dir := e.a.stageDir(stage)
	stamp := time.Now().UTC().Add(-48 * time.Hour).Format("20060102-150405")
	aside := filepath.Join(s.dir(), "data.replaced-"+stamp)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(aside, "world"), 0o750); err != nil {
		t.Fatal(err)
	}
	sc, _ := s.serverConfig()
	j := &swapJournal{ServerID: s.id, OpID: opID, Actor: "admin", Aside: filepath.Base(aside), Failed: "data.failed-restore-" + stamp, HadLive: true,
		StartedAt: time.Now().Add(-48 * time.Hour), Previous: sc, Restored: *sc, State: swapReverting}
	if err := writeSwapJournal(dir, j); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	age := func() {
		t.Helper()
		for _, p := range []string{filepath.Join(dir, swapJournalFile), dir} {
			if err := os.Chtimes(p, old, old); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
	}
	age()
	offered := func() (stageOffered, copyOffered bool) {
		t.Helper()
		o := e.diskOffered()
		return o[dir], o[aside]
	}
	if by := neededBy(t, s, rollback); by != "restore" {
		t.Fatalf("with its journal left, the rules keep the rollback archive for %q", by)
	}
	if l := e.a.diskLayout(context.Background()); !busy(l) || !slices.Equal(l.ActiveStages, []string{stage}) {
		t.Fatalf("with its journal left: busy %v, active stages %v", busy(l), l.ActiveStages)
	}
	if st, cp := offered(); st || cp {
		t.Fatalf("the Disk space page offers the unsettled restore's stage (%v) or the previous world's copy (%v)", st, cp)
	}

	if err := os.Remove(filepath.Join(dir, swapJournalFile)); err != nil {
		t.Fatal(err)
	}
	age()
	if by := neededBy(t, s, rollback); by != "" {
		t.Fatalf("without the journal, the rules keep the rollback archive for %q", by)
	}
	if st, cp := offered(); !st || !cp {
		t.Fatalf("once nothing needs them, the stage (%v) and the copy (%v) are offered", st, cp)
	}
}

// A swap journal that can't be read may be any server's, and one whose
// restore is no longer on record may be any of its server's restores. Until
// it is settled, the rules keep every rollback archive it may need, the Disk
// space page counts every server it may be as busy and offers neither the
// stage nor a set-aside world, and the agent logs, once, a journal it can't
// read.
func TestAnUnreadableSwapJournalKeepsWhatAnyRestoreMayNeed(t *testing.T) {
	e := newAgentEnv(t)
	id, phrase, _, _ := e.restoreScenario()
	s := e.srv()
	op := e.waitOp(e.startRestore(id, phrase))
	rollback, _ := op.Detail["rollbackBackupId"].(string)
	if op.Status != api.OpSucceeded || rollback == "" {
		t.Fatalf("the restore: %+v", op)
	}
	e.createWith(map[string]any{"name": "Creative"})
	other := e.srv()

	old := time.Now().Add(-48 * time.Hour)
	stamp := old.UTC().Format("20060102-150405")
	aside := filepath.Join(s.dir(), "data.replaced-"+stamp)
	if err := os.MkdirAll(filepath.Join(aside, "world"), 0o750); err != nil {
		t.Fatal(err)
	}
	stage := "0123456789abcdef"
	dir := e.a.stageDir(stage)
	journal := filepath.Join(dir, swapJournalFile)
	age := func() {
		t.Helper()
		for _, p := range []string{journal, dir} {
			if err := os.Chtimes(p, old, old); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
	}
	sc, _ := s.serverConfig()
	for _, c := range []struct {
		name  string
		write func() error
		// unreadable is a journal that may be any server's.
		unreadable bool
	}{
		{"cut short", func() error { return os.WriteFile(journal, []byte(`{"serverId":"`+s.id+`","opId":`), 0o600) }, true},
		{"not a file", func() error { return os.Mkdir(journal, 0o700) }, true},
		{"for a restore no longer on record", func() error {
			return writeSwapJournal(dir, &swapJournal{ServerID: s.id, OpID: "0000000000000000", Actor: "admin", Aside: filepath.Base(aside),
				Failed: "data.failed-restore-" + stamp, HadLive: true, StartedAt: old, Previous: sc, Restored: *sc, State: swapReverting})
		}, false},
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := c.write(); err != nil {
			t.Fatal(err)
		}
		age()
		logged := strings.Count(e.warnings.String(), "stage="+stage)
		if by := neededBy(t, s, rollback); by != "restore" {
			t.Fatalf("%s: the rules keep the rollback archive for %q", c.name, by)
		}
		l := e.a.diskLayout(context.Background())
		if !serverBusy(t, l, s.id) || serverBusy(t, l, other.id) != c.unreadable || !slices.Equal(l.ActiveStages, []string{stage}) {
			t.Fatalf("%s: busy %v, %s busy %v, active stages %v", c.name, serverBusy(t, l, s.id), other.name(), serverBusy(t, l, other.id), l.ActiveStages)
		}
		if o := e.diskOffered(); o[dir] || o[aside] {
			t.Fatalf("%s: the Disk space page offers the stage (%v) or the set-aside world (%v)", c.name, o[dir], o[aside])
		}
		want := 0
		if c.unreadable {
			want = 1
		}
		if n := strings.Count(e.warnings.String(), "stage="+stage) - logged; n != want {
			t.Fatalf("%s: the agent logged the journal %d times, not %d:\n%s", c.name, n, want, e.warnings.String())
		}
		if err := os.RemoveAll(journal); err != nil {
			t.Fatal(err)
		}
	}

	age()
	if by := neededBy(t, s, rollback); by != "" {
		t.Fatalf("without the journal, the rules keep the rollback archive for %q", by)
	}
	if l := e.a.diskLayout(context.Background()); serverBusy(t, l, s.id) || serverBusy(t, l, other.id) {
		t.Fatal("without the journal, a server still counts as busy")
	}
	if o := e.diskOffered(); !o[dir] || !o[aside] {
		t.Fatalf("once nothing needs them, the stage (%v) and the set-aside world (%v) are offered", o[dir], o[aside])
	}
}

// While a restore of a server isn't over, a stage keeping a swap journal that
// may be the server's, the World tab discards none of the server's world
// copies: the restore may still put one back. A journal that can't be read
// may be any server's; a server no journal is about can discard.
func TestTheWorldTabKeepsTheWorldCopiesOfARestoreThatIsNotOver(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	s := e.srv()
	e.createWith(map[string]any{"name": "Creative"})
	other := e.srv()

	old := time.Now().Add(-48 * time.Hour)
	stamp := old.UTC().Format("20060102-150405")
	name := "data.replaced-" + stamp
	discard := func(sv *server) (int, map[string]any) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(sv.dir(), name, "world"), 0o750); err != nil {
			t.Fatal(err)
		}
		return e.call("DELETE", "/v1/servers/"+sv.id+"/world-copies/"+name+"?actor=admin", nil)
	}
	refused := func(what string, sv *server, hint string) {
		t.Helper()
		code, out := discard(sv)
		if code != http.StatusConflict || !strings.Contains(fmt.Sprint(out["error"]), "restore isn't finished") || !strings.HasPrefix(fmt.Sprint(out["hint"]), hint) {
			t.Fatalf("%s: discarding %s's world copy: %d %v", what, sv.name(), code, out)
		}
		if !dirExists(filepath.Join(sv.dir(), name)) {
			t.Fatalf("%s: %s's world copy is gone", what, sv.name())
		}
	}
	discarded := func(what string, sv *server) {
		t.Helper()
		if code, out := discard(sv); code != http.StatusNoContent || dirExists(filepath.Join(sv.dir(), name)) {
			t.Fatalf("%s: discarding %s's world copy: %d %v", what, sv.name(), code, out)
		}
	}

	dir := e.a.stageDir("0123456789abcdef")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sc, _ := s.serverConfig()
	j := &swapJournal{ServerID: s.id, OpID: "0000000000000000", Actor: "admin", Aside: name, Failed: "data.failed-restore-" + stamp, HadLive: true,
		StartedAt: old, Previous: sc, Restored: *sc, State: swapReverting}
	if err := writeSwapJournal(dir, j); err != nil {
		t.Fatal(err)
	}
	refused("with its swap journal", s, "Playkeeper finishes it once the server is stopped, then try again.")
	discarded("with another server's swap journal", other)

	journal := filepath.Join(dir, swapJournalFile)
	if err := os.WriteFile(journal, []byte(`{"serverId":"`+s.id+`","opId":`), 0o600); err != nil {
		t.Fatal(err)
	}
	unreadable := "Playkeeper couldn't finish it: the swap journal in " + dir + " can't be read ("
	refused("with a swap journal that can't be read", s, unreadable)
	refused("with a swap journal that can't be read", other, unreadable)

	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	discarded("without a swap journal", s)
}
